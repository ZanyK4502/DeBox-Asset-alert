package marketview

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func marketScopeTestPlan(t *testing.T, code string) plans.Plan {
	t.Helper()
	c, e := plans.NewCatalog("10", 30, "USDT")
	if e != nil {
		t.Fatal(e)
	}
	p, e := c.Get(code)
	if e != nil {
		t.Fatal(e)
	}
	return p
}

func TestMarketScopeCreationRejectsBeforeProviderOrRepositoryWork(t *testing.T) {
	tooManyPools := make([]string, 6)
	for i := range tooManyPools {
		tooManyPools[i] = fmt.Sprintf("0x%040x", i+1)
	}
	for _, test := range []struct {
		name, plan string
		input      CreateProjectInput
		want       error
	}{
		{"other_chain_single", plans.Professional, CreateProjectInput{ChainKey: "ethereum", TokenAddress: testToken}, store.ErrMarketChainNotAllowed},
		{"other_chain_nested", plans.Professional, CreateProjectInput{Deployments: []CreateProjectDeploymentInput{{ChainKey: "ethereum", TokenAddress: testToken, PoolAddresses: []string{testPairA}}}}, store.ErrMarketChainNotAllowed},
		{"standard_two", plans.Standard, CreateProjectInput{ChainKey: "bsc", PoolAddresses: []string{testPairA, testPairB}}, store.ErrMarketPoolLimitReached},
		{"professional_six", plans.Professional, CreateProjectInput{Deployments: []CreateProjectDeploymentInput{{ChainKey: "bsc", PoolAddresses: tooManyPools}}}, store.ErrMarketPoolLimitReached},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := marketScopeTestPlan(t, test.plan)
			// An explicitly configured subset must still reject other chains
			// before provider reads, regardless of the six-chain default.
			plan.AllowedMarketChainKeys = []string{"bsc"}
			s := New(Dependencies{Entitlements: fakeEntitlements{plan: plan}})
			if _, err := s.CreateProject(context.Background(), "owner", test.input); !errors.Is(err, test.want) {
				t.Fatalf("creation=%v want %v", err, test.want)
			}
		})
	}
	// Pool address duplicates count once, including an explicit multi-chain subset.
	p := marketScopeTestPlan(t, plans.Professional)
	p.AllowedMarketChainKeys = []string{"bsc", "base"}
	if err := validateCreateProjectMonitoringScope(p, CreateProjectInput{ChainKey: "base", PoolAddresses: []string{testPairA, testPairA, testPairB}}); err != nil {
		t.Fatal(err)
	}
}

func TestMarketScopeCatalogKeepsQueryChainsAndAdvertisesMonitoringSeparately(t *testing.T) {
	s := New(Dependencies{Entitlements: fakeEntitlements{plan: marketScopeTestPlan(t, plans.Professional)}})
	c, err := s.Catalog(context.Background(), "owner")
	if err != nil || !reflect.DeepEqual(c.MonitoringSupportedChains, chain.SupportedChains()) || !reflect.DeepEqual(c.SupportedChains, chain.SupportedChains()) {
		t.Fatalf("catalog=%+v %v", c, err)
	}
}

func TestMarketScopeCreationAcceptsEachDefaultChainWithinTotalPoolQuota(t *testing.T) {
	for _, code := range []string{plans.Standard, plans.Professional} {
		plan := marketScopeTestPlan(t, code)
		for _, profile := range chain.SupportedChains() {
			t.Run(code+"/"+profile.Key, func(t *testing.T) {
				input := CreateProjectInput{ChainKey: profile.Key, TokenAddress: testToken, PoolAddresses: []string{testPairA}}
				if err := validateCreateProjectMonitoringScope(plan, input); err != nil {
					t.Fatalf("default monitoring chain rejected: %v", err)
				}
			})
		}
	}
	plan := marketScopeTestPlan(t, plans.Professional)
	input := CreateProjectInput{}
	for _, profile := range chain.SupportedChains() {
		input.Deployments = append(input.Deployments, CreateProjectDeploymentInput{ChainKey: profile.Key, PoolAddresses: []string{testPairA}})
	}
	if err := validateCreateProjectMonitoringScope(plan, input); !errors.Is(err, store.ErrMarketPoolLimitReached) {
		t.Fatalf("six-chain selection bypassed the project-wide five-pool limit: %v", err)
	}
	input.Deployments = input.Deployments[:plan.MarketPoolLimit]
	if err := validateCreateProjectMonitoringScope(plan, input); err != nil {
		t.Fatalf("five pools on different allowed chains rejected: %v", err)
	}
}

func TestMarketScopeFreeCatalogStillLoadsWithoutEnablingMonitoring(t *testing.T) {
	s := New(Dependencies{Entitlements: fakeEntitlements{plan: marketScopeTestPlan(t, plans.Free)}})
	c, err := s.Catalog(context.Background(), "free-owner")
	if err != nil || len(c.MonitoringSupportedChains) != 0 || c.Plan.MarketProjectLimit != 0 || c.Plan.MarketPoolLimit != 0 {
		t.Fatalf("free catalog unavailable or enabled paid monitoring: %+v %v", c, err)
	}
}
