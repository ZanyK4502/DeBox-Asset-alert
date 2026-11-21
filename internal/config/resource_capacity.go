package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
)

const (
	defaultResourceAccountMonthlyCU   int64 = 450_000_000
	defaultResourceOtherMonthlyCU     int64 = 150_000_000
	defaultResourceSafetyMonthlyCU    int64 = 60_000_000
	defaultStandardSalesSeatLimit           = 3
	defaultProfessionalSalesSeatLimit       = 2
	// Known 31-day baselines, not complete retry/cold-cache upper bounds.
	minimumOrdinaryRuleMonthlyCU    int64 = 1_437_408
	minimumInteractionRuleMonthlyCU int64 = 6_696_000
)

// ResourceCapacitySettings configures sales seats and the optional monthly CU
// admission model. When enforced, that model must be verified to allow sales.
type ResourceCapacitySettings struct {
	SeatOnly                 bool
	Verified                 bool
	AccountMonthlyCU         int64
	OtherMonthlyCU           int64
	BackgroundMonthlyCU      int64
	SafetyMonthlyCU          int64
	OrdinaryRuleMonthlyCU    int64
	InteractionRuleMonthlyCU int64
	MarketProjectMonthlyCU   int64
	// MarketProjectMonthlyCU prices one chain/token collection unit, covering
	// all its selected pools up to this scope and every listed chain. Logical
	// projects may reserve several units. Unknown cost scope stays zero/empty
	// while unverified; it is never inferred as measured.
	MarketProjectCostMaxPools  int
	MarketProjectCostChainKeys string
	MarketChainMonthlyCU       string
	StandardSalesSeatLimit     int
	ProfessionalSalesSeatLimit int
}

func defaultResourceCapacitySettings() ResourceCapacitySettings {
	return ResourceCapacitySettings{SeatOnly: true, AccountMonthlyCU: defaultResourceAccountMonthlyCU,
		OtherMonthlyCU: defaultResourceOtherMonthlyCU, SafetyMonthlyCU: defaultResourceSafetyMonthlyCU,
		OrdinaryRuleMonthlyCU: minimumOrdinaryRuleMonthlyCU, InteractionRuleMonthlyCU: minimumInteractionRuleMonthlyCU,
		MarketProjectMonthlyCU: defaultResourceAccountMonthlyCU,
		StandardSalesSeatLimit: defaultStandardSalesSeatLimit, ProfessionalSalesSeatLimit: defaultProfessionalSalesSeatLimit}
}

