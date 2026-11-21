package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func marketScopeInstallTestPolicy(db *Store) {
	settings := openSettings{}
	WithMarketScopePolicies(marketScopeTestPolicies())(&settings)
	db.marketScopePolicies = settings.marketScopePolicies
}

func marketScopeLegacyQuota(code string) QuotaPolicy {
	p := resourceConfigurationQuota()
	p.PlanCode = code
	p.AllowedMarketRuleTypes = []string{"market_price_above", "market_large_buy", "market_large_sell"}
	p.MultiPoolMonitoring = code == "professional"
	return p
}

func marketScopeProjectFixture(t *testing.T, db *Store, owner, key string, chainID int64, index int) MarketProject {
	t.Helper()
	project, err := db.CreateMarketProjectWithinQuota(context.Background(), CreateMarketProjectParams{DeBoxUserID: owner, ChainKey: key, ChainID: chainID,
		TokenAddress: fmt.Sprintf("0x%040x", index), TokenName: "Scope fixture", TokenSymbol: "SCOPE", TokenDecimals: 18}, marketScopeLegacyQuota("professional"))
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func marketScopePoolsFixture(t *testing.T, db *Store, p MarketProject, start, count int) []MarketPool {
	t.Helper()
	var pools []MarketPool
	for i := 0; i < count; i++ {
		pools = append(pools, createWizardIntegrationPool(t, db, p.ChainKey, p.ChainID, p.TokenAddress,
			"0x3333333333333333333333333333333333333333", fmt.Sprintf("0x%040x", start+i)))
	}
	return pools
}

func marketScopeLinkFixture(t *testing.T, db *Store, p MarketProject, pools []MarketPool) {
	t.Helper()
	for i, pool := range pools {
		if _, err := db.LinkMarketProjectPool(context.Background(), LinkMarketProjectPoolParams{DeBoxUserID: p.DeBoxUserID,
			MarketProjectID: p.ID, MarketPoolID: pool.ID, Selected: true, IsPrimary: i == 0}); err != nil {
			t.Fatal(err)
		}
	}
}

func marketScopeRuleFixture(t *testing.T, db *Store, p MarketProject, kind string) MarketRule {
	t.Helper()
	rule, err := db.CreateMarketRuleWithinQuota(context.Background(), CreateMarketRuleParams{DeBoxUserID: p.DeBoxUserID, MarketProjectID: p.ID,
		RuleType: kind, ThresholdValue: "1", ThresholdUnit: "token", PoolScope: "all", NotificationChatID: p.DeBoxUserID}, marketScopeLegacyQuota("professional"))
	if err != nil {
		t.Fatal(err)
	}
	return rule
}

func marketScopeOwnerSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner string) string {
	t.Helper()
	base := resourceActivationSnapshot(t, ctx, pool, owner)
	var extra json.RawMessage
	if err := pool.QueryRow(ctx, `SELECT jsonb_build_object(
 'combos',COALESCE((SELECT jsonb_agg(to_jsonb(c) ORDER BY c.id) FROM market_combination_rules c WHERE c.debox_user_id=$1),'[]'::jsonb),
 'combo_windows',COALESCE((SELECT jsonb_agg(to_jsonb(w) ORDER BY w.id) FROM market_combination_windows w JOIN market_combination_rules c ON c.id=w.market_combination_rule_id WHERE c.debox_user_id=$1),'[]'::jsonb),
 'stage_windows',COALESCE((SELECT jsonb_agg(to_jsonb(w) ORDER BY w.id) FROM market_stage_windows w JOIN market_rules r ON r.id=w.market_rule_id WHERE r.debox_user_id=$1),'[]'::jsonb))`, owner).Scan(&extra); err != nil {
		t.Fatal(err)
	}
	return base + "\n" + string(extra)
}

