package marketcollector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const MaxWebhookBodyBytes = 1 << 20

type WebhookAcceptance struct {
	InboxID   int64 `json:"inbox_id"`
	Created   bool  `json:"created"`
	Duplicate bool  `json:"duplicate"`
}

func (service *Service) AcceptWebhook(
	ctx context.Context,
	category string,
	headers map[string][]string,
	rawBody []byte,
) (WebhookAcceptance, error) {
	if service == nil || !service.settings.Enabled {
		return WebhookAcceptance{}, ErrCollectorDisabled
	}
	if len(rawBody) == 0 {
		return WebhookAcceptance{}, ErrInvalidWebhook
	}
	if len(rawBody) > MaxWebhookBodyBytes {
		return WebhookAcceptance{}, ErrWebhookBodyTooLarge
	}
	category = normalizeCategory(category)
	if category == "" {
		return WebhookAcceptance{}, ErrInvalidWebhook
	}

	now := service.now().UTC()
	normalizedHeaders := normalizeHeaders(headers)
	signature := firstHeader(normalizedHeaders, chain.NoditWebhookSignatureHeader)
	signingKey := service.settings.webhookSigningKey(category)
	signatureValid := chain.VerifyWebhookSignature(
		rawBody,
		signature,
		signingKey,
	)
	var acceptanceError error
	if strings.TrimSpace(signingKey) == "" {
		signatureValid = false
		acceptanceError = ErrWebhookUnavailable
	} else if !signatureValid {
		acceptanceError = ErrInvalidSignature
	}
	if timestamp, present, err := webhookTimestamp(normalizedHeaders); err != nil {
		signatureValid = false
		acceptanceError = ErrExpiredWebhook
	} else if present && now.Sub(timestamp).Abs() > service.settings.WebhookMaxAge {
		signatureValid = false
		acceptanceError = ErrExpiredWebhook
	}

	var payload json.RawMessage
	if json.Valid(rawBody) {
		payload = append(json.RawMessage(nil), rawBody...)
	} else if acceptanceError == nil {
		signatureValid = false
		acceptanceError = ErrInvalidWebhook
	}
	encodedHeaders, err := json.Marshal(normalizedHeaders)
	if err != nil {
		return WebhookAcceptance{}, fmt.Errorf("encode market webhook headers: %w", err)
	}
	deliveryID := firstHeader(
		normalizedHeaders,
		"x-nodit-delivery-id",
		"x-webhook-id",
		"x-request-id",
		"x-event-id",
	)
	dedupe := webhookDedupeKey(
		service.settings.ChainKey,
		category,
		deliveryID,
		rawBody,
	)
	subscription, err := service.repository.GetNoditWebhookSubscriptionByCategory(
		ctx,
		"nodit",
		service.settings.ChainID,
		category,
	)
	if err != nil {
		return WebhookAcceptance{}, err
	}
	var subscriptionID *int64
	if subscription != nil {
		subscriptionID = &subscription.ID
	}
	message, created, err := service.repository.CreateWebhookInboxMessage(
		ctx,
		store.CreateWebhookInboxParams{
			WebhookSubscriptionID: subscriptionID,
			Provider:              "nodit",
			ChainKey:              service.settings.ChainKey,
			ChainID:               service.settings.ChainID,
			DeliveryID:            deliveryID,
			DedupeKey:             dedupe,
			SignatureValid:        signatureValid,
			Headers:               encodedHeaders,
			RawBody:               append([]byte(nil), rawBody...),
			Payload:               payload,
			ReceivedAt:            now,
		},
	)
	if err != nil {
		return WebhookAcceptance{}, err
	}
	// Nodit webhook bandwidth is billed at 0.03 CU/byte. Store milli-CU
	// internally so small messages are not rounded away.
	service.estimatedCU.Add(int64(len(rawBody)) * 30)
	result := WebhookAcceptance{
		InboxID:   message.ID,
		Created:   created,
		Duplicate: !created,
	}
	if acceptanceError != nil {
		return result, acceptanceError
	}
	return result, nil
}

