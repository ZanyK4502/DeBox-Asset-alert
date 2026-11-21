package store

import (
	"context"
	"testing"
	"time"
)

func TestPostgresMarketScopeHolderSchedulerDoesNotLeaseExcludedHistoryChain(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	holderJobProject(t, db, 1, "ethereum", tradeFixtureToken, "legacy-eth-owner")
	holderJobProject(t, db, 56, "bsc", tradeFixtureToken, "bsc-owner")
	var before string
	if err := pool.QueryRow(ctx, `SELECT row_to_json(p)::text FROM market_projects p WHERE chain_id=1`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	db.marketScopePolicies = map[string]QuotaPolicy{
		"free":         {PlanCode: "free", AllowedMarketChainKeys: []string{}},
		"standard":     {PlanCode: "standard", MarketProjectLimit: 1, MarketPoolLimit: 1, AllowedMarketChainKeys: []string{"bsc"}},
		"professional": {PlanCode: "professional", MarketProjectLimit: 1, MarketPoolLimit: 2, AllowedMarketChainKeys: []string{"bsc"}},
	}
	job, err := db.ClaimMarketHolderRefreshJob(ctx, 15*time.Minute)
	if err != nil || job == nil || job.Project.ChainID != 56 {
		t.Fatalf("leased wrong chain %+v %v", job, err)
	}
	if err = db.FailMarketHolderRefreshJob(ctx, *job, "source_capacity", time.Now().Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if next, err := db.ClaimMarketHolderRefreshJob(ctx, 15*time.Minute); err != nil || next != nil {
		t.Fatalf("excluded chain leaked into next claim %+v %v", next, err)
	}
	var states int
	var after string
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_holder_refresh_states WHERE chain_id=1`).Scan(&states); err != nil || states != 0 {
		t.Fatalf("excluded holder baseline was touched %d %v", states, err)
	}
	if err = pool.QueryRow(ctx, `SELECT row_to_json(p)::text FROM market_projects p WHERE chain_id=1`).Scan(&after); err != nil || after != before {
		t.Fatal("historical project was changed", err)
	}
	// Raising the configured monitoring scope later does not need scheduler code changes.
	p := db.marketScopePolicies["professional"]
	p.AllowedMarketChainKeys = []string{"bsc", "ethereum"}
	db.marketScopePolicies["professional"] = p
	job, err = db.ClaimMarketHolderRefreshJob(ctx, 15*time.Minute)
	if err != nil || job == nil || job.Project.ChainID != 1 {
		t.Fatalf("future scope expansion failed %+v %v", job, err)
	}
}
