package marketrules

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type holderJobTestRepository struct {
	*holderRefreshTestRepository
	mu                               sync.Mutex
	jobs                             []store.MarketHolderRefreshJob
	claims, pages, completed, failed int
	reserveErr                       error
	reserveErrAt                     int
	completeErr                      error
	code                             string
	retryAt                          time.Time
}

func (r *holderJobTestRepository) ClaimMarketHolderRefreshJob(ctx context.Context, interval time.Duration) (*store.MarketHolderRefreshJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claims++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if interval != 15*time.Minute {
		return nil, fmt.Errorf("unexpected cadence %s", interval)
	}
	if len(r.jobs) == 0 {
		return nil, nil
	}
	job := r.jobs[0]
	r.jobs = r.jobs[1:]
	return &job, nil
}
func (r *holderJobTestRepository) ReserveMarketHolderRefreshPage(ctx context.Context, _ store.MarketHolderRefreshJob) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pages++
	if r.reserveErrAt > 0 && r.pages != r.reserveErrAt {
		return nil
	}
	return r.reserveErr
}
func (r *holderJobTestRepository) CompleteMarketHolderRefreshJob(ctx context.Context, j store.MarketHolderRefreshJob, p store.MarketHolderRefreshParams, _ time.Duration) (store.MarketHolderRefreshResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return store.MarketHolderRefreshResult{}, err
	}
	r.completed++
	if (j.ExpectedPreviousAt == nil) != (p.ExpectedPreviousAt == nil) || j.ExpectedPreviousAt != nil && !j.ExpectedPreviousAt.Equal(*p.ExpectedPreviousAt) {
		return store.MarketHolderRefreshResult{}, errors.New("sample CAS changed after claim")
	}
	if r.completeErr != nil {
		return store.MarketHolderRefreshResult{}, r.completeErr
	}
	r.params = append(r.params, p)
	return store.MarketHolderRefreshResult{}, nil
}
func (r *holderJobTestRepository) FailMarketHolderRefreshJob(ctx context.Context, _ store.MarketHolderRefreshJob, code string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	r.failed++
	r.code, r.retryAt = code, at
	return nil
}

