package store

import (
	"context"
	"fmt"
	"strings"
)

type EntitlementReconcileResult struct {
	ProjectsRestored           int64 `json:"projects_restored"`
	WatchRulesRestored         int64 `json:"watch_rules_restored"`
	CombinationRulesRestored   int64 `json:"combination_rules_restored"`
	MarketRulesRestored        int64 `json:"market_rules_restored"`
	MarketCombinationsRestored int64 `json:"market_combinations_restored"`
}

func (s *Store) ReconcileUserEntitlements(
	ctx context.Context,
	deboxUserID string,
	policy QuotaPolicy,
) (EntitlementReconcileResult, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (EntitlementReconcileResult, error) {
		result := EntitlementReconcileResult{}
		if err := lockUser(ctx, tx, deboxUserID); err != nil {
			return result, err
		}
		if err := requirePolicyPlan(ctx, tx, deboxUserID, policy); err != nil {
			return result, err
		}
		if policy.PlanCode == "free" {
			return result, nil
		}

		activeSlots, err := countActiveRuleSlots(ctx, tx, deboxUserID)
		if err != nil {
			return result, err
		}
		walletRows, err := collectMany[walletValue](ctx, tx, `
			SELECT DISTINCT LOWER(wallet_address) AS wallet_address
			FROM watch_rules
			WHERE debox_user_id = $1 AND enabled = 1 AND run_status = 'active'
		`, deboxUserID)
		if err != nil {
			return result, fmt.Errorf("list active entitlement wallets: %w", err)
		}
		wallets := make(map[string]struct{}, len(walletRows))
		for _, wallet := range walletRows {
			wallets[strings.ToLower(wallet.WalletAddress)] = struct{}{}
		}

		watchRules, err := collectMany[WatchRule](ctx, tx, `
			SELECT `+watchRuleColumns+`
			FROM watch_rules
			WHERE debox_user_id = $1
			  AND enabled = 1
			  AND run_status = 'paused'
			  AND rule_scope = 'standalone'
			  AND pause_reason IN ('subscription_expired', 'free_plan')
			ORDER BY created_at, id
			FOR UPDATE
		`, deboxUserID)
		if err != nil {
			return result, fmt.Errorf("list restorable watch rules: %w", err)
		}
		for _, rule := range watchRules {
			if activeSlots >= int64(policy.RuleLimit) ||
				!policy.allowsRuleType(rule.RuleType) ||
				(rule.NotificationChatType == "group" && !policy.GroupNotification) {
				continue
			}
			wallet := strings.ToLower(rule.WalletAddress)
			if _, exists := wallets[wallet]; !exists && len(wallets) >= policy.WalletLimit {
				continue
			}
			if _, err := tx.Exec(ctx, `
				UPDATE watch_rules
				SET run_status = 'active',
				    pause_reason = '',
				    aggregation_anchor_at = CASE
				      WHEN delivery_mode = 'stage' AND cycle_type = 'fixed' THEN NOW()
				      ELSE NULL
				    END
				WHERE id = $1
			`, rule.ID); err != nil {
				return result, fmt.Errorf("restore watch rule: %w", err)
			}
			wallets[wallet] = struct{}{}
			activeSlots++
			result.WatchRulesRestored++
		}

		if policy.CombinationRules {
			combinations, err := collectMany[CombinationRule](ctx, tx, `
				SELECT `+combinationRuleColumns+`
				FROM combination_rules
				WHERE debox_user_id = $1
				  AND enabled = 1
				  AND run_status = 'paused'
				  AND pause_reason IN ('subscription_expired', 'free_plan')
				ORDER BY created_at, id
				FOR UPDATE
			`, deboxUserID)
			if err != nil {
				return result, fmt.Errorf("list restorable combination rules: %w", err)
			}
			for _, combination := range combinations {
				members, err := listCombinationMembers(ctx, tx, combination.ID)
				if err != nil {
					return result, err
				}
				cost := combinationRuleSlotCost(len(members))
				if len(members) < minimumCombinationMembers ||
					activeSlots+cost > int64(policy.RuleLimit) ||
					(combination.NotificationChatType == "group" && !policy.GroupNotification) {
					continue
				}
				candidateWallets := make(map[string]struct{}, len(wallets)+len(members))
				for wallet := range wallets {
					candidateWallets[wallet] = struct{}{}
				}
				allowed := true
				for _, member := range members {
					if !policy.allowsRuleType(member.Rule.RuleType) {
						allowed = false
						break
					}
					candidateWallets[strings.ToLower(member.Rule.WalletAddress)] = struct{}{}
				}
				if !allowed || len(candidateWallets) > policy.WalletLimit {
					continue
				}
				if _, err := tx.Exec(ctx, `
					UPDATE combination_rules
					SET run_status = 'active',
					    pause_reason = '',
					    aggregation_anchor_at = CASE
					      WHEN cycle_type = 'fixed' THEN NOW()
					      ELSE NULL
					    END
					WHERE id = $1
				`, combination.ID); err != nil {
					return result, fmt.Errorf("restore combination rule: %w", err)
				}
				if _, err := tx.Exec(ctx, `
					UPDATE watch_rules
					SET run_status = 'active', pause_reason = ''
					WHERE id IN (
						SELECT watch_rule_id
						FROM combination_rule_members
						WHERE combination_rule_id = $1
					)
				`, combination.ID); err != nil {
					return result, fmt.Errorf("restore combination members: %w", err)
				}
				wallets = candidateWallets
				activeSlots += cost
				result.CombinationRulesRestored++
			}
		}

		if err := restoreExpiredMarketEntitlements(
			ctx,
			tx,
			deboxUserID,
			policy,
			&activeSlots,
			&result,
		); err != nil {
			return result, err
		}

		return result, nil
	})
}

