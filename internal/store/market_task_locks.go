package store

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	marketTaskLockAcquireTimeout = time.Second
	marketTaskSlotWaitTimeout    = 30 * time.Second
	marketTaskSlotWaiters        = 32
	legacyTaskLockConnections    = 4
)

// ErrMarketTaskCapacityDeferred distinguishes exhausted local execution
// capacity from another instance already holding the same advisory key.
var ErrMarketTaskCapacityDeferred = errors.New("market task local capacity deferred")

// Session advisory locks reserve connections until a task has finished. Keep
// those reservations out of the query pool used by the task itself.
type marketTaskLockPool struct {
	pool        *pgxpool.Pool
	slots       chan struct{}
	waiters     chan struct{}
	legacySlots chan struct{}
}

func newMarketTaskLockPool(ctx context.Context, source *pgxpool.Config, maxConns int32) (*marketTaskLockPool, error) {
	if source == nil || maxConns < 1 || maxConns > 16 {
		return nil, errors.New("market task lock connections must be between 1 and 16")
	}
	cfg := source.Copy()
	cfg.MaxConns, cfg.MinConns, cfg.MinIdleConns = maxConns+legacyTaskLockConnections, 0, 0
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = make(map[string]string)
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = "debox_market_task_locks"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create market task lock pool: %w", err)
	}
	return &marketTaskLockPool{pool: pool, slots: make(chan struct{}, maxConns), waiters: make(chan struct{}, marketTaskSlotWaiters), legacySlots: make(chan struct{}, legacyTaskLockConnections)}, nil
}

func (p *marketTaskLockPool) Close() {
	if p != nil && p.pool != nil {
		p.pool.Close()
	}
}

// MarketTaskLock uses the original connection-scoped advisory key. Its bounded
// dedicated pool cannot consume the connections needed to execute task queries.
type MarketTaskLock struct {
	conn        *pgxpool.Conn
	key         int64
	unlockQuery string
	unlockArgs  []any
	releaseSlot func()
	once        sync.Once
}

func (s *Store) TryMarketTaskLock(ctx context.Context, task string) (*MarketTaskLock, bool, error) {
	return s.marketTaskLock(ctx, task, 0)
}

// AcquireMarketTaskLock waits only for this process's bounded market execution
// capacity. The database advisory key is still a try operation: another
// instance running the same task is not retried or queued behind.
// Existing synchronous runners supply at most one waiter per task.
func (s *Store) AcquireMarketTaskLock(ctx context.Context, task string) (*MarketTaskLock, bool, error) {
	return s.marketTaskLock(ctx, task, marketTaskSlotWaitTimeout)
}

func (s *Store) marketTaskLock(ctx context.Context, task string, localWait time.Duration) (*MarketTaskLock, bool, error) {
	task = strings.TrimSpace(task)
	if task == "" {
		return nil, false, errors.New("market task name is required")
	}
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte("debox-market-collector:" + task))
	key := int64(hasher.Sum64())
	if s.taskLocks == nil || s.taskLocks.pool == nil {
		return nil, false, ErrPoolRequired
	}
	var lock *MarketTaskLock
	var acquired bool
	var err error
	if localWait == 0 {
		lock, acquired, err = s.trySessionTaskLock(ctx, s.taskLocks.slots, "SELECT pg_try_advisory_lock($1)", "SELECT pg_advisory_unlock($1)", key)
	} else {
		var releaseSlot func()
		releaseSlot, err = s.taskLocks.waitForMarketSlot(ctx, localWait)
		if err != nil {
			return nil, false, err
		}
		// The local wait has ended. Start the original database timeout from
		// the caller's context, not the nearly-expired local wait context.
		lock, acquired, err = s.sessionTaskLockWithSlot(ctx, releaseSlot, "SELECT pg_try_advisory_lock($1)", "SELECT pg_advisory_unlock($1)", key)
	}
	if lock != nil {
		lock.key = key
	}
	return lock, acquired, err
}