func TestPostgresMarketScopeCreateCannotOmitOrForgeScopePolicy(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	const owner = "scope-create"
	resourceCapacitySubscriptionFixture(t, pool, owner, "professional", 1)
	marketScopeInstallTestPolicy(db)
	quota := marketScopeLegacyQuota("professional") // Deliberately omits the new fields.
	for _, params := range []CreateMarketProjectParams{
		{DeBoxUserID: owner, ChainKey: "base", ChainID: 8453, TokenAddress: fmt.Sprintf("0x%040x", 2100)},
		{DeBoxUserID: owner, ChainKey: "bsc", ChainID: 1, TokenAddress: fmt.Sprintf("0x%040x", 2101)},
	} {
		if _, err := db.CreateMarketProjectWithinQuota(ctx, params, quota); !errors.Is(err, ErrMarketChainNotAllowed) {
			t.Fatalf("create bypassed canonical scope: %v", err)
		}
	}
	bscToken := fmt.Sprintf("0x%040x", 2102)
	baseToken := fmt.Sprintf("0x%040x", 2103)
	bsc := MarketProject{ChainKey: "bsc", ChainID: 56, TokenAddress: bscToken}
	base := MarketProject{ChainKey: "base", ChainID: 8453, TokenAddress: baseToken}
	bscPools := marketScopePoolsFixture(t, db, bsc, 2200, 3)
	basePools := marketScopePoolsFixture(t, db, base, 2300, 1)
	at := time.Now().UTC()
	params := CreateMultiChainMarketProjectParams{DeBoxUserID: owner, CanonicalName: "Scope candidate", Symbol: "SCOPE", IdentitySource: "coingecko", CanonicalAssetID: "scope-candidate", VerificationStatus: "verified",
		Deployments: []CreateMultiChainMarketDeploymentParams{wizardIntegrationDeployment("bsc", 56, bscToken, bscPools[0].ID, at), wizardIntegrationDeployment("base", 8453, baseToken, basePools[0].ID, at)}}
	before := resourceConfigurationMarketSnapshot(t, ctx, pool)
	if _, err := db.CreateMultiChainMarketProjectWithinQuota(ctx, params, quota); !errors.Is(err, ErrMarketChainNotAllowed) {
		t.Fatalf("multi-chain bypass=%v", err)
	}
	if after := resourceConfigurationMarketSnapshot(t, ctx, pool); after != before {
		t.Fatal("rejected chain create left data")
	}
	params.Deployments = params.Deployments[:1]
	params.Deployments[0].Pools = append(params.Deployments[0].Pools, CreateMultiChainMarketPoolParams{MarketPoolID: bscPools[1].ID, Selected: true}, CreateMultiChainMarketPoolParams{MarketPoolID: bscPools[2].ID, Selected: true})
	if _, err := db.CreateMultiChainMarketProjectWithinQuota(ctx, params, quota); !errors.Is(err, ErrMarketPoolLimitReached) {
		t.Fatalf("three-pool create bypass=%v", err)
	}
	if after := resourceConfigurationMarketSnapshot(t, ctx, pool); after != before {
		t.Fatal("rejected pool create left deployment/evidence/link state")
	}
	params.Deployments[0].Pools = params.Deployments[0].Pools[:2]
	if _, err := db.CreateMultiChainMarketProjectWithinQuota(ctx, params, quota); err != nil {
		t.Fatalf("valid BSC/two pool candidate: %v", err)
	}
}

