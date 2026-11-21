package marketrules

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestTradeWindowContextIncompletePreservesStateAndChecksAllCurrentTimes(t *testing.T) {
	service, repository, rule := newTradeLaneTestService(true)
	window := int32(15)
	rule.RuleType, rule.WindowMinutes = plans.MarketConsecutiveLargeBuy, &window
	target := MergeTradeRuleTargets(repository.targets)[0]
	if _, err := service.InitializeTradeRuleState(context.Background(), rule.ID, target, 100); err != nil {
		t.Fatal(err)
	}
	repository.incompleteTargets = map[string]bool{target.TargetKey: true}
	repository.tradeEvents = []store.MarketEvent{
		tradeFactsTestEvent(9, 98, service.now().Add(-30*time.Minute)),
		tradeFactsTestEvent(10, 99, service.now().Add(-2*time.Minute)),
		tradeFactsTestEvent(11, 101, service.now().Add(-time.Minute)),
		tradeFactsTestEvent(12, 102, service.now().Add(-4*time.Minute)),
	}
	before := append([]byte(nil), repository.states[target.TargetKey].State...)
	if _, err := service.evaluateRule(context.Background(), rule); !errors.Is(err, ErrTradeWindowContextIncomplete) {
		t.Fatalf("incomplete window error = %v", err)
	}
	if len(repository.created) != 0 || len(repository.changedTargets) != 0 || repository.updatedRuleState != nil ||
		!bytes.Equal(repository.states[target.TargetKey].State, before) {
		t.Fatal("incomplete window emitted a count or advanced state")
	}
	if len(repository.windowChecks) != 1 {
		t.Fatalf("window checks = %#v", repository.windowChecks)
	}
	check := repository.windowChecks[0]
	if check.cursor != 100 || !check.from.Equal(service.now().Add(-19*time.Minute)) ||
		!check.to.Equal(service.now().Add(-time.Minute)) || !reflect.DeepEqual(check.history, []int64{98, 99}) {
		t.Fatalf("window range ignored out-of-order event time or historical ready IDs: %#v", check)
	}
}

func TestTradeWindowContextPreflightsEveryDeploymentBeforeWriting(t *testing.T) {
	service, repository, rule := newTradeLaneTestService(true)
	rule.RuleType = plans.MarketConsecutiveLargeBuy
	secondDeployment, secondPool := int64(8), int64(30)
	second := repository.targets[0]
	second.TargetKey, second.MarketProjectDeploymentID, second.MarketPoolID = "deployment:8/pool:3", &secondDeployment, &secondPool
	second.ChainKey, second.ChainID = "eth", 1
	repository.targets = append(repository.targets, second)
	for _, target := range MergeTradeRuleTargets(repository.targets) {
		if _, err := service.InitializeTradeRuleState(context.Background(), rule.ID, target, 100); err != nil {
			t.Fatal(err)
		}
	}
	repository.incompleteTargets = map[string]bool{"deployment:8/trades:v2": true}
	repository.tradeEvents = []store.MarketEvent{tradeFactsTestEvent(11, 101, service.now())}
	if _, err := service.evaluateRule(context.Background(), rule); !errors.Is(err, ErrTradeWindowContextIncomplete) {
		t.Fatalf("second deployment window error = %v", err)
	}
	if len(repository.windowChecks) != 2 || len(repository.changedTargets) != 0 || len(repository.created) != 0 {
		t.Fatal("first deployment wrote before all deployment windows were checked")
	}
}

func TestTradeWindowContextIncompleteDoesNotBlockOtherRulesOrDelivery(t *testing.T) {
	service, base, tradeRule := newTradeLaneTestService(true)
	tradeRule.RuleType = plans.MarketConsecutiveLargeBuy
	target := MergeTradeRuleTargets(base.targets)[0]
	if _, err := service.InitializeTradeRuleState(context.Background(), tradeRule.ID, target, 100); err != nil {
		t.Fatal(err)
	}
	base.incompleteTargets = map[string]bool{target.TargetKey: true}
	base.tradeEvents = []store.MarketEvent{tradeFactsTestEvent(11, 101, service.now())}
	before := append([]byte(nil), base.states[target.TargetKey].State...)
	priceRule := tradeRule
	priceRule.ID, priceRule.RuleType = tradeRule.ID+1, plans.MarketPriceAbove
	repository := &tradeActivationCycleRepository{tradeLaneTestRepository: base, rules: []store.MarketRule{tradeRule, priceRule}}
	notifications := &tradeActivationNotifications{}
	service.repository, service.notifications, service.settings.Enabled = repository, notifications, true
	result, err := service.RunCycle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.TradeRulesIncompleteWindow != 1 || result.TradeRulesAwaitingActivation != 0 || result.RulesEvaluated != 1 ||
		!repository.otherRuleEvaluated || result.DeliveriesSent != 1 || notifications.calls != 1 || !repository.deliveryCompleted {
		t.Fatalf("incomplete window blocked independent work: %#v", result)
	}
	if len(base.changedTargets) != 0 || len(base.created) != 0 || !bytes.Equal(base.states[target.TargetKey].State, before) {
		t.Fatal("incomplete window wrote an evaluation or advanced the ready cursor")
	}
}

func TestTradeWindowContextWarningsAreBoundedAndIgnoreSkippedLocks(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	runner := &Runner{}
	start := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	runner.reportTradeWindow(logger, CycleResult{TradeRulesIncompleteWindow: 2}, start)
	runner.reportTradeWindow(logger, CycleResult{LockSkipped: true}, start.Add(5*time.Second))
	runner.reportTradeWindow(logger, CycleResult{TradeRulesIncompleteWindow: 2}, start.Add(10*time.Second))
	if runner.lastWindowCount != 2 || strings.Count(logs.String(), "market trade rules require complete window context") != 1 ||
		strings.Contains(logs.String(), "market trade window context backlog cleared") {
		t.Fatalf("lock skip changed window observations: %s", logs.String())
	}
	runner.reportTradeWindow(logger, CycleResult{TradeRulesIncompleteWindow: 2}, start.Add(time.Minute))
	runner.reportTradeWindow(logger, CycleResult{TradeRulesIncompleteWindow: 1}, start.Add(61*time.Second))
	runner.reportTradeWindow(logger, CycleResult{}, start.Add(62*time.Second))
	runner.reportTradeWindow(logger, CycleResult{}, start.Add(63*time.Second))
	if strings.Count(logs.String(), "market trade rules require complete window context") != 3 ||
		strings.Count(logs.String(), "market trade window context backlog cleared") != 1 {
		t.Fatalf("window backlog changes were not bounded: %s", logs.String())
	}
}
