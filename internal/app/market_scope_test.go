package app

import (
	"reflect"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/config"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
)

func TestMarketScopeAppUsesOneCatalogForAdmissionAndCollectorIntersection(t *testing.T) {
	cfg := config.Config{MarketCollectorEnabled: true, MarketChainKeys: []string{"ethereum", "bsc", "base", "bsc"}}
	keys, err := cfg.EffectiveMarketMonitoringChainKeys()
	if err != nil {
		t.Fatal(err)
	}
	collectors, err := marketCollectorChainKeys(cfg, keys)
	if err != nil || !reflect.DeepEqual(collectors, []string{"ethereum", "bsc", "base"}) {
		t.Fatal(collectors, err)
	}
	catalog, err := plans.NewCatalogWithMarketScope("10", 30, "USDT", plans.QuotaLimits{}, keys)
	if err != nil {
		t.Fatal(err)
	}
	policies := marketScopePolicies(catalog)
	for _, plan := range catalog.PublicPlans() {
		p := policies[plan.Code]
		if p.MarketPoolLimit != plan.MarketPoolLimit || p.MarketProjectLimit != plan.MarketProjectLimit || !reflect.DeepEqual(p.AllowedMarketChainKeys, plan.AllowedMarketChainKeys) {
			t.Fatalf("catalog scope diverged %s: %+v vs %+v", plan.Code, p, plan)
		}
	}
	if policies[plans.Standard].MarketProjectLimit != 1 || policies[plans.Standard].MarketPoolLimit != 1 ||
		policies[plans.Professional].MarketProjectLimit != 5 || policies[plans.Professional].MarketPoolLimit != 5 {
		t.Fatal(policies)
	}
	capacity, err := resourceCapacityPolicy(cfg, catalog)
	if err != nil || capacity.Verified || capacity.Plans[plans.Professional].MarketProjectLimit != 5 {
		t.Fatalf("market expansion lost resource reservation scope or opened unverified capacity: %+v %v", capacity, err)
	}
	// An explicit subset remains configurable independently of the six-chain default.
	cfg.MarketChainKeys = []string{"bsc", "base"}
	collectors, err = marketCollectorChainKeys(cfg, []string{"base", "bsc"})
	if err != nil || !reflect.DeepEqual(collectors, []string{"bsc", "base"}) {
		t.Fatal(collectors, err)
	}
	cfg.MarketChainKeys = []string{"ethereum"}
	if _, err = marketCollectorChainKeys(cfg, []string{"bsc"}); err == nil {
		t.Fatal("enabled collector silently runs no allowed monitoring chain")
	}
	cfg.MarketCollectorEnabled = false
	if _, err = marketCollectorChainKeys(cfg, []string{"bsc"}); err != nil {
		t.Fatalf("disabled collector was blocked: %v", err)
	}
}
