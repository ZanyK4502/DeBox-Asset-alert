package marketrules

import (
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestCrossTypeCombinationNotificationExplainsSignalAndOrder(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 7, 31, 1, 0, 0, 0, time.UTC)
	watchReached := start.Add(2 * time.Minute)
	marketReached := start.Add(5 * time.Minute)
	before, after, usd := "100", "70", "9000"
	wallet := "0x1111111111111111111111111111111111111111"
	transaction := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pool := "0x2222222222222222222222222222222222222222"
	text := MarketNotificationText(store.MarketNotificationDelivery{
		Kind:                 "combination",
		NotificationLanguage: "zh",
		Timezone:             "Asia/Shanghai",
		StartsAt:             start,
		EndsAt:               start.Add(time.Hour),
		Note:                 "<金库 & 市场>",
		CombinationMembers: []store.MarketCombinationProgress{
			{
				SourceType: "market", RuleType: plans.MarketLargeSell,
				TriggerCount: 1, RequiredTriggerCount: 1, ReachedAt: &marketReached,
				MarketRule:  &store.MarketRule{RuleType: plans.MarketLargeSell},
				RecentNotes: []string{"原始市场备注不应出现"},
				MarketEvents: []store.MarketNotificationEvent{{
					Project: store.MarketProject{TokenSymbol: "ABC"},
					Event: store.MarketEvent{
						ChainKey: "bsc", EventType: "sell", USDValue: &usd,
						WalletAddress: &wallet, TransactionHash: &transaction,
						TokenAddress: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
						OccurredAt:   marketReached,
					},
					Pool: &store.MarketPool{PoolAddress: &pool},
					Note: "原始市场事件详情不应出现",
				}},
			},
			{
				SourceType: "watch", RuleType: plans.Outgoing,
				TriggerCount: 1, RequiredTriggerCount: 1, ReachedAt: &watchReached,
				RecentNotes: []string{"完整钱包 0x3333333333333333333333333333333333333333"},
				WatchEvents: []store.StageTriggerEvent{{
					PreviousValue: &before, CurrentValue: &after,
					TokenSymbol: "BNB", OccurredAt: watchReached,
				}},
			},
		},
	})
	plainText := plainMarketNotificationText(text)
	for _, expected := range []string{
		"🔴 地址转出与大额卖出同时出现",
		"组合：&lt;金库 &amp; 市场&gt;",
		"① ✅ 市场 · ABC 单笔大额卖出 · 1/1 · 累计成交 $9,000",
		"② ✅ 地址 · 转出提醒 · 1/1 · 累计转出 30 BNB",
		"触发顺序：① 09:02 转出提醒 → ② 09:05 ABC 单笔大额卖出",
		"不代表因果关系或投资结论",
	} {
		if !strings.Contains(plainText, expected) {
			t.Fatalf("cross-type combination missing %q:\n%s", expected, text)
		}
	}
	for _, forbidden := range []string{
		wallet,
		transaction,
		pool,
		"0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"0x3333333333333333333333333333333333333333",
		"原始市场备注不应出现",
		"原始市场事件详情不应出现",
		"market_large_sell",
		"\n",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("cross-type combination contains %q:\n%s", forbidden, text)
		}
	}
	if lines := marketNotificationBlockCount(text); lines != 7 {
		t.Fatalf("cross-type combination lines = %d:\n%s", lines, text)
	}
}

func TestMarketCombinationNotificationUsesSpecificRiskSignal(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	firstReached := start.Add(3 * time.Minute)
	secondReached := start.Add(8 * time.Minute)
	sellUSD, decreaseAmount := "12500", "250000"
	text := MarketNotificationText(store.MarketNotificationDelivery{
		Kind: "combination", NotificationLanguage: "zh", Timezone: "UTC",
		StartsAt: start, EndsAt: start.Add(30 * time.Minute), Note: "大户风险",
		CombinationMembers: []store.MarketCombinationProgress{
			{
				SourceType: "market", RuleType: plans.MarketLargeSell,
				TriggerCount: 1, RequiredTriggerCount: 1, ReachedAt: &secondReached,
				MarketEvents: []store.MarketNotificationEvent{{
					Project: store.MarketProject{TokenSymbol: "RISK"},
					Event: store.MarketEvent{
						EventType: "sell", USDValue: &sellUSD, OccurredAt: secondReached,
					},
				}},
			},
			{
				SourceType: "market", RuleType: plans.MarketHolderDecrease,
				TriggerCount: 2, RequiredTriggerCount: 2, ReachedAt: &firstReached,
				MarketEvents: []store.MarketNotificationEvent{{
					Project: store.MarketProject{TokenSymbol: "RISK"},
					Event: store.MarketEvent{
						EventType: "holder_decrease", TokenAmount: &decreaseAmount,
						OccurredAt: firstReached,
					},
				}},
			},
		},
	})
	plainText := plainMarketNotificationText(text)
	for _, expected := range []string{
		"大户减持与大额卖出同时出现",
		"累计成交 $12,500",
		"累计减持 250,000 RISK",
		"触发顺序：① 12:03 RISK 大户减持 → ② 12:08 RISK 单笔大额卖出",
	} {
		if !strings.Contains(plainText, expected) {
			t.Fatalf("market combination missing %q:\n%s", expected, text)
		}
	}
}

