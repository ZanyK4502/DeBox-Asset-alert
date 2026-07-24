package plans

import "testing"

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
	if free.WalletLimit != 1 || free.RuleLimit != 1 || free.GroupLimit != 0 {
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

	standard, err := catalog.Get(" STANDARD ")
	if err != nil {
		t.Fatalf("Get(standard): %v", err)
	}
	if standard.Price != "10" || standard.Days != 30 {
		t.Fatalf("standard price/days = %s/%d", standard.Price, standard.Days)
	}
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

	professional, err := catalog.Get("professional")
	if err != nil {
		t.Fatalf("Get(professional): %v", err)
	}
	if professional.WalletLimit != 20 || professional.RuleLimit != 100 || professional.GroupLimit != 3 {
		t.Fatalf("professional limits = %d/%d/%d", professional.WalletLimit, professional.RuleLimit, professional.GroupLimit)
	}
	if !professional.GroupNotification || !professional.AllowsRuleType(AddressInteraction) {
		t.Fatal("professional capabilities do not match the product contract")
	}
	if !professional.AllowsStageNotifications() {
		t.Fatal("professional plan must allow stage notifications")
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
	second, _ := catalog.Get(Standard)
	if second.AllowedRuleTypes[0] != BalanceChange {
		t.Fatal("Get returned shared plan slices")
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
