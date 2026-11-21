package marketrules

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestNotificationBoundaryOldSourceDoesNotConsumeFreshPrivateCooldown(t *testing.T) {
	cutoff := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	old := tradeFactsTestEvent(1, 1, cutoff.Add(-time.Second))
	fresh := tradeFactsTestEvent(2, 2, cutoff)
	rule := store.MarketRule{RuleType: plans.MarketLargeBuy, ThresholdUnit: "token", ThresholdValue: "1", CooldownSeconds: 60}
	result, err := Evaluate(EvaluationInput{Rule: rule, Project: tradeFactsTestProject(), Events: []store.MarketEvent{old, fresh}, Now: cutoff.Add(time.Second), NotificationSourceAfter: &cutoff})
	if err != nil || len(result.Triggers) != 1 || result.Triggers[0].Event.ID != fresh.ID || len(result.Suppressed) != 1 || result.Suppressed[0].Event.ID != old.ID || result.Suppressed[0].Reason != "source precedes recorded recovery cutoff" {
		t.Fatalf("evaluation=%+v err=%v", result, err)
	}
	var state ruleState
	_ = json.Unmarshal(result.State, &state)
	if state.LastEventID != fresh.ID {
		t.Fatalf("source cursor=%+v", state)
	}
}

func TestNotificationBoundaryPreservesProgressConditionAndPendingUntilFreshSource(t *testing.T) {
	cutoff := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	old := tradeFactsTestEvent(2, 2, cutoff.Add(-time.Second))
	old.Metadata = json.RawMessage(`{"protocol":"four_meme","progress_percent":"95"}`)
	state, _ := json.Marshal(ruleState{LastEventID: 1, ConditionActive: true, PendingCrossing: true})
	rule := store.MarketRule{RuleType: plans.MarketFourMemeProgress, ThresholdUnit: "progress", ThresholdValue: "80", State: state}
	input := EvaluationInput{Rule: rule, Project: tradeFactsTestProject(), Events: []store.MarketEvent{old}, Now: cutoff.Add(time.Second), NotificationSourceAfter: &cutoff}
	result, err := Evaluate(input)
	var after ruleState
	_ = json.Unmarshal(result.State, &after)
	if err != nil || len(result.Triggers) != 0 || len(result.Suppressed) != 1 || after.LastEventID != 2 || !after.ConditionActive || !after.PendingCrossing {
		t.Fatalf("old progress changed state: %+v state=%+v err=%v", result, after, err)
	}
	fresh := old
	fresh.ID = 3
	fresh.EventKey = "fresh-progress"
	fresh.OccurredAt = cutoff
	input.Events = append(input.Events, fresh)
	result, err = Evaluate(input)
	if err != nil || len(result.Triggers) != 1 || result.Triggers[0].Event.ID != 3 {
		t.Fatalf("fresh pending lost=%+v err=%v", result, err)
	}
}

func TestNotificationBoundaryRetainsOldFactsInsideFreshCumulativeWindow(t *testing.T) {
	cutoff := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	old := tradeFactsTestEvent(1, 1, cutoff.Add(-time.Second))
	fresh := tradeFactsTestEvent(2, 2, cutoff)
	window := int32(5)
	rule := store.MarketRule{RuleType: plans.MarketConsecutiveLargeBuy, ThresholdUnit: "token", ThresholdValue: "1", TriggerCountThreshold: 2, WindowMinutes: &window, CooldownSeconds: 60}
	result, err := Evaluate(EvaluationInput{Rule: rule, Project: tradeFactsTestProject(), Events: []store.MarketEvent{old, fresh}, Now: cutoff.Add(time.Second), NotificationSourceAfter: &cutoff})
	if err != nil || len(result.Triggers) != 1 || *result.Triggers[0].CurrentValue != "2" || result.Triggers[0].Event.ID != fresh.ID {
		t.Fatalf("old context silently dropped=%+v err=%v", result, err)
	}
	v2 := tradeFactsTestRule(0, 0)
	v2.RuleType = rule.RuleType
	v2.ThresholdUnit = "token"
	v2.ThresholdValue = "1"
	v2.TriggerCountThreshold = 2
	for _, counts := range []map[int64]int64{nil, {2: 2}} {
		result, err = evaluateTradeFactsWithCountsAfter(v2, tradeFactsTestProject(), []store.MarketEvent{old, fresh}, counts, cutoff.Add(time.Second), &cutoff)
		if err != nil || len(result.Triggers) != 1 || *result.Triggers[0].CurrentValue != "2" || result.Triggers[0].Event.ID != fresh.ID {
			t.Fatalf("v2 context dropped counts=%v result=%+v err=%v", counts, result, err)
		}
	}
	bad := old
	bad.Metadata = json.RawMessage(`{}`)
	bad.USDValue = nil
	rule.ThresholdUnit = "usd"
	if _, err := Evaluate(EvaluationInput{Rule: rule, Project: tradeFactsTestProject(), Events: []store.MarketEvent{bad, fresh}, Now: cutoff.Add(time.Second), NotificationSourceAfter: &cutoff}); !errors.Is(err, ErrTradeWindowContextIncomplete) {
		t.Fatalf("unknown historical valuation became zero=%v", err)
	}
}

