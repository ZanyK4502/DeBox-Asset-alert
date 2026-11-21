package subscription

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestCurrentQuotaPolicyAppliesEquallyToPaidAndPermanentUsers(t *testing.T) {
	wantChains := []string{"bsc", "ethereum", "base", "polygon", "arbitrum", "optimism"}
	for _, permanent := range []int32{0, 1} {
		repo := &fakeRepository{
			activeSubscription: &store.Subscription{PlanCode: plans.Professional, IsPermanent: permanent},
			createdRule:        store.WatchRule{ID: 11},
		}
		service := newTestService(t, repo)
		if _, err := service.CreateWatchRule(context.Background(), store.CreateWatchRuleParams{DeBoxUserID: "quota-user"}); err != nil {
			t.Fatal(err)
		}
		if _, err := service.RestorePausedWatchRule(context.Background(), "quota-user", 11); err != nil {
			t.Fatal(err)
		}
		for _, policy := range []store.QuotaPolicy{repo.createdPolicy, repo.restoredPolicy} {
			if policy.RuleLimit != 10 || policy.InteractionRuleLimit != 1 || policy.MarketProjectLimit != 5 || policy.MarketPoolLimit != 5 || !slices.Equal(policy.AllowedMarketChainKeys, wantChains) ||
				!policy.CombinationRules || !policy.GroupNotification {
				t.Fatalf("permanent=%d policy=%+v", permanent, policy)
			}
		}
	}
}

func TestQuotaErrorPreservesTypedMarketScopeBeforeGenericQuotaMapping(t *testing.T) {
	catalog, err := plans.NewCatalog("5", 30, "USDT")
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := catalog.Get(plans.Professional)
	for _, scope := range []error{store.ErrMarketChainNotAllowed, store.ErrMarketPoolLimitReached, store.ErrMarketHolderTargetsReached} {
		input := errors.Join(fmt.Errorf("wrapped market scope: %w", scope), store.ErrRuleLimitReached)
		if got := quotaError(input, plan, false); got != input || !errors.Is(got, scope) {
			t.Fatalf("scope identity lost to generic quota mapping: %v", got)
		}
	}
}

func TestQuotaPolicyMarketScopeIsExplicitAndDoesNotAliasPlan(t *testing.T) {
	catalog, err := plans.NewCatalog("5", 30, "USDT")
	if err != nil {
		t.Fatal(err)
	}
	free, _ := catalog.Get(plans.Free)
	freePolicy := quotaPolicy(free)
	if freePolicy.MarketPoolLimit != 0 || freePolicy.AllowedMarketChainKeys == nil || len(freePolicy.AllowedMarketChainKeys) != 0 {
		t.Fatal("free market policy must be explicitly empty")
	}
	standard, _ := catalog.Get(plans.Standard)
	policy := quotaPolicy(standard)
	wantChains := []string{"bsc", "ethereum", "base", "polygon", "arbitrum", "optimism"}
	if policy.MarketPoolLimit != 1 || !slices.Equal(policy.AllowedMarketChainKeys, wantChains) {
		t.Fatalf("standard scope=%+v", policy)
	}
	policy.AllowedMarketChainKeys[0] = "base"
	if !slices.Equal(standard.AllowedMarketChainKeys, wantChains) {
		t.Fatal("policy scope aliased catalog plan")
	}
}

func TestInteractionCountUsesRepositoryTotalIncludingCombinationMembers(t *testing.T) {
	repo := &fakeRepository{
		activeSubscription:   &store.Subscription{PlanCode: plans.Professional, IsPermanent: 1},
		interactionRuleCount: 1,
		ruleCount:            3,
		// The standalone list intentionally excludes the combination members.
		rules: []store.WatchRule{},
	}
	entitlement, err := newTestService(t, repo).Entitlement(context.Background(), "quota-user")
	if err != nil {
		t.Fatal(err)
	}
	if !entitlement.Permanent || entitlement.InteractionRuleCount != 1 || entitlement.RuleCount != 3 ||
		entitlement.Plan.InteractionRuleLimit != 1 || len(entitlement.Rules) != 0 {
		t.Fatalf("entitlement=%+v", entitlement)
	}
	err = quotaError(store.ErrInteractionRuleLimitReached, entitlement.Plan, false)
	if err == nil || !strings.Contains(err.Error(), "1 条指定地址交互") || !strings.Contains(err.Error(), "组合成员合计") {
		t.Fatalf("quota message=%v", err)
	}
}
