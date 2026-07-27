package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestPostgresCreatesOneLogicalMultiChainProjectAtomically(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	const userID = "integration-multichain-project-wizard"
	if _, err := pool.Exec(ctx, `
		INSERT INTO subscriptions (
			debox_user_id, plan_code, status, starts_at, expires_at
		)
		VALUES ($1, 'standard', 'active', NOW() - INTERVAL '1 hour',
			NOW() + INTERVAL '1 day')
	`, userID); err != nil {
		t.Fatalf("create standard subscription: %v", err)
	}

	bscToken := "0x1111111111111111111111111111111111111111"
	baseToken := "0x2222222222222222222222222222222222222222"
	bscPool := createWizardIntegrationPool(
		t, database, "bsc", 56, bscToken,
		"0x3333333333333333333333333333333333333333",
		"0x4444444444444444444444444444444444444444",
	)
	basePool := createWizardIntegrationPool(
		t, database, "base", 8453, baseToken,
		"0x5555555555555555555555555555555555555555",
		"0x6666666666666666666666666666666666666666",
	)
	var legacyAssetID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO market_assets (
			canonical_name, symbol, identity_source, canonical_asset_id,
			verification_status
		)
		VALUES (
			'Wizard Token', 'WIZ', 'legacy',
			'eip155:56/erc20:' || $1, 'single_chain'
		)
		RETURNING id
	`, bscToken).Scan(&legacyAssetID); err != nil {
		t.Fatalf("create legacy BSC asset: %v", err)
	}
	var legacyDeploymentID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO market_asset_deployments (
			market_asset_id, chain_key, chain_id, token_address,
			token_name, token_symbol, token_decimals,
			verification_status, verification_source
		)
		VALUES ($1, 'bsc', 56, $2, 'Wizard Token', 'WIZ', 18,
			'single_chain', 'legacy')
		RETURNING id
	`, legacyAssetID, bscToken).Scan(&legacyDeploymentID); err != nil {
		t.Fatalf("create legacy BSC deployment: %v", err)
	}
	var legacyProjectID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO market_projects (
			debox_user_id, market_asset_id, chain_key, chain_id, token_address,
			token_name, token_symbol, token_decimals
		)
		VALUES (
			'integration-existing-legacy-owner', $1, 'bsc', 56, $2,
			'Wizard Token', 'WIZ', 18
		)
		RETURNING id
	`, legacyAssetID, bscToken).Scan(&legacyProjectID); err != nil {
		t.Fatalf("create legacy project: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO market_project_deployments (
			market_project_id, market_asset_id, market_asset_deployment_id
		)
		VALUES ($1, $2, $3)
	`, legacyProjectID, legacyAssetID, legacyDeploymentID); err != nil {
		t.Fatalf("create legacy project deployment: %v", err)
	}
	var canonicalAssetID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO market_assets (
			canonical_name, symbol, identity_source, canonical_asset_id,
			verification_status
		)
		VALUES ('Wizard Token', 'WIZ', 'coingecko', 'wizard-token', 'verified')
		RETURNING id
	`).Scan(&canonicalAssetID); err != nil {
		t.Fatalf("create canonical Base asset: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO market_asset_deployments (
			market_asset_id, chain_key, chain_id, token_address,
			token_name, token_symbol, token_decimals,
			verification_status, verification_source
		)
		VALUES ($1, 'base', 8453, $2, 'Wizard Token', 'WIZ', 18,
			'verified', 'coingecko')
	`, canonicalAssetID, baseToken); err != nil {
		t.Fatalf("create canonical Base deployment: %v", err)
	}
	verifiedAt := time.Now().UTC()
	project, err := database.CreateMultiChainMarketProjectWithinQuota(
		ctx,
		CreateMultiChainMarketProjectParams{
			DeBoxUserID:        userID,
			CanonicalName:      "Wizard Token",
			Symbol:             "WIZ",
			IdentitySource:     "coingecko",
			CanonicalAssetID:   "wizard-token",
			VerificationStatus: "verified",
			Deployments: []CreateMultiChainMarketDeploymentParams{
				wizardIntegrationDeployment(
					"bsc", 56, bscToken, bscPool.ID, verifiedAt,
				),
				wizardIntegrationDeployment(
					"base", 8453, baseToken, basePool.ID, verifiedAt,
				),
			},
		},
		QuotaPolicy{
			PlanCode:            "standard",
			MarketProjectLimit:  5,
			MultiPoolMonitoring: false,
		},
	)
	if err != nil {
		t.Fatalf("create multi-chain project: %v", err)
	}
	if project.MarketAssetID == nil || *project.MarketAssetID != canonicalAssetID ||
		project.ChainID != 56 ||
		project.MainPoolID == nil || *project.MainPoolID != bscPool.ID {
		t.Fatalf("project compatibility mirrors = %#v", project)
	}
	var mergedProjectAssetID, mergedDeploymentAssetID int64
	if err := pool.QueryRow(ctx, `
		SELECT mp.market_asset_id, mpd.market_asset_id
		FROM market_projects mp
		JOIN market_project_deployments mpd
		  ON mpd.market_project_id = mp.id
		WHERE mp.id = $1
	`, legacyProjectID).Scan(
		&mergedProjectAssetID,
		&mergedDeploymentAssetID,
	); err != nil ||
		mergedProjectAssetID != canonicalAssetID ||
		mergedDeploymentAssetID != canonicalAssetID {
		t.Fatalf(
			"legacy project hierarchy was not merged: project=%d deployment=%d error=%v",
			mergedProjectAssetID,
			mergedDeploymentAssetID,
			err,
		)
	}
	deployments, err := database.ListMarketProjectDeployments(
		ctx, project.ID, userID,
	)
	if err != nil || len(deployments) != 2 {
		t.Fatalf("project deployments = %#v, error = %v", deployments, err)
	}
	pools, err := database.ListMarketProjectPoolViews(ctx, project.ID, userID)
	if err != nil || len(pools) != 2 {
		t.Fatalf("project pools = %#v, error = %v", pools, err)
	}
	for _, marketPool := range pools {
		if marketPool.Selected != 1 || marketPool.IsPrimary != 1 {
			t.Fatalf("pool is not selected primary: %#v", marketPool)
		}
	}
	var legacyAssetRemaining bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM market_assets WHERE id = $1)
	`, legacyAssetID).Scan(&legacyAssetRemaining); err != nil || legacyAssetRemaining {
		t.Fatalf(
			"legacy asset remained after canonical merge: exists=%v error=%v",
			legacyAssetRemaining,
			err,
		)
	}
	var evidenceCount int64
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM market_asset_identity_evidence
		WHERE market_asset_id = $1
	`, *project.MarketAssetID).Scan(&evidenceCount); err != nil || evidenceCount != 2 {
		t.Fatalf("identity evidence count = %d, error = %v", evidenceCount, err)
	}
}

