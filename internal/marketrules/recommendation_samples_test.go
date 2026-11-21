package marketrules

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestValidatedRecommendationSamplesReuseOriginalPresetAndQuantileFormula(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	snapshot := store.MarketSnapshot{PriceUSD: stringPointer("1"), LiquidityUSD: stringPointer("1000000"), Volume24hUSD: stringPointer("10000"), QuoteQuality: liveQuoteFixture(now)}
	var events []store.MarketEvent
	var samples []store.MarketRecommendationSample
	for _, amount := range []string{"100", "200000", "300000"} {
		value := amount
		events = append(events, withValuationProofFixture(store.MarketEvent{USDValue: &value, Confirmed: 1, OccurredAt: now}, now))
		samples = append(samples, store.MarketRecommendationSample{USDValue: value, OccurredAt: now})
	}
	old := RecommendThresholdsAt(snapshot, events, now)
	validated := RecommendThresholdsWithSamplesAt(snapshot, samples, now)
	a, _ := json.Marshal(old)
	b, _ := json.Marshal(validated)
	if len(old) != 63 || !bytes.Equal(a, b) {
		t.Fatalf("dedicated samples changed the original formula: old=%s new=%s", a, b)
	}
	base := RecommendThresholdsAt(snapshot, nil, now)
	unproved := RecommendThresholdsAt(snapshot, []store.MarketEvent{{USDValue: stringPointer("200000"), Confirmed: 1, OccurredAt: now}}, now)
	a, _ = json.Marshal(base)
	b, _ = json.Marshal(unproved)
	if !bytes.Equal(a, b) {
		t.Fatal("dedicated DTO adapter relaxed ordinary Event proof")
	}
	snapshot.QuoteQuality = marketquality.Quote{Status: marketquality.StatusStaleFallback, FetchedAt: &now}
	if len(RecommendThresholdsWithSamplesAt(snapshot, samples, now)) != 0 {
		t.Fatal("validated samples bypassed current snapshot quality")
	}
}
