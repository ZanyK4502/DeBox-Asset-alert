package summary

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

var testNow = time.Date(2026, 7, 22, 12, 5, 0, 0, time.UTC)

type fakeRepository struct {
	subscriptions []store.Subscription
	scheduled     map[int64]*store.Subscription
	targets       map[int64][]store.DailySummaryTarget
	statistics    store.SummaryStatistics
	events        []store.SummaryEvent
	marketEvents  []store.MarketSummaryEvent
	listAfterIDs  []int64
	marks         []summaryMark
	targetMarks   []targetMark
	calls         []string
	listErr       error
	getErr        error
	targetErr     error
	statisticsErr error
	eventsErr     error
	markErr       error
	targetMarkErr error
}

type summaryMark struct {
	subscriptionID int64
	sentDate       string
	periodEnd      time.Time
}

type targetMark struct {
	subscriptionID int64
	periodEnd      time.Time
	target         store.DailySummaryTarget
}

func (f *fakeRepository) ListDueScheduledSubscriptions(
	_ context.Context,
	afterID int64,
	limit int,
) ([]store.Subscription, error) {
	f.calls = append(f.calls, "list")
	f.listAfterIDs = append(f.listAfterIDs, afterID)
	if f.listErr != nil {
		return nil, f.listErr
	}
	rows := make([]store.Subscription, 0, limit)
	for _, subscription := range f.subscriptions {
		if subscription.ID > afterID {
			rows = append(rows, subscription)
			if len(rows) == limit {
				break
			}
		}
	}
	return rows, nil
}

func (f *fakeRepository) GetScheduledSubscription(
	_ context.Context,
	subscriptionID int64,
) (*store.Subscription, error) {
	f.calls = append(f.calls, fmt.Sprintf("get:%d", subscriptionID))
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.scheduled != nil {
		return f.scheduled[subscriptionID], nil
	}
	for index := range f.subscriptions {
		if f.subscriptions[index].ID == subscriptionID {
			subscription := f.subscriptions[index]
			return &subscription, nil
		}
	}
	return nil, nil
}

func (f *fakeRepository) ListDailySummaryTargets(
	_ context.Context,
	subscriptionID int64,
) ([]store.DailySummaryTarget, error) {
	f.calls = append(f.calls, "targets")
	if f.targetErr != nil {
		return nil, f.targetErr
	}
	return f.summaryTargets(subscriptionID), nil
}

func (f *fakeRepository) ListPendingDailySummaryTargets(
	_ context.Context,
	subscriptionID int64,
	_ time.Time,
) ([]store.DailySummaryTarget, error) {
	f.calls = append(f.calls, "pending-targets")
	if f.targetErr != nil {
		return nil, f.targetErr
	}
	return f.summaryTargets(subscriptionID), nil
}

func (f *fakeRepository) MarkDailySummaryTargetSent(
	_ context.Context,
	subscriptionID int64,
	periodEnd time.Time,
	target store.DailySummaryTarget,
) error {
	f.calls = append(f.calls, "mark-target")
	if f.targetMarkErr != nil {
		return f.targetMarkErr
	}
	f.targetMarks = append(f.targetMarks, targetMark{
		subscriptionID: subscriptionID,
		periodEnd:      periodEnd,
		target:         target,
	})
	return nil
}

func (f *fakeRepository) summaryTargets(subscriptionID int64) []store.DailySummaryTarget {
	if f.targets != nil {
		return append([]store.DailySummaryTarget(nil), f.targets[subscriptionID]...)
	}
	for _, subscription := range f.subscriptions {
		if subscription.ID != subscriptionID {
			continue
		}
		chatID := subscription.DailySummaryChatID
		if subscription.DailySummaryChatType == "private" {
			chatID = subscription.DeBoxUserID
		}
		return []store.DailySummaryTarget{{
			SubscriptionID: subscriptionID,
			ChatType:       subscription.DailySummaryChatType,
			ChatID:         chatID,
		}}
	}
	return nil
}

func (f *fakeRepository) DailySummaryStatistics(
	_ context.Context,
	_ string,
	_ time.Time,
	_ time.Time,
) (store.SummaryStatistics, error) {
	f.calls = append(f.calls, "statistics")
	return f.statistics, f.statisticsErr
}

func (f *fakeRepository) ListSummaryRecentEvents(
	_ context.Context,
	_ string,
	_ time.Time,
	_ time.Time,
	limit int,
) ([]store.SummaryEvent, error) {
	f.calls = append(f.calls, fmt.Sprintf("events:%d", limit))
	return f.events, f.eventsErr
}

