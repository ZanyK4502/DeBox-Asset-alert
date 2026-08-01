package store

import (
	"context"
	"testing"
	"time"
)

func TestPostgresDailyMarketSummaryGroupsLogicalProjectDeployments(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	const userID = "integration-multichain-summary"
	const bscToken = "0x1111111111111111111111111111111111111111"
	const baseToken = "0x2222222222222222222222222222222222222222"

	bscPool := createSummaryIntegrationPool(
		t, database, "bsc", 56, bscToken,
		"0x3333333333333333333333333333333333333333",
		"0x4444444444444444444444444444444444444444",
	)
	basePool := createSummaryIntegrationPool(
		t, database, "base", 8453, baseToken,
		"0x5555555555555555555555555555555555555555",
		"0x6666666666666666666666666666666666666666",
	)

	var assetID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO market_assets (
			canonical_name, symbol, identity_source, canonical_asset_id,
			verification_status
		)
		VALUES ('Summary Token', 'SUM', 'coingecko', 'summary-token', 'verified')
		RETURNING id
	`).Scan(&assetID); err != nil {
		t.Fatalf("create market asset: %v", err)
	}
	var bscDeploymentID, baseDeploymentID int64
	for _, deployment := range []struct {
		id      *int64
		chain   string
		chainID int64
		token   string
		poolID  int64
	}{
		{&bscDeploymentID, "bsc", 56, bscToken, bscPool.ID},
		{&baseDeploymentID, "base", 8453, baseToken, basePool.ID},
	} {
		if err := pool.QueryRow(ctx, `
			INSERT INTO market_asset_deployments (
				market_asset_id, chain_key, chain_id, token_address,
				token_name, token_symbol, token_decimals,
				verification_status, verification_source, default_market_pool_id
			)
			VALUES ($1, $2, $3, $4, 'Summary Token', 'SUM', 18,
				'verified', 'coingecko', $5)
			RETURNING id
		`, assetID, deployment.chain, deployment.chainID,
			deployment.token, deployment.poolID).Scan(deployment.id); err != nil {
			t.Fatalf("create %s deployment: %v", deployment.chain, err)
		}
	}
	var projectID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO market_projects (
			debox_user_id, market_asset_id, chain_key, chain_id, token_address,
			token_name, token_symbol, token_decimals, main_pool_id
		)
		VALUES ($1, $2, 'bsc', 56, $3, 'Summary Token', 'SUM', 18, $4)
		RETURNING id
	`, userID, assetID, bscToken, bscPool.ID).Scan(&projectID); err != nil {
		t.Fatalf("create market project: %v", err)
	}
	for _, deployment := range []struct {
		id     int64
		poolID int64
	}{
		{bscDeploymentID, bscPool.ID},
		{baseDeploymentID, basePool.ID},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO market_project_deployments (
				market_project_id, market_asset_id,
				market_asset_deployment_id, default_market_pool_id
			)
			VALUES ($1, $2, $3, $4)
		`, projectID, assetID, deployment.id, deployment.poolID); err != nil {
			t.Fatalf("create project deployment: %v", err)
		}
	}

	periodStart := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.Add(24 * time.Hour)
	createSummaryMarketEvent(
		t, database, bscPool.ID, "bsc", 56, bscToken,
		"buy", "summary-bsc-buy", "100", periodStart.Add(time.Hour),
	)
	createSummaryMarketEvent(
		t, database, bscPool.ID, "bsc", 56, bscToken,
		"holder_increase", "summary-bsc-holder", "", periodStart.Add(2*time.Hour),
	)
	createSummaryMarketEvent(
		t, database, basePool.ID, "base", 8453, baseToken,
		"sell", "summary-base-sell", "75", periodStart.Add(3*time.Hour),
	)
	startPrice, endPrice := "1", "1.2"
	for _, snapshot := range []struct {
		price      *string
		capturedAt time.Time
	}{
		{&startPrice, periodStart.Add(10 * time.Minute)},
		{&endPrice, periodStart.Add(50 * time.Minute)},
	} {
		if _, err := database.CreateMarketSnapshot(ctx, CreateMarketSnapshotParams{
			ChainKey:     "bsc",
			ChainID:      56,
			TokenAddress: bscToken,
			MarketPoolID: bscPool.ID,
			PriceUSD:     snapshot.price,
			Source:       "integration",
			CapturedAt:   snapshot.capturedAt,
		}); err != nil {
			t.Fatalf("create summary snapshot: %v", err)
		}
	}

	statistics, err := database.DailySummaryStatistics(
		ctx, userID, periodStart, periodEnd,
	)
	if err != nil {
		t.Fatalf("DailySummaryStatistics() error = %v", err)
	}
	if statistics.MarketProjectCount != 1 ||
		statistics.MarketEventCount != 3 ||
		statistics.MarketBuyCount != 1 ||
		statistics.MarketSellCount != 1 ||
		statistics.HolderEventCount != 1 {
		t.Fatalf("unexpected market statistics: %#v", statistics)
	}

	summaries, err := database.ListDailyMarketProjectChainSummaries(
		ctx, userID, periodStart, periodEnd,
	)
	if err != nil {
		t.Fatalf("ListDailyMarketProjectChainSummaries() error = %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("summary count = %d, want 2: %#v", len(summaries), summaries)
	}
	if summaries[0].ChainKey != "bsc" ||
		summaries[0].TradeVolumeUSD != "100" ||
		summaries[0].BuyCount != 1 ||
		summaries[0].HolderIncreaseCount != 1 ||
		summaries[0].SnapshotCount != 2 ||
		summaries[0].PriceSampleCount != 2 ||
		summaries[0].LiquiditySampleCount != 0 ||
		summaries[0].VolumeSampleCount != 0 ||
		summaries[0].StartPriceUSD == nil || *summaries[0].StartPriceUSD != "1" ||
		summaries[0].EndPriceUSD == nil || *summaries[0].EndPriceUSD != "1.2" {
		t.Fatalf("unexpected BSC summary: %#v", summaries[0])
	}
	if summaries[1].ChainKey != "base" ||
		summaries[1].TradeVolumeUSD != "75" ||
		summaries[1].SellCount != 1 ||
		summaries[1].SnapshotCount != 0 ||
		summaries[1].PriceSampleCount != 0 ||
		summaries[1].LiquiditySampleCount != 0 ||
		summaries[1].VolumeSampleCount != 0 {
		t.Fatalf("unexpected Base summary: %#v", summaries[1])
	}

	recent, err := database.ListSummaryRecentMarketEvents(
		ctx, userID, periodStart, periodEnd, 10,
	)
	if err != nil {
		t.Fatalf("ListSummaryRecentMarketEvents() error = %v", err)
	}
	if len(recent) != 3 || recent[0].ChainKey != "base" ||
		recent[0].Protocol != "integration" ||
		recent[0].PoolAddress == nil {
		t.Fatalf("unexpected recent market events: %#v", recent)
	}

	basePrice := "2.5"
	if _, err := database.CreateMarketSnapshot(ctx, CreateMarketSnapshotParams{
		ChainKey:     "base",
		ChainID:      8453,
		TokenAddress: baseToken,
		MarketPoolID: basePool.ID,
		PriceUSD:     &basePrice,
		Source:       "integration",
		CapturedAt:   periodStart.Add(4 * time.Hour),
	}); err != nil {
		t.Fatalf("create Base delivery snapshot: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO subscriptions (
			debox_user_id, plan_code, status, starts_at, expires_at,
			daily_summary_timezone
		)
		VALUES ($1, 'professional', 'active', NOW() - INTERVAL '1 hour',
			NOW() + INTERVAL '1 day', 'America/New_York')
	`, userID); err != nil {
		t.Fatalf("create delivery subscription: %v", err)
	}
	var ruleID, ruleEventID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO market_rules (
			debox_user_id, market_project_id, market_pool_id,
			rule_type, threshold_value, threshold_unit,
			notification_chat_id
		)
		VALUES ($1, $2, $3, 'market_large_sell', 50, 'usd', $1)
		RETURNING id
	`, userID, projectID, basePool.ID).Scan(&ruleID); err != nil {
		t.Fatalf("create delivery rule: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO market_rule_events (
			market_rule_id, market_event_id, trigger_key,
			previous_value, current_value, notification_status, note
		)
		VALUES ($1, $2, 'base-delivery', '50', '75', 'sending', 'Base sell')
		RETURNING id
	`, ruleID, recent[0].ID).Scan(&ruleEventID); err != nil {
		t.Fatalf("create delivery rule event: %v", err)
	}
	delivery, err := database.LoadMarketDelivery(ctx, MarketDeliveryClaim{
		Kind: "realtime",
		ID:   ruleEventID,
	})
	if err != nil {
		t.Fatalf("LoadMarketDelivery() error = %v", err)
	}
	if delivery.Event == nil || delivery.Event.ChainKey != "base" ||
		delivery.Pool == nil || delivery.Pool.ID != basePool.ID ||
		delivery.Snapshot == nil || delivery.Snapshot.PriceUSD == nil ||
		*delivery.Snapshot.PriceUSD != basePrice ||
		delivery.Timezone != "America/New_York" {
		t.Fatalf("unexpected multichain delivery: %#v", delivery)
	}
	var stageWindowID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO market_stage_windows (
			debox_user_id, market_rule_id, starts_at, ends_at,
			trigger_count, notification_status
		)
		VALUES ($1, $2, $3, $4, 1, 'sending')
		RETURNING id
	`, userID, ruleID, periodStart, periodEnd).Scan(&stageWindowID); err != nil {
		t.Fatalf("create stage window: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO market_stage_window_events (
			market_stage_window_id, market_rule_event_id
		)
		VALUES ($1, $2)
	`, stageWindowID, ruleEventID); err != nil {
		t.Fatalf("create stage window event: %v", err)
	}
	stageDelivery, err := database.LoadMarketDelivery(ctx, MarketDeliveryClaim{
		Kind: "stage",
		ID:   stageWindowID,
	})
	if err != nil {
		t.Fatalf("LoadMarketDelivery(stage) error = %v", err)
	}
	if len(stageDelivery.RecentEvents) != 1 ||
		len(stageDelivery.StageEvents) != 1 ||
		stageDelivery.RecentEvents[0].Event.ChainKey != "base" ||
		stageDelivery.StageEvents[0].Event.ChainKey != "base" ||
		stageDelivery.StageEvents[0].PreviousValue == nil ||
		*stageDelivery.StageEvents[0].PreviousValue != "50" ||
		stageDelivery.StageEvents[0].CurrentValue == nil ||
		*stageDelivery.StageEvents[0].CurrentValue != "75" ||
		stageDelivery.RecentEvents[0].Pool == nil ||
		stageDelivery.RecentEvents[0].Pool.ID != basePool.ID ||
		stageDelivery.Timezone != "America/New_York" {
		t.Fatalf("unexpected stage delivery: %#v", stageDelivery)
	}

	var combinationRuleID, combinationMemberID, combinationWindowID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO market_combination_rules (
			debox_user_id, note, cycle_minutes, notification_chat_id
		)
		VALUES ($1, 'Base market combination', 60, $1)
		RETURNING id
	`, userID).Scan(&combinationRuleID); err != nil {
		t.Fatalf("create combination rule: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO market_combination_members (
			market_combination_rule_id, source_type,
			market_rule_id, required_trigger_count
		)
		VALUES ($1, 'market', $2, 1)
		RETURNING id
	`, combinationRuleID, ruleID).Scan(&combinationMemberID); err != nil {
		t.Fatalf("create combination member: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO market_combination_windows (
			debox_user_id, market_combination_rule_id,
			starts_at, ends_at, total_trigger_count, notification_status
		)
		VALUES ($1, $2, $3, $4, 1, 'sending')
		RETURNING id
	`, userID, combinationRuleID, periodStart, periodEnd).Scan(&combinationWindowID); err != nil {
		t.Fatalf("create combination window: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO market_combination_window_members (
			market_combination_window_id, market_combination_member_id,
			required_trigger_count, trigger_count, reached_at
		)
		VALUES ($1, $2, 1, 1, NOW())
	`, combinationWindowID, combinationMemberID); err != nil {
		t.Fatalf("create combination window member: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO market_combination_trigger_events (
			market_combination_window_id, market_combination_member_id,
			source_type, market_rule_event_id, note, occurred_at
		)
		VALUES ($1, $2, 'market', $3, 'Base sell', $4)
	`, combinationWindowID, combinationMemberID, ruleEventID,
		periodStart.Add(3*time.Hour)); err != nil {
		t.Fatalf("create combination trigger event: %v", err)
	}
	combinationDelivery, err := database.LoadMarketDelivery(ctx, MarketDeliveryClaim{
		Kind: "combination",
		ID:   combinationWindowID,
	})
	if err != nil {
		t.Fatalf("LoadMarketDelivery(combination) error = %v", err)
	}
	if len(combinationDelivery.CombinationMembers) != 1 ||
		combinationDelivery.CombinationMembers[0].TriggerCount != 1 ||
		combinationDelivery.CombinationMembers[0].ReachedAt == nil ||
		combinationDelivery.CombinationMembers[0].MarketRule == nil ||
		combinationDelivery.CombinationMembers[0].MarketRule.RuleType !=
			"market_large_sell" ||
		len(combinationDelivery.CombinationMembers[0].MarketEvents) != 1 ||
		combinationDelivery.CombinationMembers[0].MarketEvents[0].Event.ChainKey != "base" ||
		len(combinationDelivery.CombinationMembers[0].RecentEvents) != 1 ||
		combinationDelivery.CombinationMembers[0].RecentEvents[0].Event.ChainKey != "base" ||
		combinationDelivery.Timezone != "America/New_York" {
		t.Fatalf("unexpected combination delivery: %#v", combinationDelivery)
	}
}

func createSummaryIntegrationPool(
	t *testing.T,
	database *Store,
	chainKey string,
	chainID int64,
	tokenAddress string,
	quoteAddress string,
	poolAddress string,
) MarketPool {
	t.Helper()
	pool, err := database.UpsertMarketPool(
		context.Background(),
		UpsertMarketPoolParams{
			ChainKey: chainKey, ChainID: chainID,
			Protocol: "integration", ProtocolVersion: "v3",
			PoolKey: poolAddress, PoolAddress: &poolAddress,
			Token0Address: tokenAddress, Token0Symbol: "SUM", Token0Decimals: 18,
			Token1Address: quoteAddress, Token1Symbol: "USD", Token1Decimals: 18,
			LiquidityUSD: "100000", SupportsEventParsing: true,
			ParserAdapter: "uniswap_v3", VerificationStatus: "verified",
		},
	)
	if err != nil {
		t.Fatalf("create %s summary pool: %v", chainKey, err)
	}
	return pool
}

func createSummaryMarketEvent(
	t *testing.T,
	database *Store,
	poolID int64,
	chainKey string,
	chainID int64,
	tokenAddress string,
	eventType string,
	eventKey string,
	usdValue string,
	occurredAt time.Time,
) {
	t.Helper()
	var value *string
	if usdValue != "" {
		value = &usdValue
	}
	if _, created, err := database.CreateMarketEvent(
		context.Background(),
		CreateMarketEventParams{
			MarketPoolID: &poolID,
			ChainKey:     chainKey,
			ChainID:      chainID,
			TokenAddress: tokenAddress,
			EventType:    eventType,
			EventKey:     eventKey,
			USDValue:     value,
			Source:       "integration",
			Confirmed:    true,
			OccurredAt:   occurredAt,
		},
	); err != nil || !created {
		t.Fatalf("create %s event: created=%v error=%v", eventType, created, err)
	}
}
