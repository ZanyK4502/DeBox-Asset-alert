package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
)

func proFiveCatalogPolicies(t *testing.T) (map[string]QuotaPolicy, QuotaPolicy) {
	t.Helper()
	catalog, err := plans.NewCatalog("10", 30, "USDT")
	if err != nil {
		t.Fatal(err)
	}
	policies := make(map[string]QuotaPolicy, 3)
	for _, code := range []string{plans.Free, plans.Standard, plans.Professional} {
		plan, err := catalog.Get(code)
		if err != nil {
			t.Fatal(err)
		}
		policies[code] = QuotaPolicy{PlanCode: code, WalletLimit: plan.WalletLimit, RuleLimit: plan.RuleLimit, InteractionRuleLimit: plan.InteractionRuleLimit,
			MarketProjectLimit: plan.MarketProjectLimit, MarketPoolLimit: plan.MarketPoolLimit, AllowedMarketChainKeys: append([]string{}, plan.AllowedMarketChainKeys...),
			AllowedRuleTypes: append([]string(nil), plan.AllowedRuleTypes...), AllowedMarketRuleTypes: append([]string(nil), plan.AllowedMarketRules...),
			StageNotifications: plan.AllowsStageNotifications(), CombinationRules: plan.AllowsCombinationRules(), MultiPoolMonitoring: plan.MarketPoolMode == "multiple"}
	}
	pro := policies[plans.Professional]
	if pro.MarketProjectLimit != 5 || pro.MarketPoolLimit != 5 || pro.RuleLimit != 10 || pro.InteractionRuleLimit != 1 {
		t.Fatalf("real default catalog is not the authorized5/5 with shared10/interaction1: %+v", pro)
	}
	if standard := policies[plans.Standard]; standard.MarketProjectLimit != 1 || standard.MarketPoolLimit != 1 || standard.RuleLimit != 3 || policies[plans.Free].RuleLimit != 1 {
		t.Fatal("unrelated plan capacities changed")
	}
	return policies, pro
}

func proFiveInstall(db *Store, policies map[string]QuotaPolicy) {
	settings := openSettings{}
	WithMarketScopePolicies(policies)(&settings)
	db.marketScopePolicies = settings.marketScopePolicies
}
func proFiveProject(owner string, index int) CreateMarketProjectParams {
	return CreateMarketProjectParams{DeBoxUserID: owner, ChainKey: "bsc", ChainID: 56, TokenAddress: fmt.Sprintf("0x%040x", index), TokenName: "Five project fixture", TokenSymbol: "FIVE", TokenDecimals: 18}
}

