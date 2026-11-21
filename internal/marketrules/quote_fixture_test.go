package marketrules

import (
	"encoding/json"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func liveQuoteFixture(at time.Time) marketquality.Quote {
	at = at.UTC().Truncate(time.Microsecond)
	return marketquality.Quote{Status: marketquality.StatusLive, FetchedAt: &at}
}

func valuationProofFixture(at time.Time) map[string]any {
	at = at.UTC().Truncate(time.Microsecond)
	return map[string]any{"valuation_schema_version": 1, "valuation_complete": true,
		"valuation_quote_quality": liveQuoteFixture(at), "valuation_at": at}
}

func withValuationProofFixture(event store.MarketEvent, at time.Time) store.MarketEvent {
	if event.OccurredAt.IsZero() {
		event.OccurredAt = at
	}
	metadata := make(map[string]any)
	_ = json.Unmarshal(event.Metadata, &metadata)
	for key, value := range valuationProofFixture(at) {
		metadata[key] = value
	}
	event.Metadata, _ = json.Marshal(metadata)
	return event
}

// Existing threshold/cooldown fixtures describe successful observations. Give
// them explicit provenance; quality-failure tests call Evaluate directly.
func evaluateFreshQuoteFixture(input EvaluationInput) (Evaluation, error) {
	input.Snapshots = append([]store.MarketSnapshot(nil), input.Snapshots...)
	for index := range input.Snapshots {
		input.Snapshots[index].QuoteQuality = liveQuoteFixture(input.Snapshots[index].CapturedAt)
	}
	input.Events = append([]store.MarketEvent(nil), input.Events...)
	for index, event := range input.Events {
		input.Events[index] = withValuationProofFixture(event, event.OccurredAt)
	}
	return Evaluate(input)
}
