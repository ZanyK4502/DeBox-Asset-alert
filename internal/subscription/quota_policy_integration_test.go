package subscription

import (
	"context"
	"strings"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/testdb"
)

func TestPostgresPermanentQuotaForExistingAndNewBindings(t *testing.T) {
	db := testdb.Open(t)
	ctx := context.Background()
	// The migration creates these allowlist grants in this isolated schema.
	const oldUser = "quota-existing-permanent"
	if _, err := db.BindPermanentPlan(ctx, oldUser, allowlistedWallet); err != nil {
		t.Fatal(err)
	}
	catalog, err := plans.NewCatalog("10", 30, "USDT")
	if err != nil {
		t.Fatal(err)
	}
	service := New(db, catalog)
	if _, err := service.BindPermanentWallet(ctx, "quota-new-permanent", "0xe4f1f421d116ed75822c4527bfaf332566043b2d"); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{oldUser, "quota-new-permanent"} {
		e, err := service.Entitlement(ctx, user)
		if err != nil {
			t.Fatal(err)
		}
		if !e.Permanent || e.DaysRemaining != 0 || e.Plan.RuleLimit != 10 || e.Plan.InteractionRuleLimit != 1 ||
			e.Plan.MarketProjectLimit != 5 || e.Plan.MarketPoolLimit != 5 || !e.Plan.AllowsRuleType(plans.AddressInteraction) ||
			!e.Plan.AllowsMarketRuleType(plans.MarketHolderRankEntered) || e.Plan.GroupLimit != 5 {
			t.Fatalf("permanent entitlement=%+v", e)
		}
	}
	plan, err := service.ActivePlan(ctx, oldUser)
	if err != nil {
		t.Fatal(err)
	}
	zero := "0"
	target := "0x" + strings.Repeat("7", 40)
	member := func(kind, wallet string) store.CreateCombinationMemberParams {
		return store.CreateCombinationMemberParams{RequiredTriggerCount: 1, Rule: store.CreateWatchRuleParams{
			ChainKey: "bsc", ChainID: 56, WalletAddress: wallet, RuleType: kind, Threshold: "0", LastValue: &zero, TargetAddress: &target,
		}}
	}
	if _, err := db.CreateCombinationRuleWithinQuota(ctx, store.CreateCombinationRuleParams{
		DeBoxUserID: oldUser, CycleMinutes: 60, CycleType: "fixed", NotificationChatID: oldUser, NotificationChatType: "private",
		Members: []store.CreateCombinationMemberParams{
			member(plans.AddressInteraction, "0x"+strings.Repeat("1", 40)),
			member(plans.BalanceChange, "0x"+strings.Repeat("2", 40)),
		},
	}, quotaPolicy(plan)); err != nil {
		t.Fatal(err)
	}
	e, err := service.Entitlement(ctx, oldUser)
	if err != nil {
		t.Fatal(err)
	}
	if e.RuleCount != 3 || e.InteractionRuleCount != 1 || len(e.Rules) != 0 {
		t.Fatalf("combination count was omitted or double-counted: %+v", e)
	}
	if _, err := service.CreateWatchRule(ctx, store.CreateWatchRuleParams{
		DeBoxUserID: oldUser, ChainKey: "bsc", ChainID: 56, WalletAddress: "0x" + strings.Repeat("3", 40),
		RuleType: plans.AddressInteraction, TargetAddress: &target, Threshold: "0", NotificationChatID: oldUser, NotificationChatType: "private",
	}); err == nil || !strings.Contains(err.Error(), "1 条指定地址交互") {
		t.Fatalf("permanent interaction quota error=%v", err)
	}
	if _, err := service.BindPermanentWallet(ctx, "quota-standard-permanent", "0xcd44ffeb623bdc62a821a0301fad91e1c44c3643"); err != nil {
		t.Fatal(err)
	}
	standard, err := service.Entitlement(ctx, "quota-standard-permanent")
	if err != nil {
		t.Fatal(err)
	}
	if !standard.Permanent || standard.Plan.RuleLimit != 3 || standard.Plan.InteractionRuleLimit != 0 || standard.Plan.MarketProjectLimit != 1 || standard.Plan.MarketPoolLimit != 1 || standard.Plan.AllowsRuleType(plans.AddressInteraction) {
		t.Fatalf("standard entitlement=%+v", standard)
	}
}
