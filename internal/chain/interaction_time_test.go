package chain

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestInteractionTimeKeepsDocumentedTransactionCreationTime(t *testing.T) {
	for _, value := range []any{int64(1733082431), float64(1733082431), json.Number("1733082431"), "1733082431"} {
		at, basis := interactionSourceTime(map[string]any{"timestamp": value})
		if at == nil || !at.Equal(time.Unix(1733082431, 0)) || basis != "nodit_transaction_created_at" {
			t.Fatalf("source time lost for %v: %v/%s", value, at, basis)
		}
	}
	for _, value := range []any{nil, true, -1, 0, 1733082431000.0, 1733082431.5, math.NaN(), math.Inf(1), "2026-11-14T00:00:00Z", "1.733082431e9", json.Number("1.5")} {
		if at, basis := interactionSourceTime(map[string]any{"timestamp": value}); at != nil || basis != "" {
			t.Fatalf("invented time from invalid source %v: %v/%s", value, at, basis)
		}
	}
	if at, _ := interactionSourceTime(map[string]any{"observed_at": time.Now(), "blockTimestamp": 1733082431}); at != nil {
		t.Fatal("unverified alternate field substituted for documented timestamp")
	}
}
