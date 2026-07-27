package marketcollector

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const transferWebhookCategory = "transfer"

// SyncWebhookSubscriptions keeps the shared TOKEN_TRANSFER subscription aligned
// with every active project token. Nodit's paid plans limit the number of
// Webhooks, so one subscription carries a de-duplicated token list instead of
// allocating a Webhook per user or rule.
func (service *Service) SyncWebhookSubscriptions(ctx context.Context) error {
	if service == nil || !service.settings.Enabled ||
		!service.settings.WebhookAutoRepair {
		return nil
	}
	return service.withTaskLock(ctx, "webhook-sync", func(ctx context.Context) error {
		projects, err := service.repository.ListActiveMarketProjectsForCollection(
			ctx,
			service.settings.ChainID,
			5000,
		)
		if err != nil {
			return err
		}
		tokens := make([]string, 0, len(projects))
		seen := make(map[string]struct{}, len(projects))
		for _, project := range projects {
			token, validateErr := chain.ValidateAddress(project.TokenAddress)
			if validateErr != nil {
				return fmt.Errorf(
					"invalid active market project token %q: %w",
					project.TokenAddress,
					validateErr,
				)
			}
			token = strings.ToLower(token)
			if _, exists := seen[token]; exists {
				continue
			}
			seen[token] = struct{}{}
			tokens = append(tokens, token)
		}
		// Nodit does not accept an empty TOKEN_TRANSFER condition. Retain the
		// existing bootstrap condition until the first active project exists.
		if len(tokens) == 0 {
			return nil
		}
		sort.Strings(tokens)

		subscription, err := service.repository.GetNoditWebhookSubscriptionByCategory(
			ctx,
			"nodit",
			service.settings.ChainID,
			transferWebhookCategory,
		)
		if err != nil {
			return err
		}
		if subscription == nil || subscription.ExternalID == nil ||
			strings.TrimSpace(*subscription.ExternalID) == "" {
			return fmt.Errorf("transfer webhook subscription is not provisioned")
		}
		if service.settings.webhookSigningKey(transferWebhookCategory) == "" {
			return ErrWebhookUnavailable
		}

		condition := tokenTransferCondition(tokens)
		configuration, err := webhookConfiguration("TOKEN_TRANSFER", condition)
		if err != nil {
			return err
		}
		if jsonEqual(subscription.Configuration, configuration) {
			return nil
		}
		if err := service.chain.UpdateWebhook(
			ctx,
			service.settings.ChainKey,
			service.settings.ChainFallback,
			*subscription.ExternalID,
			chain.WebhookUpdateRequest{Condition: condition},
		); err != nil {
			return fmt.Errorf("sync transfer webhook: %w", err)
		}
		now := service.now().UTC()
		_, err = service.repository.UpsertNoditWebhookSubscription(
			ctx,
			store.UpsertNoditWebhookSubscriptionParams{
				Provider:        subscription.Provider,
				ExternalID:      subscription.ExternalID,
				ChainKey:        subscription.ChainKey,
				ChainID:         subscription.ChainID,
				EventCategory:   subscription.EventCategory,
				CallbackURLHash: subscription.CallbackURLHash,
				SecretReference: subscription.SecretReference,
				Status:          "active",
				Configuration:   configuration,
				LastSyncedAt:    &now,
				LastCheckedAt:   subscription.LastCheckedAt,
			},
		)
		return err
	})
}

func tokenTransferCondition(tokens []string) map[string]any {
	values := make([]map[string]string, 0, len(tokens))
	for _, token := range tokens {
		values = append(values, map[string]string{"contractAddress": token})
	}
	return map[string]any{"tokens": values}
}

func webhookConfiguration(eventType string, condition map[string]any) (json.RawMessage, error) {
	value, err := json.Marshal(map[string]any{
		"eventType": eventType,
		"condition": condition,
	})
	if err != nil {
		return nil, fmt.Errorf("encode webhook configuration: %w", err)
	}
	return value, nil
}

func jsonEqual(left, right json.RawMessage) bool {
	var leftValue any
	var rightValue any
	if json.Unmarshal(left, &leftValue) != nil ||
		json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftJSON, _ := json.Marshal(leftValue)
	rightJSON, _ := json.Marshal(rightValue)
	return string(leftJSON) == string(rightJSON)
}
