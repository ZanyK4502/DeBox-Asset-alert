package summary

import (
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestEnglishDailySummaryUsesSingularFailureCount(t *testing.T) {
	subscription := testSubscription(99)
	subscription.DailySummaryLanguage = "en"
	periodEnd := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	text := buildSummaryText(
		subscription,
		periodEnd.Add(-24*time.Hour),
		periodEnd,
		store.SummaryStatistics{RuleCount: 1, FailedNotificationCount: 1},
		nil,
		nil,
		nil,
	)
	if !strings.Contains(text, "Notification delivery failed 1 time;") ||
		strings.Contains(text, "1 times") {
		t.Fatalf("English failure count is not singular: %s", text)
	}
}

func TestDailySummaryNestedColonPrefixesAreBold(t *testing.T) {
	statistics := store.SummaryStatistics{
		AddressRiskEventCount: 1,
		LiquidityEventCount:   1,
	}
	startPrice := "0.01"
	endPrice := "0.02"
	tradeUSD := "18650"
	marketSummaries := []store.MarketProjectChainSummary{{
		ChainKey:      "bsc",
		TokenSymbol:   "BOX",
		StartPriceUSD: &startPrice,
		EndPriceUSD:   &endPrice,
	}}
	marketEvents := []store.MarketSummaryEvent{{
		EventType:   "buy",
		TokenSymbol: "BOX",
		USDValue:    &tradeUSD,
	}}

	checks := []struct {
		name string
		text string
		want string
	}{
		{"Chinese conclusion", dailySummaryConclusion(statistics, 0, false, true, false), "🟠 <b>需要关注</b>："},
		{"English conclusion", dailySummaryConclusion(statistics, 0, false, true, true), "🟠 <b>Attention needed</b>:"},
		{"Chinese activity", strings.Join(dailySummaryHighlights(statistics, nil, nil, nil, false, false), ""), "<b>市场动态</b>："},
		{"English activity", strings.Join(dailySummaryHighlights(statistics, nil, nil, nil, false, true), ""), "<b>Market activity</b>:"},
		{"Chinese price move", dailySummaryLargestPriceMove(marketSummaries, false), "<b>最大价格波动</b>："},
		{"English price move", dailySummaryLargestPriceMove(marketSummaries, true), "<b>Largest price move</b>:"},
		{"Chinese trade", dailySummaryLargestTrade(marketEvents, false), "<b>最大成交</b>："},
		{"English trade", dailySummaryLargestTrade(marketEvents, true), "<b>Largest trade</b>:"},
		{"English data coverage", dailySummaryDataGap(store.SummaryStatistics{MarketProjectCount: 1}, nil, true), "<b>affected</b>:"},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if !strings.Contains(check.text, check.want) {
				t.Fatalf("text does not contain bold prefix %q: %s", check.want, check.text)
			}
		})
	}
}
