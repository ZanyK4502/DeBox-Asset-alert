package config

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

var resourceCapacityEnvNames = []string{
	"RESOURCE_CAPACITY_VERIFIED", "RESOURCE_ACCOUNT_MONTHLY_CU", "RESOURCE_OTHER_MONTHLY_CU",
	"RESOURCE_BACKGROUND_MONTHLY_CU", "RESOURCE_SAFETY_MONTHLY_CU", "RESOURCE_ORDINARY_RULE_MONTHLY_CU",
	"RESOURCE_INTERACTION_RULE_MONTHLY_CU", "RESOURCE_MARKET_PROJECT_MONTHLY_CU",
	"RESOURCE_MARKET_PROJECT_COST_MAX_POOLS", "RESOURCE_MARKET_PROJECT_COST_CHAIN_KEYS",
	"RESOURCE_MARKET_CHAIN_MONTHLY_CU",
	"RESOURCE_STANDARD_SALES_SEAT_LIMIT", "RESOURCE_PROFESSIONAL_SALES_SEAT_LIMIT",
}

func TestResourceCapacityChainPricesReplaceFlatPrice(t *testing.T) {
	verifiedResourceCapacityEnvironment(t)
	t.Setenv("RESOURCE_MARKET_PROJECT_MONTHLY_CU", "")
	t.Setenv("RESOURCE_MARKET_PROJECT_COST_CHAIN_KEYS", "")
	t.Setenv("RESOURCE_MARKET_CHAIN_MONTHLY_CU", `{"bsc":{"deployment":30,"pool":15}}`)
	settings, err := loadResourceCapacitySettings()
	if err != nil || !settings.Verified {
		t.Fatalf("chain pricing requires obsolete flat settings: %+v %v", settings, err)
	}
	prices, err := settings.MarketChainCosts()
	if err != nil || len(prices) != 1 || prices["bsc"].Deployment != 30 || prices["bsc"].Pool != 15 {
		t.Fatalf("prices=%v, %v", prices, err)
	}
	for _, invalid := range []string{`{}`, `{"bnb":{"deployment":30,"pool":15}}`, `{"bsc":{"deployment":30}}`, `{"bsc":{"deployment":30,"pool":-1}}`} {
		t.Setenv("RESOURCE_MARKET_CHAIN_MONTHLY_CU", invalid)
		if _, err = loadResourceCapacitySettings(); err == nil || !strings.Contains(err.Error(), "RESOURCE_MARKET_CHAIN_MONTHLY_CU") {
			t.Fatalf("accepted invalid chain prices %s: %v", invalid, err)
		}
	}
}

func clearResourceCapacityEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range resourceCapacityEnvNames {
		t.Setenv(name, "")
	}
}

func verifiedResourceCapacityEnvironment(t *testing.T) {
	t.Helper()
	clearResourceCapacityEnvironment(t)
	t.Setenv("RESOURCE_CAPACITY_VERIFIED", "true")
	t.Setenv("RESOURCE_ACCOUNT_MONTHLY_CU", "100000000")
	t.Setenv("RESOURCE_OTHER_MONTHLY_CU", "10000000")
	t.Setenv("RESOURCE_BACKGROUND_MONTHLY_CU", "5000000")
	t.Setenv("RESOURCE_SAFETY_MONTHLY_CU", "5000000")
	t.Setenv("RESOURCE_MARKET_PROJECT_MONTHLY_CU", "4000000")
	t.Setenv("RESOURCE_MARKET_PROJECT_COST_MAX_POOLS", "5")
	t.Setenv("RESOURCE_MARKET_PROJECT_COST_CHAIN_KEYS", "bsc")
}

func TestResourceCapacityDefaultsStayUnverified(t *testing.T) {
	clearResourceCapacityEnvironment(t)
	loaded, err := loadResourceCapacitySettings()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Verified || loaded.AccountMonthlyCU != 450_000_000 || loaded.MarketProjectMonthlyCU != loaded.AccountMonthlyCU || loaded.OrdinaryRuleMonthlyCU != 1_437_408 || loaded.InteractionRuleMonthlyCU != 6_696_000 || loaded.OtherMonthlyCU != 150_000_000 || loaded.BackgroundMonthlyCU != 0 || loaded.SafetyMonthlyCU != 60_000_000 || loaded.MarketProjectCostMaxPools != 0 || loaded.MarketProjectCostChainKeys != "" {
		t.Fatalf("unexpected default policy: %+v", loaded)
	}
	direct, err := (Config{}).EffectiveResourceCapacitySettings()
	if err != nil || direct != loaded {
		t.Fatalf("omitted direct config bypassed conservative defaults: %+v, %v", direct, err)
	}
	if net := loaded.AccountMonthlyCU - loaded.OtherMonthlyCU - loaded.SafetyMonthlyCU; net != 240_000_000 {
		t.Fatalf("agreed full-month budget before unknown background=%d", net)
	}
	t.Setenv("RESOURCE_ACCOUNT_MONTHLY_CU", "25000000")
	t.Setenv("RESOURCE_OTHER_MONTHLY_CU", "0")
	t.Setenv("RESOURCE_SAFETY_MONTHLY_CU", "0")
	loaded, err = loadResourceCapacitySettings()
	if err != nil || loaded.Verified || loaded.MarketProjectMonthlyCU != 25_000_000 {
		t.Fatalf("unverified project placeholder must follow account: %+v, %v", loaded, err)
	}
}