func TestPostgresMarketScopeConcurrentRawPoolLinksKeepProLimitAndStdMainPool(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	const owner = "scope-link-pro"
	resourceCapacitySubscriptionFixture(t, pool, owner, "professional", 1)
	project := marketScopeProjectFixture(t, db, owner, "bsc", 56, 2400)
	pools := marketScopePoolsFixture(t, db, project, 2410, 3)
	marketScopeLinkFixture(t, db, project, pools[:1])
	marketScopeInstallTestPolicy(db)
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, candidate := range pools[1:] {
		workers.Add(1)
		go func(id int64) {
			defer workers.Done()
			<-start
			_, err := db.LinkMarketProjectPool(ctx, LinkMarketProjectPoolParams{DeBoxUserID: owner, MarketProjectID: project.ID, MarketPoolID: id, Selected: true})
			results <- err
		}(candidate.ID)
	}
	close(start)
	workers.Wait()
	close(results)
	accepted, rejected := 0, 0
	for err := range results {
		if err == nil {
			accepted++
		} else if errors.Is(err, ErrMarketPoolLimitReached) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("accepted=%d rejected=%d", accepted, rejected)
	}
	var selected int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_project_pools WHERE market_project_id=$1 AND selected=1`, project.ID).Scan(&selected); err != nil || selected != 2 {
		t.Fatal(selected, err)
	}
	var remaining int64
	if err := pool.QueryRow(ctx, `SELECT p.id FROM market_pools p WHERE p.id=ANY($1::bigint[]) AND NOT EXISTS(SELECT 1 FROM market_project_pools mpp WHERE mpp.market_project_id=$2 AND mpp.market_pool_id=p.id AND mpp.selected=1)`, []int64{pools[1].ID, pools[2].ID}, project.ID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	before := resourceConfigurationMarketSnapshot(t, ctx, pool)
	forged := marketScopeLegacyQuota("professional")
	forged.MarketPoolLimit = 99
	forged.AllowedMarketChainKeys = []string{"base", "bsc"}
	if _, err := db.LinkMarketProjectPoolWithinQuota(ctx, LinkMarketProjectPoolParams{DeBoxUserID: owner, MarketProjectID: project.ID, MarketPoolID: remaining, Selected: true}, forged); !errors.Is(err, ErrMarketPoolLimitReached) {
		t.Fatal(err)
	}
	if after := resourceConfigurationMarketSnapshot(t, ctx, pool); after != before {
		t.Fatal("failed expansion changed saved pool scope")
	}
	const stdOwner = "scope-link-standard"
	resourceCapacitySubscriptionFixture(t, pool, stdOwner, "standard", 1)
	std, err := db.CreateMarketProjectWithinQuota(ctx, CreateMarketProjectParams{DeBoxUserID: stdOwner, ChainKey: "bsc", ChainID: 56, TokenAddress: fmt.Sprintf("0x%040x", 2500)}, marketScopeLegacyQuota("standard"))
	if err != nil {
		t.Fatal(err)
	}
	stdPools := marketScopePoolsFixture(t, db, std, 2510, 2)
	marketScopeLinkFixture(t, db, std, stdPools)
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_project_pools WHERE market_project_id=$1 AND selected=1 AND is_primary=1`, std.ID).Scan(&selected); err != nil || selected != 1 {
		t.Fatal("standard no longer switches its single main pool", selected, err)
	}
	if _, err := db.CreateMarketRuleWithinQuota(ctx, CreateMarketRuleParams{DeBoxUserID: stdOwner, MarketProjectID: std.ID, RuleType: "market_large_buy", ThresholdValue: "1", PoolScope: "all"}, marketScopeLegacyQuota("standard")); !errors.Is(err, ErrMarketPoolMismatch) {
		t.Fatal("standard acquired all-pool rule capability", err)
	}
}

