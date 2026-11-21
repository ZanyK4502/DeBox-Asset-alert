package store

import (
	"context"
	"errors"
	"testing"
)

func marketScopeTestPolicies() map[string]QuotaPolicy {
	return map[string]QuotaPolicy{
		"free":         {PlanCode: "free", AllowedMarketChainKeys: []string{}},
		"standard":     {PlanCode: "standard", MarketProjectLimit: 1, MarketPoolLimit: 1, AllowedMarketChainKeys: []string{"bsc"}},
		"professional": {PlanCode: "professional", MarketProjectLimit: 1, MarketPoolLimit: 2, AllowedMarketChainKeys: []string{"bsc"}},
	}
}

func TestMarketScopePoliciesCopyAndOverrideOnlyScope(t *testing.T) {
	policies := marketScopeTestPolicies()
	settings := openSettings{}
	WithMarketScopePolicies(policies)(&settings)
	if err := validateMarketScopePolicies(settings.marketScopePolicies); err != nil {
		t.Fatal(err)
	}
	policies["professional"].AllowedMarketChainKeys[0] = "ethereum"
	delete(policies, "standard")
	db := &Store{marketScopePolicies: settings.marketScopePolicies}
	input := QuotaPolicy{PlanCode: "professional", RuleLimit: 10, InteractionRuleLimit: 1, MarketProjectLimit: 99, MarketPoolLimit: 99, AllowedMarketChainKeys: []string{"ethereum"}, MultiPoolMonitoring: true, CombinationRules: true}
	got, err := db.marketScopePolicy(context.Background(), nil, "local", input)
	if err != nil || got.MarketProjectLimit != 1 || got.MarketPoolLimit != 2 || len(got.AllowedMarketChainKeys) != 1 || got.AllowedMarketChainKeys[0] != "bsc" {
		t.Fatal(got, err)
	}
	if got.RuleLimit != 10 || got.InteractionRuleLimit != 1 || !got.MultiPoolMonitoring || !got.CombinationRules {
		t.Fatal("scope policy altered other rights")
	}
	if settings.marketScopePolicies["free"].AllowedMarketChainKeys == nil {
		t.Fatal("explicit empty free scope became legacy unlimited scope")
	}
	legacy := &Store{}
	if got, err = legacy.marketScopePolicy(context.Background(), nil, "local", input); err != nil || got.MarketPoolLimit != 99 {
		t.Fatal("absent option changed legacy adapter", err)
	}
}

func TestMarketScopeChainRequiresAllowedIdentityAndKeepsLegacyZeroPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		id        int64
		p         QuotaPolicy
		want      error
	}{
		{"bsc", "bsc", 56, marketScopeTestPolicies()["professional"], nil},
		{"bsc_alias", "bnb_chain", 56, marketScopeTestPolicies()["professional"], nil},
		{"wrong_id", "bsc", 1, marketScopeTestPolicies()["professional"], ErrMarketChainNotAllowed},
		{"other_chain", "base", 8453, marketScopeTestPolicies()["professional"], ErrMarketChainNotAllowed},
		{"explicit_free", "bsc", 56, marketScopeTestPolicies()["free"], ErrMarketMonitoringDenied},
		{"legacy_zero", "base", 8453, QuotaPolicy{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateMarketScopeChain(tc.p, tc.key, tc.id); !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
		})
	}
	for _, mutate := range []func(map[string]QuotaPolicy){
		func(p map[string]QuotaPolicy) { delete(p, "standard") },
		func(p map[string]QuotaPolicy) {
			q := p["professional"]
			q.AllowedMarketChainKeys = nil
			p["professional"] = q
		},
		func(p map[string]QuotaPolicy) { q := p["standard"]; q.MarketPoolLimit = 0; p["standard"] = q },
		func(p map[string]QuotaPolicy) {
			q := p["standard"]
			q.AllowedMarketChainKeys = []string{""}
			p["standard"] = q
		},
	} {
		p := marketScopeTestPolicies()
		mutate(p)
		if err := validateMarketScopePolicies(p); err == nil {
			t.Fatal("invalid explicit scope accepted")
		}
	}
}