func TestMarketCombinationNotificationEnglishHasNoInternalCodes(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	progress, ratio := "92.5", "4.2"
	text := MarketNotificationText(store.MarketNotificationDelivery{
		Kind: "combination", NotificationLanguage: "en", Timezone: "UTC",
		StartsAt: start, EndsAt: start.Add(time.Hour), Note: "Launch momentum",
		CombinationMembers: []store.MarketCombinationProgress{
			{
				SourceType: "market", RuleType: plans.MarketFourMemeProgress,
				TriggerCount: 1, RequiredTriggerCount: 1, ReachedAt: &start,
				MarketEvents: []store.MarketNotificationEvent{{
					Project:      store.MarketProject{TokenSymbol: "MEME"},
					Event:        store.MarketEvent{OccurredAt: start},
					CurrentValue: &progress,
				}},
			},
			{
				SourceType: "market", RuleType: plans.MarketVolumeSpike,
				TriggerCount: 1, RequiredTriggerCount: 1, ReachedAt: &start,
				MarketEvents: []store.MarketNotificationEvent{{
					Project:      store.MarketProject{TokenSymbol: "MEME"},
					Event:        store.MarketEvent{OccurredAt: start},
					CurrentValue: &ratio,
				}},
			},
		},
	})
	plainText := plainMarketNotificationText(text)
	for _, expected := range []string{
		"Four.meme progress and stronger volume appeared together",
		"highest progress 92.5%",
		"largest multiple 4.2×",
		"Signal order:",
		"Short-term signals can change",
	} {
		if !strings.Contains(plainText, expected) {
			t.Fatalf("English market combination missing %q:\n%s", expected, text)
		}
	}
	for _, forbidden := range []string{
		plans.MarketFourMemeProgress,
		plans.MarketVolumeSpike,
		"$-",
		"\n",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("English market combination contains %q:\n%s", forbidden, text)
		}
	}
	for _, character := range text {
		if unicode.Is(unicode.Han, character) {
			t.Fatalf("English market combination contains Chinese %q:\n%s", character, text)
		}
	}
}

func TestUnifiedCombinationCoversAllMemberRuleLabels(t *testing.T) {
	t.Parallel()
	ruleTypes := []string{
		plans.BalanceChange,
		plans.Incoming,
		plans.Outgoing,
		plans.BalanceThreshold,
		plans.HighBalanceThreshold,
		plans.ApprovalChange,
		plans.AddressInteraction,
		plans.MarketPriceAbove,
		plans.MarketPriceBelow,
		plans.MarketPriceIncrease,
		plans.MarketPriceDecrease,
		plans.MarketLiquidityBelow,
		plans.MarketLiquidityDecrease,
		plans.MarketVolumeAbove,
		plans.MarketVolumeSpike,
		plans.MarketTradeImbalance,
		plans.MarketLargeBuy,
		plans.MarketLargeSell,
		plans.MarketConsecutiveLargeBuy,
		plans.MarketConsecutiveLargeSell,
		plans.MarketLiquidityAdded,
		plans.MarketLiquidityRemoved,
		plans.MarketNewPool,
		plans.MarketHolderIncrease,
		plans.MarketHolderDecrease,
		plans.MarketHolderRankEntered,
		plans.MarketHolderRankExited,
		plans.MarketFourMemeLargeTrade,
		plans.MarketFourMemeProgress,
		plans.MarketFourMemeMigration,
	}
	for _, ruleType := range ruleTypes {
		ruleType := ruleType
		t.Run(ruleType, func(t *testing.T) {
			t.Parallel()
			sourceType := "market"
			if !strings.HasPrefix(ruleType, "market_") {
				sourceType = "watch"
			}
			member := store.MarketCombinationProgress{
				SourceType: sourceType, RuleType: ruleType,
				TriggerCount: 1, RequiredTriggerCount: 1,
			}
			for _, english := range []bool{false, true} {
				line := marketCombinationMemberLine(0, member, english)
				if strings.Contains(ruleType, "_") && strings.Contains(line, ruleType) {
					t.Fatalf("member exposes internal code %q: %s", ruleType, line)
				}
				if !strings.Contains(line, "1/1") {
					t.Fatalf("member omits progress: %s", line)
				}
				if english {
					for _, character := range line {
						if unicode.Is(unicode.Han, character) {
							t.Fatalf("English member contains Chinese %q: %s", character, line)
						}
					}
				}
			}
		})
	}
}

func TestCombinationNotificationOmitsUnavailableKeyResults(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	text := MarketNotificationText(store.MarketNotificationDelivery{
		Kind: "combination", NotificationLanguage: "en", Timezone: "UTC",
		StartsAt: start, EndsAt: start.Add(time.Hour),
		CombinationMembers: []store.MarketCombinationProgress{
			{
				SourceType: "market", RuleType: plans.MarketNewPool,
				TriggerCount: 1, RequiredTriggerCount: 1,
			},
			{
				SourceType: "market", RuleType: plans.MarketFourMemeMigration,
				TriggerCount: 1, RequiredTriggerCount: 1,
			},
		},
	})
	for _, forbidden := range []string{
		"new pools 0",
		"migration signals 0",
		"$-",
		"<nil>",
		"\n",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("combination with missing data contains %q:\n%s", forbidden, text)
		}
	}
}
