package marketcollector

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type Runner struct {
	services []*Service
}

func NewRunner(services ...*Service) *Runner {
	return &Runner{services: append([]*Service(nil), services...)}
}

func (runner *Runner) Run(ctx context.Context, logger *slog.Logger) {
	if runner == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	var services []*Service
	for _, service := range runner.services {
		if service != nil && service.Enabled() {
			services = append(services, service)
		}
	}
	if len(services) == 0 {
		return
	}
	var workers sync.WaitGroup
	workers.Add(len(services))
	for index, service := range services {
		service := service
		includeGlobalTasks := index == 0
		go func() {
			defer workers.Done()
			runService(ctx, logger, service, includeGlobalTasks)
		}()
	}
	workers.Wait()
}

func runService(
	ctx context.Context,
	logger *slog.Logger,
	service *Service,
	includeGlobalTasks bool,
) {
	tasks := []struct {
		name     string
		interval time.Duration
		run      func(context.Context) error
	}{
		{"webhook-inbox", service.settings.InboxInterval, service.ProcessInbox},
		{"market-data", service.settings.SnapshotInterval, service.RefreshMarketData},
		{"log-scanner", service.settings.ScanInterval, service.ScanLogs},
		{"webhook-sync", service.settings.DiscoveryInterval, service.SyncWebhookSubscriptions},
		{"health", service.settings.HealthInterval, service.CheckHealth},
	}
	if includeGlobalTasks {
		tasks = append(tasks, struct {
			name     string
			interval time.Duration
			run      func(context.Context) error
		}{"cleanup", service.settings.CleanupInterval, service.Cleanup})
	}
	var background sync.WaitGroup
	background.Add(len(tasks))
	for _, task := range tasks {
		task := task
		go func() {
			defer background.Done()
			runScheduledTask(
				ctx,
				logger,
				service.settings.ChainKey+":"+task.name,
				task.interval,
				task.run,
			)
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
