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

		activeProjects, err := queryCount(ctx, tx, `
			SELECT COUNT(*) FROM market_projects
			WHERE debox_user_id = $1 AND status = 'active'
		`, deboxUserID)
		if err != nil {
			return result, fmt.Errorf("count active market projects: %w", err)
		}
		remainingProjects := int64(policy.MarketProjectLimit) - activeProjects
		if remainingProjects > 0 {
			tag, err := tx.Exec(ctx, `
				UPDATE market_projects
				SET status = 'active', pause_reason = '', updated_at = NOW()
				WHERE id IN (
					SELECT id
					FROM market_projects
					WHERE debox_user_id = $1
					  AND status = 'paused'
					  AND pause_reason = 'subscription_expired'
					ORDER BY created_at, id
					LIMIT $2
					FOR UPDATE
				)
			`, deboxUserID, remainingProjects)
			if err != nil {
				return result, fmt.Errorf("restore market projects: %w", err)
			}
			result.ProjectsRestored = tag.RowsAffected()
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

		marketRules, err := collectMany[MarketRule](ctx, tx, `
			SELECT `+marketRuleColumns+`
			FROM market_rules mr
			WHERE mr.debox_user_id = $1
			  AND mr.enabled = 1
			  AND mr.run_status = 'paused'
			  AND mr.pause_reason = 'subscription_expired'
			  AND EXISTS (
				SELECT 1 FROM market_projects mp
				WHERE mp.id = mr.market_project_id AND mp.status = 'active'
			  )
			ORDER BY mr.created_at, mr.id
			FOR UPDATE
		`, deboxUserID)
		if err != nil {
			return result, fmt.Errorf("list restorable market rules: %w", err)
		}
		for _, rule := range marketRules {
			if activeSlots >= int64(policy.RuleLimit) ||
				!policy.allowsMarketRuleType(rule.RuleType) ||
				(rule.NotificationChatType == "group" && !policy.GroupNotification) ||
				(rule.DeliveryMode == "stage" && !policy.StageNotifications) {
				continue
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
				WHERE id = $1
			`, rule.ID); err != nil {
				return result, fmt.Errorf("restore market rule: %w", err)
			}
			activeSlots++
			result.MarketRulesRestored++
		}

		if policy.CombinationRules {
			marketCombinations, err := collectMany[MarketCombinationRule](ctx, tx, `
				SELECT `+marketCombinationRuleColumns+`
				FROM market_combination_rules
				WHERE debox_user_id = $1
				  AND enabled = 1
				  AND run_status = 'paused'
				  AND pause_reason = 'subscription_expired'
				ORDER BY created_at, id
				FOR UPDATE
			`, deboxUserID)
			if err != nil {
				return result, fmt.Errorf("list restorable market combinations: %w", err)
			}
			for _, combination := range marketCombinations {
				if activeSlots >= int64(policy.RuleLimit) ||
					(combination.NotificationChatType == "group" && !policy.GroupNotification) {
					continue
				}
				var eligible bool
				if err := tx.QueryRow(ctx, `
					SELECT COUNT(*) >= 2 AND BOOL_AND(
						CASE
						  WHEN mcm.source_type = 'watch'
						  THEN wr.enabled = 1 AND wr.run_status = 'active'
						  ELSE mr.enabled = 1 AND mr.run_status = 'active'
						END
					)
					FROM market_combination_members mcm
					LEFT JOIN watch_rules wr ON wr.id = mcm.watch_rule_id
					LEFT JOIN market_rules mr ON mr.id = mcm.market_rule_id
					WHERE mcm.market_combination_rule_id = $1
				`, combination.ID).Scan(&eligible); err != nil {
					return result, fmt.Errorf("check market combination members: %w", err)
				}
				if !eligible {
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
					WHERE id = $1
				`, combination.ID); err != nil {
					return result, fmt.Errorf("restore market combination: %w", err)
				}
				activeSlots++
				result.MarketCombinationsRestored++
			}
		}
		return result, nil
	})
}
