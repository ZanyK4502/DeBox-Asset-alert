package marketrules

import (
	"context"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type scheduledMarketRepository struct {
	*frozenDeliveryRepository
	claims   int
	next     time.Time
	deferred int
}

func (r *scheduledMarketRepository) ClaimMarketDeliveries(context.Context, int) ([]store.MarketDeliveryClaim, error) {
	r.claims++
	if r.claims > 1 {
		return nil, nil
	}
	return []store.MarketDeliveryClaim{{Kind: "realtime", ID: 1, LeaseToken: 1}}, nil
}
func (r *scheduledMarketRepository) NextMarketDeliveryAt(context.Context) (*time.Time, error) {
	return &r.next, nil
}
func (r *scheduledMarketRepository) DeferMarketDeliveryAdmission(context.Context, store.MarketDeliveryClaim, time.Time, bool) error {
	r.deferred++
	return nil
}

func TestMarketDeliveryOnlyCycleDoesNotEvaluateOrRebuildFrozenPayload(t *testing.T) {
	repo := &scheduledMarketRepository{frozenDeliveryRepository: &frozenDeliveryRepository{frozen: &store.MarketDeliveryPayload{SnapshotID: 1, Payload: notificationdelivery.Payload{ChatID: "owner", ChatType: "private", Text: "frozen"}}}}
	sender := &frozenDeliverySender{}
	service := New(Dependencies{Repository: repo, Notifications: sender}, Settings{Enabled: true})
	result, err := service.RunDeliveryCycle(context.Background())
	if err != nil || result.DeliveriesSent != 1 || result.RulesEvaluated != 0 || repo.loads != 0 || repo.snapshots != 0 || len(sender.payloads) != 1 {
		t.Fatalf("queue-only changed evaluation/snapshot path: %+v %v loads=%d snapshots=%d", result, err, repo.loads, repo.snapshots)
	}
}

func TestMarketDeliveryAdmissionDeferralStopsDrainAndWakesBeforeRuleInterval(t *testing.T) {
	now := time.Now()
	at := now.Add(2 * time.Second)
	backend := &marketAdmissionTestBackend{reserveErr: &notificationdelivery.AdmissionDeferred{RetryAt: at, Reason: "second_limit"}}
	admission, err := notificationdelivery.NewAdmission(backend, "schedule-bot", 100)
	if err != nil {
		t.Fatal(err)
	}
	sender := &marketAdmissionTestSender{admission: admission}
	repo := &scheduledMarketRepository{next: now, frozenDeliveryRepository: &frozenDeliveryRepository{frozen: &store.MarketDeliveryPayload{SnapshotID: 1, Payload: notificationdelivery.Payload{ChatID: "owner", ChatType: "private", Text: "frozen"}}}}
	service := New(Dependencies{Repository: repo, Notifications: sender}, Settings{Enabled: true, Interval: 20 * time.Second})
	result, err := service.RunDeliveryCycle(context.Background())
	if err != nil || repo.claims != 1 || repo.deferred != 1 || repo.begins != 0 || sender.httpCalls != 0 || result.NextDeliveryAt == nil || !result.NextDeliveryAt.Equal(at) {
		t.Fatalf("deferral spun or sent: %+v %v claims=%d defers=%d", result, err, repo.claims, repo.deferred)
	}
	runner := NewRunner(service)
	runner.nextNormalDue = now.Add(20 * time.Second)
	if delay := runner.nextDelay(context.Background(), result, nil, now); delay != 2*time.Second {
		t.Fatalf("queue wait=%s", delay)
	}
	// The shared future hint prevents even a claim on another queue-only pass.
	_, err = service.RunDeliveryCycle(context.Background())
	if err != nil || repo.claims != 1 {
		t.Fatalf("future global gate claimed: %d %v", repo.claims, err)
	}
}

func TestMarketDeliveryBadDueHeadUsesErrorBackoff(t *testing.T) {
	now := time.Now()
	repo := &scheduledMarketRepository{next: now.Add(-time.Second), frozenDeliveryRepository: &frozenDeliveryRepository{}}
	service := New(Dependencies{Repository: repo, Notifications: &frozenDeliverySender{}}, Settings{Enabled: true, Interval: 20 * time.Second})
	runner := NewRunner(service)
	runner.nextNormalDue = now.Add(20 * time.Second)
	if delay := runner.nextDelay(context.Background(), CycleResult{DeliveriesFailed: 1}, nil, now); delay != 5*time.Second {
		t.Fatalf("bad due head would spin: %s", delay)
	}
}
