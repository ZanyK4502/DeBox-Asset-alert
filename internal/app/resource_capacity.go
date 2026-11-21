package app

import (
	"fmt"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/config"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

// The same catalog instance drives both user-visible entitlements and the full
// per-plan capacity commitment, including each logical project's possible
// chain/token deployments. No copied quota defaults can underprice a plan.
func resourceCapacityPolicy(cfg config.Config, catalog *plans.Catalog) (store.ResourceCapacityPolicy, error) {
	settings, err := cfg.EffectiveResourceCapacitySettings()
	if err != nil {
		return store.ResourceCapacityPolicy{}, err
	}
	if catalog == nil {
		return store.ResourceCapacityPolicy{}, fmt.Errorf("resource capacity requires the application plan catalog")
	}
	costChains, err := settings.MarketProjectCostChains()
	if err != nil {
		return store.ResourceCapacityPolicy{}, err
	}
	chainCosts, err := settings.MarketChainCosts()
	if err != nil {
		return store.ResourceCapacityPolicy{}, err
	}
	coveredChains := make(map[string]bool, len(costChains))
	for _, key := range costChains {
		coveredChains[key] = true
	}
	policy := store.ResourceCapacityPolicy{Verified: settings.Verified, AccountMonthlyCU: settings.AccountMonthlyCU,
		OtherMonthlyCU: settings.OtherMonthlyCU, BackgroundMonthlyCU: settings.BackgroundMonthlyCU, SafetyMonthlyCU: settings.SafetyMonthlyCU,
		OrdinaryRuleMonthlyCU: settings.OrdinaryRuleMonthlyCU, InteractionRuleMonthlyCU: settings.InteractionRuleMonthlyCU, MarketProjectMonthlyCU: settings.MarketProjectMonthlyCU,
		MarketProjectCostMaxPools: settings.MarketProjectCostMaxPools, MarketProjectCostChainKeys: costChains, MarketChainMonthlyCU: chainCosts,
		StandardSalesSeatLimit: settings.StandardSalesSeatLimit, ProfessionalSalesSeatLimit: settings.ProfessionalSalesSeatLimit,
		Plans: make(map[string]store.ResourcePlanCapacity, 3)}
	for _, plan := range catalog.PublicPlans() {
		if settings.Verified && plan.MarketProjectLimit > 0 {
			if settings.MarketProjectCostMaxPools < plan.MarketPoolLimit {
				return store.ResourceCapacityPolicy{}, fmt.Errorf("RESOURCE_MARKET_PROJECT_COST_MAX_POOLS does not cover %s pool limit %d", plan.Code, plan.MarketPoolLimit)
			}
			for _, key := range plan.AllowedMarketChainKeys {
				if len(chainCosts) > 0 {
					if _, ok := chainCosts[key]; !ok {
						return store.ResourceCapacityPolicy{}, fmt.Errorf("RESOURCE_MARKET_CHAIN_MONTHLY_CU does not cover %s chain %s", plan.Code, key)
					}
					continue
				}
				if !coveredChains[key] {
					return store.ResourceCapacityPolicy{}, fmt.Errorf("RESOURCE_MARKET_PROJECT_COST_CHAIN_KEYS does not cover %s chain %s", plan.Code, key)
				}
			}
		}
		policy.Plans[plan.Code] = store.ResourcePlanCapacity{RuleLimit: plan.RuleLimit, InteractionRuleLimit: plan.InteractionRuleLimit, MarketProjectLimit: plan.MarketProjectLimit,
			MarketPoolLimit: plan.MarketPoolLimit, AllowedMarketChainKeys: append([]string(nil), plan.AllowedMarketChainKeys...)}
	}
	return policy, nil
}
