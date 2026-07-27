package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresMigrationContract(t *testing.T) {
	store, pool := openIntegrationStore(t)
	_ = store

	expected := []string{
		"aggregate_notifications",
		"aggregation_window_members",
		"aggregation_windows",
		"alert_events",
		"auth_challenges",
		"auth_sessions",
		"combination_rule_members",
		"combination_rules",
		"daily_summary_deliveries",
		"daily_summary_targets",
		"market_address_labels",
		"market_asset_deployments",
		"market_asset_identity_evidence",
		"market_assets",
		"market_chain_cursors",
		"market_combination_members",
		"market_combination_rule_projects",
		"market_combination_rules",
		"market_combination_trigger_events",
		"market_combination_window_members",
		"market_combination_windows",
		"market_events",
		"market_holder_snapshots",
		"market_holders",
		"market_pools",
		"market_project_deployments",
		"market_project_pools",
		"market_projects",
		"market_provider_health",
		"market_provider_usage",
		"market_rule_deployments",
		"market_rule_events",
		"market_rule_pools",
		"market_rule_target_states",
		"market_rules",
		"market_scanned_blocks",
		"market_snapshots",
		"market_stage_window_events",
		"market_stage_windows",
		"nodit_webhook_subscriptions",
		"notification_groups",
		"orders",
		"permanent_plan_allowlist",
		"rule_trigger_events",
		"schema_migrations",
		"subscriptions",
		"user_preferences",
		"watch_rules",
		"webhook_inbox",
	}
	rows, err := pool.Query(context.Background(), `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = current_schema()
		ORDER BY table_name
	`)
	if err != nil {
		t.Fatalf("list migrated tables: %v", err)
	}
	got, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (string, error) {
		var name string
		err := row.Scan(&name)
		return name, err
	})
	if err != nil {
		t.Fatalf("collect migrated tables: %v", err)
	}
	if fmt.Sprint(got) != fmt.Sprint(expected) {
		t.Fatalf("migrated tables = %v, want %v", got, expected)
	}
	var latestVersion int64
	var latestName string
	if err := pool.QueryRow(context.Background(), `
		SELECT version, name
		FROM schema_migrations
		ORDER BY version DESC
		LIMIT 1
	`).Scan(&latestVersion, &latestName); err != nil {
		t.Fatalf("read latest migration: %v", err)
	}
	if latestVersion != 13 || latestName != "0013_multichain_market_rules.sql" {
		t.Fatalf("latest migration = %d/%s", latestVersion, latestName)
	}
}

