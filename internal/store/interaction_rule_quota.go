package store

import (
	"context"
	"fmt"
)

const addressInteractionRuleType = "address_interaction"

func (s *Store) CountUserInteractionRules(ctx context.Context, userID string) (int64, error) {
	return countActiveInteractionRules(ctx, s.db, userID)
}

// Wallet combination members are watch_rules too. Do not join their parents or
// market combination references: each active physical rule consumes one slot.
func countActiveInteractionRules(ctx context.Context, db DBTX, userID string) (int64, error) {
	count, err := queryCount(ctx, db, `SELECT COUNT(*) FROM watch_rules
 WHERE debox_user_id=$1 AND enabled=1 AND run_status='active'
 AND rule_type='address_interaction'`, userID)
	if err != nil {
		return 0, fmt.Errorf("count active interaction rules: %w", err)
	}
	return count, nil
}

// Call under the existing user entitlement lock, before any candidate writes.
// A zero limit denies activation; it never grants unlimited interaction reads.
func requireInteractionRuleQuota(ctx context.Context, db DBTX, userID string, additional int64, limit int) error {
	if additional == 0 {
		return nil
	}
	if limit < 1 || additional > int64(limit) {
		return ErrInteractionRuleLimitReached
	}
	active, err := countActiveInteractionRules(ctx, db, userID)
	if err != nil {
		return err
	}
	if active+additional > int64(limit) {
		return ErrInteractionRuleLimitReached
	}
	return nil
}

func restoredInteractionRuleCost(rule WatchRule) int64 {
	if rule.RuleType == addressInteractionRuleType && rule.Enabled == 1 && rule.RunStatus != "active" {
		return 1
	}
	return 0
}
