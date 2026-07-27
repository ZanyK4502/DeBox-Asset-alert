package marketrules

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
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
	title := "📊 代币监控提醒"
	if english {
		title = "📊 Token market alert"
	}
	item := store.MarketNotificationEvent{
		Project: delivery.Project,
		Event:   *event,
		Pool:    delivery.Pool,
	}
	lines := []string{"<b>" + title + "</b>"}
	lines = append(lines, marketEventDetailLines(item, delivery.Timezone, english)...)
	liquidityLabel, impactLabel := "池流动性", "价格影响"
	if english {
		liquidityLabel, impactLabel = "Pool liquidity", "Price impact"
	}
	lines = append(lines,
		fmt.Sprintf("%s：$%s", liquidityLabel, escape(snapshotLiquidity(delivery.Snapshot))),
		fmt.Sprintf("%s：%s", impactLabel,
			escape(metadataDisplay(event.Metadata, "price_impact_percent", "%"))),
	)
	return strings.Join(lines, "\n")
}

func marketStageText(delivery store.MarketNotificationDelivery) string {
	english := notificationLanguage(delivery.NotificationLanguage) == "en"
	title, window, count, recent := "📈 代币阶段提醒", "统计周期", "触发次数", "最近事件"
	if english {
		title, window, count, recent = "📈 Token stage alert", "Window", "Trigger count", "Recent events"
	}
	lines := []string{
		"<b>" + title + "</b>",
		projectDisplay(delivery.Project),
		fmt.Sprintf("%s：%s — %s", window,
			localTime(delivery.StartsAt, delivery.Timezone, "2006-01-02 15:04"),
			localTime(delivery.EndsAt, delivery.Timezone, "2006-01-02 15:04")),
		fmt.Sprintf("%s：%d", count, delivery.TriggerCount),
	}
	if len(delivery.RecentEvents) > 0 {
		lines = append(lines, recent+"：")
		for index, event := range delivery.RecentEvents {
			lines = append(lines, fmt.Sprintf("— %d —", index+1))
			lines = append(lines, marketEventDetailLines(event, delivery.Timezone, english)...)
		}
	} else if len(delivery.RecentNotes) > 0 {
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
			localTime(delivery.StartsAt, delivery.Timezone, "2006-01-02 15:04"),
			localTime(delivery.EndsAt, delivery.Timezone, "2006-01-02 15:04")),
		fmt.Sprintf("%s：%d", total, delivery.TriggerCount),
	}
	for _, member := range delivery.CombinationMembers {
		status := "✅ 已完成"
		if english {
			status = "✅ Completed"
		}
		if member.TriggerCount < member.RequiredTriggerCount {
			status = "⏳ 未完成"
			if english {
				status = "⏳ Incomplete"
			}
		}
		lines = append(lines, fmt.Sprintf(
			"• %s · %s / %s：%d/%d",
			status,
			escape(sourceDisplay(member.SourceType, english)),
			escape(ruleDisplay(member.RuleType, english)),
			member.TriggerCount,
			member.RequiredTriggerCount,
		))
		if len(member.RecentEvents) > 0 {
			lines = append(lines, marketEventDetailLines(
				member.RecentEvents[0],
				delivery.Timezone,
				english,
			)...)
		}
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

func marketEventDetailLines(
	item store.MarketNotificationEvent,
	timezone string,
	english bool,
) []string {
	event := item.Event
	labels := struct {
		project, chain, contract, event, amount, usd, price string
		dex, pool, wallet, tx, occurred                     string
	}{
		project: "代币", chain: "链", contract: "合约地址", event: "事件",
		amount: "代币数量", usd: "金额", price: "成交价格", dex: "DEX",
		pool: "交易池", wallet: "钱包", tx: "交易哈希", occurred: "发生时间",
	}
	if english {
		labels = struct {
			project, chain, contract, event, amount, usd, price string
			dex, pool, wallet, tx, occurred                     string
		}{
			project: "Token", chain: "Chain", contract: "Contract", event: "Event",
			amount: "Token amount", usd: "Value", price: "Trade price", dex: "DEX",
			pool: "Pool", wallet: "Wallet", tx: "Transaction", occurred: "Occurred at",
		}
	}
	return []string{
		fmt.Sprintf("%s：%s", labels.project, projectDisplay(item.Project)),
		fmt.Sprintf("%s：%s", labels.chain, escape(chainDisplay(event.ChainKey))),
		fmt.Sprintf("%s：%s", labels.contract, escape(event.TokenAddress)),
		fmt.Sprintf("%s：%s", labels.event, escape(eventDisplay(event.EventType, english))),
		fmt.Sprintf("%s：%s", labels.amount, escape(pointerText(event.TokenAmount))),
		fmt.Sprintf("%s：$%s", labels.usd, escape(pointerText(event.USDValue))),
		fmt.Sprintf("%s：$%s", labels.price, escape(pointerText(event.PriceUSD))),
		fmt.Sprintf("%s：%s", labels.dex, escape(dexDisplay(item.Pool))),
		fmt.Sprintf("%s：%s", labels.pool, escape(poolDisplay(item.Pool))),
		fmt.Sprintf("%s：%s", labels.wallet, escape(pointerText(event.WalletAddress))),
		fmt.Sprintf("%s：%s", labels.tx, escape(pointerText(event.TransactionHash))),
		fmt.Sprintf("%s：%s", labels.occurred,
			escape(localTime(event.OccurredAt, timezone, "2006-01-02 15:04:05"))),
	}
}

func projectDisplay(project store.MarketProject) string {
	name := escape(defaultText(project.TokenName, project.TokenSymbol))
	symbol := strings.TrimSpace(project.TokenSymbol)
	if symbol == "" || strings.EqualFold(project.TokenName, symbol) {
		return "<b>" + name + "</b>"
	}
	return fmt.Sprintf("<b>%s</b> (%s)", name, escape(symbol))
}

func chainDisplay(chainKey string) string {
	if strings.TrimSpace(chainKey) == "" {
		return "-"
	}
	profile, err := chain.ChainProfile(chainKey, "")
	if err == nil {
		return profile.Name
	}
	return defaultText(chainKey, "-")
}

func sourceDisplay(sourceType string, english bool) string {
	if english {
		if sourceType == "watch" {
			return "Wallet rule"
		}
		if sourceType == "market" {
			return "Market rule"
		}
		return sourceType
	}
	if sourceType == "watch" {
		return "钱包规则"
	}
	if sourceType == "market" {
		return "市场规则"
	}
	return sourceType
}

func ruleDisplay(ruleType string, english bool) string {
	zh := map[string]string{
		"market_price_above":            "价格高于",
		"market_price_below":            "价格低于",
		"market_price_increase":         "价格上涨",
		"market_price_decrease":         "价格下跌",
		"market_liquidity_below":        "流动性低于",
		"market_liquidity_decrease":     "流动性下降",
		"market_volume_above":           "成交量超过",
		"market_volume_spike":           "成交量异动",
		"market_trade_imbalance":        "买卖失衡",
		"market_large_buy":              "大额买入",
		"market_large_sell":             "大额卖出",
		"market_consecutive_large_buy":  "连续大额买入",
		"market_consecutive_large_sell": "连续大额卖出",
		"market_liquidity_added":        "加池",
		"market_liquidity_removed":      "撤池",
		"market_new_pool":               "新交易池",
		"market_holder_increase":        "大户增持",
		"market_holder_decrease":        "大户减持",
		"market_holder_rank_entered":    "进入大户榜",
		"market_holder_rank_exited":     "退出大户榜",
		"market_four_meme_large_trade":  "Four.meme 大额成交",
		"market_four_meme_progress":     "Four.meme 进度",
		"market_four_meme_migration":    "Four.meme 迁移",
	}
	if !english {
		if value := zh[ruleType]; value != "" {
			return value
		}
		return ruleType
	}
	return strings.ReplaceAll(strings.TrimPrefix(ruleType, "market_"), "_", " ")
}

func dexDisplay(pool *store.MarketPool) string {
	if pool == nil {
		return "-"
	}
	protocol := defaultText(pool.Protocol, pool.ParserAdapter)
	if strings.TrimSpace(pool.ProtocolVersion) == "" {
		return protocol
	}
	return protocol + " " + pool.ProtocolVersion
}

func poolDisplay(pool *store.MarketPool) string {
	if pool == nil {
		return "-"
	}
	pair := strings.Trim(strings.TrimSpace(pool.Token0Symbol)+"/"+
		strings.TrimSpace(pool.Token1Symbol), "/")
	address := pointerText(pool.PoolAddress)
	if address == "-" {
		address = defaultText(pool.PoolKey, "-")
	}
	if pair == "" {
		return address
	}
	return pair + " · " + address
}

func localTime(value time.Time, timezone, layout string) string {
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		timezone = "Asia/Shanghai"
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		timezone = "Asia/Shanghai"
		location, _ = time.LoadLocation(timezone)
	}
	return value.In(location).Format(layout) + " (" + timezone + ")"
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
