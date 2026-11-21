package marketrules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type tradeLaneTestRepository struct {
	Repository
	multiChainRuleRepository
	project                          store.MarketProject
	targets                          []store.MarketRuleTarget
	states                           map[string]store.MarketRuleTarget
	tradeEvents, legacyEvents        []store.MarketEvent
	tradeEventsByTarget              map[string][]store.MarketEvent
	holders                          []store.MarketHolder
	tradeReads, legacyReads          int
	initializations, stage, combined int
	changedTargets                   []string
	loadedKeys                       []string
	created                          []store.CreateMarketRuleEventParams
	updatedRuleState                 json.RawMessage
	incompleteTargets                map[string]bool
	windowChecks                     []tradeWindowTestCheck
}

type tradeWindowTestCheck struct {
	targetKey string
	cursor    int64
	from, to  time.Time
	history   []int64
}

type tradeFactsUnavailableRepository struct {
	Repository
	project store.MarketProject
}

func (f tradeFactsUnavailableRepository) GetMarketProject(context.Context, int64, string) (*store.MarketProject, error) {
	return &f.project, nil
}

func TestTradeLaneDoesNotFallBackWhenFactsRepositoryIsMissing(t *testing.T) {
	project := tradeFactsTestProject()
	project.Status = "active"
	service := New(Dependencies{Repository: tradeFactsUnavailableRepository{project: project}}, Settings{TradeFactsEnabled: true})
	if _, err := service.evaluateRule(context.Background(), tradeFactsTestRule(0, 0)); err == nil {
		t.Fatal("enabled trade-facts lane silently fell back to legacy evaluation")
	}
}

