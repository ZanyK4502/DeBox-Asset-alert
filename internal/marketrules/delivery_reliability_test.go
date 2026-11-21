package marketrules

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func (f *tradeLaneTestRepository) LoadMarketTradeEvaluationBatchAfter(ctx context.Context, rule store.MarketRule, target store.MarketRuleTarget, last int64, at time.Time, sourceAfter *time.Time) (store.MarketTradeEvaluationBatch, error) {
	return f.LoadMarketTradeEvaluationBatch(ctx, rule, target, last, at)
}

// The original fake now models the single atomic repository boundary.
func (f *tradeLaneTestRepository) CreateMarketRuleEventAndEnqueue(ctx context.Context, p store.CreateMarketRuleEventParams) (store.MarketRuleEvent, bool, error) {
	event, created, err := f.CreateMarketRuleEvent(ctx, p)
	if err == nil && created {
		if p.NotificationStatus == "staged" {
			f.stage++
		}
		f.combined++
	}
	return event, created, err
}
func (f *tradeActivationCycleRepository) GetMarketDeliveryPayload(context.Context, store.MarketDeliveryClaim) (*store.MarketDeliveryPayload, error) {
	return nil, nil
}
func (f *tradeActivationCycleRepository) PrepareMarketDelivery(_ context.Context, _ store.MarketDeliveryClaim, p store.MarketDeliveryPayload) (store.MarketDeliveryPayload, error) {
	return p, nil
}
func (f *tradeActivationCycleRepository) BeginMarketDeliverySend(context.Context, store.MarketDeliveryClaim) error {
	return nil
}
func (f *tradeActivationCycleRepository) CompleteMarketDeliveryResult(ctx context.Context, c store.MarketDeliveryClaim, id *string, failure *notificationdelivery.Failure) error {
	note := ""
	if failure != nil {
		note = failure.Error()
	}
	return f.CompleteMarketDelivery(ctx, c, id, note)
}

type frozenDeliveryRepository struct {
	Repository
	delivery                 store.MarketNotificationDelivery
	frozen                   *store.MarketDeliveryPayload
	loads, snapshots, begins int
	outcomes                 []string
	acknowledgementError     error
}

func (f *frozenDeliveryRepository) GetMarketDeliveryPayload(context.Context, store.MarketDeliveryClaim) (*store.MarketDeliveryPayload, error) {
	return f.frozen, nil
}
func (f *frozenDeliveryRepository) LoadMarketDelivery(context.Context, store.MarketDeliveryClaim) (store.MarketNotificationDelivery, error) {
	f.loads++
	return f.delivery, nil
}
func (f *frozenDeliveryRepository) CreateNotificationDetailSnapshot(_ context.Context, p store.CreateNotificationDetailSnapshotParams) (store.NotificationDetailSnapshot, error) {
	f.snapshots++
	return store.NotificationDetailSnapshot{ID: 1, PublicID: "nd_1111111111111111111111111111111111111111", NotificationText: p.NotificationText,
		NotificationChatID: p.NotificationChatID, NotificationChatType: p.NotificationChatType, NotificationLanguage: p.NotificationLanguage}, nil
}
func (f *frozenDeliveryRepository) PrepareMarketDelivery(_ context.Context, _ store.MarketDeliveryClaim, p store.MarketDeliveryPayload) (store.MarketDeliveryPayload, error) {
	if f.frozen == nil {
		copy := p
		f.frozen = &copy
	}
	return *f.frozen, nil
}
func (f *frozenDeliveryRepository) BeginMarketDeliverySend(context.Context, store.MarketDeliveryClaim) error {
	f.begins++
	return nil
}
func (f *frozenDeliveryRepository) CompleteMarketDeliveryResult(_ context.Context, _ store.MarketDeliveryClaim, _ *string, failure *notificationdelivery.Failure) error {
	kind := "sent"
	if failure != nil {
		kind = failure.Kind
	}
	f.outcomes = append(f.outcomes, kind)
	return f.acknowledgementError
}

type frozenDeliverySender struct {
	payloads []notificationdelivery.Payload
	fail     error
}

type marketCancellingContextSender struct{ calls int }

