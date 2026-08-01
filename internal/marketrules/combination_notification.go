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

type marketCombinationMoment struct {
	at    time.Time
	label string
}

type watchCombinationStats struct {
	net, total *big.Rat
	last       *string
	symbol     string
	available  bool
}

func marketCombinationText(delivery store.MarketNotificationDelivery) string {
	english := notificationLanguage(delivery.NotificationLanguage) == "en"
	lines := make([]string, 0, len(delivery.CombinationMembers)+4)
	if note := strings.TrimSpace(delivery.Note); note != "" {
		lines = append(lines, notificationfmt.KeyValue(
			marketLabel("组合", "Combination", english),
			escape(note),
			english,
		))
	}
	lines = append(lines, notificationfmt.KeyValue(
		marketLabel("统计周期", "Window", english),
		marketStageWindow(delivery.StartsAt, delivery.EndsAt, delivery.Timezone),
		english,
	))
	for index, member := range delivery.CombinationMembers {
		lines = append(lines, marketCombinationMemberLine(index, member, english))
	}
	if timeline := marketCombinationTimeline(
		delivery.CombinationMembers,
		delivery.Timezone,
		english,
	); timeline != "" {
		lines = append(lines, notificationfmt.KeyValue(
			marketLabel("触发顺序", "Signal order", english),
			timeline,
			english,
		))
	}
	lines = append(lines, marketCombinationExplanation(
		delivery.CombinationMembers,
		english,
	))
	lines = append(
		[]string{"<b>" + marketCombinationTitle(delivery.CombinationMembers, english) + "</b>"},
		lines...,
	)
	return notificationfmt.JoinBlocks(lines...)
}

func marketCombinationMemberLine(
	index int,
	member store.MarketCombinationProgress,
	english bool,
) string {
	status := "✅"
	if member.TriggerCount < member.RequiredTriggerCount {
		status = "⏳"
	}
	line := fmt.Sprintf(
		"%s %s %s · %d/%d",
		marketCombinationOrdinal(index),
		status,
		marketCombinationMemberName(member, english),
		member.TriggerCount,
		member.RequiredTriggerCount,
	)
	if result := marketCombinationKeyResult(member, english); result != "" {
		line += " · " + result
	}
	return line
}

func marketCombinationMemberName(
	member store.MarketCombinationProgress,
	english bool,
) string {
	source := marketLabel("地址", "Address", english)
	if member.SourceType == "market" {
		source = marketLabel("市场", "Market", english)
	}
	rule := marketCombinationRuleLabel(member.RuleType, english)
	if member.SourceType != "market" {
		return source + " · " + rule
	}
	symbol := marketCombinationSymbol(member)
	if symbol == "" {
		return source + " · " + rule
	}
	return source + " · " + escape(symbol) + " " + rule
}

