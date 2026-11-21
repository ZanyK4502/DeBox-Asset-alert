package monitor

import (
	"context"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
)

type walletDeliveryScheduleRepository interface {
	NextWalletNotificationAt(context.Context, time.Time) (*time.Time, error)
}

// A queue wake uses the existing claim, eligibility, lease and send path. It
// never reads a wallet or records another detection/trigger/allowance charge.
func (e *Executor) checkWalletDeliveries(ctx context.Context) CycleResult {
	result := CycleResult{Errors: []RuleResult{}, Results: []RuleResult{}}
	e.drainWalletDeliveries(ctx, &result, 5*time.Second)
	return result
}

func (e *Executor) nextWalletDelivery(ctx context.Context, result *CycleResult, repository walletDeliveryScheduleRepository) bool {
	at, err := repository.NextWalletNotificationAt(ctx, notificationdelivery.NextAdmissionAt(e.deps.Notifications))
	if err != nil {
		result.Errors = append(result.Errors, errorResult(0, err))
		retry := time.Now().Add(5 * time.Second)
		result.NextDeliveryAt = &retry
		return false
	}
	result.NextDeliveryAt = at
	return true
}

func (e *Executor) drainWalletDeliveries(ctx context.Context, result *CycleResult, budget time.Duration) {
	repository, scheduled := e.deps.Repository.(walletDeliveryScheduleRepository)
	if scheduled && notificationdelivery.NextAdmissionAt(e.deps.Notifications).After(time.Now()) {
		// The SQL applies this hint only to sendable rows. An expired lease
		// can still wake the original claim recovery, without another HTTP.
		if !e.nextWalletDelivery(ctx, result, repository) || result.NextDeliveryAt == nil || result.NextDeliveryAt.After(time.Now()) {
			return
		}
	}
	if budget <= 0 || budget > 5*time.Second {
		budget = 5 * time.Second
	}
	deliveryContext, deliveryCancel := context.WithTimeout(ctx, budget)
	defer deliveryCancel()
	for _, delivery := range e.deliverWalletNotifications(deliveryContext, 0, 200) {
		if delivery.Row.SendStartedAt != nil {
			result.DeliveriesAttempted++
		}
		if delivery.Error != nil {
			result.DeliveriesFailed++
			result.Errors = append(result.Errors, errorResult(delivery.Row.WatchRuleID, delivery.Error))
		}
		if delivery.Row.Status == "sent" {
			result.DeliveriesSent++
			result.Alerted++
		}
	}
	if scheduled && ctx.Err() == nil {
		e.nextWalletDelivery(ctx, result, repository)
	}
}