func TestPostgresMultichainMigrationBackfillsLegacyMarketData(t *testing.T) {
	store, pool := openIntegrationStore(t)
	ctx := context.Background()
	tokenAddress := "0x1111111111111111111111111111111111111111"
	quoteAddress := "0x2222222222222222222222222222222222222222"
	holderAddress := "0x3333333333333333333333333333333333333333"

	var poolID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO market_pools (
			chain_key, chain_id, protocol, protocol_version, pool_key,
			pool_address, token0_address, token0_symbol,
			token1_address, token1_symbol,
			supports_event_parsing, parser_adapter, verification_status
		)
		VALUES (
			'bsc', 56, 'pancakeswap', 'v2', $1,
			$1, $2, 'LEGACY', $3, 'USDT',
			1, 'evm_v2', 'verified'
		)
		RETURNING id
	`, "0x4444444444444444444444444444444444444444",
		tokenAddress, quoteAddress).Scan(&poolID); err != nil {
		t.Fatalf("create legacy pool: %v", err)
	}

	var projectID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO market_projects (
			debox_user_id, chain_key, chain_id, token_address,
			token_name, token_symbol, token_decimals, total_supply_raw,
			main_pool_id
		)
		VALUES (
			'multichain-migration-user', 'bsc', 56, $1,
			'Legacy Token', 'LEGACY', 18, 1000000000000000000, $2
		)
		RETURNING id
	`, tokenAddress, poolID).Scan(&projectID); err != nil {
		t.Fatalf("create legacy project: %v", err)
	}

	var projectPoolID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO market_project_pools (
			market_project_id, market_pool_id, selected, is_primary,
			discovery_source
		)
		VALUES ($1, $2, 1, 1, 'integration')
		RETURNING id
	`, projectID, poolID).Scan(&projectPoolID); err != nil {
		t.Fatalf("link legacy project pool: %v", err)
	}

	var ruleID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO market_rules (
			debox_user_id, market_project_id, market_pool_id,
			rule_type, threshold_value, threshold_unit,
			notification_chat_id
		)
		VALUES (
			'multichain-migration-user', $1, $2,
			'market_price_above', 1, 'usd',
			'multichain-migration-user'
		)
		RETURNING id
	`, projectID, poolID).Scan(&ruleID); err != nil {
		t.Fatalf("create legacy market rule: %v", err)
	}

	insertLegacy := func(name, statement string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("create legacy %s: %v", name, err)
		}
	}
	insertLegacy("snapshot", `
		INSERT INTO market_snapshots (
			chain_key, chain_id, token_address, market_pool_id,
			price_usd, source
		)
		VALUES ('bsc', 56, $1, $2, 1, 'integration')
	`, tokenAddress, poolID)
	insertLegacy("event", `
		INSERT INTO market_events (
			market_pool_id, chain_key, chain_id, token_address,
			event_type, event_key, source, occurred_at
		)
		VALUES (
			$2, 'bsc', 56, $1,
			'buy', 'integration:legacy-event', 'integration', NOW()
		)
	`, tokenAddress, poolID)
	insertLegacy("holder", `
		INSERT INTO market_holders (
			chain_key, chain_id, token_address, holder_address,
			balance_raw, balance, source
		)
		VALUES ('bsc', 56, $1, $2, 10, 10, 'integration')
	`, tokenAddress, holderAddress)
	insertLegacy("holder snapshot", `
		INSERT INTO market_holder_snapshots (
			chain_key, chain_id, token_address, holder_address,
			balance_raw, balance, source
		)
		VALUES ('bsc', 56, $1, $2, 10, 10, 'integration')
	`, tokenAddress, holderAddress)
	insertLegacy("address label", `
		INSERT INTO market_address_labels (
			debox_user_id, market_project_id, chain_key, chain_id,
			address, label_type, label
		)
		VALUES (
			'multichain-migration-user', $1, 'bsc', 56,
			$2, 'custom', 'legacy holder'
		)
	`, projectID, holderAddress)

	body, err := migrations.Files.ReadFile("0011_multichain_market_domain.sql")
	if err != nil {
		t.Fatalf("read multi-chain migration: %v", err)
	}
	for attempt := range 2 {
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			t.Fatalf("apply multi-chain migration attempt %d: %v", attempt+1, err)
		}
	}

	var (
		projectCount                  int64
		projectPoolCount              int64
		assetCount                    int64
		deploymentCount               int64
		projectDeploymentCount        int64
		evidenceCount                 int64
		ruleDeploymentScopeCount      int64
		rulePoolScopeCount            int64
		attributedSnapshotCount       int64
		attributedEventCount          int64
		attributedHolderCount         int64
		attributedHolderSnapshotCount int64
		attributedLabelCount          int64
	)
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM market_projects
			 WHERE id = $1 AND market_asset_id IS NOT NULL),
			(SELECT COUNT(*) FROM market_project_pools
			 WHERE id = $2 AND market_project_deployment_id IS NOT NULL),
			(SELECT COUNT(*) FROM market_assets),
			(SELECT COUNT(*) FROM market_asset_deployments),
			(SELECT COUNT(*) FROM market_project_deployments),
			(SELECT COUNT(*) FROM market_asset_identity_evidence),
			(SELECT COUNT(*)
			 FROM market_rule_deployments mrd
			 JOIN market_rules mr ON mr.id = mrd.market_rule_id
			 WHERE mrd.market_rule_id = $3
			   AND mr.deployment_scope = 'selected'
			   AND mr.cooldown_scope = 'chain'),
			(SELECT COUNT(*)
			 FROM market_rule_pools mrp
			 JOIN market_rules mr ON mr.id = mrp.market_rule_id
			 WHERE mrp.market_rule_id = $3 AND mr.pool_scope = 'selected'),
			(SELECT COUNT(*) FROM market_snapshots
			 WHERE market_asset_deployment_id IS NOT NULL),
			(SELECT COUNT(*) FROM market_events
			 WHERE market_asset_deployment_id IS NOT NULL),
			(SELECT COUNT(*) FROM market_holders
			 WHERE market_asset_deployment_id IS NOT NULL),
			(SELECT COUNT(*) FROM market_holder_snapshots
			 WHERE market_asset_deployment_id IS NOT NULL),
			(SELECT COUNT(*) FROM market_address_labels
			 WHERE market_project_deployment_id IS NOT NULL)
	`, projectID, projectPoolID, ruleID).Scan(
		&projectCount,
		&projectPoolCount,
		&assetCount,
		&deploymentCount,
		&projectDeploymentCount,
		&evidenceCount,
		&ruleDeploymentScopeCount,
		&rulePoolScopeCount,
		&attributedSnapshotCount,
		&attributedEventCount,
		&attributedHolderCount,
		&attributedHolderSnapshotCount,
		&attributedLabelCount,
	); err != nil {
		t.Fatalf("read migrated legacy data: %v", err)
	}

	got := []int64{
		projectCount,
		projectPoolCount,
		assetCount,
		deploymentCount,
		projectDeploymentCount,
		evidenceCount,
		ruleDeploymentScopeCount,
		rulePoolScopeCount,
		attributedSnapshotCount,
		attributedEventCount,
		attributedHolderCount,
		attributedHolderSnapshotCount,
		attributedLabelCount,
	}
	for index, count := range got {
		if count != 1 {
			t.Fatalf("migrated count[%d] = %d, want 1; all counts = %v", index, count, got)
		}
	}

	project, err := store.GetMarketProject(
		ctx,
		projectID,
		"multichain-migration-user",
	)
	if err != nil || project == nil || project.MarketAssetID == nil {
		t.Fatalf("read migrated project: project=%#v error=%v", project, err)
	}
	asset, err := store.GetMarketAsset(ctx, *project.MarketAssetID)
	if err != nil || asset == nil ||
		asset.CanonicalAssetID !=
			"eip155:56/erc20:0x1111111111111111111111111111111111111111" ||
		asset.VerificationStatus != "single_chain" {
		t.Fatalf("read migrated asset: asset=%#v error=%v", asset, err)
	}
	deployments, err := store.ListMarketAssetDeployments(ctx, asset.ID)
	if err != nil || len(deployments) != 1 ||
		deployments[0].ChainKey != "bsc" ||
		deployments[0].ChainID != 56 ||
		deployments[0].TokenAddress != tokenAddress {
		t.Fatalf("read migrated deployments: deployments=%#v error=%v", deployments, err)
	}
	projectDeployments, err := store.ListMarketProjectDeployments(
		ctx,
		projectID,
		"multichain-migration-user",
	)
	if err != nil || len(projectDeployments) != 1 ||
		projectDeployments[0].MarketAssetDeploymentID != deployments[0].ID {
		t.Fatalf(
			"read project deployments: deployments=%#v error=%v",
			projectDeployments,
			err,
		)
	}
	evidence, err := store.ListMarketAssetIdentityEvidence(ctx, asset.ID)
	if err != nil || len(evidence) != 1 ||
		evidence[0].Verdict != "supports" ||
		evidence[0].Source != "legacy_migration" {
		t.Fatalf("read identity evidence: evidence=%#v error=%v", evidence, err)
	}
}

func TestPostgresConcurrentMarketProjectQuota(t *testing.T) {
	store, pool := openIntegrationStore(t)
	ctx := context.Background()
	const userID = "integration-market-project-quota"
	if _, err := pool.Exec(ctx, `
		INSERT INTO subscriptions (
			debox_user_id, plan_code, status, starts_at, expires_at
		)
		VALUES ($1, 'standard', 'active', NOW() - INTERVAL '1 hour', NOW() + INTERVAL '1 day')
	`, userID); err != nil {
		t.Fatalf("create standard subscription: %v", err)
	}
	policy := QuotaPolicy{
		PlanCode:               "standard",
		RuleLimit:              10,
		MarketProjectLimit:     1,
		AllowedMarketRuleTypes: []string{"market_price_above"},
	}

	const attempts = 8
	var successes atomic.Int32
	errorsSeen := make(chan error, attempts)
	var workers sync.WaitGroup
	for index := range attempts {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			address := fmt.Sprintf("0x%040x", index+1)
			_, err := store.CreateMarketProjectWithinQuota(ctx, CreateMarketProjectParams{
				DeBoxUserID:   userID,
				ChainKey:      "bsc",
				ChainID:       56,
				TokenAddress:  address,
				TokenName:     fmt.Sprintf("Token %d", index+1),
				TokenSymbol:   fmt.Sprintf("T%d", index+1),
				TokenDecimals: 18,
			}, policy)
			if err == nil {
				successes.Add(1)
				return
			}
			errorsSeen <- err
		}(index)
	}
	workers.Wait()
	close(errorsSeen)

	if got := successes.Load(); got != 1 {
		t.Fatalf("successful concurrent market projects = %d, want 1", got)
	}
	for err := range errorsSeen {
		if !errors.Is(err, ErrMarketProjectLimitReached) {
			t.Fatalf(
				"concurrent market project error = %v, want ErrMarketProjectLimitReached",
				err,
			)
		}
	}
}

func TestPostgresWalletAndMarketRulesShareQuota(t *testing.T) {
	store, pool := openIntegrationStore(t)
	ctx := context.Background()
	const userID = "integration-shared-rule-quota"
	if _, err := pool.Exec(ctx, `
		INSERT INTO subscriptions (
			debox_user_id, plan_code, status, starts_at, expires_at
		)
		VALUES ($1, 'standard', 'active', NOW() - INTERVAL '1 hour', NOW() + INTERVAL '1 day')
	`, userID); err != nil {
		t.Fatalf("create standard subscription: %v", err)
	}
	policy := QuotaPolicy{
		PlanCode:               "standard",
		WalletLimit:            3,
		RuleLimit:              10,
		MarketProjectLimit:     1,
		AllowedRuleTypes:       []string{"balance_change"},
		AllowedMarketRuleTypes: []string{"market_price_above"},
	}
	project, err := store.CreateMarketProjectWithinQuota(ctx, CreateMarketProjectParams{
		DeBoxUserID:   userID,
		ChainKey:      "bsc",
		ChainID:       56,
		TokenAddress:  "0x1111111111111111111111111111111111111111",
		TokenName:     "Integration Token",
		TokenSymbol:   "INT",
		TokenDecimals: 18,
	}, policy)
	if err != nil {
		t.Fatalf("create market project: %v", err)
	}
	for index := 0; index < 9; index++ {
		if _, err := store.CreateWatchRuleWithinQuota(ctx, CreateWatchRuleParams{
			DeBoxUserID:          userID,
			ChainKey:             "bsc",
			ChainID:              56,
			WalletAddress:        "0x2222222222222222222222222222222222222222",
			RuleType:             "balance_change",
			Threshold:            fmt.Sprint(index),
			NotificationChatID:   userID,
			NotificationChatType: "private",
		}, policy); err != nil {
			t.Fatalf("create wallet rule %d: %v", index+1, err)
		}
	}
	if _, err := store.CreateMarketRuleWithinQuota(ctx, CreateMarketRuleParams{
		DeBoxUserID:          userID,
		MarketProjectID:      project.ID,
		RuleType:             "market_price_above",
		ThresholdValue:       "1.25",
		ThresholdUnit:        "usd",
		NotificationChatID:   userID,
		NotificationChatType: "private",
	}, policy); err != nil {
		t.Fatalf("create tenth shared rule: %v", err)
	}
	if _, err := store.CreateWatchRuleWithinQuota(ctx, CreateWatchRuleParams{
		DeBoxUserID:          userID,
		ChainKey:             "bsc",
		ChainID:              56,
		WalletAddress:        "0x2222222222222222222222222222222222222222",
		RuleType:             "balance_change",
		Threshold:            "99",
		NotificationChatID:   userID,
		NotificationChatType: "private",
	}, policy); !errors.Is(err, ErrRuleLimitReached) {
		t.Fatalf("eleventh shared rule error = %v, want ErrRuleLimitReached", err)
	}
}

func TestPostgresMarketObservationsAreSharedAcrossUsers(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	const (
		firstUser   = "integration-market-shared-1"
		secondUser  = "integration-market-shared-2"
		token       = "0x1111111111111111111111111111111111111111"
		wbnb        = "0xbb4cdb9cbd36b01bd1cbaebf2de08d9173bc095c"
		poolAddress = "0x3333333333333333333333333333333333333333"
		holder      = "0x4444444444444444444444444444444444444444"
	)
	if _, err := pool.Exec(ctx, `
		INSERT INTO subscriptions (
			debox_user_id, plan_code, status, starts_at, expires_at
		)
		VALUES
			($1, 'standard', 'active', NOW() - INTERVAL '1 hour', NOW() + INTERVAL '1 day'),
			($2, 'standard', 'active', NOW() - INTERVAL '1 hour', NOW() + INTERVAL '1 day')
	`, firstUser, secondUser); err != nil {
		t.Fatalf("create shared market subscriptions: %v", err)
	}
	policy := QuotaPolicy{
		PlanCode:               "standard",
		RuleLimit:              10,
		MarketProjectLimit:     1,
		AllowedMarketRuleTypes: []string{"market_price_above"},
	}
	createProject := func(userID string) MarketProject {
		project, err := database.CreateMarketProjectWithinQuota(ctx, CreateMarketProjectParams{
			DeBoxUserID:   userID,
			ChainKey:      "bsc",
			ChainID:       56,
			TokenAddress:  token,
			TokenName:     "Shared Token",
			TokenSymbol:   "SHR",
			TokenDecimals: 18,
		}, policy)
		if err != nil {
			t.Fatalf("create shared project for %s: %v", userID, err)
		}
		return project
	}
	firstProject := createProject(firstUser)
	secondProject := createProject(secondUser)
	label, err := database.UpsertMarketAddressLabel(ctx, UpsertMarketAddressLabelParams{
		DeBoxUserID:     firstUser,
		MarketProjectID: firstProject.ID,
		Address:         holder,
		LabelType:       "team",
		Label:           "Team wallet",
		Excluded:        true,
	})
	if err != nil || label.DeBoxUserID != firstUser || label.Excluded != 1 {
		t.Fatalf("upsert user market label = %#v err:%v", label, err)
	}
	labels, err := database.ListMarketAddressLabels(ctx, firstProject.ID, firstUser)
	if err != nil || len(labels) != 1 || labels[0].ID != label.ID {
		t.Fatalf("list user market labels = %#v err:%v", labels, err)
	}
	labels, err = database.ListMarketAddressLabels(ctx, firstProject.ID, secondUser)
	if err != nil || len(labels) != 0 {
		t.Fatalf("cross-user market labels = %#v err:%v", labels, err)
	}

	marketPool, err := database.UpsertMarketPool(ctx, UpsertMarketPoolParams{
		ChainKey:        "bsc",
		ChainID:         56,
		Protocol:        "pancakeswap",
		ProtocolVersion: "v2",
		PoolKey:         poolAddress,
		PoolAddress:     pointerOf(poolAddress),
		Token0Address:   token,
		Token0Symbol:    "SHR",
		Token0Decimals:  18,
		Token1Address:   wbnb,
		Token1Symbol:    "WBNB",
		Token1Decimals:  18,
		LiquidityUSD:    "100000",
	})
	if err != nil {
		t.Fatalf("upsert shared market pool: %v", err)
	}
	fourMemePool, err := database.UpsertMarketPool(ctx, UpsertMarketPoolParams{
		ChainKey:        "bsc",
		ChainID:         56,
		Protocol:        "fourmeme",
		ProtocolVersion: "bonding",
		PoolKey:         token + ":4meme",
		PoolAddress:     nil,
		Token0Address:   token,
		Token0Symbol:    "SHR",
		Token0Decimals:  18,
		Token1Address:   wbnb,
		Token1Symbol:    "WBNB",
		Token1Decimals:  18,
		LiquidityUSD:    "0",
	})
	if err != nil || fourMemePool.PoolKey != token+":4meme" ||
		fourMemePool.PoolAddress != nil {
		t.Fatalf("upsert Four.meme pseudo pool = %#v err:%v", fourMemePool, err)
	}
	for _, project := range []MarketProject{firstProject, secondProject} {
		if _, err := database.LinkMarketProjectPool(ctx, LinkMarketProjectPoolParams{
			DeBoxUserID:     project.DeBoxUserID,
			MarketProjectID: project.ID,
			MarketPoolID:    marketPool.ID,
			Selected:        true,
			IsPrimary:       true,
			DiscoverySource: "integration",
		}); err != nil {
			t.Fatalf("link shared pool for %s: %v", project.DeBoxUserID, err)
		}
	}
	secondPoolAddress := "0x5555555555555555555555555555555555555555"
	secondPool, err := database.UpsertMarketPool(ctx, UpsertMarketPoolParams{
		ChainKey:        "bsc",
		ChainID:         56,
		Protocol:        "pancakeswap",
		ProtocolVersion: "v3",
		PoolKey:         secondPoolAddress,
		PoolAddress:     &secondPoolAddress,
		Token0Address:   token,
		Token0Symbol:    "SHR",
		Token0Decimals:  18,
		Token1Address:   wbnb,
		Token1Symbol:    "WBNB",
		Token1Decimals:  18,
		LiquidityUSD:    "50000",
	})
	if err != nil {
		t.Fatalf("upsert second market pool: %v", err)
	}
	if _, err := database.LinkMarketProjectPool(ctx, LinkMarketProjectPoolParams{
		DeBoxUserID:     firstUser,
		MarketProjectID: firstProject.ID,
		MarketPoolID:    secondPool.ID,
		DiscoverySource: "integration",
	}); err != nil {
		t.Fatalf("discover second market pool: %v", err)
	}
	if _, err := database.LinkMarketProjectPoolWithinQuota(
		ctx,
		LinkMarketProjectPoolParams{
			DeBoxUserID:     firstUser,
			MarketProjectID: firstProject.ID,
			MarketPoolID:    secondPool.ID,
			Selected:        true,
			DiscoverySource: "integration",
		},
		policy,
	); err != nil {
		t.Fatalf("select standard plan main pool: %v", err)
	}
	var selectedPoolCount, mainPoolID int64
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*)
			 FROM market_project_pools
			 WHERE market_project_id = $1 AND selected = 1),
			main_pool_id
		FROM market_projects
		WHERE id = $1
	`, firstProject.ID).Scan(&selectedPoolCount, &mainPoolID); err != nil {
		t.Fatalf("read standard selected pools: %v", err)
	}
	if selectedPoolCount != 1 || mainPoolID != secondPool.ID {
		t.Fatalf(
			"standard selected pools = count:%d main:%d, want count:1 main:%d",
			selectedPoolCount,
			mainPoolID,
			secondPool.ID,
		)
	}

	if _, err := database.CreateMarketSnapshot(ctx, CreateMarketSnapshotParams{
		ChainKey:     "bsc",
		ChainID:      56,
		TokenAddress: token,
		MarketPoolID: marketPool.ID,
		PriceUSD:     pointerOf("0.0125"),
		LiquidityUSD: pointerOf("100000"),
		Source:       "integration",
	}); err != nil {
		t.Fatalf("create shared market snapshot: %v", err)
	}
	if _, err := database.UpsertMarketHolder(ctx, UpsertMarketHolderParams{
		ChainKey:       "bsc",
		ChainID:        56,
		TokenAddress:   token,
		HolderAddress:  holder,
		BalanceRaw:     "1000000000000000000000",
		Balance:        "1000",
		SupplyPercent:  pointerOf("1"),
		Rank:           pointerOf(int32(1)),
		Source:         "integration",
		RecordSnapshot: true,
	}); err != nil {
		t.Fatalf("upsert shared market holder: %v", err)
	}
	event, created, err := database.CreateMarketEvent(ctx, CreateMarketEventParams{
		MarketPoolID: pointerOf(marketPool.ID),
		ChainKey:     "bsc",
		ChainID:      56,
		TokenAddress: token,
		EventType:    "buy",
		EventKey:     "integration:shared:buy:1",
		USDValue:     pointerOf("2500"),
		Source:       "integration",
		Confidence:   "1",
		OccurredAt:   time.Now().UTC(),
	})
	if err != nil || !created {
		t.Fatalf("create shared market event = created:%v err:%v", created, err)
	}
	duplicate, created, err := database.CreateMarketEvent(ctx, CreateMarketEventParams{
		MarketPoolID: pointerOf(marketPool.ID),
		ChainKey:     "bsc",
		ChainID:      56,
		TokenAddress: token,
		EventType:    "buy",
		EventKey:     "integration:shared:buy:1",
		USDValue:     pointerOf("2500"),
		Source:       "integration",
		Confidence:   "1",
		OccurredAt:   time.Now().UTC(),
	})
	if err != nil || created || duplicate.ID != event.ID {
		t.Fatalf("duplicate shared event = %#v created:%v err:%v", duplicate, created, err)
	}
	for _, project := range []MarketProject{firstProject, secondProject} {
		rule, err := database.CreateMarketRuleWithinQuota(ctx, CreateMarketRuleParams{
			DeBoxUserID:          project.DeBoxUserID,
			MarketProjectID:      project.ID,
			RuleType:             "market_price_above",
			ThresholdValue:       "0.01",
			ThresholdUnit:        "usd",
			NotificationChatID:   project.DeBoxUserID,
			NotificationChatType: "private",
		}, policy)
		if err != nil {
			t.Fatalf("create distributed market rule for %s: %v", project.DeBoxUserID, err)
		}
		ruleEvent, inserted, err := database.CreateMarketRuleEvent(
			ctx,
			CreateMarketRuleEventParams{
				MarketRuleID:  rule.ID,
				MarketEventID: event.ID,
				TriggerKey:    fmt.Sprintf("rule:%d:event:%d", rule.ID, event.ID),
			},
		)
		if err != nil || !inserted {
			t.Fatalf(
				"create distributed rule event for %s = %#v inserted:%v err:%v",
				project.DeBoxUserID,
				ruleEvent,
				inserted,
				err,
			)
		}
		duplicateRuleEvent, inserted, err := database.CreateMarketRuleEvent(
			ctx,
			CreateMarketRuleEventParams{
				MarketRuleID:  rule.ID,
				MarketEventID: event.ID,
				TriggerKey:    fmt.Sprintf("rule:%d:event:%d", rule.ID, event.ID),
			},
		)
		if err != nil || inserted || duplicateRuleEvent.ID != ruleEvent.ID {
			t.Fatalf(
				"duplicate distributed rule event for %s = %#v inserted:%v err:%v",
				project.DeBoxUserID,
				duplicateRuleEvent,
				inserted,
				err,
			)
		}
	}

	for _, test := range []struct {
		userID    string
		projectID int64
	}{
		{firstUser, firstProject.ID},
		{secondUser, secondProject.ID},
	} {
		snapshots, err := database.ListMarketSnapshots(ctx, test.projectID, test.userID, 10)
		if err != nil || len(snapshots) != 1 {
			t.Fatalf("shared snapshots for %s = %v err:%v", test.userID, snapshots, err)
		}
		holders, err := database.ListMarketHolders(ctx, test.projectID, test.userID, false, 10)
		if err != nil || len(holders) != 1 || holders[0].HolderAddress != holder {
			t.Fatalf("shared holders for %s = %v err:%v", test.userID, holders, err)
		}
		events, err := database.ListMarketEvents(ctx, test.projectID, test.userID, 0, 10)
		if err != nil || len(events) != 1 || events[0].ID != event.ID {
			t.Fatalf("shared events for %s = %v err:%v", test.userID, events, err)
		}
	}
	events, err := database.ListMarketEvents(ctx, firstProject.ID, secondUser, 0, 10)
	if err != nil || len(events) != 0 {
		t.Fatalf("cross-user market event access = %v err:%v", events, err)
	}
}

