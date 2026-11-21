package summary

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type summaryAdmissionBackend struct {
	full, stopAtHTTP bool
	reserves         int
}

func (b *summaryAdmissionBackend) ReserveNotificationAdmission(_ context.Context, bot string, _ int) (notificationdelivery.AdmissionPermit, error) {
	b.reserves++
	if b.full {
		return notificationdelivery.AdmissionPermit{}, &notificationdelivery.AdmissionDeferred{RetryAt: testNow.Add(time.Minute), Reason: "full"}
	}
	now := time.Now().UTC()
	return notificationdelivery.AdmissionPermit{BotID: bot, Token: "slot", GrantedAt: now, ExpiresAt: now.Add(time.Minute)}, nil
}
func (b *summaryAdmissionBackend) CheckNotificationAdmission(context.Context, notificationdelivery.AdmissionPermit) error {
	if b.stopAtHTTP {
		return &notificationdelivery.AdmissionDeferred{RetryAt: testNow.Add(time.Minute), Reason: "server_retry_after"}
	}
	return nil
}
func (*summaryAdmissionBackend) ReleaseNotificationAdmission(context.Context, notificationdelivery.AdmissionPermit) error {
	return nil
}
func (*summaryAdmissionBackend) BlockNotificationAdmission(context.Context, string, time.Duration) error {
	return nil
}

type summaryAdmissionNotifier struct {
	*fakeNotifier
	admission *notificationdelivery.Admission
	actual    int
}

func (n *summaryAdmissionNotifier) NotificationAdmission() *notificationdelivery.Admission {
	return n.admission
}
func (n *summaryAdmissionNotifier) SendNotificationContext(ctx context.Context, chat, kind, text string) (string, error) {
	if _, err := n.admission.BeforeHTTP(ctx, 0); err != nil {
		return "", err
	}
	n.actual++
	return n.fakeNotifier.SendNotification(chat, kind, text)
}
func (n *summaryAdmissionNotifier) SendNotificationWithActionContext(ctx context.Context, chat, kind, text, action, url string) (string, error) {
	return n.SendNotificationContext(ctx, chat, kind, text)
}

func (f *fakeRepository) DeferDailySummaryAdmission(_ context.Context, id int64, token string, at, _ time.Time) error {
	d := f.dispatches[id]
	if d == nil {
		return store.ErrDailySummaryDispatchConflict
	}
	if token != "" {
		if d.Status != "sending" || d.ClaimToken == nil || *d.ClaimToken != token {
			return store.ErrDailySummaryDispatchConflict
		}
		d.Attempts--
		d.Status, d.ClaimToken, d.LeaseUntil = "failed", nil, nil
	}
	d.RetryAt = &at
	return nil
}

func TestDailySummaryAdmissionWaitDoesNotCountAttemptOrRebuildFrozenReport(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_claim", true: "at_http"}[late], func(t *testing.T) {
			now := testNow
			repository := &fakeRepository{subscriptions: []store.Subscription{testSubscription(1)}}
			backend := &summaryAdmissionBackend{full: !late, stopAtHTTP: late}
			admission, _ := notificationdelivery.NewAdmission(backend, "same-bot", 5)
			notifier := &summaryAdmissionNotifier{fakeNotifier: &fakeNotifier{}, admission: admission}
			executor := New(Dependencies{Repository: repository, Notifications: notifier, TryLock: acquiredLock(nil), Now: func() time.Time { return now }})
			result, err := executor.SendDue(context.Background(), 100)
			if err != nil || len(result.Errors) != 0 || len(repository.dispatches) != 1 || repository.dispatches[1].Attempts != 0 || repository.dispatches[1].RetryAt == nil || notifier.actual != 0 {
				t.Fatalf("wait=%+v job=%+v actual=%d err=%v", result, repository.dispatches[1], notifier.actual, err)
			}
			frozen := repository.dispatches[1].Payload
			_, _ = executor.SendDue(context.Background(), 100)
			if backend.reserves != 1 {
				t.Fatal("waiting job reacquired quota before retry_at")
			}
			backend.full, backend.stopAtHTTP = false, false
			notifier.admission, _ = notificationdelivery.NewAdmission(backend, "same-bot", 5)
			now = now.Add(2 * time.Minute)
			result, err = executor.SendDue(context.Background(), 100)
			if err != nil || result.Sent != 1 || len(result.Errors) != 0 || repository.dispatches[1].Attempts != 1 || repository.dispatches[1].Payload != frozen || len(repository.snapshots) != 1 || countSummaryReads(repository) != 1 || notifier.actual != 1 {
				t.Fatalf("resume=%+v job=%+v actual=%d err=%v", result, repository.dispatches[1], notifier.actual, err)
			}
		})
	}
}

func TestDailySummaryAdmissionDoesNotRefundUnknownHTTP(t *testing.T) {
	repository := &fakeRepository{subscriptions: []store.Subscription{testSubscription(1)}}
	admission, _ := notificationdelivery.NewAdmission(&summaryAdmissionBackend{}, "same-bot", 5)
	notifier := &summaryAdmissionNotifier{fakeNotifier: &fakeNotifier{err: errors.New("response lost")}, admission: admission}
	executor := New(Dependencies{Repository: repository, Notifications: notifier, TryLock: acquiredLock(nil), Now: func() time.Time { return testNow }})
	_, _ = executor.SendDue(context.Background(), 100)
	if repository.dispatches[1].Status != "unknown" || repository.dispatches[1].Attempts != 1 || notifier.actual != 1 {
		t.Fatalf("unknown=%+v actual=%d", repository.dispatches[1], notifier.actual)
	}
}
