package marketrules

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const maxHolderRoundDuration = time.Minute

type HolderRefreshSettings struct {
	Workers int
	// PollInterval is the minimum idle delay and post-work yield, not a
	// mandatory database polling frequency. Idle delay grows to one minute.
	PollInterval time.Duration
	RoundTimeout time.Duration
}

type HolderRefreshCycleResult struct {
	Claimed       int64
	Refreshed     int64
	PagesAdmitted int64
	Idle          bool
	FailureCode   string
}

type holderJobRepository interface {
	atomicHolderRepository
	ClaimMarketHolderRefreshJob(context.Context, time.Duration) (*store.MarketHolderRefreshJob, error)
	ReserveMarketHolderRefreshPage(context.Context, store.MarketHolderRefreshJob) error
	CompleteMarketHolderRefreshJob(context.Context, store.MarketHolderRefreshJob, store.MarketHolderRefreshParams, time.Duration) (store.MarketHolderRefreshResult, error)
	FailMarketHolderRefreshJob(context.Context, store.MarketHolderRefreshJob, string, time.Time) error
}

type HolderRefreshRunner struct {
	service  *Service
	settings HolderRefreshSettings
}

func NewHolderRefreshRunner(service *Service, settings HolderRefreshSettings) (*HolderRefreshRunner, error) {
	if service == nil {
		return nil, errors.New("holder refresh service is required")
	}
	if settings.Workers == 0 {
		settings.Workers = store.MarketHolderRefreshMaxWorkers
	}
	if settings.PollInterval == 0 {
		settings.PollInterval = time.Second
	}
	if settings.RoundTimeout == 0 {
		settings.RoundTimeout = maxHolderRoundDuration
	}
	if settings.Workers < 1 || settings.Workers > store.MarketHolderRefreshMaxWorkers || settings.PollInterval <= 0 ||
		settings.PollInterval > time.Minute || settings.RoundTimeout <= 0 || settings.RoundTimeout > maxHolderRoundDuration {
		return nil, errors.New("holder refresh requires 1..2 workers, idle minimum <=1 minute and round timeout <=1 minute")
	}
	return &HolderRefreshRunner{service: service, settings: settings}, nil
}

func (runner *HolderRefreshRunner) Enabled() bool {
	return runner != nil && runner.service != nil && runner.service.Enabled() && runner.service.holders != nil
}

func holderIdleDelay(current, minimum time.Duration, didWork bool) time.Duration {
	if didWork || current < minimum {
		return minimum
	}
	if current >= time.Minute/2 {
		return time.Minute
	}
	return current * 2
}

