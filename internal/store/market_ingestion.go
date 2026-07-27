package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const marketChainCursorColumns = `
	id, market_asset_deployment_id,
	chain_key, chain_id, cursor_key, next_block_number,
	safe_block_number, last_block_hash, status, last_error,
	last_scanned_at, created_at, updated_at
`

const noditWebhookSubscriptionColumns = `
	id, provider, external_id, chain_key, chain_id, event_category,
	callback_url_hash, secret_reference, status, configuration,
	last_synced_at, last_checked_at, last_error, created_at, updated_at
`

const webhookInboxColumns = `
	id, webhook_subscription_id, provider, chain_key, chain_id,
	delivery_id, dedupe_key,
	signature_valid, headers, raw_body, payload, processing_status,
	attempts, next_attempt_at, locked_at, processed_at, last_error,
	received_at, created_at
`

type AdvanceMarketChainCursorParams struct {
	ChainKey        string
	ChainID         int64
	CursorKey       string
	NextBlockNumber int64
	SafeBlockNumber int64
	LastBlockHash   *string
	Status          string
	LastError       string
	ScannedAt       time.Time
}

type UpsertNoditWebhookSubscriptionParams struct {
	Provider        string
	ExternalID      *string
	ChainKey        string
	ChainID         int64
	EventCategory   string
	CallbackURLHash string
	SecretReference string
	Status          string
	Configuration   json.RawMessage
	LastSyncedAt    *time.Time
	LastCheckedAt   *time.Time
	LastError       string
}

type CreateWebhookInboxParams struct {
	WebhookSubscriptionID *int64
	Provider              string
	ChainKey              string
	ChainID               int64
	DeliveryID            string
	DedupeKey             string
	SignatureValid        bool
	Headers               json.RawMessage
	RawBody               []byte
	Payload               json.RawMessage
	ReceivedAt            time.Time
}

func (s *Store) AdvanceMarketChainCursor(
	ctx context.Context,
	params AdvanceMarketChainCursorParams,
) (MarketChainCursor, error) {
	params.ChainKey = strings.ToLower(strings.TrimSpace(params.ChainKey))
	params.CursorKey = strings.TrimSpace(params.CursorKey)
	params.Status = normalizeCursorStatus(params.Status)
	if params.ChainKey == "" || params.ChainID <= 0 || params.CursorKey == "" ||
		params.NextBlockNumber < 0 || params.SafeBlockNumber < 0 {
		return MarketChainCursor{}, ErrInvalidMarketCursor
	}
	if params.LastBlockHash != nil {
		normalized := strings.ToLower(strings.TrimSpace(*params.LastBlockHash))
		if len(normalized) != 66 || !strings.HasPrefix(normalized, "0x") {
			return MarketChainCursor{}, ErrInvalidMarketCursor
		}
		params.LastBlockHash = &normalized
	}
	scannedAt := params.ScannedAt.UTC()
	if scannedAt.IsZero() {
		scannedAt = time.Now().UTC()
	}
	cursor, err := collectOne[MarketChainCursor](ctx, s.db, `
		INSERT INTO market_chain_cursors (
			chain_key, chain_id, cursor_key, next_block_number,
			safe_block_number, last_block_hash, status, last_error,
			last_scanned_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (chain_id, cursor_key) DO UPDATE
		SET chain_key = EXCLUDED.chain_key,
		    next_block_number = GREATEST(
		        market_chain_cursors.next_block_number,
		        EXCLUDED.next_block_number
		    ),
		    safe_block_number = GREATEST(
		        market_chain_cursors.safe_block_number,
		        EXCLUDED.safe_block_number
		    ),
		    last_block_hash = CASE
		        WHEN EXCLUDED.next_block_number >= market_chain_cursors.next_block_number
		        THEN EXCLUDED.last_block_hash
		        ELSE market_chain_cursors.last_block_hash
		    END,
		    status = EXCLUDED.status,
		    last_error = EXCLUDED.last_error,
		    last_scanned_at = EXCLUDED.last_scanned_at,
		    updated_at = NOW()
		RETURNING `+marketChainCursorColumns,
		params.ChainKey,
		params.ChainID,
		params.CursorKey,
		params.NextBlockNumber,
		params.SafeBlockNumber,
		params.LastBlockHash,
		params.Status,
		truncate(params.LastError, 2000),
		scannedAt,
	)
	if err != nil {
		return MarketChainCursor{}, fmt.Errorf("advance market chain cursor: %w", err)
	}
	return cursor, nil
}

