package store

import (
	"context"
	"fmt"
	"time"
)

// DeferMarketRuleEvaluation records an inspection attempt and a bounded retry.
// No event cursor, condition, cooldown or user configuration is changed.
func (s *Store) DeferMarketRuleEvaluation(ctx context.Context, ruleID int64, delay time.Duration) error {
	if ruleID < 1 {
		return ErrInvalidMarketRule
	}
	if delay <= 0 || delay > time.Minute {
		delay = time.Minute
	}
	seconds := int64((delay + time.Second - 1) / time.Second)
	tag, err := s.db.Exec(ctx, `
		UPDATE market_rules SET last_evaluated_at=NOW(),
			next_evaluation_at=NOW()+($2::bigint*INTERVAL '1 second')
		WHERE id=$1
	`, ruleID, seconds)
	if err != nil {
		return fmt.Errorf("defer incomplete market rule inspection: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
