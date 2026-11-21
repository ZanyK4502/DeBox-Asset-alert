package notificationdelivery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type admissionTestBackend struct {
	mu      sync.Mutex
	permits map[string]AdmissionPermit
	blocked time.Time
	next    int
	fail    bool
}

func (b *admissionTestBackend) ReserveNotificationAdmission(_ context.Context, bot string, limit int) (AdmissionPermit, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail {
		return AdmissionPermit{}, errors.New("database unavailable")
	}
	if time.Now().Before(b.blocked) {
		return AdmissionPermit{}, &AdmissionDeferred{RetryAt: b.blocked, Reason: "server_retry_after"}
	}
	if len(b.permits) >= limit {
		return AdmissionPermit{}, &AdmissionDeferred{RetryAt: time.Now().Add(time.Minute), Reason: "rolling_budget_full"}
	}
	if b.permits == nil {
		b.permits = make(map[string]AdmissionPermit)
	}
	b.next++
	p := AdmissionPermit{BotID: bot, Token: fmt.Sprint(b.next), GrantedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Minute)}
	b.permits[p.Token] = p
	return p, nil
}
func (b *admissionTestBackend) CheckNotificationAdmission(_ context.Context, p AdmissionPermit) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail {
		return errors.New("database unavailable")
	}
	if time.Now().Before(b.blocked) {
		return &AdmissionDeferred{RetryAt: b.blocked, Reason: "server_retry_after"}
	}
	if saved, ok := b.permits[p.Token]; !ok || saved.Consumed || !p.ExpiresAt.After(time.Now()) {
		return &AdmissionDeferred{RetryAt: time.Now(), Reason: "permit_unavailable"}
	}
	p.Consumed = true
	b.permits[p.Token] = p
	return nil
}
func (b *admissionTestBackend) ReleaseNotificationAdmission(_ context.Context, p AdmissionPermit) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if saved := b.permits[p.Token]; !saved.Consumed {
		delete(b.permits, p.Token)
	}
	return nil
}
func (b *admissionTestBackend) BlockNotificationAdmission(_ context.Context, _ string, delay time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if at := time.Now().Add(delay); at.After(b.blocked) {
		b.blocked = at
	}
	return nil
}

type admissionTestSender struct{ admission *Admission }

func (s admissionTestSender) NotificationAdmission() *Admission { return s.admission }

func TestAdmissionBindsSingleUsePermitAndKeepsOrdinaryContextAlive(t *testing.T) {
	backend := &admissionTestBackend{}
	admission, err := NewAdmission(backend, "same-bot", 5)
	if err != nil {
		t.Fatal(err)
	}
	ctx, reservation, err := Acquire(context.Background(), admissionTestSender{admission})
	if err != nil {
		t.Fatal(err)
	}
	foreign, _ := NewAdmission(backend, "other-bot", 5)
	if _, err := foreign.BeforeHTTP(ctx, 0); err == nil {
		t.Fatal("foreign bot accepted permit")
	}
	if _, err := admission.BeforeHTTP(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := admission.BeforeHTTP(ctx, 0); err == nil {
		t.Fatal("permit reused")
	}
	if err := reservation.Release(context.Background()); err != nil || len(backend.permits) != 1 {
		t.Fatal("consumed reservation released")
	}
	ordinary, err := admission.BeforeHTTP(context.Background(), 20*time.Second)
	if err != nil || ordinary.Err() != nil {
		t.Fatalf("returned context already cancelled: %v %v", ordinary.Err(), err)
	}
}

func TestAdmissionWaitIsLocalBoundedAndSharedBackendSurvivesControllerRestart(t *testing.T) {
	backend := &admissionTestBackend{}
	admission, _ := NewAdmission(backend, "same-bot", 5)
	for range 5 {
		if _, err := admission.BeforeHTTP(context.Background(), 0); err != nil {
			t.Fatal(err)
		}
	}
	restarted, _ := NewAdmission(backend, "same-bot", 5)
	started := time.Now()
	_, err := restarted.BeforeHTTP(context.Background(), 15*time.Millisecond)
	if _, ok := AdmissionRetryAt(err); !ok || time.Since(started) > time.Second || len(backend.permits) != 5 {
		t.Fatalf("wait=%s err=%v permits=%d", time.Since(started), err, len(backend.permits))
	}
}

func TestAdmissionNewServerBlockBeforeHTTPDoesNotConsumeAndStoreFailureFailsClosed(t *testing.T) {
	backend := &admissionTestBackend{}
	admission, _ := NewAdmission(backend, "same-bot", 5)
	ctx, reservation, err := Acquire(context.Background(), admissionTestSender{admission})
	if err != nil {
		t.Fatal(err)
	}
	if err := admission.ObserveRetryAfter(context.Background(), 90*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := admission.BeforeHTTP(ctx, 0); err == nil || reservation.consumed.Load() {
		t.Fatal("new server block failed to stop unused permit")
	}
	_ = reservation.Release(context.Background())
	if len(backend.permits) != 0 {
		t.Fatal("unused permit was not released")
	}
	broken := &admissionTestBackend{fail: true}
	closed, _ := NewAdmission(broken, "same-bot", 5)
	if _, _, err := Acquire(context.Background(), admissionTestSender{closed}); err == nil {
		t.Fatal("database failure admitted HTTP")
	} else if _, ok := AdmissionRetryAt(err); !ok {
		t.Fatal("pre-transport database failure lost no-send proof")
	}
}

func TestAdmissionCancelledWorkerDoesNotGloballyBlockOtherWork(t *testing.T) {
	backend := &admissionTestBackend{}
	admission, _ := NewAdmission(backend, "same-bot", 5)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Acquire(ctx, admissionTestSender{admission}); err == nil {
		t.Fatal("cancelled worker admitted")
	}
	if _, _, err := Acquire(context.Background(), admissionTestSender{admission}); err != nil {
		t.Fatalf("cancelled worker blocked another delivery: %v", err)
	}
}

func TestAdmissionV2DefaultAndDurablePathRequirement(t *testing.T) {
	backend := &admissionTestBackend{}
	admission, err := NewAdmission(backend, "same-bot", AdmissionDefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = admission.BeforeHTTPPath(context.Background(), 0, strings.Repeat("a", 64)); err == nil {
		t.Fatal("real path used a minute-only backend")
	}
	if len(backend.permits) != 0 {
		t.Fatal("unsupported path backend reserved work")
	}
	for _, limit := range []int{5, 10, 100} {
		if _, err := NewAdmission(backend, "same-bot", limit); err != nil {
			t.Fatalf("valid cap%d: %v", limit, err)
		}
	}
	for _, limit := range []int{0, 6, 99, 101} {
		if _, err := NewAdmission(backend, "same-bot", limit); err == nil {
			t.Fatalf("invalid cap%d accepted", limit)
		}
	}
}