func holderSchedulerFixture(t *testing.T, p HolderProvider) (*HolderRefreshRunner, *holderJobTestRepository) {
	t.Helper()
	at := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	old := at.Add(-30 * time.Minute)
	r := &holderJobTestRepository{holderRefreshTestRepository: &holderRefreshTestRepository{}, jobs: []store.MarketHolderRefreshJob{{Project: store.MarketProject{ID: 1, ChainID: 56, ChainKey: "bsc", TokenAddress: tradeFactsTestToken}, LeaseToken: 1, StartedAt: at, LeaseUntil: at.Add(90 * time.Second), ExpectedPreviousAt: &old}}}
	s := New(Dependencies{Repository: r, Holders: p}, Settings{Enabled: true})
	s.now = func() time.Time { return at }
	runner, err := NewHolderRefreshRunner(s, HolderRefreshSettings{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	return runner, r
}

func TestHolderSchedulerSingleSharedRoundKeepsPreRequestCASAndTracking(t *testing.T) {
	count := int64(25)
	items := make([]chain.TokenHolder, 25)
	for i := range items {
		items[i] = chain.TokenHolder{OwnerAddress: fmt.Sprintf("0x%040x", i+100), BalanceRaw: fmt.Sprint(1000 - i)}
	}
	provider := &holderSampleTestProvider{pages: []chain.TokenHoldersPage{{Count: &count, Items: items}}}
	runner, repo := holderSchedulerFixture(t, provider)
	provider.onFirst = func() {
		newer := runner.service.now().Add(-time.Minute)
		repo.state = &store.MarketHolderRefreshState{LastSuccessAt: &newer}
	}
	result, err := runner.RunOnce(context.Background())
	if err != nil || result.Refreshed != 1 || result.PagesAdmitted != 1 || repo.completed != 1 || repo.attempts != 0 || repo.failed != 0 || len(repo.params) != 1 {
		t.Fatalf("round=%+v complete=%d attempts=%d failure=%d err=%v", result, repo.completed, repo.attempts, repo.failed, err)
	}
	p := repo.params[0]
	if len(p.Ranks) != 25 || len(p.Holders) != 20 || !p.ExpectedPreviousAt.Equal(runner.service.now().Add(-30*time.Minute)) {
		t.Fatalf("tracking/CAS changed: %+v", p)
	}
}

func TestHolderSchedulerPartialOrUnsupportedSampleNeverCompletesBaseline(t *testing.T) {
	for _, capacity := range []bool{false, true} {
		t.Run(fmt.Sprint(capacity), func(t *testing.T) {
			count := int64(2)
			if capacity {
				count = int64(store.MarketHolderRefreshMaxHolders + 1)
			}
			provider := &holderSampleTestProvider{pages: []chain.TokenHoldersPage{{Count: &count, Cursor: "second", Items: []chain.TokenHolder{{OwnerAddress: fmt.Sprintf("0x%040x", 100), BalanceRaw: "100"}}}}, errAt: 2}
			runner, repo := holderSchedulerFixture(t, provider)
			result, err := runner.RunOnce(context.Background())
			if err == nil || result.Refreshed != 0 || repo.completed != 0 || repo.failed != 1 || len(repo.params) != 0 {
				t.Fatalf("partial committed: %+v %v", result, err)
			}
			if capacity {
				if repo.code != "source_capacity" || !repo.retryAt.Equal(runner.service.now().Add(15*time.Minute)) || repo.pages != 1 {
					t.Fatalf("capacity busy retries: %s %s pages=%d", repo.code, repo.retryAt, repo.pages)
				}
			} else if repo.code != "source_error" || repo.pages != 2 {
				t.Fatalf("source error not typed: %s pages=%d", repo.code, repo.pages)
			}
		})
	}
}

func TestHolderSchedulerBudgetStopsBeforeProviderAndDoesNotBurnBaseline(t *testing.T) {
	provider := &holderSampleTestProvider{}
	runner, repo := holderSchedulerFixture(t, provider)
	retry := runner.service.now().Add(time.Minute)
	repo.reserveErr = &store.MarketHolderRefreshBudgetError{RetryAt: retry}
	result, err := runner.RunOnce(context.Background())
	if !errors.Is(err, store.ErrMarketHolderRefreshBudget) || len(provider.options) != 0 || repo.code != "provider_budget" || !repo.retryAt.Equal(retry) || repo.completed != 0 || result.PagesAdmitted != 0 {
		t.Fatalf("budget did HTTP/write: %+v code=%s err=%v", result, repo.code, err)
	}
}

type holderContextProvider struct{ started chan struct{} }

func (p holderContextProvider) TokenHoldersByContract(ctx context.Context, _, _, _ string, _ chain.TokenHoldersOptions) (chain.TokenHoldersPage, error) {
	if p.started != nil {
		select {
		case p.started <- struct{}{}:
		default:
		}
	}
	<-ctx.Done()
	return chain.TokenHoldersPage{}, ctx.Err()
}

func TestHolderSchedulerRoundTimeoutReleasesWithIndependentCleanupContext(t *testing.T) {
	runner, repo := holderSchedulerFixture(t, holderContextProvider{})
	runner.settings.RoundTimeout = 5 * time.Millisecond
	result, err := runner.RunOnce(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) || result.FailureCode != "round_timeout" || repo.failed != 1 || repo.completed != 0 {
		t.Fatalf("timeout leaked job: %+v failed=%d err=%v", result, repo.failed, err)
	}
}

func TestHolderSchedulerIdleBackoffAndShutdownAreBounded(t *testing.T) {
	delay := time.Duration(0)
	for _, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, time.Minute, time.Minute} {
		delay = holderIdleDelay(delay, time.Second, false)
		if delay != want {
			t.Fatalf("idle=%s want %s", delay, want)
		}
	}
	if holderIdleDelay(delay, time.Second, true) != time.Second {
		t.Fatal("real work did not reset idle delay")
	}
	runner, repo := holderSchedulerFixture(t, &holderSampleTestProvider{})
	repo.jobs = nil
	runner.settings.Workers = 2
	runner.settings.PollInterval = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Millisecond)
	defer cancel()
	start := time.Now()
	runner.Run(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)))
	repo.mu.Lock()
	claims := repo.claims
	repo.mu.Unlock()
	if claims > 14 || claims < 2 || time.Since(start) > time.Second || repo.pages != 0 || repo.failed != 0 {
		t.Fatalf("idle busy loop/leak: claims=%d elapsed=%s pages=%d", claims, time.Since(start), repo.pages)
	}
}

