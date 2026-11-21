package store

import "context"

const botPollingLockKey int64 = 7_220_026_012

type BotPollingLock struct{ lock *MarketTaskLock }

func (s *Store) TryBotPollingLock(ctx context.Context) (*BotPollingLock, bool, error) {
	lock, acquired, err := s.tryLegacyTaskLock(ctx, botPollingLockKey)
	if err != nil || !acquired {
		return nil, acquired, err
	}
	return &BotPollingLock{lock: lock}, true, nil
}

func (lock *BotPollingLock) Unlock(ctx context.Context) error {
	if lock == nil {
		return nil
	}
	return lock.lock.Unlock(ctx)
}
