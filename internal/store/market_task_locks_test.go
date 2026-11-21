package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMarketTaskLockPoolIsBoundedAndDoesNotMutateQueryConfig(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgresql:///unused?host=/tmp/debox-no-connection-created")
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns, cfg.MinConns, cfg.MinIdleConns = 4, 0, 0
	cfg.ConnConfig.RuntimeParams["application_name"] = "query-pool"
	for _, limit := range []int32{0, 17} {
		if p, err := newMarketTaskLockPool(context.Background(), cfg, limit); err == nil || p != nil {
			t.Fatalf("invalid lock limit %d accepted", limit)
		}
	}
	p, err := newMarketTaskLockPool(context.Background(), cfg, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	got := p.pool.Config()
	if got.MaxConns != 6 || got.MinConns != 0 || got.MinIdleConns != 0 || cap(p.slots) != 2 || cap(p.waiters) != 32 || cap(p.legacySlots) != 4 || got.ConnConfig.RuntimeParams["application_name"] != "debox_market_task_locks" {
		t.Fatal("task lock pool is not independently bounded")
	}
	if cfg.MaxConns != 4 || cfg.ConnConfig.RuntimeParams["application_name"] != "query-pool" {
		t.Fatal("task lock pool changed the main pool configuration")
	}
	if p.pool.Stat().TotalConns() != 0 {
		t.Fatal("idle task lock pool eagerly opened connections")
	}
}

func awaitMarketTaskWaiters(t *testing.T, pool *marketTaskLockPool, wanted int) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for len(pool.waiters) != wanted {
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("market waiters=%d, want %d", len(pool.waiters), wanted)
		}
	}
}

