package store

import (
	"context"
	"fmt"
)

type QuotaPolicy struct {
	PlanCode               string
	WalletLimit            int
	RuleLimit              int
	GroupLimit             int
	MarketProjectLimit     int
	AllowedRuleTypes       []string
	AllowedMarketRuleTypes []string
	GroupNotification      bool
	StageNotifications     bool
	CombinationRules       bool
	MultiPoolMonitoring    bool
}

func (p QuotaPolicy) allowsRuleType(ruleType string) bool {
	for _, allowed := range p.AllowedRuleTypes {
		if allowed == ruleType {
			return true
		}
	}
	return false
}

func (p QuotaPolicy) allowsMarketRuleType(ruleType string) bool {
	for _, allowed := range p.AllowedMarketRuleTypes {
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
	if _, err := expireSubscriptions(ctx, db, deboxUserID); err != nil {
		return "", err
	}
	var planCode string
	if err := db.QueryRow(ctx, `
		SELECT COALESCE((
			SELECT plan_code
			FROM subscriptions
			WHERE debox_user_id = $1
			  AND status = 'active'
			  AND (is_permanent = 1 OR expires_at > NOW())
			ORDER BY CASE plan_code WHEN 'professional' THEN 2 WHEN 'standard' THEN 1 ELSE 0 END DESC,
			         is_permanent DESC,
			         expires_at DESC
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
		if _, err := expireSubscriptions(ctx, tx, deboxUserID); err != nil {
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
				  AND (is_permanent = 1 OR expires_at > NOW())
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
				WHERE debox_user_id = $1
				  AND plan_code <> 'free'
			)
		`, deboxUserID).Scan(&paidHistory); err != nil {
			return false, fmt.Errorf("check paid subscription history: %w", err)
		}
		if !paidHistory {
			return false, nil
		}

		if err := pauseExpiredUserResources(
			ctx,
			tx,
			deboxUserID,
			exceptRuleID,
		); err != nil {
			return false, err
		}
		return true, nil
	})
}

type expiredEntitlementUser struct {
	DeBoxUserID     string `db:"debox_user_id"`
	FreeWatchRuleID *int64 `db:"free_watch_rule_id"`
}

// ApplyExpiredEntitlementFallbacks provides a background safety net so paid
// capabilities stop at expiry even when the user never opens the H5 or bot.
func (s *Store) ApplyExpiredEntitlementFallbacks(ctx context.Context) (int64, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (int64, error) {
		if _, err := expireSubscriptions(ctx, tx, ""); err != nil {
			return 0, err
		}
		users, err := collectMany[expiredEntitlementUser](ctx, tx, `
			SELECT paid.debox_user_id, up.free_watch_rule_id
			FROM (
				SELECT DISTINCT debox_user_id
				FROM subscriptions
				WHERE plan_code <> 'free'
			) paid
			LEFT JOIN user_preferences up
			  ON up.debox_user_id = paid.debox_user_id
			WHERE NOT EXISTS (
				SELECT 1
				FROM subscriptions active
				WHERE active.debox_user_id = paid.debox_user_id
				  AND active.status = 'active'
				  AND (active.is_permanent = 1 OR active.expires_at > NOW())
				  AND active.plan_code <> 'free'
			)
			  AND (
				EXISTS (
					SELECT 1 FROM watch_rules wr
					WHERE wr.debox_user_id = paid.debox_user_id
					  AND wr.enabled = 1 AND wr.run_status = 'active'
					  AND (up.free_watch_rule_id IS NULL OR wr.id <> up.free_watch_rule_id)
				)
				OR EXISTS (
					SELECT 1 FROM combination_rules cr
					WHERE cr.debox_user_id = paid.debox_user_id
					  AND cr.enabled = 1 AND cr.run_status = 'active'
				)
				OR EXISTS (
					SELECT 1 FROM market_rules mr
					WHERE mr.debox_user_id = paid.debox_user_id
					  AND mr.enabled = 1 AND mr.run_status = 'active'
				)
				OR EXISTS (
					SELECT 1 FROM market_combination_rules mcr
					WHERE mcr.debox_user_id = paid.debox_user_id
					  AND mcr.enabled = 1 AND mcr.run_status = 'active'
				)
				OR EXISTS (
					SELECT 1 FROM market_projects mp
					WHERE mp.debox_user_id = paid.debox_user_id
					  AND mp.status = 'active'
				)
			  )
			ORDER BY paid.debox_user_id
		`)
		if err != nil {
			return 0, fmt.Errorf("list expired entitlement users: %w", err)
		}
		var reconciled int64
		for _, user := range users {
			if err := lockUser(ctx, tx, user.DeBoxUserID); err != nil {
				return reconciled, err
			}
			planCode, err := effectivePlanCode(ctx, tx, user.DeBoxUserID)
			if err != nil {
				return reconciled, err
			}
			if planCode != "free" {
				continue
			}
			if err := pauseExpiredUserResources(
				ctx,
				tx,
				user.DeBoxUserID,
				user.FreeWatchRuleID,
			); err != nil {
				return reconciled, err
			}
			reconciled++
		}
		return reconciled, nil
	})
}

