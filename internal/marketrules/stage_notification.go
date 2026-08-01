package marketrules

import (
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationfmt"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type marketStageStats struct {
	firstAt, lastAt                    time.Time
	firstPrice, lastPrice              *string
	firstLiquidity, lastLiquidity      *string
	currentMin, currentMax, currentSum *big.Rat
	currentCount                       int64
	totalUSD, maxUSD                   *big.Rat
	totalToken, maxToken               *big.Rat
	totalVolume, maxVolume             *big.Rat
	hasUSD, hasToken, hasVolume        bool
	buyCount, sellCount                int64
	buyDominant, sellDominant          int64
	activeWallets                      map[string]struct{}
	pools                              map[string]struct{}
	chains                             map[string]struct{}
}

func marketStageText(delivery store.MarketNotificationDelivery) string {
	if delivery.Rule == nil {
		return "<b>⚠️ Market stage alert</b>"
	}
	rule := delivery.Rule
	english := notificationLanguage(delivery.NotificationLanguage) == "en"
	stats := calculateMarketStageStats(*rule, delivery.StageEvents)
	title := marketStageTitle(delivery, english)
	lines := []string{
		"<b>" + title + "</b>",
		notificationfmt.KeyValue(
			marketLabel("规则", "Rule", english),
			escape(ruleDisplay(rule.RuleType, english)),
			english,
		),
		notificationfmt.KeyValue(
			marketLabel("统计周期", "Window", english),
			marketStageWindow(delivery.StartsAt, delivery.EndsAt, delivery.Timezone),
			english,
		),
		notificationfmt.KeyValue(
			marketLabel("触发次数", "Triggers", english),
			marketStageTriggerCount(delivery, english),
			english,
		),
	}
	if condition := marketStageCondition(*rule, delivery.Project.TokenSymbol, english); condition != "" {
		lines = append(lines, notificationfmt.KeyValue(
			marketLabel("你的条件", "Your condition", english),
			condition,
			english,
		))
	}

	switch rule.RuleType {
	case plans.MarketPriceAbove,
		plans.MarketPriceBelow,
		plans.MarketPriceIncrease,
		plans.MarketPriceDecrease:
		lines = append(lines, marketPriceStageLines(*rule, stats, english)...)
	case plans.MarketLiquidityBelow,
		plans.MarketLiquidityDecrease,
		plans.MarketLiquidityAdded,
		plans.MarketLiquidityRemoved,
		plans.MarketNewPool:
		lines = append(lines, marketLiquidityStageLines(delivery, stats, english)...)
	case plans.MarketVolumeAbove,
		plans.MarketVolumeSpike,
		plans.MarketTradeImbalance:
		lines = append(lines, marketVolumeStageLines(*rule, stats, english)...)
	case plans.MarketLargeBuy,
		plans.MarketLargeSell,
		plans.MarketConsecutiveLargeBuy,
		plans.MarketConsecutiveLargeSell:
		lines = append(lines, marketTradeStageLines(delivery, stats, english)...)
	case plans.MarketHolderIncrease,
		plans.MarketHolderDecrease,
		plans.MarketHolderRankEntered,
		plans.MarketHolderRankExited:
		lines = append(lines, marketHolderStageLines(delivery, stats, english)...)
	case plans.MarketFourMemeLargeTrade,
		plans.MarketFourMemeProgress,
		plans.MarketFourMemeMigration:
		lines = append(lines, marketFourMemeStageLines(delivery, stats, english)...)
	}
	if context := marketStageContext(stats, delivery.Timezone, english); context != "" {
		lines = append(lines, notificationfmt.KeyValue(
			marketLabel("首次 / 最近", "First / latest", english),
			context,
			english,
		))
	}
	return notificationfmt.JoinBlocks(lines...)
}

