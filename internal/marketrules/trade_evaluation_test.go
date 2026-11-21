package marketrules

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const tradeFactsTestToken = "0x1111111111111111111111111111111111111111"

func tradeFactsTestEvent(parentID, readyID int64, at time.Time) store.MarketEvent {
	usd, token, raw, price, liquidity := "12000", "12", "12000000", "1000", "2000000"
	metadata, _ := json.Marshal(map[string]any{"protocol": "mixed_dex", "trade_projection": map[string]any{
		"schema_version": 1, "ready_id": readyID, "receipt_id": 3,
		"pool_ids": []int64{10, 20}, "selected_pool_ids": []int64{10, 20}, "source_leg_ids": []int64{11, 12},
		"token_amount_raw": raw, "token_amount": token, "quote_amount_raw": nil, "quote_amount": nil,
		"usd_value": usd, "price_usd": price, "quote_address": "", "liquidity_usd": liquidity,
		"liquidity_complete": true, "snapshot_ids": []int64{71, 72},
		"valuation_metadata": []map[string]any{valuationProofFixture(at), valuationProofFixture(at)},
		"liquidity_snapshots": []map[string]any{{"id": 71, "pool_id": 10, "captured_at": at, "quote_quality": liveQuoteFixture(at)},
			{"id": 72, "pool_id": 20, "captured_at": at, "quote_quality": liveQuoteFixture(at)}},
	}})
	hash := fmt.Sprintf("0x%064x", parentID)
	return store.MarketEvent{ID: parentID, TradeReadyID: readyID, ChainKey: "bsc", ChainID: 56,
		TokenAddress: tradeFactsTestToken, EventType: marketparse.EventBuy, EventKey: fmt.Sprintf("trade:%d", parentID),
		TransactionHash: &hash, TokenAmountRaw: &raw, TokenAmount: &token, USDValue: &usd, PriceUSD: &price,
		Confirmed: 1, OccurredAt: at, Metadata: metadata}
}

func tradeFactsTestRule(fence, cursor int64) store.MarketRule {
	state, _ := json.Marshal(tradeLaneState{ActivationFence: &fence, LastTradeReadyID: cursor})
	window := int32(15)
	return store.MarketRule{ID: 9, RuleType: plans.MarketLargeBuy, ThresholdValue: "10000", ThresholdUnit: "usd", State: state, WindowMinutes: &window}
}

func tradeFactsTestProject() store.MarketProject {
	return store.MarketProject{ID: 1, ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFactsTestToken, TokenSymbol: "TEST"}
}

