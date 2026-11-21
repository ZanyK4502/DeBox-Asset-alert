package marketrules

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type marketAdmissionTestBackend struct {
	reserveErr, checkErr error
	releases, checks     int
}

func (b *marketAdmissionTestBackend) ReserveNotificationAdmission(_ context.Context, bot string, _ int) (notificationdelivery.AdmissionPermit, error) {
	at := time.Now().UTC()
	return notificationdelivery.AdmissionPermit{BotID: bot, Token: "local-reservation", GrantedAt: at, ExpiresAt: at.Add(time.Minute)}, b.reserveErr
}
func (b *marketAdmissionTestBackend) CheckNotificationAdmission(context.Context, notificationdelivery.AdmissionPermit) error {
	b.checks++
	return b.checkErr
}
func (b *marketAdmissionTestBackend) ReleaseNotificationAdmission(context.Context, notificationdelivery.AdmissionPermit) error {
	b.releases++
	return nil
}
func (b *marketAdmissionTestBackend) BlockNotificationAdmission(context.Context, string, time.Duration) error {
	return nil
}

type marketAdmissionTestSender struct {
	admission *notificationdelivery.Admission
	httpCalls int
	sendError error
}

func (s *marketAdmissionTestSender) NotificationAdmission() *notificationdelivery.Admission {
	return s.admission
}
func (s *marketAdmissionTestSender) SendNotification(string, string, string) (string, error) {
	panic("a worker reservation requires the context transport")
}
func (s *marketAdmissionTestSender) SendNotificationContext(ctx context.Context, _, _, _ string) (string, error) {
	if _, err := s.admission.BeforeHTTP(ctx, 0); err != nil {
		return "", err
	}
	s.httpCalls++
	return "accepted-id", s.sendError
}

type marketAdmissionTestRepository struct {
	*frozenDeliveryRepository
	deferredAfterBegin []bool
	retryAt            time.Time
}

func (r *marketAdmissionTestRepository) DeferMarketDeliveryAdmission(_ context.Context, _ store.MarketDeliveryClaim, at time.Time, afterBegin bool) error {
	r.deferredAfterBegin = append(r.deferredAfterBegin, afterBegin)
	r.retryAt = at
	return nil
}

func TestMarketAdmissionWaitNeverConsumesAnHTTPAttemptOrBecomesUnknown(t *testing.T) {
	for _, phase := range []string{"before_begin", "before_http", "accepted", "transport_unknown"} {
		t.Run(phase, func(t *testing.T) {
			at := time.Now().UTC().Add(time.Minute)
			backend := &marketAdmissionTestBackend{}
			if phase == "before_begin" {
				backend.reserveErr = &notificationdelivery.AdmissionDeferred{RetryAt: at, Reason: "budget_wait"}
			}
			if phase == "before_http" {
				backend.checkErr = &notificationdelivery.AdmissionDeferred{RetryAt: at, Reason: "new_shared_server_block"}
			}
			admission, err := notificationdelivery.NewAdmission(backend, "local-test-bot", 5)
			if err != nil {
				t.Fatal(err)
			}
			sender := &marketAdmissionTestSender{admission: admission}
			if phase == "transport_unknown" {
				sender.sendError = errors.New("connection lost after Do")
			}
			repository := &marketAdmissionTestRepository{frozenDeliveryRepository: &frozenDeliveryRepository{frozen: &store.MarketDeliveryPayload{SnapshotID: 1,
				Payload: notificationdelivery.Payload{ChatID: "owner", ChatType: "private", Text: "immutable payload"}}}}
			service := New(Dependencies{Repository: repository, Notifications: sender}, Settings{})
			sent, attempted, err := service.deliverWithAttempt(context.Background(), store.MarketDeliveryClaim{Kind: "realtime", ID: 1, LeaseToken: 2})
			if phase == "before_begin" || phase == "before_http" {
				wantBegin := 0
				if phase == "before_http" {
					wantBegin = 1
				}
				if sent || attempted || err != nil || sender.httpCalls != 0 || repository.begins != wantBegin || len(repository.outcomes) != 0 || len(repository.deferredAfterBegin) != 1 || repository.deferredAfterBegin[0] != (wantBegin == 1) || !repository.retryAt.Equal(at) {
					t.Fatalf("known-unsent admission burned attempt: sent=%t attempted=%t begins=%d HTTP=%d outcomes=%v waits=%v err=%v", sent, attempted, repository.begins, sender.httpCalls, repository.outcomes, repository.deferredAfterBegin, err)
				}
				return
			}
			if !attempted || sender.httpCalls != 1 || repository.begins != 1 || backend.checks != 1 || len(repository.deferredAfterBegin) != 0 || backend.releases != 0 {
				t.Fatalf("real HTTP attempt was refunded or gated twice: attempted=%t HTTP=%d checks=%d waits=%v releases=%d", attempted, sender.httpCalls, backend.checks, repository.deferredAfterBegin, backend.releases)
			}
			if phase == "accepted" && (!sent || err != nil || len(repository.outcomes) != 1 || repository.outcomes[0] != "sent") {
				t.Fatal(sent, err, repository.outcomes)
			}
			if phase == "transport_unknown" && (sent || err == nil || len(repository.outcomes) != 1 || repository.outcomes[0] != notificationdelivery.Unknown) {
				t.Fatal(sent, err, repository.outcomes)
			}
		})
	}
}