func TestPostgresRejectsStandardMultiPoolProjectWithoutPartialRows(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	const userID = "integration-standard-multipool-rejected"
	if _, err := pool.Exec(ctx, `
		INSERT INTO subscriptions (
			debox_user_id, plan_code, status, starts_at, expires_at
		)
		VALUES ($1, 'standard', 'active', NOW() - INTERVAL '1 hour',
			NOW() + INTERVAL '1 day')
	`, userID); err != nil {
		t.Fatalf("create standard subscription: %v", err)
	}
	token := "0x7777777777777777777777777777777777777777"
	first := createWizardIntegrationPool(
		t, database, "bsc", 56, token,
		"0x8888888888888888888888888888888888888888",
		"0x9999999999999999999999999999999999999999",
	)
	second := createWizardIntegrationPool(
		t, database, "bsc", 56, token,
		"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	)
	deployment := wizardIntegrationDeployment(
		"bsc", 56, token, first.ID, time.Now().UTC(),
	)
	deployment.Pools = append(deployment.Pools, CreateMultiChainMarketPoolParams{
		MarketPoolID: second.ID,
		Selected:     true,
	})
	_, err := database.CreateMultiChainMarketProjectWithinQuota(
		ctx,
		CreateMultiChainMarketProjectParams{
			DeBoxUserID:        userID,
			CanonicalName:      "Standard",
			Symbol:             "STD",
			IdentitySource:     "onchain",
			CanonicalAssetID:   "standard-multipool",
			VerificationStatus: "single_chain",
			Deployments:        []CreateMultiChainMarketDeploymentParams{deployment},
		},
		QuotaPolicy{
			PlanCode:            "standard",
			MarketProjectLimit:  5,
			MultiPoolMonitoring: false,
		},
	)
	if !errors.Is(err, ErrMarketPoolMismatch) {
		t.Fatalf("create error = %v, want ErrMarketPoolMismatch", err)
	}
	var projectCount, assetCount int64
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM market_projects WHERE debox_user_id = $1),
			(SELECT COUNT(*) FROM market_assets
			 WHERE identity_source = 'onchain'
			   AND canonical_asset_id = 'standard-multipool')
	`, userID).Scan(&projectCount, &assetCount); err != nil {
		t.Fatalf("count rolled-back rows: %v", err)
	}
	if projectCount != 0 || assetCount != 0 {
		t.Fatalf(
			"partial rows remained after rollback: projects=%d assets=%d",
			projectCount,
			assetCount,
		)
	}
}

func createWizardIntegrationPool(
	t *testing.T,
	database *Store,
	chainKey string,
	chainID int64,
	token string,
	quote string,
	poolAddress string,
) MarketPool {
	t.Helper()
	value, err := database.UpsertMarketPool(
		context.Background(),
		UpsertMarketPoolParams{
			ChainKey: chainKey, ChainID: chainID,
			Protocol: "integration", ProtocolVersion: "v2",
			PoolKey: poolAddress, PoolAddress: &poolAddress,
			Token0Address: token, Token0Symbol: "WIZ", Token0Decimals: 18,
			Token1Address: quote, Token1Symbol: "USD", Token1Decimals: 18,
			LiquidityUSD: "100000", SupportsEventParsing: true,
			ParserAdapter: "uniswap_v2", VerificationStatus: "verified",
		},
	)
	if err != nil {
		t.Fatalf("create %s pool: %v", chainKey, err)
	}
	return value
}

func wizardIntegrationDeployment(
	chainKey string,
	chainID int64,
	token string,
	poolID int64,
	verifiedAt time.Time,
) CreateMultiChainMarketDeploymentParams {
	return CreateMultiChainMarketDeploymentParams{
		ChainKey: chainKey, ChainID: chainID, TokenAddress: token,
		TokenName: "Wizard Token", TokenSymbol: "WIZ", TokenDecimals: 18,
		VerificationStatus: "verified", VerificationSource: "coingecko",
		VerifiedAt: &verifiedAt,
		Evidence: &CreateMarketAssetEvidenceParams{
			EvidenceKey:     chainKey + ":" + token,
			Source:          "coingecko",
			EvidenceType:    "canonical_asset_id",
			ExternalAssetID: "wizard-token",
			Verdict:         "supports",
			Confidence:      "1",
			Payload:         json.RawMessage(`{"verified":true}`),
			ObservedAt:      verifiedAt,
		},
		Pools: []CreateMultiChainMarketPoolParams{{
			MarketPoolID: poolID,
			Selected:     true,
			IsPrimary:    true,
		}},
	}
}
