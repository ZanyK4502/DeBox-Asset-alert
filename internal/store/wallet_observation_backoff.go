package store

import (
	"context"
	"fmt"
	"time"
)

// DeferWalletObservation affects inspection scheduling only. It must only be
// used after a failure proven to have rolled back, never a lost commit result.
func (s *Store) DeferWalletObservation(ctx context.Context, ruleID int64, delay time.Duration) error {
	if ruleID < 1 {
		return ErrNotFound
	}
	if delay <= 0 || delay > time.Minute {
		delay = time.Minute
	}
	seconds := int64((delay + time.Second - 1) / time.Second)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tag, err := s.db.Exec(ctx, `UPDATE watch_rules SET next_observation_at=NOW()+($2::bigint*INTERVAL '1 second') WHERE id=$1`, ruleID, seconds)
	if err != nil {
		return fmt.Errorf("defer wallet observation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