func (s *Store) RewindMarketChainCursor(
	ctx context.Context,
	chainID int64,
	cursorKey string,
	nextBlockNumber int64,
	lastBlockHash *string,
	reason string,
) (MarketChainCursor, error) {
	if chainID <= 0 || nextBlockNumber < 0 || strings.TrimSpace(cursorKey) == "" {
		return MarketChainCursor{}, ErrInvalidMarketCursor
	}
	cursor, err := collectOne[MarketChainCursor](ctx, s.db, `
		UPDATE market_chain_cursors
		SET next_block_number = $1,
		    safe_block_number = LEAST(safe_block_number, $1),
		    last_block_hash = $2,
		    status = 'active',
		    last_error = $3,
		    updated_at = NOW()
		WHERE chain_id = $4 AND cursor_key = $5
		RETURNING `+marketChainCursorColumns,
		nextBlockNumber,
		lastBlockHash,
		truncate(reason, 2000),
		chainID,
		strings.TrimSpace(cursorKey),
	)
	if isNoRows(err) {
		return MarketChainCursor{}, ErrNotFound
	}
	if err != nil {
		return MarketChainCursor{}, fmt.Errorf("rewind market chain cursor: %w", err)
	}
	return cursor, nil
}

func (s *Store) GetMarketChainCursor(
	ctx context.Context,
	chainID int64,
	cursorKey string,
) (*MarketChainCursor, error) {
	cursor, err := collectOptional[MarketChainCursor](ctx, s.db, `
		SELECT `+marketChainCursorColumns+`
		FROM market_chain_cursors
		WHERE chain_id = $1 AND cursor_key = $2
	`, chainID, strings.TrimSpace(cursorKey))
	if err != nil {
		return nil, fmt.Errorf("get market chain cursor: %w", err)
	}
	return cursor, nil
}

func (s *Store) UpsertNoditWebhookSubscription(
	ctx context.Context,
	params UpsertNoditWebhookSubscriptionParams,
) (NoditWebhookSubscription, error) {
	params.Provider = strings.ToLower(strings.TrimSpace(params.Provider))
	if params.Provider == "" {
		params.Provider = "nodit"
	}
	params.ChainKey = strings.ToLower(strings.TrimSpace(params.ChainKey))
	params.EventCategory = strings.ToLower(strings.TrimSpace(params.EventCategory))
	params.Status = normalizeWebhookStatus(params.Status)
	if params.ChainKey == "" || params.ChainID <= 0 || params.EventCategory == "" {
		return NoditWebhookSubscription{}, ErrInvalidWebhookSubscription
	}
	value, err := collectOne[NoditWebhookSubscription](ctx, s.db, `
		INSERT INTO nodit_webhook_subscriptions (
			provider, external_id, chain_key, chain_id, event_category,
			callback_url_hash, secret_reference, status, configuration,
			last_synced_at, last_checked_at, last_error
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (provider, chain_id, event_category) DO UPDATE
		SET external_id = EXCLUDED.external_id,
		    chain_key = EXCLUDED.chain_key,
		    callback_url_hash = EXCLUDED.callback_url_hash,
		    secret_reference = EXCLUDED.secret_reference,
		    status = EXCLUDED.status,
		    configuration = EXCLUDED.configuration,
		    last_synced_at = EXCLUDED.last_synced_at,
		    last_checked_at = EXCLUDED.last_checked_at,
		    last_error = EXCLUDED.last_error,
		    updated_at = NOW()
		RETURNING `+noditWebhookSubscriptionColumns,
		params.Provider,
		params.ExternalID,
		params.ChainKey,
		params.ChainID,
		params.EventCategory,
		params.CallbackURLHash,
		params.SecretReference,
		params.Status,
		normalizedJSON(params.Configuration),
		params.LastSyncedAt,
		params.LastCheckedAt,
		truncate(params.LastError, 2000),
	)
	if err != nil {
		return NoditWebhookSubscription{}, fmt.Errorf("upsert Nodit webhook subscription: %w", err)
	}
	return value, nil
}

