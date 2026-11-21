package marketrules

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestCompleteTradeWindowCountsBeyond500AndReverseDoesNotReset(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	rule := tradeFactsTestRule(600, 600)
	rule.RuleType, rule.TriggerCountThreshold = plans.MarketConsecutiveLargeBuy, 600
	var candidates []store.MarketTradeWindowCandidate
	for id := int64(1); id <= 600; id++ {
		candidates = append(candidates, store.MarketTradeWindowCandidate{TradeReadyID: id, ParentID: 1000000 + id, OccurredAt: now.Add(-time.Minute), Qualified: true})
	}
	first, reverse, last := tradeFactsTestEvent(1000601, 601, now), tradeFactsTestEvent(1000602, 602, now), tradeFactsTestEvent(1000603, 603, now.Add(time.Second))
	reverse.EventType = "sell"
	candidates = append(candidates, store.MarketTradeWindowCandidate{TradeReadyID: 601, ParentID: first.ID, OccurredAt: first.OccurredAt, Qualified: true},
		store.MarketTradeWindowCandidate{TradeReadyID: 603, ParentID: last.ID, OccurredAt: last.OccurredAt, Qualified: true})
	current := []store.MarketEvent{last, reverse, first}
	counts, err := completeTradeWindowCounts(rule, current, candidates, 600)
	if err != nil || counts[601] != 601 || counts[603] != 602 {
		t.Fatalf("complete counts=%v err=%v", counts, err)
	}
	if _, present := counts[602]; present {
		t.Fatal("reverse transaction entered buy count")
	}
	evaluation, err := evaluateTradeFactsWithCounts(rule, tradeFactsTestProject(), current, counts, now.Add(time.Second))
	if err != nil || len(evaluation.Triggers) != 2 {
		t.Fatalf("full-window triggers=%#v err=%v", evaluation, err)
	}
	for index, want := range []int64{601, 602} {
		if *evaluation.Triggers[index].CurrentValue != strconv.FormatInt(want, 10) {
			t.Fatalf("count[%d]=%v", index, evaluation.Triggers[index].CurrentValue)
		}
		var details struct {
			Window struct {
				Count   int64 `json:"count"`
				Minutes int32 `json:"window_minutes"`
			} `json:"trade_window"`
		}
		if json.Unmarshal(evaluation.Triggers[index].Details, &details) != nil || details.Window.Count != want || details.Window.Minutes != 15 {
			t.Fatalf("window count was not frozen: %s", evaluation.Triggers[index].Details)
		}
	}
	var state tradeLaneState
	_ = json.Unmarshal(evaluation.State, &state)
	if state.LastTradeReadyID != 603 {
		t.Fatalf("cursor=%d", state.LastTradeReadyID)
	}
}

func TestCompleteTradeWindowCountsKeepReadyAndTimeOrdersIndependent(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	rule := tradeFactsTestRule(2, 2)
	rule.RuleType = plans.MarketConsecutiveLargeBuy
	current := tradeFactsTestEvent(103, 3, now.Add(-5*time.Minute))
	candidates := []store.MarketTradeWindowCandidate{
		{TradeReadyID: 1, ParentID: 101, OccurredAt: now.Add(-6 * time.Minute), Qualified: true},
		{TradeReadyID: 2, ParentID: 102, OccurredAt: now, Qualified: true},
		{TradeReadyID: 3, ParentID: 103, OccurredAt: current.OccurredAt, Qualified: true},
		{TradeReadyID: 4, ParentID: 104, OccurredAt: now.Add(-7 * time.Minute), Unknown: true},
	}
	counts, err := completeTradeWindowCounts(rule, []store.MarketEvent{current}, candidates, 2)
	if err != nil || counts[3] != 2 {
		t.Fatalf("future-time or later-ready data changed historical decision: %v %v", counts, err)
	}
	later := tradeFactsTestEvent(105, 5, now)
	if _, err := completeTradeWindowCounts(rule, []store.MarketEvent{current, later}, append(candidates,
		store.MarketTradeWindowCandidate{TradeReadyID: 5, ParentID: 105, OccurredAt: now, Qualified: true}), 2); !errors.Is(err, ErrTradeWindowContextIncomplete) {
		t.Fatalf("unknown qualifying context was counted as zero: %v", err)
	}
}

