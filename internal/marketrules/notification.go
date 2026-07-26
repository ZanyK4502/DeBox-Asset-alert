package marketrules

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func MarketNotificationText(delivery store.MarketNotificationDelivery) string {
	switch delivery.Kind {
	case "combination":
		return marketCombinationText(delivery)
	case "stage":
		return marketStageText(delivery)
	default:
		return marketRealtimeText(delivery)
	}
}

func marketRealtimeText(delivery store.MarketNotificationDelivery) string {
	event := delivery.Event
	rule := delivery.Rule
	if event == nil || rule == nil {
		return "⚠️ Market alert"
	}
	english := notificationLanguage(delivery.NotificationLanguage) == "en"
	title := "📊 项目币监控提醒"
	projectLabel := "项目币"
	eventLabel := "事件"
	amountLabel := "代币数量"
	usdLabel := "池内成交金额"
	priceLabel := "成交价格"
	walletLabel := "钱包"
	poolLabel := "交易池 / 协议"
	liquidityLabel := "池流动性"
	impactLabel := "价格影响"
	txLabel := "交易哈希"
	timeLabel := "发生时间"
	if english {
		title = "📊 Token market alert"
		projectLabel = "Token"
		eventLabel = "Event"
		amountLabel = "Token amount"
		usdLabel = "Pool trade value"
		priceLabel = "Trade price"
		walletLabel = "Wallet"
		poolLabel = "Pool / protocol"
		liquidityLabel = "Pool liquidity"
		impactLabel = "Price impact"
		txLabel = "Transaction"
		timeLabel = "Occurred at"
	}
	lines := []string{
		"<b>" + title + "</b>",
		fmt.Sprintf("%s：<b>%s</b> (%s)", projectLabel,
			escape(defaultText(delivery.Project.TokenName, delivery.Project.TokenSymbol)),
			escape(delivery.Project.TokenSymbol)),
		fmt.Sprintf("%s：%s", eventLabel, escape(eventDisplay(event.EventType, english))),
		fmt.Sprintf("%s：%s", amountLabel, escape(pointerText(event.TokenAmount))),
		fmt.Sprintf("%s：$%s", usdLabel, escape(pointerText(event.USDValue))),
		fmt.Sprintf("%s：$%s", priceLabel, escape(pointerText(event.PriceUSD))),
		fmt.Sprintf("%s：%s", walletLabel, escape(shortPointer(event.WalletAddress))),
		fmt.Sprintf("%s：%s", poolLabel, escape(poolDisplay(delivery))),
		fmt.Sprintf("%s：$%s", liquidityLabel, escape(snapshotLiquidity(delivery.Snapshot))),
		fmt.Sprintf("%s：%s", impactLabel,
			escape(metadataDisplay(event.Metadata, "price_impact_percent", "%"))),
		fmt.Sprintf("%s：%s", txLabel, escape(shortPointer(event.TransactionHash))),
		fmt.Sprintf("%s：%s", timeLabel,
			event.OccurredAt.UTC().Format("2006-01-02 15:04:05 UTC")),
	}
	return strings.Join(lines, "\n")
}

func marketStageText(delivery store.MarketNotificationDelivery) string {
	english := notificationLanguage(delivery.NotificationLanguage) == "en"
	title, window, count, recent := "📈 项目币阶段提醒", "统计周期", "触发次数", "最近事件"
	if english {
		title, window, count, recent = "📈 Token stage alert", "Window", "Trigger count", "Recent events"
	}
	lines := []string{
		"<b>" + title + "</b>",
		fmt.Sprintf("%s (%s)",
			escape(defaultText(delivery.Project.TokenName, delivery.Project.TokenSymbol)),
			escape(delivery.Project.TokenSymbol)),
		fmt.Sprintf("%s：%s — %s", window,
			delivery.StartsAt.UTC().Format("2006-01-02 15:04 UTC"),
			delivery.EndsAt.UTC().Format("2006-01-02 15:04 UTC")),
		fmt.Sprintf("%s：%d", count, delivery.TriggerCount),
	}
	if len(delivery.RecentNotes) > 0 {
		lines = append(lines, recent+"：")
		for _, note := range delivery.RecentNotes {
			lines = append(lines, "• "+escape(note))
		}
	}
	return strings.Join(lines, "\n")
}

func marketCombinationText(delivery store.MarketNotificationDelivery) string {
	english := notificationLanguage(delivery.NotificationLanguage) == "en"
	title, window, total := "🔗 组合规则已触发", "统计周期", "总触发次数"
	if english {
		title, window, total = "🔗 Combination rule triggered", "Window", "Total triggers"
	}
	lines := []string{
		"<b>" + title + "</b>",
		escape(delivery.Note),
		fmt.Sprintf("%s：%s — %s", window,
			delivery.StartsAt.UTC().Format("2006-01-02 15:04 UTC"),
			delivery.EndsAt.UTC().Format("2006-01-02 15:04 UTC")),
		fmt.Sprintf("%s：%d", total, delivery.TriggerCount),
	}
	for _, member := range delivery.CombinationMembers {
		lines = append(lines, fmt.Sprintf(
			"• %s / %s：%d/%d",
			escape(member.SourceType),
			escape(member.RuleType),
			member.TriggerCount,
			member.RequiredTriggerCount,
		))
		for _, note := range member.RecentNotes {
			lines = append(lines, "  - "+escape(note))
		}
	}
	return strings.Join(lines, "\n")
}

func eventDisplay(eventType string, english bool) string {
	if english {
		return strings.ReplaceAll(eventType, "_", " ")
	}
	values := map[string]string{
		"buy":                 "买入",
		"sell":                "卖出",
		"liquidity_added":     "加池",
		"liquidity_removed":   "撤池",
		"pool_initialized":    "新交易池",
		"holder_increase":     "大户增持",
		"holder_decrease":     "大户减持",
		"holder_rank_entered": "进入前 N 名",
		"holder_rank_exited":  "退出前 N 名",
		"migrated":            "Four.meme 毕业迁移",
	}
	if value := values[eventType]; value != "" {
		return value
	}
	return eventType
}

func poolDisplay(delivery store.MarketNotificationDelivery) string {
	if delivery.Pool == nil {
		return "-"
	}
	return delivery.Pool.PoolKey + " / " +
		defaultText(delivery.Pool.Protocol, delivery.Pool.ParserAdapter)
}

func snapshotLiquidity(snapshot *store.MarketSnapshot) string {
	if snapshot == nil {
		return "-"
	}
	return pointerText(snapshot.LiquidityUSD)
}

func metadataDisplay(payload json.RawMessage, key, suffix string) string {
	value := metadataDecimal(payload, key)
	if value == nil {
		return "-"
	}
	return *value + suffix
}

func shortPointer(value *string) string {
	if value == nil {
		return "-"
	}
	if len(*value) <= 14 {
		return *value
	}
	return (*value)[:8] + "…" + (*value)[len(*value)-6:]
}

func pointerText(value *string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return "-"
	}
	return *value
}

func escape(value string) string {
	return html.EscapeString(value)
}

func notificationLanguage(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "en") {
		return "en"
	}
	return "zh"
}

func defaultText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