func loadResourceCapacitySettings() (ResourceCapacitySettings, error) {
	result := defaultResourceCapacitySettings()
	enforceMonthlyCU, err := parseBoolEnv("RESOURCE_ENFORCE_MONTHLY_CU", false)
	if err != nil {
		return result, err
	}
	result.SeatOnly = !enforceMonthlyCU
	result.StandardSalesSeatLimit, err = parseIntEnv("RESOURCE_STANDARD_SALES_SEAT_LIMIT", defaultStandardSalesSeatLimit)
	if err != nil {
		return result, err
	}
	result.ProfessionalSalesSeatLimit, err = parseIntEnv("RESOURCE_PROFESSIONAL_SALES_SEAT_LIMIT", defaultProfessionalSalesSeatLimit)
	if err != nil {
		return result, err
	}
	if result.SeatOnly {
		return result, result.validate()
	}
	result.Verified, err = parseBoolEnv("RESOURCE_CAPACITY_VERIFIED", false)
	if err != nil {
		return result, err
	}
	result.MarketChainMonthlyCU = strings.TrimSpace(os.Getenv("RESOURCE_MARKET_CHAIN_MONTHLY_CU"))
	if result.Verified {
		// A missing allocation is unknown, whereas an explicitly supplied zero
		// can be valid for other projects or fixed background work. Do not turn
		// an empty environment value into an approved zero allocation.
		required := []string{"RESOURCE_OTHER_MONTHLY_CU", "RESOURCE_BACKGROUND_MONTHLY_CU", "RESOURCE_SAFETY_MONTHLY_CU", "RESOURCE_MARKET_PROJECT_COST_MAX_POOLS"}
		if result.MarketChainMonthlyCU == "" {
			required = append(required, "RESOURCE_MARKET_PROJECT_MONTHLY_CU", "RESOURCE_MARKET_PROJECT_COST_CHAIN_KEYS")
		}
		for _, key := range required {
			if strings.TrimSpace(os.Getenv(key)) == "" {
				return result, fmt.Errorf("%s must be explicitly set when RESOURCE_CAPACITY_VERIFIED=true", key)
			}
		}
	}
	result.AccountMonthlyCU, err = parseInt64Env("RESOURCE_ACCOUNT_MONTHLY_CU", defaultResourceAccountMonthlyCU)
	if err != nil {
		return result, err
	}
	result.OtherMonthlyCU, err = parseInt64Env("RESOURCE_OTHER_MONTHLY_CU", defaultResourceOtherMonthlyCU)
	if err != nil {
		return result, err
	}
	result.BackgroundMonthlyCU, err = parseInt64Env("RESOURCE_BACKGROUND_MONTHLY_CU", 0)
	if err != nil {
		return result, err
	}
	result.SafetyMonthlyCU, err = parseInt64Env("RESOURCE_SAFETY_MONTHLY_CU", defaultResourceSafetyMonthlyCU)
	if err != nil {
		return result, err
	}
	result.OrdinaryRuleMonthlyCU, err = parseInt64Env("RESOURCE_ORDINARY_RULE_MONTHLY_CU", minimumOrdinaryRuleMonthlyCU)
	if err != nil {
		return result, err
	}
	result.InteractionRuleMonthlyCU, err = parseInt64Env("RESOURCE_INTERACTION_RULE_MONTHLY_CU", minimumInteractionRuleMonthlyCU)
	if err != nil {
		return result, err
	}
	// A verified model must supply a measured positive project cost explicitly.
	// When unverified, an entire account is a deliberately conservative placeholder.
	projectDefault := result.AccountMonthlyCU
	if result.Verified && result.MarketChainMonthlyCU == "" {
		projectDefault = 0
	}
	result.MarketProjectMonthlyCU, err = parseInt64Env("RESOURCE_MARKET_PROJECT_MONTHLY_CU", projectDefault)
	if err != nil {
		return result, err
	}
	if !result.Verified && result.MarketProjectMonthlyCU == 0 {
		result.MarketProjectMonthlyCU = result.AccountMonthlyCU
	}
	result.MarketProjectCostMaxPools, err = parseIntEnv("RESOURCE_MARKET_PROJECT_COST_MAX_POOLS", 0)
	if err != nil {
		return result, err
	}
	result.MarketProjectCostChainKeys = strings.TrimSpace(os.Getenv("RESOURCE_MARKET_PROJECT_COST_CHAIN_KEYS"))
	return result, result.validate()
}

func (s ResourceCapacitySettings) MarketProjectCostChains() ([]string, error) {
	if strings.TrimSpace(s.MarketProjectCostChainKeys) == "" {
		return nil, nil
	}
	keys, err := parseMarketMonitoringChainKeys(s.MarketProjectCostChainKeys)
	if err != nil {
		return nil, fmt.Errorf("RESOURCE_MARKET_PROJECT_COST_CHAIN_KEYS: %w", err)
	}
	return keys, nil
}

func (s ResourceCapacitySettings) MarketChainCosts() (map[string]plans.MarketChainCUCost, error) {
	if strings.TrimSpace(s.MarketChainMonthlyCU) == "" {
		return nil, nil
	}
	var costs map[string]plans.MarketChainCUCost
	if err := json.Unmarshal([]byte(s.MarketChainMonthlyCU), &costs); err != nil || len(costs) == 0 {
		return nil, fmt.Errorf("RESOURCE_MARKET_CHAIN_MONTHLY_CU must be a JSON object with a cost for each chain")
	}
	for key, cost := range costs {
		profile, err := chain.ChainProfile(key, "")
		if err != nil || profile.Key != key || cost.Deployment < 1 || cost.Pool < 1 {
			return nil, fmt.Errorf("RESOURCE_MARKET_CHAIN_MONTHLY_CU has an invalid chain or nonpositive cost for %q", key)
		}
	}
	return costs, nil
}

