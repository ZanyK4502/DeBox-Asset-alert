package subscription

import (
	"context"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const allowlistedWallet = "0xcba3fce9d49ce5d7870443f324a8dd56a5788bfc"

func TestEntitlementPausesExpiredPaidRulesButKeepsSelectedFreeRule(t *testing.T) {
	t.Parallel()

	freeRuleID := int64(7)
	repository := &fakeRepository{
		paidHistory: true,
		preferences: store.UserPreference{
			DeBoxUserID:     "user-1",
			FreeWatchRuleID: &freeRuleID,
			BotLanguage:     "zh",
		},
		rules: []store.WatchRule{
			{
				ID:                   freeRuleID,
				DeBoxUserID:          "user-1",
				WalletAddress:        "0x1111111111111111111111111111111111111111",
				RuleType:             plans.BalanceChange,
				NotificationChatType: "private",
				Enabled:              1,
				RunStatus:            "active",
			},
			{
				ID:                   8,
				DeBoxUserID:          "user-1",
				WalletAddress:        "0x2222222222222222222222222222222222222222",
				RuleType:             plans.Outgoing,
				NotificationChatType: "private",
				Enabled:              1,
				RunStatus:            "active",
			},
		},
		ruleCount:   1,
		walletCount: 1,
	}
	service := newTestService(t, repository)

	result, err := service.Entitlement(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("Entitlement(): %v", err)
	}
	if !repository.expiryFallbackCalled {
		t.Fatal("Entitlement did not apply the paid-expiry fallback")
	}
	if !result.PaidHistory || !result.FallbackFree || result.Plan.Code != plans.Free {
		t.Fatalf("entitlement status = %+v", result)
	}
	if len(result.ActiveRules) != 1 || result.ActiveRules[0].ID != freeRuleID {
		t.Fatalf("active rules = %+v", result.ActiveRules)
	}
	if len(result.PausedRules) != 1 || result.PausedRules[0].ID != 8 {
		t.Fatalf("paused rules = %+v", result.PausedRules)
	}
	if !result.PausedRules[0].CanSelectFree {
		t.Fatal("eligible paused rule cannot be selected for the free plan")
	}
}

func TestCreateWatchRuleUsesAtomicFreeQuotaPolicy(t *testing.T) {
	t.Parallel()

	repository := &fakeRepository{
		createdRule: store.WatchRule{ID: 9, RunStatus: "active"},
	}
	service := newTestService(t, repository)

	rule, err := service.CreateWatchRule(context.Background(), store.CreateWatchRuleParams{
		DeBoxUserID:          "user-1",
		WalletAddress:        "0x1111111111111111111111111111111111111111",
		RuleType:             plans.BalanceChange,
		NotificationChatType: "private",
	})
	if err != nil {
		t.Fatalf("CreateWatchRule(): %v", err)
	}
	if rule.ID != 9 {
		t.Fatalf("created rule = %+v", rule)
	}
	if repository.createdPolicy.PlanCode != plans.Free ||
		repository.createdPolicy.RuleLimit != 1 ||
		repository.createdPolicy.WalletLimit != 1 {
		t.Fatalf("creation policy = %+v", repository.createdPolicy)
	}
	if repository.setFreeCalls != 0 {
		t.Fatal("free rule selection must be part of the creation transaction")
	}
}

func TestRestorePaidRuleReportsQuotaLimit(t *testing.T) {
	t.Parallel()

	repository := &fakeRepository{
		activeSubscription: &store.Subscription{PlanCode: plans.Standard},
		restoreErr:         store.ErrRuleLimitReached,
	}
	service := newTestService(t, repository)

	_, err := service.RestorePausedWatchRule(context.Background(), "user-1", 12)
	if err == nil || err.Error() != "当前套餐最多支持 10 条运行规则。" {
		t.Fatalf("RestorePausedWatchRule() error = %v", err)
	}
	if repository.restoredPolicy.PlanCode != plans.Standard {
		t.Fatalf("restore policy = %+v", repository.restoredPolicy)
	}
}

func TestComplimentaryAccessIsNormalizedAndOneTime(t *testing.T) {
	t.Parallel()

	expiresAt := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)
	repository := &fakeRepository{
		grant: &store.ComplimentaryGrant{
			WalletAddress: allowlistedWallet,
			PlanCode:      plans.Professional,
			ExpiresAt:     expiresAt,
		},
	}
	service := newTestService(t, repository)

	result, err := service.ComplimentaryAccess(
		context.Background(),
		"0xCBA3FCE9D49CE5D7870443F324A8DD56A5788BFC",
	)
	if err != nil {
		t.Fatalf("ComplimentaryAccess(): %v", err)
	}
	if !result.Eligible || !result.Used || result.Available {
		t.Fatalf("complimentary access = %+v", result)
	}
	if repository.grantWallet != allowlistedWallet {
		t.Fatalf("grant lookup wallet = %q", repository.grantWallet)
	}
}

func TestActivateComplimentaryPlanUsesThirtyDays(t *testing.T) {
	t.Parallel()

	repository := &fakeRepository{}
	service := newTestService(t, repository)

	_, err := service.ActivateComplimentaryPlan(
		context.Background(),
		"user-1",
		allowlistedWallet,
		plans.Standard,
	)
	if err != nil {
		t.Fatalf("ActivateComplimentaryPlan(): %v", err)
	}
	if repository.complimentaryWallet != allowlistedWallet ||
		repository.complimentaryPlan != plans.Standard ||
		repository.complimentaryDays != ComplimentaryDays {
		t.Fatalf(
			"complimentary activation = %s/%s/%d",
			repository.complimentaryWallet,
			repository.complimentaryPlan,
			repository.complimentaryDays,
		)
	}
}

