package monitor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type fakeRepository struct {
	rules       []store.WatchRule
	dailyEvents int64
	nextEventID int64
	calls       []string
	lastValue   string
	lastStatus  string
	lastError   string
}

func (f *fakeRepository) ListEnabledWatchRules(
	context.Context,
	int,
) ([]store.WatchRule, error) {
	f.calls = append(f.calls, "list")
	return append([]store.WatchRule(nil), f.rules...), nil
}

func (f *fakeRepository) UpdateWatchRuleValue(
	_ context.Context,
	_ int64,
	value string,
) error {
	f.calls = append(f.calls, "update_rule")
	f.lastValue = value
	return nil
}

func (f *fakeRepository) CountDailyAlertEvents(
	context.Context,
	string,
	string,
) (int64, error) {
	f.calls = append(f.calls, "count_daily")
	return f.dailyEvents, nil
}

func (f *fakeRepository) CreateAlertEvent(
	_ context.Context,
	params store.CreateAlertEventParams,
) (store.AlertEvent, error) {
	f.calls = append(f.calls, "create_event")
	if f.nextEventID == 0 {
		f.nextEventID = 31
	}
	return store.AlertEvent{
		ID:                 f.nextEventID,
		WatchRuleID:        params.WatchRuleID,
		EventType:          params.EventType,
		PreviousValue:      params.PreviousValue,
		CurrentValue:       params.CurrentValue,
		NotificationStatus: params.NotificationStatus,
	}, nil
}

func (f *fakeRepository) UpdateAlertEventNotification(
	_ context.Context,
	eventID int64,
	status string,
	messageID *string,
	notificationError string,
) (store.AlertEvent, error) {
	f.calls = append(f.calls, "update_event_"+status)
	f.lastStatus = status
	f.lastError = notificationError
	return store.AlertEvent{
		ID:                    eventID,
		NotificationStatus:    status,
		NotificationMessageID: messageID,
		NotificationError:     notificationError,
	}, nil
}

type fakeChain struct {
	balance     chain.BalanceResult
	allowance   chain.AllowanceResult
	interaction chain.InteractionResult
	calls       []string
}

func (f *fakeChain) Balance(
	context.Context,
	string,
	string,
	string,
	string,
) (chain.BalanceResult, error) {
	f.calls = append(f.calls, "balance")
	return f.balance, nil
}

func (f *fakeChain) TokenAllowance(
	context.Context,
	string,
	string,
	string,
	string,
	string,
) (chain.AllowanceResult, error) {
	f.calls = append(f.calls, "allowance")
	return f.allowance, nil
}

func (f *fakeChain) LatestInteraction(
	context.Context,
	string,
	string,
	string,
	string,
) (chain.InteractionResult, error) {
	f.calls = append(f.calls, "interaction")
	return f.interaction, nil
}

type fakeNotifier struct {
	calls   *[]string
	message string
	err     error
}

func (f *fakeNotifier) SendNotification(chatID, chatType, text string) (string, error) {
	if f.calls != nil {
		*f.calls = append(*f.calls, "send")
	}
	f.message = text
	if f.err != nil {
		return "", f.err
	}
	return "message-9", nil
}

