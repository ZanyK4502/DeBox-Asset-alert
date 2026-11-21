package summary

import (
	"context"
	"log/slog"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
)

const DefaultInterval = 60 * time.Second

type Runner struct {
	executor           *Executor
	interval           time.Duration
	nextNormalDue      time.Time
	nextMergeAttemptAt time.Time
}

func NewRunner(executor *Executor, interval time.Duration) *Runner {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Runner{executor: executor, interval: interval}
}

func (r *Runner) Run(ctx context.Context, logger *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		delay := r.runScheduledCycle(ctx, logger)
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

func (r *Runner) runCycle(ctx context.Context, logger *slog.Logger) {
	result, err := r.executor.SendDue(ctx, defaultPageSize)
	r.report(logger, result, err)
}

func (r *Runner) runScheduledCycle(ctx context.Context, logger *slog.Logger) time.Duration {
	now := time.Now()
	normal := r.nextNormalDue.IsZero() || !now.Before(r.nextNormalDue)
	var result CycleResult
	var err error
	if normal {
		r.nextNormalDue = now.Add(r.interval)
		r.nextMergeAttemptAt = time.Time{}
		result, err = r.executor.SendDue(ctx, defaultPageSize)
		if err != nil || len(result.Errors) > 0 || result.Locked > 0 {
			r.nextMergeAttemptAt = r.nextNormalDue
		}
	} else {
		result, err = r.executor.SendPending(ctx, defaultPageSize)
		if result.NeedsNormalCycle && !now.Before(r.nextMergeAttemptAt) {
			// An already-due new reporting cutoff uses the original merge path.
			// This extra cycle does not postpone the normal schedule, and one bad
			// target cannot request a full rebuild again on every queue wake.
			r.nextMergeAttemptAt = r.nextNormalDue
			result, err = r.executor.SendDue(ctx, defaultPageSize)
			if result.Sent == 0 && (err != nil || len(result.Errors) > 0 || result.Locked > 0) {
				result.NeedsNormalCycle = true
			}
		}
	}
	r.report(logger, result, err)
	return r.nextDelay(ctx, result, err, time.Now())
}

func (r *Runner) nextDelay(ctx context.Context, result CycleResult, cycleErr error, now time.Time) time.Duration {
	at := r.nextNormalDue
	if cycleErr == nil && result.Locked == 0 && !result.NeedsNormalCycle {
		if repository, ok := r.executor.deps.Repository.(pendingSummaryRepository); ok {
			if next, err := repository.NextDailySummaryDispatchAt(ctx); err == nil && next != nil {
				candidate := *next
				if result.NextDeliveryAt != nil && result.NextDeliveryAt.After(candidate) {
					candidate = *result.NextDeliveryAt
				}
				if hint := notificationdelivery.NextAdmissionAt(r.executor.deps.Notifications); hint.After(candidate) {
					candidate = hint
				}
				if !candidate.After(now) && (len(result.Errors) > 0 || result.Sent == 0 && result.Skipped > 0) {
					candidate = now.Add(min(5*time.Second, r.interval))
				}
				if candidate.Before(at) {
					at = candidate
				}
			}
		}
	}
	return max(min(time.Second, r.interval), at.Sub(now))
}

func (r *Runner) report(logger *slog.Logger, result CycleResult, err error) {
	if err != nil {
		logger.Error("summary cycle failed", "error", err)
		return
	}
	for _, item := range result.Errors {
		logger.Error(
			"summary delivery failed",
			"subscription_id", item.SubscriptionID,
			"error", item.Error,
		)
	}
	logger.Info(
		"summary cycle completed",
		"sent", result.Sent,
		"skipped", result.Skipped,
		"locked", result.Locked,
		"errors", len(result.Errors),
	)
}