func TestMarketTaskLocalSlotWaitHasCancellationAndTimeoutBounds(t *testing.T) {
	for _, test := range []struct {
		name   string
		cancel bool
	}{
		{name: "local timeout"},
		{name: "caller cancellation", cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool := &marketTaskLockPool{slots: make(chan struct{}, 1), waiters: make(chan struct{}, marketTaskSlotWaiters)}
			pool.slots <- struct{}{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			wait := 20 * time.Millisecond
			if test.cancel {
				wait = time.Second
			}
			go func() {
				release, err := pool.waitForMarketSlot(ctx, wait)
				if release != nil {
					release()
				}
				result <- err
			}()
			if test.cancel {
				awaitMarketTaskWaiters(t, pool, 1)
				cancel()
			}
			select {
			case err := <-result:
				if test.cancel && !errors.Is(err, context.Canceled) {
					t.Fatalf("caller cancellation became capacity/DB contention: %v", err)
				}
				if !test.cancel && (!errors.Is(err, ErrMarketTaskCapacityDeferred) || errors.Is(err, context.DeadlineExceeded)) {
					t.Fatalf("local timeout not distinguished: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("local slot wait did not finish within its bound")
			}
			if len(pool.slots) != 1 || len(pool.waiters) != 0 {
				t.Fatalf("failed wait consumed slot or leaked waiter: %d/%d", len(pool.slots), len(pool.waiters))
			}
		})
	}
}

func TestMarketTaskLocalWaitQueueIsHardBounded(t *testing.T) {
	pool := &marketTaskLockPool{slots: make(chan struct{}, 1), waiters: make(chan struct{}, marketTaskSlotWaiters)}
	pool.slots <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan error, marketTaskSlotWaiters)
	for range marketTaskSlotWaiters {
		go func() {
			release, err := pool.waitForMarketSlot(ctx, 5*time.Second)
			if release != nil {
				release()
			}
			results <- err
		}()
	}
	awaitMarketTaskWaiters(t, pool, marketTaskSlotWaiters)
	fullCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	release, err := pool.waitForMarketSlot(fullCtx, 2*time.Second)
	if release != nil || !errors.Is(err, ErrMarketTaskCapacityDeferred) || !strings.Contains(err.Error(), "queue is full") {
		t.Fatalf("33rd waiter did not defer immediately: release=%t, err=%v", release != nil, err)
	}
	cancel()
	for range marketTaskSlotWaiters {
		select {
		case err := <-results:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled queue waiter error=%v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("cancelled bounded wait queue did not drain")
		}
	}
	if len(pool.waiters) != 0 || len(pool.slots) != 1 {
		t.Fatal("queue cancellation leaked capacity")
	}
}

func TestPostgresMarketTaskWaitersDoNotConsumeConnectionsOrLegacyCapacity(t *testing.T) {
	_, fixture := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := fixture.Config()
	cfg.MaxConns, cfg.MinConns, cfg.MinIdleConns = 1, 0, 0
	queryPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(queryPool.Close)
	locks, err := newMarketTaskLockPool(ctx, cfg, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(locks.Close)
	database := &Store{db: queryPool, pool: queryPool, taskLocks: locks}
	var held []*MarketTaskLock
	for index := range 8 {
		lock, acquired, err := database.TryMarketTaskLock(ctx, fmt.Sprintf("held-before-wait-%d", index))
		if err != nil || !acquired {
			t.Fatalf("initial market slot: %t/%v", acquired, err)
		}
		held = append(held, lock)
		t.Cleanup(func() { _ = lock.Unlock(context.Background()) })
	}
	type result struct {
		index int
		err   error
	}
	results := make(chan result, marketTaskSlotWaiters)
	var running, maximum atomic.Int32
	for index := range marketTaskSlotWaiters {
		go func() {
			lock, acquired, err := database.AcquireMarketTaskLock(ctx, fmt.Sprintf("waiting-market-task-%d", index))
			if err != nil || !acquired {
				results <- result{index: index, err: fmt.Errorf("waiter did not execute: acquired=%t: %v", acquired, err)}
				return
			}
			active := running.Add(1)
			for previous := maximum.Load(); active > previous && !maximum.CompareAndSwap(previous, active); previous = maximum.Load() {
			}
			time.Sleep(2 * time.Millisecond)
			running.Add(-1)
			err = lock.Unlock(ctx)
			results <- result{index: index, err: err}
		}()
	}
	awaitMarketTaskWaiters(t, locks, marketTaskSlotWaiters)
	if locks.pool.Stat().TotalConns() != 8 || locks.pool.Stat().AcquiredConns() != 8 || queryPool.Stat().AcquiredConns() != 0 {
		t.Fatal("waiting market tasks consumed database/query connections")
	}
	type executionLock interface{ Unlock(context.Context) error }
	var legacy []executionLock
	register := func(lock executionLock, acquired bool, err error) {
		t.Helper()
		if err != nil || !acquired {
			t.Fatalf("queued markets blocked legacy task: %t/%v", acquired, err)
		}
		legacy = append(legacy, lock)
		t.Cleanup(func() { _ = lock.Unlock(context.Background()) })
	}
	register(database.TryBotPollingLock(ctx))
	register(database.TryMonitorExecutionLock(ctx))
	register(database.TryPaymentReconciliationLock(ctx))
	register(database.TryScheduledSummaryLock(ctx, 503))
	if locks.pool.Stat().AcquiredConns() != 12 || len(locks.legacySlots) != 4 {
		t.Fatal("legacy lane no longer has four independent reservations")
	}
	var one int
	if err := database.db.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("market wait queue blocked ordinary database queries: %d/%v", one, err)
	}
	if next, acquired, err := database.marketTaskLock(ctx, "over-bounded-waiters", time.Millisecond); next != nil || acquired || !errors.Is(err, ErrMarketTaskCapacityDeferred) {
		t.Fatalf("overfull queue was not distinguished: %t/%v", acquired, err)
	}
	for _, lock := range held {
		if err := lock.Unlock(ctx); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[int]bool{}
	for range marketTaskSlotWaiters {
		select {
		case result := <-results:
			if result.err != nil || seen[result.index] {
				t.Fatalf("waiter failed or duplicated: %+v", result)
			}
			seen[result.index] = true
		case <-ctx.Done():
			t.Fatal("bounded market waiters did not all resume after capacity release")
		}
	}
	if maximum.Load() < 1 || maximum.Load() > 8 || running.Load() != 0 || len(locks.slots) != 0 || len(locks.waiters) != 0 {
		t.Fatalf("execution capacity or cleanup mismatch: max=%d, active=%d, slots=%d, waiters=%d", maximum.Load(), running.Load(), len(locks.slots), len(locks.waiters))
	}
	for _, lock := range legacy {
		if err := lock.Unlock(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPostgresMarketTaskAcquireKeepsCrossStoreAdvisoryContentionNonblocking(t *testing.T) {
	_, queryPool := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	firstPool, err := newMarketTaskLockPool(ctx, queryPool.Config(), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(firstPool.Close)
	secondPool, err := newMarketTaskLockPool(ctx, queryPool.Config(), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(secondPool.Close)
	first, second := &Store{taskLocks: firstPool}, &Store{taskLocks: secondPool}
	lock, acquired, err := first.AcquireMarketTaskLock(ctx, "market:bsc:log-scanner")
	if err != nil || !acquired {
		t.Fatalf("first advisory acquire: %t/%v", acquired, err)
	}
	t.Cleanup(func() { _ = lock.Unlock(context.Background()) })
	if other, acquired, err := second.marketTaskLock(ctx, "market:bsc:log-scanner", time.Millisecond); other != nil || acquired || err != nil {
		t.Fatalf("another instance's advisory lock became local capacity retry: %t/%v", acquired, err)
	}
	if len(secondPool.slots) != 0 || len(secondPool.waiters) != 0 {
		t.Fatal("cross-store advisory contention retained local capacity")
	}
	if err := lock.Unlock(ctx); err != nil {
		t.Fatal(err)
	}
	other, acquired, err := second.AcquireMarketTaskLock(ctx, "market:bsc:log-scanner")
	if err != nil || !acquired {
		t.Fatalf("unchanged advisory key not reusable: %t/%v", acquired, err)
	}
	if err := other.Unlock(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresMarketTaskLocksReserveNoQueryConnectionsAndSkipAtCapacity(t *testing.T) {
	_, fixturePool := openIntegrationStore(t)
	ctx := context.Background()
	cfg := fixturePool.Config()
	cfg.MaxConns, cfg.MinConns, cfg.MinIdleConns = 1, 0, 0
	queryPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(queryPool.Close)
	locks, err := newMarketTaskLockPool(ctx, cfg, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(locks.Close)
	database := &Store{db: queryPool, pool: queryPool, taskLocks: locks}
	lock, acquired, err := database.TryMarketTaskLock(ctx, "isolated-query-capacity")
	if err != nil || !acquired {
		t.Fatalf("first task lock: %t/%v", acquired, err)
	}
	t.Cleanup(func() { _ = lock.Unlock(ctx) })
	type executionLock interface{ Unlock(context.Context) error }
	var legacyLocks []executionLock
	register := func(lock executionLock, acquired bool, err error) {
		t.Helper()
		if err != nil || !acquired {
			t.Fatalf("market slots consumed reserved execution capacity: %t/%v", acquired, err)
		}
		legacyLocks = append(legacyLocks, lock)
		t.Cleanup(func() { _ = lock.Unlock(ctx) })
	}
	register(database.TryBotPollingLock(ctx))
	register(database.TryMonitorExecutionLock(ctx))
	register(database.TryPaymentReconciliationLock(ctx))
	register(database.TryScheduledSummaryLock(ctx, 501))
	if locks.pool.Stat().AcquiredConns() != 5 || len(locks.legacySlots) != 4 {
		t.Fatal("all market and legacy reservations were not isolated")
	}
	if queryPool.Stat().AcquiredConns() != 0 {
		t.Fatal("task lock reserved the main query connection")
	}
	queryCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var one int
	if err := database.db.QueryRow(queryCtx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("task cannot execute queries with its lock held: %d/%v", one, err)
	}
	start := time.Now()
	if next, acquired, err := database.TryMarketTaskLock(ctx, "other-task-at-capacity"); err != nil || acquired || next != nil {
		t.Fatalf("capacity should skip, not block: %t/%v", acquired, err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("full lock pool waited instead of skipping the tick")
	}
	if next, acquired, err := database.TryScheduledSummaryLock(ctx, 502); err != nil || acquired || next != nil {
		t.Fatalf("full legacy lane should skip without using main capacity: %t/%v", acquired, err)
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if err := lock.Unlock(cancelled); err != nil {
		t.Fatalf("cancelled task did not release lock: %v", err)
	}
	if err := lock.Unlock(ctx); err != nil || len(locks.slots) != 0 {
		t.Fatalf("unlock is not idempotent: %v, slots=%d", err, len(locks.slots))
	}
	next, acquired, err := database.TryMarketTaskLock(ctx, "other-task-after-release")
	if err != nil || !acquired {
		t.Fatalf("released capacity cannot be reused: %t/%v", acquired, err)
	}
	if err := next.Unlock(ctx); err != nil {
		t.Fatal(err)
	}
	for _, lock := range legacyLocks {
		if err := lock.Unlock(cancelled); err != nil {
			t.Fatalf("cancelled legacy task did not release: %v", err)
		}
		if err := lock.Unlock(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(locks.legacySlots) != 0 {
		t.Fatal("legacy execution slots leaked after cancellation")
	}
}

func TestPostgresLegacyTaskLocksKeepKeysAndSummaryNamespaceAcrossStores(t *testing.T) {
	_, queryPool := openIntegrationStore(t)
	ctx := context.Background()
	firstPool, err := newMarketTaskLockPool(ctx, queryPool.Config(), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(firstPool.Close)
	secondPool, err := newMarketTaskLockPool(ctx, queryPool.Config(), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(secondPool.Close)
	first, second := &Store{taskLocks: firstPool}, &Store{taskLocks: secondPool}
	for _, key := range []int64{7_220_026_010, 7_220_026_011, 7_220_026_012} {
		var lock interface{ Unlock(context.Context) error }
		var acquired bool
		var err error
		switch key {
		case 7_220_026_010:
			lock, acquired, err = first.TryMonitorExecutionLock(ctx)
		case 7_220_026_011:
			lock, acquired, err = first.TryPaymentReconciliationLock(ctx)
		case 7_220_026_012:
			lock, acquired, err = first.TryBotPollingLock(ctx)
		}
		if err != nil || !acquired {
			t.Fatal(acquired, err)
		}
		t.Cleanup(func() { _ = lock.Unlock(ctx) })
		if other, acquired, err := second.tryLegacyTaskLock(ctx, key); err != nil || acquired || other != nil {
			t.Fatalf("original single-key execution lock changed: %t/%v", acquired, err)
		}
	}
	summary, acquired, err := first.TryScheduledSummaryLock(ctx, 501)
	if err != nil || !acquired {
		t.Fatal(acquired, err)
	}
	t.Cleanup(func() { _ = summary.Unlock(ctx) })
	if other, acquired, err := second.tryLegacyTaskLock(ctx, summaryLockNamespace, int32(501)); err != nil || acquired || other != nil {
		t.Fatalf("summary two-int namespace changed: %t/%v", acquired, err)
	}
	otherSummary, acquired, err := second.TryScheduledSummaryLock(ctx, 502)
	if err != nil || !acquired {
		t.Fatalf("per-subscription lock scope was widened: %t/%v", acquired, err)
	}
	if err := otherSummary.Unlock(ctx); err != nil {
		t.Fatal(err)
	}
	if len(secondPool.legacySlots) != 0 {
		t.Fatal("contended cross-store locks leaked capacity")
	}
}

func TestPostgresMarketTaskLockContentionAndBrokenConnectionReleaseSlots(t *testing.T) {
	_, queryPool := openIntegrationStore(t)
	ctx := context.Background()
	locks, err := newMarketTaskLockPool(ctx, queryPool.Config(), 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(locks.Close)
	database := &Store{db: queryPool, pool: queryPool, taskLocks: locks}
	lock, acquired, err := database.TryMarketTaskLock(ctx, "shared-advisory-key")
	if err != nil || !acquired {
		t.Fatal(acquired, err)
	}
	t.Cleanup(func() { _ = lock.Unlock(ctx) })
	if other, acquired, err := database.TryMarketTaskLock(ctx, "shared-advisory-key"); err != nil || acquired || other != nil || len(locks.slots) != 1 {
		t.Fatalf("contended key leaked slot or acquired twice: %t/%v, slots=%d", acquired, err, len(locks.slots))
	}
	if err := lock.conn.Conn().Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := lock.Unlock(ctx); err == nil || len(locks.slots) != 0 {
		t.Fatalf("broken unlock failed to discard reservation: %v, slots=%d", err, len(locks.slots))
	}
	lock, acquired, err = database.TryMarketTaskLock(ctx, "shared-advisory-key")
	if err != nil || !acquired {
		t.Fatalf("broken connection retained the database advisory lock: %t/%v", acquired, err)
	}
	if err := lock.Unlock(ctx); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := database.TryMarketTaskLock(cancelled, "cancelled-before-lock"); !errors.Is(err, context.Canceled) || len(locks.slots) != 0 {
		t.Fatalf("cancelled acquire leaked a reservation: %v", err)
	}
}