func (f *fakeRepository) ListSummaryRecentMarketEvents(
	_ context.Context,
	_ string,
	_ time.Time,
	_ time.Time,
	limit int,
) ([]store.MarketSummaryEvent, error) {
	f.calls = append(f.calls, fmt.Sprintf("market-events:%d", limit))
	return f.marketEvents, f.eventsErr
}

func (f *fakeRepository) MarkScheduledPushSent(
	_ context.Context,
	subscriptionID int64,
	sentDate string,
	periodEnd time.Time,
) error {
	f.calls = append(f.calls, "mark")
	if f.markErr != nil {
		return f.markErr
	}
	f.marks = append(f.marks, summaryMark{
		subscriptionID: subscriptionID,
		sentDate:       sentDate,
		periodEnd:      periodEnd,
	})
	return nil
}

type fakeNotifier struct {
	messages []sentMessage
	err      error
	calls    *[]string
}

type sentMessage struct {
	chatID   string
	chatType string
	text     string
}

func (f *fakeNotifier) SendNotification(chatID, chatType, text string) (string, error) {
	if f.calls != nil {
		*f.calls = append(*f.calls, "send")
	}
	if f.err != nil {
		return "", f.err
	}
	f.messages = append(f.messages, sentMessage{
		chatID:   chatID,
		chatType: chatType,
		text:     text,
	})
	return "message-1", nil
}

type fakeLock struct {
	subscriptionID int64
	calls          *[]string
	err            error
}

func (f *fakeLock) Unlock(context.Context) error {
	if f.calls != nil {
		*f.calls = append(*f.calls, fmt.Sprintf("unlock:%d", f.subscriptionID))
	}
	return f.err
}

func testSubscription(id int64) store.Subscription {
	return store.Subscription{
		ID:                       id,
		DeBoxUserID:              fmt.Sprintf("user-%d", id),
		PlanCode:                 "standard",
		Status:                   "active",
		DailySummaryEnabled:      1,
		DailySummaryTime:         "20:00",
		DailySummaryTimezone:     "Asia/Shanghai",
		DailySummaryChatType:     "private",
		DailySummaryChatID:       "untrusted-value",
		DailySummaryLabel:        "晚间摘要",
		DailySummaryLanguage:     "zh",
		DailySummaryLastSentDate: "",
	}
}

func newTestExecutor(
	repository *fakeRepository,
	notifier *fakeNotifier,
	tryLock TryLockFunc,
) *Executor {
	return New(Dependencies{
		Repository:    repository,
		Notifications: notifier,
		TryLock:       tryLock,
		Now: func() time.Time {
			return testNow
		},
	})
}

func acquiredLock(calls *[]string) TryLockFunc {
	return func(_ context.Context, subscriptionID int64) (Lock, bool, error) {
		if calls != nil {
			*calls = append(*calls, fmt.Sprintf("lock:%d", subscriptionID))
		}
		return &fakeLock{subscriptionID: subscriptionID, calls: calls}, true, nil
	}
}

func TestSummaryDueUsesScheduledLocalTime(t *testing.T) {
	tests := []struct {
		name       string
		timezone   string
		pushTime   string
		now        time.Time
		wantDue    bool
		wantDate   string
		wantEndUTC time.Time
	}{
		{
			name:       "Shanghai",
			timezone:   "Asia/Shanghai",
			pushTime:   "20:00",
			now:        testNow,
			wantDue:    true,
			wantDate:   "2026-07-22",
			wantEndUTC: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC),
		},
		{
			name:       "Tokyo",
			timezone:   "Asia/Tokyo",
			pushTime:   "21:00",
			now:        testNow,
			wantDue:    true,
			wantDate:   "2026-07-22",
			wantEndUTC: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC),
		},
		{
			name:       "New York",
			timezone:   "America/New_York",
			pushTime:   "08:00",
			now:        testNow,
			wantDue:    true,
			wantDate:   "2026-07-22",
			wantEndUTC: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC),
		},
		{
			name:       "before cutoff",
			timezone:   "Asia/Shanghai",
			pushTime:   "20:00",
			now:        time.Date(2026, 7, 22, 11, 59, 0, 0, time.UTC),
			wantDue:    false,
			wantDate:   "2026-07-22",
			wantEndUTC: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC),
		},
		{
			name:       "invalid values use defaults",
			timezone:   "Invalid/Zone",
			pushTime:   "99:00",
			now:        testNow,
			wantDue:    true,
			wantDate:   "2026-07-22",
			wantEndUTC: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			subscription := testSubscription(1)
			subscription.DailySummaryTimezone = test.timezone
			subscription.DailySummaryTime = test.pushTime
			due, localDate, periodEnd := summaryDue(subscription, test.now)
			if due != test.wantDue {
				t.Fatalf("due = %v, want %v", due, test.wantDue)
			}
			if localDate != test.wantDate {
				t.Fatalf("local date = %q, want %q", localDate, test.wantDate)
			}
			if !periodEnd.Equal(test.wantEndUTC) {
				t.Fatalf("period end = %s, want %s", periodEnd, test.wantEndUTC)
			}
		})
	}
}