func calculateMarketStageStats(
	rule store.MarketRule,
	events []store.MarketNotificationEvent,
) marketStageStats {
	stats := marketStageStats{
		currentSum:    new(big.Rat),
		totalUSD:      new(big.Rat),
		maxUSD:        new(big.Rat),
		totalToken:    new(big.Rat),
		maxToken:      new(big.Rat),
		totalVolume:   new(big.Rat),
		maxVolume:     new(big.Rat),
		activeWallets: make(map[string]struct{}),
		pools:         make(map[string]struct{}),
		chains:        make(map[string]struct{}),
	}
	for _, item := range events {
		event := item.Event
		if stats.firstAt.IsZero() || event.OccurredAt.Before(stats.firstAt) {
			stats.firstAt = event.OccurredAt
		}
		if stats.lastAt.IsZero() || event.OccurredAt.After(stats.lastAt) {
			stats.lastAt = event.OccurredAt
		}
		if chainKey := strings.TrimSpace(event.ChainKey); chainKey != "" {
			stats.chains[chainKey] = struct{}{}
		}
		wallet := strings.ToLower(strings.TrimSpace(pointerValue(event.WalletAddress)))
		if isHolderRule(rule.RuleType) {
			wallet = holderEventAddress(rule.RuleType, event)
		}
		if wallet != "" {
			stats.activeWallets[wallet] = struct{}{}
		}
		if item.Pool != nil {
			key := fmt.Sprintf("%d:%s", item.Pool.ID, item.Pool.PoolKey)
			stats.pools[key] = struct{}{}
		}
		if value, ok := pointerRat(event.USDValue); ok {
			stats.hasUSD = true
			absolute := new(big.Rat).Abs(value)
			stats.totalUSD.Add(stats.totalUSD, absolute)
			if absolute.Cmp(stats.maxUSD) > 0 {
				stats.maxUSD.Set(absolute)
			}
		}
		if value, ok := pointerRat(event.TokenAmount); ok {
			stats.hasToken = true
			absolute := new(big.Rat).Abs(value)
			stats.totalToken.Add(stats.totalToken, absolute)
			if absolute.Cmp(stats.maxToken) > 0 {
				stats.maxToken.Set(absolute)
			}
		}
		switch event.EventType {
		case "buy":
			stats.buyCount++
		case "sell":
			stats.sellCount++
		}
		if value, ok := pointerRat(item.CurrentValue); ok {
			stats.currentSum.Add(stats.currentSum, value)
			stats.currentCount++
			if stats.currentMin == nil || value.Cmp(stats.currentMin) < 0 {
				stats.currentMin = new(big.Rat).Set(value)
			}
			if stats.currentMax == nil || value.Cmp(stats.currentMax) > 0 {
				stats.currentMax = new(big.Rat).Set(value)
			}
		}
		if pointerValue(event.PriceUSD) != "" {
			if stats.firstPrice == nil {
				stats.firstPrice = event.PriceUSD
			}
			stats.lastPrice = event.PriceUSD
		}
		if item.Snapshot == nil {
			continue
		}
		if isLiquiditySnapshotRule(rule.RuleType) &&
			pointerValue(item.Snapshot.LiquidityUSD) != "" {
			if stats.firstLiquidity == nil {
				stats.firstLiquidity = item.Snapshot.LiquidityUSD
			}
			stats.lastLiquidity = item.Snapshot.LiquidityUSD
		}
		if isVolumeSnapshotRule(rule.RuleType) {
			if volume, ok := pointerRat(snapshotVolume(*item.Snapshot, rule.WindowMinutes)); ok {
				stats.hasVolume = true
				stats.totalVolume.Add(stats.totalVolume, volume)
				if volume.Cmp(stats.maxVolume) > 0 {
					stats.maxVolume.Set(volume)
				}
			}
		}
		if rule.RuleType == plans.MarketTradeImbalance {
			buys, sells, ok := snapshotTrades(*item.Snapshot, rule.WindowMinutes)
			if ok && sells > buys {
				stats.sellDominant++
			} else if ok {
				stats.buyDominant++
			}
		}
	}
	return stats
}

func marketPriceStageLines(
	rule store.MarketRule,
	stats marketStageStats,
	english bool,
) []string {
	lines := make([]string, 0, 3)
	appendStageLine(&lines, marketLabel("阶段价格", "Price over window", english),
		marketStagePriceTransition(stats.firstPrice, stats.lastPrice), english)
	if rule.RuleType == plans.MarketPriceIncrease ||
		rule.RuleType == plans.MarketPriceDecrease {
		appendStageLine(&lines, marketLabel("最大变动信号", "Largest move signal", english),
			marketStageRatValue(stats.currentMax, "percent", ""), english)
	} else {
		appendStageLine(&lines, marketLabel("触发价格范围", "Triggered price range", english),
			marketStageRatRange(stats.currentMin, stats.currentMax, "price", "", english), english)
	}
	appendStageLine(&lines, marketLabel("整体趋势", "Overall trend", english),
		marketStageTrend(stats.firstPrice, stats.lastPrice, english), english)
	return lines
}

