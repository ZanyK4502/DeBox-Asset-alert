package monitor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type walletWakeRepository struct {
	*fakeRepository
	claims, observations int
	observationAt        time.Time
	scheduleErr          error
}

func (r *walletWakeRepository) ListDueWatchRules(context.Context, int, time.Duration) ([]store.WatchRule, error) {
	r.observations++
	return append([]store.WatchRule(nil), r.rules...), nil
}
func (r *walletWakeRepository) NextWalletObservationAt(context.Context, time.Duration) (*time.Time, error) {
	return &r.observationAt, nil
}
func (r *walletWakeRepository) NextWalletNotificationAt(_ context.Context, gate time.Time) (*time.Time, error) {
	if r.scheduleErr != nil {
		return nil, r.scheduleErr
	}
	var next *time.Time
	for _, row := range r.walletQueue {
		var at time.Time
		switch row.Status {
		case "pending", "retry":
			if row.Attempts >= 5 {
				continue
			}
			at = row.NextAttemptAt
			if gate.After(at) {
				at = gate
			}
		case "sending":
			if row.LeaseExpiresAt == nil {
				continue
			}
			at = *row.LeaseExpiresAt
		default:
			continue
		}
		if next == nil || at.Before(*next) {
			copy := at
			next = &copy
		}
	}
	return next, nil
}
func (r *walletWakeRepository) ClaimWalletNotifications(ctx context.Context, only int64, limit int) ([]store.WalletNotificationOutbox, error) {
	r.claims++
	for i, row := range r.walletQueue {
		if row.Status == "sending" && row.LeaseExpiresAt != nil && !row.LeaseExpiresAt.After(time.Now()) {
			r.walletQueue[i].Status = "retry"
			if row.SendStartedAt != nil {
				r.walletQueue[i].Status = "unknown"
			}
			r.walletQueue[i].LeaseExpiresAt = nil
		}
	}
	return r.fakeRepository.ClaimWalletNotifications(ctx, only, limit)
}

func newWalletWakeFixture(t *testing.T, notifier NotificationService, count int) (*Executor, *walletWakeRepository, *fakeChain) {
	t.Helper()
	r := &walletWakeRepository{fakeRepository: &fakeRepository{}, observationAt: time.Now().Add(time.Minute)}
	source := &fakeChain{balance: chain.BalanceResult{Value: "12", Symbol: "BNB"}}
	e := newTestExecutor(t, r, source, notifier)
	before := "10"
	rule := testRule(plans.BalanceChange, &before, plans.Free)
	rule.Threshold = "0"
	for i := range count {
		rule.ID = int64(i + 1)
		result := e.checkRule(context.WithValue(context.Background(), walletEnqueueOnlyContextKey{}, true), rule, plans.Free)
		if result.Status != "queued" {
			t.Fatalf("seed frozen notification=%+v", result)
		}
	}
	source.calls, r.calls = nil, nil
	return e, r, source
}

func TestWalletQueueWakeDeliversWithoutObservationOrRecount(t *testing.T) {
	e, r, source := newWalletWakeFixture(t, &fakeNotifier{}, 1)
	frozen := string(r.walletQueue[0].Payload)
	runner := NewRunner(e, func(context.Context) (Lock, bool, error) { return &fakeLock{}, true, nil }, time.Minute)
	runner.nextObservationAt = r.observationAt
	runner.nextEntitlementCheckAt, runner.nextCleanupAttemptAt = r.observationAt, r.observationAt
	delay := runner.runCycle(context.Background(), discardLogger())
	if r.walletQueue[0].Status != "sent" || string(r.walletQueue[0].Payload) != frozen || r.observations != 0 || len(source.calls) != 0 {
		t.Fatalf("queue wake changed detection or payload: queue=%+v observations=%d reads=%v", r.walletQueue, r.observations, source.calls)
	}
	for _, call := range r.calls {
		if call == "count_daily" || call == "create_event" || call == "update_rule" || call == "record_stage" || call == "record_combination" {
			t.Fatalf("queue wake repeated detection/allowance: %v", r.calls)
		}
	}
	if delay < 55*time.Second {
		t.Fatalf("empty queue kept polling: %s", delay)
	}
}