func TestSummaryDueSkipsDateAlreadySent(t *testing.T) {
	subscription := testSubscription(1)
	subscription.DailySummaryLastSentDate = "2026-07-22"

	due, _, _ := summaryDue(subscription, testNow)

	if due {
		t.Fatal("summary should not be due twice on the same local date")
	}
}

func TestSummaryPeriodStartsAtPreviousCutoff(t *testing.T) {
	periodEnd := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	subscription := testSubscription(1)

	firstStart, _ := summaryPeriod(subscription, periodEnd)
	if !firstStart.Equal(periodEnd.Add(-24 * time.Hour)) {
		t.Fatalf("first period start = %s", firstStart)
	}

	previousEnd := periodEnd.Add(-25 * time.Hour)
	subscription.DailySummaryLastPeriodEndAt = &previousEnd
	nextStart, _ := summaryPeriod(subscription, periodEnd)
	if !nextStart.Equal(previousEnd) {
		t.Fatalf("next period start = %s, want %s", nextStart, previousEnd)
	}

	invalidPreviousEnd := periodEnd.Add(time.Minute)
	subscription.DailySummaryLastPeriodEndAt = &invalidPreviousEnd
	fallbackStart, _ := summaryPeriod(subscription, periodEnd)
	if !fallbackStart.Equal(periodEnd.Add(-24 * time.Hour)) {
		t.Fatalf("fallback period start = %s", fallbackStart)
	}
}

func TestBuildSummaryTextPreservesTotalsAndEscapesLabel(t *testing.T) {
	subscription := testSubscription(1)
	subscription.DailySummaryLanguage = "en"
	subscription.DailySummaryLabel = "Treasury <Main>"
	statistics := store.SummaryStatistics{
		RuleCount:               3,
		WalletCount:             2,
		AssetRuleCount:          1,
		ApprovalRuleCount:       1,
		InteractionRuleCount:    1,
		EventCount:              81,
		AssetEventCount:         78,
		ApprovalEventCount:      2,
		InteractionEventCount:   1,
		FailedNotificationCount: 2,
	}
	previous := "1"
	current := "2"
	events := make([]store.SummaryEvent, 0, 7)
	for index := 0; index < 7; index++ {
		events = append(events, store.SummaryEvent{
			EventType:     "balance_change",
			WalletAddress: "0x1111111111111111111111111111111111111111",
			PreviousValue: &previous,
			CurrentValue:  &current,
		})
	}
	periodEnd := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)

	text := buildSummaryText(
		subscription,
		periodEnd.Add(-24*time.Hour),
		periodEnd,
		statistics,
		events,
		nil,
	)

	for _, expected := range []string{
		"Daily Summary · Treasury &lt;Main&gt;",
		"Alerts this period: 81",
		"Notification failures: 2",
		"Monitored wallets: 2",
		"Running rules: 3",
		"Events: Assets 78, approvals 2, interactions 1",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("summary missing %q:\n%s", expected, text)
		}
	}
	if strings.Contains(strings.ToLower(text), "today") {
		t.Fatalf("summary should describe the reporting period, not today:\n%s", text)
	}
	if count := strings.Count(text, "- Balance change:"); count != 5 {
		t.Fatalf("recent event count = %d, want 5", count)
	}
}

func TestBuildChineseSummaryWithoutEvents(t *testing.T) {
	subscription := testSubscription(1)
	periodEnd := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)

	text := buildSummaryText(
		subscription,
		periodEnd.Add(-24*time.Hour),
		periodEnd,
		store.SummaryStatistics{},
		nil,
		nil,
	)

	for _, expected := range []string{
		"DeBox Asset Alert 每日摘要 · 晚间摘要",
		"本期触发次数：0",
		"异常提醒：无",
		"本期暂无触发事件。",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("summary missing %q:\n%s", expected, text)
		}
	}
}