func TestRuleCycleDoesNotSynchronouslyRefreshHolders(t *testing.T) {
	s, base, _ := newTradeLaneTestService(true)
	repo := &tradeActivationCycleRepository{tradeLaneTestRepository: base, deliveryCompleted: true}
	provider := &holderSampleTestProvider{}
	s.repository, s.holders, s.settings.Enabled = repo, provider, true
	s.notifications = &tradeActivationNotifications{}
	if _, err := s.RunCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provider.options) != 0 {
		t.Fatal("rule cycle blocked on holder sampling")
	}
}

type holderConcurrentCycleRepository struct {
	*holderJobTestRepository
	entitlementChecks atomic.Int64
}

func (r *holderConcurrentCycleRepository) ApplyExpiredEntitlementFallbacks(context.Context) (int64, error) {
	r.entitlementChecks.Add(1)
	return 0, nil
}
func (*holderConcurrentCycleRepository) ReconcileMarketNotificationSources(context.Context) error {
	return nil
}
func (*holderConcurrentCycleRepository) RecoverStaleMarketDeliveries(context.Context, time.Duration) (int64, error) {
	return 0, nil
}
func (*holderConcurrentCycleRepository) ProcessPendingWatchCombinationTriggers(context.Context, int) (int64, error) {
	return 0, nil
}
func (*holderConcurrentCycleRepository) ListEnabledMarketRules(context.Context, int) ([]store.MarketRule, error) {
	return nil, nil
}
func (*holderConcurrentCycleRepository) ClaimMarketDeliveries(context.Context, int) ([]store.MarketDeliveryClaim, error) {
	return nil, nil
}

// No provider/test repository mutable state is shared without synchronization;
// the only concurrently updated production state is the rule cycle's timer.
type emptyConcurrentHolderProvider struct{ calls atomic.Int64 }

func (p *emptyConcurrentHolderProvider) TokenHoldersByContract(context.Context, string, string, string, chain.TokenHoldersOptions) (chain.TokenHoldersPage, error) {
	p.calls.Add(1)
	count := int64(0)
	return chain.TokenHoldersPage{Count: &count, Items: []chain.TokenHolder{}}, nil
}

func TestHolderSchedulerDoesNotReadConcurrentRuleCycleBookkeeping(t *testing.T) {
	const rounds = 512
	provider := &emptyConcurrentHolderProvider{}
	runner, jobs := holderSchedulerFixture(t, provider)
	job := jobs.jobs[0]
	jobs.jobs = make([]store.MarketHolderRefreshJob, rounds)
	for i := range jobs.jobs {
		jobs.jobs[i] = job
		jobs.jobs[i].LeaseToken = int64(i + 1)
	}
	repository := &holderConcurrentCycleRepository{holderJobTestRepository: jobs}
	service := runner.service
	service.repository, service.notifications = repository, &tradeActivationNotifications{}
	var ticks atomic.Int64
	epoch := service.now()
	service.now = func() time.Time { return epoch.Add(time.Duration(ticks.Add(1)) * 2 * time.Minute) }
	start := make(chan struct{})
	errorsOut := make(chan error, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < rounds; i++ {
			if _, err := service.RunCycle(context.Background()); err != nil {
				errorsOut <- err
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < rounds; i++ {
			if result, err := runner.RunOnce(context.Background()); err != nil || result.Refreshed != 1 {
				errorsOut <- fmt.Errorf("holder round=%+v err=%v", result, err)
				return
			}
		}
	}()
	close(start)
	workers.Wait()
	close(errorsOut)
	for err := range errorsOut {
		t.Error(err)
	}
	if repository.entitlementChecks.Load() != rounds || provider.calls.Load() != rounds || jobs.completed != rounds {
		t.Fatalf("concurrent work was skipped: entitlement=%d provider=%d complete=%d", repository.entitlementChecks.Load(), provider.calls.Load(), jobs.completed)
	}
}