func restoreExpiredMarketEntitlements(
	ctx context.Context,
	tx DBTX,
	deboxUserID string,
	policy QuotaPolicy,
	activeSlots *int64,
	result *EntitlementReconcileResult,
) error {
	if policy.MarketProjectLimit <= 0 {
		return nil
	}

	activeProjects, err := queryCount(ctx, tx, `
		SELECT COUNT(*)
		FROM market_projects
		WHERE debox_user_id = $1 AND status = 'active'
	`, deboxUserID)
	if err != nil {
		return fmt.Errorf("count active market projects for entitlement restore: %w", err)
	}
	projects, err := collectMany[MarketProject](ctx, tx, `
		SELECT `+marketProjectColumns+`
		FROM market_projects
		WHERE debox_user_id = $1
		  AND status = 'paused'
		  AND pause_reason = 'subscription_expired'
		ORDER BY created_at, id
		FOR UPDATE
	`, deboxUserID)
	if err != nil {
		return fmt.Errorf("list restorable market projects: %w", err)
	}
	for _, project := range projects {
		if activeProjects >= int64(policy.MarketProjectLimit) {
			break
		}
		if _, err := tx.Exec(ctx, `
			UPDATE market_projects
			SET status = 'active',
			    pause_reason = '',
			    frozen_at = NULL,
			    updated_at = NOW()
			WHERE id = $1 AND debox_user_id = $2
			  AND status = 'paused'
			  AND pause_reason = 'subscription_expired'
		`, project.ID, deboxUserID); err != nil {
			return fmt.Errorf("restore expired market project: %w", err)
		}
		activeProjects++
		result.ProjectsRestored++
	}

	rules, err := collectMany[MarketRule](ctx, tx, `
		SELECT `+marketRuleColumns+`
		FROM market_rules
		WHERE debox_user_id = $1
		  AND enabled = 1
		  AND run_status = 'paused'
		  AND rule_scope = 'standalone'
		  AND pause_reason IN ('subscription_expired', 'free_plan')
		ORDER BY created_at, id
		FOR UPDATE
	`, deboxUserID)
	if err != nil {
		return fmt.Errorf("list restorable market rules: %w", err)
	}
	for _, rule := range rules {
		if *activeSlots >= int64(policy.RuleLimit) ||
			!marketRuleAllowedByPolicy(rule, policy) {
			continue
		}
		var projectActive bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM market_projects
				WHERE id = $1 AND debox_user_id = $2 AND status = 'active'
			)
		`, rule.MarketProjectID, deboxUserID).Scan(&projectActive); err != nil {
			return fmt.Errorf("check restored market project: %w", err)
		}
		if !projectActive {
			continue
		}
		if !policy.MultiPoolMonitoring && rule.PoolScope == "selected" {
			var nonPrimary bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1
					FROM market_rule_pools mrp
					JOIN market_project_pools mpp
					  ON mpp.id = mrp.market_project_pool_id
					WHERE mrp.market_rule_id = $1
					  AND mpp.is_primary <> 1
				)
			`, rule.ID).Scan(&nonPrimary); err != nil {
				return fmt.Errorf("validate restored market pool scope: %w", err)
			}
			if nonPrimary {
				continue
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE market_rules
			SET run_status = 'active',
			    pause_reason = '',
			    aggregation_anchor_at = CASE
			      WHEN delivery_mode = 'stage' AND cycle_type = 'fixed' THEN NOW()
			      ELSE NULL
			    END,
			    updated_at = NOW()
			WHERE id = $1 AND debox_user_id = $2
			  AND enabled = 1 AND run_status = 'paused'
			  AND pause_reason IN ('subscription_expired', 'free_plan')
		`, rule.ID, deboxUserID); err != nil {
			return fmt.Errorf("restore expired market rule: %w", err)
		}
		(*activeSlots)++
		result.MarketRulesRestored++
	}

	if !policy.CombinationRules || *activeSlots >= int64(policy.RuleLimit) {
		return nil
	}
	combinations, err := collectMany[MarketCombinationRule](ctx, tx, `
		SELECT `+marketCombinationRuleColumns+`
		FROM market_combination_rules
		WHERE debox_user_id = $1
		  AND enabled = 1
		  AND run_status = 'paused'
		  AND pause_reason IN ('subscription_expired', 'free_plan')
		ORDER BY created_at, id
		FOR UPDATE
	`, deboxUserID)
	if err != nil {
		return fmt.Errorf("list restorable market combinations: %w", err)
	}
	for _, combination := range combinations {
		if *activeSlots >= int64(policy.RuleLimit) ||
			(combination.NotificationChatType == "group" && !policy.GroupNotification) {
			continue
		}
		members, err := listMarketCombinationMembers(ctx, tx, combination.ID)
		if err != nil {
			return err
		}
		if len(members) < minimumCombinationMembers {
			continue
		}
		allowed := true
		for _, member := range members {
			if err := validateMarketCombinationMember(
				ctx,
				tx,
				deboxUserID,
				CreateMarketCombinationMemberParams{
					SourceType:           member.SourceType,
					WatchRuleID:          member.WatchRuleID,
					MarketRuleID:         member.MarketRuleID,
					RequiredTriggerCount: member.RequiredTriggerCount,
				},
				policy,
			); err != nil {
				if isEntitlementRestoreConstraint(err) {
					allowed = false
					break
				}
				return err
			}
		}
		if !allowed {
			continue
		}
		var projectsActive bool
		if err := tx.QueryRow(ctx, `
			SELECT NOT EXISTS (
				SELECT 1
				FROM market_combination_rule_projects mcrp
				JOIN market_projects mp ON mp.id = mcrp.market_project_id
				WHERE mcrp.market_combination_rule_id = $1
				  AND mp.status <> 'active'
			)
		`, combination.ID).Scan(&projectsActive); err != nil {
			return fmt.Errorf("validate restored market combination projects: %w", err)
		}
		if !projectsActive {
			continue
		}
		if _, err := tx.Exec(ctx, `
			UPDATE market_combination_rules
			SET run_status = 'active',
			    pause_reason = '',
			    aggregation_anchor_at = CASE
			      WHEN cycle_type = 'fixed' THEN NOW()
			      ELSE NULL
			    END,
			    updated_at = NOW()
			WHERE id = $1 AND debox_user_id = $2
			  AND enabled = 1 AND run_status = 'paused'
			  AND pause_reason IN ('subscription_expired', 'free_plan')
		`, combination.ID, deboxUserID); err != nil {
			return fmt.Errorf("restore expired market combination: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE market_combination_windows
			SET closed_at = NOW(),
			    notification_status = CASE
			      WHEN notification_status = 'collecting' THEN 'skipped'
			      ELSE notification_status
			    END,
			    updated_at = NOW()
			WHERE market_combination_rule_id = $1 AND closed_at IS NULL
		`, combination.ID); err != nil {
			return fmt.Errorf("reset restored market combination window: %w", err)
		}
		(*activeSlots)++
		result.MarketCombinationsRestored++
	}
	return nil
}

func marketRuleAllowedByPolicy(rule MarketRule, policy QuotaPolicy) bool {
	return policy.allowsMarketRuleType(rule.RuleType) &&
		(rule.NotificationChatType != "group" || policy.GroupNotification) &&
		(rule.DeliveryMode != "stage" || policy.StageNotifications) &&
		(policy.MultiPoolMonitoring || rule.PoolScope != "all")
}

func isEntitlementRestoreConstraint(err error) bool {
	return err == ErrInvalidCombinationRule ||
		err == ErrRuleTypeDenied ||
		err == ErrMarketRuleTypeDenied
}
