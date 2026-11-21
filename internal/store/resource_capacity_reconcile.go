package store

import (
	"context"
	"errors"
)

// Automatic restoration is best-effort when no additional resource share is
// available. A rejected candidate is rolled back, retaining its old baseline
// and windows; a paid confirmation or permanent login still succeeds.
func (s *Store) tryResourceCapacityCandidate(ctx context.Context, tx DBTX, action func(DBTX) error) (bool, error) {
	if s.resourceCapacity == nil {
		return true, action(tx)
	}
	_, err := withTxValue(ctx, tx, func(candidate DBTX) (struct{}, error) {
		if err := action(candidate); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, s.checkResourceCapacity(ctx, candidate)
	})
	if errors.Is(err, ErrResourceCapacityUnavailable) || errors.Is(err, ErrResourceCapacityReached) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) tryMarketEntitlementCandidate(ctx context.Context, tx DBTX, holder, resources bool, action func(DBTX) error) (bool, error) {
	if !holder {
		if resources {
			return s.tryResourceCapacityCandidate(ctx, tx, action)
		}
		return true, action(tx)
	}
	before, err := s.beginMarketHolderTargetCheck(ctx, tx, true)
	if err != nil {
		return false, err
	}
	_, err = withTxValue(ctx, tx, func(candidate DBTX) (struct{}, error) {
		if err := action(candidate); err != nil {
			return struct{}{}, err
		}
		if err := s.checkMarketHolderTargetLimit(ctx, candidate, before); err != nil {
			return struct{}{}, err
		}
		if resources {
			return struct{}{}, s.checkResourceCapacity(ctx, candidate)
		}
		return struct{}{}, nil
	})
	if errors.Is(err, ErrMarketHolderTargetsReached) || errors.Is(err, ErrResourceCapacityUnavailable) || errors.Is(err, ErrResourceCapacityReached) {
		return false, nil
	}
	return err == nil, err
}