func TestWalletQueueGateDoesNotMoveOnePendingRowEveryWake(t *testing.T) {
	backend := &walletAdmissionBackend{full: true}
	admission, _ := notificationdelivery.NewAdmission(backend, "same-bot", 100)
	notifier := &walletAdmissionNotifier{fakeNotifier: &fakeNotifier{}, admission: admission}
	e, r, source := newWalletWakeFixture(t, notifier, 4)
	first := e.checkWalletDeliveries(context.Background())
	if r.claims != 1 || first.NextDeliveryAt == nil || time.Until(*first.NextDeliveryAt) < 55*time.Second {
		t.Fatalf("quota did not establish next wake: claims=%d result=%+v", r.claims, first)
	}
	for range 4 {
		result := e.checkWalletDeliveries(context.Background())
		if result.NextDeliveryAt == nil || time.Until(*result.NextDeliveryAt) < 55*time.Second {
			t.Fatalf("pending head bypassed shared quota hint: %+v", result)
		}
	}
	if r.claims != 1 || notifier.actual != 0 || len(source.calls) != 0 {
		t.Fatalf("quota wait churned queue: claims=%d sends=%d reads=%v", r.claims, notifier.actual, source.calls)
	}
	// An ordinary observation remains independent of a longer server wait.
	if err := admission.ObserveRetryAfter(context.Background(), 120*time.Second); err != nil {
		t.Fatal(err)
	}
	r.rules = []store.WatchRule{testRule(plans.BalanceChange, nil, plans.Free)}
	runner := NewRunner(e, func(context.Context) (Lock, bool, error) { return &fakeLock{}, true, nil }, time.Minute)
	delay := runner.runCycle(context.Background(), discardLogger())
	if r.observations != 1 || len(source.calls) != 1 || r.claims != 1 || delay < 55*time.Second || delay > time.Minute {
		t.Fatalf("server wait blocked observation or caused short polling: obs=%d reads=%v claims=%d delay=%v", r.observations, source.calls, r.claims, delay)
	}
}

func TestWalletLeaseRecoveryRemainsLiveDuringQuotaWait(t *testing.T) {
	admission, _ := notificationdelivery.NewAdmission(&walletAdmissionBackend{}, "same-bot", 100)
	notifier := &walletAdmissionNotifier{fakeNotifier: &fakeNotifier{}, admission: admission}
	e, r, source := newWalletWakeFixture(t, notifier, 1)
	past := time.Now().Add(-time.Second)
	r.walletQueue[0].Status, r.walletQueue[0].LeaseExpiresAt, r.walletQueue[0].SendStartedAt = "sending", &past, &past
	if err := admission.ObserveRetryAfter(context.Background(), 120*time.Second); err != nil {
		t.Fatal(err)
	}
	first := e.checkWalletDeliveries(context.Background())
	if r.claims != 1 || r.walletQueue[0].Status != "unknown" || first.NextDeliveryAt != nil || notifier.actual != 0 {
		t.Fatalf("expired send was hidden or retried: claims=%d queue=%+v result=%+v", r.claims, r.walletQueue, first)
	}
	e.checkWalletDeliveries(context.Background())
	if r.claims != 1 || len(source.calls) != 0 {
		t.Fatal("unknown send kept waking or observing")
	}
}

func TestWalletQueueSingleRetryDoesNotBlockOtherPendingWork(t *testing.T) {
	e, r, _ := newWalletWakeFixture(t, &fakeNotifier{}, 3)
	later := time.Now().Add(30 * time.Second)
	r.walletQueue[0].Status, r.walletQueue[0].NextAttemptAt = "retry", later
	r.walletQueue[1].Status = "unknown"
	result := e.checkWalletDeliveries(context.Background())
	if result.DeliveriesSent != 1 || r.walletQueue[0].Status != "retry" || r.walletQueue[1].Status != "unknown" || r.walletQueue[2].Status != "sent" || result.NextDeliveryAt == nil || !result.NextDeliveryAt.Equal(later) {
		t.Fatalf("per-row delay globally stalled delivery: result=%+v queue=%+v", result, r.walletQueue)
	}
	r.scheduleErr = errors.New("schedule storage unavailable")
	result = e.checkWalletDeliveries(context.Background())
	if len(result.Errors) == 0 || result.NextDeliveryAt == nil || time.Until(*result.NextDeliveryAt) < 4*time.Second {
		t.Fatalf("schedule failure has no bounded retry: %+v", result)
	}
}

func TestWalletRunnerChoosesDueDeliveryWithoutBusyLoop(t *testing.T) {
	now := time.Now()
	observation, notification := now.Add(time.Minute), now.Add(2*time.Second)
	runner := &Runner{interval: time.Minute}
	if got := runner.nextDelay(CycleResult{NextObservationAt: &observation, NextDeliveryAt: &notification}, now); got != 2*time.Second {
		t.Fatalf("notification waits for observation: %v", got)
	}
	if got := runner.nextDelay(CycleResult{NextObservationAt: &observation, NextDeliveryAt: &now}, now); got != time.Second {
		t.Fatalf("due notification can spin: %v", got)
	}
}
