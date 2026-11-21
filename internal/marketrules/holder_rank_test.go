package marketrules

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestHolderRankRulesCrossActualUserNWithoutCurrentHolderMembership(t *testing.T) {
	for _, n := range []int32{10, 20, 50, 100} {
		for _, entered := range []bool{false, true} {
			typeCode := plans.MarketHolderRankExited
			old, new := n, n+1
			if entered {
				typeCode = plans.MarketHolderRankEntered
				old, new = n+1, n
			}
			var oldRank, newRank *int32
			if old <= 100 {
				oldRank = &old
			}
			if new <= 100 {
				newRank = &new
			}
			now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
			address := "0x1111111111111111111111111111111111111111"
			metadata, _ := json.Marshal(map[string]any{"holder_rank_schema_version": 1, "old_rank": oldRank, "new_rank": newRank, "old_covered_top_n": 100, "new_covered_top_n": 100, "old_observation_at": now.Add(-time.Minute), "new_observation_at": now})
			event := store.MarketEvent{ID: 1, EventType: "holder_rank_changed", Source: "nodit_holders_sample", WalletAddress: &address, Confirmed: 1, OccurredAt: now, Metadata: metadata}
			rule := store.MarketRule{RuleType: typeCode, ThresholdUnit: "count", ThresholdValue: decimalRank(n)}
			result, err := Evaluate(EvaluationInput{Rule: rule, Events: []store.MarketEvent{event}, Now: now})
			if err != nil || len(result.Triggers) != 1 {
				t.Fatalf("N=%d entered=%v boundary missed without current100: %#v,%v", n, entered, result, err)
			}
			result, err = Evaluate(EvaluationInput{Rule: rule, Events: []store.MarketEvent{event}, Labels: []store.MarketAddressLabel{{Address: address, Excluded: 1}}, Now: now})
			if err != nil || len(result.Triggers) != 0 || len(result.Suppressed) != 1 || result.Suppressed[0].Reason != "holder_excluded" {
				t.Fatalf("per-user excluded behavior changed: %#v,%v", result, err)
			}
		}
	}
}

func TestHolderRankUnknownCoverageDoesNotBecomeExitOrZero(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	rank := int32(10)
	for _, coverage := range []int32{0, 9} {
		metadata, _ := json.Marshal(map[string]any{"holder_rank_schema_version": 1, "old_rank": &rank, "new_rank": nil, "old_covered_top_n": 100, "new_covered_top_n": coverage, "old_observation_at": now.Add(-time.Minute), "new_observation_at": now})
		matched, value := holderRankCrossing(store.MarketRule{RuleType: plans.MarketHolderRankExited, ThresholdValue: "10"}, store.MarketEvent{EventType: "holder_rank_changed", Source: "nodit_holders_sample", OccurredAt: now, Metadata: metadata})
		if matched || value != nil {
			t.Fatalf("unobserved rank became an exit/zero: %v,%v", matched, value)
		}
	}
}

func decimalRank(value int32) string { raw, _ := json.Marshal(value); return string(raw) }
