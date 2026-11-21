package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
)

func TestPostgresProjectPoolViewUsesLatestQuoteAvailabilityObservation(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	proof := marketquality.Quote{Status: "live", FetchedAt: &at}
	metadata, _ := json.Marshal(map[string]any{"quote_quality": proof, "quote_fields": map[string]bool{"liquidity_usd": false}})
	if _, err := pool.Exec(ctx, `UPDATE market_pools SET liquidity_usd=999,metadata=$2 WHERE id=$1`, pools[0], metadata); err != nil {
		t.Fatal(err)
	}
	var projectID int64
	if err := pool.QueryRow(ctx, `INSERT INTO market_projects(debox_user_id,chain_key,chain_id,token_address,token_name,token_symbol,token_decimals) VALUES('pool-view-quality','bsc',56,$1,'View','VIEW',24) RETURNING id`, tradeFixtureToken).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO market_project_pools(market_project_id,market_pool_id,selected,is_primary) VALUES($1,$2,1,1)`, projectID, pools[0]); err != nil {
		t.Fatal(err)
	}
	load := func() MarketPoolView {
		t.Helper()
		values, err := database.ListMarketProjectPoolViews(ctx, projectID, "pool-view-quality")
		if err != nil || len(values) != 1 {
			t.Fatalf("pool view=%#v,%v", values, err)
		}
		return values[0]
	}
	preview := load()
	if preview.QuoteSnapshotID != nil || preview.LiquidityUSD != "999" || preview.QuoteQuality.Status != "live" {
		t.Fatalf("first preview changed: %#v", preview)
	}
	original, err := database.CreateMarketSnapshot(ctx, CreateMarketSnapshotParams{ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, MarketPoolID: pools[0], Source: "dexscreener", CapturedAt: at, QuoteQuality: proof, LiquidityUSD: pointerOf("100")})
	if err != nil {
		t.Fatal(err)
	}
	if count, err := database.RecordMarketQuoteFailure(ctx, 56, []MarketCollectionTarget{quoteFailureTarget(pools[0])}, at.Add(10*time.Second)); err != nil || count != 1 {
		t.Fatalf("record marker=%d,%v", count, err)
	}
	failed := load()
	if failed.QuoteSnapshotID == nil || *failed.QuoteSnapshotID <= original.ID || failed.QuoteQuality.Status != "stale_fallback" || failed.LiquidityUSD != "100" {
		t.Fatalf("pool card kept current pool metadata after failure: %#v", failed)
	}
	missingAt := at.Add(20 * time.Second)
	missing, err := database.CreateMarketSnapshot(ctx, CreateMarketSnapshotParams{ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, MarketPoolID: pools[0], Source: "dexscreener", CapturedAt: missingAt, QuoteQuality: marketquality.Quote{Status: "live", FetchedAt: &missingAt}})
	if err != nil {
		t.Fatal(err)
	}
	otherAt := at.Add(30 * time.Second)
	if _, err := database.CreateMarketSnapshot(ctx, CreateMarketSnapshotParams{ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureQuote, MarketPoolID: pools[0], Source: "other-token", CapturedAt: otherAt, QuoteQuality: marketquality.Quote{Status: "live", FetchedAt: &otherAt}, LiquidityUSD: pointerOf("2222")}); err != nil {
		t.Fatal(err)
	}
	futureAt := at.Add(2 * time.Hour)
	if _, err := database.CreateMarketSnapshot(ctx, CreateMarketSnapshotParams{ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, MarketPoolID: pools[0], Source: "future-quote", CapturedAt: futureAt, QuoteQuality: marketquality.Quote{Status: "live", FetchedAt: &futureAt}, LiquidityUSD: pointerOf("7777")}); err != nil {
		t.Fatal(err)
	}
	view := load()
	if view.QuoteSnapshotID == nil || *view.QuoteSnapshotID != missing.ID || view.LiquidityUSD != "" || view.QuoteQuality.Status != "live" {
		t.Fatalf("missing liquidity used current pool/other token/future snapshot: %#v", view)
	}
}

func TestPostgresProjectPoolViewUsesExactDeploymentTokenAndFrozenCutoff(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	var assetID, deploymentID, projectID, projectDeploymentID int64
	if err := pool.QueryRow(ctx, `INSERT INTO market_assets(identity_source,canonical_asset_id) VALUES('quote-view','deployment-token') RETURNING id`).Scan(&assetID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO market_asset_deployments(market_asset_id,chain_key,chain_id,token_address) VALUES($1,'bsc',56,$2) RETURNING id`, assetID, tradeFixtureQuote).Scan(&deploymentID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO market_projects(debox_user_id,market_asset_id,chain_key,chain_id,token_address,token_name,token_symbol,token_decimals) VALUES('deployment-pool-view',$1,'bsc',56,$2,'View','VIEW',24) RETURNING id`, assetID, tradeFixtureToken).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO market_project_deployments(market_project_id,market_asset_id,market_asset_deployment_id) VALUES($1,$2,$3) RETURNING id`, projectID, assetID, deploymentID).Scan(&projectDeploymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO market_project_pools(market_project_id,market_project_deployment_id,market_pool_id,selected,is_primary,created_at) VALUES($1,$2,$3,1,1,$4)`, projectID, projectDeploymentID, pools[0], at.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	makeSnapshot := func(chainID int64, token, source string, captured time.Time, usd string) MarketSnapshot {
		t.Helper()
		value, err := database.CreateMarketSnapshot(ctx, CreateMarketSnapshotParams{ChainKey: "bsc", ChainID: chainID, TokenAddress: token, MarketPoolID: pools[0], Source: source, CapturedAt: captured, QuoteQuality: marketquality.Quote{Status: "live", FetchedAt: &captured}, LiquidityUSD: &usd})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	makeSnapshot(56, tradeFixtureToken, "primary-token", at.Add(time.Second), "1111")
	first := makeSnapshot(56, tradeFixtureQuote, "deployment-token", at.Add(10*time.Second), "10")
	target := quoteFailureTarget(pools[0])
	target.TokenAddress = tradeFixtureQuote
	if count, err := database.RecordMarketQuoteFailure(ctx, 56, []MarketCollectionTarget{target}, at.Add(20*time.Second)); err != nil || count != 1 {
		t.Fatalf("deployment marker=%d,%v", count, err)
	}
	newer := makeSnapshot(56, tradeFixtureQuote, "deployment-token", at.Add(40*time.Second), "40")
	makeSnapshot(56, tradeFixtureToken, "primary-token", at.Add(50*time.Second), "5555")
	makeSnapshot(8453, tradeFixtureQuote, "wrong-chain", at.Add(55*time.Second), "8888")
	load := func() MarketPoolView {
		t.Helper()
		values, err := database.ListMarketProjectPoolViews(ctx, projectID, "deployment-pool-view")
		if err != nil || len(values) != 1 {
			t.Fatalf("deployment view=%#v,%v", values, err)
		}
		return values[0]
	}
	active := load()
	if active.QuoteSnapshotID == nil || *active.QuoteSnapshotID != newer.ID || active.LiquidityUSD != "40" {
		t.Fatalf("view used primary/other chain snapshot: %#v", active)
	}
	if _, err := pool.Exec(ctx, `UPDATE market_projects SET frozen_at=$2 WHERE id=$1`, projectID, at.Add(25*time.Second)); err != nil {
		t.Fatal(err)
	}
	frozen := load()
	if frozen.QuoteSnapshotID == nil || *frozen.QuoteSnapshotID <= first.ID || *frozen.QuoteSnapshotID >= newer.ID || frozen.QuoteQuality.Status != "stale_fallback" || frozen.LiquidityUSD != "10" {
		t.Fatalf("frozen card crossed time/token boundary or skipped fallback: %#v", frozen)
	}
	if _, err := pool.Exec(ctx, `UPDATE market_projects SET frozen_at=$2 WHERE id=$1`, projectID, at.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	unknown := load()
	if unknown.QuoteSnapshotID != nil || unknown.LiquidityUSD != "" || unknown.QuoteQuality.Status != "" {
		t.Fatalf("frozen card without exact-token snapshot invented data: %#v", unknown)
	}
}