func TestNotificationBoundaryOldOnlyTradeConsumesReadyWithoutWindowProof(t *testing.T) {
	cutoff := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	old := tradeFactsTestEvent(50, 10, cutoff.Add(-time.Hour))
	old.Metadata = json.RawMessage(`{"trade_projection_unavailable":"coverage_incomplete"}`)
	rule := tradeFactsTestRule(0, 0)
	rule.RuleType = plans.MarketConsecutiveLargeBuy
	rule.TriggerCountThreshold = 2
	result, err := evaluateTradeFactsWithCountsAfter(rule, tradeFactsTestProject(), []store.MarketEvent{old}, map[int64]int64{}, cutoff, &cutoff)
	var state tradeLaneState
	_ = json.Unmarshal(result.State, &state)
	if err != nil || len(result.Triggers) != 0 || len(result.Suppressed) != 1 || result.Suppressed[0].Event.ID != 50 || state.LastTradeReadyID != 10 {
		t.Fatalf("discarded old source blocked progress=%+v state=%+v err=%v", result, state, err)
	}
}

func TestNotificationBoundaryOldSnapshotCannotConsumeFreshCrossing(t *testing.T) {
	cutoff := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	oldSource := cutoff.Add(-time.Second)
	fetched := cutoff.Add(time.Second)
	price := "20"
	rule := store.MarketRule{RuleType: plans.MarketPriceAbove, ThresholdUnit: "usd", ThresholdValue: "10"}
	for _, quality := range []marketquality.Quote{{Status: marketquality.StatusLive, FetchedAt: &oldSource}, {Status: marketquality.StatusLive, FetchedAt: &fetched, SourceTimestamp: &oldSource}} {
		old := store.MarketSnapshot{ID: 1, ChainID: 56, TokenAddress: tradeFactsTestToken, PriceUSD: &price, CapturedAt: fetched, QuoteQuality: quality}
		input := EvaluationInput{Rule: rule, Project: tradeFactsTestProject(), Snapshots: []store.MarketSnapshot{old}, Now: fetched, NotificationSourceAfter: &cutoff}
		result, err := Evaluate(input)
		var state ruleState
		_ = json.Unmarshal(result.State, &state)
		if err != nil || len(result.Triggers) != 0 || state.ConditionActive || state.PendingCrossing || state.LastSnapshotID != 0 {
			t.Fatalf("late insertion consumed old condition=%+v state=%+v err=%v", result, state, err)
		}
		freshAt := cutoff.Add(2 * time.Second)
		fresh := old
		fresh.ID = 2
		fresh.CapturedAt = freshAt
		fresh.QuoteQuality = liveQuoteFixture(freshAt)
		input.Snapshots = append([]store.MarketSnapshot{fresh}, old)
		input.Now = freshAt
		input.Rule.State = result.State
		result, err = Evaluate(input)
		if err != nil || len(result.Triggers) != 1 {
			t.Fatalf("fresh same condition did not cross=%+v err=%v", result, err)
		}
		input.Rule.State = result.State
		again, err := Evaluate(input)
		if err != nil || len(again.Triggers) != 0 {
			t.Fatalf("fresh crossing repeated=%+v err=%v", again, err)
		}
	}
}

