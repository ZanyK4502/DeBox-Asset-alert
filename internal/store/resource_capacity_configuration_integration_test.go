package store

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/jackc/pgx/v5/pgxpool"
)

func resourceConfigurationQuota() QuotaPolicy {
	p := interactionQuotaPolicy()
	p.MarketProjectLimit = 1
	p.MultiPoolMonitoring = true
	return p
}

func resourceConfigurationMarketSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var snapshot json.RawMessage
	err := pool.QueryRow(ctx, `SELECT jsonb_build_object(
 'assets',COALESCE((SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM market_assets a),'[]'::jsonb),
 'deployments',COALESCE((SELECT jsonb_agg(to_jsonb(d) ORDER BY d.id) FROM market_asset_deployments d),'[]'::jsonb),
 'evidence',COALESCE((SELECT jsonb_agg(to_jsonb(e) ORDER BY e.id) FROM market_asset_identity_evidence e),'[]'::jsonb),
 'projects',COALESCE((SELECT jsonb_agg(to_jsonb(p) ORDER BY p.id) FROM market_projects p),'[]'::jsonb),
 'project_deployments',COALESCE((SELECT jsonb_agg(to_jsonb(d) ORDER BY d.id) FROM market_project_deployments d),'[]'::jsonb),
 'links',COALESCE((SELECT jsonb_agg(to_jsonb(l) ORDER BY l.id) FROM market_project_pools l),'[]'::jsonb),
 'scopes',COALESCE((SELECT jsonb_agg(to_jsonb(s) ORDER BY s.project_id) FROM market_project_trade_scope_changes s),'[]'::jsonb))`).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return string(snapshot)
}

func TestPostgresResourceConfigurationMultiChainBudgetRollsBackWholeCandidate(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	const owner = "capacity-multichain-owner"
	resourceCapacitySubscriptionFixture(t, pool, owner, "professional", 1)
	bscToken := "0x1111111111111111111111111111111111111111"
	baseToken := "0x2222222222222222222222222222222222222222"
	bscPool := createWizardIntegrationPool(t, db, "bsc", 56, bscToken,
		"0x3333333333333333333333333333333333333333", "0x4444444444444444444444444444444444444444")
	basePool := createWizardIntegrationPool(t, db, "base", 8453, baseToken,
		"0x5555555555555555555555555555555555555555", "0x6666666666666666666666666666666666666666")
	p := resourceCapacityTestPolicy()
	p.AccountMonthlyCU = 500 // Fixed reserves 400 leave exactly one chain/token unit.
	db.resourceCapacity = &p
	verifiedAt := time.Now().UTC()
	params := CreateMultiChainMarketProjectParams{
		DeBoxUserID: owner, CanonicalName: "Capacity Wizard", Symbol: "CAP",
		IdentitySource: "coingecko", CanonicalAssetID: "capacity-wizard", VerificationStatus: "verified",
		Deployments: []CreateMultiChainMarketDeploymentParams{
			wizardIntegrationDeployment("bsc", 56, bscToken, bscPool.ID, verifiedAt),
			wizardIntegrationDeployment("base", 8453, baseToken, basePool.ID, verifiedAt),
		},
	}
	before := resourceConfigurationMarketSnapshot(t, ctx, pool)
	if _, err := db.CreateMultiChainMarketProjectWithinQuota(ctx, params, resourceConfigurationQuota()); !errors.Is(err, ErrResourceCapacityReached) {
		t.Fatalf("two-unit project error=%v, want capacity rejection", err)
	}
	if after := resourceConfigurationMarketSnapshot(t, ctx, pool); after != before {
		t.Fatal("rejected multi-chain project left asset/deployment/evidence/link state")
	}
	if status, err := db.ReadResourceCapacityStatus(ctx); err != nil || status.ReservedMonthlyCU != 0 {
		t.Fatalf("rejected candidate reserved capacity: %+v %v", status, err)
	}
	params.Deployments = params.Deployments[:1]
	project, err := db.CreateMultiChainMarketProjectWithinQuota(ctx, params, resourceConfigurationQuota())
	if err != nil {
		t.Fatalf("one-unit candidate did not fit: %v", err)
	}
	if status, err := db.ReadResourceCapacityStatus(ctx); err != nil || status.ReservedMonthlyCU != 100 {
		t.Fatalf("one-unit accounting=%+v %v", status, err)
	}
	// The raw status API must not bypass admission for a paused project.
	if _, err = db.SetMarketProjectStatus(ctx, project.ID, owner, "paused", "local fixture"); err != nil {
		t.Fatal(err)
	}
	p.Verified = false
	before = resourceConfigurationMarketSnapshot(t, ctx, pool)
	if _, err = db.SetMarketProjectStatus(ctx, project.ID, owner, "active", ""); !errors.Is(err, ErrResourceCapacityUnavailable) {
		t.Fatalf("raw project activation bypassed admission: %v", err)
	}
	if after := resourceConfigurationMarketSnapshot(t, ctx, pool); after != before {
		t.Fatal("rejected raw activation changed paused project or deployment state")
	}
}

