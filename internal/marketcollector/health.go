package marketcollector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func (service *Service) CheckHealth(ctx context.Context) error {
	return service.withTaskLock(ctx, "health", func(ctx context.Context) error {
		var firstError error
		started := time.Now()
		_, noditErr := service.chain.LatestBlockNumber(
			ctx,
			service.settings.ChainKey,
			service.settings.ChainFallback,
		)
		if err := service.recordHealth(
			ctx,
			"nodit",
			"rpc",
			started,
			noditErr,
			nil,
		); err != nil {
			firstError = err
		} else if noditErr != nil {
			firstError = noditErr
		}
		if err := service.checkWebhookStatus(ctx); err != nil && firstError == nil {
			firstError = err
		}
		if err := service.flushEstimatedCU(ctx); err != nil && firstError == nil {
			firstError = err
		}
		return firstError
	})
}

func (service *Service) checkWebhookStatus(ctx context.Context) error {
	chainID := service.settings.ChainID
	subscriptions, err := service.repository.ListNoditWebhookSubscriptions(ctx, &chainID)
	if err != nil {
		return err
	}
	if len(subscriptions) == 0 {
		_, recordErr := service.repository.RecordMarketProviderHealth(
			ctx,
			store.RecordMarketProviderHealthParams{
				Provider:  "nodit",
				Component: "webhook_status",
				ChainKey:  service.settings.ChainKey,
				ChainID:   service.settings.ChainID,
				Success:   false,
				Error:     "no market webhook subscriptions are registered locally",
				CheckedAt: service.now().UTC(),
			},
		)
		return recordErr
	}
	started := time.Now()
	remote, listErr := service.chain.ListWebhooks(
		ctx,
		service.settings.ChainKey,
		service.settings.ChainFallback,
		chain.WebhookListOptions{RPP: 100},
	)
	if listErr != nil {
		_ = service.recordHealth(
			ctx,
			"nodit",
			"webhook_status",
			started,
			listErr,
			nil,
		)
		return listErr
	}
	remoteByID := make(map[string]chain.WebhookSubscription, len(remote.Items))
	for _, item := range remote.Items {
		remoteByID[item.SubscriptionID] = item
	}
	var driftCount int
	for _, subscription := range subscriptions {
		status := "pending"
		checkError := ""
		if subscription.ExternalID == nil || strings.TrimSpace(*subscription.ExternalID) == "" {
			checkError = "webhook has not been provisioned"
			driftCount++
		} else if current, exists := remoteByID[*subscription.ExternalID]; !exists {
			status = "error"
			checkError = "webhook is missing from Nodit"
			driftCount++
		} else {
			inactive := !current.IsActive
			callbackDrift := subscription.CallbackURLHash != "" &&
				subscription.CallbackURLHash != callbackURLHash(current.Notification.WebhookURL)
			condition, conditionDrift := webhookConditionDrift(
				subscription.Configuration,
				current.Condition,
			)
			if (inactive || callbackDrift || conditionDrift) &&
				service.settings.WebhookAutoRepair {
				repairErr := service.repairWebhook(
					ctx,
					subscription,
					inactive,
					callbackDrift,
					condition,
				)
				if repairErr == nil {
					inactive = false
					callbackDrift = false
					conditionDrift = false
				} else {
					checkError = "webhook repair failed: " + repairErr.Error()
				}
			}
			switch {
			case inactive:
				status = "paused"
				if checkError == "" {
					checkError = "webhook is inactive in Nodit"
				}
				driftCount++
			case callbackDrift:
				status = "error"
				if checkError == "" {
					checkError = "webhook callback URL drift detected"
				}
				driftCount++
			case conditionDrift:
				status = "error"
				if checkError == "" {
					checkError = "webhook condition drift detected"
				}
				driftCount++
			default:
				status = "active"
			}
		}
		if _, err := service.repository.UpdateNoditWebhookSubscriptionCheck(
			ctx,
			subscription.ID,
			status,
			checkError,
			service.now().UTC(),
		); err != nil {
			return err
		}
	}
	metadata, _ := json.Marshal(map[string]any{
		"configured": len(subscriptions),
		"remote":     len(remote.Items),
		"drifted":    driftCount,
	})
	var statusErr error
	if driftCount > 0 {
		statusErr = fmt.Errorf("%d market webhook subscription(s) require reconciliation", driftCount)
	}
	_ = service.recordHealth(
		ctx,
		"nodit",
		"webhook_status",
		started,
		statusErr,
		metadata,
	)
	return nil
}