func (f *tradeLaneTestRepository) GetMarketProject(context.Context, int64, string) (*store.MarketProject, error) {
	return &f.project, nil
}
func (f *tradeLaneTestRepository) ListMarketRuleTargets(context.Context, store.MarketRule, store.MarketProject) ([]store.MarketRuleTarget, error) {
	return f.targets, nil
}
func (f *tradeLaneTestRepository) LoadMarketRuleTargetState(_ context.Context, _ int64, target store.MarketRuleTarget) (store.MarketRuleTarget, bool, error) {
	f.loadedKeys = append(f.loadedKeys, target.TargetKey)
	state, exists := f.states[target.TargetKey]
	if exists {
		target.State, target.LastTriggeredAt = state.State, state.LastTriggeredAt
	}
	return target, exists, nil
}
func (f *tradeLaneTestRepository) UpdateMarketRuleTargetState(_ context.Context, _ int64, target store.MarketRuleTarget, state json.RawMessage, triggered bool) error {
	target.State = append(json.RawMessage(nil), state...)
	f.states[target.TargetKey] = target
	f.changedTargets = append(f.changedTargets, target.TargetKey)
	return nil
}
func (f *tradeLaneTestRepository) InitializeTradeRuleState(_ context.Context, _ int64, target store.MarketRuleTarget, state json.RawMessage) (bool, error) {
	f.initializations++
	if _, exists := f.states[target.TargetKey]; exists {
		return false, nil
	}
	target.State = append(json.RawMessage(nil), state...)
	legacyPrefix := strings.TrimSuffix(target.TargetKey, "/trades:v2") + "/pool:"
	for key, previous := range f.states {
		if strings.HasPrefix(key, legacyPrefix) && previous.LastTriggeredAt != nil &&
			(target.LastTriggeredAt == nil || previous.LastTriggeredAt.After(*target.LastTriggeredAt)) {
			target.LastTriggeredAt = previous.LastTriggeredAt
		}
	}
	f.states[target.TargetKey] = target
	return true, nil
}
func (f *tradeLaneTestRepository) ListMarketTradeEventsForTarget(_ context.Context, target store.MarketRuleTarget, cursor int64, ascending bool, limit int) ([]store.MarketEvent, error) {
	f.tradeReads++
	var events []store.MarketEvent
	input := f.tradeEvents
	if perTarget, exists := f.tradeEventsByTarget[target.TargetKey]; exists {
		input = perTarget
	}
	for _, event := range input {
		if ascending && event.TradeReadyID > cursor || !ascending && (cursor == 0 || event.TradeReadyID < cursor) {
			events = append(events, event)
		}
	}
	sort.Slice(events, func(i, j int) bool {
		if ascending {
			return events[i].TradeReadyID < events[j].TradeReadyID
		}
		return events[i].TradeReadyID > events[j].TradeReadyID
	})
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}
func (f *tradeLaneTestRepository) MarketTradeWindowContextComplete(_ context.Context, target store.MarketRuleTarget, cursor int64, from, to time.Time, history []int64) (bool, error) {
	f.windowChecks = append(f.windowChecks, tradeWindowTestCheck{targetKey: target.TargetKey, cursor: cursor, from: from, to: to, history: append([]int64(nil), history...)})
	return !f.incompleteTargets[target.TargetKey], nil
}
func (f *tradeLaneTestRepository) LoadMarketTradeEvaluationBatch(ctx context.Context, rule store.MarketRule, target store.MarketRuleTarget, cursor int64, now time.Time) (store.MarketTradeEvaluationBatch, error) {
	current, err := f.ListMarketTradeEventsForTarget(ctx, target, cursor, true, store.MarketRuleCurrentEventLimit)
	if err != nil {
		return store.MarketTradeEvaluationBatch{}, err
	}
	result := store.MarketTradeEvaluationBatch{Current: current}
	if len(current) == 0 {
		return result, nil
	}
	input := f.tradeEvents
	if perTarget, exists := f.tradeEventsByTarget[target.TargetKey]; exists {
		input = perTarget
	}
	from, through := current[0].OccurredAt, current[0].OccurredAt
	for _, event := range current[1:] {
		if event.OccurredAt.Before(from) {
			from = event.OccurredAt
		}
		if event.OccurredAt.After(through) {
			through = event.OccurredAt
		}
	}
	from = from.Add(-time.Duration(defaultWindow(rule.WindowMinutes, 15)) * time.Minute)
	var history []int64
	direction := "buy"
	if rule.RuleType == plans.MarketConsecutiveLargeSell {
		direction = "sell"
	}
	for _, event := range input {
		if event.TradeReadyID <= cursor {
			history = append(history, event.TradeReadyID)
		}
		if event.EventType != direction || event.Confirmed != 1 || event.Reorged != 0 {
			continue
		}
		unknown := tradeProjectionUnavailable(rule, event, now) != ""
		result.Candidates = append(result.Candidates, store.MarketTradeWindowCandidate{TradeReadyID: event.TradeReadyID, ParentID: event.ID, OccurredAt: event.OccurredAt,
			Qualified: !unknown && eventThresholdMatch(rule, f.project, nil, event), Unknown: unknown})
	}
	f.windowChecks = append(f.windowChecks, tradeWindowTestCheck{targetKey: target.TargetKey, cursor: cursor, from: from, to: through, history: history})
	if f.incompleteTargets[target.TargetKey] {
		return store.MarketTradeEvaluationBatch{}, fmt.Errorf("%w: fake_scan_coverage", store.ErrMarketTradeWindowUnavailable)
	}
	return result, nil
}
func (f *tradeLaneTestRepository) ListMarketEventsForTarget(context.Context, store.MarketRuleTarget, int64, bool, int) ([]store.MarketEvent, error) {
	f.legacyReads++
	return f.legacyEvents, nil
}
func (f *tradeLaneTestRepository) ListMarketSnapshotsForTarget(context.Context, store.MarketRuleTarget, int) ([]store.MarketSnapshot, error) {
	return nil, nil
}
func (f *tradeLaneTestRepository) ListMarketHoldersForTarget(context.Context, store.MarketRuleTarget, int) ([]store.MarketHolder, error) {
	return f.holders, nil
}
func (f *tradeLaneTestRepository) ListMarketAddressLabelsForTarget(context.Context, int64, string, store.MarketRuleTarget) ([]store.MarketAddressLabel, error) {
	return nil, nil
}
func (f *tradeLaneTestRepository) CreateMarketRuleEvent(_ context.Context, params store.CreateMarketRuleEventParams) (store.MarketRuleEvent, bool, error) {
	f.created = append(f.created, params)
	return store.MarketRuleEvent{ID: int64(len(f.created))}, true, nil
}
func (f *tradeLaneTestRepository) UpdateMarketRuleState(_ context.Context, _ int64, state json.RawMessage, _ bool) (store.MarketRule, error) {
	f.updatedRuleState = append(json.RawMessage(nil), state...)
	return store.MarketRule{}, nil
}
func (f *tradeLaneTestRepository) RecordMarketStageTrigger(context.Context, int64) (store.MarketStageWindow, bool, error) {
	f.stage++
	return store.MarketStageWindow{}, true, nil
}
func (f *tradeLaneTestRepository) RecordMarketCombinationTrigger(context.Context, store.RecordMarketCombinationTriggerParams) (int64, error) {
	f.combined++
	return 0, nil
}

