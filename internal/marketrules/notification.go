package marketrules

import (
	"encoding/json"
	"html"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationfmt"
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
		"market_large_buy":              "单笔大额买入",
		"market_large_sell":             "单笔大额卖出",
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
	if ruleType == "market_large_buy" {
		return "single large buy"
	}
	if ruleType == "market_large_sell" {
		return "single large sell"
	}
	return strings.ReplaceAll(strings.TrimPrefix(ruleType, "market_"), "_", " ")
}

func dexDisplay(pool *store.MarketPool) string {
	if pool == nil {
		return ""
	}
	protocol := defaultText(pool.Protocol, pool.ParserAdapter)
	if strings.TrimSpace(pool.ProtocolVersion) == "" {
		return protocol
	}
	return protocol + " " + pool.ProtocolVersion
}

func poolDisplay(pool *store.MarketPool) string {
	if pool == nil {
		return ""
	}
	pair := strings.Trim(strings.TrimSpace(pool.Token0Symbol)+"/"+
		strings.TrimSpace(pool.Token1Symbol), "/")
	address := pointerValue(pool.PoolAddress)
	if address == "" {
		address = strings.TrimSpace(pool.PoolKey)
	}
	address = notificationfmt.ShortIdentifier(address)
	if pair == "" {
		return address
	}
	if address == "" {
		return pair
	}
	return pair + " · " + address
}

func snapshotLiquidity(snapshot *store.MarketSnapshot) string {
	if snapshot == nil {
		return ""
	}
	return notificationfmt.Money(pointerValue(snapshot.LiquidityUSD))
}

func metadataDisplay(payload json.RawMessage, key, suffix string) string {
	value := metadataDecimal(payload, key)
	if value == nil {
		return ""
	}
	return notificationfmt.Percentage(*value) + suffix
}

func pointerValue(value *string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return ""
	}
	return strings.TrimSpace(*value)
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
