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

type walletAdmissionBackend struct{ full, stopAtHTTP bool }

func (b *walletAdmissionBackend) ReserveNotificationAdmission(_ context.Context, bot string, _ int) (notificationdelivery.AdmissionPermit, error) {
	if b.full {
		return notificationdelivery.AdmissionPermit{}, &notificationdelivery.AdmissionDeferred{RetryAt: time.Now().Add(time.Minute), Reason: "full"}
	}
	now := time.Now().UTC()
	return notificationdelivery.AdmissionPermit{BotID: bot, Token: "slot", GrantedAt: now, ExpiresAt: now.Add(time.Minute)}, nil
}
func (b *walletAdmissionBackend) CheckNotificationAdmission(context.Context, notificationdelivery.AdmissionPermit) error {
	if b.stopAtHTTP {
		return &notificationdelivery.AdmissionDeferred{RetryAt: time.Now().Add(time.Minute), Reason: "server_retry_after"}
	}
	return nil
}
func (*walletAdmissionBackend) ReleaseNotificationAdmission(context.Context, notificationdelivery.AdmissionPermit) error {
	return nil
}
func (*walletAdmissionBackend) BlockNotificationAdmission(context.Context, string, time.Duration) error {
	return nil
}

type walletAdmissionNotifier struct {
	*fakeNotifier
	admission *notificationdelivery.Admission
	actual    int
}

func (n *walletAdmissionNotifier) NotificationAdmission() *notificationdelivery.Admission {
	return n.admission
}
func (n *walletAdmissionNotifier) SendNotificationContext(ctx context.Context, chat, kind, text string) (string, error) {
	if _, err := n.admission.BeforeHTTP(ctx, 0); err != nil {
		return "", err
	}
	n.actual++
	return n.fakeNotifier.SendNotification(chat, kind, text)
}
func (n *walletAdmissionNotifier) SendNotificationWithActionContext(ctx context.Context, chat, kind, text, action, url string) (string, error) {
	return n.SendNotificationContext(ctx, chat, kind, text)
}

func (f *fakeRepository) DeferWalletNotificationAdmission(_ context.Context, id int64, token string, at time.Time) (store.WalletNotificationOutbox, error) {
	for i, row := range f.walletQueue {
		if row.ID == id && row.Status == "sending" && row.LeaseToken != nil && *row.LeaseToken == token {
			if row.SendStartedAt != nil {
				row.Attempts--
			}
			row.Status, row.NextAttemptAt, row.SendStartedAt, row.LeaseToken = "retry", at, nil, nil
			f.walletQueue[i] = row
			return row, nil
		}
	}
	return store.WalletNotificationOutbox{}, store.ErrWalletObservationStale
}

func TestWalletAdmissionWaitDoesNotSpendNetworkAttemptsOrRecountDetection(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_start", true: "at_http"}[late], func(t *testing.T) {
			previous := "10"
			repository := &fakeRepository{}
			backend := &walletAdmissionBackend{full: !late, stopAtHTTP: late}
			admission, _ := notificationdelivery.NewAdmission(backend, "same-bot", 5)
			notifier := &walletAdmissionNotifier{fakeNotifier: &fakeNotifier{}, admission: admission}
			executor := newTestExecutor(t, repository, &fakeChain{balance: chain.BalanceResult{Value: "12", Symbol: "BNB"}}, notifier)
			rule := testRule(plans.BalanceChange, &previous, plans.Free)
			rule.Threshold = "0"
			result := executor.checkRule(context.Background(), rule, plans.Free)
			if result.Status != "retry" || len(repository.walletQueue) != 1 || repository.walletQueue[0].Attempts != 0 || notifier.actual != 0 {
				t.Fatalf("result=%+v queue=%+v actual=%d", result, repository.walletQueue, notifier.actual)
			}
			frozen := string(repository.walletQueue[0].Payload)
			backend.full, backend.stopAtHTTP = false, false
			// A fresh controller models the next process/cycle; durable quota
			// and snapshot behavior are exercised in PostgreSQL tests.
			notifier.admission, _ = notificationdelivery.NewAdmission(backend, "same-bot", 5)
			repository.walletQueue[0].NextAttemptAt = time.Time{}
			got := executor.deliverWalletNotifications(context.Background(), 0, 10)
			if len(got) != 1 || got[0].Error != nil || got[0].Row.Status != "sent" || got[0].Row.Attempts != 1 || string(got[0].Row.Payload) != frozen || notifier.actual != 1 {
				t.Fatalf("resume=%+v actual=%d", got, notifier.actual)
			}
		})
	}
}

func TestWalletAdmissionDoesNotRefundActualUnknownTransport(t *testing.T) {
	previous := "10"
	repository := &fakeRepository{}
	admission, _ := notificationdelivery.NewAdmission(&walletAdmissionBackend{}, "same-bot", 5)
	notifier := &walletAdmissionNotifier{fakeNotifier: &fakeNotifier{err: errors.New("response lost")}, admission: admission}
	executor := newTestExecutor(t, repository, &fakeChain{balance: chain.BalanceResult{Value: "12"}}, notifier)
	rule := testRule(plans.BalanceChange, &previous, plans.Standard)
	rule.Threshold = "0"
	_ = executor.checkRule(context.Background(), rule, plans.Standard)
	if len(repository.walletQueue) != 1 || repository.walletQueue[0].Attempts != 1 || repository.walletQueue[0].Status != "unknown" || notifier.actual != 1 {
		t.Fatalf("unknown was refunded or retried: %+v actual=%d", repository.walletQueue, notifier.actual)
	}
}
