package store

import (
	"context"
	"fmt"
)

type QuotaPolicy struct {
	PlanCode          string
	WalletLimit       int
	RuleLimit         int
	GroupLimit        int
	AllowedRuleTypes  []string
	GroupNotification bool
	CombinationRules  bool
}

func (p QuotaPolicy) allowsRuleType(ruleType string) bool {
	for _, allowed := range p.AllowedRuleTypes {
		if allowed == ruleType {
			return true
		}
	}
	return false
}

func lockUser(ctx context.Context, db DBTX, deboxUserID string) error {
	if _, err := db.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", deboxUserID); err != nil {
		return fmt.Errorf("lock user entitlement: %w", err)
	}
	return nil
}

func effectivePlanCode(ctx context.Context, db DBTX, deboxUserID string) (string, error) {
	if _, err := db.Exec(ctx, `
		UPDATE subscriptions
		SET status = 'expired'
		WHERE debox_user_id = $1 AND status = 'active' AND expires_at < NOW()
	`, deboxUserID); err != nil {
		return "", fmt.Errorf("expire user subscriptions: %w", err)
	}
	var planCode string
	if err := db.QueryRow(ctx, `
		SELECT COALESCE((
			SELECT plan_code
			FROM subscriptions
			WHERE debox_user_id = $1
			  AND status = 'active'
			  AND expires_at > NOW()
			ORDER BY expires_at DESC
			LIMIT 1
		), 'free')
	`, deboxUserID).Scan(&planCode); err != nil {
		return "", fmt.Errorf("select effective plan: %w", err)
	}
	return planCode, nil
}

func requirePolicyPlan(
	ctx context.Context,
	db DBTX,
	deboxUserID string,
	policy QuotaPolicy,
) error {
	planCode, err := effectivePlanCode(ctx, db, deboxUserID)
	if err != nil {
		return err
	}
	if planCode != policy.PlanCode {
		return ErrSubscriptionChanged
	}
	return nil
}

