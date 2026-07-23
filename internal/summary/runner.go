package summary

import (
	"context"
	"log/slog"
	"time"
)

const DefaultInterval = 60 * time.Second

type Runner struct {
	executor *Executor
	interval time.Duration
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
	result, err := r.executor.SendDue(ctx, defaultPageSize)
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
