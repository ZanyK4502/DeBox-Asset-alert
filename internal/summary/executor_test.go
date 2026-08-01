package summary

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationfmt"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

var testNow = time.Date(2026, 7, 22, 12, 5, 0, 0, time.UTC)

type fakeRepository struct {
	subscriptions   []store.Subscription
	scheduled       map[int64]*store.Subscription
	targets         map[int64][]store.DailySummaryTarget
	statistics      store.SummaryStatistics
	events          []store.SummaryEvent
	marketEvents    []store.MarketSummaryEvent
	marketSummaries []store.MarketProjectChainSummary
	snapshots       []store.CreateNotificationDetailSnapshotParams
	snapshotErr     error
	listAfterIDs    []int64
	marks           []summaryMark
	targetMarks     []targetMark
	calls           []string
	listErr         error
	getErr          error
	targetErr       error
	statisticsErr   error
	eventsErr       error
	markErr         error
	targetMarkErr   error
}

func (f *fakeRepository) CreateNotificationDetailSnapshot(
	_ context.Context,
	params store.CreateNotificationDetailSnapshotParams,
) (store.NotificationDetailSnapshot, error) {
	f.calls = append(f.calls, "snapshot")
	if f.snapshotErr != nil {
		return store.NotificationDetailSnapshot{}, f.snapshotErr
	}
	f.snapshots = append(f.snapshots, params)
	return store.NotificationDetailSnapshot{
		ID:               int64(len(f.snapshots)),
		PublicID:         "nd_1123456789abcdef0123456789abcdef01234567",
		SourceKey:        params.SourceKey,
		NotificationKind: params.NotificationKind,
		Details:          params.Details,
	}, nil
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

func (f *fakeRepository) ListDailyMarketProjectChainSummaries(
	_ context.Context,
	_ string,
	_ time.Time,
	_ time.Time,
) ([]store.MarketProjectChainSummary, error) {
	f.calls = append(f.calls, "market-summaries")
	return f.marketSummaries, f.eventsErr
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
	actions  []sentAction
	err      error
	calls    *[]string
}

type sentMessage struct {
	chatID   string
	chatType string
	text     string
}

type sentAction struct {
	chatID     string
	chatType   string
	text       string
	actionText string
	actionURL  string
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

func (f *fakeNotifier) SendNotificationWithAction(
	chatID, chatType, text, actionText, actionURL string,
) (string, error) {
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
	f.actions = append(f.actions, sentAction{
		chatID:     chatID,
		chatType:   chatType,
		text:       text,
		actionText: actionText,
		actionURL:  actionURL,
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
		EventCount:              81,
		AddressRiskEventCount:   1,
		AddressOutgoingCount:    1,
		FailedNotificationCount: 2,
	}
	events := []store.SummaryEvent{{
		EventType:     "outgoing",
		WalletAddress: "0x1111111111111111111111111111111111111111",
	}}
	periodEnd := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)

	text := buildSummaryText(
		subscription,
		periodEnd.Add(-24*time.Hour),
		periodEnd,
		statistics,
		events,
		nil,
		nil,
	)

	for _, expected := range []string{
		"Daily Summary · Treasury &lt;Main&gt;",
		"Today&#39;s conclusion",
		"2 notification deliveries need attention",
		"wallet 0x1111",
		"Risk overview",
		"2 wallets, 3 active rules",
		"Period",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("summary missing %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "<Main>") {
		t.Fatalf("summary label was not escaped:\n%s", text)
	}
	if lines := strings.Count(text, notificationfmt.BlockBreak) + 1; lines > 10 {
		t.Fatalf("summary lines = %d, want at most 10:\n%s", lines, text)
	}
	for _, omitted := range []string{
		"Asset rules",
		"Recent address events",
		"81 alerts",
	} {
		if strings.Contains(text, omitted) {
			t.Fatalf("summary should omit verbose detail %q:\n%s", omitted, text)
		}
	}
}

func TestBuildChineseSummaryWithoutEvents(t *testing.T) {
	subscription := testSubscription(1)
	subscription.DailySummaryLabel = "晚间摘要"
	periodEnd := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)

	text := buildSummaryText(
		subscription,
		periodEnd.Add(-24*time.Hour),
		periodEnd,
		store.SummaryStatistics{
			RuleCount:   2,
			WalletCount: 1,
		},
		nil,
		nil,
		nil,
	)

	for _, expected := range []string{
		"每日摘要 · 晚间摘要",
		"今日监控正常，无需处理",
		"1 个钱包，2 条运行规则",
		"统计周期",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("summary missing %q:\n%s", expected, text)
		}
	}
	if lines := strings.Count(text, notificationfmt.BlockBreak) + 1; lines > 6 {
		t.Fatalf("normal summary lines = %d, want at most 6:\n%s", lines, text)
	}
	for _, omitted := range []string{"0 次", "0 条", "最近", "风险概览"} {
		if strings.Contains(text, omitted) {
			t.Fatalf("normal summary should omit %q:\n%s", omitted, text)
		}
	}
}

func TestBuildSummaryDoesNotCallEmptyConfigurationNormal(t *testing.T) {
	subscription := testSubscription(1)
	subscription.DailySummaryLanguage = "en"
	subscription.DailySummaryLabel = ""
	periodEnd := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)

	text := buildSummaryText(
		subscription,
		periodEnd.Add(-24*time.Hour),
		periodEnd,
		store.SummaryStatistics{},
		nil,
		nil,
		nil,
	)

	if !strings.Contains(text, "No monitoring items are enabled") ||
		!strings.Contains(text, "No active monitoring items") ||
		strings.Contains(text, "normal") {
		t.Fatalf("empty monitoring summary is misleading:\n%s", text)
	}
	for _, zeroValue := range []string{"0 wallet", "0 rule", "0 event", "$0"} {
		if strings.Contains(text, zeroValue) {
			t.Fatalf("summary should hide zero value %q:\n%s", zeroValue, text)
		}
	}
	if lines := strings.Count(text, notificationfmt.BlockBreak) + 1; lines > 6 {
		t.Fatalf("empty summary lines = %d, want at most 6:\n%s", lines, text)
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
			MarketAnomalyCount:            3,
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
			TokenSymbol:     "<PRJ>",
			ChainKey:        "base",
			Protocol:        "uniswap",
			ProtocolVersion: "v3",
			PoolAddress:     pointerString("0x2222222222222222222222222222222222222222"),
			EventType:       "buy",
			WalletAddress:   &wallet,
			USDValue:        &buyUSD,
		}},
		[]store.MarketProjectChainSummary{
			{
				MarketProjectID:      1,
				TokenSymbol:          "<PRJ>",
				ChainKey:             "base",
				SnapshotCount:        4,
				PriceSampleCount:     4,
				LiquiditySampleCount: 4,
				VolumeSampleCount:    4,
				StartPriceUSD:        pointerString("1"),
				EndPriceUSD:          pointerString("1.1"),
			},
			{
				MarketProjectID:      2,
				TokenSymbol:          "PRJ2",
				ChainKey:             "bsc",
				SnapshotCount:        4,
				PriceSampleCount:     4,
				LiquiditySampleCount: 4,
				VolumeSampleCount:    4,
				StartPriceUSD:        pointerString("1"),
				EndPriceUSD:          pointerString("1.02"),
			},
		},
	)

	for _, expected := range []string{
		"有 2 条通知发送失败",
		"<b>最大价格波动</b>：&lt;PRJ&gt;（Base）+10.00%",
		"<b>最大成交</b>：&lt;PRJ&gt; 买入 $1,250.5",
		"市场异常 3",
		"净流入 $850.5（买入 4 笔，卖出 3 笔）",
		"2 个代币项目，7 条运行规则",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("unified summary missing %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "<PRJ>") {
		t.Fatalf("market token symbol was not escaped:\n%s", text)
	}
	for _, privateDetail := range []string{
		wallet,
		"0x1111",
		"0x2222",
		"uniswap",
	} {
		if strings.Contains(text, privateDetail) {
			t.Fatalf("digest leaked market detail %q:\n%s", privateDetail, text)
		}
	}
	if lines := strings.Count(text, notificationfmt.BlockBreak) + 1; lines > 10 {
		t.Fatalf("summary lines = %d, want at most 10:\n%s", lines, text)
	}
}