func (p *marketTaskLockPool) waitForMarketSlot(ctx context.Context, wait time.Duration) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	release := func() { <-p.slots }
	select {
	case p.slots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		return release, nil
	default:
	}
	select {
	case p.waiters <- struct{}{}:
		defer func() { <-p.waiters }()
	default:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: wait queue is full", ErrMarketTaskCapacityDeferred)
	}
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	select {
	case p.slots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		if waitCtx.Err() != nil {
			release()
			return nil, fmt.Errorf("%w: local wait exceeded %s", ErrMarketTaskCapacityDeferred, wait)
		}
		return release, nil
	case <-waitCtx.Done():
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: local wait exceeded %s", ErrMarketTaskCapacityDeferred, wait)
	}
}

// Legacy execution locks reserve separate capacity from market jobs. Preserve
// their exact key(s), including the two-int per-subscription summary namespace.
func (s *Store) tryLegacyTaskLock(ctx context.Context, keys ...any) (*MarketTaskLock, bool, error) {
	if s.taskLocks == nil {
		return nil, false, ErrPoolRequired
	}
	query, unlock := "SELECT pg_try_advisory_lock($1)", "SELECT pg_advisory_unlock($1)"
	if len(keys) == 2 {
		query, unlock = "SELECT pg_try_advisory_lock($1, $2)", "SELECT pg_advisory_unlock($1, $2)"
	} else if len(keys) != 1 {
		return nil, false, errors.New("invalid execution advisory key")
	}
	return s.trySessionTaskLock(ctx, s.taskLocks.legacySlots, query, unlock, keys...)
}

func (s *Store) trySessionTaskLock(ctx context.Context, slots chan struct{}, query, unlock string, keys ...any) (*MarketTaskLock, bool, error) {
	if s.taskLocks == nil || s.taskLocks.pool == nil {
		return nil, false, ErrPoolRequired
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	// This is a try operation: capacity pressure skips a tick instead of
	// accumulating goroutines waiting for a reserved session connection.
	select {
	case slots <- struct{}{}:
	default:
		return nil, false, nil
	}
	releaseSlot := func() { <-slots }
	return s.sessionTaskLockWithSlot(ctx, releaseSlot, query, unlock, keys...)
}

// The caller has reserved one local slot. All error paths release it; an
// acquired advisory lock keeps it until Unlock finishes.
func (s *Store) sessionTaskLockWithSlot(ctx context.Context, releaseSlot func(), query, unlock string, keys ...any) (*MarketTaskLock, bool, error) {
	if err := ctx.Err(); err != nil {
		releaseSlot()
		return nil, false, err
	}
	acquireCtx, cancel := context.WithTimeout(ctx, marketTaskLockAcquireTimeout)
	defer cancel()
	conn, err := s.taskLocks.pool.Acquire(acquireCtx)
	if err != nil {
		releaseSlot()
		return nil, false, fmt.Errorf("acquire market task connection: %w", err)
	}
	var acquired bool
	if err := conn.QueryRow(acquireCtx, query, keys...).Scan(&acquired); err != nil {
		// An interrupted reply cannot prove that the server did not take the
		// session lock. Never return this connection to the reusable pool.
		discardMarketTaskConnection(conn)
		releaseSlot()
		return nil, false, fmt.Errorf("try market task lock: %w", err)
	}
	if !acquired {
		conn.Release()
		releaseSlot()
		return nil, false, nil
	}
	return &MarketTaskLock{conn: conn, unlockQuery: unlock, unlockArgs: append([]any(nil), keys...), releaseSlot: releaseSlot}, true, nil
}

func discardMarketTaskConnection(conn *pgxpool.Conn) {
	raw := conn.Hijack()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = raw.Close(ctx)
}

func (lock *MarketTaskLock) Unlock(ctx context.Context) (err error) {
	if lock == nil {
		return nil
	}
	lock.once.Do(func() {
		if lock.releaseSlot != nil {
			defer lock.releaseSlot()
		}
		base := context.Background()
		if ctx != nil {
			base = context.WithoutCancel(ctx)
		}
		unlockCtx, cancel := context.WithTimeout(base, 5*time.Second)
		defer cancel()
		var unlocked bool
		if scanErr := lock.conn.QueryRow(unlockCtx, lock.unlockQuery, lock.unlockArgs...).Scan(&unlocked); scanErr != nil {
			discardMarketTaskConnection(lock.conn)
			err = fmt.Errorf("unlock market task: %w", scanErr)
			return
		}
		lock.conn.Release()
		if !unlocked {
			err = errors.New("unlock market task: lock was not held")
		}
	})
	return err
}