func TestPostgresMarketScopeRejectsRestoresAndReconcilePreservesOldParameters(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		id        int64
		pools     int
		want      error
	}{{"chain", "base", 8453, 1, ErrMarketChainNotAllowed}, {"pools", "bsc", 56, 3, ErrMarketPoolLimitReached}} {
		t.Run(tc.name, func(t *testing.T) {
			db, pool := openIntegrationStore(t)
			ctx := context.Background()
			owner := "scope-restore-" + tc.name
			resourceCapacitySubscriptionFixture(t, pool, owner, "professional", 1)
			project := marketScopeProjectFixture(t, db, owner, tc.key, tc.id, 2600)
			marketScopeLinkFixture(t, db, project, marketScopePoolsFixture(t, db, project, 2610, tc.pools))
			buy := marketScopeRuleFixture(t, db, project, "market_large_buy")
			sell := marketScopeRuleFixture(t, db, project, "market_large_sell")
			combo, err := db.CreateMarketCombinationWithinQuota(ctx, CreateMarketCombinationParams{DeBoxUserID: owner, Note: "preserve historical scope", CycleType: "fixed", CycleMinutes: 60, Members: []CreateMarketCombinationMemberParams{{SourceType: "market", MarketRuleID: &buy.ID}, {SourceType: "market", MarketRuleID: &sell.ID}}}, marketScopeLegacyQuota("professional"))
			if err != nil {
				t.Fatal(err)
			}
			marketScopeInstallTestPolicy(db)
			// Even a new combination using still-active old sources is an expansion.
			if _, err = db.CreateMarketCombinationWithinQuota(ctx, CreateMarketCombinationParams{DeBoxUserID: owner, Note: "must reject", Members: []CreateMarketCombinationMemberParams{{SourceType: "market", MarketRuleID: &buy.ID}, {SourceType: "market", MarketRuleID: &sell.ID}}}, marketScopeLegacyQuota("professional")); !errors.Is(err, tc.want) {
				t.Fatalf("combination create=%v", err)
			}
			if _, err = pool.Exec(ctx, `UPDATE market_rules SET run_status='paused',pause_reason='subscription_expired',state='{"keep":123}',last_evaluated_at='2026-01-02T03:04:05Z',last_triggered_at='2026-01-02T02:00:00Z',aggregation_anchor_at='2026-01-02T01:00:00Z' WHERE debox_user_id=$1`, owner); err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, `UPDATE market_combination_rules SET run_status='paused',pause_reason='subscription_expired',aggregation_anchor_at='2026-01-02T01:00:00Z' WHERE id=$1`, combo.ID); err != nil {
				t.Fatal(err)
			}
			before := marketScopeOwnerSnapshot(t, ctx, pool, owner)
			if _, err = db.RestoreMarketRuleWithinQuota(ctx, buy.ID, owner, marketScopeLegacyQuota("professional")); !errors.Is(err, tc.want) {
				t.Fatalf("rule restore=%v", err)
			}
			if after := marketScopeOwnerSnapshot(t, ctx, pool, owner); after != before {
				t.Fatal("refused rule restore changed original state")
			}
			// Restore validates active member scopes; keep sources active so the
			// precise scope refusal is tested before any delivery fence/window edit.
			if _, err = pool.Exec(ctx, `UPDATE market_rules SET run_status='active' WHERE debox_user_id=$1`, owner); err != nil {
				t.Fatal(err)
			}
			before = marketScopeOwnerSnapshot(t, ctx, pool, owner)
			if _, err = db.RestoreMarketCombinationWithinQuota(ctx, combo.ID, owner, marketScopeLegacyQuota("professional")); !errors.Is(err, tc.want) {
				t.Fatalf("combo restore=%v", err)
			}
			if after := marketScopeOwnerSnapshot(t, ctx, pool, owner); after != before {
				t.Fatal("refused combo restore changed original state")
			}
			if _, err = pool.Exec(ctx, `UPDATE market_rules SET run_status='paused' WHERE debox_user_id=$1`, owner); err != nil {
				t.Fatal(err)
			}
			before = marketScopeOwnerSnapshot(t, ctx, pool, owner)
			if result, err := db.ReconcileUserEntitlements(ctx, owner, marketScopeLegacyQuota("professional")); err != nil || result.MarketRulesRestored != 0 || result.MarketCombinationsRestored != 0 {
				t.Fatalf("reconcile=%+v %v", result, err)
			}
			if after := marketScopeOwnerSnapshot(t, ctx, pool, owner); after != before {
				t.Fatal("reconcile altered incompatible saved parameters/state")
			}
			if _, err = db.SetMarketProjectStatus(ctx, project.ID, owner, "paused", "user_paused"); err != nil {
				t.Fatal(err)
			}
			before = marketScopeOwnerSnapshot(t, ctx, pool, owner)
			if _, err = db.RestoreMarketProjectWithinQuota(ctx, project.ID, owner, marketScopeLegacyQuota("professional")); !errors.Is(err, tc.want) {
				t.Fatalf("project restore=%v", err)
			}
			if _, err = db.SetMarketProjectStatus(ctx, project.ID, owner, "active", ""); !errors.Is(err, tc.want) {
				t.Fatalf("raw project restore=%v", err)
			}
			if after := marketScopeOwnerSnapshot(t, ctx, pool, owner); after != before {
				t.Fatal("project restore changed incompatible saved scope")
			}
			if _, err = pool.Exec(ctx, `UPDATE market_projects SET pause_reason='subscription_expired',frozen_at='2026-01-02T01:00:00Z' WHERE id=$1`, project.ID); err != nil {
				t.Fatal(err)
			}
			before = marketScopeOwnerSnapshot(t, ctx, pool, owner)
			if result, err := db.ReconcileUserEntitlements(ctx, owner, marketScopeLegacyQuota("professional")); err != nil || result.ProjectsRestored != 0 {
				t.Fatalf("project reconcile=%+v %v", result, err)
			}
			if after := marketScopeOwnerSnapshot(t, ctx, pool, owner); after != before {
				t.Fatal("project reconcile rewrote old scope/state")
			}
		})
	}
}

