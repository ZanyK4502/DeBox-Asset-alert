package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func (f *fakeNotifier) SendNotificationContext(_ context.Context, chatID, chatType, text string) (string, error) {
	return f.SendNotification(chatID, chatType, text)
}
func (f *fakeNotifier) SendNotificationWithActionContext(_ context.Context, chatID, chatType, text, actionText, actionURL string) (string, error) {
	return f.SendNotificationWithAction(chatID, chatType, text, actionText, actionURL)
}

// This fake models one committed observation; PostgreSQL tests exercise the
// actual rollback, row locks, CAS, quota and immutable source constraints.
func (f *fakeRepository) DeferWalletObservation(context.Context, int64, time.Duration) error {
	f.calls = append(f.calls, "defer_observation")
	return nil
}

func (f *fakeRepository) RecordWalletObservation(ctx context.Context, p store.RecordWalletObservationParams) (store.WalletObservationResult, error) {
	previous := f.lastValue
	_ = f.UpdateWatchRuleValue(ctx, p.Rule.ID, p.CurrentValue)
	r := store.WalletObservationResult{Rule: p.Rule, Status: p.NoTriggerStatus, Value: p.CurrentValue, SourceOccurredAt: p.SourceOccurredAt, SourceTimeBasis: p.SourceTimeBasis, ReadObservedAt: p.ReadObservedAt}
	if !p.Matched {
		return r, nil
	}
	if p.DailyAlertLimit != nil {
		used, _ := f.CountDailyAlertEvents(ctx, p.Rule.DeBoxUserID, freeAlertTimezone)
		if used >= *p.DailyAlertLimit {
			r.Status = "daily_limit"
			r.Used = used
			r.Limit = *p.DailyAlertLimit
			return r, nil
		}
	}
	r.Status = "counted"
	switch {
	case p.Rule.RuleScope == "combination":
		v, err := f.RecordCombinationTrigger(ctx, store.RecordCombinationTriggerParams{WatchRuleID: p.Rule.ID, DeBoxUserID: p.Rule.DeBoxUserID, PreviousValue: p.Rule.LastValue, CurrentValue: &p.CurrentValue, Note: p.Note, TokenSymbol: p.TokenSymbol})
		if err != nil {
			return r, err
		}
		r.Combination = &v
	case p.Rule.DeliveryMode == "stage":
		v, err := f.RecordStageTrigger(ctx, store.RecordStageTriggerParams{WatchRuleID: p.Rule.ID, DeBoxUserID: p.Rule.DeBoxUserID, PreviousValue: p.Rule.LastValue, CurrentValue: &p.CurrentValue, Note: p.Note, TokenSymbol: p.TokenSymbol})
		if err != nil {
			return r, err
		}
		r.Stage = &v
	default:
		v, err := f.CreateAlertEvent(ctx, store.CreateAlertEventParams{WatchRuleID: p.Rule.ID, EventType: p.Rule.RuleType, PreviousValue: p.Rule.LastValue, CurrentValue: &p.CurrentValue, NotificationStatus: "pending"})
		if err != nil {
			return r, err
		}
		r.Event = &v
	}
	if r.Event == nil && (r.Stage == nil || !r.Stage.NotificationDue) && (r.Combination == nil || !r.Combination.NotificationDue) {
		return r, nil
	}
	sp, payload, err := p.BuildNotification(r)
	if err != nil {
		f.lastValue = previous
		return r, err
	}
	snapshot, err := f.CreateNotificationDetailSnapshot(ctx, sp)
	if err != nil {
		f.lastValue = previous
		return r, err
	}
	payload.ChatID = sp.NotificationChatID
	payload.ChatType = sp.NotificationChatType
	payload.Text = sp.NotificationText
	payload.Language = sp.NotificationLanguage
	payload.PublicID = snapshot.PublicID
	payload.ActionURL = strings.ReplaceAll(payload.ActionURL, store.WalletNotificationIDPlaceholder, snapshot.PublicID)
	encoded, _ := json.Marshal(payload)
	row := store.WalletNotificationOutbox{ID: int64(len(f.walletQueue) + 1), SourceKey: sp.SourceKey, SourceType: sp.SourceType, SourceID: *sp.SourceID, WatchRuleID: p.Rule.ID, DeBoxUserID: p.Rule.DeBoxUserID, SnapshotID: snapshot.ID, Payload: encoded, Status: "pending", CreatedAt: time.Now()}
	if f.walletRules == nil {
		f.walletRules = make(map[int64]store.WatchRule)
	}
	f.walletRules[row.ID] = p.Rule
	f.walletQueue = append(f.walletQueue, row)
	r.Outbox = &row
	r.Status = "queued"
	return r, nil
}

