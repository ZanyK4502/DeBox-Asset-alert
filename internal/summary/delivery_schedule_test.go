package summary

import (
	"context"
	"io"
	"log/slog"
	"sort"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type scheduledSummaryRepository struct {
	*fakeRepository
	now *time.Time
}

func (r *scheduledSummaryRepository) ListDueDailySummaryDispatches(_ context.Context, now time.Time, limit int) ([]store.DailySummaryDispatchRef, error) {
	refs := []store.DailySummaryDispatchRef{}
	for _, d := range r.dispatches {
		if (d.Status == "pending" || d.Status == "failed" && d.RetryAt != nil) && d.Attempts < notificationdelivery.MaxAttempts && (d.RetryAt == nil || !d.RetryAt.After(now)) {
			refs = append(refs, store.DailySummaryDispatchRef{ID: d.ID, SubscriptionID: d.SubscriptionID, TargetID: d.TargetID})
		}
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
	if len(refs) > limit {
		refs = refs[:limit]
	}
	return refs, nil
}
func (r *scheduledSummaryRepository) GetDailySummaryRetryTarget(ctx context.Context, subID, targetID int64) (*store.DailySummaryTarget, error) {
	targets, err := r.ListDailySummaryDispatchTargets(ctx, subID, targetID-1, 1)
	if err != nil || len(targets) == 0 || targets[0].ID != targetID {
		return nil, err
	}
	return &targets[0], nil
}
func (r *scheduledSummaryRepository) NextDailySummaryDispatchAt(context.Context) (*time.Time, error) {
	var next *time.Time
	for _, d := range r.dispatches {
		if d.Status != "pending" && (d.Status != "failed" || d.RetryAt == nil) || d.Attempts >= notificationdelivery.MaxAttempts {
			continue
		}
		at := *r.now
		if d.RetryAt != nil {
			at = *d.RetryAt
		}
		if next == nil || at.Before(*next) {
			copy := at
			next = &copy
		}
	}
	return next, nil
}

func TestSummaryQueueWakeRetriesFrozenDispatchWithoutScanningSubscriptionsOrRebuilding(t *testing.T) {
	now := testNow
	base := &fakeRepository{subscriptions: []store.Subscription{testSubscription(1)}}
	repo := &scheduledSummaryRepository{fakeRepository: base, now: &now}
	sender := &fakeNotifier{err: &notificationdelivery.Failure{Kind: notificationdelivery.Retryable, Code: "explicit_failure"}}
	e := New(Dependencies{Repository: repo, Notifications: sender, TryLock: acquiredLock(nil), Now: func() time.Time { return now }})
	if _, err := e.SendDue(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	frozen := repo.dispatches[1].Payload
	reads, lists := countSummaryReads(base), len(base.listAfterIDs)
	now = *repo.dispatches[1].RetryAt
	sender.err = nil
	result, err := e.SendPending(context.Background(), 100)
	if err != nil || result.Sent != 1 || countSummaryReads(base) != reads || len(base.snapshots) != 1 || len(base.listAfterIDs) != lists || len(sender.messages) != 1 || sender.messages[0].text != frozen.Text {
		t.Fatalf("retry rebuilt or rescanned: %+v %v reads=%d snapshots=%d lists=%v", result, err, countSummaryReads(base), len(base.snapshots), base.listAfterIDs)
	}
	if result, err = e.SendPending(context.Background(), 100); err != nil || result.Sent != 0 || len(sender.messages) != 1 {
		t.Fatalf("sent retried: %+v %v", result, err)
	}
}

func TestSummaryQueueWakeDefersCrossPeriodMergeToNormalCycleAndPreservesUnknown(t *testing.T) {
	now := testNow
	base := &fakeRepository{subscriptions: []store.Subscription{testSubscription(1)}}
	repo := &scheduledSummaryRepository{fakeRepository: base, now: &now}
	sender := &fakeNotifier{err: &notificationdelivery.Failure{Kind: notificationdelivery.Retryable, Code: "explicit_failure"}}
	e := New(Dependencies{Repository: repo, Notifications: sender, TryLock: acquiredLock(nil), Now: func() time.Time { return now }})
	if _, err := e.SendDue(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	now = now.Add(24 * time.Hour)
	sender.err = nil
	result, err := e.SendPending(context.Background(), 100)
	if err != nil || !result.NeedsNormalCycle || result.Sent != 0 || len(base.snapshots) != 1 || repo.dispatches[1].Status != "failed" || countSummaryReads(base) != 1 {
		t.Fatalf("queue rebuilt/sent old report: %+v %v", result, err)
	}
	repo.dispatches[1].Status = "unknown"
	if result, err = e.SendPending(context.Background(), 100); err != nil || result.Sent != 0 || result.NeedsNormalCycle || len(sender.messages) != 0 {
		t.Fatalf("unknown revived: %+v %v", result, err)
	}
}

func TestSummaryQueueTimerUsesRetryWithoutMovingNormalClock(t *testing.T) {
	now := testNow
	next := now.Add(time.Second)
	base := &fakeRepository{dispatches: map[int64]*store.DailySummaryDispatch{1: {ID: 1, Status: "failed", RetryAt: &next}}}
	repo := &scheduledSummaryRepository{fakeRepository: base, now: &now}
	e := New(Dependencies{Repository: repo, Notifications: &fakeNotifier{}, Now: func() time.Time { return now }})
	r := NewRunner(e, time.Minute)
	r.nextNormalDue = now.Add(time.Minute)
	if d := r.nextDelay(context.Background(), CycleResult{}, nil, now); d != time.Second {
		t.Fatalf("retry delayed by normal schedule: %s", d)
	}
	if !r.nextNormalDue.Equal(now.Add(time.Minute)) {
		t.Fatal("queue wake moved normal clock")
	}
	if d := r.nextDelay(context.Background(), CycleResult{NeedsNormalCycle: true}, nil, now); d != time.Minute {
		t.Fatalf("unresolved merge would spin: %s", d)
	}
	past := now.Add(-time.Second)
	base.dispatches[1].RetryAt = &past
	if d := r.nextDelay(context.Background(), CycleResult{Errors: []ErrorResult{{Error: "failed lookup"}}}, nil, now); d != 5*time.Second {
		t.Fatalf("bad due head would spin: %s", d)
	}
	if d := r.nextDelay(context.Background(), CycleResult{Skipped: 1}, nil, now); d != 5*time.Second {
		t.Fatalf("unchanged due head would spin: %s", d)
	}
}

func TestSummaryQueueMergeLockFailureKeepsNormalRequestBackoff(t *testing.T) {
	now := testNow
	base := &fakeRepository{subscriptions: []store.Subscription{testSubscription(1)}}
	repo := &scheduledSummaryRepository{fakeRepository: base, now: &now}
	sender := &fakeNotifier{err: &notificationdelivery.Failure{Kind: notificationdelivery.Retryable, Code: "not_sent"}}
	e := New(Dependencies{Repository: repo, Notifications: sender, TryLock: acquiredLock(nil), Now: func() time.Time { return now }})
	if _, err := e.SendDue(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	now = now.Add(24 * time.Hour)
	sender.err = nil
	locks := 0
	e.deps.TryLock = func(context.Context, int64) (Lock, bool, error) {
		locks++
		if locks == 1 {
			return &fakeLock{}, true, nil
		}
		return nil, false, nil
	}
	r := NewRunner(e, time.Minute)
	before := time.Now()
	r.nextNormalDue = before.Add(time.Minute)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	delay := r.runScheduledCycle(context.Background(), logger)
	if locks != 2 || delay < 50*time.Second || !r.nextMergeAttemptAt.Equal(r.nextNormalDue) || len(base.snapshots) != 1 || repo.dispatches[1].Status != "failed" {
		t.Fatalf("merge lock failure lost suppression: locks=%d delay=%s snapshots=%d status=%s", locks, delay, len(base.snapshots), repo.dispatches[1].Status)
	}
	// Even if invoked early, the same unchanged target cannot request a second
	// whole-subscription merge scan before the original normal clock.
	locks = 0
	r.runScheduledCycle(context.Background(), logger)
	if locks != 1 || len(base.snapshots) != 1 {
		t.Fatalf("bad target repeated full merge: locks=%d snapshots=%d", locks, len(base.snapshots))
	}
}

func TestSummaryQueueAdmissionWaitPersistsWakeAndDoesNotBurnAttempt(t *testing.T) {
	now := testNow
	base := &fakeRepository{subscriptions: []store.Subscription{testSubscription(1)}}
	repo := &scheduledSummaryRepository{fakeRepository: base, now: &now}
	e := New(Dependencies{Repository: repo, Notifications: &fakeNotifier{err: &notificationdelivery.Failure{Kind: notificationdelivery.Retryable, Code: "unsent"}}, TryLock: acquiredLock(nil), Now: func() time.Time { return now }})
	if _, err := e.SendDue(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	now = *base.dispatches[1].RetryAt
	backend := &summaryAdmissionBackend{full: true}
	admission, err := notificationdelivery.NewAdmission(backend, "queue-bot", 100)
	if err != nil {
		t.Fatal(err)
	}
	sender := &summaryAdmissionNotifier{fakeNotifier: &fakeNotifier{}, admission: admission}
	e.deps.Notifications = sender
	result, err := e.SendPending(context.Background(), 100)
	if err != nil || result.NextDeliveryAt == nil || !result.NextDeliveryAt.Equal(testNow.Add(time.Minute)) || backend.reserves != 1 || sender.actual != 0 || base.dispatches[1].Attempts != 1 || len(base.snapshots) != 1 || countSummaryReads(base) != 1 {
		t.Fatalf("admission retry lost evidence: %+v %v attempts=%d reserves=%d HTTP=%d", result, err, base.dispatches[1].Attempts, backend.reserves, sender.actual)
	}
	if _, err = e.SendPending(context.Background(), 100); err != nil || backend.reserves != 1 {
		t.Fatalf("future gate reacquired: %d %v", backend.reserves, err)
	}
}