func TestPostgresMarketIngestionStateIsIdempotent(t *testing.T) {
	database, _ := openIntegrationStore(t)
	ctx := context.Background()

	cursor, err := database.AdvanceMarketChainCursor(ctx, AdvanceMarketChainCursorParams{
		ChainKey:        "bsc",
		ChainID:         56,
		CursorKey:       "market-logs",
		NextBlockNumber: 101,
		SafeBlockNumber: 95,
		Status:          "active",
	})
	if err != nil || cursor.NextBlockNumber != 101 || cursor.SafeBlockNumber != 95 {
		t.Fatalf("initial market cursor = %#v err:%v", cursor, err)
	}
	cursor, err = database.AdvanceMarketChainCursor(ctx, AdvanceMarketChainCursorParams{
		ChainKey:        "bsc",
		ChainID:         56,
		CursorKey:       "market-logs",
		NextBlockNumber: 90,
		SafeBlockNumber: 80,
		Status:          "active",
	})
	if err != nil || cursor.NextBlockNumber != 101 || cursor.SafeBlockNumber != 95 {
		t.Fatalf("non-regressing market cursor = %#v err:%v", cursor, err)
	}
	cursor, err = database.RewindMarketChainCursor(
		ctx,
		56,
		"market-logs",
		88,
		nil,
		"integration reorg",
	)
	if err != nil || cursor.NextBlockNumber != 88 || cursor.SafeBlockNumber != 88 {
		t.Fatalf("rewound market cursor = %#v err:%v", cursor, err)
	}

	syncedAt := time.Now().UTC()
	webhook, err := database.UpsertNoditWebhookSubscription(
		ctx,
		UpsertNoditWebhookSubscriptionParams{
			Provider:        "nodit",
			ExternalID:      pointerOf("integration-webhook"),
			ChainKey:        "bsc",
			ChainID:         56,
			EventCategory:   "v2-v3-swaps",
			CallbackURLHash: "sha256:integration",
			SecretReference: "env:NODIT_WEBHOOK_SECRET",
			Status:          "active",
			Configuration:   []byte(`{"event":"LOG"}`),
			LastSyncedAt:    &syncedAt,
		},
	)
	if err != nil || webhook.Status != "active" {
		t.Fatalf("upsert webhook subscription = %#v err:%v", webhook, err)
	}
	params := CreateWebhookInboxParams{
		WebhookSubscriptionID: &webhook.ID,
		Provider:              "nodit",
		ChainKey:              "bsc",
		ChainID:               56,
		DeliveryID:            "delivery-1",
		DedupeKey:             "delivery-1",
		SignatureValid:        true,
		Headers:               []byte(`{"content-type":"application/json"}`),
		RawBody:               []byte(`{"event":"LOG"}`),
		Payload:               []byte(`{"event":"LOG"}`),
	}
	message, inserted, err := database.CreateWebhookInboxMessage(ctx, params)
	if err != nil || !inserted || message.ProcessingStatus != "pending" {
		t.Fatalf("create webhook inbox = %#v inserted:%v err:%v", message, inserted, err)
	}
	duplicate, inserted, err := database.CreateWebhookInboxMessage(ctx, params)
	if err != nil || inserted || duplicate.ID != message.ID {
		t.Fatalf("duplicate webhook inbox = %#v inserted:%v err:%v", duplicate, inserted, err)
	}
	baseParams := params
	baseParams.WebhookSubscriptionID = nil
	baseParams.ChainKey = "base"
	baseParams.ChainID = 8453
	baseParams.DeliveryID = "delivery-base-1"
	baseParams.DedupeKey = "base:delivery-1"
	baseMessage, inserted, err := database.CreateWebhookInboxMessage(ctx, baseParams)
	if err != nil || !inserted {
		t.Fatalf("create Base webhook inbox = %#v inserted:%v err:%v", baseMessage, inserted, err)
	}
	claimed, err := database.ClaimWebhookInboxMessages(ctx, 56, 10)
	if err != nil || len(claimed) != 1 ||
		claimed[0].ID != message.ID ||
		claimed[0].ProcessingStatus != "processing" ||
		claimed[0].Attempts != 1 {
		t.Fatalf("claimed webhook inbox = %#v err:%v", claimed, err)
	}
	processed, err := database.MarkWebhookInboxProcessed(ctx, message.ID)
	if err != nil || processed.ProcessingStatus != "processed" ||
		processed.ProcessedAt == nil {
		t.Fatalf("processed webhook inbox = %#v err:%v", processed, err)
	}
	baseClaimed, err := database.ClaimWebhookInboxMessages(ctx, 8453, 10)
	if err != nil || len(baseClaimed) != 1 || baseClaimed[0].ID != baseMessage.ID {
		t.Fatalf("claimed Base webhook inbox = %#v err:%v", baseClaimed, err)
	}

	blockHashA := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	blockHashB := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	parentHash := "0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if _, err := database.UpsertMarketScannedBlock(ctx, UpsertMarketScannedBlockParams{
		ChainKey:    "bsc",
		ChainID:     56,
		CursorKey:   "market-logs",
		BlockNumber: 87,
		BlockHash:   blockHashA,
		ParentHash:  parentHash,
	}); err != nil {
		t.Fatalf("upsert scanned block: %v", err)
	}
	if _, err := database.UpsertMarketScannedBlock(ctx, UpsertMarketScannedBlockParams{
		ChainKey:    "bsc",
		ChainID:     56,
		CursorKey:   "market-logs",
		BlockNumber: 87,
		BlockHash:   blockHashB,
		ParentHash:  parentHash,
	}); err != nil {
		t.Fatalf("replace scanned block: %v", err)
	}
	blocks, err := database.ListCanonicalMarketScannedBlocks(
		ctx,
		56,
		"market-logs",
		87,
		10,
	)
	if err != nil || len(blocks) != 1 || blocks[0].BlockHash != blockHashB {
		t.Fatalf("canonical scanned blocks = %#v err:%v", blocks, err)
	}

	for attempt := 0; attempt < 3; attempt++ {
		health, err := database.RecordMarketProviderHealth(
			ctx,
			RecordMarketProviderHealthParams{
				Provider:  "nodit",
				Component: "integration",
				ChainKey:  "bsc",
				ChainID:   56,
				Success:   false,
				Error:     "integration failure",
			},
		)
		if err != nil {
			t.Fatalf("record provider failure: %v", err)
		}
		if attempt == 2 && health.Status != "unavailable" {
			t.Fatalf("provider health after failures = %#v", health)
		}
	}
	usage, err := database.AddMarketProviderUsage(ctx, AddMarketProviderUsageParams{
		Provider:    "nodit",
		Metric:      "integration_cu",
		PeriodStart: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		PeriodEnd:   time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		DeltaUnits:  "75",
		LimitUnits:  "100",
	})
	if err != nil || usage.AlertLevel != 70 || usage.UsagePercent == nil ||
		*usage.UsagePercent != "75.000" {
		t.Fatalf("provider usage = %#v err:%v", usage, err)
	}
}