func (service *Service) ProcessInbox(ctx context.Context) error {
	return service.withTaskLock(ctx, "webhook-inbox", func(ctx context.Context) error {
		now := service.now().UTC()
		if _, err := service.repository.RecoverStaleWebhookInbox(
			ctx,
			service.settings.ChainID,
			now.Add(-service.settings.WebhookLease),
			service.settings.WebhookMaxAttempts,
		); err != nil {
			return err
		}
		messages, err := service.repository.ClaimWebhookInboxMessages(
			ctx,
			service.settings.ChainID,
			service.settings.InboxBatchSize,
		)
		if err != nil {
			return err
		}
		for _, message := range messages {
			if err := service.processInboxMessage(ctx, message); err != nil {
				dead := message.Attempts >= int32(service.settings.WebhookMaxAttempts) ||
					errors.Is(err, ErrInvalidWebhook) ||
					errors.Is(err, ErrInvalidSignature) ||
					errors.Is(err, ErrExpiredWebhook)
				retryAt := now.Add(webhookRetryDelay(message.Attempts, message.ID))
				if _, markErr := service.repository.MarkWebhookInboxFailed(
					ctx,
					message.ID,
					err.Error(),
					retryAt,
					dead,
				); markErr != nil {
					return fmt.Errorf("mark webhook inbox %d failed: %w", message.ID, markErr)
				}
				continue
			}
			if _, err := service.repository.MarkWebhookInboxProcessed(ctx, message.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

func (service *Service) processInboxMessage(
	ctx context.Context,
	message store.WebhookInboxMessage,
) error {
	if message.ChainID != 0 && message.ChainID != service.settings.ChainID {
		return fmt.Errorf(
			"%w: inbox chain %d does not match worker chain %d",
			ErrInvalidWebhook,
			message.ChainID,
			service.settings.ChainID,
		)
	}
	if message.SignatureValid != 1 {
		return ErrInvalidSignature
	}
	if len(message.Payload) == 0 || !json.Valid(message.Payload) {
		return ErrInvalidWebhook
	}
	transactionHashes, err := extractTransactionHashes(message.Payload)
	if err != nil {
		return err
	}
	if len(transactionHashes) == 0 {
		return fmt.Errorf("%w: payload contains no transaction hash", ErrInvalidWebhook)
	}
	parserContext, err := service.loadParserContext(ctx)
	if err != nil {
		return err
	}
	for _, transactionHash := range transactionHashes {
		if err := service.hydrateParseAndPersist(
			ctx,
			transactionHash,
			parserContext,
			"nodit_webhook",
			false,
		); err != nil {
			return err
		}
	}
	return nil
}

func extractTransactionHashes(payload json.RawMessage) ([]string, error) {
	var decoded any
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("%w: decode payload: %v", ErrInvalidWebhook, err)
	}
	seen := make(map[string]struct{})
	var visit func(any)
	visit = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				normalized := strings.ToLower(strings.ReplaceAll(key, "_", ""))
				if normalized == "transactionhash" || normalized == "txhash" {
					if hash, ok := child.(string); ok {
						hash = strings.ToLower(strings.TrimSpace(hash))
						if _, err := chain.ValidateTransactionHash(hash); err == nil {
							seen[hash] = struct{}{}
						}
					}
				}
				visit(child)
			}
		case []any:
			for _, child := range typed {
				visit(child)
			}
		}
	}
	visit(decoded)
	result := make([]string, 0, len(seen))
	for hash := range seen {
		result = append(result, hash)
	}
	sortStrings(result)
	return result, nil
}

func normalizeHeaders(headers map[string][]string) map[string][]string {
	result := make(map[string][]string)
	for key, values := range headers {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		// Persist delivery diagnostics, not authorization or cookies.
		if key != "content-type" && key != "user-agent" &&
			!strings.HasPrefix(key, "x-") {
			continue
		}
		copied := make([]string, 0, len(values))
		for _, value := range values {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				copied = append(copied, trimmed)
			}
		}
		if len(copied) > 0 {
			result[key] = copied
		}
	}
	return result
}

func firstHeader(headers map[string][]string, names ...string) string {
	for _, name := range names {
		values := headers[strings.ToLower(name)]
		if len(values) > 0 {
			return strings.TrimSpace(values[0])
		}
	}
	return ""
}

func webhookTimestamp(headers map[string][]string) (time.Time, bool, error) {
	value := firstHeader(
		headers,
		"x-nodit-timestamp",
		"x-webhook-timestamp",
		"x-timestamp",
	)
	if value == "" {
		// Classic Nodit Webhook currently authenticates the raw payload but does
		// not guarantee a timestamp header. Body/delivery dedupe still prevents
		// repeated processing.
		return time.Time{}, false, nil
	}
	if unix, err := strconv.ParseInt(value, 10, 64); err == nil {
		if unix > 10_000_000_000 {
			unix /= 1000
		}
		return time.Unix(unix, 0).UTC(), true, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, true, err
	}
	return parsed.UTC(), true, nil
}

func webhookDedupeKey(chainKey, category, deliveryID string, rawBody []byte) string {
	hasher := sha256.New()
	// Preserve the historical BNB key so a delivery retried across the
	// multi-chain rollout is not processed twice. Other chains include their
	// key, preventing identical provider delivery IDs from colliding.
	if normalizedChain := chain.NormalizeChainKey(chainKey, ""); normalizedChain != "bsc" {
		_, _ = hasher.Write([]byte(normalizedChain))
		_, _ = hasher.Write([]byte{0})
	}
	_, _ = hasher.Write([]byte(category))
	_, _ = hasher.Write([]byte{0})
	if deliveryID != "" {
		_, _ = hasher.Write([]byte(deliveryID))
	} else {
		_, _ = hasher.Write(rawBody)
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func webhookRetryDelay(attempts int32, inboxID int64) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	exponent := attempts - 1
	if exponent > 8 {
		exponent = 8
	}
	delay := time.Second * time.Duration(1<<exponent)
	jitter := time.Duration(inboxID%1000) * time.Millisecond
	if delay+jitter > 15*time.Minute {
		return 15 * time.Minute
	}
	return delay + jitter
}

func normalizeCategory(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 80 {
		return ""
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') &&
			char != '_' && char != '-' {
			return ""
		}
	}
	return value
}

func normalizeWebhookKeyScope(value string) string {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(value)), ":")
	switch len(parts) {
	case 1:
		return normalizeCategory(parts[0])
	case 2:
		profile, err := chain.ChainProfile(parts[0], "")
		category := normalizeCategory(parts[1])
		if err != nil || category == "" {
			return ""
		}
		return profile.Key + ":" + category
	default:
		return ""
	}
}

func sortStrings(values []string) {
	for index := 1; index < len(values); index++ {
		for current := index; current > 0 && values[current] < values[current-1]; current-- {
			values[current], values[current-1] = values[current-1], values[current]
		}
	}
}
