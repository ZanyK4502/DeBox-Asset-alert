package marketcollector

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const (
	webhookRecoveredKey = "nodit:console-key:v1"
	webhookCreateIntent = "nodit:create-pending:v1"
	webhookSealedKey    = "nodit:aes-gcm:v1:"
)

func (s *Service) webhookKeyCipher() (cipher.AEAD, error) {
	key, err := base64.StdEncoding.DecodeString(s.settings.WebhookWrappingKey)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("%w: NODIT_WEBHOOK_WRAPPING_KEY requires 32 base64-encoded bytes", ErrWebhookUnavailable)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrWebhookUnavailable
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func (s *Service) webhookKeyContext(id, category string) []byte {
	return []byte("nodit\x00" + s.settings.ChainKey + "\x00" + category + "\x00" + id)
}

func (s *Service) subscriptionSigningKey(sub *store.NoditWebhookSubscription, category string) (string, error) {
	if sub != nil && sub.SecretReference == webhookRecoveredKey && sub.ExternalID != nil {
		// Explicitly bind a Console-recovered key to its remote ID. A replacement
		// subscription gets a different key even on the same chain and category.
		key, matches := strings.CutPrefix(s.settings.webhookSigningKey(category), *sub.ExternalID+":")
		if !matches || key == "" {
			return "", fmt.Errorf("%w: configure %s:%s as subscriptionId:signingKey for Nodit subscription %s", ErrWebhookUnavailable, s.settings.ChainKey, category, *sub.ExternalID)
		}
		return key, nil
	}
	if sub == nil || !strings.HasPrefix(sub.SecretReference, "nodit:") {
		return s.settings.webhookSigningKey(category), nil
	}
	if !strings.HasPrefix(sub.SecretReference, webhookSealedKey) || sub.ExternalID == nil {
		return "", ErrWebhookUnavailable
	}
	aead, err := s.webhookKeyCipher()
	if err != nil {
		return "", err
	}
	sealed, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(sub.SecretReference, webhookSealedKey))
	if err != nil || len(sealed) < aead.Overhead() {
		return "", ErrWebhookUnavailable
	}
	key, err := aead.Open(nil, nil, sealed, s.webhookKeyContext(*sub.ExternalID, category))
	if err != nil || len(key) == 0 {
		return "", ErrWebhookUnavailable
	}
	return string(key), nil
}

func (s *Service) syncChainTransfer(ctx context.Context, local *store.NoditWebhookSubscription, tokens []string) error {
	idle := len(tokens) == 0
	if idle && (local == nil || local.Status == "paused") {
		return nil
	}
	callback := strings.TrimRight(s.settings.PublicAppURL, "/") + "/api/market/webhook/" + s.settings.ChainKey + "/transfer"
	target, err := url.Parse(callback)
	if err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" {
		return fmt.Errorf("managed webhook requires a public HTTPS app URL")
	}
	list, err := s.chain.ListWebhooks(ctx, s.settings.ChainKey, s.settings.ChainFallback, chain.WebhookListOptions{RPP: 100})
	if err != nil {
		return err
	}
	// Account quotas are below a full page today. If that changes, absence
	// in the first page cannot justify creating a second subscription.
	if list.Total != int64(len(list.Items)) {
		return fmt.Errorf("incomplete Nodit webhook inventory for %s", s.settings.ChainKey)
	}
	externalID := ""
	if local != nil && local.ExternalID != nil {
		externalID = *local.ExternalID
	}
	description := "DeBox Asset Alert " + s.settings.ChainKey + " transfer"
	var match *chain.WebhookSubscription
	for i := range list.Items {
		item := &list.Items[i]
		knownID := externalID != "" && item.SubscriptionID == externalID
		if !knownID && item.Notification.WebhookURL != callback {
			continue
		}
		if match != nil || item.SubscriptionID == "" || item.EventType != "TOKEN_TRANSFER" || !item.IsInstant ||
			(!knownID && item.Description != description) {
			return fmt.Errorf("conflicting Nodit subscription at %s transfer callback; retained for review", s.settings.ChainKey)
		}
		match = item
	}
	if match == nil {
		if local != nil && local.SecretReference == webhookCreateIntent {
			return fmt.Errorf("Nodit transfer creation outcome unknown; verify provider state before retry")
		}
		if idle {
			return nil
		}
		if externalID != "" && local.LastSyncedAt != nil && s.now().Sub(*local.LastSyncedAt) < s.settings.DiscoveryInterval {
			return fmt.Errorf("recently synchronized Nodit transfer is not yet visible")
		}
		return s.createChainTransfer(ctx, callback, description, tokens)
	}

	params := store.UpsertNoditWebhookSubscriptionParams{
		Provider: "nodit", ChainKey: s.settings.ChainKey, ChainID: s.settings.ChainID,
		EventCategory: transferWebhookCategory, ExternalID: &match.SubscriptionID,
		CallbackURLHash: callbackURLHash(callback), Status: "active",
	}
	if local != nil && externalID == match.SubscriptionID {
		params.SecretReference = local.SecretReference
		params.Configuration = local.Configuration
	}
	if idle {
		params.Status = "paused"
		if match.IsActive {
			active := false
			if err := s.chain.UpdateWebhook(ctx, s.settings.ChainKey, s.settings.ChainFallback, match.SubscriptionID, chain.WebhookUpdateRequest{IsActive: &active}); err != nil {
				return err
			}
		}
		// A late response may have left an orphan with no key. Preserve the
		// recovery marker until the operator supplies its signing key.
		if local != nil && local.SecretReference == webhookCreateIntent {
			params.SecretReference = webhookCreateIntent
		}
		return s.saveTransferRegistration(ctx, params)
	}

	// The signing key is available only in the creation response or in the
	// operator's Console. An exact callback/description recovers identity;
	// a missing key must come from that chain's scoped configuration.
	if externalID != match.SubscriptionID || params.SecretReference == webhookCreateIntent {
		params.SecretReference = webhookRecoveredKey
	}
	key, err := s.subscriptionSigningKey(&store.NoditWebhookSubscription{ExternalID: params.ExternalID, SecretReference: params.SecretReference}, transferWebhookCategory)
	if err != nil {
		return err
	}
	if key == "" {
		return ErrWebhookUnavailable
	}
	condition := tokenTransferCondition(tokens)
	params.Configuration, err = webhookConfiguration("TOKEN_TRANSFER", condition)
	if err != nil {
		return err
	}
	update := chain.WebhookUpdateRequest{}
	if !match.IsActive {
		active := true
		update.IsActive = &active
	}
	if match.Notification.WebhookURL != callback {
		update.Notification = &chain.WebhookNotification{WebhookURL: callback}
	}
	if expected, drift := webhookConditionDrift(params.Configuration, match.Condition); drift {
		update.Condition = expected
	}
	changed := update.IsActive != nil || update.Condition != nil || update.Notification != nil
	if changed {
		if err := s.chain.UpdateWebhook(ctx, s.settings.ChainKey, s.settings.ChainFallback, match.SubscriptionID, update); err != nil {
			return err
		}
	}
	if !changed && local != nil && externalID == match.SubscriptionID && local.Status == "active" && local.CallbackURLHash == params.CallbackURLHash && local.SecretReference == params.SecretReference && jsonEqual(local.Configuration, params.Configuration) {
		return nil
	}
	return s.saveTransferRegistration(ctx, params)
}

