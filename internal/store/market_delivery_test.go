package store

import (
	"encoding/json"
	"testing"
)

func TestMarketRuleEventSnapshotID(t *testing.T) {
	if got := marketRuleEventSnapshotID(json.RawMessage(`{"snapshot_id":42}`)); got != 42 {
		t.Fatalf("marketRuleEventSnapshotID() = %d, want 42", got)
	}
	for _, details := range []json.RawMessage{
		nil,
		json.RawMessage(`{}`),
		json.RawMessage(`{"snapshot_id":"42"}`),
		json.RawMessage(`not-json`),
	} {
		if got := marketRuleEventSnapshotID(details); got != 0 {
			t.Fatalf("marketRuleEventSnapshotID(%q) = %d, want 0", details, got)
		}
	}
}
