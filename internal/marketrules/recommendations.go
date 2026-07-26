package marketrules

import (
	"math/big"
	"sort"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type Recommendation struct {
	RuleType      string `json:"rule_type"`
	Sensitivity   string `json:"sensitivity"`
	Threshold     string `json:"threshold"`
	ThresholdUnit string `json:"threshold_unit"`
	WindowMinutes int32  `json:"window_minutes"`
	Cooldown      int32  `json:"cooldown_seconds"`
}

func RecommendThresholds(
	snapshot store.MarketSnapshot,
	recentEvents []store.MarketEvent,
) []Recommendation {
	largeTrade := recommendedLargeTrade(snapshot, recentEvents)
	result := make([]Recommendation, 0, 21)
	for _, preset := range []struct {
		name       string
		multiplier *big.Rat
		price      string
		liquidity  string
		volume     string
		imbalance  string
		cooldown   int32
	}{
		{"sensitive", big.NewRat(1, 2), "2", "5", "1.5", "65", 120},
		{"balanced", big.NewRat(1, 1), "5", "10", "2", "75", 300},
		{"stable", big.NewRat(2, 1), "10", "20", "3", "85", 900},
	} {
		trade := new(big.Rat).Mul(largeTrade, preset.multiplier)
		trade = clampRat(trade, big.NewRat(50, 1), dynamicTradeMaximum(snapshot))
		result = append(result,
			Recommendation{plans.MarketLargeBuy, preset.name, moneyString(trade), "usd", 15, preset.cooldown},
			Recommendation{plans.MarketLargeSell, preset.name, moneyString(trade), "usd", 15, preset.cooldown},
			Recommendation{plans.MarketPriceIncrease, preset.name, preset.price, "percent", 60, preset.cooldown},
			Recommendation{plans.MarketPriceDecrease, preset.name, preset.price, "percent", 60, preset.cooldown},
			Recommendation{plans.MarketLiquidityDecrease, preset.name, preset.liquidity, "percent", 60, preset.cooldown},
			Recommendation{plans.MarketVolumeSpike, preset.name, preset.volume, "ratio", 60, preset.cooldown},
			Recommendation{plans.MarketTradeImbalance, preset.name, preset.imbalance, "percent", 60, preset.cooldown},
		)
	}
	return result
}

func recommendedLargeTrade(
	snapshot store.MarketSnapshot,
	recentEvents []store.MarketEvent,
) *big.Rat {
	candidates := []*big.Rat{big.NewRat(100, 1)}
	if liquidity, ok := pointerRat(snapshot.LiquidityUSD); ok {
		candidates = append(candidates, new(big.Rat).Mul(liquidity, big.NewRat(5, 1000)))
	}
	if volume, ok := pointerRat(snapshot.Volume24hUSD); ok {
		candidates = append(candidates, new(big.Rat).Mul(volume, big.NewRat(1, 100)))
	}
	trades := make([]*big.Rat, 0, len(recentEvents))
	for _, event := range recentEvents {
		if value, ok := pointerRat(event.USDValue); ok && value.Sign() > 0 {
			trades = append(trades, value)
		}
	}
	if len(trades) > 0 {
		sort.Slice(trades, func(i, j int) bool { return trades[i].Cmp(trades[j]) < 0 })
		index := (len(trades) - 1) * 3 / 4
		candidates = append(candidates, trades[index])
	}
	result := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.Cmp(result) > 0 {
			result = candidate
		}
	}
	return clampRat(result, big.NewRat(50, 1), dynamicTradeMaximum(snapshot))
}

func dynamicTradeMaximum(snapshot store.MarketSnapshot) *big.Rat {
	maximum := big.NewRat(1_000_000, 1)
	if liquidity, ok := pointerRat(snapshot.LiquidityUSD); ok && liquidity.Sign() > 0 {
		candidate := new(big.Rat).Mul(liquidity, big.NewRat(1, 10))
		if candidate.Cmp(big.NewRat(500, 1)) < 0 {
			candidate = big.NewRat(500, 1)
		}
		if candidate.Cmp(maximum) < 0 {
			maximum = candidate
		}
	}
	return maximum
}

func clampRat(value, minimum, maximum *big.Rat) *big.Rat {
	if value.Cmp(minimum) < 0 {
		return new(big.Rat).Set(minimum)
	}
	if maximum.Sign() > 0 && value.Cmp(maximum) > 0 {
		return new(big.Rat).Set(maximum)
	}
	return value
}

func moneyString(value *big.Rat) string {
	rounded := new(big.Rat).Add(value, big.NewRat(1, 2))
	integer := new(big.Int).Quo(rounded.Num(), rounded.Denom())
	return integer.String()
}