func TestShouldAlertAssetPreservesRuleSemantics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		ruleType  string
		previous  string
		current   string
		threshold string
		want      bool
	}{
		{"balance changed above threshold", plans.BalanceChange, "10", "12", "2", true},
		{"balance change below threshold", plans.BalanceChange, "10", "11", "2", false},
		{"incoming", plans.Incoming, "10", "12", "2", true},
		{"incoming ignores outgoing", plans.Incoming, "10", "8", "1", false},
		{"outgoing", plans.Outgoing, "10", "8", "2", true},
		{"outgoing ignores incoming", plans.Outgoing, "10", "12", "1", false},
		{"threshold crosses downward", plans.BalanceThreshold, "11", "10", "10", true},
		{"threshold remains below", plans.BalanceThreshold, "9", "8", "10", false},
		{"threshold recovers", plans.BalanceThreshold, "9", "11", "10", false},
		{"scientific notation", plans.BalanceChange, "1e2", "1.01e2", "1", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := shouldAlertAsset(test.ruleType, test.previous, test.current, test.threshold)
			if got != test.want {
				t.Fatalf("shouldAlertAsset() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestInitialBalanceBelowThresholdAlertsOnceAndRecordsBeforeSending(t *testing.T) {
	repository := &fakeRepository{}
	chainService := &fakeChain{balance: chain.BalanceResult{Value: "5", Symbol: "BNB"}}
	notifier := &fakeNotifier{calls: &repository.calls}
	executor := newTestExecutor(t, repository, chainService, notifier)
	rule := testRule(plans.BalanceThreshold, nil, plans.Standard)

	result := executor.checkRule(context.Background(), rule, plans.Standard)

	if result.Status != "alerted" || result.Event == nil || result.Event.NotificationStatus != "sent" {
		t.Fatalf("result = %#v", result)
	}
	wantCalls := []string{"update_rule", "create_event", "send", "update_event_sent"}
	if strings.Join(repository.calls, ",") != strings.Join(wantCalls, ",") {
		t.Fatalf("calls = %v, want %v", repository.calls, wantCalls)
	}
	if !strings.Contains(notifier.message, "余额达到或低于阈值 10") {
		t.Fatalf("notification = %q", notifier.message)
	}
}

func TestContinuingBelowThresholdDoesNotRepeat(t *testing.T) {
	repository := &fakeRepository{}
	chainService := &fakeChain{balance: chain.BalanceResult{Value: "4", Symbol: "BNB"}}
	notifier := &fakeNotifier{}
	executor := newTestExecutor(t, repository, chainService, notifier)
	lastValue := "5"
	rule := testRule(plans.BalanceThreshold, &lastValue, plans.Standard)

	result := executor.checkRule(context.Background(), rule, plans.Standard)

	if result.Status != "no_change" {
		t.Fatalf("status = %q, want no_change", result.Status)
	}
	if strings.Contains(strings.Join(repository.calls, ","), "create_event") {
		t.Fatalf("unexpected event calls: %v", repository.calls)
	}
}

func TestApprovalAndInteractionRulesAlertOnNewValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		ruleType string
		prepare  func(*fakeChain)
		last     string
	}{
		{
			name:     "approval",
			ruleType: plans.ApprovalChange,
			prepare: func(service *fakeChain) {
				service.allowance = chain.AllowanceResult{Value: "25"}
			},
			last: "10",
		},
		{
			name:     "interaction",
			ruleType: plans.AddressInteraction,
			prepare: func(service *fakeChain) {
				service.interaction = chain.InteractionResult{Cursor: "0xnew", Matched: true}
			},
			last: "0xold",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &fakeRepository{}
			chainService := &fakeChain{}
			test.prepare(chainService)
			notifier := &fakeNotifier{}
			executor := newTestExecutor(t, repository, chainService, notifier)
			rule := testRule(test.ruleType, &test.last, plans.Professional)

			result := executor.checkRule(context.Background(), rule, plans.Professional)

			if result.Status != "alerted" {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestPlanLimitStopsRuleBeforeChainRequest(t *testing.T) {
	repository := &fakeRepository{}
	chainService := &fakeChain{}
	executor := newTestExecutor(t, repository, chainService, &fakeNotifier{})
	rule := testRule(plans.AddressInteraction, nil, plans.Free)

	result := executor.checkRule(context.Background(), rule, plans.Free)

	if result.Status != "plan_limited" || result.Reason != "rule_type" {
		t.Fatalf("result = %#v", result)
	}
	if len(chainService.calls) != 0 {
		t.Fatalf("chain calls = %v, want none", chainService.calls)
	}
}

func TestFreeDailyLimitAdvancesValueWithoutCreatingEvent(t *testing.T) {
	repository := &fakeRepository{dailyEvents: 5}
	chainService := &fakeChain{balance: chain.BalanceResult{Value: "12", Symbol: "BNB"}}
	executor := newTestExecutor(t, repository, chainService, &fakeNotifier{})
	lastValue := "10"
	rule := testRule(plans.BalanceChange, &lastValue, plans.Free)
	rule.Threshold = "0"

	result := executor.checkRule(context.Background(), rule, plans.Free)

	if result.Status != "daily_limit" || result.Limit != 5 || result.Used != 5 {
		t.Fatalf("result = %#v", result)
	}
	if repository.lastValue != "12" {
		t.Fatalf("last value = %q, want 12", repository.lastValue)
	}
	if strings.Contains(strings.Join(repository.calls, ","), "create_event") {
		t.Fatalf("unexpected event calls: %v", repository.calls)
	}
}

func TestNotificationFailureIsRecorded(t *testing.T) {
	repository := &fakeRepository{}
	chainService := &fakeChain{balance: chain.BalanceResult{Value: "12", Symbol: "BNB"}}
	notifier := &fakeNotifier{calls: &repository.calls, err: errors.New("DeBox unavailable")}
	executor := newTestExecutor(t, repository, chainService, notifier)
	lastValue := "10"
	rule := testRule(plans.BalanceChange, &lastValue, plans.Standard)
	rule.Threshold = "0"

	result := executor.checkRule(context.Background(), rule, plans.Standard)

	if result.Status != "error" || !strings.Contains(result.Error, "DeBox unavailable") {
		t.Fatalf("result = %#v", result)
	}
	if repository.lastStatus != "failed" || repository.lastError != "DeBox unavailable" {
		t.Fatalf("notification state = %q/%q", repository.lastStatus, repository.lastError)
	}
	wantCalls := []string{"update_rule", "create_event", "send", "update_event_failed"}
	if strings.Join(repository.calls, ",") != strings.Join(wantCalls, ",") {
		t.Fatalf("calls = %v, want %v", repository.calls, wantCalls)
	}
}

func TestCheckAllCollectsErrorsWithoutStoppingOtherRules(t *testing.T) {
	lastValue := "10"
	repository := &fakeRepository{rules: []store.WatchRule{
		testRule(plans.BalanceChange, &lastValue, plans.Standard),
		testRule("unknown", nil, plans.Standard),
	}}
	repository.rules[0].ID = 1
	repository.rules[1].ID = 2
	chainService := &fakeChain{balance: chain.BalanceResult{Value: "10", Symbol: "BNB"}}
	executor := newTestExecutor(t, repository, chainService, &fakeNotifier{})

	result, err := executor.CheckAll(context.Background(), 200)

	if err != nil {
		t.Fatalf("CheckAll() error = %v", err)
	}
	if result.Checked != 2 || len(result.Results) != 2 {
		t.Fatalf("result = %#v", result)
	}
	if result.Results[0].Status != "no_change" || result.Results[1].Status != "plan_limited" {
		t.Fatalf("statuses = %q/%q", result.Results[0].Status, result.Results[1].Status)
	}
}

func newTestExecutor(
	t *testing.T,
	repository Repository,
	chainService ChainService,
	notifications NotificationService,
) *Executor {
	t.Helper()
	catalog, err := plans.NewCatalog("10", 30, "USDT")
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	return New(Dependencies{
		Repository:      repository,
		Chain:           chainService,
		Notifications:   notifications,
		Catalog:         catalog,
		DefaultChainKey: "bsc",
	})
}

func testRule(ruleType string, lastValue *string, planCode string) store.WatchRule {
	tokenAddress := "0x2222222222222222222222222222222222222222"
	targetAddress := "0x3333333333333333333333333333333333333333"
	return store.WatchRule{
		ID:                   7,
		DeBoxUserID:          "user-1",
		ChainKey:             "bsc",
		ChainID:              56,
		WalletAddress:        "0x1111111111111111111111111111111111111111",
		TokenAddress:         &tokenAddress,
		TargetAddress:        &targetAddress,
		RuleType:             ruleType,
		Threshold:            "10",
		NotificationChatID:   "user-1",
		NotificationChatType: "private",
		NotificationLanguage: "zh",
		RunStatus:            "active",
		Enabled:              1,
		LastValue:            lastValue,
		EffectivePlanCode:    planCode,
	}
}
