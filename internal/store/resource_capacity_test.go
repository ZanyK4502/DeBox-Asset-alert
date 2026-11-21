package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func resourceCapacityTestPolicy() ResourceCapacityPolicy {
	return ResourceCapacityPolicy{Verified: true, AccountMonthlyCU: 1000, OtherMonthlyCU: 100, BackgroundMonthlyCU: 200, SafetyMonthlyCU: 100,
		OrdinaryRuleMonthlyCU: 10, InteractionRuleMonthlyCU: 50, MarketProjectMonthlyCU: 100,
		MarketProjectCostMaxPools: 5, MarketProjectCostChainKeys: []string{"bsc", "ethereum", "base", "polygon", "arbitrum", "optimism"},
		Plans: map[string]ResourcePlanCapacity{"free": {RuleLimit: 1},
			"standard":     {RuleLimit: 3, MarketProjectLimit: 1, MarketPoolLimit: 1, AllowedMarketChainKeys: []string{"bsc"}},
			"professional": {RuleLimit: 10, InteractionRuleLimit: 1, MarketProjectLimit: 1, MarketPoolLimit: 1, AllowedMarketChainKeys: []string{"bsc"}}}}
}

func TestResourceCapacityPolicyCostsArePositiveBoundedAndNotInvented(t *testing.T) {
	p := resourceCapacityTestPolicy()
	if err := validateResourceCapacityPolicy(p); err != nil {
		t.Fatal(err)
	}
	for code, want := range map[string]int64{"free": 10, "standard": 130, "professional": 240} {
		got, err := resourcePlanMonthlyCU(p, p.Plans[code])
		if err != nil || got != want {
			t.Fatalf("%s CU=%d/%v want%d", code, got, err, want)
		}
	}
	if got := resourceNetMonthlyCU(p); got != 600 {
		t.Fatal(got)
	}
	p.Verified = false
	p.AccountMonthlyCU = 0
	if err := validateResourceCapacityPolicy(p); err != nil || resourceNetMonthlyCU(p) != 0 {
		t.Fatalf("unknown sellable budget must remain closed without invalidating model: %v", err)
	}
	for _, mutate := range []func(*ResourceCapacityPolicy){
		func(p *ResourceCapacityPolicy) { p.OrdinaryRuleMonthlyCU = 0 },
		func(p *ResourceCapacityPolicy) { p.InteractionRuleMonthlyCU = 9 },
		func(p *ResourceCapacityPolicy) { p.MarketProjectMonthlyCU = 0 },
		func(p *ResourceCapacityPolicy) { p.SafetyMonthlyCU = -1 },
		func(p *ResourceCapacityPolicy) { delete(p.Plans, "free") },
		func(p *ResourceCapacityPolicy) {
			p.Plans["professional"] = ResourcePlanCapacity{RuleLimit: 1, InteractionRuleLimit: 2}
		},
		func(p *ResourceCapacityPolicy) {
			p.InteractionRuleMonthlyCU = math.MaxInt64
			p.Plans["professional"] = ResourcePlanCapacity{RuleLimit: 10, InteractionRuleLimit: 2}
		},
	} {
		candidate := resourceCapacityTestPolicy()
		candidate.Verified = false
		mutate(&candidate)
		if err := validateResourceCapacityPolicy(candidate); err == nil {
			t.Fatal("invalid or unaccountable model accepted")
		}
	}
	p = resourceCapacityTestPolicy()
	p.OtherMonthlyCU = math.MaxInt64
	p.BackgroundMonthlyCU = math.MaxInt64
	if got := resourceNetMonthlyCU(p); got != 0 {
		t.Fatalf("reserve overflow produced sellable capacity: %d", got)
	}
}

func TestResourceCapacityChainPricingReservesWorstAllowedPoolPlacement(t *testing.T) {
	p := resourceCapacityTestPolicy()
	p.MarketChainMonthlyCU = map[string]plans.MarketChainCUCost{
		"bsc": {Deployment: 50, Pool: 10}, "base": {Deployment: 30, Pool: 35}, "ethereum": {Deployment: 80, Pool: 1},
	}
	pro := p.Plans["professional"]
	pro.AllowedMarketChainKeys = []string{"bsc", "base", "ethereum"}
	pro.MarketProjectLimit, pro.MarketPoolLimit = 2, 2
	p.Plans["professional"] = pro
	if err := validateResourceCapacityPolicy(p); err != nil {
		t.Fatal(err)
	}
	// For each project, Base can hold both pools and Ethereum can keep an
	// active deployment with no pools. The BSC deployment isn't needed.
	if got, err := resourcePlanMonthlyCU(p, pro); err != nil || got != 500 {
		t.Fatalf("professional CU=%d, %v; want 500", got, err)
	}
	if got, err := resourcePlanMonthlyCU(p, p.Plans["standard"]); err != nil || got != 90 {
		t.Fatalf("single-chain standard CU=%d, %v; want 90", got, err)
	}
	settings := openSettings{}
	WithResourceCapacityPolicy(p)(&settings)
	delete(p.MarketChainMonthlyCU, "base")
	if validateResourceCapacityPolicy(p) == nil || validateResourceCapacityPolicy(*settings.resourceCapacity) != nil {
		t.Fatal("missing price admitted or caller modified installed prices")
	}
}

