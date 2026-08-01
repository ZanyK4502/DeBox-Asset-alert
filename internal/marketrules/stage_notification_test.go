package marketrules

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestAllMarketStageRulesUseStatisticalSummaries(t *testing.T) {
	tests := marketStageRuleCases()
	for _, test := range tests {
		t.Run(test.ruleType, func(t *testing.T) {
			delivery := marketStageTestDelivery(test, "zh")
			text := MarketNotificationText(delivery)
			plainText := plainMarketNotificationText(text)
			for _, expected := range []string{
				test.expectedZH,
				"规则：",
				"统计周期：2026-07-31 20:00 → 2026-07-31 21:00",
				"触发次数：3 次（达到 3 次时发送）",
				"你的条件：",
				"首次 / 最近：BNB Chain · 20:05 / 20:45",
			} {
				if !strings.Contains(plainText, expected) {
					t.Fatalf("stage notification missing %q:\n%s", expected, text)
				}
			}
			for _, forbidden := range []string{
				test.ruleType,
				"最近事件",
				"raw event note",
				"合约地址",
				"交易哈希",
				"0x1111111111111111111111111111111111111111",
				"$-",
				"：-",
				"\n",
			} {
				if strings.Contains(text, forbidden) {
					t.Fatalf("stage notification contains %q:\n%s", forbidden, text)
				}
			}
			if lines := marketNotificationBlockCount(text); lines > 10 {
				t.Fatalf("stage notification is too long (%d lines):\n%s", lines, text)
			}
		})
	}
}

func TestMarketStageFamiliesShowRelevantAggregateFields(t *testing.T) {
	cases := map[string][]string{
		plans.MarketPriceIncrease: {
			"阶段价格：$1 → $1.2",
			"最大变动信号：20%",
			"整体趋势：上涨 20%",
		},
		plans.MarketLiquidityDecrease: {
			"阶段流动性：$100,000 → $80,000",
			"最大下降信号：20%",
			"整体趋势：下降 20%",
		},
		plans.MarketVolumeSpike: {
			"最大放大倍数：4×",
			"平均放大倍数：3×",
			"最高成交量：$100,000",
		},
		plans.MarketLargeBuy: {
			"累计成交：$49,000",
			"最大单笔：$25,000",
			"买入 / 卖出：3 / 0",
			"活跃钱包：2",
		},
		plans.MarketHolderIncrease: {
			"累计变动：$49,000",
			"最大单次：$25,000",
			"活跃钱包：2",
		},
		plans.MarketFourMemeProgress: {
			"进度范围：60% → 90%",
			"最高进度：90%",
		},
	}
	for ruleType, expectedValues := range cases {
		var selected marketStageRuleCase
		for _, item := range marketStageRuleCases() {
			if item.ruleType == ruleType {
				selected = item
				break
			}
		}
		text := MarketNotificationText(marketStageTestDelivery(selected, "zh"))
		plainText := plainMarketNotificationText(text)
		for _, expected := range expectedValues {
			if !strings.Contains(plainText, expected) {
				t.Fatalf("%s notification missing %q:\n%s", ruleType, expected, text)
			}
		}
	}
}

func TestAllMarketStageRulesAreFullyLocalizedInEnglish(t *testing.T) {
	for _, test := range marketStageRuleCases() {
		t.Run(test.ruleType, func(t *testing.T) {
			text := MarketNotificationText(marketStageTestDelivery(test, "en"))
			plainText := plainMarketNotificationText(text)
			for _, expected := range []string{
				"stage summary",
				"Rule: ",
				"Window: 2026-07-31 12:00 → 2026-07-31 13:00",
				"Triggers: 3 (send at 3)",
				"Your condition: ",
				"First / latest: BNB Chain · 12:05 / 12:45",
			} {
				if !strings.Contains(plainText, expected) {
					t.Fatalf("English stage notification missing %q:\n%s", expected, text)
				}
			}
			if strings.Contains(text, test.ruleType) ||
				strings.Contains(text, "（") ||
				strings.Contains(text, "\n") {
				t.Fatalf("English stage notification contains internal or localized formatting:\n%s", text)
			}
			for _, character := range text {
				if unicode.Is(unicode.Han, character) {
					t.Fatalf("English stage notification contains Chinese %q:\n%s", character, text)
				}
			}
		})
	}
}

