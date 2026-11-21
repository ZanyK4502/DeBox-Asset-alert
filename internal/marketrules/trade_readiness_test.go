package marketrules

import (
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"testing"
	"time"
)

func TestTradeReadinessHistoricalUnknownWarmsOnlyUntilItLeavesOriginalWindow(t *testing.T) {
	at := time.Now().UTC()
	window := int32(5)
	rule := store.MarketRule{RuleType: plans.MarketConsecutiveLargeBuy, WindowMinutes: &window}
	candidates := []store.MarketTradeWindowCandidate{{TradeReadyID: 1, ParentID: 11, OccurredAt: at, Unknown: true}, {TradeReadyID: 2, ParentID: 12, OccurredAt: at.Add(time.Minute), Qualified: true}, {TradeReadyID: 3, ParentID: 13, OccurredAt: at.Add(6 * time.Minute), Qualified: true}}
	events := []store.MarketEvent{{ID: 12, TradeReadyID: 2, OccurredAt: at.Add(time.Minute), EventType: "buy", Confirmed: 1}, {ID: 13, TradeReadyID: 3, OccurredAt: at.Add(6 * time.Minute), EventType: "buy", Confirmed: 1}}
	counts, warmup, err := completeTradeWindowCountsReadiness(rule, events, candidates, 1)
	if err != nil || warmup[12] != "historical_valuation_unavailable" || warmup[13] != "" || counts[3] != 2 {
		t.Fatalf("historical warmup changed window/count semantics: counts=%v warmup=%v error=%v", counts, warmup, err)
	}
	candidates[1].Qualified = false
	candidates[1].Unknown = true
	if _, _, err := completeTradeWindowCountsReadiness(rule, events, candidates, 1); err == nil {
		t.Fatal("current unknown candidate treated as disposable historical warmup")
	}
}
