package marketrules

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type tradeActivationCycleRepository struct {
	*tradeLaneTestRepository
	rules              []store.MarketRule
	otherRuleEvaluated bool
	deliveryCompleted  bool
	deferredRules      []int64
	deferFailure       error
}

func (f *tradeActivationCycleRepository) DeferMarketRuleEvaluation(_ context.Context, id int64, delay time.Duration) error {
	if delay != time.Minute {
		return errors.New("incomplete rule retry exceeded confirmed resource boundary")
	}
	f.deferredRules = append(f.deferredRules, id)
	return f.deferFailure
}

func (f *tradeActivationCycleRepository) ListEnabledMarketRules(context.Context, int) ([]store.MarketRule, error) {
	return f.rules, nil
}
func (f *tradeActivationCycleRepository) ApplyExpiredEntitlementFallbacks(context.Context) (int64, error) {
	return 0, nil
}
func (f *tradeActivationCycleRepository) ReconcileMarketNotificationSources(context.Context) error {
	return nil
}
func (f *tradeActivationCycleRepository) RecoverStaleMarketDeliveries(context.Context, time.Duration) (int64, error) {
	return 0, nil
}
func (f *tradeActivationCycleRepository) ProcessPendingWatchCombinationTriggers(context.Context, int) (int64, error) {
	return 0, nil
}
func (f *tradeActivationCycleRepository) ListMarketRuleTargets(ctx context.Context, rule store.MarketRule, project store.MarketProject) ([]store.MarketRuleTarget, error) {
	if rule.RuleType == plans.MarketPriceAbove {
		f.otherRuleEvaluated = true
		return nil, nil
	}
	return f.tradeLaneTestRepository.ListMarketRuleTargets(ctx, rule, project)
}
func (f *tradeActivationCycleRepository) ClaimMarketDeliveries(context.Context, int) ([]store.MarketDeliveryClaim, error) {
	if f.deliveryCompleted {
		return nil, nil
	}
	return []store.MarketDeliveryClaim{{Kind: "stage", ID: 20}}, nil
}
func (f *tradeActivationCycleRepository) LoadMarketDelivery(context.Context, store.MarketDeliveryClaim) (store.MarketNotificationDelivery, error) {
	return store.MarketNotificationDelivery{Kind: "stage", ID: 20, DeBoxUserID: "fixture-owner", Project: f.project}, nil
}
func (f *tradeActivationCycleRepository) CreateNotificationDetailSnapshot(_ context.Context, p store.CreateNotificationDetailSnapshotParams) (store.NotificationDetailSnapshot, error) {
	return store.NotificationDetailSnapshot{ID: 1, PublicID: "fixture-snapshot", NotificationText: p.NotificationText, NotificationChatID: "fixture-owner", NotificationChatType: "private"}, nil
}
func (f *tradeActivationCycleRepository) CompleteMarketDelivery(_ context.Context, _ store.MarketDeliveryClaim, messageID *string, failure string) error {
	f.deliveryCompleted = messageID != nil && failure == ""
	return nil
}

type tradeActivationNotifications struct{ calls int }

func (f *tradeActivationNotifications) SendNotification(string, string, string) (string, error) {
	f.calls++
	return "fixture-message", nil
}

func TestTradeActivationUninitializedRuleDoesNotBlockOtherRulesOrDelivery(t *testing.T) {
	service, base, tradeRule := newTradeLaneTestService(true)
	before := make(map[string]string)
	for key, target := range base.states {
		before[key] = string(target.State)
	}
	priceRule := tradeRule
	priceRule.ID, priceRule.RuleType = tradeRule.ID+1, plans.MarketPriceAbove
	repository := &tradeActivationCycleRepository{tradeLaneTestRepository: base, rules: []store.MarketRule{tradeRule, priceRule}}
	notifications := &tradeActivationNotifications{}
	service.repository, service.notifications, service.settings.Enabled = repository, notifications, true
	result, err := service.RunCycle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.TradeRulesAwaitingActivation != 1 || result.RulesEvaluated != 1 || !repository.otherRuleEvaluated ||
		result.DeliveriesAttempted != 1 || result.DeliveriesSent != 1 || notifications.calls != 1 || !repository.deliveryCompleted {
		t.Fatalf("uninitialized lane blocked independent work: result=%#v other=%v delivered=%v calls=%d", result, repository.otherRuleEvaluated, repository.deliveryCompleted, notifications.calls)
	}
	if base.initializations != 0 || base.tradeReads != 0 || len(base.changedTargets) != 0 {
		t.Fatal("uninitialized lane wrote state or consumed trade facts")
	}
	if len(repository.deferredRules) != 1 || repository.deferredRules[0] != tradeRule.ID {
		t.Fatalf("uninitialized lane was not deferred once: %v", repository.deferredRules)
	}
	for key, state := range before {
		if string(base.states[key].State) != state {
			t.Fatalf("legacy state changed for %s", key)
		}
	}
}

