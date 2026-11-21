package marketcollector

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketrules"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

// These tests run the real six-chain collector and rule-engine runners against
// an empty, migrated local schema. They exercise their clocks and actual lock
// admission, without calling providers, sending notifications or creating rules.
type runnerCapacityTrace struct {
	database *store.Store
	hold     time.Duration
	changed  chan struct{}
	mu       sync.Mutex
	attempts map[string]bool
	active   map[string]int
	complete map[string]int
	held     int
	peak     int
	failures []string
}

func newRunnerCapacityTrace(database *store.Store, hold time.Duration) *runnerCapacityTrace {
	return &runnerCapacityTrace{
		database: database, hold: hold, changed: make(chan struct{}, 1),
		attempts: map[string]bool{}, active: map[string]int{}, complete: map[string]int{},
	}
}

func (trace *runnerCapacityTrace) notify() {
	select {
	case trace.changed <- struct{}{}:
	default:
	}
}

func (trace *runnerCapacityTrace) acquire(ctx context.Context, task string) (*runnerCapacityLock, bool, error) {
	trace.mu.Lock()
	trace.attempts[task] = true
	trace.mu.Unlock()
	trace.notify()
	lock, acquired, err := trace.database.AcquireMarketTaskLock(ctx, task)
	if err != nil || !acquired {
		if err != nil && ctx.Err() == nil {
			trace.mu.Lock()
			trace.failures = append(trace.failures, task+": "+err.Error())
			trace.mu.Unlock()
			trace.notify()
		}
		return nil, acquired, err
	}
	trace.mu.Lock()
	trace.active[task]++
	trace.held++
	trace.peak = max(trace.peak, trace.held)
	if trace.active[task] != 1 {
		trace.failures = append(trace.failures, "overlapping execution: "+task)
	}
	trace.mu.Unlock()
	wrapped := &runnerCapacityLock{inner: lock, trace: trace, task: task}
	if trace.hold > 0 {
		// Hold a real slot briefly to reproduce simultaneous task pressure.
		// This creates no extra goroutine or work per missed ticker period.
		timer := time.NewTimer(trace.hold)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			_ = wrapped.Unlock(context.WithoutCancel(ctx))
			return nil, false, ctx.Err()
		}
	}
	return wrapped, true, nil
}

type runnerCapacityLock struct {
	inner *store.MarketTaskLock
	trace *runnerCapacityTrace
	task  string
	once  sync.Once
}

func (lock *runnerCapacityLock) Unlock(ctx context.Context) (err error) {
	lock.once.Do(func() {
		lock.trace.mu.Lock()
		// Serialize accounting with the underlying release. A newly admitted
		// callback can otherwise increment the trace before this one decrements
		// it, falsely reporting nine held slots while the real maximum is eight.
		err = lock.inner.Unlock(ctx)
		lock.trace.active[lock.task]--
		lock.trace.held--
		if err == nil {
			lock.trace.complete[lock.task]++
		} else {
			lock.trace.failures = append(lock.trace.failures, lock.task+": unlock: "+err.Error())
		}
		lock.trace.mu.Unlock()
		lock.trace.notify()
	})
	return err
}

type runnerCapacityErrorLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (log *runnerCapacityErrorLog) Write(p []byte) (int, error) {
	log.mu.Lock()
	defer log.mu.Unlock()
	return log.b.Write(p)
}

func (log *runnerCapacityErrorLog) text() string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return log.b.String()
}

type runnerCapacityNotifier struct{ calls atomic.Int64 }

func (notifier *runnerCapacityNotifier) SendNotification(_, _, _ string) (string, error) {
	notifier.calls.Add(1)
	return "", fmt.Errorf("empty runner fixture unexpectedly attempted a notification")
}

type runnerCapacityFixture struct {
	ctx      context.Context
	cancel   context.CancelFunc
	trace    *runnerCapacityTrace
	errors   *runnerCapacityErrorLog
	notifier *runnerCapacityNotifier
	expected []string
	done     [2]chan struct{}
	stopOnce sync.Once
}

