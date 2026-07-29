package marketrules

import (
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestMarketRealtimeNotificationEscapesUserAndChainData(t *testing.T) {
	amount, usd, price := "1000", "2500", "2.5"
	wallet := "0x1111111111111111111111111111111111111111"
	tx := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	text := MarketNotificationText(store.MarketNotificationDelivery{
		Kind:                 "realtime",
		NotificationLanguage: "zh",
		Timezone:             "Asia/Shanghai",
		AddressLabel:         "项目方金库",
		Project: store.MarketProject{
			TokenName:   "<script>alert(1)</script>",
			TokenSymbol: "T&ST",
		},
		Rule: &store.MarketRule{},
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
	if strings.Contains(text, "<script>") || !strings.Contains(text, "&lt;script&gt;") {
		t.Fatalf("notification is not HTML escaped: %s", text)
	}
	for _, expected := range []string{
		"代币监控提醒",
		"链：Base",
		"合约地址：0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"DEX：uniswap v3",
		"交易池：TEST/USDC · 0x2222222222222222222222222222222222222222",
		"钱包：" + wallet,
		"地址标签：项目方金库",
		"交易哈希：" + tx,
		"金额：$2500",
		"发生时间：2026-07-26 20:00:00 (Asia/Shanghai)",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("notification missing %q: %s", expected, text)
		}
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
		StartsAt: start, EndsAt: start.Add(time.Hour), TriggerCount: 3,
		RecentNotes: []string{"<large buy>"},
	})
	if !strings.Contains(stage, "触发次数：3") ||
		!strings.Contains(stage, "&lt;large buy&gt;") {
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
	if !strings.Contains(combination, "Combination rule triggered") ||
		!strings.Contains(combination, "✅ Completed") ||
		!strings.Contains(combination, "2/2") {
		t.Fatalf("combination notification = %s", combination)
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
		Project:  store.MarketProject{TokenName: "ABC", TokenSymbol: "ABC"},
		StartsAt: start, EndsAt: start.Add(time.Hour), TriggerCount: 1,
		RecentEvents: []store.MarketNotificationEvent{item},
	})
	for _, expected := range []string{
		"链：Arbitrum",
		"合约地址：0x9999999999999999999999999999999999999999",
		"交易池：ABC/USDC · " + poolAddress,
		"金额：$9000",
		"钱包：" + wallet,
		"交易哈希：" + tx,
		"2026-07-26 12:00:00 (UTC)",
	} {
		if !strings.Contains(stage, expected) {
			t.Fatalf("stage notification missing %q:\n%s", expected, stage)
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
	for _, expected := range []string{
		"⏳ Incomplete",
		"Market rule / single large sell",
		"1/2",
		"Chain：Arbitrum",
		"Contract：0x9999999999999999999999999999999999999999",
		"Pool：ABC/USDC · " + poolAddress,
	} {
		if !strings.Contains(combination, expected) {
			t.Fatalf("combination notification missing %q:\n%s", expected, combination)
		}
	}
}

func pointer(value string) *string {
	return &value
}
