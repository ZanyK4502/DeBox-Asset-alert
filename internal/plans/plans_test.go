package plans

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestBalanceThresholdCodesRemainBackwardCompatible(t *testing.T) {
	t.Parallel()

	if BalanceThreshold != "balance_threshold" {
		t.Fatalf("legacy balance threshold code = %q", BalanceThreshold)
	}
	if LowBalanceThreshold != BalanceThreshold {
		t.Fatalf(
			"low balance threshold code = %q, want legacy code %q",
			LowBalanceThreshold,
			BalanceThreshold,
		)
	}
	if HighBalanceThreshold != "balance_threshold_high" {
		t.Fatalf("high balance threshold code = %q", HighBalanceThreshold)
	}
}

func TestCatalogMatchesProductPlans(t *testing.T) {
	t.Parallel()

	catalog, err := NewCatalog("10", 30, "USDT")
	if err != nil {
		t.Fatalf("NewCatalog(): %v", err)
	}

	free, err := catalog.Get("free")
	if err != nil {
		t.Fatalf("Get(free): %v", err)
	}
	if free.WalletLimit != 1 || free.RuleLimit != 1 || free.GroupLimit != 0 || free.InteractionRuleLimit != 0 {
		t.Fatalf("free limits = %d/%d/%d", free.WalletLimit, free.RuleLimit, free.GroupLimit)
	}
	if free.DailyAlertLimit == nil || *free.DailyAlertLimit != 5 {
		t.Fatalf("free daily alert limit = %v, want 5", free.DailyAlertLimit)
	}
	if free.DailySummary || free.GroupNotification ||
		!free.AllowsRuleType(BalanceThreshold) ||
		!free.AllowsRuleType(HighBalanceThreshold) {
		t.Fatal("free plan capabilities do not match the product contract")
	}
	if free.MarketQuery || free.MarketProjectLimit != 0 ||
		free.MarketPoolLimit != 0 || free.AllowedMarketChainKeys == nil || len(free.AllowedMarketChainKeys) != 0 ||
		len(free.AllowedMarketRules) != 0 ||
		free.MarketPoolMode != "none" ||
		free.FourMemeMode != "none" ||
		free.MarketCombination {
		t.Fatal("free market capabilities do not match the product contract")
	}

	standard, err := catalog.Get(" STANDARD ")
	if err != nil {
		t.Fatalf("Get(standard): %v", err)
	}
	if standard.Price != "10" || standard.Days != 30 {
		t.Fatalf("standard price/days = %s/%d", standard.Price, standard.Days)
	}
	if standard.WalletLimit != 3 || standard.RuleLimit != 3 || standard.GroupLimit != 0 || standard.InteractionRuleLimit != 0 {
		t.Fatalf("standard limits = %+v", standard)
	}
	assertBillingOption(t, standard, Monthly, "10", 30)
	assertBillingOption(t, standard, Quarterly, "14", 90)
	assertBillingOption(t, standard, Annual, "50", 365)
	if !standard.AllowsRuleType(ApprovalChange) ||
		!standard.AllowsRuleType(HighBalanceThreshold) ||
		standard.AllowsRuleType(AddressInteraction) {
		t.Fatal("standard rule capabilities do not match the product contract")
	}
	if !standard.AllowsSummaryTarget("private") || standard.AllowsSummaryTarget("group") {
		t.Fatal("standard summary targets do not match the product contract")
	}
	if free.AllowsStageNotifications() || !standard.AllowsStageNotifications() {
		t.Fatal("stage notification capabilities do not match the product contract")
	}
	if standard.MarketProjectLimit != 1 ||
		standard.MarketPoolLimit != 1 || !reflect.DeepEqual(standard.AllowedMarketChainKeys, []string{"bsc", "ethereum", "base", "polygon", "arbitrum", "optimism"}) ||
		!standard.AllowsMarketRuleType(MarketPriceAbove) ||
		standard.AllowsMarketRuleType(MarketLargeBuy) ||
		standard.MarketPoolMode != "main" ||
		standard.FourMemeMode != "market" ||
		standard.MarketCombination {
		t.Fatal("standard market capabilities do not match the product contract")
	}

	professional, err := catalog.Get("professional")
	if err != nil {
		t.Fatalf("Get(professional): %v", err)
	}
	if professional.WalletLimit != 20 || professional.RuleLimit != 10 || professional.GroupLimit != 5 || professional.InteractionRuleLimit != 1 {
		t.Fatalf("professional limits = %d/%d/%d", professional.WalletLimit, professional.RuleLimit, professional.GroupLimit)
	}
	if !professional.GroupNotification || !professional.AllowsRuleType(AddressInteraction) {
		t.Fatal("professional capabilities do not match the product contract")
	}
	if !professional.AllowsStageNotifications() {
		t.Fatal("professional plan must allow stage notifications")
	}
	if professional.MarketProjectLimit != 5 ||
		professional.MarketPoolLimit != 5 || !reflect.DeepEqual(professional.AllowedMarketChainKeys, []string{"bsc", "ethereum", "base", "polygon", "arbitrum", "optimism"}) ||
		!professional.AllowsMarketRuleType(MarketPriceAbove) ||
		!professional.AllowsMarketRuleType(MarketLargeBuy) ||
		!professional.AllowsMarketRuleType(MarketHolderDecrease) ||
		professional.MarketPoolMode != "multiple" ||
		professional.FourMemeMode != "full" ||
		!professional.MarketCombination {
		t.Fatal("professional market capabilities do not match the product contract")
	}
	assertBillingOption(t, professional, Monthly, "15", 30)
	assertBillingOption(t, professional, Quarterly, "42", 90)
	assertBillingOption(t, professional, Annual, "150", 365)
}