func TestSalesSeatLimitsDefaultIndependentlyAndValidateOverrides(t *testing.T) {
	clearResourceCapacityEnvironment(t)
	t.Setenv("RESOURCE_STANDARD_SALES_SEAT_LIMIT", "4")
	settings, err := loadResourceCapacitySettings()
	if err != nil || settings.StandardSalesSeatLimit != 4 || settings.ProfessionalSalesSeatLimit != 2 {
		t.Fatalf("seat settings: %+v, %v", settings, err)
	}
	settings.StandardSalesSeatLimit = 0
	got, err := (Config{ResourceCapacity: settings}).EffectiveResourceCapacitySettings()
	if err != nil || got.StandardSalesSeatLimit != 3 || got.ProfessionalSalesSeatLimit != 2 {
		t.Fatalf("omitted programmatic seat settings: %+v, %v", got, err)
	}
	for _, name := range []string{"RESOURCE_STANDARD_SALES_SEAT_LIMIT", "RESOURCE_PROFESSIONAL_SALES_SEAT_LIMIT"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "0")
			if _, err := loadResourceCapacitySettings(); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("unsafe zero seat override: %v", err)
			}
		})
	}
}

func TestResourceCapacityVerifiedRequiresExplicitAllocations(t *testing.T) {
	for _, name := range []string{"RESOURCE_OTHER_MONTHLY_CU", "RESOURCE_BACKGROUND_MONTHLY_CU", "RESOURCE_SAFETY_MONTHLY_CU", "RESOURCE_MARKET_PROJECT_MONTHLY_CU", "RESOURCE_MARKET_PROJECT_COST_MAX_POOLS", "RESOURCE_MARKET_PROJECT_COST_CHAIN_KEYS"} {
		t.Run(name, func(t *testing.T) {
			verifiedResourceCapacityEnvironment(t)
			t.Setenv(name, " ")
			if _, err := loadResourceCapacitySettings(); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("missing allocation became approved zero: %v", err)
			}
		})
	}
	verifiedResourceCapacityEnvironment(t)
	t.Setenv("RESOURCE_OTHER_MONTHLY_CU", "0")
	t.Setenv("RESOURCE_BACKGROUND_MONTHLY_CU", "0")
	settings, err := loadResourceCapacitySettings()
	if err != nil || !settings.Verified || settings.OtherMonthlyCU != 0 || settings.BackgroundMonthlyCU != 0 {
		t.Fatalf("explicit measured zero allocations rejected: %+v, %v", settings, err)
	}
	for _, name := range []string{"RESOURCE_SAFETY_MONTHLY_CU", "RESOURCE_MARKET_PROJECT_MONTHLY_CU", "RESOURCE_MARKET_PROJECT_COST_MAX_POOLS"} {
		t.Run(name+"/zero", func(t *testing.T) {
			verifiedResourceCapacityEnvironment(t)
			t.Setenv(name, "0")
			if _, err := loadResourceCapacitySettings(); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("unsafe verified zero accepted: %v", err)
			}
		})
	}
}

func TestResourceCapacityReservationsMayExhaustButNeverExceedBudget(t *testing.T) {
	verifiedResourceCapacityEnvironment(t)
	t.Setenv("RESOURCE_OTHER_MONTHLY_CU", "90000000")
	settings, err := loadResourceCapacitySettings()
	if err != nil || settings.OtherMonthlyCU+settings.BackgroundMonthlyCU+settings.SafetyMonthlyCU != settings.AccountMonthlyCU {
		t.Fatalf("zero available capacity must allow startup: %+v, %v", settings, err)
	}
	t.Setenv("RESOURCE_OTHER_MONTHLY_CU", "90000001")
	if _, err := loadResourceCapacitySettings(); err == nil {
		t.Fatal("overcommitted reservations accepted")
	}
	t.Setenv("RESOURCE_ACCOUNT_MONTHLY_CU", strconv.FormatInt(math.MaxInt64, 10))
	t.Setenv("RESOURCE_OTHER_MONTHLY_CU", strconv.FormatInt(math.MaxInt64, 10))
	if _, err := loadResourceCapacitySettings(); err == nil {
		t.Fatal("overflowed reservation sum became available capacity")
	}
}

