package summary

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func countSummaryReads(repository *fakeRepository) int {
	count := 0
	for _, call := range repository.calls {
		if call == "statistics" {
			count++
		}
	}
	return count
}

func TestDailySummaryRetryKeepsFrozenBodyAndButtonWithoutReloadingData(t *testing.T) {
	now := testNow
	repository := &fakeRepository{subscriptions: []store.Subscription{testSubscription(1)}, statistics: store.SummaryStatistics{EventCount: 1}}
	notifier := &fakeNotifier{err: &notificationdelivery.Failure{Kind: notificationdelivery.Retryable, Code: "explicit_rate_limit"}}
	executor := New(Dependencies{Repository: repository, Notifications: notifier, TryLock: acquiredLock(nil), Now: func() time.Time { return now }, PublicAppURL: "https://original.example/app"})
	if result, err := executor.SendDue(context.Background(), 100); err != nil || len(result.Errors) != 1 {
		t.Fatalf("first attempt=%+v %v", result, err)
	}
	dispatch := repository.dispatches[1]
	original := dispatch.Payload
	if dispatch.Status != "failed" || dispatch.RetryAt == nil || dispatch.Attempts != 1 {
		t.Fatalf("retry evidence=%+v", dispatch)
	}
	now = now.Add(31 * time.Second)
	notifier.err = nil
	repository.statistics.EventCount = 999
	executor.deps.PublicAppURL = "https://changed.example/app"
	if result, err := executor.SendDue(context.Background(), 100); err != nil || result.Sent != 1 || len(result.Errors) != 0 {
		t.Fatalf("retry=%+v %v", result, err)
	}
	if countSummaryReads(repository) != 1 || len(repository.snapshots) != 1 || len(notifier.messages) != 1 || notifier.messages[0].text != original.Text || len(notifier.actions) != 1 || notifier.actions[0].actionURL != original.ActionURL || dispatch.Attempts != 2 {
		t.Fatalf("retry changed frozen data: reads%d snapshots%d messages%+v", countSummaryReads(repository), len(repository.snapshots), notifier.messages)
	}
}