func TestPostgresConcurrentFreeRuleQuota(t *testing.T) {
	store, _ := openIntegrationStore(t)
	const attempts = 8
	policy := QuotaPolicy{
		PlanCode:         "free",
		WalletLimit:      1,
		RuleLimit:        1,
		AllowedRuleTypes: []string{"balance_change"},
	}
	params := CreateWatchRuleParams{
		DeBoxUserID:          "integration-free-user",
		ChainKey:             "bsc",
		ChainID:              56,
		WalletAddress:        "0x1111111111111111111111111111111111111111",
		RuleType:             "balance_change",
		Threshold:            "0",
		NotificationChatID:   "integration-free-user",
		NotificationChatType: "private",
		NotificationLanguage: "zh",
	}

	var successes atomic.Int32
	errorsSeen := make(chan error, attempts)
	var workers sync.WaitGroup
	for range attempts {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, err := store.CreateWatchRuleWithinQuota(context.Background(), params, policy)
			if err == nil {
				successes.Add(1)
				return
			}
			errorsSeen <- err
		}()
	}
	workers.Wait()
	close(errorsSeen)

	if got := successes.Load(); got != 1 {
		t.Fatalf("successful concurrent creates = %d, want 1", got)
	}
	for err := range errorsSeen {
		if !errors.Is(err, ErrRuleLimitReached) {
			t.Fatalf("concurrent create error = %v, want ErrRuleLimitReached", err)
		}
	}
}