func pauseExpiredUserResources(
	ctx context.Context,
	tx DBTX,
	deboxUserID string,
	exceptRuleID *int64,
) error {
	query := `
		UPDATE watch_rules
		SET run_status = 'paused', pause_reason = 'subscription_expired'
		WHERE debox_user_id = $1 AND enabled = 1 AND run_status = 'active'
	`
	args := []any{deboxUserID}
	if exceptRuleID != nil {
		query += " AND id <> $2"
		args = append(args, *exceptRuleID)
	}
	if _, err := tx.Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("pause expired subscription rules: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE combination_rules
		SET run_status = 'paused', pause_reason = 'subscription_expired'
		WHERE debox_user_id = $1 AND enabled = 1 AND run_status = 'active'
	`, deboxUserID); err != nil {
		return fmt.Errorf("pause expired combination rules: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE market_rule_events mre
		SET notification_status = 'skipped',
		    notification_error = 'subscription expired before delivery'
		FROM market_rules mr
		WHERE mr.id = mre.market_rule_id
		  AND mr.debox_user_id = $1
		  AND mre.notification_status IN (
			'pending', 'sending', 'failed', 'staged', 'combined'
		  )
	`, deboxUserID); err != nil {
		return fmt.Errorf("skip expired market rule deliveries: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE market_stage_windows msw
		SET notification_status = 'skipped',
		    notification_error = 'subscription expired before delivery',
		    closed_at = COALESCE(closed_at, NOW()),
		    updated_at = NOW()
		FROM market_rules mr
		WHERE mr.id = msw.market_rule_id
		  AND mr.debox_user_id = $1
		  AND msw.notification_status NOT IN ('sent', 'skipped')
	`, deboxUserID); err != nil {
		return fmt.Errorf("close expired market stage windows: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE market_combination_windows mcw
		SET notification_status = 'skipped',
		    notification_error = 'subscription expired before delivery',
		    closed_at = COALESCE(closed_at, NOW()),
		    updated_at = NOW()
		FROM market_combination_rules mcr
		WHERE mcr.id = mcw.market_combination_rule_id
		  AND mcr.debox_user_id = $1
		  AND mcw.notification_status NOT IN ('sent', 'skipped')
	`, deboxUserID); err != nil {
		return fmt.Errorf("close expired market combination windows: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE market_rules
		SET run_status = 'paused',
		    pause_reason = 'subscription_expired',
		    updated_at = NOW()
		WHERE debox_user_id = $1 AND enabled = 1 AND run_status = 'active'
	`, deboxUserID); err != nil {
		return fmt.Errorf("pause expired market rules: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE market_combination_rules
		SET run_status = 'paused',
		    pause_reason = 'subscription_expired',
		    updated_at = NOW()
		WHERE debox_user_id = $1 AND enabled = 1 AND run_status = 'active'
	`, deboxUserID); err != nil {
		return fmt.Errorf("pause expired market combinations: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE market_projects
		SET status = 'paused',
		    pause_reason = 'subscription_expired',
		    frozen_at = COALESCE(frozen_at, NOW()),
		    updated_at = NOW()
		WHERE debox_user_id = $1 AND status = 'active'
	`, deboxUserID); err != nil {
		return fmt.Errorf("pause expired market projects: %w", err)
	}
	return nil
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
			    pause_reason = '',
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
		) + (
			SELECT COUNT(*)
			FROM market_rules
			WHERE debox_user_id = $1 AND enabled = 1 AND run_status = 'active'
		) + (
			SELECT COUNT(*)
			FROM market_combination_rules
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
