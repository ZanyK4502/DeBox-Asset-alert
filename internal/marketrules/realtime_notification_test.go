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

func TestAllMarketRealtimeRulesUseDedicatedReadableTemplates(t *testing.T) {
	window := int32(60)
	current := "125"
	previous := "100"
	amount := "250000"
	tokenAmount := "125000"
	price := "2"
	wallet := "0x1111111111111111111111111111111111111111"
	buys, sells := int64(80), int64(20)
	poolAddress := "0x2222222222222222222222222222222222222222"
	metadata, _ := json.Marshal(map[string]any{
		"holder_address":       wallet,
		"old_balance":          "100000",
		"new_balance":          "125000",
		"old_rank":             12,
		"new_rank":             8,
		"progress_percent":     "92",
		"price_impact_percent": "3.5",
		"protocol":             "four_meme",
	})
	base := store.MarketNotificationDelivery{
		Kind:                 "realtime",
		NotificationLanguage: "zh",
		Timezone:             "Asia/Shanghai",
		AddressLabel:         "项目方金库",
		Project: store.MarketProject{
			TokenName:   "测试币",
			TokenSymbol: "TEST",
		},
		Event: &store.MarketEvent{
			ChainKey:      "bsc",
			EventType:     "buy",
			TokenAmount:   &tokenAmount,
			USDValue:      &amount,
			PriceUSD:      &price,
			WalletAddress: &wallet,
			OccurredAt:    time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
			Metadata:      metadata,
		},
		Pool: &store.MarketPool{
			Protocol: "pancakeswap", ProtocolVersion: "v3",
			Token0Symbol: "TEST", Token1Symbol: "USDT",
			PoolAddress: &poolAddress,
		},
		Snapshot: &store.MarketSnapshot{
			PriceUSD:     &price,
			LiquidityUSD: pointer("1000000"),
			Volume1hUSD:  &amount,
			Buys1h:       &buys,
			Sells1h:      &sells,
		},
		PreviousValue: &previous,
		CurrentValue:  &current,
	}

	tests := []struct {
		ruleType  string
		unit      string
		threshold string
		eventType string
		current   string
		contains  string
	}{
		{plans.MarketPriceAbove, "usd", "100", "", "125", "价格已突破上限"},
		{plans.MarketPriceBelow, "usd", "150", "", "125", "价格已跌破下限"},
		{plans.MarketPriceIncrease, "percent", "20", "", "25", "1小时内上涨 25%"},
		{plans.MarketPriceDecrease, "percent", "20", "", "25", "1小时内下跌 25%"},
		{plans.MarketLiquidityBelow, "usd", "150000", "", "125000", "流动性降至"},
		{plans.MarketLiquidityDecrease, "percent", "20", "", "25", "流动性在1小时内下降 25%"},
		{plans.MarketVolumeAbove, "usd", "200000", "", "250000", "1小时成交量达到"},
		{plans.MarketVolumeSpike, "ratio", "4", "", "5", "成交量放大至 5×"},
		{plans.MarketTradeImbalance, "percent", "70", "", "80", "成交明显偏向买方"},
		{plans.MarketLargeBuy, "usd", "200000", "buy", "250000", "大额买入"},
		{plans.MarketLargeSell, "usd", "200000", "sell", "250000", "大额卖出"},
		{plans.MarketConsecutiveLargeBuy, "usd", "200000", "buy", "3", "连续大额买入"},
		{plans.MarketConsecutiveLargeSell, "usd", "200000", "sell", "3", "连续大额卖出"},
		{plans.MarketLiquidityAdded, "usd", "200000", "liquidity_added", "250000", "新增流动性"},
		{plans.MarketLiquidityRemoved, "usd", "200000", "liquidity_removed", "250000", "发生撤池"},
		{plans.MarketNewPool, "count", "1", "pool_initialized", "1", "发现 TEST 新交易池"},
		{plans.MarketHolderIncrease, "percent", "20", "holder_increase", "25", "大户增持"},
		{plans.MarketHolderDecrease, "percent", "20", "holder_decrease", "25", "大户减持"},
		{plans.MarketHolderRankEntered, "count", "10", "holder_rank_entered", "8", "进入大户榜"},
		{plans.MarketHolderRankExited, "count", "10", "holder_rank_exited", "8", "退出大户榜"},
		{plans.MarketFourMemeLargeTrade, "usd", "200000", "buy", "250000", "Four.meme 内盘大额买入"},
		{plans.MarketFourMemeProgress, "percent", "90", "buy", "92", "Four.meme 进度达到 92%"},
		{plans.MarketFourMemeMigration, "count", "1", "migrated", "1", "已从 Four.meme 毕业迁移"},
	}

	for _, test := range tests {
		t.Run(test.ruleType, func(t *testing.T) {
			delivery := base
			event := *base.Event
			event.EventType = test.eventType
			delivery.Event = &event
			delivery.CurrentValue = pointer(test.current)
			delivery.Rule = &store.MarketRule{
				RuleType:              test.ruleType,
				ThresholdValue:        test.threshold,
				ThresholdUnit:         test.unit,
				WindowMinutes:         &window,
				TriggerCountThreshold: 3,
			}
			text := MarketNotificationText(delivery)
			plainText := plainMarketNotificationText(text)
			for _, expected := range []string{
				test.contains,
				"规则：",
				"条件：",
				"发生于：BNB Chain · 2026-07-26 20:00",
			} {
				if !strings.Contains(plainText, expected) {
					t.Fatalf("notification missing %q:\n%s", expected, text)
				}
			}
			for _, forbidden := range []string{
				test.ruleType,
				"$-",
				"：-",
				"\n",
				"交易哈希",
				"合约地址",
			} {
				if strings.Contains(text, forbidden) {
					t.Fatalf("notification contains %q:\n%s", forbidden, text)
				}
			}
			if lines := marketNotificationBlockCount(text); lines > 9 {
				t.Fatalf("notification is too long (%d lines):\n%s", lines, text)
			}
		})
	}
}

