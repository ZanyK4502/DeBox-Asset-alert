package app

import (
	"fmt"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/config"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func marketScopePolicies(catalog *plans.Catalog) map[string]store.QuotaPolicy {
	policies := make(map[string]store.QuotaPolicy, 3)
	for _, plan := range catalog.PublicPlans() {
		policies[plan.Code] = store.QuotaPolicy{PlanCode: plan.Code, MarketProjectLimit: plan.MarketProjectLimit, MarketPoolLimit: plan.MarketPoolLimit, AllowedMarketChainKeys: append([]string{}, plan.AllowedMarketChainKeys...)}
	}
	return policies
}

// Infrastructure may retain configuration for historical/query-only chains.
// Only the current product scope starts collectors or accepts their callbacks.
func marketCollectorChainKeys(cfg config.Config, monitoring []string) ([]string, error) {
	allowed := make(map[string]bool, len(monitoring))
	for _, key := range monitoring {
		profile, err := chain.ChainProfile(key, "")
		if err != nil {
			return nil, err
		}
		allowed[profile.Key] = true
	}
	result := make([]string, 0, len(monitoring))
	seen := make(map[string]bool, len(monitoring))
	for _, key := range cfg.MarketChainKeys {
		profile, err := chain.ChainProfile(key, "")
		if err != nil {
			return nil, err
		}
		if allowed[profile.Key] && !seen[profile.Key] {
			result = append(result, profile.Key)
			seen[profile.Key] = true
		}
	}
	if cfg.MarketCollectorEnabled && len(result) == 0 {
		return nil, fmt.Errorf("MARKET_CHAIN_KEYS has no overlap with MARKET_MONITORING_CHAIN_KEYS")
	}
	return result, nil
}