func TestCatalogMarketScopeDefaultsNormalizationAndIndependentCopies(t *testing.T) {
	wantDefaults := []string{"bsc", "ethereum", "base", "polygon", "arbitrum", "optimism"}
	defaults := DefaultMarketMonitoringChainKeys()
	if !reflect.DeepEqual(defaults, wantDefaults) {
		t.Fatalf("default scope=%v", defaults)
	}
	defaults[0] = "optimism"
	normalizedDefaults, err := NormalizeMarketMonitoringChainKeys(nil)
	if err != nil || !reflect.DeepEqual(normalizedDefaults, wantDefaults) {
		t.Fatalf("default scope was mutated: %v %v", normalizedDefaults, err)
	}
	limits := DefaultQuotaLimits()
	limits.ProfessionalMarketPoolLimit = 4
	input := []string{"BNB", "base", "bsc"}
	catalog, err := NewCatalogWithMarketScope("5", 30, "USDT", limits, input)
	if err != nil {
		t.Fatal(err)
	}
	input[0] = "ethereum"
	for _, code := range []string{Standard, Professional} {
		plan, _ := catalog.Get(code)
		if len(plan.AllowedMarketChainKeys) != 2 || plan.AllowedMarketChainKeys[0] != "bsc" || plan.AllowedMarketChainKeys[1] != "base" {
			t.Fatalf("%s scope=%v", code, plan.AllowedMarketChainKeys)
		}
		plan.AllowedMarketChainKeys[0] = "optimism"
		again, _ := catalog.Get(code)
		if again.AllowedMarketChainKeys[0] != "bsc" {
			t.Fatal("returned scope mutated catalog")
		}
	}
	pro, _ := catalog.Get(Professional)
	if pro.MarketPoolLimit != 4 {
		t.Fatal(pro.MarketPoolLimit)
	}
	legacy := QuotaLimits{FreeRuleLimit: 1, StandardRuleLimit: 6, ProfessionalRuleLimit: 20, ProfessionalInteractionRuleLimit: 2, ProfessionalMarketProjectLimit: 3}
	normalized, err := NormalizeQuotaLimits(legacy)
	if err != nil || normalized.StandardMarketPoolLimit != 1 || normalized.ProfessionalMarketPoolLimit != 5 || normalized.ProfessionalMarketProjectLimit != 3 || normalized.ProfessionalRuleLimit != 20 {
		t.Fatalf("old explicit quota config lost compatibility: %+v %v", normalized, err)
	}
	legacy.ProfessionalMarketPoolLimit = 2
	normalized, err = NormalizeQuotaLimits(legacy)
	if err != nil || normalized.ProfessionalMarketProjectLimit != 3 || normalized.ProfessionalMarketPoolLimit != 2 {
		t.Fatalf("explicit legacy market capacities were overwritten: %+v %v", normalized, err)
	}
	for _, keys := range [][]string{{}, {""}, {"bsc", " "}, {"solana"}} {
		if _, err := NewCatalogWithMarketScope("5", 30, "USDT", limits, keys); err == nil {
			t.Fatalf("invalid scope accepted: %#v", keys)
		}
	}
	for _, change := range []func(*QuotaLimits){func(q *QuotaLimits) { q.StandardMarketPoolLimit = 2 }, func(q *QuotaLimits) { q.ProfessionalMarketPoolLimit = -1 }} {
		q := DefaultQuotaLimits()
		change(&q)
		if _, err := NormalizeQuotaLimits(q); err == nil {
			t.Fatalf("invalid pool cap accepted: %+v", q)
		}
	}
}