func TestTradeFactsEvaluationUsesReadyCursorAndRestoresParentIdentity(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	// The old parent became ready after a newer parent: source IDs cannot be the cursor.
	event := tradeFactsTestEvent(5, 102, now)
	result, err := evaluateTradeFacts(tradeFactsTestRule(100, 101), tradeFactsTestProject(), []store.MarketEvent{event, event}, now)
	if err != nil || len(result.Triggers) != 1 || result.Triggers[0].Event.ID != 5 ||
		result.Triggers[0].Event.TradeReadyID != 102 || *result.Triggers[0].CurrentValue != "12000" {
		t.Fatalf("ready evaluation = %#v, %v", result, err)
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(result.State, &state); err != nil {
		t.Fatal(err)
	}
	if string(state["activation_fence"]) != "100" || string(state["last_trade_ready_id"]) != "102" || state["last_event_id"] != nil {
		t.Fatalf("trade cursor overwrote legacy cursor or activation fence: %s", result.State)
	}
	if string(result.Triggers[0].Details) != string(event.Metadata) {
		t.Fatal("trigger did not freeze the selected-pool projection")
	}
	secondRule := tradeFactsTestRule(100, 101)
	secondRule.State = result.State
	second, err := evaluateTradeFacts(secondRule, tradeFactsTestProject(), []store.MarketEvent{event}, now)
	if err != nil || len(second.Triggers) != 0 || len(second.Suppressed) != 0 {
		t.Fatalf("repeated publication replayed a trade: %#v, %v", second, err)
	}
}

func TestTradeFactsPercentUsesCompleteSelectedPoolLiquidity(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	event := tradeFactsTestEvent(5, 101, now)
	rule := tradeFactsTestRule(100, 100)
	rule.ThresholdUnit, rule.ThresholdValue = "percent", "1"
	below, err := evaluateTradeFacts(rule, tradeFactsTestProject(), []store.MarketEvent{event}, now)
	if err != nil || len(below.Triggers) != 0 {
		t.Fatalf("$12k/$2m=.6%% must not meet 1%%: %#v, %v", below, err)
	}
	rule.ThresholdValue = "0.5"
	matched, err := evaluateTradeFacts(rule, tradeFactsTestProject(), []store.MarketEvent{event}, now)
	if err != nil || len(matched.Triggers) != 1 || *matched.Triggers[0].CurrentValue != "0.6" {
		t.Fatalf("selected-pool percentage = %#v, %v", matched, err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(event.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	projection := metadata["trade_projection"].(map[string]any)
	projection["liquidity_complete"], projection["liquidity_usd"] = false, nil
	event.Metadata, _ = json.Marshal(metadata)
	unavailable, err := evaluateTradeFacts(rule, tradeFactsTestProject(), []store.MarketEvent{event}, now)
	if err != nil || len(unavailable.Triggers) != 0 || len(unavailable.Suppressed) != 1 || unavailable.Suppressed[0].Reason != "trade_liquidity_unavailable" {
		t.Fatalf("missing pool liquidity was not audited: %#v, %v", unavailable, err)
	}
	// Missing liquidity must not disable an otherwise complete USD projection.
	rule.ThresholdUnit, rule.ThresholdValue = "usd", "10000"
	usd, err := evaluateTradeFacts(rule, tradeFactsTestProject(), []store.MarketEvent{event}, now)
	if err != nil || len(usd.Triggers) != 1 {
		t.Fatalf("USD projection unexpectedly depends on liquidity: %#v, %v", usd, err)
	}
}

func TestTradeFactsUnavailableCoverageIsAuditedWithoutZeroValuation(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	event := tradeFactsTestEvent(5, 101, now)
	event.Metadata = json.RawMessage(`{"trade_projection_unavailable":"coverage_incomplete"}`)
	event.TokenAmount, event.USDValue = nil, nil
	result, err := evaluateTradeFacts(tradeFactsTestRule(100, 100), tradeFactsTestProject(), []store.MarketEvent{event}, now)
	if err != nil || len(result.Triggers) != 0 || len(result.Suppressed) != 1 ||
		result.Suppressed[0].Reason != "coverage_incomplete" || result.Suppressed[0].Event.ID != 5 || result.Suppressed[0].CurrentValue != nil {
		t.Fatalf("incomplete coverage = %#v, %v", result, err)
	}
	var state tradeLaneState
	if err := json.Unmarshal(result.State, &state); err != nil || state.LastTradeReadyID != 101 {
		t.Fatalf("unavailable trade audit progress = %s, %v", result.State, err)
	}
}

func TestTradeFactsUnconfirmedParentBlocksCursorAndWindowUsesValidLogicalTrades(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	first, second := tradeFactsTestEvent(500, 101, now), tradeFactsTestEvent(5, 102, now)
	first.Confirmed = 0
	result, err := evaluateTradeFacts(tradeFactsTestRule(100, 100), tradeFactsTestProject(), []store.MarketEvent{second, first}, now)
	var state tradeLaneState
	_ = json.Unmarshal(result.State, &state)
	if err != nil || len(result.Triggers) != 0 || state.LastTradeReadyID != 100 {
		t.Fatalf("unconfirmed ready barrier was skipped: %#v, %v", result, err)
	}
	first.Confirmed = 1
	rule := tradeFactsTestRule(100, 100)
	rule.RuleType, rule.TriggerCountThreshold = plans.MarketConsecutiveLargeBuy, 2
	historical := tradeFactsTestEvent(3, 99, now.Add(-time.Minute))
	historical.Reorged = 1
	result, err = evaluateTradeFacts(rule, tradeFactsTestProject(), []store.MarketEvent{historical, second, second}, now)
	if err != nil || len(result.Triggers) != 0 {
		t.Fatalf("reorged or duplicate facts inflated cumulative count: %#v, %v", result, err)
	}
	result, err = evaluateTradeFacts(rule, tradeFactsTestProject(), []store.MarketEvent{first, second}, now)
	if err != nil || len(result.Triggers) != 1 || result.Triggers[0].Event.ID != 5 || *result.Triggers[0].CurrentValue != "2" {
		t.Fatalf("two logical trades should meet cumulative threshold once: %#v, %v", result, err)
	}
}

func TestTradeFactsRequiresExplicitActivationAndValidatedScope(t *testing.T) {
	now := time.Now().UTC()
	rule := tradeFactsTestRule(0, 0)
	rule.State = json.RawMessage(`{"last_trade_ready_id":0}`)
	if _, err := evaluateTradeFacts(rule, tradeFactsTestProject(), nil, now); err != ErrTradeRuleStateUninitialized {
		t.Fatalf("missing activation fence error = %v", err)
	}
	event := tradeFactsTestEvent(5, 1, now)
	event.ChainID = 137
	if _, err := evaluateTradeFacts(tradeFactsTestRule(0, 0), tradeFactsTestProject(), []store.MarketEvent{event}, now); err == nil {
		t.Fatal("cross-chain source escaped the selected target")
	}
}
