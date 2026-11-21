package store

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
)

func TestFrozenMarketTradeEventPreservesTriggerTimeScopeAndExactAmounts(t *testing.T) {
	globalToken, globalUSD, globalPrice := "999999", "999999999", "12345"
	globalPool := int64(99)
	parent := MarketEvent{ID: 51, ChainID: 56, TokenAddress: "0x1111111111111111111111111111111111111111",
		TokenAmount: &globalToken, USDValue: &globalUSD, PriceUSD: &globalPrice, MarketPoolID: &globalPool,
		Metadata: json.RawMessage(`{"current_parent_value":"must not be used"}`)}
	details := json.RawMessage(`{"trade_projection":{"schema_version":1,"ready_id":103,"pool_ids":[10,20],"selected_pool_ids":[10,20],"token_amount_raw":"12345678901234567890123456789","quote_amount_raw":null,"token_amount":"1.2345678901234567890123456789","quote_amount":null,"usd_value":"12000","price_usd":"1000","quote_address":"","liquidity_usd":"2000000","liquidity_complete":true,"snapshot_ids":[71,72]}}`)
	event, snapshot, present, err := frozenMarketTradeEvent(parent, details)
	if err != nil || !present || event.ID != 51 || event.TradeReadyID != 103 || event.MarketPoolID != nil ||
		event.TokenAmount == nil || *event.TokenAmount != "1.2345678901234567890123456789" ||
		event.USDValue == nil || *event.USDValue != "12000" || event.QuoteAmount != nil || event.QuoteAmountRaw != nil {
		t.Fatalf("frozen selected-pool values = %#v, present=%v, err=%v", event, present, err)
	}
	if snapshot == nil || snapshot.PriceUSD == nil || *snapshot.PriceUSD != "1000" ||
		snapshot.LiquidityUSD == nil || *snapshot.LiquidityUSD != "2000000" {
		t.Fatalf("snapshot should use frozen quote and denominator: %#v", snapshot)
	}
	if *parent.TokenAmount != globalToken || *parent.USDValue != globalUSD || *parent.MarketPoolID != 99 {
		t.Fatal("projection changed the shared parent event")
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal(event.Metadata, &metadata) != nil || len(metadata["trade_projection"]) == 0 {
		t.Fatal("frozen scope/provenance was not carried to the notification snapshot")
	}
}

func TestFrozenMarketTradeEventKeepsNullQuoteAndSingleContributingPool(t *testing.T) {
	oldPrice := "999"
	parent := MarketEvent{ID: 51, PriceUSD: &oldPrice}
	event, snapshot, present, err := frozenMarketTradeEvent(parent, json.RawMessage(`{"trade_projection":{"schema_version":1,"ready_id":103,"pool_ids":[20],"price_usd":null,"liquidity_usd":null,"token_amount":"12"}}`))
	if err != nil || !present || event.MarketPoolID == nil || *event.MarketPoolID != 20 ||
		event.PriceUSD != nil || snapshot == nil || snapshot.PriceUSD != nil || snapshot.LiquidityUSD != nil {
		t.Fatalf("missing frozen quote fell back to current parent data: %#v, %#v, %v", event, snapshot, err)
	}
}

func TestFrozenMarketTradeEventPreservesLegacyAndRejectsMalformedProjection(t *testing.T) {
	parent := MarketEvent{ID: 51, Metadata: json.RawMessage(`{"legacy":true}`)}
	event, snapshot, present, err := frozenMarketTradeEvent(parent, json.RawMessage(`{"snapshot_id":7}`))
	if err != nil || present || snapshot != nil || !reflect.DeepEqual(event, parent) {
		t.Fatalf("legacy notification path changed: %#v, %v", event, err)
	}
	for _, details := range []json.RawMessage{
		json.RawMessage(`{"trade_projection":null}`),
		json.RawMessage(`{"trade_projection":"malformed"}`),
		json.RawMessage(`{"trade_projection":{"schema_version":2,"ready_id":103,"pool_ids":[10]}}`),
		json.RawMessage(`{"trade_projection":{"schema_version":1,"ready_id":0,"pool_ids":[10]}}`),
	} {
		if _, _, present, err := frozenMarketTradeEvent(parent, details); err == nil || !present {
			t.Fatalf("invalid frozen projection fell back to global event: %s, %v", details, err)
		}
	}
}

func TestFrozenMarketTradeProjectionRetainsHistoricalQualityWithoutTodaysRevaluation(t *testing.T) {
	details := json.RawMessage(`{"trade_projection":{"schema_version":1,"ready_id":10,"pool_ids":[3],"usd_value":"12","price_usd":"2","liquidity_usd":"100","liquidity_snapshots":[{"id":7,"pool_id":3,"captured_at":"2020-01-01T00:00:00Z","quote_quality":{"status":"live","fetched_at":"2020-01-01T00:00:00Z"}}]}}`)
	event, snapshot, present, err := frozenMarketTradeEvent(MarketEvent{ID: 1}, details)
	if err != nil || !present || event.USDValue == nil || *event.USDValue != "12" || snapshot == nil || snapshot.LiquidityUSD == nil || *snapshot.LiquidityUSD != "100" {
		t.Fatalf("historical frozen quote was revalued/rejected: %#v,%#v,%v", event, snapshot, err)
	}
	var frozen map[string]json.RawMessage
	if err := json.Unmarshal(event.Metadata, &frozen); err != nil {
		t.Fatal(err)
	}
	var original map[string]json.RawMessage
	_ = json.Unmarshal(details, &original)
	var got, want any
	_ = json.Unmarshal(frozen["trade_projection"], &got)
	_ = json.Unmarshal(original["trade_projection"], &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatal("notification changed trigger-time quote evidence")
	}
}

func TestFrozenTradeValuationQualityRequiresCompleteContemporaneousLegProofs(t *testing.T) {
	at := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	fetched, source := at.Add(-time.Second), at.Add(-2*time.Second)
	proof := marketquality.Quote{Status: "cached", FetchedAt: &fetched, SourceTimestamp: &source}
	raw, _ := json.Marshal(map[string]any{"valuation_schema_version": 1, "valuation_complete": true, "valuation_at": at, "valuation_quote_quality": proof})
	quality, when := frozenTradeValuationQuality([]int64{1, 2}, []json.RawMessage{raw, raw}, at)
	if proof.AvailableAt(at) {
		if quality.Status != "live" || quality.FetchedAt == nil || !quality.FetchedAt.Equal(fetched) || quality.SourceTimestamp == nil || !quality.SourceTimestamp.Equal(source) || !when.Equal(at) {
			t.Fatalf("frozen historical provenance not restored: %#v,%v", quality, when)
		}
	} else if quality.Status != "" || !when.IsZero() {
		t.Fatal("unavailable evidence was labelled live")
	}
	for _, cases := range []struct {
		ids      []int64
		metadata []json.RawMessage
	}{
		{nil, []json.RawMessage{raw}}, {[]int64{1, 2}, []json.RawMessage{raw}}, {[]int64{1, 2}, []json.RawMessage{raw, json.RawMessage(`{}`)}},
		{[]int64{1}, []json.RawMessage{json.RawMessage(`{"valuation_schema_version":1,"valuation_complete":true,"valuation_at":"2020-01-01T00:00:00Z","valuation_quote_quality":{"status":"stale_fallback","fetched_at":"2020-01-01T00:00:00Z"}}`)}},
	} {
		if got, when := frozenTradeValuationQuality(cases.ids, cases.metadata, at); got.Status != "" || !when.IsZero() {
			t.Fatalf("partial/missing/fallback proof forged quality: %#v,%v", got, when)
		}
	}
	if got, when := frozenTradeValuationQuality([]int64{1, 2}, []json.RawMessage{raw, raw}, at.Add(-time.Hour)); got.Status != "" || !when.IsZero() {
		t.Fatal("later valuation timestamps washed a historical parent fresh")
	}
}
