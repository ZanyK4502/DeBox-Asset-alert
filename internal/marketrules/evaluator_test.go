package marketrules

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestEvaluateSnapshotRuleCrossesAndRearms(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	price := "120"
	input := EvaluationInput{
		Rule: store.MarketRule{
			RuleType:       plans.MarketPriceAbove,
			ThresholdValue: "100",
			ThresholdUnit:  "usd",
		},
		Project: store.MarketProject{TokenSymbol: "TEST"},
		Snapshots: []store.MarketSnapshot{{
			ID: 1, MarketPoolID: 10, PriceUSD: &price, CapturedAt: now,
		}},
		Now: now,
	}
	first, err := Evaluate(input)
	if err != nil {
		t.Fatalf("first evaluation: %v", err)
	}
	if len(first.Triggers) != 1 {
		t.Fatalf("first triggers = %d, want 1", len(first.Triggers))
	}

	input.Rule.State = first.State
	second, err := Evaluate(input)
	if err != nil {
		t.Fatalf("second evaluation: %v", err)
	}
	if len(second.Triggers) != 0 {
		t.Fatalf("second triggers = %d, want 0 while condition remains active", len(second.Triggers))
	}

	price = "90"
	input.Snapshots[0].ID = 2
	input.Snapshots[0].PriceUSD = &price
	input.Rule.State = second.State
	rearmed, err := Evaluate(input)
	if err != nil {
		t.Fatalf("rearm evaluation: %v", err)
	}
	price = "110"
	input.Snapshots[0].ID = 3
	input.Snapshots[0].PriceUSD = &price
	input.Rule.State = rearmed.State
	crossedAgain, err := Evaluate(input)
	if err != nil {
		t.Fatalf("second crossing evaluation: %v", err)
	}
	if len(crossedAgain.Triggers) != 1 {
		t.Fatalf("second crossing triggers = %d, want 1", len(crossedAgain.Triggers))
	}
}

func TestEvaluateAbsolutePriceDoesNotRepeatAfterCooldownWhileConditionRemainsActive(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	price := "120"
	input := EvaluationInput{
		Rule: store.MarketRule{
			RuleType:        plans.MarketPriceAbove,
			ThresholdValue:  "100",
			ThresholdUnit:   "usd",
			CooldownSeconds: 60,
		},
		Project: store.MarketProject{TokenSymbol: "TEST"},
		Snapshots: []store.MarketSnapshot{{
			ID: 1, MarketPoolID: 10, PriceUSD: &price, CapturedAt: now,
		}},
		Now: now,
	}
	first, err := Evaluate(input)
	if err != nil {
		t.Fatalf("first evaluation: %v", err)
	}
	if len(first.Triggers) != 1 {
		t.Fatalf("first triggers = %d, want 1", len(first.Triggers))
	}

	input.Rule.State = first.State
	input.Rule.LastTriggeredAt = &now
	input.Now = now.Add(61 * time.Second)
	input.Snapshots[0].ID = 2
	input.Snapshots[0].CapturedAt = input.Now
	repeated, err := Evaluate(input)
	if err != nil {
		t.Fatalf("post-cooldown evaluation: %v", err)
	}
	if len(repeated.Triggers) != 0 {
		t.Fatalf("post-cooldown triggers = %d, want 0", len(repeated.Triggers))
	}
}

