package marketwindow

import (
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
)

func TestPeriodsAreRuleSpecificWithoutSilentRounding(t *testing.T) {
	for _, sample := range []struct {
		code    string
		minutes int32
		valid   bool
	}{
		{plans.MarketVolumeAbove, 5, true}, {plans.MarketVolumeAbove, 15, false},
		{plans.MarketVolumeAbove, 7, false}, {plans.MarketVolumeAbove, 360, true},
		{plans.MarketTradeImbalance, 360, false}, {plans.MarketTradeImbalance, 1440, true},
		{plans.MarketConsecutiveLargeBuy, 17, true}, {plans.MarketConsecutiveLargeSell, 1441, false},
		{plans.MarketPriceIncrease, 1, true}, {plans.MarketPriceDecrease, 1440, true},
		{plans.MarketLiquidityDecrease, 0, false}, {plans.MarketNewPool, 999, true},
	} {
		original := sample.minutes
		_, valid := Effective(sample.code, &sample.minutes)
		if valid != sample.valid || sample.minutes != original {
			t.Fatalf("%s/%d validity=%v, expected %v without changing the original", sample.code, original, valid, sample.valid)
		}
	}
	if Validate(plans.MarketVolumeAbove, nil) == nil || Validate("not-a-rule", nil) == nil {
		t.Fatal("unknown or missing required period accepted")
	}
	if MaxBaselineLag != 30*time.Second || RecommendationLookback != 24*time.Hour || MaxRecommendationSamples != 100 {
		t.Fatal("confirmed policy changed")
	}
}

func TestRanksAreIntegerBoundedAndLegacyValuesRemainUnchanged(t *testing.T) {
	for _, input := range []string{"1", "10", "20", "50", "100", "20.0"} {
		if _, ok := HolderRank(input); !ok {
			t.Fatalf("supported rank %q rejected", input)
		}
	}
	for _, input := range []string{"0", "101", "10.5", "-1", "", "1/3"} {
		if _, ok := HolderRank(input); ok {
			t.Fatalf("unsupported rank %q accepted", input)
		}
		if len(RuntimeIssues(plans.MarketHolderRankEntered, nil, input)) == 0 {
			t.Fatalf("saved unsupported rank %q not reported", input)
		}
	}
}
