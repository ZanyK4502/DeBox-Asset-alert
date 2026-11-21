// Package marketwindow defines the supported rule periods and rank boundaries.
// It performs no I/O and never rewrites a saved parameter.
package marketwindow

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
)

const (
	KindNone                       = "none"
	KindFixed                      = "fixed"
	KindCustomSnapshot             = "custom_snapshot"
	KindCustomEvent                = "custom_event"
	MaxCustomWindowMinutes   int32 = 1440
	MaxHolderRank            int32 = 100
	MaxBaselineLag                 = 30 * time.Second
	RecommendationLookback         = 24 * time.Hour
	MaxRecommendationSamples       = 100
)

var ErrUnsupported = errors.New("unsupported market rule parameters")

type Capability struct {
	Kind           string  `json:"kind"`
	FixedMinutes   []int32 `json:"fixed_minutes,omitempty"`
	MinMinutes     int32   `json:"min_minutes,omitempty"`
	MaxMinutes     int32   `json:"max_minutes,omitempty"`
	DefaultMinutes int32   `json:"default_minutes"`
}

type Issue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func ForRule(code string) Capability {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case plans.MarketPriceIncrease, plans.MarketPriceDecrease, plans.MarketLiquidityDecrease:
		return Capability{Kind: KindCustomSnapshot, MinMinutes: 1, MaxMinutes: MaxCustomWindowMinutes, DefaultMinutes: 60}
	case plans.MarketConsecutiveLargeBuy, plans.MarketConsecutiveLargeSell:
		return Capability{Kind: KindCustomEvent, MinMinutes: 1, MaxMinutes: MaxCustomWindowMinutes, DefaultMinutes: 15}
	case plans.MarketVolumeAbove, plans.MarketVolumeSpike:
		return Capability{Kind: KindFixed, FixedMinutes: []int32{5, 60, 360, 1440}, DefaultMinutes: 60}
	case plans.MarketTradeImbalance:
		return Capability{Kind: KindFixed, FixedMinutes: []int32{5, 60, 1440}, DefaultMinutes: 60}
	case plans.MarketPriceAbove, plans.MarketPriceBelow, plans.MarketLiquidityBelow,
		plans.MarketLargeBuy, plans.MarketLargeSell, plans.MarketLiquidityAdded, plans.MarketLiquidityRemoved,
		plans.MarketNewPool, plans.MarketHolderIncrease, plans.MarketHolderDecrease,
		plans.MarketHolderRankEntered, plans.MarketHolderRankExited,
		plans.MarketFourMemeLargeTrade, plans.MarketFourMemeProgress, plans.MarketFourMemeMigration:
		return Capability{Kind: KindNone}
	default:
		return Capability{}
	}
}

// Effective validates required periods. An unused legacy period on a rule with
// no statistical window remains untouched and has no effect on its evaluation.
func Effective(code string, minutes *int32) (int32, bool) {
	capability := ForRule(code)
	if capability.Kind == KindNone {
		return 0, true
	}
	if capability.Kind == "" || minutes == nil {
		return 0, false
	}
	if capability.Kind == KindFixed {
		for _, allowed := range capability.FixedMinutes {
			if *minutes == allowed {
				return *minutes, true
			}
		}
		return 0, false
	}
	if *minutes < capability.MinMinutes || *minutes > capability.MaxMinutes {
		return 0, false
	}
	return *minutes, true
}

func Validate(code string, minutes *int32) error {
	if _, ok := Effective(code, minutes); ok {
		return nil
	}
	return fmt.Errorf("%w: rule %s does not support this statistical period", ErrUnsupported, code)
}

func HolderRank(threshold string) (int32, bool) {
	threshold = strings.TrimSpace(threshold)
	if len(threshold) == 0 || len(threshold) > 128 {
		return 0, false
	}
	number, ok := new(big.Rat).SetString(threshold)
	if !ok || !number.IsInt() || number.Sign() <= 0 || number.Cmp(big.NewRat(int64(MaxHolderRank), 1)) > 0 {
		return 0, false
	}
	return int32(number.Num().Int64()), true
}

func RuntimeIssues(code string, minutes *int32, threshold string) []Issue {
	code = strings.ToLower(strings.TrimSpace(code))
	var issues []Issue
	if err := Validate(code, minutes); err != nil {
		issues = append(issues, Issue{Code: "unsupported_statistical_period", Message: err.Error()})
	}
	if code == plans.MarketHolderRankEntered || code == plans.MarketHolderRankExited {
		if _, ok := HolderRank(threshold); !ok {
			issues = append(issues, Issue{Code: "unsupported_holder_rank", Message: "Holder rank must be an integer from 1 to 100; the saved value has been preserved."})
		}
	}
	return issues
}

// KnownRules is a fresh slice so callers cannot mutate the shared capability
// registry while preparing a catalog, an audit, or a database predicate.
func KnownRules() []string {
	return []string{
		plans.MarketPriceAbove, plans.MarketPriceBelow, plans.MarketPriceIncrease, plans.MarketPriceDecrease,
		plans.MarketLiquidityBelow, plans.MarketLiquidityDecrease, plans.MarketVolumeAbove, plans.MarketVolumeSpike,
		plans.MarketTradeImbalance, plans.MarketLargeBuy, plans.MarketLargeSell,
		plans.MarketConsecutiveLargeBuy, plans.MarketConsecutiveLargeSell,
		plans.MarketLiquidityAdded, plans.MarketLiquidityRemoved, plans.MarketNewPool,
		plans.MarketHolderIncrease, plans.MarketHolderDecrease, plans.MarketHolderRankEntered, plans.MarketHolderRankExited,
		plans.MarketFourMemeLargeTrade, plans.MarketFourMemeProgress, plans.MarketFourMemeMigration,
	}
}