func marketLiquidityStageLines(
	delivery store.MarketNotificationDelivery,
	stats marketStageStats,
	english bool,
) []string {
	rule := delivery.Rule
	lines := make([]string, 0, 3)
	switch rule.RuleType {
	case plans.MarketLiquidityBelow:
		appendStageLine(&lines, marketLabel("阶段流动性", "Liquidity over window", english),
			marketStageMoneyTransition(stats.firstLiquidity, stats.lastLiquidity), english)
		appendStageLine(&lines, marketLabel("最低触发值", "Lowest trigger value", english),
			marketStageRatValue(stats.currentMin, "usd", ""), english)
		appendStageLine(&lines, marketLabel("整体趋势", "Overall trend", english),
			marketStageTrend(stats.firstLiquidity, stats.lastLiquidity, english), english)
	case plans.MarketLiquidityDecrease:
		appendStageLine(&lines, marketLabel("阶段流动性", "Liquidity over window", english),
			marketStageMoneyTransition(stats.firstLiquidity, stats.lastLiquidity), english)
		appendStageLine(&lines, marketLabel("最大下降信号", "Largest drop signal", english),
			marketStageRatValue(stats.currentMax, "percent", ""), english)
		appendStageLine(&lines, marketLabel("整体趋势", "Overall trend", english),
			marketStageTrend(stats.firstLiquidity, stats.lastLiquidity, english), english)
	case plans.MarketLiquidityAdded:
		appendStageLine(&lines, marketLabel("累计加池", "Total liquidity added", english),
			marketStageTotalAmount(stats, delivery.Project.TokenSymbol), english)
		appendStageLine(&lines, marketLabel("最大单次加池", "Largest addition", english),
			marketStageMaxAmount(stats, delivery.Project.TokenSymbol), english)
		appendStageLine(&lines, marketLabel("涉及交易池", "Pools involved", english),
			marketStageCount(len(stats.pools), marketLabel("个", " pool(s)", english), english), english)
	case plans.MarketLiquidityRemoved:
		appendStageLine(&lines, marketLabel("累计撤池", "Total liquidity removed", english),
			marketStageTotalAmount(stats, delivery.Project.TokenSymbol), english)
		appendStageLine(&lines, marketLabel("最大单次撤池", "Largest removal", english),
			marketStageMaxAmount(stats, delivery.Project.TokenSymbol), english)
		appendStageLine(&lines, marketLabel("涉及交易池", "Pools involved", english),
			marketStageCount(len(stats.pools), marketLabel("个", " pool(s)", english), english), english)
	case plans.MarketNewPool:
		appendStageLine(&lines, marketLabel("发现新池", "New pools detected", english),
			marketStageCount(len(delivery.StageEvents),
				marketLabel(" 个", " pool(s)", english), english), english)
		appendStageLine(&lines, marketLabel("涉及交易池", "Distinct pools", english),
			marketStageCount(len(stats.pools),
				marketLabel(" 个", " pool(s)", english), english), english)
	}
	return lines
}