func TestResourceCapacityRejectsInvalidAndUnderestimatedCosts(t *testing.T) {
	for _, entry := range []struct{ name, value string }{
		{"RESOURCE_CAPACITY_VERIFIED", "perhaps"}, {"RESOURCE_ACCOUNT_MONTHLY_CU", "0"},
		{"RESOURCE_ACCOUNT_MONTHLY_CU", "-1"}, {"RESOURCE_ACCOUNT_MONTHLY_CU", "1.5"},
		{"RESOURCE_ACCOUNT_MONTHLY_CU", "999999999999999999999999"}, {"RESOURCE_OTHER_MONTHLY_CU", "-1"},
		{"RESOURCE_BACKGROUND_MONTHLY_CU", "-1"}, {"RESOURCE_SAFETY_MONTHLY_CU", "-1"},
		{"RESOURCE_ORDINARY_RULE_MONTHLY_CU", "1437407"}, {"RESOURCE_INTERACTION_RULE_MONTHLY_CU", "6695999"},
		{"RESOURCE_MARKET_PROJECT_MONTHLY_CU", "-1"},
		{"RESOURCE_MARKET_PROJECT_COST_MAX_POOLS", "-1"}, {"RESOURCE_MARKET_PROJECT_COST_MAX_POOLS", "1.5"},
		{"RESOURCE_MARKET_PROJECT_COST_CHAIN_KEYS", "solana"}, {"RESOURCE_MARKET_PROJECT_COST_CHAIN_KEYS", "bsc,"},
	} {
		t.Run(entry.name+"/"+entry.value, func(t *testing.T) {
			clearResourceCapacityEnvironment(t)
			t.Setenv(entry.name, entry.value)
			if _, err := loadResourceCapacitySettings(); err == nil || !strings.Contains(err.Error(), entry.name) {
				t.Fatalf("invalid input accepted or unlabelled: %v", err)
			}
		})
	}
	clearResourceCapacityEnvironment(t)
	t.Setenv("RESOURCE_ORDINARY_RULE_MONTHLY_CU", "7000000")
	if _, err := loadResourceCapacitySettings(); err == nil {
		t.Fatal("interaction cost below ordinary cost accepted")
	}
	if _, err := (Config{ResourceCapacity: ResourceCapacitySettings{AccountMonthlyCU: 10}}).EffectiveResourceCapacitySettings(); err == nil {
		t.Fatal("partial programmatic config silently filled missing unit costs")
	}
}

func TestLoadWiresResourceCapacitySettings(t *testing.T) {
	verifiedResourceCapacityEnvironment(t)
	t.Setenv("STATIC_DIR", testStaticDir(t))
	t.Setenv("MARKET_COLLECTOR_ENABLED", "false")
	t.Setenv("MARKET_RULE_ENGINE_ENABLED", "false")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	settings, err := cfg.EffectiveResourceCapacitySettings()
	if err != nil || !settings.Verified || settings.OtherMonthlyCU != 10_000_000 || settings.MarketProjectMonthlyCU != 4_000_000 {
		t.Fatalf("resource env not wired through Config: %+v, %v", settings, err)
	}
	cfg.ResourceCapacity.SafetyMonthlyCU = 0
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "RESOURCE_SAFETY_MONTHLY_CU") {
		t.Fatalf("programmatic config bypassed validation: %v", err)
	}
}

func TestResourceCapacityCostScopeIsExplicitAndNormalizesChains(t *testing.T) {
	clearResourceCapacityEnvironment(t)
	t.Setenv("RESOURCE_MARKET_PROJECT_COST_MAX_POOLS", "5")
	t.Setenv("RESOURCE_MARKET_PROJECT_COST_CHAIN_KEYS", " BNB,base,bsc ")
	settings, err := loadResourceCapacitySettings()
	if err != nil || settings.Verified {
		t.Fatalf("cost scope alone verified the model: %+v %v", settings, err)
	}
	keys, err := settings.MarketProjectCostChains()
	if err != nil || len(keys) != 2 || keys[0] != "bsc" || keys[1] != "base" {
		t.Fatalf("cost chains=%v %v", keys, err)
	}
	keys[0] = "ethereum"
	again, err := settings.MarketProjectCostChains()
	if err != nil || again[0] != "bsc" {
		t.Fatal("cost chain result leaked backing state", again, err)
	}
	verifiedResourceCapacityEnvironment(t)
	settings, err = loadResourceCapacitySettings()
	if err != nil {
		t.Fatal(err)
	}
	settings.MarketProjectCostMaxPools = 0
	if _, err := (Config{ResourceCapacity: settings}).EffectiveResourceCapacitySettings(); err == nil {
		t.Fatal("direct verified configuration inferred a missing pool-cost scope")
	}
}