func (s *Service) saveTransferRegistration(ctx context.Context, p store.UpsertNoditWebhookSubscriptionParams) error {
	now := s.now().UTC()
	p.LastSyncedAt, p.LastCheckedAt = &now, &now
	_, err := s.repository.UpsertNoditWebhookSubscription(ctx, p)
	return err
}

func (s *Service) createChainTransfer(ctx context.Context, callback, description string, tokens []string) error {
	aead, err := s.webhookKeyCipher()
	if err != nil {
		return err
	}
	condition := tokenTransferCondition(tokens)
	cfg, err := webhookConfiguration("TOKEN_TRANSFER", condition)
	if err != nil {
		return err
	}
	p := store.UpsertNoditWebhookSubscriptionParams{
		Provider: "nodit", ChainKey: s.settings.ChainKey, ChainID: s.settings.ChainID, EventCategory: transferWebhookCategory,
		CallbackURLHash: callbackURLHash(callback), Status: "pending", SecretReference: webhookCreateIntent, Configuration: cfg,
	}
	if err := s.saveTransferRegistration(ctx, p); err != nil {
		return err
	}
	created, err := s.chain.CreateWebhook(ctx, s.settings.ChainKey, s.settings.ChainFallback, chain.WebhookCreateRequest{
		EventType: "TOKEN_TRANSFER", IsInstant: true, Description: description, Notification: chain.WebhookNotification{WebhookURL: callback}, Condition: condition,
	})
	if err != nil {
		var responseError *chain.WebhookHTTPError
		if errors.As(err, &responseError) && responseError.DefinitelyRejected() {
			p.Status = "error"
			p.SecretReference = ""
			p.LastError = responseError.Error()
			if saveErr := s.saveTransferRegistration(ctx, p); saveErr != nil {
				return saveErr
			}
		}
		return err
	}
	if strings.TrimSpace(created.SubscriptionID) == "" || strings.TrimSpace(created.SigningKey) == "" {
		return fmt.Errorf("incomplete Nodit transfer creation response; reconcile before retry")
	}
	sealed := aead.Seal(nil, nil, []byte(created.SigningKey), s.webhookKeyContext(created.SubscriptionID, transferWebhookCategory))
	p.SecretReference = webhookSealedKey + base64.RawStdEncoding.EncodeToString(sealed)
	p.ExternalID = &created.SubscriptionID
	p.Status = "active"
	if (created.EventType != "" && created.EventType != "TOKEN_TRANSFER") ||
		(created.Notification.WebhookURL != "" && created.Notification.WebhookURL != callback) ||
		(created.InstantReported && !created.IsInstant) {
		// Keep the one-time key even if the provider echoed conflicting
		// metadata. The next sync must reconcile the actual subscription.
		p.Status = "error"
		p.LastError = "Nodit transfer creation metadata conflicts with the request"
	}
	if err := s.saveTransferRegistration(ctx, p); err != nil {
		return err
	}
	if p.Status == "error" {
		return errors.New(p.LastError)
	}
	return nil
}