func TestPostgresResourceConfigurationChargesActiveDeploymentsAndSelectedPools(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	const owner = "chain-pool-pricing"
	resourceCapacitySubscriptionFixture(t, pool, owner, plans.Professional, 1)
	bscToken := "0x1111111111111111111111111111111111111111"
	baseToken := "0x2222222222222222222222222222222222222222"
	bsc := marketScopePoolsFixture(t, db, MarketProject{ChainKey: "bsc", ChainID: 56, TokenAddress: bscToken}, 800, 2)
	base := marketScopePoolsFixture(t, db, MarketProject{ChainKey: "base", ChainID: 8453, TokenAddress: baseToken}, 810, 2)
	p := resourceCapacityTestPolicy()
	p.AccountMonthlyCU = 560 // 160 CU after fixed reserves.
	p.MarketChainMonthlyCU = map[string]plans.MarketChainCUCost{
		"bsc": {Deployment: 50, Pool: 10}, "base": {Deployment: 30, Pool: 35},
	}
	db.resourceCapacity = &p
	now := time.Now().UTC()
	project, err := db.CreateMultiChainMarketProjectWithinQuota(ctx, CreateMultiChainMarketProjectParams{
		DeBoxUserID: owner, CanonicalName: "Capacity Wizard", Symbol: "CAP", IdentitySource: "coingecko",
		CanonicalAssetID: "chain-pool-pricing", VerificationStatus: "verified",
		Deployments: []CreateMultiChainMarketDeploymentParams{
			wizardIntegrationDeployment("bsc", 56, bscToken, bsc[0].ID, now),
			wizardIntegrationDeployment("base", 8453, baseToken, base[0].ID, now),
		},
	}, resourceConfigurationQuota())
	if err != nil {
		t.Fatal(err)
	}
	assertReserve := func(want int64, unknown bool) {
		t.Helper()
		status, err := db.ReadResourceCapacityStatus(ctx)
		if err != nil || status.ReservedMonthlyCU != want || status.UnknownPlan != unknown {
			t.Fatalf("reserve=%+v, %v; want %d, unknown=%v", status, err, want, unknown)
		}
	}
	selectPool := func(id int64, selected bool) error {
		_, err := db.LinkMarketProjectPool(ctx, LinkMarketProjectPoolParams{DeBoxUserID: owner, MarketProjectID: project.ID, MarketPoolID: id, Selected: selected})
		return err
	}
	assertReserve(125, false)
	if err := selectPool(bsc[1].ID, true); err != nil {
		t.Fatal(err)
	}
	assertReserve(135, false)
	if err := selectPool(base[1].ID, true); !errors.Is(err, ErrResourceCapacityReached) {
		t.Fatalf("additional Base pool should exceed reserve: %v", err)
	}
	assertReserve(135, false)
	if err := selectPool(base[0].ID, false); err != nil {
		t.Fatal(err)
	}
	assertReserve(100, false) // An active Base deployment still has 30 CU with no pool.
	if _, err := pool.Exec(ctx, `UPDATE market_project_pools SET market_project_deployment_id=NULL WHERE market_project_id=$1 AND market_pool_id=$2`, project.ID, bsc[1].ID); err != nil {
		t.Fatal(err)
	}
	assertReserve(100, false) // The legacy selected link still counts on BSC only.
	if _, err := pool.Exec(ctx, `INSERT INTO market_projects(debox_user_id,chain_id,chain_key,token_address) VALUES('legacy-unpriced',137,'polygon',$1)`, bscToken); err != nil {
		t.Fatal(err)
	}
	assertReserve(660, true) // Unknown legacy scope cannot free resources or block a read.
	const legacyBuyer = "old-buyer-with-unpriced-scope"
	orderID := resourceCapacityOrderFixture(t, pool, legacyBuyer, plans.Standard, "pending", false)
	const hash = "0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	if _, err := db.ClaimOrderTransaction(ctx, orderID, legacyBuyer, "0x1111111111111111111111111111111111111111", hash); err != nil {
		t.Fatalf("claim old order with unpriced market scope: %v", err)
	}
	if paid, err := db.FinalizePaidOrder(ctx, orderID, hash, 100, 3); err != nil || paid.Subscription == nil {
		t.Fatalf("finalize old order with unpriced market scope: %+v, %v", paid, err)
	}
}

