package marketrules

import (
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestMarketRealtimeNotificationEscapesUserAndChainData(t *testing.T) {
	amount, usd, price, current := "1000", "2500", "2.5", "2500"
	wallet := "0x1111111111111111111111111111111111111111"
	tx := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	text := MarketNotificationText(store.MarketNotificationDelivery{
		Kind:                 "realtime",
		NotificationLanguage: "zh",
		Timezone:             "Asia/Shanghai",
		AddressLabel:         "<项目方金库>",
		CurrentValue:         &current,
		Project: store.MarketProject{
			TokenName:   "Test",
			TokenSymbol: "T&ST<script>",
		},
		Rule: &store.MarketRule{
			RuleType:       "market_large_buy",
			ThresholdValue: "2000",
			ThresholdUnit:  "usd",
		},
		Event: &store.MarketEvent{
			ChainKey:        "base",
			TokenAddress:    "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			EventType:       "buy",
			TokenAmount:     &amount,
			USDValue:        &usd,
			PriceUSD:        &price,
			WalletAddress:   &wallet,
			TransactionHash: &tx,
			OccurredAt:      time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
		},
		Pool: &store.MarketPool{
			Protocol:        "uniswap",
			ProtocolVersion: "v3",
			Token0Symbol:    "TEST",
			Token1Symbol:    "USDC",
			PoolAddress:     pointer("0x2222222222222222222222222222222222222222"),
		},
	})
	plainText := plainMarketNotificationText(text)
	if strings.Contains(text, "<script>") || !strings.Contains(text, "&lt;script&gt;") {
		t.Fatalf("notification is not HTML escaped: %s", text)
	}
	for _, expected := range []string{
		"大额买入",
		"规则：单笔大额买入",
		"成交详情：$2,500 · 1K T&amp;ST&lt;script&gt;",
		"相关钱包：&lt;项目方金库&gt; (0x1111…1111)",
		"你的条件：≥ $2,000",
		"超出阈值：+$500",
		"发生于：Base · 2026-07-26 20:00",
	} {
		if !strings.Contains(plainText, expected) {
			t.Fatalf("notification missing %q: %s", expected, text)
		}
	}
	for _, unexpected := range []string{"交易哈希", "DEX：", "交易池：", "合约地址"} {
		if strings.Contains(text, unexpected) {
			t.Fatalf("realtime trade notification contains noise %q: %s", unexpected, text)
		}
	}
	if strings.Contains(text, "\n") || !strings.Contains(text, "<br/>") {
		t.Fatalf("notification does not use DeBox HTML line breaks: %q", text)
	}
}

func TestMarketStageAndCombinationNotificationsIncludeProgress(t *testing.T) {
	start := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	stage := MarketNotificationText(store.MarketNotificationDelivery{
		Kind: "stage", Timezone: "UTC",
		Project: store.MarketProject{
			TokenName:   "Test",
			TokenSymbol: "TEST",
		},
		Rule: &store.MarketRule{
			RuleType: plans.MarketLargeBuy, ThresholdValue: "1000",
			ThresholdUnit: "usd", TriggerCountThreshold: 3,
		},
		StartsAt: start, EndsAt: start.Add(time.Hour), TriggerCount: 3,
		RecentNotes: []string{"<large buy>"},
	})
	if !strings.Contains(plainMarketNotificationText(stage), "触发次数：3 次（达到 3 次时发送）") ||
		strings.Contains(stage, "&lt;large buy&gt;") ||
		strings.Contains(stage, "最近事件") {
		t.Fatalf("stage notification = %s", stage)
	}

	combination := MarketNotificationText(store.MarketNotificationDelivery{
		Kind: "combination", NotificationLanguage: "en",
		StartsAt: start, EndsAt: start.Add(time.Hour), TriggerCount: 4,
		Note: "wallet + market",
		CombinationMembers: []store.MarketCombinationProgress{{
			SourceType: "market", RuleType: "market_large_buy",
			TriggerCount: 2, RequiredTriggerCount: 2,
		}},
	})
	if !strings.Contains(combination, "Multiple market signals aligned") ||
		!strings.Contains(combination, "① ✅ Market · single large buy") ||
		!strings.Contains(combination, "2/2") {
		t.Fatalf("combination notification = %s", combination)
	}
}

func TestMarketRealtimeNotificationOmitsUnavailableFields(t *testing.T) {
	text := MarketNotificationText(store.MarketNotificationDelivery{
		Kind:                 "realtime",
		NotificationLanguage: "en",
		Project:              store.MarketProject{TokenSymbol: "TEST"},
		Rule: &store.MarketRule{
			RuleType:       "market_price_above",
			ThresholdValue: "1",
			ThresholdUnit:  "usd",
		},
		Event: &store.MarketEvent{
			ChainKey:   "bsc",
			EventType:  "buy",
			OccurredAt: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
		},
	})
	for _, unexpected := range []string{
		"Contract:",
		"Token amount:",
		"Value:",
		"Trade price:",
		"DEX:",
		"Pool:",
		"Wallet:",
		"Transaction:",
		"Current price:",
		"Above threshold:",
	} {
		if strings.Contains(text, unexpected) {
			t.Fatalf("notification should omit unavailable field %q: %s", unexpected, text)
		}
	}
	if !strings.Contains(plainMarketNotificationText(text), "Occurred: BNB Chain") {
		t.Fatalf("notification should use the formal chain name: %s", text)
	}
}

func TestStageAndCombinationMarketEventsKeepMultichainContext(t *testing.T) {
	start := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	wallet := "0x7777777777777777777777777777777777777777"
	tx := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	poolAddress := "0x8888888888888888888888888888888888888888"
	item := store.MarketNotificationEvent{
		Project: store.MarketProject{TokenName: "ABC", TokenSymbol: "ABC"},
		Event: store.MarketEvent{
			ChainKey:        "arbitrum",
			TokenAddress:    "0x9999999999999999999999999999999999999999",
			EventType:       "sell",
			USDValue:        pointer("9000"),
			WalletAddress:   &wallet,
			TransactionHash: &tx,
			OccurredAt:      start,
		},
		Pool: &store.MarketPool{
			Protocol: "uniswap", ProtocolVersion: "v3",
			Token0Symbol: "ABC", Token1Symbol: "USDC",
			PoolAddress: &poolAddress,
		},
	}
	stage := MarketNotificationText(store.MarketNotificationDelivery{
		Kind: "stage", Timezone: "UTC",
		Project: store.MarketProject{TokenName: "ABC", TokenSymbol: "ABC"},
		Rule: &store.MarketRule{
			RuleType: plans.MarketLargeSell, ThresholdValue: "5000",
			ThresholdUnit: "usd", TriggerCountThreshold: 1,
		},
		StartsAt: start, EndsAt: start.Add(time.Hour), TriggerCount: 1,
		StageEvents: []store.MarketNotificationEvent{item},
	})
	plainStage := plainMarketNotificationText(stage)
	for _, expected := range []string{
		"单笔大额卖出阶段汇总",
		"规则：单笔大额卖出",
		"累计成交：$9,000",
		"最大单笔：$9,000",
		"买入 / 卖出：0 / 1",
		"活跃钱包：1",
		"首次 / 最近：Arbitrum · 12:00",
	} {
		if !strings.Contains(plainStage, expected) {
			t.Fatalf("stage notification missing %q:\n%s", expected, stage)
		}
	}
	for _, forbidden := range []string{
		"合约地址",
		"交易池：",
		"<br/>钱包：",
		"交易哈希",
		"0x9999",
		"0xbbbb",
	} {
		if strings.Contains(stage, forbidden) {
			t.Fatalf("stage notification copied event field %q:\n%s", forbidden, stage)
		}
	}

	combination := MarketNotificationText(store.MarketNotificationDelivery{
		Kind: "combination", Timezone: "UTC", Note: "ABC combination",
		NotificationLanguage: "en",
		StartsAt:             start, EndsAt: start.Add(time.Hour), TriggerCount: 1,
		CombinationMembers: []store.MarketCombinationProgress{{
			SourceType: "market", RuleType: "market_large_sell",
			TriggerCount: 1, RequiredTriggerCount: 2,
			RecentEvents: []store.MarketNotificationEvent{item},
		}},
	})
	plainCombination := plainMarketNotificationText(combination)
	for _, expected := range []string{
		"① ⏳ Market · ABC single large sell",
		"1/2",
		"total traded $9,000",
		"Signal order: ① 12:00 ABC single large sell",
		"does not prove causation",
	} {
		if !strings.Contains(plainCombination, expected) {
			t.Fatalf("combination notification missing %q:\n%s", expected, combination)
		}
	}
	for _, forbidden := range []string{
		"Contract:",
		"Pool:",
		"Wallet:",
		"Transaction:",
		"0x9999",
		"0xbbbb",
		"0x8888",
		"Chain：",
		"\n",
	} {
		if strings.Contains(combination, forbidden) {
			t.Fatalf("English combination contains %q:\n%s", forbidden, combination)
		}
	}
}

func pointer(value string) *string {
	return &value
}