func TestStageExpiredSourceDoesNotConsumeFreshCooldownAndRemainsWindowContext(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	old := tradeFactsTestEvent(1, 1, now.Add(-15*time.Minute))
	fresh := tradeFactsTestEvent(2, 2, now)
	rule := store.MarketRule{RuleType: plans.MarketLargeBuy, ThresholdUnit: "token", ThresholdValue: "1", CooldownSeconds: 60, DeliveryMode: "stage", CycleMinutes: 15}
	input := EvaluationInput{Rule: rule, Project: tradeFactsTestProject(), Events: []store.MarketEvent{old, fresh}, Now: now}
	result, err := Evaluate(input)
	if err != nil || len(result.Triggers) != 1 || result.Triggers[0].Event.ID != 2 || len(result.Suppressed) != 1 || result.Suppressed[0].Reason != "source window expired or precedes recorded recovery cutoff" {
		t.Fatalf("expired source used cooldown=%+v err=%v", result, err)
	}
	window := int32(15)
	input.Rule.RuleType = plans.MarketConsecutiveLargeBuy
	input.Rule.WindowMinutes = &window
	input.Rule.TriggerCountThreshold = 2
	result, err = Evaluate(input)
	if err != nil || len(result.Triggers) != 1 || *result.Triggers[0].CurrentValue != "2" {
		t.Fatalf("expired candidate was deleted as valid window context=%+v err=%v", result, err)
	}
	input.Rule = rule
	input.Rule.RuleScope = "combination"
	result, err = Evaluate(input)
	if err != nil || len(result.Triggers) != 1 || result.Triggers[0].Event.ID != 1 {
		t.Fatalf("combination incorrectly inherited standalone stage expiry=%+v err=%v", result, err)
	}
	price := "20"
	snapshotRule := store.MarketRule{RuleType: plans.MarketPriceAbove, ThresholdValue: "10", ThresholdUnit: "usd", DeliveryMode: "stage", CycleMinutes: 1}
	fetched := now.Add(-90 * time.Second)
	result, err = Evaluate(EvaluationInput{Rule: snapshotRule, Project: tradeFactsTestProject(), Snapshots: []store.MarketSnapshot{{ID: 1, ChainID: 56, TokenAddress: tradeFactsTestToken, PriceUSD: &price, CapturedAt: now, QuoteQuality: liveQuoteFixture(fetched)}}, Now: now})
	if err != nil || len(result.Triggers) != 1 {
		t.Fatalf("snapshot detection time changed to event expiry=%+v err=%v", result, err)
	}
}

func TestExistingNotificationDecisionCannotConsumeFreshPrivateCooldown(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	replay := tradeFactsTestEvent(51, 101, now.Add(-time.Second))
	fresh := tradeFactsTestEvent(52, 102, now)
	rule := store.MarketRule{RuleType: plans.MarketLargeBuy, ThresholdUnit: "token", ThresholdValue: "1", CooldownSeconds: 60}
	for _, terminal := range []bool{true, false} {
		t.Run(map[bool]string{true: "terminal", false: "verified_reorg_recovery"}[terminal], func(t *testing.T) {
			decisions := map[int64]bool{replay.ID: terminal}
			result, err := Evaluate(EvaluationInput{Rule: rule, Project: tradeFactsTestProject(), Events: []store.MarketEvent{replay, fresh}, Now: now, NotificationEventDecisions: decisions})
			want := 1
			if !terminal {
				want = 2
			}
			if err != nil || len(result.Triggers) != want || result.Triggers[len(result.Triggers)-1].Event.ID != fresh.ID || len(result.Suppressed) != 0 {
				t.Fatalf("replay consumed fresh cooldown=%+v err=%v", result, err)
			}
			v2 := tradeFactsTestRule(100, 100)
			v2.ThresholdUnit, v2.ThresholdValue, v2.CooldownSeconds = "token", "1", 60
			result, err = evaluateTradeFactsWithCountsAfter(v2, tradeFactsTestProject(), []store.MarketEvent{replay, fresh}, nil, now, nil, decisions)
			if err != nil || len(result.Triggers) != want || result.Triggers[len(result.Triggers)-1].Event.ID != fresh.ID || len(result.Suppressed) != 0 {
				t.Fatalf("parent-to-ready decision lost=%+v err=%v", result, err)
			}
			// A recovered logical trigger does not create or reset cooldown.
			if !terminal {
				v2.LastTriggeredAt = &now
				result, err = evaluateTradeFactsWithCountsAfter(v2, tradeFactsTestProject(), []store.MarketEvent{replay, fresh}, nil, now, nil, decisions)
				if err != nil || len(result.Triggers) != 1 || result.Triggers[0].Event.ID != replay.ID || len(result.Suppressed) != 1 || result.Suppressed[0].Event.ID != fresh.ID || result.Suppressed[0].Reason != "cooldown_active" {
					t.Fatalf("recovery changed original cooldown=%+v err=%v", result, err)
				}
			}
		})
	}
}