func TestTradeActivationBackoffWriteFailureIsReportedWithoutAdvancingState(t *testing.T) {
	service, base, rule := newTradeLaneTestService(true)
	failure := errors.New("backoff database write unavailable")
	repository := &tradeActivationCycleRepository{tradeLaneTestRepository: base, rules: []store.MarketRule{rule}, deferFailure: failure}
	service.repository, service.notifications, service.settings.Enabled = repository, &tradeActivationNotifications{}, true
	if _, err := service.RunCycle(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("backoff write failure hidden: %v", err)
	}
	if base.initializations != 0 || base.tradeReads != 0 || len(base.changedTargets) != 0 || len(base.created) != 0 {
		t.Fatal("backoff failure advanced business state")
	}
}

func TestTradeActivationPreflightsAllDeploymentsBeforeConsumption(t *testing.T) {
	service, repository, rule := newTradeLaneTestService(true)
	secondDeployment, secondPool := int64(8), int64(30)
	second := repository.targets[0]
	second.TargetKey, second.MarketProjectDeploymentID, second.MarketPoolID = "deployment:8/pool:3", &secondDeployment, &secondPool
	second.ChainKey, second.ChainID = "eth", 1
	repository.targets = append(repository.targets, second)
	first := MergeTradeRuleTargets(repository.targets)[0]
	if _, err := service.InitializeTradeRuleState(context.Background(), rule.ID, first, 100); err != nil {
		t.Fatal(err)
	}
	stateBefore := append(json.RawMessage(nil), repository.states[first.TargetKey].State...)
	if _, err := service.evaluateRule(context.Background(), rule); !errors.Is(err, ErrTradeRuleStateUninitialized) {
		t.Fatalf("partially initialized rule error = %v", err)
	}
	if repository.tradeReads != 0 || len(repository.changedTargets) != 0 || len(repository.created) != 0 ||
		!bytes.Equal(repository.states[first.TargetKey].State, stateBefore) {
		t.Fatal("first deployment consumed or wrote data before another deployment was initialized")
	}
}

func TestTradeActivationWarningsAreBoundedAndReportCountChanges(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	runner := &Runner{}
	start := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	for _, seconds := range []int{0, 5, 59, 60} {
		runner.reportTradeActivation(logger, CycleResult{TradeRulesAwaitingActivation: 2}, start.Add(time.Duration(seconds)*time.Second))
	}
	if count := strings.Count(logs.String(), "market trade rules await explicit activation"); count != 2 {
		t.Fatalf("unchanged backlog generated %d warnings, want 2 in one minute", count)
	}
	runner.reportTradeActivation(logger, CycleResult{TradeRulesAwaitingActivation: 3}, start.Add(61*time.Second))
	runner.reportTradeActivation(logger, CycleResult{}, start.Add(62*time.Second))
	runner.reportTradeActivation(logger, CycleResult{}, start.Add(63*time.Second))
	runner.reportTradeActivation(logger, CycleResult{TradeRulesAwaitingActivation: 2}, start.Add(64*time.Second))
	if strings.Count(logs.String(), "market trade rules await explicit activation") != 4 ||
		strings.Count(logs.String(), "market trade activation backlog cleared") != 1 {
		t.Fatalf("backlog state changes were not reported once: %s", logs.String())
	}
}

func TestTradeActivationSkippedLockDoesNotClearBacklogOrRepeatWarning(t *testing.T) {
	service, _, _ := newTradeLaneTestService(true)
	service.settings.Enabled = true
	service.notifications = &tradeActivationNotifications{}
	service.tryLock = func(context.Context, string) (Lock, bool, error) { return nil, false, nil }
	result, err := service.RunCycle(context.Background())
	if err != nil || !result.LockSkipped || result.TradeRulesAwaitingActivation != 0 {
		t.Fatalf("skipped lock should be distinguishable from a completed empty cycle: %#v, %v", result, err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	runner := &Runner{}
	start := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	runner.reportTradeActivation(logger, CycleResult{TradeRulesAwaitingActivation: 2}, start)
	runner.reportTradeActivation(logger, result, start.Add(5*time.Second))
	runner.reportTradeActivation(logger, CycleResult{TradeRulesAwaitingActivation: 2}, start.Add(10*time.Second))
	if runner.lastActivationCount != 2 || !runner.lastActivationWarning.Equal(start) ||
		strings.Count(logs.String(), "market trade rules await explicit activation") != 1 ||
		strings.Contains(logs.String(), "market trade activation backlog cleared") {
		t.Fatalf("lock skip changed activation observations: %s", logs.String())
	}
}