func marketCombinationKeyResult(
	member store.MarketCombinationProgress,
	english bool,
) string {
	if member.SourceType == "watch" {
		return watchCombinationKeyResult(member, english)
	}
	events := member.MarketEvents
	if len(events) == 0 {
		events = member.RecentEvents
	}
	rule := store.MarketRule{RuleType: member.RuleType}
	if member.MarketRule != nil {
		rule = *member.MarketRule
	}
	stats := calculateMarketStageStats(rule, events)
	symbol := marketCombinationSymbol(member)
	switch member.RuleType {
	case plans.MarketPriceAbove, plans.MarketPriceBelow:
		value := marketCombinationLastCurrent(events, "price", "")
		if value == "" {
			value = marketPrice(stats.lastPrice)
		}
		return marketCombinationResult("最近触发价", "latest trigger price", value, english)
	case plans.MarketPriceIncrease, plans.MarketPriceDecrease:
		return marketCombinationResult(
			"最大变动",
			"largest move",
			marketStageRatValue(stats.currentMax, "percent", ""),
			english,
		)
	case plans.MarketLiquidityBelow:
		value := marketCombinationLastCurrent(events, "usd", "")
		if value == "" {
			value = marketUSD(stats.lastLiquidity)
		}
		return marketCombinationResult("最近流动性", "latest liquidity", value, english)
	case plans.MarketLiquidityDecrease:
		return marketCombinationResult(
			"最大降幅",
			"largest drop",
			marketStageRatValue(stats.currentMax, "percent", ""),
			english,
		)
	case plans.MarketVolumeAbove:
		return marketCombinationResult(
			"最高成交量",
			"highest volume",
			marketStageRatValue(stats.currentMax, "usd", ""),
			english,
		)
	case plans.MarketVolumeSpike:
		return marketCombinationResult(
			"最大放大",
			"largest multiple",
			marketStageRatValue(stats.currentMax, "ratio", ""),
			english,
		)
	case plans.MarketTradeImbalance:
		return marketCombinationResult(
			"最高单边占比",
			"highest one-sided share",
			marketStageRatValue(stats.currentMax, "percent", ""),
			english,
		)
	case plans.MarketLargeBuy,
		plans.MarketLargeSell,
		plans.MarketConsecutiveLargeBuy,
		plans.MarketConsecutiveLargeSell,
		plans.MarketFourMemeLargeTrade:
		return marketCombinationResult(
			"累计成交",
			"total traded",
			marketStageTotalAmount(stats, symbol),
			english,
		)
	case plans.MarketLiquidityAdded:
		return marketCombinationResult(
			"累计加池",
			"total added",
			marketStageTotalAmount(stats, symbol),
			english,
		)
	case plans.MarketLiquidityRemoved:
		return marketCombinationResult(
			"累计撤池",
			"total removed",
			marketStageTotalAmount(stats, symbol),
			english,
		)
	case plans.MarketNewPool:
		return marketCombinationCountResult(
			"新池",
			"new pools",
			len(events),
			english,
		)
	case plans.MarketHolderIncrease:
		return marketCombinationResult(
			"累计增持",
			"total increase",
			marketStageTotalAmount(stats, symbol),
			english,
		)
	case plans.MarketHolderDecrease:
		return marketCombinationResult(
			"累计减持",
			"total decrease",
			marketStageTotalAmount(stats, symbol),
			english,
		)
	case plans.MarketHolderRankEntered:
		return marketCombinationCountResult(
			"进入榜单",
			"ranking entries",
			len(events),
			english,
		)
	case plans.MarketHolderRankExited:
		return marketCombinationCountResult(
			"退出榜单",
			"ranking exits",
			len(events),
			english,
		)
	case plans.MarketFourMemeProgress:
		return marketCombinationResult(
			"最高进度",
			"highest progress",
			marketStageRatValue(stats.currentMax, "percent", ""),
			english,
		)
	case plans.MarketFourMemeMigration:
		return marketCombinationCountResult(
			"迁移信号",
			"migration signals",
			len(events),
			english,
		)
	default:
		return ""
	}
}

func watchCombinationKeyResult(
	member store.MarketCombinationProgress,
	english bool,
) string {
	stats := calculateWatchCombinationStats(member.WatchEvents)
	switch member.RuleType {
	case plans.BalanceChange:
		return marketCombinationResult(
			"净变化",
			"net change",
			watchCombinationAmount(stats.net, stats.symbol, stats.available, true),
			english,
		)
	case plans.Incoming:
		return marketCombinationResult(
			"累计转入",
			"total received",
			watchCombinationAmount(stats.total, stats.symbol, stats.available, false),
			english,
		)
	case plans.Outgoing:
		return marketCombinationResult(
			"累计转出",
			"total sent",
			watchCombinationAmount(stats.total, stats.symbol, stats.available, false),
			english,
		)
	case plans.BalanceThreshold, plans.HighBalanceThreshold:
		value := ""
		if stats.last != nil {
			value = marketUnitValue(stats.last, "token", stats.symbol, true)
		}
		return marketCombinationResult("最近余额", "latest balance", value, english)
	case plans.ApprovalChange:
		value := ""
		if stats.last != nil {
			value = marketUnitValue(stats.last, "token", stats.symbol, true)
		}
		return marketCombinationResult("最近授权额度", "latest allowance", value, english)
	case plans.AddressInteraction:
		count := len(member.WatchEvents)
		if count == 0 {
			return ""
		}
		return marketCombinationResult(
			"交互",
			"interactions",
			fmt.Sprintf("%d", count),
			english,
		)
	default:
		return ""
	}
}

func calculateWatchCombinationStats(
	events []store.StageTriggerEvent,
) watchCombinationStats {
	stats := watchCombinationStats{
		net:   new(big.Rat),
		total: new(big.Rat),
	}
	for _, event := range events {
		if stats.symbol == "" {
			stats.symbol = strings.TrimSpace(event.TokenSymbol)
		}
		if event.CurrentValue != nil {
			stats.last = event.CurrentValue
		}
		current, currentOK := pointerRat(event.CurrentValue)
		previous, previousOK := pointerRat(event.PreviousValue)
		if !currentOK || !previousOK {
			continue
		}
		stats.available = true
		delta := new(big.Rat).Sub(current, previous)
		stats.net.Add(stats.net, delta)
		stats.total.Add(stats.total, new(big.Rat).Abs(delta))
	}
	return stats
}