func TestBuildUnifiedSummaryIncludesMarketStatisticsAndEscapesEvents(t *testing.T) {
	subscription := testSubscription(1)
	buyUSD := "1250.50"
	wallet := "0x1111111111111111111111111111111111111111"
	periodEnd := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)

	text := buildSummaryText(
		subscription,
		periodEnd.Add(-24*time.Hour),
		periodEnd,
		store.SummaryStatistics{
			MarketProjectCount:            2,
			MarketRuleCount:               7,
			MarketEventCount:              9,
			MarketBuyCount:                4,
			MarketSellCount:               3,
			MarketBuyUSD:                  "1250.50",
			MarketSellUSD:                 "400",
			MarketNetBuyUSD:               "850.50",
			LiquidityEventCount:           1,
			HolderEventCount:              1,
			MarketFailedNotificationCount: 2,
		},
		nil,
		[]store.MarketSummaryEvent{{
			TokenSymbol:   "<PRJ>",
			EventType:     "buy",
			WalletAddress: &wallet,
			USDValue:      &buyUSD,
		}},
	)

	for _, expected := range []string{
		"通知失败次数：2",
		"市场监控：2 个项目币，7 条运行规则",
		"买入 4 笔（$1250.5）",
		"卖出 3 笔（$400）",
		"净买入 $+850.5",
		"市场事件：共 9 条，流动性 1 条，大户变化 1 条",
		"&lt;PRJ&gt; · 买入 $1250.5",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("unified summary missing %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "<PRJ>") {
		t.Fatalf("market token symbol was not escaped:\n%s", text)
	}
}

func TestSendDuePagesAllSubscriptionsAndMarksAfterSend(t *testing.T) {
	repository := &fakeRepository{
		statistics: store.SummaryStatistics{},
	}
	for id := int64(1); id <= 205; id++ {
		repository.subscriptions = append(repository.subscriptions, testSubscription(id))
	}
	notifier := &fakeNotifier{calls: &repository.calls}
	executor := newTestExecutor(
		repository,
		notifier,
		acquiredLock(&repository.calls),
	)

	result, err := executor.SendDue(context.Background(), 100)

	if err != nil {
		t.Fatalf("SendDue() error = %v", err)
	}
	if result.Sent != 205 || result.Skipped != 0 || result.Locked != 0 ||
		len(result.Errors) != 0 {
		t.Fatalf("result = %#v", result)
	}
	if len(notifier.messages) != 205 || len(repository.marks) != 205 {
		t.Fatalf(
			"messages = %d, marks = %d",
			len(notifier.messages),
			len(repository.marks),
		)
	}
	wantAfterIDs := []int64{0, 100, 200, 205}
	if fmt.Sprint(repository.listAfterIDs) != fmt.Sprint(wantAfterIDs) {
		t.Fatalf("after IDs = %v, want %v", repository.listAfterIDs, wantAfterIDs)
	}
	for index, call := range repository.calls {
		if call == "mark-target" && (index == 0 || repository.calls[index-1] != "send") {
			t.Fatalf("target mark must immediately follow a successful send: %v", repository.calls)
		}
		if call == "mark" && (index == 0 || repository.calls[index-1] != "mark-target") {
			t.Fatalf("summary mark must follow the target mark: %v", repository.calls)
		}
	}
}

func TestPrivateSummaryUsesAuthenticatedUserID(t *testing.T) {
	subscription := testSubscription(1)
	repository := &fakeRepository{subscriptions: []store.Subscription{subscription}}
	notifier := &fakeNotifier{}
	executor := newTestExecutor(repository, notifier, acquiredLock(nil))

	result, err := executor.SendDue(context.Background(), 100)

	if err != nil || len(result.Errors) != 0 {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if len(notifier.messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(notifier.messages))
	}
	if notifier.messages[0].chatID != "user-1" ||
		notifier.messages[0].chatType != "private" {
		t.Fatalf("target = %#v", notifier.messages[0])
	}
}