func marketVolumeStageLines(
	rule store.MarketRule,
	stats marketStageStats,
	english bool,
) []string {
	lines := make([]string, 0, 3)
	switch rule.RuleType {
	case plans.MarketVolumeAbove:
		appendStageLine(&lines, marketLabel(
			"触发时成交量合计",
			"Sum of triggered volume values",
			english,
		), marketStageRatValue(stats.currentSum, "usd", ""), english)
		appendStageLine(&lines, marketLabel("最高成交量", "Highest volume", english),
			marketStageRatValue(stats.currentMax, "usd", ""), english)
		appendStageLine(&lines, marketLabel("平均触发值", "Average trigger value", english),
			marketStageAverage(stats.currentSum, stats.currentCount, "usd", ""), english)
	case plans.MarketVolumeSpike:
		appendStageLine(&lines, marketLabel("最大放大倍数", "Largest volume multiple", english),
			marketStageRatValue(stats.currentMax, "ratio", ""), english)
		appendStageLine(&lines, marketLabel("平均放大倍数", "Average volume multiple", english),
			marketStageAverage(stats.currentSum, stats.currentCount, "ratio", ""), english)
		appendStageLine(&lines, marketLabel("最高成交量", "Highest volume", english),
			marketStageAvailableRat(stats.maxVolume, stats.hasVolume, "usd", ""), english)
	case plans.MarketTradeImbalance:
		direction := fmt.Sprintf(
			"%d / %d",
			stats.buyDominant,
			stats.sellDominant,
		)
		appendStageLine(&lines, marketLabel(
			"买方主导 / 卖方主导",
			"Buyer-led / seller-led signals",
			english,
		), direction, english)
		appendStageLine(&lines, marketLabel("最高单边占比", "Highest one-sided share", english),
			marketStageRatValue(stats.currentMax, "percent", ""), english)
	}
	return lines
}

func marketTradeStageLines(
	delivery store.MarketNotificationDelivery,
	stats marketStageStats,
	english bool,
) []string {
	lines := make([]string, 0, 3)
	appendStageLine(&lines, marketLabel("累计成交", "Total traded", english),
		marketStageTotalAmount(stats, delivery.Project.TokenSymbol), english)
	appendStageLine(&lines, marketLabel("最大单笔", "Largest trade", english),
		marketStageMaxAmount(stats, delivery.Project.TokenSymbol), english)
	if stats.buyCount+stats.sellCount > 0 {
		appendStageLine(&lines, marketLabel("买入 / 卖出", "Buys / sells", english),
			fmt.Sprintf("%d / %d", stats.buyCount, stats.sellCount), english)
	}
	if len(stats.activeWallets) > 0 {
		appendStageLine(&lines, marketLabel("活跃钱包", "Active wallets", english),
			fmt.Sprintf("%d", len(stats.activeWallets)), english)
	}
	return lines
}

func marketHolderStageLines(
	delivery store.MarketNotificationDelivery,
	stats marketStageStats,
	english bool,
) []string {
	lines := make([]string, 0, 3)
	switch delivery.Rule.RuleType {
	case plans.MarketHolderIncrease, plans.MarketHolderDecrease:
		appendStageLine(&lines, marketLabel("累计变动", "Total holding change", english),
			marketStageTotalAmount(stats, delivery.Project.TokenSymbol), english)
		appendStageLine(&lines, marketLabel("最大单次", "Largest single change", english),
			marketStageMaxAmount(stats, delivery.Project.TokenSymbol), english)
	case plans.MarketHolderRankEntered:
		appendStageLine(&lines, marketLabel("进入榜单", "Entered ranking", english),
			marketStageCount(len(delivery.StageEvents),
				marketLabel(" 次", " time(s)", english), english), english)
	case plans.MarketHolderRankExited:
		appendStageLine(&lines, marketLabel("退出榜单", "Exited ranking", english),
			marketStageCount(len(delivery.StageEvents),
				marketLabel(" 次", " time(s)", english), english), english)
	}
	appendStageLine(&lines, marketLabel("活跃钱包", "Active wallets", english),
		marketStageCount(len(stats.activeWallets),
			marketLabel(" 个", "", english), english), english)
	return lines
}

func marketFourMemeStageLines(
	delivery store.MarketNotificationDelivery,
	stats marketStageStats,
	english bool,
) []string {
	lines := make([]string, 0, 3)
	switch delivery.Rule.RuleType {
	case plans.MarketFourMemeLargeTrade:
		lines = append(lines, marketTradeStageLines(delivery, stats, english)...)
	case plans.MarketFourMemeProgress:
		appendStageLine(&lines, marketLabel("进度范围", "Progress range", english),
			marketStageRatRange(stats.currentMin, stats.currentMax, "percent", "", english), english)
		appendStageLine(&lines, marketLabel("最高进度", "Highest progress", english),
			marketStageRatValue(stats.currentMax, "percent", ""), english)
	case plans.MarketFourMemeMigration:
		appendStageLine(&lines, marketLabel("检测到迁移", "Migrations detected", english),
			marketStageCount(len(delivery.StageEvents),
				marketLabel(" 次", " time(s)", english), english), english)
		appendStageLine(&lines, marketLabel("涉及交易池", "Pools involved", english),
			marketStageCount(len(stats.pools),
				marketLabel(" 个", " pool(s)", english), english), english)
	}
	return lines
}

