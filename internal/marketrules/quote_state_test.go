package marketrules

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestSnapshotMissingDataPreservesConditionPendingAndSnapshotCursor(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	window := int32(60)
	price := "120"
	for _, test := range []struct {
		name     string
		kind     string
		snapshot store.MarketSnapshot
	}{
		{"missing price", plans.MarketPriceAbove, store.MarketSnapshot{}},
		{"missing liquidity", plans.MarketLiquidityBelow, store.MarketSnapshot{}},
		{"missing baseline", plans.MarketPriceIncrease, store.MarketSnapshot{PriceUSD: &price}},
		{"missing volume", plans.MarketVolumeAbove, store.MarketSnapshot{}},
		{"missing counts", plans.MarketTradeImbalance, store.MarketSnapshot{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, _ := json.Marshal(ruleState{LastSnapshotID: 40, LastEventID: 30, ConditionActive: true, PendingCrossing: true})
			snapshot := test.snapshot
			snapshot.ID, snapshot.CapturedAt = 41, now
			snapshot.QuoteQuality = liveQuoteFixture(now)
			lastTriggered := now.Add(-30 * time.Second)
			rule := store.MarketRule{RuleType: test.kind, ThresholdValue: "100", ThresholdUnit: "usd", WindowMinutes: &window,
				State: before, LastTriggeredAt: &lastTriggered, CooldownSeconds: 60}
			result, err := Evaluate(EvaluationInput{Rule: rule, Snapshots: []store.MarketSnapshot{snapshot}, Now: now})
			if err != nil || len(result.Triggers) != 0 || !bytes.Equal(result.State, before) || !rule.LastTriggeredAt.Equal(lastTriggered) {
				t.Fatalf("missing observation changed condition, pending crossing, cursor or cooldown: state=%s err=%v", result.State, err)
			}
		})
	}
}

func TestSnapshotKnownZeroActivityClearsConditionAndZeroLiquidityCanTrigger(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	state, _ := json.Marshal(ruleState{LastSnapshotID: 40, ConditionActive: true, PendingCrossing: true})
	zero := int64(0)
	window := int32(60)
	result, err := Evaluate(EvaluationInput{Rule: store.MarketRule{RuleType: plans.MarketTradeImbalance, ThresholdValue: "75", WindowMinutes: &window, State: state},
		Snapshots: []store.MarketSnapshot{{ID: 41, CapturedAt: now, QuoteQuality: liveQuoteFixture(now), Buys1h: &zero, Sells1h: &zero}}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	var after ruleState
	if json.Unmarshal(result.State, &after) != nil || after.LastSnapshotID != 41 || after.ConditionActive || after.PendingCrossing || len(result.Triggers) != 0 {
		t.Fatalf("known no-activity result changed: %s", result.State)
	}
	liquidity := "0"
	result, err = Evaluate(EvaluationInput{Rule: store.MarketRule{RuleType: plans.MarketLiquidityBelow, ThresholdValue: "100", ThresholdUnit: "usd"},
		Snapshots: []store.MarketSnapshot{{ID: 42, CapturedAt: now, QuoteQuality: liveQuoteFixture(now), LiquidityUSD: &liquidity}}, Now: now})
	if err != nil || len(result.Triggers) != 1 {
		t.Fatalf("fresh real zero liquidity must still match: %#v, %v", result, err)
	}
}