func TestTerminalDecisionRemainsCumulativeFactButHasNoNewWindowObligation(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	replay := tradeFactsTestEvent(51, 101, now.Add(-time.Second))
	fresh := tradeFactsTestEvent(52, 102, now)
	rule := tradeFactsTestRule(100, 100)
	rule.RuleType, rule.ThresholdUnit, rule.ThresholdValue, rule.TriggerCountThreshold = plans.MarketConsecutiveLargeBuy, "token", "1", 2
	rule.CooldownSeconds = 60
	decisions := map[int64]bool{replay.ID: true}
	for _, counts := range []map[int64]int64{nil, {fresh.TradeReadyID: 2}} {
		result, err := evaluateTradeFactsWithCountsAfter(rule, tradeFactsTestProject(), []store.MarketEvent{replay, fresh}, counts, now, nil, decisions)
		if err != nil || len(result.Triggers) != 1 || result.Triggers[0].Event.ID != fresh.ID || *result.Triggers[0].CurrentValue != "2" || len(result.Suppressed) != 0 {
			t.Fatalf("existing fact deleted from cumulative context=%+v err=%v", result, err)
		}
	}
	replay.Metadata = json.RawMessage(`{"trade_projection_unavailable":"coverage_incomplete"}`)
	rule.ThresholdUnit = "usd"
	result, err := evaluateTradeFactsWithCountsAfter(rule, tradeFactsTestProject(), []store.MarketEvent{replay}, map[int64]int64{}, now, nil, decisions)
	var state tradeLaneState
	_ = json.Unmarshal(result.State, &state)
	if err != nil || len(result.Triggers) != 0 || len(result.Suppressed) != 0 || state.LastTradeReadyID != replay.TradeReadyID {
		t.Fatalf("terminal replay demanded new window proof=%+v state=%+v err=%v", result, state, err)
	}
	if _, err := evaluateTradeFactsWithCountsAfter(rule, tradeFactsTestProject(), []store.MarketEvent{replay, fresh}, nil, now, nil, decisions); !errors.Is(err, ErrTradeWindowContextIncomplete) {
		t.Fatalf("unknown historical replay became zero for fresh window=%v", err)
	}
}

func TestExistingProgressDecisionPreservesConditionAndConfirmationBarrier(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	event := tradeFactsTestEvent(2, 2, now)
	event.Metadata = json.RawMessage(`{"protocol":"four_meme","progress_percent":"10"}`)
	state, _ := json.Marshal(ruleState{LastEventID: 1, ConditionActive: true, PendingCrossing: true})
	rule := store.MarketRule{RuleType: plans.MarketFourMemeProgress, ThresholdUnit: "progress", ThresholdValue: "80", State: state}
	input := EvaluationInput{Rule: rule, Project: tradeFactsTestProject(), Events: []store.MarketEvent{event}, Now: now, NotificationEventDecisions: map[int64]bool{2: true}}
	result, err := Evaluate(input)
	var next ruleState
	_ = json.Unmarshal(result.State, &next)
	if err != nil || len(result.Triggers) != 0 || !next.ConditionActive || !next.PendingCrossing || next.LastEventID != 2 {
		t.Fatalf("existing decision changed crossing=%+v state=%+v err=%v", result, next, err)
	}
	input.Events[0].Confirmed = 0
	result, err = Evaluate(input)
	_ = json.Unmarshal(result.State, &next)
	if err != nil || next.LastEventID != 1 {
		t.Fatalf("existing decision crossed confirmation barrier=%+v err=%v", next, err)
	}
}