func TestPostgresStageTriggerSendsOncePerWindow(t *testing.T) {
	store, pool := openIntegrationStore(t)
	ctx := context.Background()
	rule, err := store.CreateWatchRule(ctx, CreateWatchRuleParams{
		DeBoxUserID:           "integration-stage-user",
		ChainKey:              "bsc",
		ChainID:               56,
		WalletAddress:         "0x1111111111111111111111111111111111111111",
		RuleType:              "balance_change",
		Threshold:             "0",
		NotificationChatID:    "integration-stage-user",
		NotificationChatType:  "private",
		NotificationLanguage:  "zh",
		DeliveryMode:          "stage",
		CycleType:             "fixed",
		CycleMinutes:          60,
		TriggerCountThreshold: 2,
	})
	if err != nil {
		t.Fatalf("create stage rule: %v", err)
	}

	for index, wantDue := range []bool{false, true, false} {
		previous := fmt.Sprint(index)
		current := fmt.Sprint(index + 1)
		result, err := store.RecordStageTrigger(ctx, RecordStageTriggerParams{
			WatchRuleID:   rule.ID,
			DeBoxUserID:   rule.DeBoxUserID,
			PreviousValue: &previous,
			CurrentValue:  &current,
			Note:          "integration event",
		})
		if err != nil {
			t.Fatalf("record stage trigger %d: %v", index+1, err)
		}
		if result.TotalTriggerCount != int64(index+1) ||
			result.NotificationDue != wantDue {
			t.Fatalf("stage trigger %d = %#v", index+1, result)
		}
	}

	var eventCount, notificationCount int64
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM rule_trigger_events WHERE watch_rule_id = $1),
			(SELECT COUNT(*)
			 FROM aggregate_notifications an
			 JOIN aggregation_windows aw ON aw.id = an.aggregation_window_id
			 WHERE aw.watch_rule_id = $1)
	`, rule.ID).Scan(&eventCount, &notificationCount); err != nil {
		t.Fatalf("count stage records: %v", err)
	}
	if eventCount != 3 || notificationCount != 1 {
		t.Fatalf("stage records = events:%d notifications:%d", eventCount, notificationCount)
	}
}

func TestPostgresCombinationSendsAfterEveryMemberReachesThreshold(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	userID := "integration-combination-user"
	if _, err := pool.Exec(ctx, `
		INSERT INTO subscriptions (
			debox_user_id, plan_code, status, starts_at, expires_at
		)
		VALUES ($1, 'professional', 'active', NOW() - INTERVAL '1 hour', NOW() + INTERVAL '1 day')
	`, userID); err != nil {
		t.Fatalf("create professional subscription: %v", err)
	}
	memberRule := func(wallet string) CreateCombinationMemberParams {
		return CreateCombinationMemberParams{
			Rule: CreateWatchRuleParams{
				DeBoxUserID:   userID,
				ChainKey:      "bsc",
				ChainID:       56,
				WalletAddress: wallet,
				RuleType:      "balance_change",
				Threshold:     "0",
			},
			RequiredTriggerCount: 1,
		}
	}
	first := memberRule("0x1111111111111111111111111111111111111111")
	first.RequiredTriggerCount = 2
	combination, err := database.CreateCombinationRuleWithinQuota(
		ctx,
		CreateCombinationRuleParams{
			DeBoxUserID:          userID,
			Note:                 "integration combination",
			CycleType:            "fixed",
			CycleMinutes:         60,
			NotificationChatID:   userID,
			NotificationChatType: "private",
			NotificationLanguage: "en",
			Members: []CreateCombinationMemberParams{
				first,
				memberRule("0x2222222222222222222222222222222222222222"),
			},
		},
		QuotaPolicy{
			PlanCode:          "professional",
			WalletLimit:       20,
			RuleLimit:         100,
			AllowedRuleTypes:  []string{"balance_change"},
			GroupNotification: true,
			CombinationRules:  true,
		},
	)
	if err != nil {
		t.Fatalf("create combination rule: %v", err)
	}
	if len(combination.Members) != 2 {
		t.Fatalf("combination members = %d, want 2", len(combination.Members))
	}

	sequence := []struct {
		memberIndex int
		wantDue     bool
	}{
		{memberIndex: 0, wantDue: false},
		{memberIndex: 0, wantDue: false},
		{memberIndex: 1, wantDue: true},
		{memberIndex: 1, wantDue: false},
	}
	for index, item := range sequence {
		current := fmt.Sprint(index + 1)
		result, err := database.RecordCombinationTrigger(ctx, RecordCombinationTriggerParams{
			WatchRuleID:  combination.Members[item.memberIndex].WatchRuleID,
			DeBoxUserID:  userID,
			CurrentValue: &current,
			Note:         "combination event",
		})
		if err != nil {
			t.Fatalf("record combination trigger %d: %v", index+1, err)
		}
		if result.NotificationDue != item.wantDue {
			t.Fatalf("trigger %d due = %v, want %v", index+1, result.NotificationDue, item.wantDue)
		}
	}

	var eventCount, notificationCount int64
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*)
			 FROM rule_trigger_events rte
			 JOIN aggregation_windows aw ON aw.id = rte.aggregation_window_id
			 WHERE aw.combination_rule_id = $1),
			(SELECT COUNT(*)
			 FROM aggregate_notifications an
			 JOIN aggregation_windows aw ON aw.id = an.aggregation_window_id
			 WHERE aw.combination_rule_id = $1)
	`, combination.ID).Scan(&eventCount, &notificationCount); err != nil {
		t.Fatalf("count combination records: %v", err)
	}
	if eventCount != 4 || notificationCount != 1 {
		t.Fatalf("combination records = events:%d notifications:%d", eventCount, notificationCount)
	}

	history, err := database.ListAggregationEventHistory(ctx, userID, 0, 10)
	if err != nil {
		t.Fatalf("list combination history: %v", err)
	}
	if len(history.Events) != 4 ||
		history.Stats.EventCount != 4 ||
		history.Stats.CombinationEventCount != 4 ||
		history.Stats.StageEventCount != 0 {
		t.Fatalf("combination history = %#v", history)
	}
	for _, event := range history.Events {
		if event.SourceType != "combination" ||
			event.CombinationRuleID == nil ||
			*event.CombinationRuleID != combination.ID ||
			event.CombinationNote != combination.Note ||
			event.NotificationKind != "combination" {
			t.Fatalf("combination history event = %#v", event)
		}
	}
}

