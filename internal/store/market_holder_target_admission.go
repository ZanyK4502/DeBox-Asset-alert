package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	MarketHolderTargetLimit = 6
	noHolderTargetCheck     = -1
)

var ErrMarketHolderTargetsReached = errors.New("active market holder target limit reached")

const holderRuleTypesSQL = `'market_holder_increase','market_holder_decrease','market_holder_rank_entered','market_holder_rank_exited'`

func isMarketHolderRule(ruleType string) bool {
	switch ruleType {
	case "market_holder_increase", "market_holder_decrease", "market_holder_rank_entered", "market_holder_rank_exited":
		return true
	default:
		return false
	}
}

func hasActiveProjectHolderRules(ctx context.Context, db DBTX, projectID int64) (bool, error) {
	var hasRules bool
	err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM market_rules
 WHERE market_project_id=$1 AND enabled=1 AND run_status='active' AND rule_type IN(`+holderRuleTypesSQL+`))`, projectID).Scan(&hasRules)
	if err != nil {
		return false, fmt.Errorf("check project holder rules: %w", err)
	}
	return hasRules, nil
}

func (s *Store) beginProjectHolderTargetCheck(ctx context.Context, tx DBTX, projectID int64) (int64, error) {
	hasRules, err := hasActiveProjectHolderRules(ctx, tx, projectID)
	if err != nil {
		return 0, err
	}
	return s.beginMarketHolderTargetCheck(ctx, tx, hasRules)
}

// Admission is evaluated inside the application transaction after the owner
// lock. Separate owners can share one target and must serialize additions to
// the global set. Hold the advisory lock through commit.
func (s *Store) beginMarketHolderTargetCheck(ctx context.Context, tx DBTX, needed bool) (int64, error) {
	if !needed {
		return noHolderTargetCheck, nil
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := tx.Exec(bounded, `SELECT pg_advisory_xact_lock(1684370038,2)`); err != nil {
		return 0, fmt.Errorf("lock market holder admission: %w", err)
	}
	return s.countMarketHolderTargets(bounded, tx)
}

func (s *Store) checkMarketHolderTargetLimit(ctx context.Context, tx DBTX, before int64) error {
	if before == noHolderTargetCheck {
		return nil
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	after, err := s.countMarketHolderTargets(bounded, tx)
	if err != nil {
		return err
	}
	// Do not interrupt pre-existing demand. A target still demanded by another
	// active owner stays allocated, but creating an additional target needs a
	// slot. Pausing, deleting or losing the last eligible rule frees its slot.
	if after > MarketHolderTargetLimit && after > before {
		return ErrMarketHolderTargetsReached
	}
	return nil
}

func (s *Store) countMarketHolderTargets(ctx context.Context, db DBTX) (int64, error) {
	chainIDs := s.marketMonitoringChainIDs()
	if chainIDs == nil {
		chainIDs = []int64{1, 10, 56, 137, 8453, 42161}
	}
	var count int64
	err := db.QueryRow(ctx, `WITH demand AS NOT MATERIALIZED (`+holderDemandSourceSQL()+`)
 SELECT COUNT(DISTINCT (chain_id,lower(token_address))) FROM demand WHERE chain_id=ANY($1::bigint[])`, chainIDs).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active market holder targets: %w", err)
	}
	return count, nil
}

func (s *Store) beginSubscriptionHolderTargetCheck(ctx context.Context, tx DBTX, owner, plan string) (int64, error) {
	if plan != "professional" {
		return noHolderTargetCheck, nil
	}
	var hasRules bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM market_rules
 WHERE debox_user_id=$1 AND enabled=1 AND run_status='active' AND rule_type IN(`+holderRuleTypesSQL+`))`, owner).Scan(&hasRules)
	if err != nil {
		return 0, fmt.Errorf("check renewing owner's holder rules: %w", err)
	}
	return s.beginMarketHolderTargetCheck(ctx, tx, hasRules)
}

// Existing renewal terms use the transaction clock. If a renewal waited for
// locks past expiry, another owner may have already taken its former holder
// slot. Settle the payment on its original terms and defer the old holder
// demand for the ordinary entitlement restore instead of over-admitting it.
func (s *Store) deferSubscriptionHolderTargets(ctx context.Context, tx DBTX, owner string, before int64) error {
	if err := s.checkMarketHolderTargetLimit(ctx, tx, before); !errors.Is(err, ErrMarketHolderTargetsReached) {
		return err
	}
	rows, err := tx.Query(ctx, `UPDATE market_combination_rules parent
 SET run_status='paused',pause_reason='subscription_expired',updated_at=NOW()
 WHERE parent.debox_user_id=$1 AND parent.enabled=1 AND parent.run_status='active' AND EXISTS(
 SELECT 1 FROM market_combination_members member JOIN market_rules mr ON mr.id=member.market_rule_id
 WHERE member.market_combination_rule_id=parent.id AND member.source_type='market' AND mr.debox_user_id=parent.debox_user_id
 AND mr.enabled=1 AND mr.run_status='active' AND mr.rule_type IN(`+holderRuleTypesSQL+`))
 RETURNING parent.id`, owner)
	if err != nil {
		return fmt.Errorf("defer renewing owner's holder combinations: %w", err)
	}
	combinationIDs, err := collectInt64Rows(rows)
	if err != nil {
		return err
	}
	for _, id := range combinationIDs {
		if err := advanceMarketCombinationNotificationFence(ctx, tx, id); err != nil {
			return err
		}
	}
	rows, err = tx.Query(ctx, `UPDATE market_rules
 SET run_status='paused',pause_reason='subscription_expired',updated_at=NOW()
 WHERE debox_user_id=$1 AND enabled=1 AND run_status='active' AND rule_scope='standalone'
 AND rule_type IN(`+holderRuleTypesSQL+`) RETURNING id`, owner)
	if err != nil {
		return fmt.Errorf("defer renewing owner's holder rules: %w", err)
	}
	ruleIDs, err := collectInt64Rows(rows)
	if err != nil {
		return err
	}
	for _, id := range ruleIDs {
		if err := advanceMarketRuleNotificationFence(ctx, tx, id); err != nil {
			return err
		}
	}
	return s.checkMarketHolderTargetLimit(ctx, tx, before)
}