func (s *Store) ListNoditWebhookSubscriptions(
	ctx context.Context,
	chainID *int64,
) ([]NoditWebhookSubscription, error) {
	query := `
		SELECT ` + noditWebhookSubscriptionColumns + `
		FROM nodit_webhook_subscriptions
	`
	args := []any{}
	if chainID != nil {
		query += " WHERE chain_id = $1"
		args = append(args, *chainID)
	}
	query += " ORDER BY chain_id, event_category"
	values, err := collectMany[NoditWebhookSubscription](ctx, s.db, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list Nodit webhook subscriptions: %w", err)
	}
	return values, nil
}

func (s *Store) CreateWebhookInboxMessage(
	ctx context.Context,
	params CreateWebhookInboxParams,
) (WebhookInboxMessage, bool, error) {
	params.Provider = strings.ToLower(strings.TrimSpace(params.Provider))
	params.ChainKey = strings.ToLower(strings.TrimSpace(params.ChainKey))
	params.DeliveryID = strings.TrimSpace(params.DeliveryID)
	params.DedupeKey = strings.TrimSpace(params.DedupeKey)
	if params.Provider == "" || params.ChainKey == "" || params.ChainID <= 0 ||
		params.DedupeKey == "" || len(params.RawBody) == 0 {
		return WebhookInboxMessage{}, false, ErrInvalidWebhookDelivery
	}
	receivedAt := params.ReceivedAt.UTC()
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	}
	value, err := collectOne[WebhookInboxMessage](ctx, s.db, `
		INSERT INTO webhook_inbox (
			webhook_subscription_id, provider, chain_key, chain_id,
			delivery_id, dedupe_key,
			signature_valid, headers, raw_body, payload, received_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (provider, dedupe_key) DO NOTHING
		RETURNING `+webhookInboxColumns,
		params.WebhookSubscriptionID,
		params.Provider,
		params.ChainKey,
		params.ChainID,
		params.DeliveryID,
		params.DedupeKey,
		boolInt(params.SignatureValid),
		normalizedJSON(params.Headers),
		params.RawBody,
		nullableJSON(params.Payload),
		receivedAt,
	)
	if err == nil {
		return value, true, nil
	}
	if !isNoRows(err) {
		return WebhookInboxMessage{}, false, fmt.Errorf("create webhook inbox message: %w", err)
	}
	existing, err := collectOne[WebhookInboxMessage](ctx, s.db, `
		SELECT `+webhookInboxColumns+`
		FROM webhook_inbox
		WHERE provider = $1 AND dedupe_key = $2
	`, params.Provider, params.DedupeKey)
	if err != nil {
		return WebhookInboxMessage{}, false, fmt.Errorf("get duplicate webhook inbox message: %w", err)
	}
	// Nodit Easy Resend reuses the original delivery payload. A valid resend of
	// a previously failed/dead delivery must re-arm that same inbox row rather
	// than being discarded by the dedupe key.
	if params.SignatureValid &&
		(existing.ProcessingStatus == "failed" || existing.ProcessingStatus == "dead") {
		replayed, replayErr := collectOne[WebhookInboxMessage](ctx, s.db, `
			UPDATE webhook_inbox
			SET webhook_subscription_id = COALESCE($1, webhook_subscription_id),
			    chain_key = $2,
			    chain_id = $3,
			    delivery_id = $4,
			    signature_valid = 1,
			    headers = $5,
			    raw_body = $6,
			    payload = $7,
			    processing_status = 'pending',
			    attempts = 0,
			    next_attempt_at = NOW(),
			    locked_at = NULL,
			    processed_at = NULL,
			    last_error = '',
			    received_at = $8
			WHERE id = $9 AND processing_status IN ('failed', 'dead')
			RETURNING `+webhookInboxColumns,
			params.WebhookSubscriptionID,
			params.ChainKey,
			params.ChainID,
			params.DeliveryID,
			normalizedJSON(params.Headers),
			params.RawBody,
			nullableJSON(params.Payload),
			receivedAt,
			existing.ID,
		)
		if replayErr == nil {
			return replayed, false, nil
		}
		if !isNoRows(replayErr) {
			return WebhookInboxMessage{}, false, fmt.Errorf(
				"rearm replayed webhook inbox message: %w",
				replayErr,
			)
		}
	}
	return existing, false, nil
}