func (f *fakeRepository) ClaimWalletNotifications(_ context.Context, onlyID int64, limit int) ([]store.WalletNotificationOutbox, error) {
	for i, row := range f.walletQueue {
		if (onlyID == 0 || row.ID == onlyID) && (row.Status == "pending" || row.Status == "retry") && !row.NextAttemptAt.After(time.Now()) {
			row.Status = "sending"
			token := "test-lease"
			row.LeaseToken = &token
			f.walletQueue[i] = row
			return []store.WalletNotificationOutbox{row}, nil
		}
	}
	return nil, nil
}

func (f *fakeRepository) StartWalletNotificationAttempt(_ context.Context, id int64, token string) (store.WalletNotificationOutbox, error) {
	for i, row := range f.walletQueue {
		if row.ID == id && row.Status == "sending" && row.LeaseToken != nil && *row.LeaseToken == token {
			row.Attempts++
			now := time.Now()
			row.SendStartedAt = &now
			f.walletQueue[i] = row
			return row, nil
		}
	}
	return store.WalletNotificationOutbox{}, store.ErrWalletObservationStale
}
func (f *fakeRepository) WalletNotificationRule(_ context.Context, row store.WalletNotificationOutbox) (store.WatchRule, bool, error) {
	r, ok := f.walletRules[row.ID]
	return r, ok, f.walletEligibilityErr
}
func (f *fakeRepository) FinishWalletNotification(ctx context.Context, p store.FinishWalletNotificationParams) (store.WalletNotificationOutbox, error) {
	if f.walletFinishErr != nil {
		return store.WalletNotificationOutbox{}, f.walletFinishErr
	}
	for i, row := range f.walletQueue {
		if row.ID == p.ID && row.Status == "sending" {
			row.Status = p.Status
			row.MessageID = p.MessageID
			row.LastError = p.Error
			if p.RetryAt != nil {
				row.NextAttemptAt = *p.RetryAt
			}
			f.walletQueue[i] = row
			status := p.Status
			if status == "retry" {
				status = "failed"
			}
			if row.SourceType == "alert_event" {
				_, _ = f.UpdateAlertEventNotification(ctx, row.SourceID, status, p.MessageID, p.Error)
			} else {
				_, _ = f.UpdateAggregateNotification(ctx, row.SourceID, status, p.MessageID, p.Error)
			}
			return row, nil
		}
	}
	return store.WalletNotificationOutbox{}, store.ErrWalletObservationStale
}