func TestResourceCapacityOptionCopiesPlansAndAbsentOptionKeepsLegacyPath(t *testing.T) {
	p := resourceCapacityTestPolicy()
	settings := openSettings{}
	WithResourceCapacityPolicy(p)(&settings)
	p.MarketProjectCostChainKeys[0] = "base"
	p.Plans["professional"].AllowedMarketChainKeys[0] = "polygon"
	p.Plans["professional"] = ResourcePlanCapacity{RuleLimit: 999}
	if settings.resourceCapacity.Plans["professional"].RuleLimit != 10 ||
		settings.resourceCapacity.Plans["professional"].AllowedMarketChainKeys[0] != "bsc" ||
		settings.resourceCapacity.MarketProjectCostChainKeys[0] != "bsc" {
		t.Fatal("caller mutated installed resource policy")
	}
	db := newWithDB(nil)
	ctx := context.Background()
	got, cancel, err := db.beginResourceCapacityCheck(ctx, nil)
	defer cancel()
	if err != nil || got != ctx {
		t.Fatal(err)
	}
	if err = db.checkResourceCapacity(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err = db.reserveResourceCapacityOrder(ctx, nil, Order{}); err != nil {
		t.Fatal(err)
	}
	if err = db.linkResourceCapacityOrder(ctx, nil, 0, 0); err != nil {
		t.Fatal(err)
	}
	status, err := db.ReadResourceCapacityStatus(ctx)
	if err != nil || status.Configured {
		t.Fatal(status, err)
	}
}

func TestResourceCapacityVerifiedCostScopeCoversEachChainTokenWithoutChargingEachPool(t *testing.T) {
	p := resourceCapacityTestPolicy()
	pro := p.Plans["professional"]
	pro.MarketProjectLimit, pro.MarketPoolLimit = 5, 5
	p.Plans["professional"] = pro
	if err := validateResourceCapacityPolicy(p); err != nil {
		t.Fatal(err)
	}
	if cu, err := resourcePlanMonthlyCU(p, pro); err != nil || cu != 640 {
		t.Fatalf("five single-chain project envelopes were priced as individual pools: %d/%v", cu, err)
	}
	pro.MarketPoolLimit = 1
	if cu, err := resourcePlanMonthlyCU(p, pro); err != nil || cu != 640 {
		t.Fatalf("single-chain collection cost changed within covered pool scope: %d/%v", cu, err)
	}
	for _, mutate := range []func(*ResourceCapacityPolicy){
		func(p *ResourceCapacityPolicy) { p.MarketProjectCostMaxPools = 4 },
		func(p *ResourceCapacityPolicy) { p.MarketProjectCostMaxPools = 0 },
		func(p *ResourceCapacityPolicy) { p.MarketProjectCostChainKeys = nil },
		func(p *ResourceCapacityPolicy) { p.MarketProjectCostChainKeys = []string{} },
		func(p *ResourceCapacityPolicy) { p.MarketProjectCostChainKeys = []string{"ethereum"} },
		func(p *ResourceCapacityPolicy) {
			pro := p.Plans["professional"]
			pro.MarketPoolLimit = 0
			p.Plans["professional"] = pro
		},
		func(p *ResourceCapacityPolicy) {
			pro := p.Plans["professional"]
			pro.AllowedMarketChainKeys = nil
			p.Plans["professional"] = pro
		},
	} {
		candidate := resourceCapacityTestPolicy()
		pro := candidate.Plans["professional"]
		pro.MarketProjectLimit, pro.MarketPoolLimit = 5, 5
		candidate.Plans["professional"] = pro
		mutate(&candidate)
		if err := validateResourceCapacityPolicy(candidate); !errors.Is(err, ErrResourceCapacityState) {
			t.Fatalf("verified model accepted an uncovered pool/chain promise: %v", err)
		}
	}
}

func TestResourceCapacityMultiChainPlanReservesEveryPotentialDeployment(t *testing.T) {
	allChains := []string{"bsc", "ethereum", "base", "polygon", "arbitrum", "optimism"}
	for _, test := range []struct {
		name        string
		chains      []string
		pools       int
		deployments int
		monthly     int64
	}{
		{"single_chain_five_pools", []string{"BNB"}, 5, 1, 640},
		{"two_chains_five_pools", []string{"bsc", "base"}, 5, 2, 1140},
		{"six_chains_five_pools", allChains, 5, 5, 2640},
		{"six_chains_one_pool", allChains, 1, 1, 640},
		{"unknown_chains_unverified", nil, 5, 5, 2640},
		{"unknown_pools_unverified", []string{"bsc", "base"}, 0, 2, 1140},
		{"unknown_scope_unverified", nil, 0, 6, 3140},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := resourceCapacityTestPolicy()
			p.Verified = test.pools > 0 && len(test.chains) > 0
			pro := p.Plans["professional"]
			pro.MarketProjectLimit, pro.MarketPoolLimit, pro.AllowedMarketChainKeys = 5, test.pools, test.chains
			p.Plans["professional"] = pro
			if err := validateResourceCapacityPolicy(p); err != nil {
				t.Fatal(err)
			}
			if units, err := resourcePlanDeploymentsPerProject(pro); err != nil || units != test.deployments {
				t.Fatalf("deployment envelope=%d/%v want%d", units, err, test.deployments)
			}
			if monthly, err := resourcePlanMonthlyCU(p, pro); err != nil || monthly != test.monthly {
				t.Fatalf("monthly promise=%d/%v want%d", monthly, err, test.monthly)
			}
			if !p.Verified {
				p.Verified = true
				if err := validateResourceCapacityPolicy(p); !errors.Is(err, ErrResourceCapacityState) {
					t.Fatalf("unknown scope became saleable: %v", err)
				}
			}
		})
	}
	p := resourceCapacityTestPolicy()
	pro := p.Plans["professional"]
	pro.MarketProjectLimit, pro.MarketPoolLimit, pro.AllowedMarketChainKeys = 5, 5, allChains
	p.MarketProjectMonthlyCU = math.MaxInt64 / 6
	if _, err := resourcePlanMonthlyCU(p, pro); !errors.Is(err, ErrResourceCapacityState) {
		t.Fatalf("multi-chain multiplication overflow admitted: %v", err)
	}
	free := p.Plans["free"]
	if units, err := resourcePlanDeploymentsPerProject(free); err != nil || units != 0 {
		t.Fatalf("no-market plan acquired collection units: %d/%v", units, err)
	}
}