func TestMarketRealtimeFamilyFieldsStayRelevant(t *testing.T) {
	current := "25"
	metadata := json.RawMessage(`{
		"holder_address":"0x1111111111111111111111111111111111111111",
		"old_balance":"100",
		"new_balance":"125",
		"old_rank":12,
		"new_rank":8
	}`)
	holder := MarketNotificationText(store.MarketNotificationDelivery{
		Kind:    "realtime",
		Project: store.MarketProject{TokenSymbol: "TEST"},
		Rule: &store.MarketRule{
			RuleType:       plans.MarketHolderIncrease,
			ThresholdValue: "20", ThresholdUnit: "percent",
		},
		Event: &store.MarketEvent{
			ChainKey: "bsc", EventType: "holder_increase",
			OccurredAt: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
			Metadata:   metadata,
		},
		CurrentValue: &current,
		Pool: &store.MarketPool{
			Protocol: "pancakeswap", Token0Symbol: "TEST", Token1Symbol: "USDT",
		},
	})
	for _, forbidden := range []string{"DEX", "交易池", "交易哈希"} {
		if strings.Contains(holder, forbidden) {
			t.Fatalf("holder alert contains irrelevant field %q:\n%s", forbidden, holder)
		}
	}

	price := MarketNotificationText(store.MarketNotificationDelivery{
		Kind:    "realtime",
		Project: store.MarketProject{TokenSymbol: "TEST"},
		Rule: &store.MarketRule{
			RuleType:       plans.MarketPriceAbove,
			ThresholdValue: "1", ThresholdUnit: "usd",
		},
		Event: &store.MarketEvent{
			ChainKey: "bsc", EventType: plans.MarketPriceAbove,
			OccurredAt:    time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
			WalletAddress: pointer("0x1111111111111111111111111111111111111111"),
		},
		CurrentValue: pointer("1.25"),
	})
	for _, forbidden := range []string{"钱包", "交易哈希", "交易池", "DEX"} {
		if strings.Contains(price, forbidden) {
			t.Fatalf("price alert contains irrelevant field %q:\n%s", forbidden, price)
		}
	}
}

