package store

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const paymentReconciliationLockKey int64 = 7_220_026_011

type PaymentReconciliationLock struct {
	conn *pgxpool.Conn
	once sync.Once
}

func (s *Store) TryPaymentReconciliationLock(
	ctx context.Context,
) (*PaymentReconciliationLock, bool, error) {
	if s.pool == nil {
		return nil, false, ErrPoolRequired
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquire payment reconciliation connection: %w", err)
	}
	var acquired bool
	if err := conn.QueryRow(
		ctx,
		"SELECT pg_try_advisory_lock($1)",
		paymentReconciliationLockKey,
	).Scan(&acquired); err != nil {
		conn.Release()
		return nil, false, fmt.Errorf("try payment reconciliation lock: %w", err)
	}
	if !acquired {
		conn.Release()
		return nil, false, nil
	}
	return &PaymentReconciliationLock{conn: conn}, true, nil
}

func (lock *PaymentReconciliationLock) Unlock(ctx context.Context) (err error) {
	lock.once.Do(func() {
		baseContext := context.Background()
		if ctx != nil {
			baseContext = context.WithoutCancel(ctx)
		}
		unlockContext, cancel := context.WithTimeout(baseContext, 5*time.Second)
		defer cancel()

		var unlocked bool
		if scanErr := lock.conn.QueryRow(
			unlockContext,
			"SELECT pg_advisory_unlock($1)",
			paymentReconciliationLockKey,
		).Scan(&unlocked); scanErr != nil {
			rawConn := lock.conn.Hijack()
			closeContext, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer closeCancel()
			_ = rawConn.Close(closeContext)
			err = fmt.Errorf("unlock payment reconciliation: %w", scanErr)
			return
		}
		lock.conn.Release()
		if !unlocked {
			err = fmt.Errorf("unlock payment reconciliation: lock was not held")
		}
	})
	return err
}