func (s *Store) ApplyPaidExpiryFallback(
	ctx context.Context,
	deboxUserID string,
	exceptRuleID *int64,
) (bool, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (bool, error) {
		if err := lockUser(ctx, tx, deboxUserID); err != nil {
			return false, err
		}
		planCode, err := effectivePlanCode(ctx, tx, deboxUserID)
		if err != nil {
			return false, err
		}
		if planCode != "free" {
			return false, nil
		}
		var activeSubscription bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM subscriptions
				WHERE debox_user_id = $1
				  AND status = 'active'
				  AND expires_at > NOW()
			)
		`, deboxUserID).Scan(&activeSubscription); err != nil {
			return false, fmt.Errorf("check active subscription: %w", err)
		}
		if activeSubscription {
			return false, nil
		}
		var paidHistory bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM subscriptions
				WHERE debox_user_id = $1 AND plan_code <> 'free'
			)
		`, deboxUserID).Scan(&paidHistory); err != nil {
			return false, fmt.Errorf("check paid subscription history: %w", err)
		}
		if !paidHistory {
			return false, nil
		}

		query := `
			UPDATE watch_rules
			SET run_status = 'paused'
			WHERE debox_user_id = $1 AND enabled = 1 AND run_status = 'active'
		`
		args := []any{deboxUserID}
		if exceptRuleID != nil {
			query += " AND id <> $2"
			args = append(args, *exceptRuleID)
		}
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			return false, fmt.Errorf("pause expired subscription rules: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE combination_rules
			SET run_status = 'paused'
			WHERE debox_user_id = $1 AND enabled = 1 AND run_status = 'active'
		`, deboxUserID); err != nil {
			return false, fmt.Errorf("pause expired combination rules: %w", err)
		}
		return true, nil
	})
}

func (s *Store) CreateWatchRuleWithinQuota(
	ctx context.Context,
	params CreateWatchRuleParams,
	policy QuotaPolicy,
) (WatchRule, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (WatchRule, error) {
		if normalizeRuleScope(params.RuleScope) == "combination" {
			return WatchRule{}, ErrCombinationMemberManaged
		}
		if err := lockUser(ctx, tx, params.DeBoxUserID); err != nil {
			return WatchRule{}, err
		}
		if err := requirePolicyPlan(ctx, tx, params.DeBoxUserID, policy); err != nil {
			return WatchRule{}, err
		}
		if !policy.allowsRuleType(params.RuleType) {
			return WatchRule{}, ErrRuleTypeDenied
		}
		if params.NotificationChatType == "group" && !policy.GroupNotification {
			return WatchRule{}, ErrGroupNotificationDenied
		}

		ruleCount, err := countActiveRuleSlots(ctx, tx, params.DeBoxUserID)
		if err != nil {
			return WatchRule{}, err
		}
		if ruleCount >= int64(policy.RuleLimit) {
			return WatchRule{}, ErrRuleLimitReached
		}

		var walletMonitored bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM watch_rules
				WHERE debox_user_id = $1
				  AND LOWER(wallet_address) = LOWER($2)
				  AND enabled = 1
				  AND run_status = 'active'
			)
		`, params.DeBoxUserID, params.WalletAddress).Scan(&walletMonitored); err != nil {
			return WatchRule{}, fmt.Errorf("check monitored wallet: %w", err)
		}
		if !walletMonitored {
			walletCount, err := queryCount(ctx, tx, `
				SELECT COUNT(DISTINCT LOWER(wallet_address))
				FROM watch_rules
				WHERE debox_user_id = $1 AND enabled = 1 AND run_status = 'active'
			`, params.DeBoxUserID)
			if err != nil {
				return WatchRule{}, fmt.Errorf("count active wallets: %w", err)
			}
			if walletCount >= int64(policy.WalletLimit) {
				return WatchRule{}, ErrWalletLimitReached
			}
		}
		rule, err := createWatchRule(ctx, tx, params)
		if err != nil {
			return WatchRule{}, err
		}
		if policy.PlanCode == "free" {
			if _, err := setFreeWatchRule(ctx, tx, params.DeBoxUserID, rule.ID); err != nil {
				return WatchRule{}, err
			}
		}
		return rule, nil
	})
}

