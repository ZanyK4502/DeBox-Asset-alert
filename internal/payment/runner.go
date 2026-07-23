package payment

import (
	"context"
	"log/slog"
	"time"
)

const (
	DefaultInterval      = 60 * time.Second
	defaultReconcileSize = 50
)

type Lock interface {
	Unlock(context.Context) error
}

type TryLockFunc func(context.Context) (Lock, bool, error)

type Runner struct {
	service  *Service
	tryLock  TryLockFunc
	interval time.Duration
}

func NewRunner(service *Service, tryLock TryLockFunc, interval time.Duration) *Runner {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Runner{service: service, tryLock: tryLock, interval: interval}
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
	if r.tryLock == nil {
		logger.Error("payment reconciliation lock is not configured")
		return
	}
	lock, acquired, err := r.tryLock(ctx)
	if err != nil {
		logger.Error("payment reconciliation lock failed", "error", err)
		return
	}
	if !acquired {
		logger.Debug("payment reconciliation skipped", "reason", "lock_not_acquired")
		return
	}
	defer func() {
		if err := lock.Unlock(ctx); err != nil {
			logger.Error("payment reconciliation unlock failed", "error", err)
		}
	}()

	result, err := r.service.Reconcile(ctx, defaultReconcileSize)
	if err != nil {
		logger.Error("payment reconciliation failed", "error", err)
		return
	}
	for _, item := range result.Errors {
		logger.Error(
			"payment verification failed",
			"order_id", item.OrderID,
			"error", item.Error,
		)
	}
	logger.Info(
		"payment reconciliation completed",
		"checked", result.Checked,
		"expired", result.Expired,
		"paid", result.Paid,
		"confirming", result.Confirming,
		"failed", result.Failed,
		"errors", len(result.Errors),
	)
}
