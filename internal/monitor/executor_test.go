package monitor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type fakeRepository struct {
	rules              []store.WatchRule
	dailyEvents        int64
	nextEventID        int64
	calls              []string
	lastValue          string
	lastStatus         string
	lastError          string
	stageParams        store.RecordStageTriggerParams
	stageResult        store.StageTriggerResult
	combinationParams  store.RecordCombinationTriggerParams
	combinationResult  store.CombinationTriggerResult
	snapshots          []store.CreateNotificationDetailSnapshotParams
	snapshotErr        error
	expiredUsersPaused int64
	expiryErr          error
}

func (f *fakeRepository) ApplyExpiredEntitlementFallbacks(
	context.Context,
) (int64, error) {
	f.calls = append(f.calls, "expire_entitlements")
	return f.expiredUsersPaused, f.expiryErr
}

func (f *fakeRepository) CreateNotificationDetailSnapshot(
	_ context.Context,
	params store.CreateNotificationDetailSnapshotParams,
) (store.NotificationDetailSnapshot, error) {
	f.calls = append(f.calls, "create_snapshot")
	if f.snapshotErr != nil {
		return store.NotificationDetailSnapshot{}, f.snapshotErr
	}
	f.snapshots = append(f.snapshots, params)
	return store.NotificationDetailSnapshot{
		ID:               int64(len(f.snapshots)),
		PublicID:         "nd_0123456789abcdef0123456789abcdef01234567",
		SourceKey:        params.SourceKey,
		NotificationKind: params.NotificationKind,
		Details:          params.Details,
	}, nil
}

func (f *fakeRepository) ListEnabledWatchRules(
	context.Context,
	int,
) ([]store.WatchRule, error) {
	f.calls = append(f.calls, "list")
	return append([]store.WatchRule(nil), f.rules...), nil
}

