package app

import (
	"strings"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/config"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
)

func TestResourceCapacityPolicyUsesActualCatalogQuotas(t *testing.T) {
	limits := plans.DefaultQuotaLimits()
	limits.StandardRuleLimit = 4
	limits.ProfessionalRuleLimit = 14
	limits.ProfessionalInteractionRuleLimit = 2
	limits.ProfessionalMarketProjectLimit = 3
	catalog, err := plans.NewCatalogWithQuotaLimits("10", 30, "USDT", limits)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := resourceCapacityPolicy(config.Config{}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Verified || policy.AccountMonthlyCU != 450_000_000 || policy.OtherMonthlyCU != 150_000_000 || policy.SafetyMonthlyCU != 60_000_000 || policy.MarketProjectMonthlyCU != policy.AccountMonthlyCU || policy.MarketProjectCostMaxPools != 0 || len(policy.MarketProjectCostChainKeys) != 0 {
		t.Fatalf("omitted settings must produce conservative unverified policy: %+v", policy)
	}
	if policy.StandardSalesSeatLimit != 3 || policy.ProfessionalSalesSeatLimit != 2 {
		t.Fatalf("default order seat limits: %+v", policy)
	}
	for _, plan := range catalog.PublicPlans() {
		capacity, ok := policy.Plans[plan.Code]
		if !ok || capacity.RuleLimit != plan.RuleLimit || capacity.InteractionRuleLimit != plan.InteractionRuleLimit || capacity.MarketProjectLimit != plan.MarketProjectLimit || capacity.MarketPoolLimit != plan.MarketPoolLimit {
			t.Fatalf("capacity undercounted actual %s plan: %+v vs %+v", plan.Code, capacity, plan)
		}
	}
	if policy.Plans[plans.Professional].RuleLimit != 14 || policy.Plans[plans.Professional].InteractionRuleLimit != 2 || policy.Plans[plans.Professional].MarketProjectLimit != 3 || len(policy.Plans) != 3 {
		t.Fatalf("independent defaults replaced configured quotas: %+v", policy.Plans)
	}
}

func TestResourceCapacityVerificationMustCoverCurrentMarketPoolAndChainScope(t *testing.T) {
	catalog, err := plans.NewCatalog("10", 30, "USDT")
	if err != nil {
		t.Fatal(err)
	}
	settings := config.ResourceCapacitySettings{Verified: true, AccountMonthlyCU: 450_000_000,
		OtherMonthlyCU: 150_000_000, SafetyMonthlyCU: 60_000_000,
		OrdinaryRuleMonthlyCU: 1_437_408, InteractionRuleMonthlyCU: 6_696_000,
		MarketProjectMonthlyCU: 20_000_000, MarketProjectCostMaxPools: 2, MarketProjectCostChainKeys: "bsc,ethereum,base,polygon,arbitrum,optimism"}
	if _, err := resourceCapacityPolicy(config.Config{ResourceCapacity: settings}, catalog); err == nil || !strings.Contains(err.Error(), "RESOURCE_MARKET_PROJECT_COST_MAX_POOLS") {
		t.Fatalf("old two-pool verification admitted five-pool catalog: %v", err)
	}
	settings.MarketProjectCostMaxPools = 5
	settings.MarketProjectCostChainKeys = "base"
	if _, err := resourceCapacityPolicy(config.Config{ResourceCapacity: settings}, catalog); err == nil || !strings.Contains(err.Error(), "RESOURCE_MARKET_PROJECT_COST_CHAIN_KEYS") {
		t.Fatalf("uncovered chain admitted: %v", err)
	}
	settings.MarketProjectCostChainKeys = "BNB"
	if _, err := resourceCapacityPolicy(config.Config{ResourceCapacity: settings}, catalog); err == nil || !strings.Contains(err.Error(), "RESOURCE_MARKET_PROJECT_COST_CHAIN_KEYS") {
		t.Fatalf("BSC-only cost admitted six-chain catalog: %v", err)
	}
	settings.MarketProjectCostChainKeys = "BNB,ethereum,base,polygon,arbitrum,optimism"
	policy, err := resourceCapacityPolicy(config.Config{ResourceCapacity: settings}, catalog)
	if err != nil || policy.Plans[plans.Professional].MarketProjectLimit != 5 || policy.Plans[plans.Professional].MarketPoolLimit != 5 || len(policy.MarketProjectCostChainKeys) != 6 || policy.MarketProjectCostChainKeys[0] != "bsc" || len(policy.Plans[plans.Professional].AllowedMarketChainKeys) != 6 {
		t.Fatalf("covered synthetic model lost full catalog scope: %+v %v", policy, err)
	}
	// The cost above is test data, not an accepted production project price.
	limits := plans.DefaultQuotaLimits()
	limits.ProfessionalMarketPoolLimit = 6
	expanded, err := plans.NewCatalogWithQuotaLimits("10", 30, "USDT", limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resourceCapacityPolicy(config.Config{ResourceCapacity: settings}, expanded); err == nil {
		t.Fatal("future pool expansion retained an older cost verification")
	}
}

func TestResourceCapacityPolicyPreservesVerifiedBudgetAndRejectsIncompleteModel(t *testing.T) {
	catalog, err := plans.NewCatalog("10", 30, "USDT")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{ResourceCapacity: config.ResourceCapacitySettings{Verified: true, AccountMonthlyCU: 100_000_000,
		OtherMonthlyCU: 0, BackgroundMonthlyCU: 5_000_000, SafetyMonthlyCU: 5_000_000,
		OrdinaryRuleMonthlyCU: 1_437_408, InteractionRuleMonthlyCU: 6_696_000, MarketProjectMonthlyCU: 4_000_000,
		MarketProjectCostMaxPools: 5, MarketProjectCostChainKeys: "bsc,ethereum,base,polygon,arbitrum,optimism",
		StandardSalesSeatLimit: 5, ProfessionalSalesSeatLimit: 4}}
	policy, err := resourceCapacityPolicy(cfg, catalog)
	if err != nil || !policy.Verified || policy.OtherMonthlyCU != 0 || policy.BackgroundMonthlyCU != 5_000_000 || policy.SafetyMonthlyCU != 5_000_000 || policy.MarketProjectMonthlyCU != 4_000_000 {
		t.Fatalf("verified settings were changed: %+v, %v", policy, err)
	}
	if policy.StandardSalesSeatLimit != 5 || policy.ProfessionalSalesSeatLimit != 4 {
		t.Fatalf("configured order seat limits: %+v", policy)
	}
	if _, err := resourceCapacityPolicy(cfg, nil); err == nil {
		t.Fatal("capacity accepted no plan catalog")
	}
	cfg.ResourceCapacity.MarketProjectMonthlyCU = 0
	if _, err := resourceCapacityPolicy(cfg, catalog); err == nil {
		t.Fatal("unknown project cost became saleable")
	}
}