func TestCompleteTradeWindowClosedBoundariesAndUnknownOutsideDoNotInterfere(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	rule := tradeFactsTestRule(3, 3)
	window := int32(1)
	rule.RuleType, rule.WindowMinutes = plans.MarketConsecutiveLargeBuy, &window
	current := tradeFactsTestEvent(104, 4, now)
	candidates := []store.MarketTradeWindowCandidate{
		{TradeReadyID: 1, ParentID: 101, OccurredAt: now.Add(-time.Minute), Qualified: true},
		{TradeReadyID: 2, ParentID: 102, OccurredAt: now.Add(-time.Minute - time.Microsecond), Qualified: true},
		{TradeReadyID: 3, ParentID: 103, OccurredAt: now.Add(time.Microsecond), Unknown: true},
		{TradeReadyID: 4, ParentID: 104, OccurredAt: now, Qualified: true},
	}
	counts, err := completeTradeWindowCounts(rule, []store.MarketEvent{current}, candidates, 3)
	if err != nil || counts[4] != 2 {
		t.Fatalf("closed interval was changed: %v %v", counts, err)
	}
}

func TestCompleteTradeCountIsIndependentOfCooldownAndRetainsSuppressedAudit(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	rule := tradeFactsTestRule(1, 1)
	rule.RuleType, rule.TriggerCountThreshold = plans.MarketConsecutiveLargeBuy, 2
	rule.LastTriggeredAt, rule.CooldownSeconds = &now, 60
	current := tradeFactsTestEvent(102, 2, now.Add(time.Second))
	candidates := []store.MarketTradeWindowCandidate{
		{TradeReadyID: 1, ParentID: 101, OccurredAt: now, Qualified: true},
		{TradeReadyID: 2, ParentID: 102, OccurredAt: current.OccurredAt, Qualified: true},
	}
	counts, err := completeTradeWindowCounts(rule, []store.MarketEvent{current}, candidates, 1)
	if err != nil || counts[2] != 2 {
		t.Fatalf("cooldown erased a qualifying trade: %v %v", counts, err)
	}
	evaluation, err := evaluateTradeFactsWithCounts(rule, tradeFactsTestProject(), []store.MarketEvent{current}, counts, current.OccurredAt)
	if err != nil || len(evaluation.Triggers) != 0 || len(evaluation.Suppressed) != 1 || evaluation.Suppressed[0].Reason != "cooldown_active" || *evaluation.Suppressed[0].CurrentValue != "2" {
		t.Fatalf("cooldown behavior changed: %#v %v", evaluation, err)
	}
}

func TestCompleteTradeWindowRejectsMissingCountsDuplicatesAndBudget(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	rule := tradeFactsTestRule(0, 0)
	rule.RuleType, rule.TriggerCountThreshold = plans.MarketConsecutiveLargeBuy, 1
	current := tradeFactsTestEvent(101, 1, now)
	valid := store.MarketTradeWindowCandidate{TradeReadyID: 1, ParentID: 101, OccurredAt: now, Qualified: true}
	for _, candidates := range [][]store.MarketTradeWindowCandidate{nil, {valid, valid}, {{TradeReadyID: 1, ParentID: 999, OccurredAt: now, Qualified: true}}, make([]store.MarketTradeWindowCandidate, 100001)} {
		if _, err := completeTradeWindowCounts(rule, []store.MarketEvent{current}, candidates, 0); !errors.Is(err, ErrTradeWindowContextIncomplete) {
			t.Fatalf("invalid compact result accepted: %v", err)
		}
	}
	if _, err := evaluateTradeFactsWithCounts(rule, tradeFactsTestProject(), []store.MarketEvent{current}, map[int64]int64{}, now); !errors.Is(err, ErrTradeWindowContextIncomplete) {
		t.Fatalf("missing count silently became zero: %v", err)
	}
}