func TestPostgresResourceConfigurationRawCreatesSerializeGlobalCapacity(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	p := resourceCapacityTestPolicy()
	p.AccountMonthlyCU = 410 // Exactly one ordinary rule across both owners.
	db.resourceCapacity = &p
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for i, owner := range []string{"raw-capacity-a", "raw-capacity-b"} {
		workers.Add(1)
		go func(owner string, index int) {
			defer workers.Done()
			<-start
			_, err := db.CreateWatchRule(ctx, interactionQuotaRule(owner, 1600+index, "balance_change"))
			results <- err
		}(owner, i)
	}
	close(start)
	workers.Wait()
	close(results)
	accepted, rejected := 0, 0
	for err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, ErrResourceCapacityReached):
			rejected++
		default:
			t.Fatalf("unexpected concurrent create error: %v", err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("accepted=%d rejected=%d, want exactly one of each", accepted, rejected)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM watch_rules`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("raw race left %d rows: %v", count, err)
	}
	if status, err := db.ReadResourceCapacityStatus(ctx); err != nil || status.ReservedMonthlyCU != 10 || status.Users != 1 {
		t.Fatalf("global admission oversold: %+v %v", status, err)
	}
}

func TestPostgresResourceConfigurationRawRestorePreservesRejectedState(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	const owner = "raw-restore-capacity"
	rule, err := db.CreateWatchRule(ctx, interactionQuotaRule(owner, 1700, "balance_change"))
	if err != nil {
		t.Fatal(err)
	}
	interactionQuotaPauseRule(t, ctx, pool, rule)
	p := resourceCapacityTestPolicy()
	p.Verified = false
	db.resourceCapacity = &p
	before := interactionQuotaSnapshot(t, ctx, pool, owner, nil, nil)
	if _, err = db.RestoreWatchRule(ctx, rule.ID, owner); !errors.Is(err, ErrResourceCapacityUnavailable) {
		t.Fatalf("raw restore bypassed admission: %v", err)
	}
	if after := interactionQuotaSnapshot(t, ctx, pool, owner, nil, nil); after != before {
		t.Fatal("rejected raw restore altered baseline/time/anchor/window/generation")
	}
}

func TestPostgresResourceConfigurationPoolLinkKeepsExistingScopeSemantics(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	const owner = "pool-link-capacity"
	resourceCapacitySubscriptionFixture(t, pool, owner, "professional", 1)
	project, err := db.CreateMarketProjectWithinQuota(ctx, resourceActivationProject(owner), resourceConfigurationQuota())
	if err != nil {
		t.Fatal(err)
	}
	localPool := createWizardIntegrationPool(t, db, "bsc", 56, project.TokenAddress,
		"0x3333333333333333333333333333333333333333", "0x7777777777777777777777777777777777777777")
	otherPool := createWizardIntegrationPool(t, db, "base", 8453,
		"0x2222222222222222222222222222222222222222", "0x3333333333333333333333333333333333333333",
		"0x8888888888888888888888888888888888888888")
	p := resourceCapacityTestPolicy()
	p.Verified = false
	db.resourceCapacity = &p
	params := LinkMarketProjectPoolParams{DeBoxUserID: owner, MarketProjectID: project.ID,
		MarketPoolID: localPool.ID, Selected: true, IsPrimary: true, DiscoverySource: "local fixture"}
	// A pool link adds no chain/token unit to the flat project resource model.
	// Unverified budget must therefore preserve already-accounted operations.
	if _, err = db.LinkMarketProjectPool(ctx, params); err != nil {
		t.Fatalf("neutral existing-chain link was rejected: %v", err)
	}
	if _, err = db.LinkMarketProjectPoolWithinQuota(ctx, params, resourceConfigurationQuota()); err != nil {
		t.Fatalf("quota link changed neutral operation semantics: %v", err)
	}
	if status, err := db.ReadResourceCapacityStatus(ctx); err != nil || status.ReservedMonthlyCU != 100 {
		t.Fatalf("link created another project resource unit: %+v %v", status, err)
	}
	before := resourceConfigurationMarketSnapshot(t, ctx, pool)
	params.MarketPoolID = otherPool.ID
	for _, withinQuota := range []bool{false, true} {
		if withinQuota {
			_, err = db.LinkMarketProjectPoolWithinQuota(ctx, params, resourceConfigurationQuota())
		} else {
			_, err = db.LinkMarketProjectPool(ctx, params)
		}
		if !errors.Is(err, ErrMarketPoolMismatch) {
			t.Fatalf("link invented unmatched deployment (quota=%t): %v", withinQuota, err)
		}
		if after := resourceConfigurationMarketSnapshot(t, ctx, pool); after != before {
			t.Fatal("invalid cross-chain link left scope/deployment/pool state")
		}
	}
}