func TestWalletRetryReusesFrozenPayloadWithoutAnotherDetectionOrQuota(t *testing.T) {
	previous := "10"
	repository := &fakeRepository{}
	notifier := &fakeNotifier{calls: &repository.calls, err: &notificationdelivery.Failure{Kind: notificationdelivery.Retryable, Code: "busy", RetryAfter: time.Minute, Cause: errors.New("busy")}}
	executor := newTestExecutor(t, repository, &fakeChain{balance: chain.BalanceResult{Value: "12", Symbol: "BNB"}}, notifier)
	rule := testRule(plans.BalanceChange, &previous, plans.Free)
	rule.Threshold = "0"
	result := executor.checkRule(context.Background(), rule, plans.Free)
	if result.Status != "error" || len(repository.walletQueue) != 1 || repository.walletQueue[0].Status != "retry" {
		t.Fatalf("result=%+v queue=%+v", result, repository.walletQueue)
	}
	original := notifier.message
	var frozen store.WalletNotificationPayload
	_ = json.Unmarshal(repository.walletQueue[0].Payload, &frozen)
	notifier.err = nil
	repository.walletQueue[0].NextAttemptAt = time.Now().Add(-time.Second)
	executor.deps.PublicAppURL = "https://changed.invalid"
	repository.calls = nil
	deliveries := executor.deliverWalletNotifications(context.Background(), 0, 10)
	if len(deliveries) != 1 || deliveries[0].Error != nil || deliveries[0].Row.Status != "sent" || notifier.message != original || notifier.actionURL != frozen.ActionURL {
		t.Fatalf("delivery=%+v bodychanged=%t", deliveries, notifier.message != original)
	}
	if strings.Contains(strings.Join(repository.calls, ","), "create_event") || strings.Contains(strings.Join(repository.calls, ","), "count_daily") || strings.Contains(strings.Join(repository.calls, ","), "update_rule") {
		t.Fatalf("retry repeated detection: %v", repository.calls)
	}
}

func TestWalletUnclassifiedFailureIsUnknownAndNotAutomaticallyRetried(t *testing.T) {
	previous := "10"
	repository := &fakeRepository{}
	notifier := &fakeNotifier{err: errors.New("read response timeout")}
	executor := newTestExecutor(t, repository, &fakeChain{balance: chain.BalanceResult{Value: "12", Symbol: "BNB"}}, notifier)
	rule := testRule(plans.BalanceChange, &previous, plans.Standard)
	rule.Threshold = "0"
	_ = executor.checkRule(context.Background(), rule, plans.Standard)
	if len(repository.walletQueue) != 1 || repository.walletQueue[0].Status != "unknown" {
		t.Fatalf("queue=%+v", repository.walletQueue)
	}
	notifier.err = nil
	notifier.message = ""
	if got := executor.deliverWalletNotifications(context.Background(), 0, 10); len(got) != 0 || notifier.message != "" {
		t.Fatalf("unknown resent: %+v", got)
	}
}

func TestWalletAcceptedMessageAcknowledgementFailureNeverResendsAndLogsEvidence(t *testing.T) {
	previous := "10"
	repository := &fakeRepository{walletFinishErr: errors.New("database acknowledgement unavailable")}
	notifier := &fakeNotifier{calls: &repository.calls}
	executor := newTestExecutor(t, repository, &fakeChain{balance: chain.BalanceResult{Value: "12", Symbol: "BNB"}}, notifier)
	rule := testRule(plans.BalanceChange, &previous, plans.Standard)
	rule.Threshold = "0"
	var logs bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(originalLogger)
	result := executor.checkRule(context.Background(), rule, plans.Standard)
	if result.Status != "error" || !strings.Contains(result.Error, "accepted message message-9") || len(repository.walletQueue) != 1 || repository.walletQueue[0].Status != "sending" {
		t.Fatalf("result=%+v queue=%+v", result, repository.walletQueue)
	}
	if !strings.Contains(logs.String(), `"message_id":"message-9"`) || !strings.Contains(logs.String(), `"outbox_id":1`) || strings.Contains(logs.String(), "chat_id") || strings.Contains(logs.String(), notifier.message) {
		t.Fatalf("unsafe or missing evidence: %s", logs.String())
	}
	repository.calls = nil
	_ = executor.deliverWalletNotifications(context.Background(), 0, 10)
	if strings.Contains(strings.Join(repository.calls, ","), "send") {
		t.Fatalf("HTTP resent after accepted ack failure: %v", repository.calls)
	}
}