func watchCombinationAmount(
	value *big.Rat,
	symbol string,
	available bool,
	signed bool,
) string {
	if !available || value == nil {
		return ""
	}
	sign := ""
	if signed && value.Sign() > 0 {
		sign = "+"
	} else if signed && value.Sign() < 0 {
		sign = "-"
	}
	absolute := new(big.Rat).Abs(new(big.Rat).Set(value))
	return sign + marketUnitValue(
		stringPointer(decimalString(absolute)),
		"token",
		symbol,
		true,
	)
}

func marketCombinationLastCurrent(
	events []store.MarketNotificationEvent,
	unit string,
	symbol string,
) string {
	for index := len(events) - 1; index >= 0; index-- {
		if pointerValue(events[index].CurrentValue) != "" {
			return marketUnitValue(events[index].CurrentValue, unit, symbol, false)
		}
	}
	return ""
}

func marketCombinationResult(
	chineseLabel, englishLabel, value string,
	english bool,
) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	if english {
		return englishLabel + " " + value
	}
	return chineseLabel + " " + value
}

func marketCombinationCountResult(
	chineseLabel, englishLabel string,
	count int,
	english bool,
) string {
	if count <= 0 {
		return ""
	}
	return marketCombinationResult(
		chineseLabel,
		englishLabel,
		fmt.Sprintf("%d", count),
		english,
	)
}

func marketCombinationTimeline(
	members []store.MarketCombinationProgress,
	timezone string,
	english bool,
) string {
	moments := make([]marketCombinationMoment, 0, len(members))
	for _, member := range members {
		at := time.Time{}
		if member.ReachedAt != nil {
			at = *member.ReachedAt
		}
		if at.IsZero() {
			at = latestMarketCombinationEvent(member)
		}
		moments = append(moments, marketCombinationMoment{
			at:    at,
			label: marketCombinationTimelineLabel(member, english),
		})
	}
	sort.SliceStable(moments, func(left, right int) bool {
		if moments[left].at.IsZero() {
			return false
		}
		if moments[right].at.IsZero() {
			return true
		}
		return moments[left].at.Before(moments[right].at)
	})
	parts := make([]string, 0, len(moments))
	for index, moment := range moments {
		value := marketCombinationOrdinal(index) + " "
		if !moment.at.IsZero() {
			value += notificationfmt.LocalTime(moment.at, timezone, "15:04") + " "
		}
		parts = append(parts, value+moment.label)
	}
	return strings.Join(parts, " → ")
}

func latestMarketCombinationEvent(member store.MarketCombinationProgress) time.Time {
	var latest time.Time
	for _, event := range member.WatchEvents {
		if event.OccurredAt.After(latest) {
			latest = event.OccurredAt
		}
	}
	events := member.MarketEvents
	if len(events) == 0 {
		events = member.RecentEvents
	}
	for _, event := range events {
		if event.Event.OccurredAt.After(latest) {
			latest = event.Event.OccurredAt
		}
	}
	return latest
}

func marketCombinationTimelineLabel(
	member store.MarketCombinationProgress,
	english bool,
) string {
	rule := marketCombinationRuleLabel(member.RuleType, english)
	if symbol := marketCombinationSymbol(member); symbol != "" {
		return escape(symbol) + " " + rule
	}
	return rule
}

func marketCombinationSymbol(member store.MarketCombinationProgress) string {
	events := member.MarketEvents
	if len(events) == 0 {
		events = member.RecentEvents
	}
	for _, event := range events {
		if symbol := strings.TrimSpace(event.Project.TokenSymbol); symbol != "" {
			return symbol
		}
	}
	return ""
}

