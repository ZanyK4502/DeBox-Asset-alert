package summary

import (
	"strings"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestDailySummaryUnknownUSDDoesNotClaimBalancedFlow(t *testing.T) {
	statistics := store.SummaryStatistics{ValuationSchemaVersion: 2, MarketProjectCount: 1, MarketBuyCount: 1, MarketSellCount: 1, MarketNetBuyUSD: "0"}
	for _, english := range []bool{false, true} {
		text := dailySummaryFundsDirection(statistics, english)
		if strings.Contains(text, "基本持平") || strings.Contains(text, "balanced") || strings.Contains(text, "$0") {
			t.Fatalf("unknown estimates shown as zero/balanced: %s", text)
		}
		gap := dailySummaryDataGap(statistics, nil, english)
		if !strings.Contains(gap, dailySummaryLabel("完整成交美元估值", "complete trade USD estimates", english)) {
			t.Fatalf("valuation gap missing: %s", gap)
		}
	}
}

func TestDailySummaryPartialValuationShowsSubtotalWithoutNetConclusion(t *testing.T) {
	statistics := store.SummaryStatistics{ValuationSchemaVersion: 2, MarketBuyCount: 2, MarketBuyValuedCount: 1, MarketBuyUSD: "120", MarketNetBuyUSD: "120"}
	for _, english := range []bool{false, true} {
		text := dailySummaryFundsDirection(statistics, english)
		if !strings.Contains(text, "$120") || !strings.Contains(text, dailySummaryLabel("部分小计", "partial subtotals", english)) || strings.Contains(text, "net inflow") || strings.Contains(text, "净流入") {
			t.Fatalf("partial subtotal claimed complete direction: %s", text)
		}
	}
}

func TestDailySummaryReportsUnknownQuoteEvidenceEvenWithSomeValidSamples(t *testing.T) {
	statistics := store.SummaryStatistics{MarketProjectCount: 1}
	price := "1"
	items := []store.MarketProjectChainSummary{{SnapshotCount: 3, PriceSampleCount: 2, LiquiditySampleCount: 2, VolumeSampleCount: 2, StartPriceUSD: &price, EndPriceUSD: &price, QuoteUnavailableCount: 1}}
	if text := dailySummaryDataGap(statistics, items, true); !strings.Contains(text, "valid quote evidence") {
		t.Fatalf("unknown quote observation hidden by valid samples: %s", text)
	}
}