func (f *fakeRepository) CleanupAggregationHistory(
	context.Context,
) (store.AggregationCleanupResult, error) {
	f.calls = append(f.calls, "cleanup")
	return store.AggregationCleanupResult{}, nil
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

func (f *fakeRepository) RecordStageTrigger(
	_ context.Context,
	params store.RecordStageTriggerParams,
) (store.StageTriggerResult, error) {
	f.calls = append(f.calls, "record_stage")
	f.stageParams = params
	return f.stageResult, nil
}

func (f *fakeRepository) RecordCombinationTrigger(
	_ context.Context,
	params store.RecordCombinationTriggerParams,
) (store.CombinationTriggerResult, error) {
	f.calls = append(f.calls, "record_combination")
	f.combinationParams = params
	return f.combinationResult, nil
}

func (f *fakeRepository) UpdateAggregateNotification(
	_ context.Context,
	notificationID int64,
	status string,
	messageID *string,
	notificationError string,
) (store.AggregateNotification, error) {
	f.calls = append(f.calls, "update_aggregate_"+status)
	f.lastStatus = status
	f.lastError = notificationError
	return store.AggregateNotification{
		ID:                    notificationID,
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
	calls      *[]string
	message    string
	actionText string
	actionURL  string
	err        error
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

func (f *fakeNotifier) SendNotificationWithAction(
	chatID, chatType, text, actionText, actionURL string,
) (string, error) {
	f.actionText = actionText
	f.actionURL = actionURL
	return f.SendNotification(chatID, chatType, text)
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
		{"high threshold crosses upward", plans.HighBalanceThreshold, "9", "10", "10", true},
		{"high threshold remains above", plans.HighBalanceThreshold, "11", "12", "10", false},
		{"high threshold recovers", plans.HighBalanceThreshold, "11", "9", "10", false},
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

func TestAlertTextUsesSharedDisplayFormatting(t *testing.T) {
	rule := testRule(plans.BalanceChange, nil, plans.Standard)
	previous := "1234567.89"
	message := alertText(
		rule,
		&previous,
		"2850000",
		"BNB",
		"BNB 余额触发监控条件。",
		time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC),
	)
	plainMessage := plainNotificationText(message)
	for _, expected := range []string{
		"0x1111…1111 余额增加 1.62M BNB",
		"当前余额：2,850,000 BNB",
		"变化前：1,234,567.89 BNB",
		"网络：BNB Chain · 今天 20:00",
	} {
		if !strings.Contains(plainMessage, expected) {
			t.Fatalf("notification missing %q: %s", expected, message)
		}
	}
	if strings.Contains(message, "网络：bsc") ||
		strings.Contains(message, rule.WalletAddress) {
		t.Fatalf("notification contains an internal chain key or full address: %s", message)
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
	wantCalls := []string{
		"update_rule", "create_event", "create_snapshot", "send", "update_event_sent",
	}
	if strings.Join(repository.calls, ",") != strings.Join(wantCalls, ",") {
		t.Fatalf("calls = %v, want %v", repository.calls, wantCalls)
	}
	if len(repository.snapshots) != 1 {
		t.Fatalf("snapshots = %#v", repository.snapshots)
	}
	snapshot := repository.snapshots[0]
	if snapshot.NotificationKind != store.NotificationKindAddressRealtime ||
		snapshot.SourceKey != "address_realtime:31" ||
		snapshot.RuleID == nil || *snapshot.RuleID != rule.ID ||
		snapshot.RuleThreshold != "10" || snapshot.ActualValue != "5" ||
		snapshot.NotificationText != notifier.message ||
		!strings.Contains(string(snapshot.Details), `"rule_type":"balance_threshold"`) {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	plainMessage := plainNotificationText(notifier.message)
	if !strings.Contains(plainMessage, "余额低于 10 BNB") ||
		!strings.Contains(plainMessage, "低于阈值：5 BNB") ||
		!strings.Contains(plainMessage, "🔴 风险等级：高") {
		t.Fatalf("notification = %q", notifier.message)
	}
	if notifier.actionText != "查看详情" ||
		notifier.actionURL != "https://example.test?notification_id=nd_0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("realtime action = %q / %q", notifier.actionText, notifier.actionURL)
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

func TestInitialBalanceAboveHighThresholdAlertsOnce(t *testing.T) {
	repository := &fakeRepository{}
	chainService := &fakeChain{balance: chain.BalanceResult{Value: "15", Symbol: "BNB"}}
	notifier := &fakeNotifier{calls: &repository.calls}
	executor := newTestExecutor(t, repository, chainService, notifier)
	rule := testRule(plans.HighBalanceThreshold, nil, plans.Standard)

	result := executor.checkRule(context.Background(), rule, plans.Standard)

	if result.Status != "alerted" || result.Event == nil || result.Event.NotificationStatus != "sent" {
		t.Fatalf("result = %#v", result)
	}
	plainMessage := plainNotificationText(notifier.message)
	if !strings.Contains(plainMessage, "余额高于 10 BNB") ||
		!strings.Contains(plainMessage, "超过阈值：5 BNB") {
		t.Fatalf("notification = %q", notifier.message)
	}
}

func TestContinuingAboveHighThresholdDoesNotRepeat(t *testing.T) {
	repository := &fakeRepository{}
	chainService := &fakeChain{balance: chain.BalanceResult{Value: "12", Symbol: "BNB"}}
	notifier := &fakeNotifier{}
	executor := newTestExecutor(t, repository, chainService, notifier)
	lastValue := "11"
	rule := testRule(plans.HighBalanceThreshold, &lastValue, plans.Standard)

	result := executor.checkRule(context.Background(), rule, plans.Standard)

	if result.Status != "no_change" {
		t.Fatalf("status = %q, want no_change", result.Status)
	}
	if strings.Contains(strings.Join(repository.calls, ","), "create_event") {
		t.Fatalf("unexpected event calls: %v", repository.calls)
	}
}

func TestStageRuleCountsWithoutSendingBeforeThreshold(t *testing.T) {
	lastValue := "10"
	repository := &fakeRepository{stageResult: store.StageTriggerResult{
		WindowID:              41,
		TotalTriggerCount:     1,
		TriggerCountThreshold: 3,
	}}
	chainService := &fakeChain{balance: chain.BalanceResult{Value: "12", Symbol: "BNB"}}
	notifier := &fakeNotifier{calls: &repository.calls}
	executor := newTestExecutor(t, repository, chainService, notifier)
	rule := testRule(plans.BalanceChange, &lastValue, plans.Standard)
	rule.Threshold = "1"
	rule.DeliveryMode = "stage"
	rule.CycleType = "fixed"
	rule.CycleMinutes = 15
	rule.TriggerCountThreshold = 3

	result := executor.checkRule(context.Background(), rule, plans.Standard)

	if result.Status != "counted" || result.TriggerCount != 1 || result.TriggerThreshold != 3 {
		t.Fatalf("result = %#v", result)
	}
	if repository.stageParams.WatchRuleID != rule.ID ||
		repository.stageParams.DeBoxUserID != rule.DeBoxUserID ||
		repository.stageParams.CurrentValue == nil ||
		*repository.stageParams.CurrentValue != "12" ||
		repository.stageParams.TokenSymbol != "BNB" {
		t.Fatalf("stage params = %#v", repository.stageParams)
	}
	wantCalls := []string{"update_rule", "record_stage"}
	if strings.Join(repository.calls, ",") != strings.Join(wantCalls, ",") {
		t.Fatalf("calls = %v, want %v", repository.calls, wantCalls)
	}
}

func TestStageRuleSendsOnceWhenThresholdIsReached(t *testing.T) {
	lastValue := "10"
	start := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	repository := &fakeRepository{stageResult: store.StageTriggerResult{
		WindowID:              41,
		TotalTriggerCount:     3,
		TriggerCountThreshold: 3,
		WindowStartsAt:        start,
		WindowEndsAt:          start.Add(30 * time.Minute),
		Timezone:              "Asia/Shanghai",
		NotificationDue:       true,
		Events: []store.StageTriggerEvent{
			{
				PreviousValue: stringPointer("10"), CurrentValue: stringPointer("11"),
				TokenSymbol: "BNB", OccurredAt: start.Add(5 * time.Minute),
			},
			{
				PreviousValue: stringPointer("11"), CurrentValue: stringPointer("13"),
				TokenSymbol: "BNB", OccurredAt: start.Add(15 * time.Minute),
			},
			{
				PreviousValue: stringPointer("13"), CurrentValue: stringPointer("12"),
				TokenSymbol: "BNB", OccurredAt: start.Add(25 * time.Minute),
			},
		},
		Notification: &store.AggregateNotification{
			ID:                   51,
			NotificationChatID:   "user-1",
			NotificationChatType: "private",
		},
	}}
	chainService := &fakeChain{balance: chain.BalanceResult{Value: "12", Symbol: "BNB"}}
	notifier := &fakeNotifier{calls: &repository.calls}
	executor := newTestExecutor(t, repository, chainService, notifier)
	rule := testRule(plans.BalanceChange, &lastValue, plans.Standard)
	rule.Threshold = "1"
	rule.DeliveryMode = "stage"
	rule.CycleType = "follow"
	rule.CycleMinutes = 30
	rule.TriggerCountThreshold = 3

	result := executor.checkRule(context.Background(), rule, plans.Standard)

	if result.Status != "alerted" || result.Aggregate == nil ||
		result.Aggregate.NotificationStatus != "sent" {
		t.Fatalf("result = %#v", result)
	}
	for _, expected := range []string{
		"阶段余额净变化 +2 BNB",
		"统计周期：2026-07-31 20:00 → 2026-07-31 20:30",
		"触发次数：3 次（达到 3 次时发送）",
		"阶段余额：10 BNB → 12 BNB",
		"累计净变化：+2 BNB",
		"最大单次变化：2 BNB",
		"首次 / 最近：20:05 / 20:25",
		"网络：BNB Chain",
	} {
		if !strings.Contains(plainNotificationText(notifier.message), expected) {
			t.Fatalf("notification missing %q: %q", expected, notifier.message)
		}
	}
	if strings.Contains(notifier.message, "最近事件") ||
		strings.Contains(notifier.message, "余额触发监控条件") {
		t.Fatalf("stage notification copied realtime events: %q", notifier.message)
	}
	if notifier.actionText != "查看全部事件" ||
		notifier.actionURL != "https://example.test?notification_id=nd_0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("stage action = %q / %q", notifier.actionText, notifier.actionURL)
	}
	wantCalls := []string{
		"update_rule",
		"record_stage",
		"create_snapshot",
		"send",
		"update_aggregate_sent",
	}
	if strings.Join(repository.calls, ",") != strings.Join(wantCalls, ",") {
		t.Fatalf("calls = %v, want %v", repository.calls, wantCalls)
	}
	if len(repository.snapshots) != 1 {
		t.Fatalf("snapshots = %#v", repository.snapshots)
	}
	snapshot := repository.snapshots[0]
	if snapshot.NotificationKind != store.NotificationKindAddressStage ||
		snapshot.SourceKey != "address_stage:51" ||
		snapshot.RuleThreshold != "1;send_after=3" ||
		snapshot.ActualValue != "12" ||
		snapshot.NotificationText != notifier.message ||
		!strings.Contains(string(snapshot.Details), `"total_trigger_count":3`) {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestFreePlanDoesNotRunStageRules(t *testing.T) {
	repository := &fakeRepository{}
	chainService := &fakeChain{}
	notifier := &fakeNotifier{}
	executor := newTestExecutor(t, repository, chainService, notifier)
	rule := testRule(plans.BalanceChange, nil, plans.Free)
	rule.DeliveryMode = "stage"

	result := executor.checkRule(context.Background(), rule, plans.Free)

	if result.Status != "plan_limited" || result.Reason != "stage_notification" {
		t.Fatalf("result = %#v", result)
	}
	if len(chainService.calls) != 0 || len(repository.calls) != 0 {
		t.Fatalf("unexpected calls: chain=%v repository=%v", chainService.calls, repository.calls)
	}
}

func TestApprovalAndInteractionRulesAlertOnNewValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		ruleType string
		prepare  func(*fakeChain)
		last     string
		expected string
	}{
		{
			name:     "approval",
			ruleType: plans.ApprovalChange,
			prepare: func(service *fakeChain) {
				service.allowance = chain.AllowanceResult{Value: "25", Symbol: "USDT"}
			},
			last:     "10",
			expected: "授权对象：项目金库 · 0x3333…3333",
		},
		{
			name:     "interaction",
			ruleType: plans.AddressInteraction,
			prepare: func(service *fakeChain) {
				service.interaction = chain.InteractionResult{Cursor: "0xnew", Matched: true}
			},
			last:     "0xold",
			expected: "目标地址：项目金库 · 0x3333…3333",
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
			rule.TargetLabel = "项目金库"

			result := executor.checkRule(context.Background(), rule, plans.Professional)

			if result.Status != "alerted" {
				t.Fatalf("result = %#v", result)
			}
			if !strings.Contains(plainNotificationText(notifier.message), test.expected) {
				t.Fatalf("notification missing target label: %q", notifier.message)
			}
		})
	}
}

func TestTargetLabelFlowsIntoStageAndCombinationEventNotes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		ruleScope string
	}{
		{name: "stage", ruleScope: "standalone"},
		{name: "combination", ruleScope: "combination"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			last := "0xold"
			repository := &fakeRepository{}
			if test.ruleScope == "standalone" {
				repository.stageResult = store.StageTriggerResult{
					TotalTriggerCount:     1,
					TriggerCountThreshold: 2,
				}
			}
			executor := newTestExecutor(
				t,
				repository,
				&fakeChain{interaction: chain.InteractionResult{
					Cursor:  "0xnew",
					Matched: true,
				}},
				&fakeNotifier{},
			)
			rule := testRule(plans.AddressInteraction, &last, plans.Professional)
			rule.TargetLabel = "风险合约"
			rule.RuleScope = test.ruleScope
			if test.ruleScope == "standalone" {
				rule.DeliveryMode = "stage"
				rule.CycleMinutes = 15
				rule.TriggerCountThreshold = 2
			}

			result := executor.checkRule(context.Background(), rule, plans.Professional)
			if result.Status != "counted" {
				t.Fatalf("result = %#v", result)
			}
			note := repository.combinationParams.Note
			if test.ruleScope == "standalone" {
				note = repository.stageParams.Note
			}
			if !strings.Contains(note, "目标备注：风险合约") {
				t.Fatalf("event note missing target label: %q", note)
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
	wantCalls := []string{
		"update_rule", "create_event", "create_snapshot", "send", "update_event_failed",
	}
	if strings.Join(repository.calls, ",") != strings.Join(wantCalls, ",") {
		t.Fatalf("calls = %v, want %v", repository.calls, wantCalls)
	}
}

func TestCombinationMemberCountsWithoutSendingUntilAllMembersReachThreshold(t *testing.T) {
	t.Parallel()
	previous := "10"
	repository := &fakeRepository{combinationResult: store.CombinationTriggerResult{
		CombinationRuleID: 44,
		MemberProgress: []store.CombinationMemberProgress{
			{WatchRuleID: 7, RuleType: plans.BalanceChange, TriggerCount: 1, RequiredTriggerCount: 2},
			{WatchRuleID: 8, RuleType: plans.ApprovalChange, TriggerCount: 1, RequiredTriggerCount: 1},
		},
	}}
	rule := testRule(plans.BalanceChange, &previous, plans.Professional)
	rule.RuleScope = "combination"
	rule.Threshold = "0"
	executor := newTestExecutor(
		t,
		repository,
		&fakeChain{balance: chain.BalanceResult{Value: "12", Symbol: "BNB"}},
		&fakeNotifier{},
	)

	value, err := executor.CheckRule(context.Background(), rule, plans.Professional)
	if err != nil {
		t.Fatalf("CheckRule() error = %v", err)
	}
	result := value.(RuleResult)
	if result.Status != "counted" || result.CombinationID != 44 ||
		len(result.MemberProgress) != 2 {
		t.Fatalf("combination result = %#v", result)
	}
	if repository.combinationParams.WatchRuleID != rule.ID {
		t.Fatalf("combination params = %#v", repository.combinationParams)
	}
	if repository.combinationParams.TokenSymbol != "BNB" {
		t.Fatalf("combination token symbol = %q", repository.combinationParams.TokenSymbol)
	}
	if strings.Contains(strings.Join(repository.calls, ","), "send") {
		t.Fatalf("calls = %v", repository.calls)
	}
}

func TestCombinationMemberSendsOneSummaryWhenCombinationIsDue(t *testing.T) {
	t.Parallel()
	previous := "10"
	firstAt := time.Date(2026, 7, 31, 2, 1, 0, 0, time.UTC)
	secondAt := firstAt.Add(4 * time.Minute)
	ten, twelve, fifteen, zero, hundred := "10", "12", "15", "0", "100"
	notification := store.AggregateNotification{
		ID:                   55,
		NotificationChatID:   "user-1",
		NotificationChatType: "private",
		NotificationLanguage: "en",
		Note:                 "Treasury safety",
	}
	repository := &fakeRepository{combinationResult: store.CombinationTriggerResult{
		CombinationRuleID: 44,
		NotificationDue:   true,
		Notification:      &notification,
		MemberProgress: []store.CombinationMemberProgress{
			{
				WatchRuleID:          7,
				RuleType:             plans.BalanceChange,
				TriggerCount:         2,
				RequiredTriggerCount: 2,
				ReachedAt:            &firstAt,
				RecentNotes:          []string{"BNB balance changed.", "Another BNB change."},
				Events: []store.StageTriggerEvent{
					{
						PreviousValue: &ten, CurrentValue: &twelve,
						TokenSymbol: "BNB", OccurredAt: firstAt.Add(-time.Minute),
					},
					{
						PreviousValue: &twelve, CurrentValue: &fifteen,
						TokenSymbol: "BNB", OccurredAt: firstAt,
					},
				},
			},
			{
				WatchRuleID:          8,
				RuleType:             plans.ApprovalChange,
				TriggerCount:         1,
				RequiredTriggerCount: 1,
				ReachedAt:            &secondAt,
				RecentNotes:          []string{"Approval changed."},
				Events: []store.StageTriggerEvent{{
					PreviousValue: &zero, CurrentValue: &hundred,
					TokenSymbol: "BNB", OccurredAt: secondAt,
				}},
			},
		},
	}}
	rule := testRule(plans.BalanceChange, &previous, plans.Professional)
	rule.RuleScope = "combination"
	rule.Threshold = "0"
	notifier := &fakeNotifier{}
	executor := newTestExecutor(
		t,
		repository,
		&fakeChain{balance: chain.BalanceResult{Value: "12", Symbol: "BNB"}},
		notifier,
	)

	value, err := executor.CheckRule(context.Background(), rule, plans.Professional)
	if err != nil {
		t.Fatalf("CheckRule() error = %v", err)
	}
	result := value.(RuleResult)
	if result.Status != "alerted" || result.Aggregate == nil ||
		result.Aggregate.NotificationStatus != "sent" {
		t.Fatalf("combination result = %#v", result)
	}
	for _, expected := range []string{
		"Multiple address signals appeared in the same window",
		"Combination: Treasury safety",
		"Balance change · 2/2 · net change +5 BNB",
		"Approval change · 1/1 · allowance 0 BNB → 100 BNB",
		"Signal order: ① 10:01 Balance change → ② 10:05 Approval change",
		"not causation",
	} {
		if !strings.Contains(plainNotificationText(notifier.message), expected) {
			t.Fatalf("combination message missing %q: %q", expected, notifier.message)
		}
	}
	for _, unexpected := range []string{
		"BNB balance changed.",
		"Another BNB change.",
		"Approval changed.",
		"\n",
	} {
		if strings.Contains(notifier.message, unexpected) {
			t.Fatalf("combination message contains %q: %q", unexpected, notifier.message)
		}
	}
	if notifier.actionText != "View full analysis" ||
		notifier.actionURL != "https://example.test?notification_id=nd_0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("combination message = %q", notifier.message)
	}
	if len(repository.snapshots) != 1 {
		t.Fatalf("snapshots = %#v", repository.snapshots)
	}
	snapshot := repository.snapshots[0]
	if snapshot.NotificationKind != store.NotificationKindAddressCombination ||
		snapshot.SourceKey != "address_combination:55" ||
		snapshot.RuleID == nil || *snapshot.RuleID != 44 ||
		snapshot.RuleThreshold != "balance_change>=2;approval_change>=1" ||
		snapshot.ActualValue != "balance_change=2;approval_change=1" ||
		snapshot.NotificationText != notifier.message ||
		!strings.Contains(string(snapshot.Details), `"combination_rule_id":44`) {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestAddressSnapshotFailurePreventsNotificationDelivery(t *testing.T) {
	t.Parallel()
	lastValue := "10"
	repository := &fakeRepository{snapshotErr: errors.New("snapshot database unavailable")}
	notifier := &fakeNotifier{calls: &repository.calls}
	executor := newTestExecutor(
		t,
		repository,
		&fakeChain{balance: chain.BalanceResult{Value: "12", Symbol: "BNB"}},
		notifier,
	)
	rule := testRule(plans.BalanceChange, &lastValue, plans.Standard)
	rule.Threshold = "0"

	result := executor.checkRule(context.Background(), rule, plans.Standard)

	if result.Status != "error" ||
		!strings.Contains(result.Error, "snapshot database unavailable") {
		t.Fatalf("result = %#v", result)
	}
	if notifier.message != "" {
		t.Fatalf("notification was sent without a snapshot: %q", notifier.message)
	}
	wantCalls := []string{
		"update_rule", "create_event", "create_snapshot", "update_event_failed",
	}
	if strings.Join(repository.calls, ",") != strings.Join(wantCalls, ",") {
		t.Fatalf("calls = %v, want %v", repository.calls, wantCalls)
	}
}

func TestStandardPlanDoesNotRunCombinationMembers(t *testing.T) {
	t.Parallel()
	rule := testRule(plans.BalanceChange, nil, plans.Standard)
	rule.RuleScope = "combination"
	repository := &fakeRepository{}
	executor := newTestExecutor(t, repository, &fakeChain{}, &fakeNotifier{})

	value, err := executor.CheckRule(context.Background(), rule, plans.Standard)
	if err != nil {
		t.Fatalf("CheckRule() error = %v", err)
	}
	result := value.(RuleResult)
	if result.Status != "plan_limited" || result.Reason != "combination_rule" {
		t.Fatalf("combination plan result = %#v", result)
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
		PublicAppURL:    "https://example.test",
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