func TestDaysRemainingRoundsUp(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	subscription := &store.Subscription{ExpiresAt: now.Add(24*time.Hour + time.Second)}
	if got := daysRemaining(subscription, now); got != 2 {
		t.Fatalf("daysRemaining() = %d, want 2", got)
	}
}

func newTestService(t *testing.T, repository Repository) *Service {
	t.Helper()
	catalog, err := plans.NewCatalog("10", 30, "USDT")
	if err != nil {
		t.Fatalf("NewCatalog(): %v", err)
	}
	return New(repository, catalog, allowlistedWallet)
}

type fakeRepository struct {
	activeSubscription *store.Subscription
	paidHistory        bool
	preferences        store.UserPreference
	rules              []store.WatchRule
	groups             []store.NotificationGroup
	ruleCount          int64
	walletCount        int64
	groupCount         int64

	expiryFallbackCalled bool
	setFreeCalls         int
	createdRule          store.WatchRule
	createdPolicy        store.QuotaPolicy
	createErr            error
	restoredPolicy       store.QuotaPolicy
	restoreErr           error
	createdGroup         store.NotificationGroup
	groupErr             error

	grant               *store.ComplimentaryGrant
	grantWallet         string
	complimentaryWallet string
	complimentaryPlan   string
	complimentaryDays   int
	complimentaryErr    error
}

func (f *fakeRepository) GetActiveSubscription(
	context.Context,
	string,
) (*store.Subscription, error) {
	return f.activeSubscription, nil
}

func (f *fakeRepository) HasPaidSubscriptionHistory(context.Context, string) (bool, error) {
	return f.paidHistory, nil
}

func (f *fakeRepository) GetUserPreferences(
	_ context.Context,
	deboxUserID string,
) (store.UserPreference, error) {
	if f.preferences.DeBoxUserID == "" {
		f.preferences.DeBoxUserID = deboxUserID
		f.preferences.BotLanguage = "zh"
	}
	return f.preferences, nil
}

func (f *fakeRepository) ApplyPaidExpiryFallback(
	_ context.Context,
	_ string,
	exceptRuleID *int64,
) (bool, error) {
	f.expiryFallbackCalled = true
	if !f.paidHistory || f.activeSubscription != nil {
		return false, nil
	}
	for index := range f.rules {
		if exceptRuleID == nil || f.rules[index].ID != *exceptRuleID {
			f.rules[index].RunStatus = "paused"
		}
	}
	return true, nil
}

func (f *fakeRepository) ListUserWatchRules(
	context.Context,
	string,
) ([]store.WatchRule, error) {
	return append([]store.WatchRule(nil), f.rules...), nil
}

func (f *fakeRepository) ListNotificationGroups(
	context.Context,
	string,
) ([]store.NotificationGroup, error) {
	return append([]store.NotificationGroup(nil), f.groups...), nil
}

func (f *fakeRepository) CountUserWatchRules(context.Context, string) (int64, error) {
	return f.ruleCount, nil
}

func (f *fakeRepository) CountUserWallets(context.Context, string) (int64, error) {
	return f.walletCount, nil
}

func (f *fakeRepository) CountNotificationGroups(context.Context, string) (int64, error) {
	return f.groupCount, nil
}

func (f *fakeRepository) SetFreeWatchRule(
	context.Context,
	string,
	int64,
) (store.UserPreference, error) {
	f.setFreeCalls++
	return f.preferences, nil
}

func (f *fakeRepository) CreateWatchRuleWithinQuota(
	_ context.Context,
	_ store.CreateWatchRuleParams,
	policy store.QuotaPolicy,
) (store.WatchRule, error) {
	f.createdPolicy = policy
	return f.createdRule, f.createErr
}

func (f *fakeRepository) RestoreWatchRuleWithinQuota(
	_ context.Context,
	_ int64,
	_ string,
	policy store.QuotaPolicy,
) (store.WatchRule, error) {
	f.restoredPolicy = policy
	return store.WatchRule{}, f.restoreErr
}

func (f *fakeRepository) CreateNotificationGroupWithinQuota(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ store.QuotaPolicy,
) (store.NotificationGroup, error) {
	return f.createdGroup, f.groupErr
}

func (f *fakeRepository) ActivateSubscription(
	context.Context,
	string,
	string,
	int,
) (store.Subscription, error) {
	return store.Subscription{}, nil
}

func (f *fakeRepository) GetComplimentaryGrant(
	_ context.Context,
	wallet string,
) (*store.ComplimentaryGrant, error) {
	f.grantWallet = wallet
	return f.grant, nil
}

func (f *fakeRepository) ActivateComplimentarySubscription(
	_ context.Context,
	_ string,
	wallet string,
	plan string,
	days int,
) (store.ComplimentaryActivation, error) {
	f.complimentaryWallet = wallet
	f.complimentaryPlan = plan
	f.complimentaryDays = days
	return store.ComplimentaryActivation{}, f.complimentaryErr
}

var _ Repository = (*fakeRepository)(nil)
