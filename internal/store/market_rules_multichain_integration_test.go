package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/migrations"
)

func TestPostgresMultiChainMarketRuleTargetsAndCombinationLifecycle(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	const userID = "integration-multichain-market-rules"
	if _, err := pool.Exec(ctx, `
		INSERT INTO subscriptions (
			debox_user_id, plan_code, status, starts_at, expires_at
		)
		VALUES ($1, 'professional', 'active', NOW() - INTERVAL '1 hour',
			NOW() + INTERVAL '1 day')
	`, userID); err != nil {
		t.Fatalf("create professional subscription: %v", err)
	}
	policy := QuotaPolicy{
		PlanCode:               "professional",
		RuleLimit:              100,
		MarketProjectLimit:     5,
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
			TokenName:     "Multi",
			TokenSymbol:   "MULTI",
			TokenDecimals: 18,
		},
		policy,
	)
	if err != nil {
		t.Fatalf("create market project: %v", err)
	}

	var assetID, bscDeploymentID, baseDeploymentID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO market_assets (
			canonical_name, symbol, identity_source, canonical_asset_id,
			verification_status
		)
		VALUES ('Multi', 'MULTI', 'integration', 'multi', 'verified')
		RETURNING id
	`).Scan(&assetID); err != nil {
		t.Fatalf("create market asset: %v", err)
	}
	for _, deployment := range []struct {
		chainKey string
		chainID  int64
		address  string
		target   *int64
	}{
		{"bsc", 56, "0x1111111111111111111111111111111111111111", &bscDeploymentID},
		{"base", 8453, "0x2222222222222222222222222222222222222222", &baseDeploymentID},
	} {
		if err := pool.QueryRow(ctx, `
			INSERT INTO market_asset_deployments (
				market_asset_id, chain_key, chain_id, token_address,
				token_name, token_symbol, token_decimals,
				verification_status, verification_source
			)
			VALUES ($1, $2, $3, $4, 'Multi', 'MULTI', 18, 'verified', 'integration')
			RETURNING id
		`,
			assetID,
			deployment.chainKey,
			deployment.chainID,
			deployment.address,
		).Scan(deployment.target); err != nil {
			t.Fatalf("create %s deployment: %v", deployment.chainKey, err)
		}
	}
	if _, err := pool.Exec(ctx, `
		UPDATE market_projects SET market_asset_id = $1 WHERE id = $2
	`, assetID, project.ID); err != nil {
		t.Fatalf("bind project asset: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO market_project_deployments (
			market_project_id, market_asset_id, market_asset_deployment_id
		)
		VALUES ($2, $1, $3), ($2, $1, $4)
	`, assetID, project.ID, bscDeploymentID, baseDeploymentID); err != nil {
		t.Fatalf("bind project deployments: %v", err)
	}

	type deploymentPool struct {
		deploymentID int64
		chainKey     string
		chainID      int64
		token        string
		quote        string
		poolAddress  string
	}
	for _, target := range []deploymentPool{
		{
			bscDeploymentID, "bsc", 56,
			"0x1111111111111111111111111111111111111111",
			"0x3333333333333333333333333333333333333333",
			"0x4444444444444444444444444444444444444444",
		},
		{
			baseDeploymentID, "base", 8453,
			"0x2222222222222222222222222222222222222222",
			"0x5555555555555555555555555555555555555555",
			"0x6666666666666666666666666666666666666666",
		},
	} {
		address := target.poolAddress
		marketPool, err := database.UpsertMarketPool(ctx, UpsertMarketPoolParams{
			ChainKey: target.chainKey, ChainID: target.chainID,
			Protocol: "integration", ProtocolVersion: "v2",
			PoolKey: target.poolAddress, PoolAddress: &address,
			Token0Address: target.token, Token0Symbol: "MULTI", Token0Decimals: 18,
			Token1Address: target.quote, Token1Symbol: "USD", Token1Decimals: 18,
			LiquidityUSD: "100000", SupportsEventParsing: true,
			ParserAdapter: "uniswap_v2", VerificationStatus: "verified",
		})
		if err != nil {
			t.Fatalf("create %s pool: %v", target.chainKey, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO market_project_pools (
				market_project_id, market_project_deployment_id, market_pool_id,
				selected, is_primary, discovery_source
			)
			VALUES ($1, $2, $3, 1, 1, 'integration')
		`, project.ID, target.deploymentID, marketPool.ID); err != nil {
			t.Fatalf("link %s pool: %v", target.chainKey, err)
		}
	}

	baseProjects, err := database.ListActiveMarketProjectsForCollection(
		ctx,
		8453,
		10,
	)
	if err != nil || len(baseProjects) != 1 ||
		baseProjects[0].ChainKey != "base" ||
		baseProjects[0].TokenAddress !=
			"0x2222222222222222222222222222222222222222" ||
		baseProjects[0].MarketAssetDeploymentID == nil ||
		*baseProjects[0].MarketAssetDeploymentID != baseDeploymentID ||
		baseProjects[0].MarketProjectDeploymentID == nil {
		t.Fatalf(
			"Base collection projects = %#v, error = %v",
			baseProjects,
			err,
		)
	}
	baseTargets, err := database.ListMarketCollectionTargets(ctx, 8453)
	if err != nil || len(baseTargets) != 1 ||
		baseTargets[0].ChainKey != "base" ||
		baseTargets[0].TokenAddress !=
			"0x2222222222222222222222222222222222222222" {
		t.Fatalf("Base collection targets = %#v, error = %v", baseTargets, err)
	}
	dueBase, err := database.ListMarketProjectsDueHolderRefresh(
		ctx,
		8453,
		time.Now().UTC(),
		10,
	)
	if err != nil || len(dueBase) != 1 ||
		dueBase[0].MarketProjectDeploymentID == nil ||
		dueBase[0].ChainID != 8453 {
		t.Fatalf("Base holder refresh targets = %#v, error = %v", dueBase, err)
	}

	discoveredAddress := "0x7777777777777777777777777777777777777777"
	discoveredPool, err := database.UpsertMarketPool(ctx, UpsertMarketPoolParams{
		ChainKey: "base", ChainID: 8453,
		Protocol: "integration", ProtocolVersion: "v2",
		PoolKey: discoveredAddress, PoolAddress: &discoveredAddress,
		Token0Address: "0x2222222222222222222222222222222222222222",
		Token0Symbol:  "MULTI", Token0Decimals: 18,
		Token1Address: "0x8888888888888888888888888888888888888888",
		Token1Symbol:  "USD", Token1Decimals: 18,
		LiquidityUSD: "50000", SupportsEventParsing: true,
		ParserAdapter: "uniswap_v2", VerificationStatus: "verified",
	})
	if err != nil {
		t.Fatalf("create discovered Base pool: %v", err)
	}
	discoveredLink, err := database.EnsureMarketProjectPool(
		ctx,
		EnsureMarketProjectPoolParams{
			DeBoxUserID:     userID,
			MarketProjectID: project.ID,
			MarketPoolID:    discoveredPool.ID,
			SelectIfNone:    true,
			DiscoverySource: "integration",
		},
	)
	if err != nil ||
		discoveredLink.MarketProjectDeploymentID == nil ||
		*discoveredLink.MarketProjectDeploymentID == 0 ||
		discoveredLink.Selected != 0 ||
		discoveredLink.IsPrimary != 0 {
		t.Fatalf(
			"discovered Base project pool = %#v, error = %v",
			discoveredLink,
			err,
		)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE market_project_pools
		SET market_project_deployment_id = NULL
		WHERE id = $1
	`, discoveredLink.ID); err != nil {
		t.Fatalf("clear discovered deployment link: %v", err)
	}
	repairMigration, err := migrations.Files.ReadFile(
		"0014_repair_multichain_collection_links.sql",
	)
	if err != nil {
		t.Fatalf("read collection link repair migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(repairMigration)); err != nil {
		t.Fatalf("apply collection link repair migration: %v", err)
	}
	var repairedDeploymentID *int64
	if err := pool.QueryRow(ctx, `
		SELECT market_project_deployment_id
		FROM market_project_pools
		WHERE id = $1
	`, discoveredLink.ID).Scan(&repairedDeploymentID); err != nil ||
		repairedDeploymentID == nil {
		t.Fatalf(
			"repaired deployment id = %v, error = %v",
			repairedDeploymentID,
			err,
		)
	}

	first, err := database.CreateMarketRuleWithinQuota(ctx, CreateMarketRuleParams{
		DeBoxUserID: userID, MarketProjectID: project.ID,
		RuleType: "market_price_above", ThresholdValue: "1",
		ThresholdUnit: "usd", DeploymentScope: "all", PoolScope: "all",
		CooldownScope: "chain", NotificationChatID: userID,
	}, policy)
	if err != nil {
		t.Fatalf("create all-deployment rule: %v", err)
	}
	targets, err := database.ListMarketRuleTargets(ctx, first, project)
	if err != nil {
		t.Fatalf("list rule targets: %v", err)
	}
	if len(targets) != 2 || targets[0].ChainID == targets[1].ChainID {
		t.Fatalf("rule targets = %#v, want one BSC and one Base target", targets)
	}
	if err := database.UpdateMarketRuleTargetState(
		ctx, first.ID, targets[0], []byte(`{"last_event_id":123}`), true,
	); err != nil {
		t.Fatalf("update target state: %v", err)
	}
	loadedTarget, exists, err := database.LoadMarketRuleTargetState(
		ctx, first.ID, targets[0],
	)
	var loadedState map[string]int64
	decodeErr := json.Unmarshal(loadedTarget.State, &loadedState)
	if err != nil || !exists || decodeErr != nil || loadedState["last_event_id"] != 123 {
		t.Fatalf("loaded target state = %#v, exists=%v, error=%v", loadedTarget, exists, err)
	}
	baseEvent, _, err := database.CreateMarketEvent(ctx, CreateMarketEventParams{
		ChainKey: "base", ChainID: 8453,
		TokenAddress: "0x2222222222222222222222222222222222222222",
		EventType:    "integration", EventKey: "integration:base-bound",
		Source: "integration", Confidence: "1", Confirmed: true,
		OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create Base source event: %v", err)
	}
	if _, inserted, err := database.CreateMarketRuleEvent(
		ctx,
		CreateMarketRuleEventParams{
			MarketRuleID: first.ID, MarketEventID: baseEvent.ID,
			TriggerKey: "integration:base-bound",
		},
	); err != nil || !inserted {
		t.Fatalf("bind Base event to multi-chain rule: inserted=%v error=%v", inserted, err)
	}
	unrelatedEvent, _, err := database.CreateMarketEvent(ctx, CreateMarketEventParams{
		ChainKey: "ethereum", ChainID: 1,
		TokenAddress: "0x9999999999999999999999999999999999999999",
		EventType:    "integration", EventKey: "integration:unrelated",
		Source: "integration", Confidence: "1", Confirmed: true,
		OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create unrelated source event: %v", err)
	}
	if _, _, err := database.CreateMarketRuleEvent(
		ctx,
		CreateMarketRuleEventParams{
			MarketRuleID: first.ID, MarketEventID: unrelatedEvent.ID,
			TriggerKey: "integration:unrelated",
		},
	); !errors.Is(err, ErrInvalidMarketRuleEvent) {
		t.Fatalf("unrelated event error = %v, want ErrInvalidMarketRuleEvent", err)
	}
	second, err := database.CreateMarketRuleWithinQuota(ctx, CreateMarketRuleParams{
		DeBoxUserID: userID, MarketProjectID: project.ID,
		RuleType: "market_volume_above", ThresholdValue: "1000",
		ThresholdUnit: "usd", DeploymentScope: "all", PoolScope: "primary",
		CooldownScope: "project", NotificationChatID: userID,
	}, policy)
	if err != nil {
		t.Fatalf("create second rule: %v", err)
	}
	combination, err := database.CreateMarketCombinationWithinQuota(
		ctx,
		CreateMarketCombinationParams{
			DeBoxUserID: userID, Note: "price and volume",
			CycleType: "follow", CycleMinutes: 15,
			NotificationChatID: userID,
			Members: []CreateMarketCombinationMemberParams{
				{SourceType: "market", MarketRuleID: &first.ID},
				{SourceType: "market", MarketRuleID: &second.ID},
			},
		},
		policy,
	)
	if err != nil {
		t.Fatalf("create market combination: %v", err)
	}
	var projectLinks int64
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM market_combination_rule_projects
		WHERE market_combination_rule_id = $1
	`, combination.ID).Scan(&projectLinks); err != nil || projectLinks != 1 {
		t.Fatalf("combination project links = %d, error = %v", projectLinks, err)
	}
	if archived, err := database.ArchiveMarketCombinationRule(
		ctx, combination.ID, userID,
	); err != nil || !archived {
		t.Fatalf("archive combination = %v, error = %v", archived, err)
	}
	restored, err := database.RestoreMarketCombinationWithinQuota(
		ctx, combination.ID, userID, policy,
	)
	if err != nil || restored.RunStatus != "active" || len(restored.Members) != 2 {
		t.Fatalf("restored combination = %#v, error = %v", restored, err)
	}
	occurredAt := time.Now().UTC()
	recordRuleTrigger := func(ruleID int64, key string) {
		t.Helper()
		event, _, err := database.CreateMarketEvent(ctx, CreateMarketEventParams{
			ChainKey: "bsc", ChainID: 56,
			TokenAddress: "0x1111111111111111111111111111111111111111",
			EventType:    "integration", EventKey: key,
			Source: "integration", Confidence: "1", Confirmed: true,
			OccurredAt: occurredAt,
		})
		if err != nil {
			t.Fatalf("create source event %s: %v", key, err)
		}
		ruleEvent, inserted, err := database.CreateMarketRuleEvent(
			ctx,
			CreateMarketRuleEventParams{
				MarketRuleID: ruleID, MarketEventID: event.ID,
				TriggerKey: key, NotificationStatus: "combined",
			},
		)
		if err != nil || !inserted {
			t.Fatalf("create rule event %s: inserted=%v error=%v", key, inserted, err)
		}
		if _, err := database.RecordMarketCombinationTrigger(
			ctx,
			RecordMarketCombinationTriggerParams{
				SourceType: "market", MarketRuleEventID: &ruleEvent.ID,
				OccurredAt: occurredAt,
			},
		); err != nil {
			t.Fatalf("record combination trigger %s: %v", key, err)
		}
	}
	recordRuleTrigger(first.ID, "integration:first")
	var status string
	if err := pool.QueryRow(ctx, `
		SELECT notification_status
		FROM market_combination_windows
		WHERE market_combination_rule_id = $1 AND closed_at IS NULL
	`, combination.ID).Scan(&status); err != nil || status != "collecting" {
		t.Fatalf("one-member combination status = %q, error = %v", status, err)
	}
	recordRuleTrigger(second.ID, "integration:second")
	if err := pool.QueryRow(ctx, `
		SELECT notification_status
		FROM market_combination_windows
		WHERE market_combination_rule_id = $1 AND closed_at IS NULL
	`, combination.ID).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("complete combination status = %q, error = %v", status, err)
	}
}

func TestPostgresStandardMarketRuleRejectsAllPoolScope(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	const userID = "integration-standard-pool-scope"
	if _, err := pool.Exec(ctx, `
		INSERT INTO subscriptions (
			debox_user_id, plan_code, status, starts_at, expires_at
		)
		VALUES ($1, 'standard', 'active', NOW() - INTERVAL '1 hour',
			NOW() + INTERVAL '1 day')
	`, userID); err != nil {
		t.Fatalf("create standard subscription: %v", err)
	}
	policy := QuotaPolicy{
		PlanCode: "standard", RuleLimit: 10, MarketProjectLimit: 1,
		AllowedMarketRuleTypes: []string{"market_price_above"},
	}
	project, err := database.CreateMarketProjectWithinQuota(
		ctx,
		CreateMarketProjectParams{
			DeBoxUserID: userID, ChainKey: "bsc", ChainID: 56,
			TokenAddress: "0x7777777777777777777777777777777777777777",
			TokenName:    "Standard", TokenSymbol: "STD", TokenDecimals: 18,
		},
		policy,
	)
	if err != nil {
		t.Fatalf("create standard project: %v", err)
	}
	_, err = database.CreateMarketRuleWithinQuota(ctx, CreateMarketRuleParams{
		DeBoxUserID: userID, MarketProjectID: project.ID,
		RuleType: "market_price_above", ThresholdValue: "1",
		ThresholdUnit: "usd", PoolScope: "all",
		NotificationChatID: userID,
	}, policy)
	if !errors.Is(err, ErrMarketPoolMismatch) {
		t.Fatalf("standard all-pool rule error = %v, want ErrMarketPoolMismatch", err)
	}
}