func TestSummaryWithoutTargetsDoesNotSend(t *testing.T) {
	subscription := testSubscription(1)
	repository := &fakeRepository{
		subscriptions: []store.Subscription{subscription},
		targets:       map[int64][]store.DailySummaryTarget{},
	}
	notifier := &fakeNotifier{}
	executor := newTestExecutor(repository, notifier, acquiredLock(nil))

	result, err := executor.SendDue(context.Background(), 100)

	if err != nil {
		t.Fatalf("SendDue() error = %v", err)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if len(notifier.messages) != 0 || len(repository.marks) != 0 {
		t.Fatal("summary without targets must not send or advance the cursor")
	}
}

func TestSummarySendsToAllSelectedTargets(t *testing.T) {
	subscription := testSubscription(1)
	repository := &fakeRepository{
		subscriptions: []store.Subscription{subscription},
		targets: map[int64][]store.DailySummaryTarget{
			1: {
				{SubscriptionID: 1, ChatType: "private", ChatID: "user-1"},
				{SubscriptionID: 1, ChatType: "group", ChatID: "group-1"},
			},
		},
	}
	notifier := &fakeNotifier{}
	executor := newTestExecutor(repository, notifier, acquiredLock(nil))

	result, err := executor.SendDue(context.Background(), 100)

	if err != nil || len(result.Errors) != 0 || result.Sent != 1 {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if len(notifier.messages) != 2 || len(repository.targetMarks) != 2 ||
		notifier.messages[0].chatID != "user-1" ||
		notifier.messages[0].chatType != "private" ||
		notifier.messages[1].chatID != "group-1" ||
		notifier.messages[1].chatType != "group" {
		t.Fatalf("messages/marks = %#v / %#v", notifier.messages, repository.targetMarks)
	}
}

func TestLockedSubscriptionIsSkippedWithoutReadingIt(t *testing.T) {
	subscription := testSubscription(1)
	repository := &fakeRepository{subscriptions: []store.Subscription{subscription}}
	notifier := &fakeNotifier{}
	executor := newTestExecutor(
		repository,
		notifier,
		func(context.Context, int64) (Lock, bool, error) {
			return nil, false, nil
		},
	)

	result, err := executor.SendDue(context.Background(), 100)

	if err != nil {
		t.Fatalf("SendDue() error = %v", err)
	}
	if result.Locked != 1 || len(result.Errors) != 0 {
		t.Fatalf("result = %#v", result)
	}
	if len(repository.calls) != 2 ||
		repository.calls[0] != "list" ||
		repository.calls[1] != "list" {
		t.Fatalf("repository calls = %v", repository.calls)
	}
}

func TestSubscriptionIsRecheckedAfterLock(t *testing.T) {
	subscription := testSubscription(1)
	repository := &fakeRepository{
		subscriptions: []store.Subscription{subscription},
		scheduled:     map[int64]*store.Subscription{1: nil},
	}
	executor := newTestExecutor(repository, &fakeNotifier{}, acquiredLock(nil))

	result, err := executor.SendDue(context.Background(), 100)

	if err != nil || result.Skipped != 1 || len(result.Errors) != 0 {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestSendFailureDoesNotAdvanceCursorAndOtherSubscriptionsContinue(t *testing.T) {
	repository := &fakeRepository{
		subscriptions: []store.Subscription{
			testSubscription(1),
			testSubscription(2),
		},
	}
	notifier := &selectiveNotifier{}
	executor := New(Dependencies{
		Repository:    repository,
		Notifications: notifier,
		TryLock:       acquiredLock(nil),
		Now:           func() time.Time { return testNow },
	})

	result, err := executor.SendDue(context.Background(), 100)

	if err != nil {
		t.Fatalf("SendDue() error = %v", err)
	}
	if result.Sent != 1 || len(result.Errors) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if len(repository.marks) != 1 || repository.marks[0].subscriptionID != 2 {
		t.Fatalf("marks = %#v, want only subscription 2", repository.marks)
	}
}

type selectiveNotifier struct{}

func (selectiveNotifier) SendNotification(chatID, _, _ string) (string, error) {
	if chatID == "user-1" {
		return "", errors.New("send failed")
	}
	return "message-2", nil
}

func TestListFailureStopsCycle(t *testing.T) {
	repository := &fakeRepository{listErr: errors.New("database unavailable")}
	executor := newTestExecutor(repository, &fakeNotifier{}, acquiredLock(nil))

	result, err := executor.SendDue(context.Background(), 100)

	if !errors.Is(err, repository.listErr) {
		t.Fatalf("error = %v", err)
	}
	if result.Sent != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestUnlockFailureIsReportedAfterSuccessfulSend(t *testing.T) {
	subscription := testSubscription(1)
	repository := &fakeRepository{subscriptions: []store.Subscription{subscription}}
	executor := newTestExecutor(
		repository,
		&fakeNotifier{},
		func(context.Context, int64) (Lock, bool, error) {
			return &fakeLock{err: errors.New("unlock failed")}, true, nil
		},
	)

	result, err := executor.SendDue(context.Background(), 100)

	if err != nil {
		t.Fatalf("SendDue() error = %v", err)
	}
	if result.Sent != 1 || len(result.Errors) != 1 ||
		!strings.Contains(result.Errors[0].Error, "unlock failed") {
		t.Fatalf("result = %#v", result)
	}
	if len(repository.marks) != 1 {
		t.Fatal("successful summary must remain marked even if lock release reports an error")
	}
}