func TestEvaluatePriceChangeRepeatsAfterCooldownWhileConditionRemainsActive(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	window := int32(60)
	tests := []struct {
		name        string
		ruleType    string
		latestPrice string
	}{
		{name: "increase", ruleType: plans.MarketPriceIncrease, latestPrice: "120"},
		{name: "decrease", ruleType: plans.MarketPriceDecrease, latestPrice: "80"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			baselinePrice := "100"
			latestPrice := test.latestPrice
			input := EvaluationInput{
				Rule: store.MarketRule{
					RuleType:          test.ruleType,
					ThresholdValue:    "10",
					ThresholdUnit:     "percent",
					WindowMinutes:     &window,
					CooldownSeconds:   60,
					RepeatWhileActive: true,
				},
				Project: store.MarketProject{TokenSymbol: "TEST"},
				Snapshots: []store.MarketSnapshot{
					{ID: 2, MarketPoolID: 10, PriceUSD: &latestPrice, CapturedAt: now},
					{ID: 1, MarketPoolID: 10, PriceUSD: &baselinePrice, CapturedAt: now.Add(-time.Hour)},
				},
				Now: now,
			}
			first, err := Evaluate(input)
			if err != nil {
				t.Fatalf("first evaluation: %v", err)
			}
			if len(first.Triggers) != 1 {
				t.Fatalf("first triggers = %d, want 1", len(first.Triggers))
			}

			input.Rule.State = first.State
			input.Rule.LastTriggeredAt = &now
			input.Now = now.Add(30 * time.Second)
			input.Snapshots[0].ID = 3
			input.Snapshots[0].CapturedAt = input.Now
			duringCooldown, err := Evaluate(input)
			if err != nil {
				t.Fatalf("cooldown evaluation: %v", err)
			}
			if len(duringCooldown.Triggers) != 0 {
				t.Fatalf("cooldown triggers = %d, want 0", len(duringCooldown.Triggers))
			}

			input.Rule.State = duringCooldown.State
			input.Now = now.Add(61 * time.Second)
			input.Snapshots[0].ID = 4
			input.Snapshots[0].CapturedAt = input.Now
			afterCooldown, err := Evaluate(input)
			if err != nil {
				t.Fatalf("post-cooldown evaluation: %v", err)
			}
			if len(afterCooldown.Triggers) != 1 {
				t.Fatalf("post-cooldown triggers = %d, want 1", len(afterCooldown.Triggers))
			}
		})
	}
}

func TestEvaluatePriceChangeDoesNotRepeatWhenOptionIsDisabled(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	window := int32(60)
	baselinePrice, latestPrice := "100", "120"
	input := EvaluationInput{
		Rule: store.MarketRule{
			RuleType:        plans.MarketPriceIncrease,
			ThresholdValue:  "10",
			ThresholdUnit:   "percent",
			WindowMinutes:   &window,
			CooldownSeconds: 60,
		},
		Project: store.MarketProject{TokenSymbol: "TEST"},
		Snapshots: []store.MarketSnapshot{
			{ID: 2, MarketPoolID: 10, PriceUSD: &latestPrice, CapturedAt: now},
			{ID: 1, MarketPoolID: 10, PriceUSD: &baselinePrice, CapturedAt: now.Add(-time.Hour)},
		},
		Now: now,
	}
	first, err := Evaluate(input)
	if err != nil {
		t.Fatalf("first evaluation: %v", err)
	}
	if len(first.Triggers) != 1 {
		t.Fatalf("first triggers = %d, want 1", len(first.Triggers))
	}

	input.Rule.State = first.State
	input.Rule.LastTriggeredAt = &now
	input.Now = now.Add(61 * time.Second)
	input.Snapshots[0].ID = 3
	input.Snapshots[0].CapturedAt = input.Now
	repeated, err := Evaluate(input)
	if err != nil {
		t.Fatalf("post-cooldown evaluation: %v", err)
	}
	if len(repeated.Triggers) != 0 {
		t.Fatalf("post-cooldown triggers = %d, want 0", len(repeated.Triggers))
	}
}

func TestEvaluateLiquidityDecreaseRepeatsAfterCooldownWhenEnabled(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	window := int32(60)
	baselineLiquidity, latestLiquidity := "100000", "80000"
	input := EvaluationInput{
		Rule: store.MarketRule{
			RuleType:          plans.MarketLiquidityDecrease,
			ThresholdValue:    "10",
			ThresholdUnit:     "percent",
			WindowMinutes:     &window,
			CooldownSeconds:   60,
			RepeatWhileActive: true,
		},
		Project: store.MarketProject{TokenSymbol: "TEST"},
		Snapshots: []store.MarketSnapshot{
			{ID: 2, MarketPoolID: 10, LiquidityUSD: &latestLiquidity, CapturedAt: now},
			{ID: 1, MarketPoolID: 10, LiquidityUSD: &baselineLiquidity, CapturedAt: now.Add(-time.Hour)},
		},
		Now: now,
	}
	first, err := Evaluate(input)
	if err != nil {
		t.Fatalf("first evaluation: %v", err)
	}
	if len(first.Triggers) != 1 {
		t.Fatalf("first triggers = %d, want 1", len(first.Triggers))
	}

	input.Rule.State = first.State
	input.Rule.LastTriggeredAt = &now
	input.Now = now.Add(61 * time.Second)
	input.Snapshots[0].ID = 3
	input.Snapshots[0].CapturedAt = input.Now
	repeated, err := Evaluate(input)
	if err != nil {
		t.Fatalf("post-cooldown evaluation: %v", err)
	}
	if len(repeated.Triggers) != 1 {
		t.Fatalf("post-cooldown triggers = %d, want 1", len(repeated.Triggers))
	}
}