type resourceCapacityJSONArgument func(any) bool

func (matches resourceCapacityJSONArgument) Match(value any) bool { return matches(value) }

func TestResourceCapacityNewOrderStoresMultiChainV3Promise(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	p := resourceCapacityTestPolicy()
	p.AccountMonthlyCU = 10_000
	pro := p.Plans["professional"]
	pro.MarketProjectLimit, pro.MarketPoolLimit = 5, 5
	pro.AllowedMarketChainKeys = append([]string(nil), p.MarketProjectCostChainKeys...)
	p.Plans["professional"] = pro
	db := newWithDB(mock)
	db.resourceCapacity = &p
	ctx := context.WithValue(context.Background(), resourceCapacityLockKey{}, resourceCapacityAdmission{policy: &p, owner: "owner"})
	matchedSnapshot := false
	mock.ExpectExec("INSERT INTO resource_capacity_holds").
		WithArgs(int64(7), "owner", "professional", int64(2640), resourceCapacityJSONArgument(func(value any) bool {
			body, ok := value.([]byte)
			if !ok {
				return false
			}
			var snapshot resourcePlanSnapshot
			if json.Unmarshal(body, &snapshot) != nil {
				return false
			}
			matchedSnapshot = snapshot.SchemaVersion == 3 && snapshot.PlanCode == "professional" && snapshot.MonthDays == 31 &&
				snapshot.MonthlyCU == 2640 && snapshot.MarketDeploymentsPerProject == 5 && snapshot.MarketProjectLimit == 5 && snapshot.MarketPoolLimit == 5 &&
				len(snapshot.AllowedMarketChainKeys) == 6 && len(snapshot.MarketProjectCostChainKeys) == 6 && snapshot.MarketProjectMonthlyCU == 100 &&
				snapshot.RuleLimit == 10 && snapshot.InteractionRuleLimit == 1 && snapshot.OrdinaryRuleMonthlyCU == 10 && snapshot.InteractionRuleMonthlyCU == 50
			return matchedSnapshot
		})).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery("WITH").WithArgs(pgxmock.AnyArg(), int64(10), int64(50), int64(100), "owner", json.RawMessage(nil), 5).
		WillReturnRows(pgxmock.NewRows([]string{"cu", "users", "unknown", "owner_cu"}).AddRow("2640", int64(1), false, "2640"))
	if err := db.reserveResourceCapacityOrder(ctx, mock, Order{ID: 7, DeBoxUserID: "owner", PlanCode: "professional", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if !matchedSnapshot {
		t.Fatal("new order did not persist its complete multi-chain promise")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestResourceCapacityUnknownCostScopeRemainsUnverified(t *testing.T) {
	p := resourceCapacityTestPolicy()
	p.Verified = false
	p.MarketProjectCostMaxPools, p.MarketProjectCostChainKeys = 0, nil
	pro := p.Plans["professional"]
	pro.MarketProjectLimit, pro.MarketPoolLimit = 5, 5
	p.Plans["professional"] = pro
	if err := validateResourceCapacityPolicy(p); err != nil || p.Verified || p.MarketProjectCostMaxPools != 0 || p.MarketProjectCostChainKeys != nil {
		t.Fatalf("unknown cost envelope was rejected or silently declared covered: %+v/%v", p, err)
	}
	if err := requireResourceCapacityStatus(ResourceCapacityStatus{Verified: p.Verified, NetMonthlyCU: 240_000_000, ReservedMonthlyCU: 0}); !errors.Is(err, ErrResourceCapacityUnavailable) {
		t.Fatalf("positive budget opened unverified sales: %v", err)
	}
	p.Verified = true
	if err := validateResourceCapacityPolicy(p); !errors.Is(err, ErrResourceCapacityState) {
		t.Fatalf("unknown cost scope became verified: %v", err)
	}
}

func TestResourceCapacityChainScopeNormalizesAliasesWithoutInventingDefaults(t *testing.T) {
	p := resourceCapacityTestPolicy()
	p.MarketProjectCostChainKeys = []string{" BNB "}
	pro := p.Plans["professional"]
	pro.AllowedMarketChainKeys = []string{"bnb_chain"}
	p.Plans["professional"] = pro
	if err := validateResourceCapacityPolicy(p); err != nil {
		t.Fatal(err)
	}
	if p.MarketProjectCostChainKeys[0] != " BNB " || p.Plans["professional"].AllowedMarketChainKeys[0] != "bnb_chain" {
		t.Fatal("validator modified caller scope slices")
	}
	for _, keys := range [][]string{{""}, {" "}, {"solana"}, {"bsc", "BNB"}, {"bsc", "ethereum", "base", "polygon", "arbitrum", "optimism", "bsc"}} {
		for _, verified := range []bool{false, true} {
			candidate := resourceCapacityTestPolicy()
			candidate.Verified, candidate.MarketProjectCostChainKeys = verified, keys
			if err := validateResourceCapacityPolicy(candidate); !errors.Is(err, ErrResourceCapacityState) {
				t.Fatalf("malformed explicit cost scope accepted: %v/%v", keys, err)
			}
			candidate = resourceCapacityTestPolicy()
			candidate.Verified = verified
			pro := candidate.Plans["professional"]
			pro.AllowedMarketChainKeys = keys
			candidate.Plans["professional"] = pro
			if err := validateResourceCapacityPolicy(candidate); !errors.Is(err, ErrResourceCapacityState) {
				t.Fatalf("malformed explicit plan scope accepted: %v/%v", keys, err)
			}
		}
	}
	for _, verified := range []bool{false, true} {
		candidate := resourceCapacityTestPolicy()
		candidate.Verified, candidate.MarketProjectCostMaxPools = verified, -1
		if err := validateResourceCapacityPolicy(candidate); !errors.Is(err, ErrResourceCapacityState) {
			t.Fatalf("negative cost pool scope accepted: %v", err)
		}
		candidate = resourceCapacityTestPolicy()
		candidate.Verified = verified
		pro := candidate.Plans["professional"]
		pro.MarketPoolLimit = -1
		candidate.Plans["professional"] = pro
		if err := validateResourceCapacityPolicy(candidate); !errors.Is(err, ErrResourceCapacityState) {
			t.Fatalf("negative plan pool scope accepted: %v", err)
		}
	}
}

func TestResourceCapacityChecksAllowOnlyNetNeutralUnverifiedOrOvercommittedWrites(t *testing.T) {
	for _, test := range []struct {
		name                      string
		verified                  bool
		before, after             int64
		globalBefore, globalAfter int64
		want                      error
	}{
		{"unverified_neutral", false, 100, 100, 100, 100, nil},
		{"unverified_reduction", false, 100, 90, 100, 90, nil},
		{"unverified_growth", false, 100, 110, 100, 110, ErrResourceCapacityUnavailable},
		{"unverified_growth_after_other_user_release", false, 0, 10, 100, 10, ErrResourceCapacityUnavailable},
		{"overcommitted_neutral", true, 700, 700, 700, 700, nil},
		{"overcommitted_growth", true, 700, 710, 700, 710, ErrResourceCapacityReached},
		{"verified_growth_within_budget", true, 100, 110, 100, 110, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()
			p := resourceCapacityTestPolicy()
			p.Verified = test.verified
			db := newWithDB(mock)
			db.resourceCapacity = &p
			mock.ExpectExec("SET LOCAL").WillReturnResult(pgxmock.NewResult("SET", 0))
			mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery("WITH").WithArgs(pgxmock.AnyArg(), p.OrdinaryRuleMonthlyCU, p.InteractionRuleMonthlyCU, p.MarketProjectMonthlyCU, "owner", json.RawMessage(nil), p.MarketProjectCostMaxPools).
				WillReturnRows(pgxmock.NewRows([]string{"cu", "users", "unknown", "owner_cu"}).AddRow(strconv.FormatInt(test.globalBefore, 10), int64(1), false, strconv.FormatInt(test.before, 10)))
			ctx, cancel, err := db.beginResourceCapacityCheck(context.Background(), mock, "owner")
			if err != nil {
				t.Fatal(err)
			}
			defer cancel()
			mock.ExpectQuery("WITH").WithArgs(pgxmock.AnyArg(), p.OrdinaryRuleMonthlyCU, p.InteractionRuleMonthlyCU, p.MarketProjectMonthlyCU, "owner", json.RawMessage(nil), p.MarketProjectCostMaxPools).
				WillReturnRows(pgxmock.NewRows([]string{"cu", "users", "unknown", "owner_cu"}).AddRow(strconv.FormatInt(test.globalAfter, 10), int64(1), false, strconv.FormatInt(test.after, 10)))
			if err = db.checkResourceCapacity(ctx, mock); !errors.Is(err, test.want) {
				t.Fatalf("check=%v want%v", err, test.want)
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResourceCapacityOrderAlwaysNeedsVerifiedBudgetAndGlobalLock(t *testing.T) {
	p := resourceCapacityTestPolicy()
	p.Verified = false
	db := newWithDB(nil)
	db.resourceCapacity = &p
	ctx := context.WithValue(context.Background(), resourceCapacityLockKey{}, resourceCapacityAdmission{policy: &p, beforeMonthlyCU: 240})
	if err := db.reserveResourceCapacityOrder(ctx, nil, Order{ID: 1, DeBoxUserID: "owner", PlanCode: "professional", Status: "pending"}); !errors.Is(err, ErrResourceCapacityUnavailable) {
		t.Fatalf("net-neutral sale bypassed verified requirement: %v", err)
	}
	if err := db.checkResourceCapacity(context.Background(), nil); !errors.Is(err, ErrResourceCapacityState) {
		t.Fatalf("candidate checked outside global lock: %v", err)
	}
	if err := db.checkResourceCapacity(ctx, nil); !errors.Is(err, ErrResourceCapacityState) {
		t.Fatalf("owner-free lock-only context admitted configuration: %v", err)
	}
}

func TestResourceCapacityDatabaseFailureKeepsStateAndUnderlyingClassification(t *testing.T) {
	for _, at := range []string{"budget_statement", "aggregate"} {
		t.Run(at, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()
			p := resourceCapacityTestPolicy()
			db := newWithDB(mock)
			db.resourceCapacity = &p
			cause := errors.New("injected capacity database failure")
			if at == "budget_statement" {
				mock.ExpectExec("SET LOCAL").WillReturnError(cause)
				_, cancel, beginErr := db.beginResourceCapacityCheck(context.Background(), mock)
				defer cancel()
				err = beginErr
			} else {
				mock.ExpectQuery("WITH").WithArgs(pgxmock.AnyArg(), p.OrdinaryRuleMonthlyCU, p.InteractionRuleMonthlyCU, p.MarketProjectMonthlyCU, "", json.RawMessage(nil), p.MarketProjectCostMaxPools).WillReturnError(cause)
				_, err = db.ReadResourceCapacityStatus(context.Background())
			}
			if !errors.Is(err, ErrResourceCapacityState) || !errors.Is(err, cause) || errors.Is(err, ErrResourceCapacityReached) || errors.Is(err, ErrResourceCapacityUnavailable) {
				t.Fatalf("DB failure misclassified: %v", err)
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