func TestDailySummaryUnknownBlocksLaterCutoffAndDoesNotReloadOrResend(t *testing.T) {
	now := testNow
	repository := &fakeRepository{subscriptions: []store.Subscription{testSubscription(1)}}
	notifier := &fakeNotifier{err: errors.New("transport result missing")}
	executor := New(Dependencies{Repository: repository, Notifications: notifier, TryLock: acquiredLock(nil), Now: func() time.Time { return now }})
	if _, err := executor.SendDue(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if repository.dispatches[1].Status != "unknown" {
		t.Fatalf("unclassified failure was assumed unsent: %+v", repository.dispatches[1])
	}
	now = now.Add(48 * time.Hour)
	notifier.err = nil
	if result, err := executor.SendDue(context.Background(), 100); err != nil || result.Sent != 0 || len(result.Errors) != 0 {
		t.Fatalf("unknown cycle=%+v %v", result, err)
	}
	if len(repository.dispatches) != 1 || len(repository.snapshots) != 1 || len(notifier.messages) != 0 || countSummaryReads(repository) != 1 || len(repository.targetMarks) != 0 {
		t.Fatal("unknown result created/replaced/retried a report or moved its cutoff")
	}
}

func TestDailySummaryKnownUnsentIsSupersededThenMergedFromLastConfirmedCutoff(t *testing.T) {
	now := testNow
	last := now.Add(-4 * 24 * time.Hour)
	repository := &fakeRepository{subscriptions: []store.Subscription{testSubscription(1)}, statistics: store.SummaryStatistics{EventCount: 1}, targets: map[int64][]store.DailySummaryTarget{1: {{ID: 41, SubscriptionID: 1, ChatType: "private", ChatID: "user-1", Enabled: 1, PushTime: "20:00", Timezone: "Asia/Shanghai", Language: "en", LastPeriodEndAt: &last}}}}
	notifier := &fakeNotifier{err: &notificationdelivery.Failure{Kind: notificationdelivery.Retryable, Code: "explicit_not_sent"}}
	executor := New(Dependencies{Repository: repository, Notifications: notifier, TryLock: acquiredLock(nil), Now: func() time.Time { return now }})
	if _, err := executor.SendDue(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	original := repository.dispatches[1].Payload.Text
	now = now.Add(24 * time.Hour)
	notifier.err = nil
	repository.statistics.EventCount = 5
	if result, err := executor.SendDue(context.Background(), 100); err != nil || result.Sent != 1 || len(result.Errors) != 0 {
		t.Fatalf("new cutoff=%+v %v", result, err)
	}
	old, newer := repository.dispatches[1], repository.dispatches[2]
	if old.Status != "superseded" || old.Payload.Text != original || newer.Status != "sent" || !newer.PeriodStartAt.Equal(last) || !newer.PeriodEndAt.Equal(time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)) || len(notifier.messages) != 1 {
		t.Fatalf("merge mutated old report or lost cutoff: old%+v new%+v", old, newer)
	}
	if !strings.Contains(newer.Payload.Text, "Combined report") || !strings.Contains(newer.Payload.Text, "5 events") {
		t.Fatalf("merged body=%s", newer.Payload.Text)
	}
}

func TestDailySummaryKnownSuccessRecordFailureBecomesUnknownAndNeverResends(t *testing.T) {
	now := testNow
	repository := &fakeRepository{subscriptions: []store.Subscription{testSubscription(1)}, targetMarkErr: errors.New("result database unavailable")}
	notifier := &fakeNotifier{}
	executor := New(Dependencies{Repository: repository, Notifications: notifier, TryLock: acquiredLock(nil), Now: func() time.Time { return now }})
	result, err := executor.SendDue(context.Background(), 100)
	if err != nil || len(result.Errors) != 1 || len(notifier.messages) != 1 || repository.dispatches[1].Status != "unknown" || len(repository.targetMarks) != 0 {
		t.Fatalf("known send record failure=%+v %v", result, err)
	}
	if !strings.Contains(result.Errors[0].Error, "dispatch_id=1") || !strings.Contains(result.Errors[0].Error, `accepted_message_id="message-1"`) || repository.dispatches[1].MessageID == nil || *repository.dispatches[1].MessageID != "message-1" {
		t.Fatalf("accepted SDK evidence was lost: %+v %s", repository.dispatches[1], result.Errors[0].Error)
	}
	repository.targetMarkErr = nil
	now = now.Add(24 * time.Hour)
	if _, err := executor.SendDue(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if len(notifier.messages) != 1 || len(repository.dispatches) != 1 {
		t.Fatal("known external send was repeated after local record failure")
	}
}

func TestDailySummaryExpiredSendingClaimBecomesUnknownWithoutAnotherAttempt(t *testing.T) {
	now := testNow
	repository := &fakeRepository{subscriptions: []store.Subscription{testSubscription(1)}}
	notifier := &fakeNotifier{}
	executor := New(Dependencies{Repository: repository, Notifications: notifier, TryLock: acquiredLock(nil), Now: func() time.Time { return now }})
	targets, _ := repository.ListDailySummaryDispatchTargets(context.Background(), 1, 0, 100)
	target := effectiveSummaryTarget(repository.subscriptions[0], targets[0])
	_, date, end := summaryTargetDue(target, now)
	from, _ := summaryTargetPeriod(target, end)
	params, err := dailySummarySnapshotParams(repository.subscriptions[0], target, from, end, "frozen before crash", dailySummaryContent{})
	if err != nil {
		t.Fatal(err)
	}
	d, err := repository.PrepareDailySummaryDispatch(context.Background(), store.PrepareDailySummaryDispatchParams{SubscriptionID: 1, TargetID: target.ID, PeriodStartAt: from, PeriodEndAt: end, LocalDate: date, Snapshot: params, BuildPayload: executor.frozenSummaryPayload})
	if err != nil {
		t.Fatal(err)
	}
	if claim, err := repository.ClaimDailySummaryDispatch(context.Background(), d.ID, now); err != nil || claim == nil {
		t.Fatalf("pre-crash claim=%+v %v", claim, err)
	}
	now = now.Add(store.DailySummaryClaimLease + time.Second)
	if _, err := executor.SendDue(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if repository.dispatches[1].Status != "unknown" || repository.dispatches[1].Attempts != 1 || len(notifier.messages) != 0 || countSummaryReads(repository) != 0 {
		t.Fatal("expired in-flight attempt was replayed")
	}
}

func TestDailySummaryTargetFailureDoesNotPreventOtherTargetOnSameSubscription(t *testing.T) {
	repository := &fakeRepository{subscriptions: []store.Subscription{testSubscription(1)}, targets: map[int64][]store.DailySummaryTarget{1: {{ChatType: "private", ChatID: "user-1"}, {ChatType: "group", ChatID: "group-1"}}}}
	executor := New(Dependencies{Repository: repository, Notifications: selectiveNotifier{}, TryLock: acquiredLock(nil), Now: func() time.Time { return testNow }})
	result, err := executor.SendDue(context.Background(), 100)
	if err != nil || result.Sent != 1 || len(result.Errors) != 1 || len(repository.targetMarks) != 1 || repository.targetMarks[0].target.ChatID != "group-1" || repository.dispatches[1].Status != "unknown" || repository.dispatches[2].Status != "sent" {
		t.Fatalf("same-sub target isolation=%+v %v marks%+v", result, err, repository.targetMarks)
	}
}

func TestDailySummaryCycleHasBoundedFairSubscriptionAndTargetPaging(t *testing.T) {
	t.Run("subscriptions", func(t *testing.T) {
		repository := &fakeRepository{}
		for id := int64(1); id <= 1005; id++ {
			repository.subscriptions = append(repository.subscriptions, testSubscription(id))
		}
		notifier := &fakeNotifier{}
		executor := newTestExecutor(repository, notifier, acquiredLock(nil))
		if result, err := executor.SendDue(context.Background(), 100); err != nil || result.Sent != 1000 {
			t.Fatalf("first bounded page=%+v %v", result, err)
		}
		if result, err := executor.SendDue(context.Background(), 100); err != nil || result.Sent != 5 || len(notifier.messages) != 1005 {
			t.Fatalf("remaining subscriptions starved=%+v %v count%d", result, err, len(notifier.messages))
		}
	})
	t.Run("targets", func(t *testing.T) {
		repository := &fakeRepository{subscriptions: []store.Subscription{testSubscription(1)}, targets: map[int64][]store.DailySummaryTarget{1: {}}}
		for i := 1; i <= 1005; i++ {
			repository.targets[1] = append(repository.targets[1], store.DailySummaryTarget{ID: int64(i), SubscriptionID: 1, ChatType: "group", ChatID: time.Unix(int64(i), 0).String()})
		}
		notifier := &fakeNotifier{}
		executor := newTestExecutor(repository, notifier, acquiredLock(nil))
		if _, err := executor.SendDue(context.Background(), 100); err != nil || len(notifier.messages) != 1000 {
			t.Fatalf("target budget=%d %v", len(notifier.messages), err)
		}
		if _, err := executor.SendDue(context.Background(), 100); err != nil || len(notifier.messages) != 1005 {
			t.Fatalf("remaining targets starved=%d %v", len(notifier.messages), err)
		}
	})
}

func TestDailySummaryRecoveryGapDoesNotDescribeUncollectedActivityAsNormal(t *testing.T) {
	for _, language := range []string{"zh", "en"} {
		subscription := testSubscription(1)
		subscription.DailySummaryLanguage = language
		content := dailySummaryContent{Statistics: store.SummaryStatistics{MarketProjectCount: 1}, RecoveryCoverage: &store.SummaryRecoveryCoverage{Status: "incomplete", Scope: "configured_collection", Chains: []store.SummaryChainCoverage{{ChainID: 56, ChainKey: "bsc", Status: "unknown"}}}}
		text := buildDailySummaryReport(subscription, testNow.Add(-3*24*time.Hour), testNow, content)
		if !strings.Contains(text, dailySummaryLabel("合并日报", "Combined report", language == "en")) || !strings.Contains(text, dailySummaryLabel("零条记录不代表链上没有活动", "zero recorded events does not prove there was no on-chain activity", language == "en")) || strings.Contains(text, dailySummaryLabel("今日监控正常，无需处理", "All monitored items were normal today", language == "en")) {
			t.Fatalf("coverage language%s: %s", language, text)
		}
	}
}

func TestDailySummaryDueDoesNotMoveBackwardAfterScheduleChange(t *testing.T) {
	future := testNow.Add(time.Hour)
	if due, _, _ := summaryTargetDue(store.DailySummaryTarget{PushTime: "20:00", Timezone: "Asia/Shanghai", LastPeriodEndAt: &future}, testNow); due {
		t.Fatal("schedule reset allowed a cutoff older than the last confirmed success")
	}
}

func TestDailySummaryPreflightDatabaseFailureDoesNotBurnAttemptOrQuarantine(t *testing.T) {
	repository := &fakeRepository{subscriptions: []store.Subscription{testSubscription(1)}, claimErr: errors.New("preflight database unavailable")}
	notifier := &fakeNotifier{}
	executor := newTestExecutor(repository, notifier, acquiredLock(nil))
	if _, err := executor.SendDue(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if repository.dispatches[1].Status != "pending" || repository.dispatches[1].Attempts != 0 || len(notifier.messages) != 0 {
		t.Fatal("preflight DB error consumed an attempt or became uncertain delivery")
	}
	repository.claimErr = nil
	if result, err := executor.SendDue(context.Background(), 100); err != nil || result.Sent != 1 || repository.dispatches[1].Attempts != 1 || len(notifier.messages) != 1 {
		t.Fatalf("preflight recovery=%+v %v", result, err)
	}
}

func TestDailySummaryFinalizationRetriesDatabaseOnceWithoutResendingSDK(t *testing.T) {
	repository := &fakeRepository{subscriptions: []store.Subscription{testSubscription(1)}, completeFailOnce: true}
	notifier := &fakeNotifier{}
	executor := newTestExecutor(repository, notifier, acquiredLock(nil))
	if result, err := executor.SendDue(context.Background(), 100); err != nil || result.Sent != 1 || len(result.Errors) != 0 || len(notifier.messages) != 1 || repository.dispatches[1].Status != "sent" || repository.dispatches[1].Attempts != 1 {
		t.Fatalf("finalization retry resent SDK or lost success: %+v %v", result, err)
	}
}

func TestDailySummaryMergedReportRetainsFutureProviderRetryAfter(t *testing.T) {
	now := testNow
	repository := &fakeRepository{subscriptions: []store.Subscription{testSubscription(1)}}
	notifier := &fakeNotifier{err: &notificationdelivery.Failure{Kind: notificationdelivery.Retryable, Code: "explicit_rate_limit", RequestID: "provider-request", RetryAfter: 72 * time.Hour}}
	executor := New(Dependencies{Repository: repository, Notifications: notifier, TryLock: acquiredLock(nil), Now: func() time.Time { return now }})
	if _, err := executor.SendDue(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	oldWait := *repository.dispatches[1].RetryAt
	if !strings.Contains(repository.dispatches[1].LastError, "provider-request") {
		t.Fatal("provider request trace was discarded")
	}
	now = now.Add(24 * time.Hour)
	notifier.err = nil
	if result, err := executor.SendDue(context.Background(), 100); err != nil || result.Sent != 0 {
		t.Fatalf("new cutoff bypassed provider wait: %+v %v", result, err)
	}
	if repository.dispatches[1].Status != "superseded" || repository.dispatches[1].RetryAt == nil || !repository.dispatches[1].RetryAt.Equal(oldWait) || repository.dispatches[2].RetryAt == nil || !repository.dispatches[2].RetryAt.Equal(oldWait) || repository.dispatches[2].Attempts != 0 || len(notifier.messages) != 0 {
		t.Fatal("new merged dispatch reset explicit RetryAfter")
	}
}