func (s ResourceCapacitySettings) validate() error {
	if s.StandardSalesSeatLimit < 1 {
		return fmt.Errorf("RESOURCE_STANDARD_SALES_SEAT_LIMIT must be positive")
	}
	if s.ProfessionalSalesSeatLimit < 1 {
		return fmt.Errorf("RESOURCE_PROFESSIONAL_SALES_SEAT_LIMIT must be positive")
	}
	if s.SeatOnly {
		return nil
	}
	if s.AccountMonthlyCU <= 0 {
		return fmt.Errorf("RESOURCE_ACCOUNT_MONTHLY_CU must be greater than zero")
	}
	if s.OrdinaryRuleMonthlyCU < minimumOrdinaryRuleMonthlyCU {
		return fmt.Errorf("RESOURCE_ORDINARY_RULE_MONTHLY_CU must not be below the known per-rule baseline %d", minimumOrdinaryRuleMonthlyCU)
	}
	if s.InteractionRuleMonthlyCU < minimumInteractionRuleMonthlyCU || s.InteractionRuleMonthlyCU < s.OrdinaryRuleMonthlyCU {
		return fmt.Errorf("RESOURCE_INTERACTION_RULE_MONTHLY_CU must be at least %d and not below the ordinary-rule cost", minimumInteractionRuleMonthlyCU)
	}
	chainCosts, err := s.MarketChainCosts()
	if err != nil {
		return err
	}
	if s.MarketProjectMonthlyCU < 0 || len(chainCosts) == 0 && s.MarketProjectMonthlyCU == 0 {
		return fmt.Errorf("RESOURCE_MARKET_PROJECT_MONTHLY_CU must be explicitly positive when RESOURCE_CAPACITY_VERIFIED=true")
	}
	if s.MarketProjectCostMaxPools < 0 || (s.Verified || len(chainCosts) > 0) && s.MarketProjectCostMaxPools == 0 {
		return fmt.Errorf("RESOURCE_MARKET_PROJECT_COST_MAX_POOLS must be positive for a verified model and must not be negative")
	}
	costChains, err := s.MarketProjectCostChains()
	if err != nil {
		return err
	}
	if s.Verified && len(chainCosts) == 0 && len(costChains) == 0 {
		return fmt.Errorf("RESOURCE_MARKET_PROJECT_COST_CHAIN_KEYS must explicitly cover a verified model")
	}
	remaining := s.AccountMonthlyCU
	for _, reserve := range []struct {
		name  string
		value int64
	}{
		{"RESOURCE_OTHER_MONTHLY_CU", s.OtherMonthlyCU}, {"RESOURCE_BACKGROUND_MONTHLY_CU", s.BackgroundMonthlyCU}, {"RESOURCE_SAFETY_MONTHLY_CU", s.SafetyMonthlyCU},
	} {
		if reserve.value < 0 {
			return fmt.Errorf("%s must not be negative", reserve.name)
		}
		if s.Verified && reserve.name == "RESOURCE_SAFETY_MONTHLY_CU" && reserve.value == 0 {
			return fmt.Errorf("%s must be explicitly positive when RESOURCE_CAPACITY_VERIFIED=true", reserve.name)
		}
		// Subtract instead of summing: MaxInt64 inputs cannot wrap to free capacity.
		if reserve.value > remaining {
			return fmt.Errorf("RESOURCE_OTHER_MONTHLY_CU, RESOURCE_BACKGROUND_MONTHLY_CU and RESOURCE_SAFETY_MONTHLY_CU must not exceed the account budget together")
		}
		remaining -= reserve.value
	}
	return nil
}

// EffectiveResourceCapacitySettings also protects programmatic Config callers:
// an omitted model becomes an unverified default, never a nil-policy bypass.
func (c Config) EffectiveResourceCapacitySettings() (ResourceCapacitySettings, error) {
	result := c.ResourceCapacity
	if result == (ResourceCapacitySettings{}) {
		result = defaultResourceCapacitySettings()
	}
	if !result.Verified && result.MarketProjectMonthlyCU == 0 {
		result.MarketProjectMonthlyCU = result.AccountMonthlyCU
	}
	if result.StandardSalesSeatLimit == 0 {
		result.StandardSalesSeatLimit = defaultStandardSalesSeatLimit
	}
	if result.ProfessionalSalesSeatLimit == 0 {
		result.ProfessionalSalesSeatLimit = defaultProfessionalSalesSeatLimit
	}
	return result, result.validate()
}