func (service *Service) repairWebhook(
	ctx context.Context,
	subscription store.NoditWebhookSubscription,
	inactive bool,
	callbackDrift bool,
	condition map[string]any,
) error {
	if subscription.ExternalID == nil {
		return fmt.Errorf("webhook external ID is unavailable")
	}
	update := chain.WebhookUpdateRequest{}
	if inactive {
		active := true
		update.IsActive = &active
	}
	if callbackDrift {
		if strings.TrimSpace(service.settings.PublicAppURL) == "" {
			return fmt.Errorf("public app URL is unavailable")
		}
		update.Notification = &chain.WebhookNotification{
			WebhookURL: strings.TrimRight(service.settings.PublicAppURL, "/") +
				"/api/market/webhook/" + subscription.EventCategory,
		}
	}
	if condition != nil {
		update.Condition = condition
	}
	return service.chain.UpdateWebhook(
		ctx,
		service.settings.ChainKey,
		service.settings.ChainFallback,
		*subscription.ExternalID,
		update,
	)
}

func webhookConditionDrift(
	configuration json.RawMessage,
	current json.RawMessage,
) (map[string]any, bool) {
	if len(configuration) == 0 {
		return nil, false
	}
	var configured struct {
		Condition map[string]any `json:"condition"`
	}
	if json.Unmarshal(configuration, &configured) != nil || configured.Condition == nil {
		return nil, false
	}
	var currentCondition map[string]any
	if json.Unmarshal(current, &currentCondition) != nil {
		return configured.Condition, true
	}
	expectedJSON, _ := json.Marshal(configured.Condition)
	currentJSON, _ := json.Marshal(currentCondition)
	return configured.Condition, string(expectedJSON) != string(currentJSON)
}

func (service *Service) recordHealth(
	ctx context.Context,
	provider string,
	component string,
	started time.Time,
	operationError error,
	metadata json.RawMessage,
) error {
	_, err := service.repository.RecordMarketProviderHealth(
		ctx,
		store.RecordMarketProviderHealthParams{
			Provider:  provider,
			Component: component,
			ChainKey:  service.settings.ChainKey,
			ChainID:   service.settings.ChainID,
			Success:   operationError == nil,
			Latency:   time.Since(started),
			Error:     errorString(operationError),
			Metadata:  metadata,
			CheckedAt: service.now().UTC(),
		},
	)
	return err
}

func (service *Service) flushEstimatedCU(ctx context.Context) error {
	milliCU := service.estimatedCU.Swap(0)
	if milliCU <= 0 {
		return nil
	}
	now := service.now().UTC()
	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0)
	metadata, _ := json.Marshal(map[string]any{
		"scope": "application_nodit_client_and_market_webhooks",
		"basis": "documented Node/Data API method CU plus webhook bytes at 0.03 CU/byte",
	})
	_, err := service.repository.AddMarketProviderUsage(
		ctx,
		store.AddMarketProviderUsageParams{
			Provider:    "nodit",
			Metric:      "estimated_compute_units",
			PeriodStart: periodStart,
			PeriodEnd:   periodEnd,
			DeltaUnits:  milliCUDecimal(milliCU),
			LimitUnits:  strconv.FormatInt(service.settings.MonthlyCULimit, 10),
			Metadata:    metadata,
			CheckedAt:   now,
		},
	)
	if err != nil {
		service.estimatedCU.Add(milliCU)
	}
	return err
}

func (service *Service) Cleanup(ctx context.Context) error {
	return service.withTaskLock(ctx, "cleanup", func(ctx context.Context) error {
		now := service.now().UTC()
		_, err := service.repository.CleanupMarketCollectionData(
			ctx,
			now.Add(-7*24*time.Hour),
			now.Add(-30*24*time.Hour),
			now.Add(-30*24*time.Hour),
		)
		return err
	})
}

func callbackURLHash(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}

func milliCUDecimal(value int64) string {
	whole := value / 1000
	fraction := value % 1000
	if fraction == 0 {
		return strconv.FormatInt(whole, 10)
	}
	result := fmt.Sprintf("%d.%03d", whole, fraction)
	return strings.TrimRight(result, "0")
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
