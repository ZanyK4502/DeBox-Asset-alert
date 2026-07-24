package monitor

import (
	"context"
	"log/slog"
	"time"
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
	executor      *Executor
	tryLock       TryLockFunc
	interval      time.Duration
	lastCleanupAt time.Time
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
		r.runCycle(ctx, logger)
		timer := time.NewTimer(r.interval)
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
	lock, acquired, err := r.tryLock(ctx)
	if err != nil {
		logger.Error("monitor lock failed", "error", err)
		return
	}
	if !acquired {
		logger.Debug("monitor cycle skipped", "reason", "lock_not_acquired")
		return
	}
	defer func() {
		if err := lock.Unlock(ctx); err != nil {
			logger.Error("monitor unlock failed", "error", err)
		}
	}()

	result, err := r.executor.CheckAll(ctx, 200)
	if err != nil {
		logger.Error("monitor cycle failed", "error", err)
	} else {
		logger.Info(
			"monitor cycle completed",
			"checked", result.Checked,
			"alerted", result.Alerted,
			"errors", len(result.Errors),
		)
	}
	r.cleanupHistory(ctx, logger)
}

func (r *Runner) cleanupHistory(ctx context.Context, logger *slog.Logger) {
	now := time.Now()
	if !r.lastCleanupAt.IsZero() && now.Sub(r.lastCleanupAt) < historyCleanupInterval {
		return
	}
	result, err := r.executor.CleanupAggregationHistory(ctx)
	if err != nil {
		logger.Error("aggregation history cleanup failed", "error", err)
		return
	}
	r.lastCleanupAt = now
	logger.Info(
		"aggregation history cleanup completed",
		"notifications_deleted", result.NotificationsDeleted,
		"events_deleted", result.EventsDeleted,
		"windows_deleted", result.WindowsDeleted,
	)
}