func pointerString(value string) *string {
	return &value
}

func TestBuildSummaryReportsPartialMarketCoverageWithoutRawIdentifiers(t *testing.T) {
	subscription := testSubscription(1)
	subscription.DailySummaryChatType = "group"
	subscription.DailySummaryLabel = "群监控"
	startPrice := "1"
	endPrice := "1.25"
	periodEnd := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)

	text := buildSummaryText(
		subscription,
		periodEnd.Add(-24*time.Hour),
		periodEnd,
		store.SummaryStatistics{
			MarketProjectCount: 1,
			MarketRuleCount:    4,
			MarketEventCount:   1,
		},
		nil,
		nil,
		[]store.MarketProjectChainSummary{
			{
				MarketProjectID:      1,
				TokenName:            "ABC",
				TokenSymbol:          "ABC",
				ChainKey:             "bsc",
				TokenAddress:         "0x1111111111111111111111111111111111111111",
				StartPriceUSD:        &startPrice,
				EndPriceUSD:          &endPrice,
				SnapshotCount:        4,
				PriceSampleCount:     4,
				LiquiditySampleCount: 4,
				VolumeSampleCount:    4,
				TradeVolumeUSD:       "15000.5",
				BuyCount:             4,
				SellCount:            2,
				LargeTradeCount:      2,
				HolderIncreaseCount:  1,
			},
			{
				MarketProjectID:     1,
				TokenName:           "ABC",
				TokenSymbol:         "ABC",
				ChainKey:            "base",
				TokenAddress:        "0x2222222222222222222222222222222222222222",
				TradeVolumeUSD:      "300",
				BuyCount:            1,
				SellCount:           1,
				HolderDecreaseCount: 1,
			},
		},
	)

	for _, expected := range []string{
		"群组每日摘要 · 群监控",
		"<b>最大价格波动</b>：ABC（BNB Chain）+25.00%",
		"缺少价格、流动性、成交量",
		"影响涨跌与价格规则、流动性规则、成交量规则",
		"1 个代币项目，4 条运行规则",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("multichain summary missing %q:\n%s", expected, text)
		}
	}
	for _, rawIdentifier := range []string{"0x1111", "0x2222"} {
		if strings.Contains(text, rawIdentifier) {
			t.Fatalf("group digest leaked identifier %q:\n%s", rawIdentifier, text)
		}
	}
}