func TestMarketRealtimeEnglishUsesEnglishLabelsAndEscapesValues(t *testing.T) {
	text := MarketNotificationText(store.MarketNotificationDelivery{
		Kind:                 "realtime",
		NotificationLanguage: "en",
		Project:              store.MarketProject{TokenSymbol: "<TEST>"},
		Rule: &store.MarketRule{
			RuleType:       plans.MarketVolumeSpike,
			ThresholdValue: "4", ThresholdUnit: "ratio",
			WindowMinutes: int32Pointer(60),
		},
		Event: &store.MarketEvent{
			ChainKey:   "base",
			OccurredAt: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
		},
		CurrentValue:  pointer("5"),
		PreviousValue: pointer("50000"),
		Snapshot:      &store.MarketSnapshot{Volume1hUSD: pointer("250000")},
	})
	plainText := plainMarketNotificationText(text)
	for _, expected := range []string{
		"&lt;TEST&gt;",
		"volume spiked to 5×",
		"Rule: volume spike",
		"Your condition: ≥ 4×",
		"Exceeded by: +1×",
		"Occurred: Base · 2026-07-26 20:00",
	} {
		if !strings.Contains(plainText, expected) {
			t.Fatalf("English notification missing %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "规则：") || strings.Contains(text, "<TEST>") {
		t.Fatalf("English notification is not localized or escaped:\n%s", text)
	}
}

func TestAllMarketRealtimeRulesAreFullyLocalizedInEnglish(t *testing.T) {
	window := int32(60)
	wallet := "0x1111111111111111111111111111111111111111"
	buys, sells := int64(80), int64(20)
	metadata := json.RawMessage(`{
		"holder_address":"0x1111111111111111111111111111111111111111",
		"old_balance":"100",
		"new_balance":"125",
		"old_rank":12,
		"new_rank":8,
		"progress_percent":"92",
		"protocol":"four_meme"
	}`)
	ruleTypes := []string{
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
		t.Run(ruleType, func(t *testing.T) {
			unit, threshold, current, eventType := "usd", "20", "25", "buy"
			switch ruleType {
			case plans.MarketPriceIncrease, plans.MarketPriceDecrease,
				plans.MarketLiquidityDecrease, plans.MarketTradeImbalance,
				plans.MarketHolderIncrease, plans.MarketHolderDecrease,
				plans.MarketFourMemeProgress:
				unit = "percent"
			case plans.MarketVolumeSpike:
				unit, threshold, current = "ratio", "4", "5"
			case plans.MarketNewPool:
				unit, threshold, current, eventType = "count", "1", "1", "pool_initialized"
			case plans.MarketHolderRankEntered:
				unit, threshold, current, eventType = "count", "10", "8", "holder_rank_entered"
			case plans.MarketHolderRankExited:
				unit, threshold, current, eventType = "count", "10", "8", "holder_rank_exited"
			case plans.MarketFourMemeMigration:
				unit, threshold, current, eventType = "count", "1", "1", "migrated"
			case plans.MarketLargeSell, plans.MarketConsecutiveLargeSell:
				eventType = "sell"
			case plans.MarketLiquidityAdded:
				eventType = "liquidity_added"
			case plans.MarketLiquidityRemoved:
				eventType = "liquidity_removed"
			}
			delivery := store.MarketNotificationDelivery{
				Kind:                 "realtime",
				NotificationLanguage: "en",
				Project:              store.MarketProject{TokenSymbol: "TEST"},
				Rule: &store.MarketRule{
					RuleType: ruleType, ThresholdValue: threshold,
					ThresholdUnit: unit, WindowMinutes: &window,
					TriggerCountThreshold: 3,
				},
				Event: &store.MarketEvent{
					ChainKey: "bsc", EventType: eventType,
					TokenAmount: pointer("125"), USDValue: pointer("25"),
					PriceUSD: pointer("0.2"), WalletAddress: &wallet,
					OccurredAt: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
					Metadata:   metadata,
				},
				Snapshot: &store.MarketSnapshot{
					PriceUSD: pointer("0.2"), LiquidityUSD: pointer("25"),
					Volume1hUSD: pointer("25"), Buys1h: &buys, Sells1h: &sells,
				},
				PreviousValue: pointer("20"),
				CurrentValue:  pointer(current),
			}
			text := MarketNotificationText(delivery)
			plainText := plainMarketNotificationText(text)
			for _, expected := range []string{"Rule: ", "Occurred: BNB Chain"} {
				if !strings.Contains(plainText, expected) {
					t.Fatalf("English notification missing %q:\n%s", expected, text)
				}
			}
			if strings.Contains(text, ruleType) || strings.Contains(text, "$-") ||
				strings.Contains(text, "\n") {
				t.Fatalf("English notification contains an internal or empty value:\n%s", text)
			}
			for _, character := range text {
				if unicode.Is(unicode.Han, character) {
					t.Fatalf("English notification contains Chinese text %q:\n%s", character, text)
				}
			}
		})
	}
}

func TestMarketRealtimeDeliveryUsesExistingMarketHistoryEntry(t *testing.T) {
	const notificationID = "nd_2123456789abcdef0123456789abcdef01234567"
	notifier := &marketActionNotifier{}
	service := New(Dependencies{
		Notifications: notifier,
		PublicAppURL:  "https://alerts.example/root/#old",
	}, Settings{})
	delivery := store.MarketNotificationDelivery{
		Kind:                 "realtime",
		NotificationChatID:   "user-1",
		NotificationChatType: "private",
		NotificationLanguage: "zh",
	}
	messageID, err := service.sendMarketNotification(delivery, "market alert", notificationID)
	if err != nil {
		t.Fatalf("sendMarketNotification() error = %v", err)
	}
	if messageID != "action-message" ||
		notifier.actionText != "查看详情" ||
		notifier.actionURL != "https://alerts.example/root/?notification_id="+notificationID {
		t.Fatalf("realtime action = %#v", notifier)
	}

	notifier = &marketActionNotifier{}
	service.notifications = notifier
	delivery.NotificationLanguage = "en"
	if _, err := service.sendMarketNotification(delivery, "market alert", notificationID); err != nil {
		t.Fatalf("send English market notification: %v", err)
	}
	if notifier.actionText != "View details" {
		t.Fatalf("English realtime action = %#v", notifier)
	}

	notifier = &marketActionNotifier{}
	service.notifications = notifier
	delivery.Kind = "stage"
	if _, err := service.sendMarketNotification(delivery, "stage alert", notificationID); err != nil {
		t.Fatalf("send stage notification: %v", err)
	}
	if notifier.actionCalls != 1 || notifier.plainCalls != 0 ||
		notifier.actionText != "View all events" ||
		notifier.actionURL != "https://alerts.example/root/?notification_id="+notificationID {
		t.Fatalf("stage action = %#v", notifier)
	}

	notifier = &marketActionNotifier{}
	service.notifications = notifier
	delivery.Kind = "combination"
	delivery.NotificationLanguage = "zh"
	if _, err := service.sendMarketNotification(delivery, "combination alert", notificationID); err != nil {
		t.Fatalf("send combination notification: %v", err)
	}
	if notifier.actionText != "查看完整分析" ||
		notifier.actionURL != "https://alerts.example/root/?notification_id="+notificationID {
		t.Fatalf("combination action = %#v", notifier)
	}

	notifier = &marketActionNotifier{}
	service.notifications = notifier
	delivery.NotificationLanguage = "en"
	if _, err := service.sendMarketNotification(delivery, "combination alert", notificationID); err != nil {
		t.Fatalf("send English combination notification: %v", err)
	}
	if notifier.actionText != "View full analysis" {
		t.Fatalf("English combination action = %#v", notifier)
	}

	notifier = &marketActionNotifier{}
	service.notifications = notifier
	if _, err := service.sendMarketNotification(delivery, "safe fallback", "invalid"); err != nil {
		t.Fatalf("send fallback market notification: %v", err)
	}
	if notifier.plainCalls != 1 || notifier.actionCalls != 0 {
		t.Fatalf("invalid detail IDs must not create broken buttons: %#v", notifier)
	}
}

type marketActionNotifier struct {
	plainCalls  int
	actionCalls int
	actionText  string
	actionURL   string
}

func (notifier *marketActionNotifier) SendNotification(
	_, _, _ string,
) (string, error) {
	notifier.plainCalls++
	return "plain-message", nil
}

func (notifier *marketActionNotifier) SendNotificationWithAction(
	_, _, _, actionText, actionURL string,
) (string, error) {
	notifier.actionCalls++
	notifier.actionText = actionText
	notifier.actionURL = actionURL
	return "action-message", nil
}

func int32Pointer(value int32) *int32 {
	return &value
}