func TestEvaluateVolumeAndImbalanceRulesRepeatAfterCooldownWhenEnabled(t *testing.T) {
	now := time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC)
	window := int32(60)
	volumeHigh, volumeBaseline := "1000", "100"
	buys, sells := int64(80), int64(20)
	tests := []struct {
		name      string
		ruleType  string
		threshold string
		unit      string
		snapshots []store.MarketSnapshot
	}{
		{
			name: "volume above", ruleType: plans.MarketVolumeAbove,
			threshold: "500", unit: "usd",
			snapshots: []store.MarketSnapshot{
				{ID: 2, MarketPoolID: 10, Volume1hUSD: &volumeHigh, CapturedAt: now},
			},
		},
		{
			name: "volume spike", ruleType: plans.MarketVolumeSpike,
			threshold: "5", unit: "ratio",
			snapshots: []store.MarketSnapshot{
				{ID: 2, MarketPoolID: 10, Volume1hUSD: &volumeHigh, CapturedAt: now},
				{ID: 1, MarketPoolID: 10, Volume1hUSD: &volumeBaseline, CapturedAt: now.Add(-time.Hour)},
			},
		},
		{
			name: "trade imbalance", ruleType: plans.MarketTradeImbalance,
			threshold: "70", unit: "percent",
			snapshots: []store.MarketSnapshot{
				{ID: 2, MarketPoolID: 10, Buys1h: &buys, Sells1h: &sells, CapturedAt: now},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := EvaluationInput{
				Rule: store.MarketRule{
					RuleType:          test.ruleType,
					ThresholdValue:    test.threshold,
					ThresholdUnit:     test.unit,
					WindowMinutes:     &window,
					CooldownSeconds:   60,
					RepeatWhileActive: true,
				},
				Project:   store.MarketProject{TokenSymbol: "TEST"},
				Snapshots: test.snapshots,
				Now:       now,
			}
			first, err := Evaluate(input)
			if err != nil {
				t.Fatalf("first evaluation: %v", err)
			}
			if len(first.Triggers) != 1 {
				t.Fatalf("first triggers = %d, want 1", len(first.Triggers))
			}

			input.Rule.State = first.State
			input.Rule.LastTriggeredAt = &now
			input.Now = now.Add(61 * time.Second)
			input.Snapshots[0].ID = 3
			input.Snapshots[0].CapturedAt = input.Now
			repeated, err := Evaluate(input)
			if err != nil {
				t.Fatalf("post-cooldown evaluation: %v", err)
			}
			if len(repeated.Triggers) != 1 {
				t.Fatalf("post-cooldown triggers = %d, want 1", len(repeated.Triggers))
			}
		})
	}
}

