package marketview

import (
	"encoding/json"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"strings"
	"testing"
)

func TestTradeReadinessViewDistinguishesHistoryWarmupAndUnavailableCurrentData(t *testing.T) {
	warming := tradeRuleReadinessView(store.MarketTradeReadiness{Status: "warming", Reason: "scan_coverage_incomplete", ScopeFingerprint: "secret internal scope"})
	blocked := tradeRuleReadinessView(store.MarketTradeReadiness{Status: "blocked", Reason: "database_budget", ScopeFingerprint: "secret internal scope"})
	if !strings.Contains(warming.Description, "预热") || !strings.Contains(warming.Description, "不补发") || !strings.Contains(blocked.Description, "保留原进度") || warming.Description == blocked.Description {
		t.Fatalf("states are misleading: %+v/%+v", warming, blocked)
	}
	encoded, _ := json.Marshal([]TradeRuleReadinessView{warming, blocked})
	if strings.Contains(string(encoded), "secret internal scope") || strings.Contains(string(encoded), "database_budget") {
		t.Fatalf("internal provenance leaked in product descriptions: %s", encoded)
	}
}