func TestMarketStageNotificationOmitsUnavailableStatistics(t *testing.T) {
	text := MarketNotificationText(store.MarketNotificationDelivery{
		Kind:                 "stage",
		NotificationLanguage: "zh",
		Project:              store.MarketProject{TokenSymbol: "TEST"},
		Rule: &store.MarketRule{
			RuleType: plans.MarketLargeBuy, ThresholdValue: "1000",
			ThresholdUnit: "usd", TriggerCountThreshold: 1,
		},
		TriggerCount: 1,
	})
	for _, forbidden := range []string{
		"累计成交：",
		"最大单笔：",
		"活跃钱包：",
		"首次 / 最近：",
		"$-",
		"：0",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("notification should omit %q:\n%s", forbidden, text)
		}
	}
}

func TestMarketStageNotificationKeepsMultichainContextCompact(t *testing.T) {
	test := marketStageRuleCases()[9]
	delivery := marketStageTestDelivery(test, "zh")
	delivery.StageEvents[1].Event.ChainKey = "base"
	text := MarketNotificationText(delivery)
	if !strings.Contains(plainMarketNotificationText(text), "首次 / 最近：BNB Chain / Base · 20:05 / 20:45") {
		t.Fatalf("multichain context is missing:\n%s", text)
	}
	if strings.Contains(text, "合约地址") || strings.Contains(text, "交易哈希") {
		t.Fatalf("multichain summary copied raw events:\n%s", text)
	}
}

func TestMarketStageNotificationEscapesProjectData(t *testing.T) {
	test := marketStageRuleCases()[9]
	delivery := marketStageTestDelivery(test, "en")
	delivery.Project.TokenSymbol = "<TEST>"
	text := MarketNotificationText(delivery)
	if strings.Contains(text, "<TEST>") || !strings.Contains(text, "&lt;TEST&gt;") {
		t.Fatalf("market stage notification is not escaped:\n%s", text)
	}
}

type marketStageRuleCase struct {
	ruleType   string
	unit       string
	threshold  string
	currents   [3]string
	eventType  string
	expectedZH string
}

func marketStageRuleCases() []marketStageRuleCase {
	return []marketStageRuleCase{
		{plans.MarketPriceAbove, "usd", "0.9", [3]string{"1", "1.1", "1.2"}, "", "价格高于阶段汇总"},
		{plans.MarketPriceBelow, "usd", "1.3", [3]string{"1.2", "1.1", "1"}, "", "价格低于阶段汇总"},
		{plans.MarketPriceIncrease, "percent", "10", [3]string{"10", "15", "20"}, "", "价格上涨阶段汇总"},
		{plans.MarketPriceDecrease, "percent", "10", [3]string{"10", "15", "20"}, "", "价格下跌阶段汇总"},
		{plans.MarketLiquidityBelow, "usd", "120000", [3]string{"100000", "90000", "80000"}, "", "流动性低于阶段汇总"},
		{plans.MarketLiquidityDecrease, "percent", "10", [3]string{"10", "15", "20"}, "", "流动性下降阶段汇总"},
		{plans.MarketVolumeAbove, "usd", "40000", [3]string{"50000", "75000", "100000"}, "", "成交量超过阶段汇总"},
		{plans.MarketVolumeSpike, "ratio", "2", [3]string{"2", "3", "4"}, "", "成交量异动阶段汇总"},
		{plans.MarketTradeImbalance, "percent", "60", [3]string{"80", "70", "90"}, "", "买卖失衡阶段汇总"},
		{plans.MarketLargeBuy, "usd", "5000", [3]string{"9000", "15000", "25000"}, "buy", "单笔大额买入阶段汇总"},
		{plans.MarketLargeSell, "usd", "5000", [3]string{"9000", "15000", "25000"}, "sell", "单笔大额卖出阶段汇总"},
		{plans.MarketConsecutiveLargeBuy, "usd", "5000", [3]string{"3", "3", "3"}, "buy", "连续大额买入阶段汇总"},
		{plans.MarketConsecutiveLargeSell, "usd", "5000", [3]string{"3", "3", "3"}, "sell", "连续大额卖出阶段汇总"},
		{plans.MarketLiquidityAdded, "usd", "5000", [3]string{"9000", "15000", "25000"}, "liquidity_added", "加池阶段汇总"},
		{plans.MarketLiquidityRemoved, "usd", "5000", [3]string{"9000", "15000", "25000"}, "liquidity_removed", "撤池阶段汇总"},
		{plans.MarketNewPool, "count", "1", [3]string{"1", "1", "1"}, "pool_initialized", "新交易池阶段汇总"},
		{plans.MarketHolderIncrease, "percent", "5", [3]string{"10", "15", "20"}, "holder_increase", "大户增持阶段汇总"},
		{plans.MarketHolderDecrease, "percent", "5", [3]string{"10", "15", "20"}, "holder_decrease", "大户减持阶段汇总"},
		{plans.MarketHolderRankEntered, "count", "10", [3]string{"10", "8", "5"}, "holder_rank_entered", "进入大户榜阶段汇总"},
		{plans.MarketHolderRankExited, "count", "10", [3]string{"10", "8", "5"}, "holder_rank_exited", "退出大户榜阶段汇总"},
		{plans.MarketFourMemeLargeTrade, "usd", "5000", [3]string{"9000", "15000", "25000"}, "buy", "Four.meme 大额成交阶段汇总"},
		{plans.MarketFourMemeProgress, "percent", "50", [3]string{"60", "75", "90"}, "buy", "Four.meme 进度阶段汇总"},
		{plans.MarketFourMemeMigration, "count", "1", [3]string{"1", "1", "1"}, "migrated", "Four.meme 迁移阶段汇总"},
	}
}