func marketCombinationTitle(
	members []store.MarketCombinationProgress,
	english bool,
) string {
	types := make(map[string]bool, len(members))
	hasWatch, hasMarket := false, false
	for _, member := range members {
		types[member.RuleType] = true
		hasWatch = hasWatch || member.SourceType == "watch"
		hasMarket = hasMarket || member.SourceType == "market"
	}
	switch {
	case types[plans.Outgoing] &&
		(types[plans.MarketLargeSell] ||
			types[plans.MarketConsecutiveLargeSell] ||
			types[plans.MarketFourMemeLargeTrade]):
		return marketPlainText(
			"🔴 地址转出与大额卖出同时出现",
			"🔴 Address outflow and a large sell appeared together",
			english,
		)
	case (types[plans.MarketLiquidityBelow] ||
		types[plans.MarketLiquidityDecrease] ||
		types[plans.MarketLiquidityRemoved]) &&
		(types[plans.MarketPriceBelow] || types[plans.MarketPriceDecrease]):
		return marketPlainText(
			"🔴 流动性走弱与价格下跌同时出现",
			"🔴 Weaker liquidity and falling price appeared together",
			english,
		)
	case types[plans.MarketHolderDecrease] &&
		(types[plans.MarketLargeSell] || types[plans.MarketConsecutiveLargeSell]):
		return marketPlainText(
			"🔴 大户减持与大额卖出同时出现",
			"🔴 Holder reduction and a large sell appeared together",
			english,
		)
	case types[plans.MarketFourMemeProgress] &&
		(types[plans.MarketVolumeSpike] || types[plans.MarketVolumeAbove]):
		return marketPlainText(
			"🚀 Four.meme 进度与成交量放大同时出现",
			"🚀 Four.meme progress and stronger volume appeared together",
			english,
		)
	case (types[plans.MarketLargeBuy] || types[plans.MarketConsecutiveLargeBuy]) &&
		types[plans.MarketHolderIncrease]:
		return marketPlainText(
			"🟢 大额买入与大户增持同时出现",
			"🟢 A large buy and holder accumulation appeared together",
			english,
		)
	case hasWatch && hasMarket:
		return marketPlainText(
			"🔗 地址与市场信号在同一周期内形成联动",
			"🔗 Address and market signals aligned in the same window",
			english,
		)
	default:
		return marketPlainText(
			"🔗 多项市场信号在同一周期内形成联动",
			"🔗 Multiple market signals aligned in the same window",
			english,
		)
	}
}

func marketCombinationExplanation(
	members []store.MarketCombinationProgress,
	english bool,
) string {
	highRisk := false
	for _, member := range members {
		switch member.RuleType {
		case plans.Outgoing,
			plans.BalanceThreshold,
			plans.ApprovalChange,
			plans.MarketPriceBelow,
			plans.MarketPriceDecrease,
			plans.MarketLiquidityBelow,
			plans.MarketLiquidityDecrease,
			plans.MarketLiquidityRemoved,
			plans.MarketLargeSell,
			plans.MarketConsecutiveLargeSell,
			plans.MarketHolderDecrease:
			highRisk = true
		}
	}
	if highRisk {
		return marketPlainText(
			"这些风险信号在同一周期内同时满足，值得提高关注；请结合完整事件确认，不代表因果关系或投资结论。",
			"These risk signals were met in the same window and deserve attention. Review the full events; this does not prove causation or provide investment advice.",
			english,
		)
	}
	return marketPlainText(
		"这些条件在同一周期内都已满足，说明信号存在时间上的联动；短期信号可能变化，请结合完整事件谨慎确认。",
		"These conditions were met in the same window, showing timing alignment. Short-term signals can change, so review the full events before acting.",
		english,
	)
}

func marketCombinationRuleLabel(ruleType string, english bool) string {
	addressLabelsZH := map[string]string{
		plans.BalanceChange:        "余额变化",
		plans.Incoming:             "转入提醒",
		plans.Outgoing:             "转出提醒",
		plans.BalanceThreshold:     "低余额警戒",
		plans.HighBalanceThreshold: "高余额提醒",
		plans.ApprovalChange:       "授权变化",
		plans.AddressInteraction:   "指定地址交互",
	}
	addressLabelsEN := map[string]string{
		plans.BalanceChange:        "balance change",
		plans.Incoming:             "incoming transfer",
		plans.Outgoing:             "outgoing transfer",
		plans.BalanceThreshold:     "low-balance warning",
		plans.HighBalanceThreshold: "high-balance signal",
		plans.ApprovalChange:       "allowance change",
		plans.AddressInteraction:   "watched-address interaction",
	}
	if english {
		if label := addressLabelsEN[ruleType]; label != "" {
			return label
		}
	} else if label := addressLabelsZH[ruleType]; label != "" {
		return label
	}
	return escape(ruleDisplay(ruleType, english))
}

func marketCombinationOrdinal(index int) string {
	values := []string{"①", "②", "③", "④", "⑤", "⑥", "⑦", "⑧", "⑨", "⑩"}
	if index >= 0 && index < len(values) {
		return values[index]
	}
	return fmt.Sprintf("%d.", index+1)
}