func newTradeLaneTestService(enabled bool) (*Service, *tradeLaneTestRepository, store.MarketRule) {
	deployment, projectPoolA, projectPoolB, poolA, poolB := int64(7), int64(1), int64(2), int64(10), int64(20)
	project := tradeFactsTestProject()
	project.Status = "active"
	repository := &tradeLaneTestRepository{project: project, states: make(map[string]store.MarketRuleTarget)}
	for _, pair := range [][2]*int64{{&projectPoolA, &poolA}, {&projectPoolB, &poolB}} {
		key := "deployment:7/pool:1"
		if *pair[0] == 2 {
			key = "deployment:7/pool:2"
		}
		target := store.MarketRuleTarget{TargetKey: key, MarketProjectDeploymentID: &deployment,
			MarketProjectPoolID: pair[0], MarketPoolID: pair[1], ChainKey: "bsc", ChainID: 56,
			TokenAddress: tradeFactsTestToken, TokenSymbol: "TEST", TokenDecimals: 6,
			State: json.RawMessage(`{"last_event_id":999,"condition_active":true}`)}
		repository.targets = append(repository.targets, target)
		repository.states[key] = target
	}
	rule := tradeFactsTestRule(100, 100)
	rule.State = json.RawMessage(`{"last_event_id":777,"condition_active":true}`)
	rule.MarketProjectID, rule.DeBoxUserID, rule.CooldownScope = project.ID, "owner", "chain"
	service := New(Dependencies{Repository: repository}, Settings{TradeFactsEnabled: enabled})
	service.now = func() time.Time { return time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC) }
	return service, repository, rule
}

func TestTradeLaneRequiresExplicitFenceAndLeavesLegacyStatesUntouched(t *testing.T) {
	service, repository, rule := newTradeLaneTestService(true)
	before := make(map[string]string)
	for key, target := range repository.states {
		before[key] = string(target.State)
	}
	if _, err := service.evaluateRule(context.Background(), rule); !errors.Is(err, ErrTradeRuleStateUninitialized) {
		t.Fatalf("uninitialized lane error = %v", err)
	}
	if repository.initializations != 0 || repository.tradeReads != 0 || len(repository.changedTargets) != 0 {
		t.Fatal("runtime silently initialized or consumed the trade lane")
	}
	merged := MergeTradeRuleTargets(repository.targets)
	if len(merged) != 1 || merged[0].TargetKey != "deployment:7/trades:v2" ||
		!reflect.DeepEqual(merged[0].TradePoolIDs, []int64{10, 20}) || merged[0].MarketPoolID != nil {
		t.Fatalf("selected pools were not merged per deployment: %#v", merged)
	}
	initialized, err := service.InitializeTradeRuleState(context.Background(), rule.ID, merged[0], 100)
	if err != nil || !initialized {
		t.Fatalf("explicit activation = %v, %v", initialized, err)
	}
	if changed, err := service.InitializeTradeRuleState(context.Background(), rule.ID, merged[0], 999); err != nil || changed {
		t.Fatalf("repeat activation overwrote the fence: %v, %v", changed, err)
	}
	rule.DeliveryMode = "stage"
	repository.tradeEvents = []store.MarketEvent{tradeFactsTestEvent(5, 101, service.now())}
	created, err := service.evaluateRule(context.Background(), rule)
	if err != nil || created != 1 || len(repository.created) != 1 || repository.stage != 1 || repository.combined != 1 {
		t.Fatalf("one logical trade should count once: created=%d, stage=%d, combined=%d, err=%v", created, repository.stage, repository.combined, err)
	}
	if repository.created[0].MarketEventID != 5 || *repository.created[0].CurrentValue != "12000" {
		t.Fatalf("parent identity or selected-pool amount was lost: %#v", repository.created[0])
	}
	if string(repository.updatedRuleState) != string(rule.State) {
		t.Fatal("new lane overwrote the global legacy state")
	}
	for key, state := range before {
		if string(repository.states[key].State) != state {
			t.Fatalf("legacy pool watermark changed: %s", key)
		}
	}
	if len(repository.changedTargets) != 1 || repository.changedTargets[0] != merged[0].TargetKey {
		t.Fatalf("new lane updated unrelated target states: %#v", repository.changedTargets)
	}
}

