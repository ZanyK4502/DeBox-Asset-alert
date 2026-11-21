package subscription

import (
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestValidateMarketRuleExactStatisticalPeriods(t *testing.T) {
	for _, rule := range []struct {
		code, unit string
		valid      []int32
	}{
		{plans.MarketVolumeAbove, "usd", []int32{5, 60, 360, 1440}},
		{plans.MarketVolumeSpike, "ratio", []int32{5, 60, 360, 1440}},
		{plans.MarketTradeImbalance, "percent", []int32{5, 60, 1440}},
		{plans.MarketPriceIncrease, "percent", []int32{1, 17, 1440}},
		{plans.MarketConsecutiveLargeBuy, "token", []int32{1, 17, 1440}},
	} {
		for _, window := range []int32{0, 1, 5, 15, 17, 60, 360, 1440, 1441} {
			want := false
			for _, valid := range rule.valid {
				if valid == window {
					want = true
				}
			}
			// Custom rules accept every integer inside their range.
			if rule.code == plans.MarketPriceIncrease || rule.code == plans.MarketConsecutiveLargeBuy {
				want = window >= 1 && window <= 1440
			}
			params := store.CreateMarketRuleParams{DeBoxUserID: "fixture", MarketProjectID: 1, RuleType: rule.code, ThresholdValue: "2", ThresholdUnit: rule.unit, WindowMinutes: &window}
			if got := validateMarketRule(params) == nil; got != want {
				t.Fatalf("%s window%d accepted=%v want%v", rule.code, window, got, want)
			}
			if *params.WindowMinutes != window {
				t.Fatal("validation rewrote stored parameter")
			}
		}
	}
	legacy := int32(-30)
	if err := validateMarketRule(store.CreateMarketRuleParams{DeBoxUserID: "fixture", MarketProjectID: 1, RuleType: plans.MarketPriceAbove, ThresholdValue: "2", ThresholdUnit: "usd", WindowMinutes: &legacy}); err != nil || legacy != -30 {
		t.Fatalf("irrelevant legacy window changed/rejected: %d %v", legacy, err)
	}
}

func TestValidateMarketHolderRankIntegerBoundWithoutCaseBypass(t *testing.T) {
	for _, threshold := range []string{"0", "1", "20", "100", "101", "1.5"} {
		for _, code := range []string{plans.MarketHolderRankEntered, " MARKET_HOLDER_RANK_EXITED "} {
			err := validateMarketRule(store.CreateMarketRuleParams{DeBoxUserID: "fixture", MarketProjectID: 1, RuleType: code, ThresholdValue: threshold, ThresholdUnit: "count"})
			want := threshold == "1" || threshold == "20" || threshold == "100"
			if (err == nil) != want {
				t.Fatalf("rank=%s/%s err=%v", code, threshold, err)
			}
		}
	}
}

func TestValidateMarketStatisticalWindowDoesNotRestrictStageCycle(t *testing.T) {
	window := int32(5)
	params := store.CreateMarketRuleParams{DeBoxUserID: "fixture", MarketProjectID: 1, RuleType: plans.MarketVolumeAbove, ThresholdValue: "2", ThresholdUnit: "usd", WindowMinutes: &window, DeliveryMode: "stage", CycleMinutes: 17, TriggerCountThreshold: 2}
	if err := validateMarketRule(params); err != nil {
		t.Fatalf("independent stage cycle rejected: %v", err)
	}
	if params.CycleMinutes != 17 || *params.WindowMinutes != 5 {
		t.Fatal("stage/statistical periods conflated")
	}
}
