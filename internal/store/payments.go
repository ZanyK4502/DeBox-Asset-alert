package store

import "context"

const paymentReconciliationLockKey int64 = 7_220_026_011

type PaymentReconciliationLock struct{ lock *MarketTaskLock }

func (s *Store) TryPaymentReconciliationLock(ctx context.Context) (*PaymentReconciliationLock, bool, error) {
	lock, acquired, err := s.tryLegacyTaskLock(ctx, paymentReconciliationLockKey)
	if err != nil || !acquired {
		return nil, acquired, err
	}
	return &PaymentReconciliationLock{lock: lock}, true, nil
}

func (lock *PaymentReconciliationLock) Unlock(ctx context.Context) error {
	if lock == nil {
		return nil
	}
	return lock.lock.Unlock(ctx)
}