func TestPostgresProFiveScopeRealCatalogCreatesFiveProjectsAndFivePools(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	const owner = "pro-five-projects"
	policies, pro := proFiveCatalogPolicies(t)
	proFiveInstall(db, policies)
	resourceCapacitySubscriptionFixture(t, pool, owner, plans.Professional, 1)
	forged := pro
	forged.MarketProjectLimit = 99
	forged.MarketPoolLimit = 99
	forged.AllowedMarketChainKeys = []string{"bsc", "base"}
	for i := 0; i < 5; i++ {
		params := proFiveProject(owner, 5000+i)
		pools := marketScopePoolsFixture(t, db, MarketProject{ChainKey: "bsc", ChainID: 56, TokenAddress: params.TokenAddress}, 5100+i*10, 6)
		var project MarketProject
		var err error
		if i%2 == 0 {
			project, err = db.CreateMarketProjectWithinQuota(ctx, params, forged)
			if err != nil {
				t.Fatalf("project%d: %v", i+1, err)
			}
			for j := 0; j < 5; j++ {
				_, err = db.LinkMarketProjectPoolWithinQuota(ctx, LinkMarketProjectPoolParams{DeBoxUserID: owner, MarketProjectID: project.ID, MarketPoolID: pools[j].ID, Selected: true, IsPrimary: j == 0}, forged)
				if err != nil {
					t.Fatalf("project%d selected pool%d: %v", i+1, j+1, err)
				}
				if j >= 2 {
					var count int
					if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_project_pools WHERE market_project_id=$1 AND selected=1`, project.ID).Scan(&count); err != nil || count != j+1 {
						t.Fatal("selected3..5 not retained", count, err)
					}
				}
			}
			if _, err = db.EnsureMarketProjectPool(ctx, EnsureMarketProjectPoolParams{DeBoxUserID: owner, MarketProjectID: project.ID, MarketPoolID: pools[5].ID}); err != nil {
				t.Fatal(err)
			}
		} else {
			deployment := wizardIntegrationDeployment("bsc", 56, params.TokenAddress, pools[0].ID, time.Now().UTC())
			for j := 1; j < 6; j++ {
				deployment.Pools = append(deployment.Pools, CreateMultiChainMarketPoolParams{MarketPoolID: pools[j].ID, Selected: j < 5})
			}
			project, err = db.CreateMultiChainMarketProjectWithinQuota(ctx, CreateMultiChainMarketProjectParams{DeBoxUserID: owner, CanonicalName: params.TokenName, Symbol: params.TokenSymbol, IdentitySource: "local-pro-five", CanonicalAssetID: fmt.Sprint(i), VerificationStatus: "verified", Deployments: []CreateMultiChainMarketDeploymentParams{deployment}}, forged)
			if err != nil {
				t.Fatalf("multichain public API single BSC project%d/five selected: %v", i+1, err)
			}
		}
		before := resourceConfigurationMarketSnapshot(t, ctx, pool)
		for _, raw := range []bool{false, true} {
			params := LinkMarketProjectPoolParams{DeBoxUserID: owner, MarketProjectID: project.ID, MarketPoolID: pools[5].ID, Selected: true}
			if raw {
				_, err = db.LinkMarketProjectPool(ctx, params)
			} else {
				_, err = db.LinkMarketProjectPoolWithinQuota(ctx, params, forged)
			}
			if !errors.Is(err, ErrMarketPoolLimitReached) {
				t.Fatalf("project%d sixth pool(raw=%v) error=%v", i+1, raw, err)
			}
			if after := resourceConfigurationMarketSnapshot(t, ctx, pool); after != before {
				t.Fatal("sixth-pool rejection changed original scope/metadata")
			}
		}
		for _, kind := range []string{plans.MarketLargeBuy, plans.MarketLargeSell} {
			rule, err := db.CreateMarketRuleWithinQuota(ctx, CreateMarketRuleParams{DeBoxUserID: owner, MarketProjectID: project.ID, RuleType: kind, ThresholdValue: "1", ThresholdUnit: "token", PoolScope: "all"}, pro)
			if err != nil {
				t.Fatal(err)
			}
			targets, err := db.ListMarketRuleTargets(ctx, rule, project)
			if err != nil || len(targets) != 5 {
				t.Fatalf("five actual selected pools not in rule targets: %d %v", len(targets), err)
			}
			if i == 0 && kind == plans.MarketLargeBuy {
				at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
				if _, err := pool.Exec(ctx, `UPDATE market_rules SET run_status='paused',pause_reason='user_paused',state='{"keep":5}',last_triggered_at=$2 WHERE id=$1`, rule.ID, at); err != nil {
					t.Fatal(err)
				}
				restored, err := db.RestoreMarketRuleWithinQuota(ctx, rule.ID, owner, pro)
				if err != nil {
					t.Fatal("valid five-pool rule restore was rejected", err)
				}
				var state map[string]int
				if err := json.Unmarshal(restored.State, &state); err != nil || state["keep"] != 5 || restored.LastTriggeredAt == nil || !restored.LastTriggeredAt.Equal(at) {
					t.Fatal("valid restore altered existing rule state/cooldown", err)
				}
			}
		}
	}
	before := resourceConfigurationMarketSnapshot(t, ctx, pool)
	if _, err := db.CreateMarketProjectWithinQuota(ctx, proFiveProject(owner, 5900), forged); !errors.Is(err, ErrMarketProjectLimitReached) {
		t.Fatalf("sixth project=%v", err)
	}
	if after := resourceConfigurationMarketSnapshot(t, ctx, pool); after != before {
		t.Fatal("sixth project left residue")
	}
	var projectID int64
	if err := pool.QueryRow(ctx, `SELECT MIN(id) FROM market_projects WHERE debox_user_id=$1`, owner).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateMarketRuleWithinQuota(ctx, CreateMarketRuleParams{DeBoxUserID: owner, MarketProjectID: projectID, RuleType: plans.MarketPriceAbove, ThresholdValue: "1", PoolScope: "primary"}, pro); !errors.Is(err, ErrRuleLimitReached) {
		t.Fatal("five projects expanded shared10 rule quota", err)
	}
	var projects, selected, allLinks, rules int
	if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM market_projects WHERE debox_user_id=$1 AND status='active'),(SELECT COUNT(*) FROM market_project_pools mpp JOIN market_projects mp ON mp.id=mpp.market_project_id WHERE mp.debox_user_id=$1 AND mpp.selected=1),(SELECT COUNT(*) FROM market_project_pools mpp JOIN market_projects mp ON mp.id=mpp.market_project_id WHERE mp.debox_user_id=$1),(SELECT COUNT(*) FROM market_rules WHERE debox_user_id=$1 AND enabled=1 AND run_status='active')`, owner).Scan(&projects, &selected, &allLinks, &rules); err != nil || projects != 5 || selected != 25 || allLinks != 30 || rules != 10 {
		t.Fatal(projects, selected, allLinks, rules, err)
	}
	// Base is now an allowed monitoring chain, but switching chains must not
	// create a sixth logical project outside the shared project quota.
	if _, err := db.CreateMarketProjectWithinQuota(ctx, CreateMarketProjectParams{DeBoxUserID: owner, ChainKey: "base", ChainID: 8453, TokenAddress: fmt.Sprintf("0x%040x", 5999)}, forged); !errors.Is(err, ErrMarketProjectLimitReached) {
		t.Fatal("cross-chain project bypassed the shared five-project limit", err)
	}
}

func TestPostgresProFiveScopeConcurrentFifthProjectAndPoolDoNotOversell(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	const owner = "pro-five-race"
	policies, pro := proFiveCatalogPolicies(t)
	proFiveInstall(db, policies)
	resourceCapacitySubscriptionFixture(t, pool, owner, plans.Professional, 1)
	var first MarketProject
	for i := 0; i < 4; i++ {
		project, err := db.CreateMarketProjectWithinQuota(ctx, proFiveProject(owner, 6000+i), pro)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = project
		}
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			<-start
			_, err := db.CreateMarketProjectWithinQuota(ctx, proFiveProject(owner, 6010+index), pro)
			results <- err
		}(i)
	}
	close(start)
	workers.Wait()
	close(results)
	success, refused := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrMarketProjectLimitReached) {
			refused++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || refused != 1 {
		t.Fatal("concurrent projects exceeded five", success, refused)
	}
	pools := marketScopePoolsFixture(t, db, first, 6100, 6)
	marketScopeLinkFixture(t, db, first, pools[:4])
	start = make(chan struct{})
	results = make(chan error, 2)
	for _, candidate := range pools[4:] {
		workers.Add(1)
		go func(id int64) {
			defer workers.Done()
			<-start
			_, err := db.LinkMarketProjectPool(ctx, LinkMarketProjectPoolParams{DeBoxUserID: owner, MarketProjectID: first.ID, MarketPoolID: id, Selected: true})
			results <- err
		}(candidate.ID)
	}
	close(start)
	workers.Wait()
	close(results)
	success, refused = 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrMarketPoolLimitReached) {
			refused++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || refused != 1 {
		t.Fatal("concurrent selections exceeded five", success, refused)
	}
	var projects, selected, links int
	if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM market_projects WHERE debox_user_id=$1 AND status='active'),(SELECT COUNT(*) FROM market_project_pools WHERE market_project_id=$2 AND selected=1),(SELECT COUNT(*) FROM market_project_pools WHERE market_project_id=$2)`, owner, first.ID).Scan(&projects, &selected, &links); err != nil || projects != 5 || selected != 5 || links != 5 {
		t.Fatal("failed candidate left partial row", projects, selected, links, err)
	}
}

func TestPostgresProFiveScopeRestoresFifthButPreservesSixthAndOldOverscope(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	policies, pro := proFiveCatalogPolicies(t)
	const owner = "pro-five-restore"
	resourceCapacitySubscriptionFixture(t, pool, owner, plans.Professional, 1)
	legacy := pro
	legacy.MarketProjectLimit = 10
	legacy.MarketPoolLimit = 0
	legacy.AllowedMarketChainKeys = nil
	var projects []MarketProject
	for i := 0; i < 6; i++ {
		project, err := db.CreateMarketProjectWithinQuota(ctx, proFiveProject(owner, 6200+i), legacy)
		if err != nil {
			t.Fatal(err)
		}
		projects = append(projects, project)
	}
	for _, project := range projects[4:] {
		if _, err := db.SetMarketProjectStatus(ctx, project.ID, owner, "paused", "user_paused"); err != nil {
			t.Fatal(err)
		}
	}
	proFiveInstall(db, policies)
	if _, err := db.RestoreMarketProjectWithinQuota(ctx, projects[4].ID, owner, pro); err != nil {
		t.Fatalf("fifth restore=%v", err)
	}
	before := marketScopeOwnerSnapshot(t, ctx, pool, owner)
	if _, err := db.RestoreMarketProjectWithinQuota(ctx, projects[5].ID, owner, pro); !errors.Is(err, ErrMarketProjectLimitReached) {
		t.Fatalf("sixth restore=%v", err)
	}
	if after := marketScopeOwnerSnapshot(t, ctx, pool, owner); after != before {
		t.Fatal("refused restore altered saved project state")
	}
	// A preexisting six-pool configuration stays intact and is never clipped
	// merely because the new policy is installed or reconciliation runs.
	const oldOwner = "pro-five-legacy-six-pools"
	resourceCapacitySubscriptionFixture(t, pool, oldOwner, plans.Professional, 1)
	db.marketScopePolicies = nil
	old, err := db.CreateMarketProjectWithinQuota(ctx, proFiveProject(oldOwner, 6300), legacy)
	if err != nil {
		t.Fatal(err)
	}
	marketScopeLinkFixture(t, db, old, marketScopePoolsFixture(t, db, old, 6310, 6))
	rule, err := db.CreateMarketRuleWithinQuota(ctx, CreateMarketRuleParams{DeBoxUserID: oldOwner, MarketProjectID: old.ID, RuleType: plans.MarketLargeBuy, ThresholdValue: "123", ThresholdUnit: "token", PoolScope: "all", State: json.RawMessage(`{"keep":42}`)}, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE market_projects SET status='paused',pause_reason='user_paused' WHERE id=$1`, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE market_rules SET run_status='paused',pause_reason='user_paused',last_evaluated_at='2026-01-02T03:04:05Z',last_triggered_at='2026-01-02T02:00:00Z' WHERE id=$1`, rule.ID); err != nil {
		t.Fatal(err)
	}
	proFiveInstall(db, policies)
	before = marketScopeOwnerSnapshot(t, ctx, pool, oldOwner)
	if _, err = db.RestoreMarketProjectWithinQuota(ctx, old.ID, oldOwner, pro); !errors.Is(err, ErrMarketPoolLimitReached) {
		t.Fatal("old six-pool project restored", err)
	}
	if _, err = db.RestoreMarketRuleWithinQuota(ctx, rule.ID, oldOwner, pro); !errors.Is(err, ErrMarketPoolLimitReached) {
		t.Fatal("old six-pool rule restored", err)
	}
	if after := marketScopeOwnerSnapshot(t, ctx, pool, oldOwner); after != before {
		t.Fatal("old overscope was clipped or rule baseline changed")
	}
	if _, err = pool.Exec(ctx, `UPDATE market_projects SET pause_reason='subscription_expired' WHERE id=$1`, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE market_rules SET pause_reason='subscription_expired' WHERE id=$1`, rule.ID); err != nil {
		t.Fatal(err)
	}
	before = marketScopeOwnerSnapshot(t, ctx, pool, oldOwner)
	if result, err := db.ReconcileUserEntitlements(ctx, oldOwner, pro); err != nil || result.ProjectsRestored != 0 || result.MarketRulesRestored != 0 {
		t.Fatal(result, err)
	}
	if after := marketScopeOwnerSnapshot(t, ctx, pool, oldOwner); after != before {
		t.Fatal("auto reconcile clipped or rewrote old six-pool scope")
	}
}
