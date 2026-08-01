package store

import (
	"context"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestRestoreExpiredMarketEntitlementsRestoresProjectAndCompatibleRule(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	defer mock.Close()

	const (
		userID    = "market-lifecycle-user"
		projectID = int64(11)
		ruleID    = int64(21)
	)
	mock.ExpectQuery("SELECT COUNT.*FROM market_projects").
		WithArgs(userID).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(0)))
	mock.ExpectQuery("SELECT .*FROM market_projects.*subscription_expired").
		WithArgs(userID).
		WillReturnRows(marketProjectRowsForDelete(
			projectID,
			userID,
			"paused",
			"subscription_expired",
		))
	mock.ExpectExec("UPDATE market_projects.*status = 'active'").
		WithArgs(projectID, userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("SELECT .*FROM market_rules.*subscription_expired").
		WithArgs(userID).
		WillReturnRows(entitlementMarketRuleRows(
			ruleID,
			userID,
			projectID,
			"market_price_above",
		))
	mock.ExpectQuery("SELECT EXISTS.*FROM market_projects").
		WithArgs(projectID, userID).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("UPDATE market_rules.*run_status = 'active'").
		WithArgs(ruleID, userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	activeSlots := int64(0)
	result := EntitlementReconcileResult{}
	err = restoreExpiredMarketEntitlements(
		context.Background(),
		mock,
		userID,
		QuotaPolicy{
			PlanCode:               "standard",
			RuleLimit:              1,
			MarketProjectLimit:     1,
			AllowedMarketRuleTypes: []string{"market_price_above"},
		},
		&activeSlots,
		&result,
	)
	if err != nil {
		t.Fatalf("restoreExpiredMarketEntitlements(): %v", err)
	}
	if activeSlots != 1 || result.ProjectsRestored != 1 ||
		result.MarketRulesRestored != 1 {
		t.Fatalf("active slots/result = %d/%+v", activeSlots, result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestRestoreExpiredMarketEntitlementsRestoresEligibleCombination(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	defer mock.Close()

	const (
		userID        = "market-combination-lifecycle-user"
		combinationID = int64(31)
		firstRuleID   = int64(41)
		secondRuleID  = int64(42)
	)
	now := time.Now().UTC()
	mock.ExpectQuery("SELECT COUNT.*FROM market_projects").
		WithArgs(userID).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(1)))
	mock.ExpectQuery("SELECT .*FROM market_projects.*subscription_expired").
		WithArgs(userID).
		WillReturnRows(entitlementMarketProjectRows())
	mock.ExpectQuery("SELECT .*FROM market_rules.*subscription_expired").
		WithArgs(userID).
		WillReturnRows(entitlementMarketRuleRows())
	mock.ExpectQuery("SELECT .*FROM market_combination_rules.*subscription_expired").
		WithArgs(userID).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "debox_user_id", "note", "cycle_type", "cycle_minutes",
			"notification_chat_id", "notification_chat_type", "notification_label",
			"notification_language", "enabled", "run_status", "pause_reason",
			"aggregation_anchor_at", "created_at", "updated_at",
		}).AddRow(
			combinationID, userID, "risk signals", "fixed", int32(60),
			userID, "private", "", "zh", int32(1), "paused",
			"subscription_expired", nil, now, now,
		))
	mock.ExpectQuery("SELECT .*FROM market_combination_members").
		WithArgs(combinationID).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "market_combination_rule_id", "source_type", "watch_rule_id",
			"market_rule_id", "required_trigger_count", "created_at",
		}).AddRow(
			int64(51), combinationID, "market", nil, pointerOf[int64](firstRuleID), int64(1), now,
		).AddRow(
			int64(52), combinationID, "market", nil, pointerOf[int64](secondRuleID), int64(1), now,
		))
	for _, ruleID := range []int64{firstRuleID, secondRuleID} {
		mock.ExpectQuery("SELECT rule_type, enabled, run_status.*FROM market_rules").
			WithArgs(ruleID, userID).
			WillReturnRows(pgxmock.NewRows([]string{
				"rule_type", "enabled", "run_status",
			}).AddRow("market_price_above", int32(1), "active"))
	}
	mock.ExpectQuery("SELECT NOT EXISTS.*market_combination_rule_projects").
		WithArgs(combinationID).
		WillReturnRows(pgxmock.NewRows([]string{"active"}).AddRow(true))
	mock.ExpectExec("UPDATE market_combination_rules.*run_status = 'active'").
		WithArgs(combinationID, userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE market_combination_windows").
		WithArgs(combinationID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	activeSlots := int64(2)
	result := EntitlementReconcileResult{}
	err = restoreExpiredMarketEntitlements(
		context.Background(),
		mock,
		userID,
		QuotaPolicy{
			PlanCode:               "professional",
			RuleLimit:              3,
			MarketProjectLimit:     1,
			AllowedMarketRuleTypes: []string{"market_price_above"},
			CombinationRules:       true,
		},
		&activeSlots,
		&result,
	)
	if err != nil {
		t.Fatalf("restoreExpiredMarketEntitlements(): %v", err)
	}
	if activeSlots != 3 || result.MarketCombinationsRestored != 1 {
		t.Fatalf("active slots/result = %d/%+v", activeSlots, result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestMarketRuleAllowedByPolicyRejectsDowngradeIncompatibilities(t *testing.T) {
	t.Parallel()
	policy := QuotaPolicy{
		AllowedMarketRuleTypes: []string{"market_price_above"},
	}
	for name, rule := range map[string]MarketRule{
		"unsupported type": {RuleType: "market_volume_above"},
		"group delivery": {
			RuleType: "market_price_above", NotificationChatType: "group",
		},
		"stage delivery": {
			RuleType: "market_price_above", DeliveryMode: "stage",
		},
		"multiple pools": {
			RuleType: "market_price_above", PoolScope: "all",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if marketRuleAllowedByPolicy(rule, policy) {
				t.Fatalf("rule unexpectedly allowed: %+v", rule)
			}
		})
	}
	if !marketRuleAllowedByPolicy(
		MarketRule{RuleType: "market_price_above", PoolScope: "primary"},
		policy,
	) {
		t.Fatal("compatible market rule was not allowed")
	}
}

func entitlementMarketRuleRows(values ...any) *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{
		"id", "debox_user_id", "market_project_id", "market_pool_id", "rule_type",
		"threshold_value", "threshold_unit", "window_minutes", "sensitivity",
		"cooldown_seconds", "repeat_while_active", "rule_scope", "delivery_mode",
		"cycle_type", "cycle_minutes", "trigger_count_threshold", "deployment_scope",
		"pool_scope", "cooldown_scope", "notification_chat_id",
		"notification_chat_type", "notification_label", "notification_language",
		"enabled", "run_status", "pause_reason", "aggregation_anchor_at", "state",
		"last_evaluated_at", "last_triggered_at", "created_at", "updated_at",
	})
	if len(values) == 0 {
		return rows
	}
	ruleID := values[0].(int64)
	userID := values[1].(string)
	projectID := values[2].(int64)
	ruleType := values[3].(string)
	now := time.Now().UTC()
	return rows.AddRow(
		ruleID, userID, projectID, nil, ruleType,
		"1", "usd", nil, "balanced", int32(0), false, "standalone", "realtime",
		"fixed", int32(60), int64(1), "all", "primary", "chain", userID,
		"private", "", "zh", int32(1), "paused", "subscription_expired", nil,
		[]byte(`{}`), nil, nil, now, now,
	)
}

func entitlementMarketProjectRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "debox_user_id", "market_asset_id", "chain_key", "chain_id",
		"token_address", "token_name", "token_symbol", "token_decimals",
		"total_supply_raw", "status", "pause_reason", "four_meme_status",
		"main_pool_id", "metadata", "last_discovered_at", "frozen_at",
		"created_at", "updated_at",
	})
}