func marketStageTitle(delivery store.MarketNotificationDelivery, english bool) string {
	token := escape(marketRealtimeToken(delivery.Project, english))
	ruleName := escape(ruleDisplay(delivery.Rule.RuleType, english))
	icon := "📊"
	switch delivery.Rule.RuleType {
	case plans.MarketPriceAbove, plans.MarketPriceIncrease:
		icon = "📈"
	case plans.MarketPriceBelow, plans.MarketPriceDecrease:
		icon = "📉"
	case plans.MarketLiquidityBelow, plans.MarketLiquidityDecrease,
		plans.MarketLiquidityRemoved:
		icon = "⚠️"
	case plans.MarketLiquidityAdded, plans.MarketNewPool:
		icon = "💧"
	case plans.MarketVolumeAbove, plans.MarketVolumeSpike,
		plans.MarketTradeImbalance:
		icon = "🔥"
	case plans.MarketLargeBuy, plans.MarketConsecutiveLargeBuy:
		icon = "🟢"
	case plans.MarketLargeSell, plans.MarketConsecutiveLargeSell:
		icon = "🔴"
	case plans.MarketHolderIncrease, plans.MarketHolderDecrease,
		plans.MarketHolderRankEntered, plans.MarketHolderRankExited:
		icon = "🐋"
	case plans.MarketFourMemeLargeTrade, plans.MarketFourMemeProgress,
		plans.MarketFourMemeMigration:
		icon = "🚀"
	}
	if english {
		return icon + " " + token + " · " + ruleName + " stage summary"
	}
	return icon + " " + token + " · " + ruleName + "阶段汇总"
}

func marketStageCondition(rule store.MarketRule, symbol string, english bool) string {
	threshold := marketThreshold(&rule, symbol)
	if rule.RuleType == plans.MarketPriceAbove || rule.RuleType == plans.MarketPriceBelow {
		threshold = marketPriceThreshold(&rule)
	}
	switch rule.RuleType {
	case plans.MarketPriceBelow, plans.MarketLiquidityBelow:
		return "≤ " + threshold
	case plans.MarketHolderRankEntered, plans.MarketHolderRankExited:
		return "Top " + threshold
	case plans.MarketNewPool:
		return marketPlainText("发现 ≥ "+threshold+" 个新池",
			"Detect ≥ "+threshold+" new pool(s)", english)
	case plans.MarketFourMemeMigration:
		return marketPlainText("检测到 ≥ "+threshold+" 次迁移",
			"Detect ≥ "+threshold+" migration(s)", english)
	case plans.MarketConsecutiveLargeBuy, plans.MarketConsecutiveLargeSell:
		return marketPlainText(
			"单笔 ≥ "+threshold+" · 连续 ≥ "+
				fmt.Sprintf("%d", rule.TriggerCountThreshold)+" 笔",
			"Each ≥ "+threshold+" · consecutive count ≥ "+
				fmt.Sprintf("%d", rule.TriggerCountThreshold),
			english,
		)
	default:
		return "≥ " + threshold
	}
}

func marketStageTriggerCount(
	delivery store.MarketNotificationDelivery,
	english bool,
) string {
	required := int64(1)
	if delivery.Rule != nil && delivery.Rule.TriggerCountThreshold > 0 {
		required = delivery.Rule.TriggerCountThreshold
	}
	if english {
		return fmt.Sprintf("%d (send at %d)", delivery.TriggerCount, required)
	}
	return fmt.Sprintf("%d 次（达到 %d 次时发送）", delivery.TriggerCount, required)
}

func marketStageWindow(start, end time.Time, timezone string) string {
	startText := notificationfmt.LocalTime(start, timezone, "2006-01-02 15:04")
	endText := notificationfmt.LocalTime(end, timezone, "2006-01-02 15:04")
	switch {
	case startText == "":
		return endText
	case endText == "":
		return startText
	default:
		return startText + " → " + endText
	}
}