func TestEvaluateEventCooldownLimitsOneTriggerPerBatch(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	firstValue, secondValue := "1000", "2000"
	evaluation, err := Evaluate(EvaluationInput{
		Rule: store.MarketRule{
			RuleType:        plans.MarketLargeBuy,
			ThresholdValue:  "500",
			ThresholdUnit:   "usd",
			CooldownSeconds: 60,
		},
		Project: store.MarketProject{TokenSymbol: "TEST"},
		Events: []store.MarketEvent{
			{ID: 1, EventType: marketparse.EventBuy, EventKey: "buy:1", USDValue: &firstValue, Confirmed: 1, OccurredAt: now.Add(-2 * time.Second)},
			{ID: 2, EventType: marketparse.EventBuy, EventKey: "buy:2", USDValue: &secondValue, Confirmed: 1, OccurredAt: now.Add(-time.Second)},
		},
		Now: now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(evaluation.Triggers) != 1 || evaluation.Triggers[0].EventKey != "buy:1" {
		t.Fatalf("triggers = %#v, want only first qualifying event", evaluation.Triggers)
	}
	if len(evaluation.Suppressed) != 1 ||
		evaluation.Suppressed[0].EventKey != "buy:2" ||
		evaluation.Suppressed[0].Reason != "cooldown_active" {
		t.Fatalf(
			"suppressed = %#v, want second qualifying event blocked by cooldown",
			evaluation.Suppressed,
		)
	}
	var state ruleState
	if err := json.Unmarshal(evaluation.State, &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if state.LastEventID != 2 {
		t.Fatalf("last event id = %d, want 2", state.LastEventID)
	}
}

func TestEvaluateFourMemeProgressOnlyOnCrossing(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	metadata := func(progress string) json.RawMessage {
		value, _ := json.Marshal(map[string]string{
			"protocol":         "four_meme",
			"progress_percent": progress,
		})
		return value
	}
	input := EvaluationInput{
		Rule: store.MarketRule{
			RuleType:       plans.MarketFourMemeProgress,
			ThresholdValue: "80",
			ThresholdUnit:  "progress",
		},
		Project: store.MarketProject{TokenSymbol: "TEST"},
		Events: []store.MarketEvent{
			{ID: 1, EventType: marketparse.EventBuy, EventKey: "four:1", Metadata: metadata("70"), Confirmed: 1, OccurredAt: now.Add(-2 * time.Minute)},
			{ID: 2, EventType: marketparse.EventBuy, EventKey: "four:2", Metadata: metadata("85"), Confirmed: 1, OccurredAt: now.Add(-time.Minute)},
		},
		Now: now,
	}
	first, err := Evaluate(input)
	if err != nil {
		t.Fatalf("first evaluation: %v", err)
	}
	if len(first.Triggers) != 1 || first.Triggers[0].EventKey != "four:2" {
		t.Fatalf("first triggers = %#v", first.Triggers)
	}
	input.Rule.State = first.State
	second, err := Evaluate(input)
	if err != nil {
		t.Fatalf("second evaluation: %v", err)
	}
	if len(second.Triggers) != 0 {
		t.Fatalf("second triggers = %d, want 0", len(second.Triggers))
	}
}

func TestEvaluateSkipsReorgedEvents(t *testing.T) {
	value := "1000"
	evaluation, err := Evaluate(EvaluationInput{
		Rule: store.MarketRule{
			RuleType:       plans.MarketLargeSell,
			ThresholdValue: "100",
			ThresholdUnit:  "usd",
		},
		Events: []store.MarketEvent{{
			ID: 1, EventType: marketparse.EventSell, EventKey: "sell:1",
			USDValue: &value, Reorged: 1, OccurredAt: time.Now().UTC(),
		}},
		Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(evaluation.Triggers) != 0 {
		t.Fatalf("triggers = %d, want 0", len(evaluation.Triggers))
	}
}

func TestEvaluateDoesNotAdvancePastUnconfirmedEvent(t *testing.T) {
	now := time.Now().UTC()
	value := "1000"
	evaluation, err := Evaluate(EvaluationInput{
		Rule: store.MarketRule{
			RuleType:       plans.MarketLargeBuy,
			ThresholdValue: "100",
			ThresholdUnit:  "usd",
		},
		Events: []store.MarketEvent{
			{ID: 1, EventType: marketparse.EventBuy, EventKey: "buy:1", USDValue: &value, Confirmed: 0, OccurredAt: now},
			{ID: 2, EventType: marketparse.EventBuy, EventKey: "buy:2", USDValue: &value, Confirmed: 1, OccurredAt: now},
		},
		Now: now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(evaluation.Triggers) != 0 {
		t.Fatalf("triggers = %d, want 0", len(evaluation.Triggers))
	}
	var state ruleState
	if err := json.Unmarshal(evaluation.State, &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if state.LastEventID != 0 {
		t.Fatalf("last event id = %d, want 0", state.LastEventID)
	}
}

func TestMergeMarketEventsDoesNotJumpAcrossBacklog(t *testing.T) {
	events := mergeMarketEvents(
		100,
		[]store.MarketEvent{{ID: 90}, {ID: 5000}},
		[]store.MarketEvent{{ID: 101}, {ID: 102}},
	)
	seen := map[int64]bool{}
	for _, event := range events {
		seen[event.ID] = true
	}
	if !seen[90] || !seen[101] || !seen[102] || seen[5000] {
		t.Fatalf("merged event ids = %#v", seen)
	}
}

func TestEvaluateHolderRuleCarriesAddressLabel(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	address := "0x1111111111111111111111111111111111111111"
	value := "25"
	evaluation, err := Evaluate(EvaluationInput{
		Rule: store.MarketRule{
			RuleType:       plans.MarketHolderIncrease,
			ThresholdValue: "10",
			ThresholdUnit:  "usd",
		},
		Project: store.MarketProject{ChainID: 56, TokenSymbol: "TEST"},
		Events: []store.MarketEvent{{
			ID:            1,
			ChainID:       56,
			EventType:     "holder_increase",
			EventKey:      "holder-increase:1",
			WalletAddress: &address,
			USDValue:      &value,
			Confirmed:     1,
			OccurredAt:    now,
		}},
		Holders: []store.MarketHolder{{
			ChainID: 56, HolderAddress: address,
		}},
		Labels: []store.MarketAddressLabel{{
			ChainID: 56, Address: address, Label: "项目方金库",
		}},
		Now: now,
	})
	if err != nil {
		t.Fatalf("Evaluate(): %v", err)
	}
	if len(evaluation.Triggers) != 1 || len(evaluation.Suppressed) != 0 {
		t.Fatalf("triggers/suppressed = %d/%d, want 1/0",
			len(evaluation.Triggers), len(evaluation.Suppressed))
	}
	var details struct {
		AddressLabel    string `json:"address_label"`
		AddressExcluded bool   `json:"address_excluded"`
	}
	if err := json.Unmarshal(evaluation.Triggers[0].Details, &details); err != nil {
		t.Fatalf("decode trigger details: %v", err)
	}
	if details.AddressLabel != "项目方金库" || details.AddressExcluded {
		t.Fatalf("unexpected trigger details: %#v", details)
	}
}

func TestEvaluateHolderRuleAuditsExcludedAddressWithoutNotification(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	address := "0x2222222222222222222222222222222222222222"
	value := "25"
	evaluation, err := Evaluate(EvaluationInput{
		Rule: store.MarketRule{
			RuleType:       plans.MarketHolderDecrease,
			ThresholdValue: "10",
			ThresholdUnit:  "usd",
		},
		Project: store.MarketProject{ChainID: 56, TokenSymbol: "TEST"},
		Events: []store.MarketEvent{{
			ID:            1,
			ChainID:       56,
			EventType:     "holder_decrease",
			EventKey:      "holder-decrease:1",
			WalletAddress: &address,
			USDValue:      &value,
			Confirmed:     1,
			OccurredAt:    now,
		}},
		Holders: []store.MarketHolder{{
			ChainID: 56, HolderAddress: address,
		}},
		Labels: []store.MarketAddressLabel{{
			ChainID: 56, Address: address, Label: "内部钱包", Excluded: 1,
		}},
		Now: now,
	})
	if err != nil {
		t.Fatalf("Evaluate(): %v", err)
	}
	if len(evaluation.Triggers) != 0 || len(evaluation.Suppressed) != 1 {
		t.Fatalf("triggers/suppressed = %d/%d, want 0/1",
			len(evaluation.Triggers), len(evaluation.Suppressed))
	}
	if evaluation.Suppressed[0].Reason != "holder_excluded" {
		t.Fatalf("suppression reason = %q, want holder_excluded",
			evaluation.Suppressed[0].Reason)
	}
	var details struct {
		AddressLabel    string `json:"address_label"`
		AddressExcluded bool   `json:"address_excluded"`
	}
	if err := json.Unmarshal(evaluation.Suppressed[0].Details, &details); err != nil {
		t.Fatalf("decode suppressed details: %v", err)
	}
	if details.AddressLabel != "内部钱包" || !details.AddressExcluded {
		t.Fatalf("unexpected suppressed details: %#v", details)
	}
}
