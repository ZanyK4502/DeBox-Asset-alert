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
		Project: store.MarketProject{
			TokenName:   "<script>alert(1)</script>",
			TokenSymbol: "T&ST",
		},
		Rule: &store.MarketRule{},
		Event: &store.MarketEvent{
			EventType:       "buy",
			TokenAmount:     &amount,
			USDValue:        &usd,
			PriceUSD:        &price,
			WalletAddress:   &wallet,
			TransactionHash: &tx,
			OccurredAt:      time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
		},
	})
	if strings.Contains(text, "<script>") || !strings.Contains(text, "&lt;script&gt;") {
		t.Fatalf("notification is not HTML escaped: %s", text)
	}
	for _, expected := range []string{"项目币监控提醒", "买入", "2500", "2026-07-26 12:00:00 UTC"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("notification missing %q: %s", expected, text)
		}
	}
}

func TestMarketStageAndCombinationNotificationsIncludeProgress(t *testing.T) {
	start := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	stage := MarketNotificationText(store.MarketNotificationDelivery{
		Kind: "stage",
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
		!strings.Contains(combination, "2/2") {
		t.Fatalf("combination notification = %s", combination)
	}
}