func TestPostgresMarketScopeDiscoveryKeepsMetadataButDoesNotSelectOtherChains(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	const owner = "scope-discovery"
	resourceCapacitySubscriptionFixture(t, pool, owner, "professional", 1)
	project := marketScopeProjectFixture(t, db, owner, "base", 8453, 2700)
	pools := marketScopePoolsFixture(t, db, project, 2710, 2)
	marketScopeInstallTestPolicy(db)
	if _, err := db.EnsureMarketProjectPool(ctx, EnsureMarketProjectPoolParams{DeBoxUserID: owner, MarketProjectID: project.ID, MarketPoolID: pools[0].ID}); err != nil {
		t.Fatalf("unselected history metadata rejected: %v", err)
	}
	before := resourceConfigurationMarketSnapshot(t, ctx, pool)
	if _, err := db.EnsureMarketProjectPool(ctx, EnsureMarketProjectPoolParams{DeBoxUserID: owner, MarketProjectID: project.ID, MarketPoolID: pools[1].ID, SelectIfNone: true}); !errors.Is(err, ErrMarketChainNotAllowed) {
		t.Fatalf("automatic other-chain select=%v", err)
	}
	if after := resourceConfigurationMarketSnapshot(t, ctx, pool); after != before {
		t.Fatal("failed discovery selection changed saved scope")
	}
	if _, err := db.LinkMarketProjectPool(ctx, LinkMarketProjectPoolParams{DeBoxUserID: owner, MarketProjectID: project.ID, MarketPoolID: pools[0].ID, Selected: true}); !errors.Is(err, ErrMarketChainNotAllowed) {
		t.Fatal("raw link bypassed chain gate", err)
	}
	// Stopping an old configuration is always available.
	if _, err := db.LinkMarketProjectPool(ctx, LinkMarketProjectPoolParams{DeBoxUserID: owner, MarketProjectID: project.ID, MarketPoolID: pools[0].ID, Selected: false}); err != nil {
		t.Fatal("old-chain deselection was blocked", err)
	}
}

