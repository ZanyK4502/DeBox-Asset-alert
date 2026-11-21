package store

import "context"

const monitorExecutionLockKey int64 = 7_220_026_010

type MonitorLock struct{ lock *MarketTaskLock }

func (s *Store) TryMonitorExecutionLock(ctx context.Context) (*MonitorLock, bool, error) {
	lock, acquired, err := s.tryLegacyTaskLock(ctx, monitorExecutionLockKey)
	if err != nil || !acquired {
		return nil, acquired, err
	}
	return &MonitorLock{lock: lock}, true, nil
}

func (lock *MonitorLock) Unlock(ctx context.Context) error {
	if lock == nil {
		return nil
	}
	return lock.lock.Unlock(ctx)
}