func TestCatalogQuotaLimitsAreConfigurableWithoutChangingCapabilitiesOrBilling(t *testing.T) {
	limits := DefaultQuotaLimits()
	limits.StandardRuleLimit = 6
	limits.ProfessionalRuleLimit = 20
	limits.ProfessionalInteractionRuleLimit = 2
	limits.ProfessionalMarketProjectLimit = 3
	catalog, err := NewCatalogWithQuotaLimits("12.5", 45, "USDT", limits)
	if err != nil {
		t.Fatal(err)
	}
	standard, _ := catalog.Get(Standard)
	pro, _ := catalog.Get(Professional)
	if standard.RuleLimit != 6 || pro.RuleLimit != 20 || pro.InteractionRuleLimit != 2 || pro.MarketProjectLimit != 3 || standard.MarketProjectLimit != 1 {
		t.Fatalf("custom quotas not applied: std=%+v pro=%+v", standard, pro)
	}
	if standard.Price != "12.5" || standard.Days != 45 || standard.WalletLimit != 3 || pro.WalletLimit != 20 || pro.GroupLimit != 5 || !pro.AllowsRuleType(AddressInteraction) || !pro.AllowsMarketRuleType(MarketHolderRankEntered) {
		t.Fatal("capacity configuration changed unrelated contract")
	}
	assertBillingOption(t, pro, Monthly, "15", 30)
	assertBillingOption(t, pro, Quarterly, "42", 90)
	assertBillingOption(t, pro, Annual, "150", 365)
	for _, plan := range catalog.PublicPlans() {
		raw, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		var view map[string]any
		if err = json.Unmarshal(raw, &view); err != nil {
			t.Fatal(err)
		}
		if got, exists := view["interaction_rule_limit"]; !exists || int(got.(float64)) != plan.InteractionRuleLimit {
			t.Fatalf("missing interaction JSON field for %s: %s", plan.Code, raw)
		}
	}
}

func TestCatalogQuotaLimitsRejectPartialOrInconsistentConfiguration(t *testing.T) {
	for _, mutate := range []func(*QuotaLimits){
		func(q *QuotaLimits) { q.FreeRuleLimit = 2 },
		func(q *QuotaLimits) { q.StandardRuleLimit = 0 },
		func(q *QuotaLimits) { q.ProfessionalRuleLimit = -1 },
		func(q *QuotaLimits) { q.ProfessionalInteractionRuleLimit = 0 },
		func(q *QuotaLimits) { q.ProfessionalInteractionRuleLimit = 11 },
		func(q *QuotaLimits) { q.ProfessionalMarketProjectLimit = 0 },
	} {
		q := DefaultQuotaLimits()
		mutate(&q)
		if _, err := NewCatalogWithQuotaLimits("5", 30, "USDT", q); err == nil {
			t.Fatalf("invalid quotas accepted: %+v", q)
		}
	}
	catalog, err := NewCatalogWithQuotaLimits("5", 30, "USDT", QuotaLimits{})
	if err != nil {
		t.Fatal(err)
	}
	pro, _ := catalog.Get(Professional)
	if pro.RuleLimit != 10 || pro.InteractionRuleLimit != 1 || pro.MarketProjectLimit != 5 || pro.MarketPoolLimit != 5 {
		t.Fatal("zero limits did not use defaults")
	}
}

func TestIsBalanceThreshold(t *testing.T) {
	t.Parallel()

	if !IsBalanceThreshold(BalanceThreshold) ||
		!IsBalanceThreshold(HighBalanceThreshold) ||
		IsBalanceThreshold(BalanceChange) {
		t.Fatal("balance threshold classification is incorrect")
	}
}

func TestCatalogDefaultsToStandardAndReturnsCopies(t *testing.T) {
	t.Parallel()

	catalog, err := NewCatalog("12.5", 45, "USDT")
	if err != nil {
		t.Fatalf("NewCatalog(): %v", err)
	}
	first, err := catalog.Get("")
	if err != nil {
		t.Fatalf("Get(default): %v", err)
	}
	if first.Code != Standard || first.Price != "12.5" || first.Days != 45 {
		t.Fatalf("default plan = %+v", first)
	}

	first.AllowedRuleTypes[0] = "changed"
	first.AllowedMarketRules[0] = "changed"
	first.BillingOptions[0].Price = "changed"
	second, _ := catalog.Get(Standard)
	if second.AllowedRuleTypes[0] != BalanceChange {
		t.Fatal("Get returned shared plan slices")
	}
	if second.BillingOptions[0].Price != "12.5" {
		t.Fatal("Get returned shared billing option slices")
	}
	if second.AllowedMarketRules[0] != MarketPriceAbove {
		t.Fatal("Get returned shared market rule slices")
	}
}

func assertBillingOption(
	t *testing.T,
	plan Plan,
	code string,
	wantPrice string,
	wantDays int,
) {
	t.Helper()
	option, err := plan.BillingOption(code)
	if err != nil {
		t.Fatalf("BillingOption(%s): %v", code, err)
	}
	if option.Price != wantPrice || option.Days != wantDays {
		t.Fatalf(
			"BillingOption(%s) = %s/%d, want %s/%d",
			code,
			option.Price,
			option.Days,
			wantPrice,
			wantDays,
		)
	}
}

func TestCatalogRejectsUnsupportedPlan(t *testing.T) {
	t.Parallel()

	catalog, err := NewCatalog("10", 30, "USDT")
	if err != nil {
		t.Fatalf("NewCatalog(): %v", err)
	}
	if _, err := catalog.Get("enterprise"); err == nil {
		t.Fatal("Get(enterprise) error = nil")
	}
}