func TestPostgresMarketScopeRemovedDeploymentIsHistoryAndBSCDiscoveryStillWorks(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	const owner = "scope-removed-history"
	resourceCapacitySubscriptionFixture(t, pool, owner, "professional", 1)
	baseToken := fmt.Sprintf("0x%040x", 2800)
	bscToken := fmt.Sprintf("0x%040x", 2801)
	basePool := marketScopePoolsFixture(t, db, MarketProject{ChainKey: "base", ChainID: 8453, TokenAddress: baseToken}, 2810, 1)[0]
	bscPool := marketScopePoolsFixture(t, db, MarketProject{ChainKey: "bsc", ChainID: 56, TokenAddress: bscToken}, 2820, 1)[0]
	at := time.Now().UTC()
	project, err := db.CreateMultiChainMarketProjectWithinQuota(ctx, CreateMultiChainMarketProjectParams{DeBoxUserID: owner, CanonicalName: "Historical primary", Symbol: "HIST", IdentitySource: "coingecko", CanonicalAssetID: "scope-historical-primary", VerificationStatus: "verified", Deployments: []CreateMultiChainMarketDeploymentParams{
		wizardIntegrationDeployment("base", 8453, baseToken, basePool.ID, at), wizardIntegrationDeployment("bsc", 56, bscToken, bscPool.ID, at),
	}}, marketScopeLegacyQuota("professional"))
	if err != nil {
		t.Fatal(err)
	}
	var oldDeploymentID int64
	if err = pool.QueryRow(ctx, `SELECT mpd.id FROM market_project_deployments mpd JOIN market_asset_deployments mad ON mad.id=mpd.market_asset_deployment_id WHERE mpd.market_project_id=$1 AND mad.chain_id=8453`, project.ID).Scan(&oldDeploymentID); err != nil {
		t.Fatal(err)
	}
	// The all-scope resolver ignores these old explicit selectors once their
	// deployment is removed; retain them as history rather than rewriting them.
	dynamicRule, err := db.CreateMarketRuleWithinQuota(ctx, CreateMarketRuleParams{DeBoxUserID: owner, MarketProjectID: project.ID, MarketPoolID: &basePool.ID,
		RuleType: "market_large_buy", ThresholdValue: "1", ThresholdUnit: "token", DeploymentScope: "all", MarketProjectDeploymentIDs: []int64{oldDeploymentID}, PoolScope: "all"}, marketScopeLegacyQuota("professional"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE market_project_deployments SET status='removed' WHERE market_project_id=$1 AND market_asset_deployment_id IN(SELECT id FROM market_asset_deployments WHERE chain_id=8453)`, project.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE market_project_pools SET selected=0,is_primary=0 WHERE market_project_id=$1 AND market_pool_id=$2`, project.ID, basePool.ID); err != nil {
		t.Fatal(err)
	}
	marketScopeInstallTestPolicy(db)
	if _, err = db.SetMarketProjectStatus(ctx, project.ID, owner, "paused", "user_paused"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.RestoreMarketProjectWithinQuota(ctx, project.ID, owner, marketScopeLegacyQuota("professional")); err != nil {
		t.Fatalf("historical legacy primary blocked remaining BSC deployment: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE market_rules SET run_status='paused',pause_reason='user_paused' WHERE id=$1`, dynamicRule.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.RestoreMarketRuleWithinQuota(ctx, dynamicRule.ID, owner, marketScopeLegacyQuota("professional")); err != nil {
		t.Fatalf("inactive old selectors blocked an all-scope rule: %v", err)
	}
	var removed int
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_project_deployments WHERE market_project_id=$1 AND status='removed'`, project.ID).Scan(&removed); err != nil || removed != 1 {
		t.Fatal("restore altered removed history", removed, err)
	}
	const freshOwner = "scope-valid-discovery"
	resourceCapacitySubscriptionFixture(t, pool, freshOwner, "professional", 1)
	fresh := marketScopeProjectFixture(t, db, freshOwner, "bsc", 56, 2900)
	freshPool := marketScopePoolsFixture(t, db, fresh, 2910, 1)[0]
	link, err := db.EnsureMarketProjectPool(ctx, EnsureMarketProjectPoolParams{DeBoxUserID: freshOwner, MarketProjectID: fresh.ID, MarketPoolID: freshPool.ID, SelectIfNone: true})
	if err != nil || link.Selected != 1 || link.IsPrimary != 1 {
		t.Fatalf("valid BSC automatic first selection=%+v %v", link, err)
	}
}

func TestPostgresMarketScopeDynamicPoolRestoreIgnoresInactiveSelectors(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	const owner = "scope-dynamic-primary"
	resourceCapacitySubscriptionFixture(t, pool, owner, "professional", 1)
	project := marketScopeProjectFixture(t, db, owner, "bsc", 56, 3000)
	pools := marketScopePoolsFixture(t, db, project, 3010, 2)
	marketScopeLinkFixture(t, db, project, pools)
	marketScopeInstallTestPolicy(db)
	rules := make(map[string]MarketRule, 3)
	for _, scope := range []string{"primary", "all", "selected"} {
		rule, err := db.CreateMarketRuleWithinQuota(ctx, CreateMarketRuleParams{DeBoxUserID: owner, MarketProjectID: project.ID, MarketPoolID: &pools[0].ID,
			RuleType: "market_large_buy", ThresholdValue: "1", ThresholdUnit: "token", PoolScope: scope}, marketScopeLegacyQuota("professional"))
		if err != nil {
			t.Fatalf("create %s: %v", scope, err)
		}
		rules[scope] = rule
	}
	if _, err := db.LinkMarketProjectPool(ctx, LinkMarketProjectPoolParams{DeBoxUserID: owner, MarketProjectID: project.ID, MarketPoolID: pools[1].ID, Selected: true, IsPrimary: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LinkMarketProjectPool(ctx, LinkMarketProjectPoolParams{DeBoxUserID: owner, MarketProjectID: project.ID, MarketPoolID: pools[0].ID, Selected: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE market_rules SET run_status='paused',pause_reason='user_paused',state='{"keep":456}' WHERE debox_user_id=$1`, owner); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"primary", "all"} {
		restored, err := db.RestoreMarketRuleWithinQuota(ctx, rules[scope].ID, owner, marketScopeLegacyQuota("professional"))
		if err != nil {
			t.Fatalf("restore %s after primary switch: %v", scope, err)
		}
		var state map[string]int
		stateErr := json.Unmarshal(restored.State, &state)
		if restored.MarketPoolID == nil || *restored.MarketPoolID != pools[0].ID || stateErr != nil || state["keep"] != 456 {
			t.Fatalf("restore rewrote saved selector/state: %+v", restored)
		}
		targets, err := db.ListMarketRuleTargets(ctx, restored, project)
		if err != nil || len(targets) != 1 || targets[0].MarketPoolID == nil || *targets[0].MarketPoolID != pools[1].ID {
			t.Fatalf("%s no longer follows current effective pool: %+v %v", scope, targets, err)
		}
	}
	// A selected scope still uses the legacy scalar ID and saved selections.
	// It must remain refused while A is no longer selected, without rewriting it.
	before := marketScopeOwnerSnapshot(t, ctx, pool, owner)
	if _, err := db.RestoreMarketRuleWithinQuota(ctx, rules["selected"].ID, owner, marketScopeLegacyQuota("professional")); !errors.Is(err, ErrMarketPoolMismatch) {
		t.Fatalf("inactive explicit selector was ignored: %v", err)
	}
	if after := marketScopeOwnerSnapshot(t, ctx, pool, owner); after != before {
		t.Fatal("rejected selected restore changed saved state")
	}
}