func startRunnerCapacityFixture(t *testing.T, database *store.Store, interval, hold time.Duration) *runnerCapacityFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	fixture := &runnerCapacityFixture{
		ctx: ctx, cancel: cancel, trace: newRunnerCapacityTrace(database, hold),
		errors: &runnerCapacityErrorLog{}, notifier: &runnerCapacityNotifier{},
		done: [2]chan struct{}{make(chan struct{}), make(chan struct{})},
	}
	t.Cleanup(func() { fixture.stop(t) })
	var services []*Service
	var chainKeys []string
	for _, profile := range chain.SupportedChains() {
		chainKeys = append(chainKeys, profile.Key)
		services = append(services, New(Dependencies{
			Repository: database, Chain: &fakeChain{}, Market: fakeMarket{},
			TryLock: func(ctx context.Context, task string) (Lock, bool, error) {
				lock, acquired, err := fixture.trace.acquire(ctx, task)
				return lock, acquired, err
			},
		}, Settings{
			Enabled: true, ChainKey: profile.Key, ChainID: profile.ChainID,
			WebhookAutoRepair: true, InboxInterval: interval, ScanInterval: interval,
			SnapshotInterval: interval, DiscoveryInterval: interval,
			HealthInterval: interval, CleanupInterval: interval,
		}))
		for _, name := range []string{"webhook-inbox", "market-data", "log-scanner", "webhook-sync", "health"} {
			fixture.expected = append(fixture.expected, "market:"+profile.Key+":"+name)
		}
	}
	cluster, err := NewCluster(services, "bsc")
	if err != nil {
		t.Fatal(err)
	}
	fixture.expected = append(fixture.expected, "market:"+cluster.Services()[0].settings.ChainKey+":cleanup", "rule-engine")
	logger := slog.New(slog.NewTextHandler(fixture.errors, &slog.HandlerOptions{Level: slog.LevelError}))
	collector := NewRunner(cluster.Services()...)
	rules := marketrules.New(marketrules.Dependencies{
		Repository: database, Notifications: fixture.notifier,
		TryLock: func(ctx context.Context, task string) (marketrules.Lock, bool, error) {
			lock, acquired, err := fixture.trace.acquire(ctx, task)
			return lock, acquired, err
		},
	}, marketrules.Settings{Enabled: true, TradeFactsEnabled: true, Interval: interval, MonitoringChainKeys: chainKeys})
	go func() {
		defer close(fixture.done[0])
		collector.Run(ctx, logger)
	}()
	go func() {
		defer close(fixture.done[1])
		marketrules.NewRunner(rules).Run(ctx, logger)
	}()
	return fixture
}

func (fixture *runnerCapacityFixture) stop(t *testing.T) {
	t.Helper()
	fixture.stopOnce.Do(func() {
		fixture.cancel()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		for _, done := range fixture.done {
			select {
			case <-done:
			case <-timer.C:
				t.Error("six-chain runners retained work after cancellation")
				return
			}
		}
	})
}

func (fixture *runnerCapacityFixture) wait(t *testing.T, label string, ready func(*runnerCapacityTrace) bool) {
	t.Helper()
	for {
		if err := fixture.ctx.Err(); err != nil {
			t.Fatalf("%s did not finish before bounded runner deadline: %v", label, err)
		}
		fixture.trace.mu.Lock()
		ok := ready(fixture.trace)
		failures := strings.Join(fixture.trace.failures, "; ")
		fixture.trace.mu.Unlock()
		if failures != "" {
			t.Fatalf("runner lock admission: %s", failures)
		}
		if ok {
			if text := fixture.errors.text(); text != "" {
				t.Fatalf("runner task failed before cancellation: %s", text)
			}
			if fixture.notifier.calls.Load() != 0 {
				t.Fatal("empty runner fixture generated a notification")
			}
			return
		}
		select {
		case <-fixture.trace.changed:
		case <-fixture.ctx.Done():
		}
	}
}

func TestPostgresSixChainRunnerWaitsForStartupCapacity(t *testing.T) {
	database := localTradeStore(t)
	var occupied []*store.MarketTaskLock
	for index := range 8 {
		lock, acquired, err := database.TryMarketTaskLock(context.Background(), fmt.Sprintf("startup-capacity-%d", index))
		if err != nil || !acquired {
			t.Fatalf("reserve local slot %d: acquired=%t err=%v", index, acquired, err)
		}
		occupied = append(occupied, lock)
		t.Cleanup(func() { _ = lock.Unlock(context.Background()) })
	}
	// No subsequent hourly tick can rescue a startup callback that was dropped
	// while all eight slots were busy. Every original callback must be retained.
	fixture := startRunnerCapacityFixture(t, database, time.Hour, 0)
	fixture.wait(t, "all 32 original task attempts", func(trace *runnerCapacityTrace) bool {
		return len(trace.attempts) == len(fixture.expected)
	})
	for _, lock := range occupied {
		if err := lock.Unlock(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	fixture.wait(t, "all 32 startup task completions", func(trace *runnerCapacityTrace) bool {
		for _, task := range fixture.expected {
			if trace.complete[task] != 1 {
				return false
			}
		}
		return true
	})
	fixture.stop(t)
	fixture.trace.mu.Lock()
	defer fixture.trace.mu.Unlock()
	if fixture.trace.peak > 8 || fixture.trace.held != 0 {
		t.Fatalf("startup capacity escaped its bound or leaked: peak=%d held=%d", fixture.trace.peak, fixture.trace.held)
	}
}

func TestPostgresSixChainRunnerTickCapacityIsBounded(t *testing.T) {
	database := localTradeStore(t)
	fixture := startRunnerCapacityFixture(t, database, 40*time.Millisecond, 10*time.Millisecond)
	fixture.wait(t, "two completed scanner cycles per chain", func(trace *runnerCapacityTrace) bool {
		for _, profile := range chain.SupportedChains() {
			if trace.complete["market:"+profile.Key+":log-scanner"] < 2 {
				return false
			}
		}
		return trace.complete["rule-engine"] > 0
	})
	fixture.stop(t)
	fixture.trace.mu.Lock()
	defer fixture.trace.mu.Unlock()
	if fixture.trace.peak > 8 || fixture.trace.held != 0 {
		t.Fatalf("ticker capacity escaped its bound or leaked: peak=%d held=%d", fixture.trace.peak, fixture.trace.held)
	}
	for task, active := range fixture.trace.active {
		if active != 0 {
			t.Fatalf("task %s retained %d in-flight callbacks", task, active)
		}
	}
}