func TestWalletPreflightDatabaseFailureDoesNotConsumeSendBudget(t *testing.T) {
	previous := "10"
	repository := &fakeRepository{walletEligibilityErr: errors.New("database unavailable before send")}
	notifier := &fakeNotifier{calls: &repository.calls}
	executor := newTestExecutor(t, repository, &fakeChain{balance: chain.BalanceResult{Value: "12", Symbol: "BNB"}}, notifier)
	rule := testRule(plans.BalanceChange, &previous, plans.Standard)
	rule.Threshold = "0"
	result := executor.checkRule(context.Background(), rule, plans.Standard)
	if result.Status != "error" || len(repository.walletQueue) != 1 || repository.walletQueue[0].Status != "retry" || repository.walletQueue[0].Attempts != 0 || notifier.message != "" {
		t.Fatalf("result=%+v queue=%+v sent=%s", result, repository.walletQueue, notifier.message)
	}
}

func TestWalletCapacityRollbackUsesInspectionBackoffWithoutBaselineAdvance(t *testing.T) {
	previous := "10"
	repository := &fakeRepository{snapshotErr: notificationdelivery.ErrCapacity, lastValue: "10"}
	executor := newTestExecutor(t, repository, &fakeChain{balance: chain.BalanceResult{Value: "12", Symbol: "BNB"}}, &fakeNotifier{})
	rule := testRule(plans.BalanceChange, &previous, plans.Standard)
	rule.Threshold = "0"
	result := executor.checkRule(context.Background(), rule, plans.Standard)
	if result.Status != "error" || repository.lastValue != "10" || !strings.Contains(strings.Join(repository.calls, ","), "defer_observation") {
		t.Fatalf("result=%+v calls=%v last=%s", result, repository.calls, repository.lastValue)
	}
}

type blockingWalletContextNotifier struct {
	repository             *fakeRepository
	observationsBeforeSend int
	calls                  int
	legacyCalls            int
}

func (n *blockingWalletContextNotifier) SendNotification(string, string, string) (string, error) {
	n.legacyCalls++
	return "", errors.New("uncancellable sender must not run")
}
func (n *blockingWalletContextNotifier) SendNotificationContext(ctx context.Context, _, _, _ string) (string, error) {
	n.calls++
	n.observationsBeforeSend = strings.Count(strings.Join(n.repository.calls, ","), "update_rule")
	<-ctx.Done()
	return "", &notificationdelivery.Failure{Kind: notificationdelivery.Retryable, Code: "cancelled_before_send", Cause: ctx.Err()}
}

func TestWalletBackgroundCycleEnqueuesAllDetectionsBeforeBoundedDrain(t *testing.T) {
	previous := "10"
	first := testRule(plans.BalanceChange, &previous, plans.Standard)
	first.Threshold = "0"
	second := first
	second.ID = 8
	repository := &fakeRepository{rules: []store.WatchRule{first, second}}
	chainService := &fakeChain{balance: chain.BalanceResult{Value: "12", Symbol: "BNB"}}
	notifier := &blockingWalletContextNotifier{repository: repository}
	executor := newTestExecutor(t, repository, chainService, notifier)
	started := time.Now()
	result, err := executor.checkAllWithDeliveryBudget(context.Background(), 200, 20*time.Millisecond)
	if err != nil || time.Since(started) > time.Second || result.Checked != 2 || result.Queued != 2 || result.Alerted != 0 || result.DeliveriesAttempted != 1 || result.DeliveriesSent != 0 || notifier.observationsBeforeSend != 2 || notifier.calls != 1 || notifier.legacyCalls != 0 {
		t.Fatalf("result=%+v sender=%+v elapsed=%s err=%v", result, notifier, time.Since(started), err)
	}
	if len(repository.walletQueue) != 2 || repository.walletQueue[0].Status != "retry" || repository.walletQueue[1].Status != "pending" || repository.walletQueue[1].Attempts != 0 {
		t.Fatalf("unattempted row changed: %+v", repository.walletQueue)
	}
	for _, item := range result.Results {
		if item.Status != "queued" || item.OutboxID < 1 {
			t.Fatalf("queued misreported as sent: %+v", item)
		}
	}
	current := "12"
	next := testRule(plans.BalanceChange, &current, plans.Standard)
	next.Threshold = "0"
	if got := executor.checkRule(context.Background(), next, plans.Standard); got.Status != "no_change" {
		t.Fatalf("drain cancellation stopped subsequent detection: %+v", got)
	}
}
