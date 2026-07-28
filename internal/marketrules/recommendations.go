package marketrules

import (
	"math/big"
	"sort"
	"strings"

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
	volumeAbove := recommendedVolumeAbove(snapshot)
	liquidityChange := recommendedLiquidityChange(snapshot)
	price, hasPrice := pointerRat(snapshot.PriceUSD)
	liquidity, hasLiquidity := pointerRat(snapshot.LiquidityUSD)
	result := make([]Recommendation, 0, 60)
	for _, preset := range []struct {
		name                   string
		multiplier             *big.Rat
		priceAboveMultiplier   *big.Rat
		priceBelowMultiplier   *big.Rat
		liquidityBelowMultiple *big.Rat
		priceChange            string
		liquidityChangePercent string
		volumeSpike            string
		imbalance              string
		holderChange           string
		holderRank             string
		fourMemeProgress       string
		priceAboveFallback     string
		priceBelowFallback     string
		liquidityFallback      string
		cooldown               int32
	}{
		{
			"sensitive", big.NewRat(1, 2),
			big.NewRat(102, 100), big.NewRat(98, 100), big.NewRat(90, 100),
			"2", "5", "1.5", "65", "2", "50", "60",
			"0.5", "0.2", "20000", 120,
		},
		{
			"balanced", big.NewRat(1, 1),
			big.NewRat(105, 100), big.NewRat(95, 100), big.NewRat(75, 100),
			"5", "10", "2", "75", "5", "20", "80",
			"1", "0.1", "10000", 300,
		},
		{
			"stable", big.NewRat(2, 1),
			big.NewRat(110, 100), big.NewRat(90, 100), big.NewRat(50, 100),
			"10", "20", "3", "85", "10", "10", "95",
			"2", "0.05", "5000", 900,
		},
	} {
		trade := new(big.Rat).Mul(largeTrade, preset.multiplier)
		trade = clampRat(trade, big.NewRat(50, 1), dynamicTradeMaximum(snapshot))
		volume := new(big.Rat).Mul(volumeAbove, preset.multiplier)
		volume = clampRat(volume, big.NewRat(50, 1), big.NewRat(10_000_000, 1))
		liquidityEvent := new(big.Rat).Mul(liquidityChange, preset.multiplier)
		liquidityEvent = clampRat(
			liquidityEvent, big.NewRat(100, 1), big.NewRat(10_000_000, 1),
		)
		priceAbove := preset.priceAboveFallback
		priceBelow := preset.priceBelowFallback
		if hasPrice && price.Sign() > 0 {
			priceAbove = recommendationDecimalString(
				new(big.Rat).Mul(price, preset.priceAboveMultiplier),
			)
			priceBelow = recommendationDecimalString(
				new(big.Rat).Mul(price, preset.priceBelowMultiplier),
			)
		}
		liquidityBelow := preset.liquidityFallback
		if hasLiquidity && liquidity.Sign() > 0 {
			liquidityBelow = moneyString(
				new(big.Rat).Mul(liquidity, preset.liquidityBelowMultiple),
			)
		}
		result = append(result,
			Recommendation{plans.MarketPriceAbove, preset.name, priceAbove, "usd", 0, preset.cooldown},
			Recommendation{plans.MarketPriceBelow, preset.name, priceBelow, "usd", 0, preset.cooldown},
			Recommendation{plans.MarketPriceIncrease, preset.name, preset.priceChange, "percent", 60, preset.cooldown},
			Recommendation{plans.MarketPriceDecrease, preset.name, preset.priceChange, "percent", 60, preset.cooldown},
			Recommendation{plans.MarketLiquidityBelow, preset.name, liquidityBelow, "usd", 0, preset.cooldown},
			Recommendation{plans.MarketLiquidityDecrease, preset.name, preset.liquidityChangePercent, "percent", 60, preset.cooldown},
			Recommendation{plans.MarketVolumeAbove, preset.name, moneyString(volume), "usd", 60, preset.cooldown},
			Recommendation{plans.MarketVolumeSpike, preset.name, preset.volumeSpike, "ratio", 60, preset.cooldown},
			Recommendation{plans.MarketTradeImbalance, preset.name, preset.imbalance, "percent", 60, preset.cooldown},
			Recommendation{plans.MarketLargeBuy, preset.name, moneyString(trade), "usd", 15, preset.cooldown},
			Recommendation{plans.MarketLargeSell, preset.name, moneyString(trade), "usd", 15, preset.cooldown},
			Recommendation{plans.MarketConsecutiveLargeBuy, preset.name, moneyString(trade), "usd", 15, preset.cooldown},
			Recommendation{plans.MarketConsecutiveLargeSell, preset.name, moneyString(trade), "usd", 15, preset.cooldown},
			Recommendation{plans.MarketLiquidityAdded, preset.name, moneyString(liquidityEvent), "usd", 0, preset.cooldown},
			Recommendation{plans.MarketLiquidityRemoved, preset.name, moneyString(liquidityEvent), "usd", 0, preset.cooldown},
			Recommendation{plans.MarketHolderIncrease, preset.name, preset.holderChange, "percent", 0, preset.cooldown},
			Recommendation{plans.MarketHolderDecrease, preset.name, preset.holderChange, "percent", 0, preset.cooldown},
			Recommendation{plans.MarketHolderRankEntered, preset.name, preset.holderRank, "count", 0, preset.cooldown},
			Recommendation{plans.MarketHolderRankExited, preset.name, preset.holderRank, "count", 0, preset.cooldown},
			Recommendation{plans.MarketFourMemeLargeTrade, preset.name, moneyString(trade), "usd", 0, preset.cooldown},
			Recommendation{plans.MarketFourMemeProgress, preset.name, preset.fourMemeProgress, "percent", 0, preset.cooldown},
		)
	}
	return result
}

func recommendedVolumeAbove(snapshot store.MarketSnapshot) *big.Rat {
	if value, ok := pointerRat(snapshot.Volume1hUSD); ok && value.Sign() > 0 {
		return value
	}
	if value, ok := pointerRat(snapshot.Volume24hUSD); ok && value.Sign() > 0 {
		return new(big.Rat).Quo(value, big.NewRat(24, 1))
	}
	return big.NewRat(10_000, 1)
}

func recommendedLiquidityChange(snapshot store.MarketSnapshot) *big.Rat {
	if liquidity, ok := pointerRat(snapshot.LiquidityUSD); ok && liquidity.Sign() > 0 {
		return clampRat(
			new(big.Rat).Mul(liquidity, big.NewRat(5, 1000)),
			big.NewRat(500, 1),
			big.NewRat(10_000_000, 1),
		)
	}
	return big.NewRat(5_000, 1)
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

func recommendationDecimalString(value *big.Rat) string {
	result := strings.TrimRight(strings.TrimRight(value.FloatString(18), "0"), ".")
	if result == "" {
		return "0"
	}
	return result
}