func TestBuildEnglishGroupSummaryHidesWalletAndUsesEnglishOnly(t *testing.T) {
	subscription := testSubscription(1)
	subscription.DailySummaryLanguage = "en"
	subscription.DailySummaryChatType = "group"
	subscription.DailySummaryLabel = "Risk desk"
	startPrice, endPrice := "2", "1.5"
	periodEnd := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	text := buildSummaryText(
		subscription,
		periodEnd.Add(-24*time.Hour),
		periodEnd,
		store.SummaryStatistics{
			RuleCount:             1,
			WalletCount:           1,
			EventCount:            1,
			AddressRiskEventCount: 1,
			MarketProjectCount:    1,
			MarketRuleCount:       1,
			MarketEventCount:      1,
		},
		[]store.SummaryEvent{{
			EventType:     "outgoing",
			WalletAddress: "0x1111111111111111111111111111111111111111",
		}},
		nil,
		[]store.MarketProjectChainSummary{{
			MarketProjectID:      1,
			TokenName:            "<ABC>",
			ChainKey:             "ethereum",
			TokenAddress:         "0x1111111111111111111111111111111111111111",
			StartPriceUSD:        &startPrice,
			EndPriceUSD:          &endPrice,
			SnapshotCount:        3,
			PriceSampleCount:     3,
			LiquiditySampleCount: 3,
			VolumeSampleCount:    3,
		}},
	)
	for _, expected := range []string{
		"Group Daily Summary · Risk desk",
		"Address monitoring detected 1 risk event",
		"wallet details are hidden",
		"<b>Largest price move</b>: &lt;ABC&gt; on Ethereum, -25.00%",
		"1 wallet, 1 token project, 2 active rules",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("English multichain summary missing %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "<ABC>") {
		t.Fatalf("English project name was not escaped:\n%s", text)
	}
	for _, address := range []string{
		"0x1111111111111111111111111111111111111111",
		"0x1111",
	} {
		if strings.Contains(text, address) {
			t.Fatalf("group summary leaked wallet %q:\n%s", address, text)
		}
	}
	for _, character := range text {
		if unicode.Is(unicode.Han, character) {
			t.Fatalf("English summary contains Chinese text %q:\n%s", character, text)
		}
	}
}

func TestChinesePrivateSummaryMayIdentifyWalletButGroupSummaryDoesNot(t *testing.T) {
	wallet := "0x1234567890123456789012345678901234567890"
	periodEnd := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	statistics := store.SummaryStatistics{
		RuleCount:             1,
		WalletCount:           1,
		EventCount:            1,
		AddressRiskEventCount: 1,
		AddressOutgoingCount:  1,
	}
	events := []store.SummaryEvent{{
		EventType:     "outgoing",
		WalletAddress: wallet,
	}}
	privateSubscription := testSubscription(1)
	privateSubscription.DailySummaryLabel = "个人监控"
	privateText := buildSummaryText(
		privateSubscription,
		periodEnd.Add(-24*time.Hour),
		periodEnd,
		statistics,
		events,
		nil,
		nil,
	)
	shortWallet := shortAddress(wallet)
	if !strings.Contains(privateText, shortWallet) {
		t.Fatalf("private summary missing shortened wallet %q:\n%s", shortWallet, privateText)
	}

	groupSubscription := privateSubscription
	groupSubscription.DailySummaryChatType = "group"
	groupSubscription.DailySummaryLabel = "群监控"
	groupText := buildSummaryText(
		groupSubscription,
		periodEnd.Add(-24*time.Hour),
		periodEnd,
		statistics,
		events,
		nil,
		nil,
	)
	if !strings.Contains(groupText, "群组摘要已隐藏钱包信息") {
		t.Fatalf("group privacy notice missing:\n%s", groupText)
	}
	for _, identifier := range []string{wallet, shortWallet} {
		if strings.Contains(groupText, identifier) {
			t.Fatalf("group summary leaked wallet %q:\n%s", identifier, groupText)
		}
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
	if len(repository.snapshots) != 205 {
		t.Fatalf("snapshots = %d, want 205", len(repository.snapshots))
	}
	wantAfterIDs := []int64{0, 100, 200, 205}
	if fmt.Sprint(repository.listAfterIDs) != fmt.Sprint(wantAfterIDs) {
		t.Fatalf("after IDs = %v, want %v", repository.listAfterIDs, wantAfterIDs)
	}
	for index, call := range repository.calls {
		if call == "send" && (index == 0 || repository.calls[index-1] != "snapshot") {
			t.Fatalf("snapshot must immediately precede a send: %v", repository.calls)
		}
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
		statistics: store.SummaryStatistics{
			EventCount: 2, MarketEventCount: 3, MarketAnomalyCount: 1,
		},
		events:       []store.SummaryEvent{{ID: 11, EventType: "incoming"}},
		marketEvents: []store.MarketSummaryEvent{{ID: 12, EventType: "buy"}},
		marketSummaries: []store.MarketProjectChainSummary{{
			MarketProjectID: 13, TokenSymbol: "TEST", ChainKey: "bsc",
		}},
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
	if len(repository.snapshots) != 2 {
		t.Fatalf("snapshots = %#v", repository.snapshots)
	}
	for index, snapshot := range repository.snapshots {
		if snapshot.NotificationKind != store.NotificationKindDailySummary ||
			snapshot.SourceType != "daily_summary_target" ||
			snapshot.DeBoxUserID != "user-1" ||
			snapshot.NotificationChatID != notifier.messages[index].chatID ||
			snapshot.NotificationChatType != notifier.messages[index].chatType ||
			snapshot.NotificationText != notifier.messages[index].text ||
			snapshot.ActualValue != "address_events=2;market_events=3;market_anomalies=1" ||
			!strings.Contains(string(snapshot.Details), `"schema_version":1`) ||
			!strings.Contains(string(snapshot.Details), `"address_events":[{"id":11`) ||
			!strings.Contains(string(snapshot.Details), `"market_events":[{"id":12`) ||
			!strings.Contains(string(snapshot.Details), `"market_project_chain_summaries":[{"market_project_id":13`) {
			t.Fatalf("snapshot[%d] = %#v", index, snapshot)
		}
	}
}

func TestSummarySnapshotFailurePreventsSendAndCursorAdvance(t *testing.T) {
	t.Parallel()
	subscription := testSubscription(1)
	repository := &fakeRepository{
		subscriptions: []store.Subscription{subscription},
		snapshotErr:   errors.New("snapshot database unavailable"),
	}
	notifier := &fakeNotifier{}
	executor := newTestExecutor(repository, notifier, acquiredLock(nil))

	result, err := executor.SendDue(context.Background(), 100)

	if err != nil {
		t.Fatalf("SendDue() error = %v", err)
	}
	if len(result.Errors) != 1 ||
		!strings.Contains(result.Errors[0].Error, "snapshot database unavailable") {
		t.Fatalf("result = %#v", result)
	}
	if len(notifier.messages) != 0 || len(repository.targetMarks) != 0 ||
		len(repository.marks) != 0 {
		t.Fatalf(
			"summary advanced without a snapshot: messages=%d targets=%d marks=%d",
			len(notifier.messages), len(repository.targetMarks), len(repository.marks),
		)
	}
}

func TestSummaryUsesFullReportActionWhenPublicAppURLIsConfigured(t *testing.T) {
	subscription := testSubscription(1)
	subscription.DailySummaryLanguage = "en"
	repository := &fakeRepository{
		subscriptions: []store.Subscription{subscription},
	}
	notifier := &fakeNotifier{}
	executor := New(Dependencies{
		Repository:    repository,
		Notifications: notifier,
		PublicAppURL:  "https://alerts.example/app#old",
		TryLock:       acquiredLock(nil),
		Now:           func() time.Time { return testNow },
	})

	result, err := executor.SendDue(context.Background(), 100)

	if err != nil || result.Sent != 1 || len(result.Errors) != 0 {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if len(notifier.actions) != 1 {
		t.Fatalf("actions = %#v", notifier.actions)
	}
	action := notifier.actions[0]
	if action.actionText != "View full report" ||
		action.actionURL != "https://alerts.example/app?notification_id=nd_1123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("action = %#v", action)
	}
}

func TestSummaryTargetsUseIndependentSchedulesAndLanguages(t *testing.T) {
	subscription := testSubscription(1)
	repository := &fakeRepository{
		subscriptions: []store.Subscription{subscription},
		targets: map[int64][]store.DailySummaryTarget{
			1: {
				{
					SubscriptionID: 1,
					ChatType:       "private",
					ChatID:         "user-1",
					Enabled:        1,
					PushTime:       "20:00",
					Timezone:       "Asia/Shanghai",
					Label:          "早间摘要",
					Language:       "zh",
				},
				{
					SubscriptionID: 1,
					ChatType:       "group",
					ChatID:         "group-1",
					Enabled:        1,
					PushTime:       "08:00",
					Timezone:       "America/New_York",
					Label:          "New York close",
					Language:       "en",
				},
			},
		},
	}
	notifier := &fakeNotifier{}
	executor := newTestExecutor(repository, notifier, acquiredLock(nil))

	result, err := executor.SendDue(context.Background(), 100)

	if err != nil || len(result.Errors) != 0 || result.Sent != 1 {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if len(notifier.messages) != 2 {
		t.Fatalf("messages = %#v", notifier.messages)
	}
	if !strings.Contains(notifier.messages[0].text, "每日摘要 · 早间摘要") ||
		!strings.Contains(notifier.messages[0].text, "Asia/Shanghai") {
		t.Fatalf("private summary = %s", notifier.messages[0].text)
	}
	if !strings.Contains(notifier.messages[1].text, "Group Daily Summary · New York close") ||
		!strings.Contains(notifier.messages[1].text, "America/New_York") {
		t.Fatalf("group summary = %s", notifier.messages[1].text)
	}
}

func TestSummaryTargetsAreEvaluatedAtTheirOwnLocalCutoff(t *testing.T) {
	subscription := testSubscription(1)
	repository := &fakeRepository{
		subscriptions: []store.Subscription{subscription},
		targets: map[int64][]store.DailySummaryTarget{
			1: {
				{
					SubscriptionID: 1,
					ChatType:       "private",
					ChatID:         "user-1",
					Enabled:        1,
					PushTime:       "20:00",
					Timezone:       "Asia/Shanghai",
					Language:       "zh",
				},
				{
					SubscriptionID: 1,
					ChatType:       "group",
					ChatID:         "group-1",
					Enabled:        1,
					PushTime:       "09:00",
					Timezone:       "America/New_York",
					Language:       "en",
				},
			},
		},
	}
	notifier := &fakeNotifier{}
	executor := newTestExecutor(repository, notifier, acquiredLock(nil))

	result, err := executor.SendDue(context.Background(), 100)

	if err != nil || len(result.Errors) != 0 || result.Sent != 1 {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if len(notifier.messages) != 1 ||
		notifier.messages[0].chatType != "private" ||
		notifier.messages[0].chatID != "user-1" {
		t.Fatalf("messages = %#v", notifier.messages)
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