func (s *Store) RestoreWatchRuleWithinQuota(
	ctx context.Context,
	ruleID int64,
	deboxUserID string,
	policy QuotaPolicy,
) (WatchRule, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (WatchRule, error) {
		if err := lockUser(ctx, tx, deboxUserID); err != nil {
			return WatchRule{}, err
		}
		if err := requirePolicyPlan(ctx, tx, deboxUserID, policy); err != nil {
			return WatchRule{}, err
		}
		rule, err := collectOne[WatchRule](ctx, tx, `
			SELECT `+watchRuleColumns+`
			FROM watch_rules
			WHERE id = $1 AND debox_user_id = $2
			FOR UPDATE
		`, ruleID, deboxUserID)
		if isNoRows(err) {
			return WatchRule{}, ErrNotFound
		}
		if err != nil {
			return WatchRule{}, fmt.Errorf("lock watch rule: %w", err)
		}
		if rule.Enabled != 1 {
			return WatchRule{}, ErrNotFound
		}
		if rule.RuleScope == "combination" {
			return WatchRule{}, ErrCombinationMemberManaged
		}
		if !policy.allowsRuleType(rule.RuleType) {
			return WatchRule{}, ErrRuleTypeDenied
		}
		if rule.NotificationChatType == "group" && !policy.GroupNotification {
			return WatchRule{}, ErrGroupNotificationDenied
		}
		if rule.RunStatus == "active" {
			return rule, nil
		}

		ruleCount, err := countActiveRuleSlots(ctx, tx, deboxUserID)
		if err != nil {
			return WatchRule{}, err
		}
		if ruleCount >= int64(policy.RuleLimit) {
			return WatchRule{}, ErrRuleLimitReached
		}
		var walletMonitored bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM watch_rules
				WHERE debox_user_id = $1
				  AND LOWER(wallet_address) = LOWER($2)
				  AND enabled = 1
				  AND run_status = 'active'
			)
		`, deboxUserID, rule.WalletAddress).Scan(&walletMonitored); err != nil {
			return WatchRule{}, fmt.Errorf("check monitored wallet: %w", err)
		}
		if !walletMonitored {
			walletCount, err := queryCount(ctx, tx, `
				SELECT COUNT(DISTINCT LOWER(wallet_address))
				FROM watch_rules
				WHERE debox_user_id = $1 AND enabled = 1 AND run_status = 'active'
			`, deboxUserID)
			if err != nil {
				return WatchRule{}, fmt.Errorf("count active wallets: %w", err)
			}
			if walletCount >= int64(policy.WalletLimit) {
				return WatchRule{}, ErrWalletLimitReached
			}
		}

		restored, err := collectOne[WatchRule](ctx, tx, `
			UPDATE watch_rules
			SET run_status = 'active',
			    aggregation_anchor_at = CASE
			      WHEN delivery_mode = 'stage' AND cycle_type = 'fixed' THEN NOW()
			      ELSE NULL
			    END
			WHERE id = $1 AND debox_user_id = $2 AND enabled = 1
			RETURNING `+watchRuleColumns,
			ruleID,
			deboxUserID,
		)
		if err != nil {
			return WatchRule{}, fmt.Errorf("restore watch rule: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE aggregation_windows
			SET closed_at = NOW(), updated_at = NOW()
			WHERE watch_rule_id = $1 AND closed_at IS NULL
		`, ruleID); err != nil {
			return WatchRule{}, fmt.Errorf("reset restored watch rule aggregation: %w", err)
		}
		return restored, nil
	})
}

func countActiveRuleSlots(ctx context.Context, db DBTX, deboxUserID string) (int64, error) {
	count, err := queryCount(ctx, db, `
		SELECT COUNT(*) + (
			SELECT COUNT(*)
			FROM combination_rules
			WHERE debox_user_id = $1 AND enabled = 1 AND run_status = 'active'
		)
		FROM watch_rules
		WHERE debox_user_id = $1 AND enabled = 1 AND run_status = 'active'
	`, deboxUserID)
	if err != nil {
		return 0, fmt.Errorf("count active rule slots: %w", err)
	}
	return count, nil
}

func (s *Store) CreateNotificationGroupWithinQuota(
	ctx context.Context,
	deboxUserID string,
	gid string,
	name string,
	policy QuotaPolicy,
) (NotificationGroup, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (NotificationGroup, error) {
		if err := lockUser(ctx, tx, deboxUserID); err != nil {
			return NotificationGroup{}, err
		}
		if err := requirePolicyPlan(ctx, tx, deboxUserID, policy); err != nil {
			return NotificationGroup{}, err
		}
		if !policy.GroupNotification {
			return NotificationGroup{}, ErrGroupNotificationDenied
		}

		var alreadyEnabled bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM notification_groups
				WHERE debox_user_id = $1 AND gid = $2 AND enabled = 1
			)
		`, deboxUserID, gid).Scan(&alreadyEnabled); err != nil {
			return NotificationGroup{}, fmt.Errorf("check notification group: %w", err)
		}
		if !alreadyEnabled {
			groupCount, err := queryCount(ctx, tx, `
				SELECT COUNT(*)
				FROM notification_groups
				WHERE debox_user_id = $1 AND enabled = 1
			`, deboxUserID)
			if err != nil {
				return NotificationGroup{}, fmt.Errorf("count notification groups: %w", err)
			}
			if groupCount >= int64(policy.GroupLimit) {
				return NotificationGroup{}, ErrGroupLimitReached
			}
		}
		return createNotificationGroup(ctx, tx, deboxUserID, gid, name)
	})
}
