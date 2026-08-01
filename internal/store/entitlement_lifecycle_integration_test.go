package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresMembershipLifecycleRestoresCompatibleMarketResources(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	const userID = "integration-membership-lifecycle"

	if _, err := database.ActivateSubscription(ctx, userID, "professional", 30); err != nil {
		t.Fatalf("activate professional subscription: %v", err)
	}
	professional := QuotaPolicy{
		PlanCode:               "professional",
		RuleLimit:              3,
		MarketProjectLimit:     1,
		AllowedMarketRuleTypes: []string{"market_price_above", "market_volume_above"},
		CombinationRules:       true,
		MultiPoolMonitoring:    true,
	}
	project, err := database.CreateMarketProjectWithinQuota(
		ctx,
		CreateMarketProjectParams{
			DeBoxUserID:   userID,
			ChainKey:      "bsc",
			ChainID:       56,
			TokenAddress:  "0x1111111111111111111111111111111111111111",
			TokenName:     "Lifecycle Token",
			TokenSymbol:   "LIFE",
			TokenDecimals: 18,
		},
		professional,
	)
	if err != nil {
		t.Fatalf("create lifecycle market project: %v", err)
	}
	priceRule, err := database.CreateMarketRuleWithinQuota(ctx, CreateMarketRuleParams{
		DeBoxUserID: userID, MarketProjectID: project.ID,
		RuleType: "market_price_above", ThresholdValue: "1",
		ThresholdUnit: "usd", NotificationChatID: userID,
	}, professional)
	if err != nil {
		t.Fatalf("create lifecycle price rule: %v", err)
	}
	volumeRule, err := database.CreateMarketRuleWithinQuota(ctx, CreateMarketRuleParams{
		DeBoxUserID: userID, MarketProjectID: project.ID,
		RuleType: "market_volume_above", ThresholdValue: "1000",
		ThresholdUnit: "usd", NotificationChatID: userID,
	}, professional)
	if err != nil {
		t.Fatalf("create lifecycle volume rule: %v", err)
	}
	combination, err := database.CreateMarketCombinationWithinQuota(
		ctx,
		CreateMarketCombinationParams{
			DeBoxUserID: userID, Note: "price and volume",
			CycleType: "follow", CycleMinutes: 60,
			NotificationChatID: userID,
			Members: []CreateMarketCombinationMemberParams{
				{SourceType: "market", MarketRuleID: &priceRule.ID},
				{SourceType: "market", MarketRuleID: &volumeRule.ID},
			},
		},
		professional,
	)
	if err != nil {
		t.Fatalf("create lifecycle market combination: %v", err)
	}

	snapshot, err := database.CreateNotificationDetailSnapshot(
		ctx,
		CreateNotificationDetailSnapshotParams{
			SourceKey:            "integration:lifecycle:snapshot",
			NotificationKind:     NotificationKindMarketRealtime,
			SourceType:           "market_rule_event",
			DeBoxUserID:          userID,
			RuleID:               &priceRule.ID,
			RuleType:             priceRule.RuleType,
			RuleName:             "LIFE",
			NotificationChatID:   userID,
			NotificationChatType: "private",
			NotificationLanguage: "zh",
			NotificationText:     "membership lifecycle snapshot",
			Details:              json.RawMessage(`{"phase":"professional"}`),
		},
	)
	if err != nil {
		t.Fatalf("create lifecycle notification snapshot: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE subscriptions
		SET expires_at = NOW() - INTERVAL '1 minute'
		WHERE debox_user_id = $1 AND status = 'active'
	`, userID); err != nil {
		t.Fatalf("expire professional subscription: %v", err)
	}
	if paused, err := database.ApplyExpiredEntitlementFallbacks(ctx); err != nil || paused != 1 {
		t.Fatalf("pause expired professional resources = %d, error = %v", paused, err)
	}
	assertLifecycleMarketStatuses(
		t,
		pool,
		project.ID,
		priceRule.ID,
		volumeRule.ID,
		combination.ID,
		"paused",
		"paused",
		"paused",
		"paused",
	)

	if _, err := database.ActivateSubscription(ctx, userID, "standard", 30); err != nil {
		t.Fatalf("activate standard subscription: %v", err)
	}
	standard := QuotaPolicy{
		PlanCode:               "standard",
		RuleLimit:              1,
		MarketProjectLimit:     1,
		AllowedMarketRuleTypes: []string{"market_price_above"},
	}
	result, err := database.ReconcileUserEntitlements(ctx, userID, standard)
	if err != nil {
		t.Fatalf("reconcile standard entitlements: %v", err)
	}
	if result.ProjectsRestored != 1 || result.MarketRulesRestored != 1 ||
		result.MarketCombinationsRestored != 0 {
		t.Fatalf("standard entitlement restore = %+v", result)
	}
	assertLifecycleMarketStatuses(
		t,
		pool,
		project.ID,
		priceRule.ID,
		volumeRule.ID,
		combination.ID,
		"active",
		"active",
		"paused",
		"paused",
	)

	if _, err := pool.Exec(ctx, `
		UPDATE subscriptions
		SET expires_at = NOW() - INTERVAL '1 minute'
		WHERE debox_user_id = $1 AND status = 'active'
	`, userID); err != nil {
		t.Fatalf("expire standard subscription: %v", err)
	}
	if paused, err := database.ApplyExpiredEntitlementFallbacks(ctx); err != nil || paused != 1 {
		t.Fatalf("pause expired standard resources = %d, error = %v", paused, err)
	}
	if _, err := database.ActivateSubscription(ctx, userID, "professional", 30); err != nil {
		t.Fatalf("reactivate professional subscription: %v", err)
	}
	result, err = database.ReconcileUserEntitlements(ctx, userID, professional)
	if err != nil {
		t.Fatalf("reconcile professional entitlements: %v", err)
	}
	if result.ProjectsRestored != 1 || result.MarketRulesRestored != 2 ||
		result.MarketCombinationsRestored != 1 {
		t.Fatalf("professional entitlement restore = %+v", result)
	}
	assertLifecycleMarketStatuses(
		t,
		pool,
		project.ID,
		priceRule.ID,
		volumeRule.ID,
		combination.ID,
		"active",
		"active",
		"active",
		"active",
	)

	storedSnapshot, err := database.GetNotificationDetailSnapshot(ctx, snapshot.PublicID)
	if err != nil || storedSnapshot == nil || storedSnapshot.ID != snapshot.ID {
		t.Fatalf(
			"notification snapshot after membership changes = %#v, error = %v",
			storedSnapshot,
			err,
		)
	}
}

func assertLifecycleMarketStatuses(
	t *testing.T,
	pool *pgxpool.Pool,
	projectID int64,
	priceRuleID int64,
	volumeRuleID int64,
	combinationID int64,
	wantProject string,
	wantPrice string,
	wantVolume string,
	wantCombination string,
) {
	t.Helper()
	var projectStatus, priceStatus, volumeStatus, combinationStatus string
	if err := pool.QueryRow(context.Background(), `
		SELECT
		  (SELECT status FROM market_projects WHERE id = $1),
		  (SELECT run_status FROM market_rules WHERE id = $2),
		  (SELECT run_status FROM market_rules WHERE id = $3),
		  (SELECT run_status FROM market_combination_rules WHERE id = $4)
	`, projectID, priceRuleID, volumeRuleID, combinationID).Scan(
		&projectStatus,
		&priceStatus,
		&volumeStatus,
		&combinationStatus,
	); err != nil {
		t.Fatalf("read lifecycle market statuses: %v", err)
	}
	if projectStatus != wantProject || priceStatus != wantPrice ||
		volumeStatus != wantVolume || combinationStatus != wantCombination {
		t.Fatalf(
			"market statuses = %s/%s/%s/%s, want %s/%s/%s/%s",
			projectStatus,
			priceStatus,
			volumeStatus,
			combinationStatus,
			wantProject,
			wantPrice,
			wantVolume,
			wantCombination,
		)
	}
}
