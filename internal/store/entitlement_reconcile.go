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

		return result, nil
	})
}