func (s *Store) ClaimWebhookInboxMessages(
	ctx context.Context,
	chainID int64,
	limit int,
) ([]WebhookInboxMessage, error) {
	if chainID <= 0 {
		return nil, ErrInvalidWebhookDelivery
	}
	limit = clamp(limit, 1, 500)
	values, err := collectMany[WebhookInboxMessage](ctx, s.db, `
		WITH picked AS (
			SELECT id
			FROM webhook_inbox
			WHERE chain_id = $1
			  AND processing_status IN ('pending', 'failed')
			  AND next_attempt_at <= NOW()
			ORDER BY next_attempt_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		)
		UPDATE webhook_inbox wi
		SET processing_status = 'processing',
		    attempts = wi.attempts + 1,
		    locked_at = NOW()
		FROM picked
		WHERE wi.id = picked.id
		RETURNING
			wi.id, wi.webhook_subscription_id, wi.provider,
			wi.chain_key, wi.chain_id, wi.delivery_id,
			wi.dedupe_key, wi.signature_valid, wi.headers, wi.raw_body,
			wi.payload, wi.processing_status, wi.attempts, wi.next_attempt_at,
			wi.locked_at, wi.processed_at, wi.last_error, wi.received_at,
			wi.created_at
	`, chainID, limit)
	if err != nil {
		return nil, fmt.Errorf("claim webhook inbox messages: %w", err)
	}
	return values, nil
}

func (s *Store) MarkWebhookInboxProcessed(
	ctx context.Context,
	inboxID int64,
) (WebhookInboxMessage, error) {
	value, err := collectOne[WebhookInboxMessage](ctx, s.db, `
		UPDATE webhook_inbox
		SET processing_status = 'processed',
		    processed_at = NOW(),
		    locked_at = NULL,
		    last_error = ''
		WHERE id = $1
		RETURNING `+webhookInboxColumns,
		inboxID,
	)
	if isNoRows(err) {
		return WebhookInboxMessage{}, ErrNotFound
	}
	if err != nil {
		return WebhookInboxMessage{}, fmt.Errorf("mark webhook inbox processed: %w", err)
	}
	return value, nil
}

func (s *Store) MarkWebhookInboxFailed(
	ctx context.Context,
	inboxID int64,
	processingError string,
	retryAt time.Time,
	dead bool,
) (WebhookInboxMessage, error) {
	status := "failed"
	if dead {
		status = "dead"
	}
	retryAt = retryAt.UTC()
	if retryAt.IsZero() {
		retryAt = time.Now().UTC()
	}
	value, err := collectOne[WebhookInboxMessage](ctx, s.db, `
		UPDATE webhook_inbox
		SET processing_status = $1,
		    next_attempt_at = $2,
		    locked_at = NULL,
		    last_error = $3
		WHERE id = $4
		RETURNING `+webhookInboxColumns,
		status,
		retryAt,
		truncate(processingError, 2000),
		inboxID,
	)
	if isNoRows(err) {
		return WebhookInboxMessage{}, ErrNotFound
	}
	if err != nil {
		return WebhookInboxMessage{}, fmt.Errorf("mark webhook inbox failed: %w", err)
	}
	return value, nil
}

func normalizeCursorStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "paused", "error":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "active"
	}
}

func normalizeWebhookStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "active", "paused", "error", "deleted":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "pending"
	}
}

func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 || !json.Valid(value) {
		return nil
	}
	return value
}
