package marketrules

import (
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestRecommendThresholdsUsesMarketDepthAndPresetOrder(t *testing.T) {
	liquidity, volume := "100000", "200000"
	trade100, trade1000 := "100", "1000"
	values := RecommendThresholds(
		store.MarketSnapshot{LiquidityUSD: &liquidity, Volume24hUSD: &volume},
		[]store.MarketEvent{{USDValue: &trade100}, {USDValue: &trade1000}},
	)
	if len(values) != 63 {
		t.Fatalf("recommendations = %d, want 63", len(values))
	}
	var largeBuy []Recommendation
	for _, value := range values {
		if value.RuleType == plans.MarketLargeBuy {
			largeBuy = append(largeBuy, value)
		}
	}
	if len(largeBuy) != 3 {
		t.Fatalf("large buy presets = %d, want 3", len(largeBuy))
	}
	for _, value := range largeBuy {
		if value.WindowMinutes != 0 {
			t.Fatalf("single large buy window = %d, want 0", value.WindowMinutes)
		}
	}
	counts := make(map[string]int)
	for _, value := range values {
		counts[value.RuleType]++
	}
	for ruleType, count := range counts {
		if count != 3 {
			t.Fatalf("%s presets = %d, want 3", ruleType, count)
		}
	}
	if counts[plans.MarketNewPool] != 0 ||
		counts[plans.MarketFourMemeMigration] != 0 {
		t.Fatalf("event-only rules unexpectedly have recommendations: %#v", counts)
	}
	if largeBuy[0].Sensitivity != "sensitive" ||
		largeBuy[1].Sensitivity != "balanced" ||
		largeBuy[2].Sensitivity != "stable" {
		t.Fatalf("preset order = %#v", largeBuy)
	}
	sensitive, _ := rat(largeBuy[0].Threshold)
	balanced, _ := rat(largeBuy[1].Threshold)
	stable, _ := rat(largeBuy[2].Threshold)
	if sensitive.Cmp(balanced) >= 0 || balanced.Cmp(stable) >= 0 {
		t.Fatalf("thresholds are not increasing: %#v", largeBuy)
	}
}
