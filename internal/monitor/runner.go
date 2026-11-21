package monitor

import (
	"context"
	"log/slog"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const (
	DefaultInterval        = 60 * time.Second
	historyCleanupInterval = 24 * time.Hour
)

type Lock interface {
	Unlock(context.Context) error
}

type TryLockFunc func(context.Context) (Lock, bool, error)

type Runner struct {
	executor               *Executor
	tryLock                TryLockFunc
	interval               time.Duration
	lastCleanupAt          time.Time
	nextCleanupAttemptAt   time.Time
	nextEntitlementCheckAt time.Time
	walletAfter            *store.WalletObservationCursor
	walletThrough          *store.WalletObservationCursor
	walletPassHadError     bool
	nextObservationAt      time.Time
}

func NewRunner(executor *Executor, tryLock TryLockFunc, interval time.Duration) *Runner {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Runner{executor: executor, tryLock: tryLock, interval: interval}
}

func (r *Runner) Run(ctx context.Context, logger *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		delay := r.runCycle(ctx, logger)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
	}
}

func (r *Runner) runCycle(ctx context.Context, logger *slog.Logger) time.Duration {
	lock, acquired, err := r.tryLock(ctx)
	if err != nil {
		logger.Error("monitor lock failed", "error", err)
		return r.errorDelay()
	}
	if !acquired {
		logger.Debug("monitor cycle skipped", "reason", "lock_not_acquired")
		return r.interval
	}
	defer func() {
		if err := lock.Unlock(ctx); err != nil {
			logger.Error("monitor unlock failed", "error", err)
		}
	}()

	if !time.Now().Before(r.nextEntitlementCheckAt) {
		// Advancing small due pages must not turn the old once-per-period
		// entitlement sweep into a whole-site query after every page.
		r.nextEntitlementCheckAt = time.Now().Add(r.interval)
		paused, err := r.executor.ApplyExpiredEntitlementFallbacks(ctx)
		if err != nil {
			logger.Error("expired entitlement reconciliation failed", "error", err)
		} else if paused > 0 {
			logger.Info("expired entitlements reconciled", "users_paused", paused)
		}
	}

	_, scheduled := r.executor.deps.Repository.(walletDeliveryScheduleRepository)
	deliveryOnly := scheduled && time.Now().Before(r.nextObservationAt)
	var result CycleResult
	if deliveryOnly {
		result = r.executor.checkWalletDeliveries(ctx)
		at := r.nextObservationAt
		result.NextObservationAt = &at
	} else {
		result, err = r.executor.checkDuePage(ctx, 200, r.interval, r.walletAfter, r.walletThrough)
		r.walletPassHadError = r.walletPassHadError || err != nil || len(result.Errors) > 0
		if result.through != nil {
			r.walletThrough = result.through
			if result.lastAttempted != nil {
				r.walletAfter = result.lastAttempted
			}
			if !result.HasMoreDue {
				r.walletAfter, r.walletThrough = nil, nil
			}
		}
		if scheduled {
			r.nextObservationAt = time.Now().Add(r.interval)
			if err != nil {
				r.nextObservationAt = time.Now().Add(r.errorDelay())
			} else if result.HasMoreDue {
				r.nextObservationAt = time.Now().Add(min(r.interval, 250*time.Millisecond))
			} else if result.NextObservationAt != nil {
				r.nextObservationAt = *result.NextObservationAt
			}
		}
	}
	passHadError := r.walletPassHadError
	if !deliveryOnly && err == nil && !result.HasMoreDue {
		r.walletPassHadError = false
	}
	if err != nil {
		logger.Error("monitor cycle failed", "error", err)
	} else {
		logger.Info(
			"monitor cycle completed",
			"checked", result.Checked,
			"selected", result.Selected,
			"alerted", result.Alerted,
			"queued", result.Queued, "deliveries_attempted", result.DeliveriesAttempted, "deliveries_sent", result.DeliveriesSent, "deliveries_failed", result.DeliveriesFailed,
			"errors", len(result.Errors),
			"source_reads", result.Reads.Requests, "read_memo_hits", result.Reads.Hits,
			"read_failures", result.Reads.Failures, "read_memo_expired", result.Reads.Expired,
			"read_memo_entries", result.Reads.Entries, "read_memo_bytes", result.Reads.Bytes,
			"read_memo_not_cached", result.Reads.NotCached,
			"more_due", result.HasMoreDue,
			"delivery_only", deliveryOnly,
			"last_attempted_rule_id", walletObservationRuleID(result.lastAttempted),
		)
	}
	r.cleanupHistory(ctx, logger)
	if err != nil {
		return r.errorDelay()
	}
	delay := r.nextDelay(result, time.Now())
	if (!deliveryOnly && !result.HasMoreDue && passHadError || deliveryOnly && len(result.Errors) > 0) && delay < r.errorDelay() {
		// An uncertain commit must not be rewritten as a persisted failure.
		// A short local pause after this complete traversal still bounds retries.
		delay = r.errorDelay()
	}
	return delay
}

func (r *Runner) errorDelay() time.Duration {
	if r.interval < 5*time.Second {
		return r.interval
	}
	return 5 * time.Second
}

func (r *Runner) nextDelay(result CycleResult, now time.Time) time.Duration {
	if result.HasMoreDue {
		// A fixed yield bounds empty/failing follow-up probes without imposing
		// the observation period on the unprocessed tail of a due queue.
		if r.interval < 250*time.Millisecond {
			return r.interval
		}
		return 250 * time.Millisecond
	}
	delay := r.interval
	if result.NextObservationAt != nil {
		delay = result.NextObservationAt.Sub(now)
		if delay > r.interval {
			delay = r.interval
		}
	}
	if result.NextDeliveryAt != nil && result.NextDeliveryAt.Sub(now) < delay {
		delay = result.NextDeliveryAt.Sub(now)
	}
	minimum := time.Second
	if r.interval < minimum {
		minimum = r.interval
	}
	if delay < minimum {
		delay = minimum
	}
	return delay
}

func (r *Runner) cleanupHistory(ctx context.Context, logger *slog.Logger) {
	now := time.Now()
	if now.Before(r.nextCleanupAttemptAt) {
		return
	}
	if !r.lastCleanupAt.IsZero() && now.Sub(r.lastCleanupAt) < historyCleanupInterval {
		return
	}
	result, err := r.executor.CleanupAggregationHistory(ctx)
	if err != nil {
		r.nextCleanupAttemptAt = time.Now().Add(time.Minute)
		logger.Error("aggregation history cleanup failed", "error", err)
		return
	}
	r.lastCleanupAt = now
	r.nextCleanupAttemptAt = time.Time{}
	logger.Info(
		"aggregation history cleanup completed",
		"notifications_deleted", result.NotificationsDeleted,
		"events_deleted", result.EventsDeleted,
		"windows_deleted", result.WindowsDeleted,
		"notification_detail_snapshots_deleted",
		result.NotificationDetailSnapshotsDeleted,
		"reliability_records_deleted", result.ReliabilityRecordsDeleted,
	)
}