func TestPostgresGroupDeletionFallsBackAllRuleTargetsToPrivate(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	userID := "integration-group-fallback-user"
	groupID := "integration-group"
	if _, err := pool.Exec(ctx, `
		INSERT INTO subscriptions (
			debox_user_id, plan_code, status, starts_at, expires_at
		)
		VALUES ($1, 'professional', 'active', NOW() - INTERVAL '1 hour', NOW() + INTERVAL '1 day')
	`, userID); err != nil {
		t.Fatalf("create professional subscription: %v", err)
	}
	group, err := database.CreateNotificationGroup(ctx, userID, groupID, "Integration Group")
	if err != nil {
		t.Fatalf("create notification group: %v", err)
	}
	standalone, err := database.CreateWatchRule(ctx, CreateWatchRuleParams{
		DeBoxUserID:          userID,
		ChainKey:             "bsc",
		ChainID:              56,
		WalletAddress:        "0x1111111111111111111111111111111111111111",
		RuleType:             "balance_change",
		Threshold:            "0",
		NotificationChatID:   groupID,
		NotificationChatType: "group",
		NotificationLabel:    group.Name,
	})
	if err != nil {
		t.Fatalf("create standalone rule: %v", err)
	}
	member := func(wallet string) CreateCombinationMemberParams {
		return CreateCombinationMemberParams{
			Rule: CreateWatchRuleParams{
				DeBoxUserID:   userID,
				ChainKey:      "bsc",
				ChainID:       56,
				WalletAddress: wallet,
				RuleType:      "balance_change",
				Threshold:     "0",
			},
			RequiredTriggerCount: 1,
		}
	}
	combination, err := database.CreateCombinationRuleWithinQuota(
		ctx,
		CreateCombinationRuleParams{
			DeBoxUserID:          userID,
			Note:                 "group fallback combination",
			CycleType:            "fixed",
			CycleMinutes:         60,
			NotificationChatID:   groupID,
			NotificationChatType: "group",
			NotificationLabel:    group.Name,
			Members: []CreateCombinationMemberParams{
				member("0x2222222222222222222222222222222222222222"),
				member("0x3333333333333333333333333333333333333333"),
			},
		},
		QuotaPolicy{
			PlanCode:          "professional",
			WalletLimit:       20,
			RuleLimit:         100,
			AllowedRuleTypes:  []string{"balance_change"},
			GroupNotification: true,
			CombinationRules:  true,
		},
	)
	if err != nil {
		t.Fatalf("create combination rule: %v", err)
	}

	if _, err := database.DeleteNotificationGroup(ctx, group.ID, userID); err != nil {
		t.Fatalf("delete notification group: %v", err)
	}

	var standaloneChatID, standaloneChatType, standaloneLabel string
	if err := pool.QueryRow(ctx, `
		SELECT notification_chat_id, notification_chat_type, notification_label
		FROM watch_rules
		WHERE id = $1
	`, standalone.ID).Scan(
		&standaloneChatID,
		&standaloneChatType,
		&standaloneLabel,
	); err != nil {
		t.Fatalf("read standalone fallback: %v", err)
	}
	if standaloneChatID != userID ||
		standaloneChatType != "private" ||
		standaloneLabel != "私聊通知" {
		t.Fatalf(
			"standalone fallback = %q/%q/%q",
			standaloneChatID,
			standaloneChatType,
			standaloneLabel,
		)
	}

	var combinationChatID, combinationChatType, combinationLabel string
	if err := pool.QueryRow(ctx, `
		SELECT notification_chat_id, notification_chat_type, notification_label
		FROM combination_rules
		WHERE id = $1
	`, combination.ID).Scan(
		&combinationChatID,
		&combinationChatType,
		&combinationLabel,
	); err != nil {
		t.Fatalf("read combination fallback: %v", err)
	}
	if combinationChatID != userID ||
		combinationChatType != "private" ||
		combinationLabel != "私聊通知" {
		t.Fatalf(
			"combination fallback = %q/%q/%q",
			combinationChatID,
			combinationChatType,
			combinationLabel,
		)
	}
	for _, combinationMember := range combination.Members {
		var memberChatID, memberChatType, memberLabel string
		if err := pool.QueryRow(ctx, `
			SELECT notification_chat_id, notification_chat_type, notification_label
			FROM watch_rules
			WHERE id = $1
		`, combinationMember.WatchRuleID).Scan(
			&memberChatID,
			&memberChatType,
			&memberLabel,
		); err != nil {
			t.Fatalf("read combination member fallback: %v", err)
		}
		if memberChatID != userID ||
			memberChatType != "private" ||
			memberLabel != "私聊通知" {
			t.Fatalf(
				"combination member fallback = %q/%q/%q",
				memberChatID,
				memberChatType,
				memberLabel,
			)
		}
	}
}

