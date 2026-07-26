package subscription

import (
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestValidateMarketRuleRejectsMismatchedUnits(t *testing.T) {
	window := int32(60)
	tests := []store.CreateMarketRuleParams{
		{
			DeBoxUserID: "user", MarketProjectID: 1,
			RuleType: plans.MarketPriceAbove, ThresholdValue: "1",
			ThresholdUnit: "percent",
		},
		{
			DeBoxUserID: "user", MarketProjectID: 1,
			RuleType: plans.MarketVolumeSpike, ThresholdValue: "2",
			ThresholdUnit: "usd", WindowMinutes: &window,
		},
		{
			DeBoxUserID: "user", MarketProjectID: 1,
			RuleType: plans.MarketHolderRankEntered, ThresholdValue: "20",
			ThresholdUnit: "token",
		},
	}
	for _, params := range tests {
		if err := validateMarketRule(params); err == nil {
			t.Fatalf("validateMarketRule(%s/%s) succeeded", params.RuleType, params.ThresholdUnit)
		}
	}
}

func TestValidateMarketRuleAcceptsEveryRuleWithCanonicalUnit(t *testing.T) {
	window := int32(60)
	tests := []struct {
		ruleType string
		unit     string
	}{
		{plans.MarketPriceAbove, "usd"},
		{plans.MarketPriceBelow, "usd"},
		{plans.MarketPriceIncrease, "percent"},
		{plans.MarketPriceDecrease, "percent"},
		{plans.MarketLiquidityBelow, "usd"},
		{plans.MarketLiquidityDecrease, "percent"},
		{plans.MarketVolumeAbove, "usd"},
		{plans.MarketVolumeSpike, "ratio"},
		{plans.MarketTradeImbalance, "percent"},
		{plans.MarketLargeBuy, "usd"},
		{plans.MarketLargeSell, "usd"},
		{plans.MarketConsecutiveLargeBuy, "usd"},
		{plans.MarketConsecutiveLargeSell, "usd"},
		{plans.MarketLiquidityAdded, "usd"},
		{plans.MarketLiquidityRemoved, "usd"},
		{plans.MarketNewPool, "count"},
		{plans.MarketHolderIncrease, "usd"},
		{plans.MarketHolderDecrease, "usd"},
		{plans.MarketHolderRankEntered, "count"},
		{plans.MarketHolderRankExited, "count"},
		{plans.MarketFourMemeLargeTrade, "usd"},
		{plans.MarketFourMemeProgress, "progress"},
		{plans.MarketFourMemeMigration, "count"},
	}
	for _, test := range tests {
		params := store.CreateMarketRuleParams{
			DeBoxUserID: "user", MarketProjectID: 1,
			RuleType: test.ruleType, ThresholdValue: "1", ThresholdUnit: test.unit,
			WindowMinutes: &window,
		}
		if err := validateMarketRule(params); err != nil {
			t.Fatalf("validateMarketRule(%s/%s): %v", test.ruleType, test.unit, err)
		}
	}
}
