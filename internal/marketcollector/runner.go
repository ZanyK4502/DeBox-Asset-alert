package marketcollector

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type Runner struct {
	service *Service
}

func NewRunner(service *Service) *Runner {
	return &Runner{service: service}
}

func (runner *Runner) Run(ctx context.Context, logger *slog.Logger) {
	if runner == nil || runner.service == nil || !runner.service.Enabled() {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	tasks := []struct {
		name     string
		interval time.Duration
		run      func(context.Context) error
	}{
		{"webhook-inbox", runner.service.settings.InboxInterval, runner.service.ProcessInbox},
		{"market-data", runner.service.settings.SnapshotInterval, runner.service.RefreshMarketData},
		{"log-scanner", runner.service.settings.ScanInterval, runner.service.ScanLogs},
		{"webhook-sync", runner.service.settings.DiscoveryInterval, runner.service.SyncWebhookSubscriptions},
		{"health", runner.service.settings.HealthInterval, runner.service.CheckHealth},
		{"cleanup", runner.service.settings.CleanupInterval, runner.service.Cleanup},
	}
	var background sync.WaitGroup
	background.Add(len(tasks))
	for _, task := range tasks {
		task := task
		go func() {
			defer background.Done()
			runScheduledTask(ctx, logger, task.name, task.interval, task.run)
		}()
	}
	background.Wait()
}

func runScheduledTask(
	ctx context.Context,
	logger *slog.Logger,
	name string,
	interval time.Duration,
	run func(context.Context) error,
) {
	recordTaskError(ctx, logger, name, run(ctx))
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			recordTaskError(ctx, logger, name, run(ctx))
		}
	}
}