func TestPostgresAggregationHistoryIsScopedPaginatedAndLimitedToThirtyDays(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	userID := "integration-history-user"
	rule, err := database.CreateWatchRule(ctx, CreateWatchRuleParams{
		DeBoxUserID:           userID,
		ChainKey:              "bsc",
		ChainID:               56,
		WalletAddress:         "0x1111111111111111111111111111111111111111",
		RuleType:              "balance_change",
		Threshold:             "0",
		NotificationChatID:    userID,
		NotificationChatType:  "private",
		NotificationLanguage:  "zh",
		DeliveryMode:          "stage",
		CycleType:             "fixed",
		CycleMinutes:          60,
		TriggerCountThreshold: 2,
	})
	if err != nil {
		t.Fatalf("create history stage rule: %v", err)
	}

	eventIDs := make([]int64, 0, 4)
	for index := range 4 {
		previous := fmt.Sprint(index)
		current := fmt.Sprint(index + 1)
		result, err := database.RecordStageTrigger(ctx, RecordStageTriggerParams{
			WatchRuleID:   rule.ID,
			DeBoxUserID:   userID,
			PreviousValue: &previous,
			CurrentValue:  &current,
			Note:          fmt.Sprintf("history event %d", index+1),
		})
		if err != nil {
			t.Fatalf("record history event %d: %v", index+1, err)
		}
		eventIDs = append(eventIDs, result.TriggerEventID)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE rule_trigger_events
		SET created_at = NOW() - INTERVAL '31 days',
		    detected_at = NOW() - INTERVAL '31 days'
		WHERE id = $1
	`, eventIDs[0]); err != nil {
		t.Fatalf("age first history event: %v", err)
	}

	firstPage, err := database.ListAggregationEventHistory(ctx, userID, 0, 2)
	if err != nil {
		t.Fatalf("list first history page: %v", err)
	}
	if len(firstPage.Events) != 2 ||
		!firstPage.HasMore ||
		firstPage.NextBeforeID == nil ||
		firstPage.RetentionDays != 30 {
		t.Fatalf("first history page = %#v", firstPage)
	}
	if firstPage.Stats.EventCount != 3 ||
		firstPage.Stats.StageEventCount != 3 ||
		firstPage.Stats.CombinationEventCount != 0 ||
		firstPage.Stats.NotificationCount != 1 {
		t.Fatalf("history stats = %#v", firstPage.Stats)
	}
	for _, event := range firstPage.Events {
		if event.SourceType != "rule" ||
			event.WatchRuleID != rule.ID ||
			event.NotificationKind != "stage" ||
			event.Note == "" {
			t.Fatalf("history event = %#v", event)
		}
	}

	secondPage, err := database.ListAggregationEventHistory(
		ctx,
		userID,
		*firstPage.NextBeforeID,
		2,
	)
	if err != nil {
		t.Fatalf("list second history page: %v", err)
	}
	if len(secondPage.Events) != 1 ||
		secondPage.HasMore ||
		secondPage.NextBeforeID != nil {
		t.Fatalf("second history page = %#v", secondPage)
	}

	otherUserPage, err := database.ListAggregationEventHistory(
		ctx,
		"integration-other-user",
		0,
		50,
	)
	if err != nil {
		t.Fatalf("list other user history: %v", err)
	}
	if len(otherUserPage.Events) != 0 ||
		otherUserPage.Stats.EventCount != 0 {
		t.Fatalf("other user history = %#v", otherUserPage)
	}
}

func TestPostgresTransactionHashIsGloballyUnique(t *testing.T) {
	store, _ := openIntegrationStore(t)
	token := "0x55d398326f99059ff775485246999027b3197955"
	first := createIntegrationOrder(t, store, "integration-user-1", token)
	second := createIntegrationOrder(t, store, "integration-user-2", token)
	txHash := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	if _, err := store.ClaimOrderTransaction(
		context.Background(), first.ID, first.DeBoxUserID, first.PayerAddress, txHash,
	); err != nil {
		t.Fatalf("claim first transaction: %v", err)
	}
	if _, err := store.ClaimOrderTransaction(
		context.Background(), second.ID, second.DeBoxUserID, second.PayerAddress, txHash,
	); !errors.Is(err, ErrOrderTransactionUsed) {
		t.Fatalf("claim duplicate transaction error = %v, want ErrOrderTransactionUsed", err)
	}
}

func openIntegrationStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	if os.Getenv("RUN_POSTGRES_INTEGRATION") != "1" {
		t.Skip("set RUN_POSTGRES_INTEGRATION=1 to run PostgreSQL integration tests")
	}
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("TEST_DATABASE_URL is required; production DATABASE_URL is intentionally ignored")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	adminConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	adminPool, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("open integration admin pool: %v", err)
	}
	t.Cleanup(adminPool.Close)
	if err := adminPool.Ping(ctx); err != nil {
		t.Fatalf("ping integration database: %v", err)
	}

	schema := fmt.Sprintf("go_contract_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create integration schema: %v", err)
	}

	schemaConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse schema pool config: %v", err)
	}
	if schemaConfig.ConnConfig.RuntimeParams == nil {
		schemaConfig.ConnConfig.RuntimeParams = map[string]string{}
	}
	schemaConfig.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, schemaConfig)
	if err != nil {
		t.Fatalf("open integration schema pool: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		_, _ = adminPool.Exec(dropCtx, "DROP SCHEMA "+quotedSchema+" CASCADE")
	})

	store := &Store{db: pool, pool: pool}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate integration schema: %v", err)
	}
	return store, pool
}

func createIntegrationOrder(t *testing.T, store *Store, userID, token string) Order {
	t.Helper()
	order, err := store.CreateOrder(context.Background(), CreateOrderParams{
		DeBoxUserID:      userID,
		PayerAddress:     "0x2222222222222222222222222222222222222222",
		PlanCode:         "standard",
		BillingCycle:     "monthly",
		SubscriptionDays: 30,
		ChainKey:         "bsc",
		ChainID:          56,
		TokenAddress:     &token,
		TokenSymbol:      "USDT",
		TokenDecimals:    18,
		TotalAmount:      "10",
		RecipientAddress: "0x3333333333333333333333333333333333333333",
	})
	if err != nil {
		t.Fatalf("create order for %s: %v", userID, err)
	}
	return order
}

func pointerOf[T any](value T) *T {
	return &value
}