func marketStageContext(
	stats marketStageStats,
	timezone string,
	english bool,
) string {
	parts := make([]string, 0, 2)
	chains := make([]string, 0, len(stats.chains))
	for chainKey := range stats.chains {
		chains = append(chains, notificationfmt.ChainName(chainKey))
	}
	sort.Strings(chains)
	if len(chains) > 0 {
		parts = append(parts, escape(strings.Join(chains, " / ")))
	}
	first := notificationfmt.LocalTime(stats.firstAt, timezone, "15:04")
	last := notificationfmt.LocalTime(stats.lastAt, timezone, "15:04")
	switch {
	case first != "" && last != "" && first != last:
		parts = append(parts, escape(first+" / "+last))
	case first != "":
		parts = append(parts, escape(first))
	case last != "":
		parts = append(parts, escape(last))
	}
	return strings.Join(parts, " · ")
}

func marketStageMoneyTransition(first, last *string) string {
	if pointerValue(first) == "" || pointerValue(last) == "" {
		return ""
	}
	return marketUSD(first) + " → " + marketUSD(last)
}

func marketStagePriceTransition(first, last *string) string {
	if pointerValue(first) == "" || pointerValue(last) == "" {
		return ""
	}
	return marketPrice(first) + " → " + marketPrice(last)
}

func marketStageTrend(first, last *string, english bool) string {
	change, ok := percentChange(last, first)
	if !ok {
		return ""
	}
	if change.Sign() == 0 {
		return marketPlainText("基本持平", "flat", english)
	}
	value := notificationfmt.Percentage(new(big.Rat).Abs(change).RatString()) + "%"
	if change.Sign() > 0 {
		return marketPlainText("上涨 "+value, "up "+value, english)
	}
	return marketPlainText("下降 "+value, "down "+value, english)
}

func marketStageRatRange(
	minimum, maximum *big.Rat,
	unit, symbol string,
	english bool,
) string {
	if minimum == nil || maximum == nil {
		return ""
	}
	minimumText := marketStageRatValue(minimum, unit, symbol)
	maximumText := marketStageRatValue(maximum, unit, symbol)
	if minimum.Cmp(maximum) == 0 {
		return minimumText
	}
	return minimumText + " → " + maximumText
}

func marketStageRatValue(value *big.Rat, unit, symbol string) string {
	if value == nil {
		return ""
	}
	return marketUnitValue(stringPointer(decimalString(value)), unit, symbol, false)
}

func marketStageAvailableRat(
	value *big.Rat,
	available bool,
	unit, symbol string,
) string {
	if !available {
		return ""
	}
	return marketStageRatValue(value, unit, symbol)
}

func marketStageAverage(
	total *big.Rat,
	count int64,
	unit, symbol string,
) string {
	if total == nil || count <= 0 {
		return ""
	}
	average := new(big.Rat).Quo(new(big.Rat).Set(total), big.NewRat(count, 1))
	return marketStageRatValue(average, unit, symbol)
}

func marketStageTotalAmount(stats marketStageStats, symbol string) string {
	if stats.hasUSD {
		return marketStageRatValue(stats.totalUSD, "usd", "")
	}
	if stats.hasToken {
		return marketStageRatValue(stats.totalToken, "token", symbol)
	}
	return ""
}

func marketStageMaxAmount(stats marketStageStats, symbol string) string {
	if stats.hasUSD {
		return marketStageRatValue(stats.maxUSD, "usd", "")
	}
	if stats.hasToken {
		return marketStageRatValue(stats.maxToken, "token", symbol)
	}
	return ""
}

func marketStageCount(count int, suffix string, english bool) string {
	if count <= 0 {
		return ""
	}
	return fmt.Sprintf("%d%s", count, suffix)
}

func appendStageLine(lines *[]string, label, value string, english bool) {
	if line := notificationfmt.KeyValue(label, value, english); line != "" {
		*lines = append(*lines, line)
	}
}

func isLiquiditySnapshotRule(ruleType string) bool {
	return ruleType == plans.MarketLiquidityBelow ||
		ruleType == plans.MarketLiquidityDecrease
}

func isVolumeSnapshotRule(ruleType string) bool {
	return ruleType == plans.MarketVolumeAbove ||
		ruleType == plans.MarketVolumeSpike ||
		ruleType == plans.MarketTradeImbalance
}