func marketStageTestDelivery(
	test marketStageRuleCase,
	language string,
) store.MarketNotificationDelivery {
	start := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	timezone := "Asia/Shanghai"
	if language == "en" {
		timezone = "UTC"
	}
	window := int32(60)
	walletOne := "0x1111111111111111111111111111111111111111"
	walletTwo := "0x2222222222222222222222222222222222222222"
	prices := [3]string{"1", "1.1", "1.2"}
	liquidities := [3]string{"100000", "90000", "80000"}
	volumes := [3]string{"50000", "75000", "100000"}
	usdValues := [3]string{"9000", "15000", "25000"}
	tokenValues := [3]string{"9000", "13636.36", "20833.33"}
	buys := [3]int64{8, 3, 9}
	sells := [3]int64{2, 7, 1}
	events := make([]store.MarketNotificationEvent, 0, 3)
	for index := 0; index < 3; index++ {
		eventType := test.eventType
		if eventType == "" {
			eventType = test.ruleType
		}
		wallet := &walletOne
		if index == 1 {
			wallet = &walletTwo
		}
		metadata, _ := json.Marshal(map[string]any{
			"progress_percent": test.currents[index],
			"protocol":         "four_meme",
		})
		poolAddress := "0x3333333333333333333333333333333333333333"
		current := test.currents[index]
		previous := "0"
		events = append(events, store.MarketNotificationEvent{
			Project: store.MarketProject{TokenSymbol: "TEST"},
			Event: store.MarketEvent{
				ChainKey: "bsc", EventType: eventType,
				TokenAmount: &tokenValues[index], USDValue: &usdValues[index],
				PriceUSD: &prices[index], WalletAddress: wallet,
				OccurredAt: start.Add(time.Duration(5+index*20) * time.Minute),
				Metadata:   metadata,
			},
			Pool: &store.MarketPool{
				ID: 99, PoolKey: "test-usdt", Token0Symbol: "TEST",
				Token1Symbol: "USDT", PoolAddress: &poolAddress,
			},
			Snapshot: &store.MarketSnapshot{
				LiquidityUSD: &liquidities[index],
				Volume1hUSD:  &volumes[index],
				Buys1h:       &buys[index],
				Sells1h:      &sells[index],
			},
			PreviousValue: &previous,
			CurrentValue:  &current,
			Note:          "raw event note",
		})
	}
	return store.MarketNotificationDelivery{
		Kind:                 "stage",
		NotificationLanguage: language,
		Timezone:             timezone,
		Project:              store.MarketProject{TokenSymbol: "TEST"},
		Rule: &store.MarketRule{
			RuleType: test.ruleType, ThresholdValue: test.threshold,
			ThresholdUnit: test.unit, WindowMinutes: &window,
			TriggerCountThreshold: 3,
		},
		TriggerCount: 3,
		StartsAt:     start,
		EndsAt:       start.Add(time.Hour),
		StageEvents:  events,
		RecentEvents: events,
	}
}