func (runner *HolderRefreshRunner) Run(ctx context.Context, logger *slog.Logger) {
	if !runner.Enabled() {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	var workers sync.WaitGroup
	workers.Add(runner.settings.Workers)
	for range runner.settings.Workers {
		go func() {
			defer workers.Done()
			delay := time.Duration(0)
			for ctx.Err() == nil {
				result, err := runner.RunOnce(ctx)
				if ctx.Err() != nil {
					return
				}
				if err != nil {
					// Typed outcomes only: no owner, provider body, or raw URL.
					logger.Warn("holder refresh deferred", "failure_code", result.FailureCode, "claimed", result.Claimed, "pages_admitted", result.PagesAdmitted)
				} else if result.Claimed > 0 {
					logger.Info("holder refresh completed", "refreshed", result.Refreshed, "pages_admitted", result.PagesAdmitted)
				}
				delay = holderIdleDelay(delay, runner.settings.PollInterval, result.Claimed > 0)
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}()
	}
	workers.Wait()
}

type leasedHolderProvider struct {
	base  HolderProvider
	repo  holderJobRepository
	job   store.MarketHolderRefreshJob
	pages *int64
}

func (provider leasedHolderProvider) TokenHoldersByContract(ctx context.Context, token, key, fallback string, options chain.TokenHoldersOptions) (chain.TokenHoldersPage, error) {
	if err := ctx.Err(); err != nil {
		return chain.TokenHoldersPage{}, err
	}
	if token != provider.job.Project.TokenAddress || key != provider.job.Project.ChainKey {
		return chain.TokenHoldersPage{}, errors.New("holder job scope mismatch")
	}
	if err := provider.repo.ReserveMarketHolderRefreshPage(ctx, provider.job); err != nil {
		return chain.TokenHoldersPage{}, err
	}
	*provider.pages++
	if err := ctx.Err(); err != nil {
		return chain.TokenHoldersPage{}, err
	}
	return provider.base.TokenHoldersByContract(ctx, token, key, fallback, options)
}

func (runner *HolderRefreshRunner) RunOnce(ctx context.Context) (HolderRefreshCycleResult, error) {
	result := HolderRefreshCycleResult{Idle: true}
	if !runner.Enabled() {
		return result, nil
	}
	repository, ok := runner.service.repository.(holderJobRepository)
	if !ok {
		result.FailureCode = "database_error"
		return result, errors.New("leased holder refresh repository is unavailable")
	}
	job, err := repository.ClaimMarketHolderRefreshJob(ctx, runner.service.settings.HolderRefreshInterval)
	if err != nil {
		result.FailureCode = "database_error"
		return result, err
	}
	if job == nil {
		return result, nil
	}
	result.Claimed, result.Idle = 1, false
	roundCtx, cancel := context.WithTimeout(ctx, runner.settings.RoundTimeout)
	defer cancel()
	// The rule runner owns mutable cycle bookkeeping (lastEntitlementCheck).
	// Build from immutable dependencies rather than copying that live Service.
	worker := Service{
		repository:   runner.service.repository,
		holders:      leasedHolderProvider{base: runner.service.holders, repo: repository, job: *job, pages: &result.PagesAdmitted},
		settings:     runner.service.settings,
		now:          runner.service.now,
		publicAppURL: runner.service.publicAppURL,
	}
	budget := store.MarketHolderRefreshMaxPages
	params, err := worker.prepareMarketHolderRefresh(roundCtx, job.Project, &budget, job.ExpectedPreviousAt)
	if err == nil {
		_, err = repository.CompleteMarketHolderRefreshJob(roundCtx, *job, params, runner.service.settings.HolderRefreshInterval)
	}
	if err == nil {
		result.Refreshed = 1
		return result, nil
	}
	code, retryAt := holderRefreshFailure(*job, err, runner.service.now(), runner.service.settings.HolderRefreshInterval)
	result.FailureCode = code
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cleanupCancel()
	cleanupErr := repository.FailMarketHolderRefreshJob(cleanupCtx, *job, code, retryAt)
	if errors.Is(cleanupErr, store.ErrMarketHolderRefreshLeaseLost) {
		// A newer lease owns the outcome. Never let an old worker change it.
		cleanupErr = nil
	}
	return result, errors.Join(err, cleanupErr)
}

func holderRefreshFailure(job store.MarketHolderRefreshJob, err error, now time.Time, interval time.Duration) (string, time.Time) {
	var budget *store.MarketHolderRefreshBudgetError
	if errors.As(err, &budget) {
		return "provider_budget", budget.RetryAt
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, store.ErrMarketHolderRefreshDemandLost) {
		return "round_cancelled", now.Add(time.Second)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "round_timeout", now.Add(30 * time.Second)
	}
	if errors.Is(err, store.ErrStaleMarketHolderRefresh) || errors.Is(err, store.ErrMarketHolderRefreshLeaseLost) {
		return "baseline_stale", now.Add(5 * time.Second)
	}
	code := "database_error"
	var source *holderSamplingFailure
	if errors.As(err, &source) {
		code = source.code
	}
	if code == "source_capacity" {
		// A known unchanged coverage cap is not a transient outage. Keep the
		// original cadence for probing it instead of repeatedly buying pages.
		return code, now.Add(interval)
	}
	failures := min(job.ConsecutiveFailures, 4)
	delay := min(30*time.Second*time.Duration(1<<uint(failures)), 5*time.Minute)
	return code, now.Add(delay)
}

type holderSamplingFailure struct {
	code  string
	cause error
}

func (failure *holderSamplingFailure) Error() string {
	return fmt.Sprintf("source_coverage_unknown: %v", failure.cause)
}
func (failure *holderSamplingFailure) Unwrap() error { return failure.cause }