func TestTradeLaneDisabledAndNonTradeRulesKeepOriginalRoute(t *testing.T) {
	for _, test := range []struct {
		name, kind string
		enabled    bool
	}{{"disabled", plans.MarketLargeBuy, false}, {"price unaffected", plans.MarketPriceAbove, true}} {
		t.Run(test.name, func(t *testing.T) {
			service, repository, rule := newTradeLaneTestService(test.enabled)
			rule.RuleType = test.kind
			if _, err := service.evaluateRule(context.Background(), rule); err != nil {
				t.Fatal(err)
			}
			if repository.tradeReads != 0 || repository.initializations != 0 || len(repository.changedTargets) != 2 {
				t.Fatalf("legacy route unexpectedly used new lane: %#v", repository.changedTargets)
			}
		})
	}
}

func TestTradeLaneInheritsLatestLegacyChainCooldownWithoutMergingWatermarks(t *testing.T) {
	service, repository, rule := newTradeLaneTestService(true)
	rule.CooldownSeconds = 120
	old, latest := service.now().Add(-time.Minute), service.now().Add(-20*time.Second)
	for key, state := range repository.states {
		state.LastTriggeredAt = &old
		if key == "deployment:7/pool:2" {
			state.LastTriggeredAt = &latest
		}
		repository.states[key] = state
	}
	target := MergeTradeRuleTargets(repository.targets)[0]
	if _, err := service.InitializeTradeRuleState(context.Background(), rule.ID, target, 100); err != nil {
		t.Fatal(err)
	}
	if inherited := repository.states[target.TargetKey].LastTriggeredAt; inherited == nil || !inherited.Equal(latest) {
		t.Fatalf("explicit initialization did not persist the latest legacy cooldown: %v", inherited)
	}
	repository.tradeEvents = []store.MarketEvent{tradeFactsTestEvent(5, 101, service.now())}
	created, err := service.evaluateRule(context.Background(), rule)
	if err != nil || created != 0 || len(repository.created) != 1 ||
		repository.created[0].NotificationStatus != "skipped" || repository.created[0].NotificationError != "cooldown_active" {
		t.Fatalf("legacy cooldown was bypassed: created=%d, rows=%#v, err=%v", created, repository.created, err)
	}
	if !reflect.DeepEqual(repository.loadedKeys, []string{"deployment:7/trades:v2"}) {
		t.Fatalf("trade cycle reread legacy per-pool states: %#v", repository.loadedKeys)
	}
}

func TestMultiChainHolderPercentageUsesTargetSupplyIncludingMissingSupply(t *testing.T) {
	for _, test := range []struct {
		name, supply string
		wantTriggers int64
	}{{"target supply", "10000", 0}, {"threshold matched", "2000", 1}, {"missing target supply", "", 0}} {
		t.Run(test.name, func(t *testing.T) {
			service, repository, rule := newTradeLaneTestService(true)
			primarySupply := "1000"
			repository.project.TotalSupplyRaw = &primarySupply
			rule.RuleType, rule.ThresholdUnit, rule.ThresholdValue = plans.MarketHolderIncrease, "percent", "10"
			target := repository.targets[0]
			target.TargetKey, target.MarketPoolID, target.MarketProjectPoolID = "deployment:7", nil, nil
			target.State = json.RawMessage(`{"last_event_id":0}`)
			if test.supply != "" {
				target.TotalSupplyRaw = &test.supply
			}
			repository.targets = []store.MarketRuleTarget{target}
			repository.states[target.TargetKey] = target
			wallet, raw := "0x2222222222222222222222222222222222222222", "200"
			repository.holders = []store.MarketHolder{{HolderAddress: wallet, ChainID: 56}}
			repository.legacyEvents = []store.MarketEvent{{ID: 5, ChainID: 56, TokenAddress: tradeFactsTestToken,
				EventType: "holder_increase", EventKey: "holder:5", WalletAddress: &wallet, TokenAmountRaw: &raw,
				Confirmed: 1, OccurredAt: service.now(), Metadata: json.RawMessage(`{}`)}}
			created, err := service.evaluateRule(context.Background(), rule)
			if err != nil || created != test.wantTriggers {
				t.Fatalf("target supply result = %d, %v; want %d", created, err, test.wantTriggers)
			}
		})
	}
}