func (s *marketCancellingContextSender) SendNotification(string, string, string) (string, error) {
	panic("context-free sender must not be used")
}
func (s *marketCancellingContextSender) SendNotificationContext(ctx context.Context, _ string, _ string, _ string) (string, error) {
	s.calls++
	<-ctx.Done()
	return "", &notificationdelivery.Failure{Kind: notificationdelivery.Unknown, Code: "context_cancelled_after_send_started", Cause: ctx.Err()}
}

type marketDrainRepository struct {
	*tradeActivationCycleRepository
	failure *notificationdelivery.Failure
}

func (f *marketDrainRepository) CompleteMarketDeliveryResult(_ context.Context, _ store.MarketDeliveryClaim, _ *string, failure *notificationdelivery.Failure) error {
	f.failure = failure
	f.deliveryCompleted = true
	return nil
}
func TestMarketNetworkDrainUsesCancellationWithoutRetryingUnknownSend(t *testing.T) {
	service, base, _ := newTradeLaneTestService(true)
	service.settings.Enabled = true
	repo := &marketDrainRepository{tradeActivationCycleRepository: &tradeActivationCycleRepository{tradeLaneTestRepository: base}}
	sender := &marketCancellingContextSender{}
	service.repository = repo
	service.notifications = sender
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	result, _ := service.RunCycle(ctx)
	if time.Since(start) > time.Second || sender.calls != 1 || result.DeliveriesAttempted != 1 || repo.failure == nil || repo.failure.Kind != notificationdelivery.Unknown {
		t.Fatalf("network drain escaped context or resent: elapsed=%s calls=%d result=%#v failure=%#v", time.Since(start), sender.calls, result, repo.failure)
	}
}

func (f *frozenDeliverySender) SendNotification(chat, kind, text string) (string, error) {
	return f.SendNotificationWithAction(chat, kind, text, "", "")
}
func (f *frozenDeliverySender) SendNotificationWithAction(chat, kind, text, action, url string) (string, error) {
	f.payloads = append(f.payloads, notificationdelivery.Payload{ChatID: chat, ChatType: kind, Text: text, ActionText: action, ActionURL: url})
	return "message-id", f.fail
}

func TestMarketDeliveryRetriesUseFirstFrozenTextDestinationAndAction(t *testing.T) {
	at := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	repo := &frozenDeliveryRepository{delivery: store.MarketNotificationDelivery{Kind: "realtime", ID: 1, DeBoxUserID: "owner",
		NotificationChatID: "original-chat", NotificationChatType: "private", NotificationLanguage: "en", Timezone: "UTC",
		Project: store.MarketProject{TokenSymbol: "ORIGINAL"}, Rule: &store.MarketRule{RuleType: "market_price_above", ThresholdValue: "1"},
		Event: &store.MarketEvent{EventType: "price_above", OccurredAt: at}, StartsAt: at, EndsAt: at}}
	sender := &frozenDeliverySender{fail: &notificationdelivery.Failure{Kind: notificationdelivery.Retryable, Code: "rate_limited"}}
	service := New(Dependencies{Repository: repo, Notifications: sender, PublicAppURL: "https://original.example"}, Settings{})
	service.now = func() time.Time { return at }
	claim := store.MarketDeliveryClaim{Kind: "realtime", ID: 1, LeaseToken: 1}
	if sent, err := service.deliver(context.Background(), claim); sent || err == nil {
		t.Fatalf("retryable result=%t,%v", sent, err)
	}
	repo.delivery.NotificationChatID = "new-chat"
	repo.delivery.NotificationLanguage = "zh"
	repo.delivery.Project.TokenSymbol = "CHANGED"
	service.publicAppURL = "https://changed.example"
	service.now = func() time.Time { return at.Add(time.Hour) }
	sender.fail = nil
	claim.LeaseToken++
	if sent, err := service.deliver(context.Background(), claim); !sent || err != nil {
		t.Fatalf("retry result=%t,%v", sent, err)
	}
	if repo.loads != 1 || repo.snapshots != 1 || repo.begins != 2 || len(sender.payloads) != 2 || !reflect.DeepEqual(sender.payloads[0], sender.payloads[1]) {
		t.Fatalf("retry rebuilt frozen evidence: loads=%d snapshots=%d payloads=%#v", repo.loads, repo.snapshots, sender.payloads)
	}
	if !reflect.DeepEqual(repo.outcomes, []string{notificationdelivery.Retryable, "sent"}) {
		t.Fatal(repo.outcomes)
	}
}

func TestMarketDeliveryUnknownAndAcknowledgementFailureNeverSendTwice(t *testing.T) {
	for _, ackFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "transport_unknown", true: "database_ack_failed"}[ackFailure], func(t *testing.T) {
			frozen := store.MarketDeliveryPayload{SnapshotID: 1, Payload: notificationdelivery.Payload{ChatID: "owner", ChatType: "private", Text: "frozen"}}
			repo := &frozenDeliveryRepository{frozen: &frozen}
			sender := &frozenDeliverySender{}
			if ackFailure {
				repo.acknowledgementError = errors.New("database acknowledgement failed")
			} else {
				sender.fail = errors.New("connection lost after request")
			}
			service := New(Dependencies{Repository: repo, Notifications: sender}, Settings{})
			sent, err := service.deliver(context.Background(), store.MarketDeliveryClaim{Kind: "stage", ID: 1, LeaseToken: 1})
			if sent || err == nil || len(sender.payloads) != 1 || repo.begins != 1 {
				t.Fatalf("attempt result %t/%v calls=%d", sent, err, len(sender.payloads))
			}
			if ackFailure {
				var accepted *MarketDeliveryAcknowledgementError
				if !errors.As(err, &accepted) || accepted.Kind != "stage" || accepted.SourceID != 1 || accepted.LeaseToken != 1 || accepted.AcceptedMessageID != "message-id" {
					t.Fatalf("accepted acknowledgement evidence lost: %#v/%v", accepted, err)
				}
			}
			want := notificationdelivery.Unknown
			if ackFailure {
				want = "sent"
			}
			if len(repo.outcomes) != 1 || repo.outcomes[0] != want {
				t.Fatal(repo.outcomes)
			}
		})
	}
}

type marketDeferredRepairRepository struct {
	*tradeActivationCycleRepository
}

func (f *marketDeferredRepairRepository) ReconcileMarketNotificationSources(context.Context) error {
	return store.ErrMarketDeliveryBudget
}
func TestMarketMaintenanceBudgetDefersRepairWithoutBlockingReadyDelivery(t *testing.T) {
	service, base, _ := newTradeLaneTestService(true)
	service.settings.Enabled = true
	repo := &marketDeferredRepairRepository{&tradeActivationCycleRepository{tradeLaneTestRepository: base}}
	sender := &tradeActivationNotifications{}
	service.repository = repo
	service.notifications = sender
	result, err := service.RunCycle(context.Background())
	if err != nil || !result.NotificationMaintenanceDeferred || result.DeliveriesSent != 1 || sender.calls != 1 {
		t.Fatalf("deferred repair blocked known work: %#v/%v calls=%d", result, err, sender.calls)
	}
}

type marketBadRuleRepository struct {
	*tradeActivationCycleRepository
	bad error
}

func (f *marketBadRuleRepository) GetMarketProject(ctx context.Context, id int64, owner string) (*store.MarketProject, error) {
	if id == 999 {
		return nil, f.bad
	}
	return f.tradeLaneTestRepository.GetMarketProject(ctx, id, owner)
}
func TestMarketBadRuleDoesNotBlockIndependentRulesOrAlreadyQueuedDelivery(t *testing.T) {
	service, base, rule := newTradeLaneTestService(true)
	service.settings.Enabled = true
	bad := errors.New("injected independent rule read failure")
	good := rule
	good.ID++
	good.RuleType = "market_price_above"
	rule.MarketProjectID = 999
	repo := &marketBadRuleRepository{tradeActivationCycleRepository: &tradeActivationCycleRepository{tradeLaneTestRepository: base, rules: []store.MarketRule{rule, good}}, bad: bad}
	sender := &tradeActivationNotifications{}
	service.repository = repo
	service.notifications = sender
	result, err := service.RunCycle(context.Background())
	if !errors.Is(err, bad) || !repo.otherRuleEvaluated || result.DeliveriesSent != 1 || sender.calls != 1 {
		t.Fatalf("bad rule blocked independent work: %#v/%v evaluated=%t calls=%d", result, err, repo.otherRuleEvaluated, sender.calls)
	}
}
