package monitor

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationfmt"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type addressStageStats struct {
	firstValue *string
	lastValue  *string
	netChange  *big.Rat
	totalMove  *big.Rat
	maxMove    *big.Rat
	firstAt    time.Time
	lastAt     time.Time
	symbol     string
	hasNumbers bool
}

func stageAlertText(rule store.WatchRule, result store.StageTriggerResult) string {
	english := normalizeLanguage(rule.NotificationLanguage) == "en"
	stats := addressStageStatistics(rule, result.Events)
	title := addressStageTitle(rule, result, stats, english)
	ruleLabel := ruleTypeLabels[normalizeLanguage(rule.NotificationLanguage)][rule.RuleType]
	if ruleLabel == "" {
		ruleLabel = strings.ReplaceAll(rule.RuleType, "_", " ")
	}
	lines := []string{
		localizedField("规则", "Rule", ruleLabel, english),
		localizedField(
			"统计周期",
			"Window",
			stageWindow(result.WindowStartsAt, result.WindowEndsAt, result.Timezone),
			english,
		),
		localizedField(
			"触发次数",
			"Triggers",
			stageTriggerCount(result, english),
			english,
		),
	}

	switch rule.RuleType {
	case plans.BalanceChange, plans.BalanceThreshold, plans.HighBalanceThreshold:
		lines = append(lines, localizedField(
			"阶段余额",
			"Balance",
			addressStageValueRange(stats, false, english),
			english,
		))
		lines = append(lines, localizedField(
			"累计净变化",
			"Net change",
			signedRatWithSymbol(stats.netChange, stats.symbol, stats.hasNumbers),
			english,
		))
		lines = append(lines, localizedField(
			"最大单次变化",
			"Largest single move",
			addressStageMove(stats.maxMove, stats.symbol, stats.hasNumbers),
			english,
		))
		if condition := addressStageThreshold(rule, stats.symbol, english); condition != "" {
			lines = append(lines, localizedField("你的条件", "Your condition", condition, english))
		}
	case plans.Incoming:
		lines = append(lines, localizedField(
			"累计转入",
			"Total received",
			addressStageMove(stats.totalMove, stats.symbol, stats.hasNumbers),
			english,
		))
		lines = append(lines, localizedField(
			"最大单次转入",
			"Largest receipt",
			addressStageMove(stats.maxMove, stats.symbol, stats.hasNumbers),
			english,
		))
		lines = append(lines, localizedField(
			"余额变化",
			"Balance",
			addressStageValueRange(stats, false, english),
			english,
		))
	case plans.Outgoing:
		lines = append(lines, localizedField(
			"累计转出",
			"Total sent",
			addressStageMove(stats.totalMove, stats.symbol, stats.hasNumbers),
			english,
		))
		lines = append(lines, localizedField(
			"最大单次转出",
			"Largest transfer",
			addressStageMove(stats.maxMove, stats.symbol, stats.hasNumbers),
			english,
		))
		lines = append(lines, localizedField(
			"余额变化",
			"Balance",
			addressStageValueRange(stats, false, english),
			english,
		))
	case plans.ApprovalChange:
		lines = append(lines, localizedField(
			"授权对象",
			"Approved spender",
			targetDescription(rule),
			english,
		))
		lines = append(lines, localizedField(
			"授权额度",
			"Allowance",
			addressStageValueRange(stats, true, english),
			english,
		))
		lines = append(lines, localizedField(
			"累计净变化",
			"Net change",
			signedRatWithSymbol(stats.netChange, stats.symbol, stats.hasNumbers),
			english,
		))
		lines = append(lines, localizedField(
			"最大单次变化",
			"Largest single move",
			addressStageMove(stats.maxMove, stats.symbol, stats.hasNumbers),
			english,
		))
	case plans.AddressInteraction:
		lines = append(lines, localizedField(
			"交互对象",
			"Interaction target",
			targetDescription(rule),
			english,
		))
	}
	lines = append(lines, localizedField(
		"首次 / 最近",
		"First / latest",
		stageEventTimeRange(stats.firstAt, stats.lastAt, result.Timezone, english),
		english,
	))
	lines = append(lines, localizedField(
		"网络",
		"Network",
		notificationfmt.ChainName(rule.ChainKey),
		english,
	))
	return buildAlert(title, lines...)
}

func addressStageStatistics(
	rule store.WatchRule,
	events []store.StageTriggerEvent,
) addressStageStats {
	stats := addressStageStats{
		netChange: new(big.Rat),
		totalMove: new(big.Rat),
		maxMove:   new(big.Rat),
	}
	for _, event := range events {
		if stats.symbol == "" && strings.TrimSpace(event.TokenSymbol) != "" {
			stats.symbol = strings.TrimSpace(event.TokenSymbol)
		}
		if stats.firstAt.IsZero() || (!event.OccurredAt.IsZero() &&
			event.OccurredAt.Before(stats.firstAt)) {
			stats.firstValue = event.PreviousValue
			stats.firstAt = event.OccurredAt
		}
		if !event.OccurredAt.IsZero() {
			if stats.lastAt.IsZero() || event.OccurredAt.After(stats.lastAt) {
				stats.lastAt = event.OccurredAt
				stats.lastValue = event.CurrentValue
			}
		} else if stats.lastValue == nil {
			stats.lastValue = event.CurrentValue
		}
		if event.CurrentValue == nil {
			continue
		}
		delta, ok := balanceDelta(event.PreviousValue, *event.CurrentValue)
		if !ok {
			continue
		}
		stats.hasNumbers = true
		stats.netChange.Add(stats.netChange, delta)
		absolute := absRat(delta)
		stats.totalMove.Add(stats.totalMove, absolute)
		if absolute.Cmp(stats.maxMove) > 0 {
			stats.maxMove.Set(absolute)
		}
	}
	if stats.symbol == "" && rule.TokenAddress == nil {
		if profile, err := chain.ChainProfile(rule.ChainKey, ""); err == nil {
			stats.symbol = profile.NativeSymbol
		}
	}
	return stats
}

func addressStageTitle(
	rule store.WatchRule,
	result store.StageTriggerResult,
	stats addressStageStats,
	english bool,
) string {
	wallet := monitoredWallet(rule)
	window := stageDurationLabel(result.WindowStartsAt, result.WindowEndsAt, english)
	switch rule.RuleType {
	case plans.Incoming:
		amount := addressStageMove(stats.totalMove, stats.symbol, stats.hasNumbers)
		if amount == "" {
			if english {
				return "📥 " + wallet + " incoming transfer summary"
			}
			return "📥 " + wallet + " 转入阶段汇总"
		}
		if english {
			return "📥 " + wallet + " received " + amount + " in " + window
		}
		return "📥 " + wallet + " " + window + "累计转入 " + amount
	case plans.Outgoing:
		amount := addressStageMove(stats.totalMove, stats.symbol, stats.hasNumbers)
		if amount == "" {
			if english {
				return "📤 " + wallet + " outgoing transfer summary"
			}
			return "📤 " + wallet + " 转出阶段汇总"
		}
		if english {
			return "📤 " + wallet + " sent " + amount + " in " + window
		}
		return "📤 " + wallet + " " + window + "累计转出 " + amount
	case plans.BalanceChange:
		change := signedRatWithSymbol(stats.netChange, stats.symbol, stats.hasNumbers)
		if change == "" {
			if english {
				return "📊 " + wallet + " balance stage summary"
			}
			return "📊 " + wallet + " 余额阶段汇总"
		}
		if english {
			return "📊 " + wallet + " net balance move " + change
		}
		return "📊 " + wallet + " 阶段余额净变化 " + change
	case plans.BalanceThreshold, plans.HighBalanceThreshold:
		if english {
			return "⚠️ " + wallet + " balance threshold summary"
		}
		return "⚠️ " + wallet + " 余额阈值阶段汇总"
	case plans.ApprovalChange:
		if english {
			return "🔐 " + wallet + " allowance change summary"
		}
		return "🔐 " + wallet + " 授权额度阶段汇总"
	case plans.AddressInteraction:
		if english {
			return fmt.Sprintf(
				"🔗 %s interacted with %s %d times",
				wallet,
				targetName(rule),
				result.TotalTriggerCount,
			)
		}
		return fmt.Sprintf(
			"🔗 %s 与%s发生 %d 次交互",
			wallet,
			targetName(rule),
			result.TotalTriggerCount,
		)
	default:
		if english {
			return "📊 " + wallet + " stage summary"
		}
		return "📊 " + wallet + " 阶段汇总"
	}
}

func addressStageValueRange(
	stats addressStageStats,
	allowance bool,
	english bool,
) string {
	if stats.firstValue == nil || stats.lastValue == nil {
		return ""
	}
	if allowance {
		return allowanceAmount(*stats.firstValue, stats.symbol, english) + " → " +
			allowanceAmount(*stats.lastValue, stats.symbol, english)
	}
	return pointerAmount(stats.firstValue, stats.symbol) + " → " +
		pointerAmount(stats.lastValue, stats.symbol)
}

func addressStageThreshold(rule store.WatchRule, symbol string, english bool) string {
	threshold := fullAmountWithSymbol(rule.Threshold, symbol)
	switch rule.RuleType {
	case plans.BalanceThreshold:
		if english {
			return "Balance ≤ " + threshold
		}
		return "余额 ≤ " + threshold
	case plans.HighBalanceThreshold:
		if english {
			return "Balance ≥ " + threshold
		}
		return "余额 ≥ " + threshold
	default:
		return ""
	}
}

func addressStageMove(value *big.Rat, symbol string, available bool) string {
	if !available {
		return ""
	}
	return compactRatWithSymbol(value, symbol)
}

func stageTriggerCount(result store.StageTriggerResult, english bool) string {
	if english {
		return fmt.Sprintf(
			"%d (send at %d)",
			result.TotalTriggerCount,
			result.TriggerCountThreshold,
		)
	}
	return fmt.Sprintf(
		"%d 次（达到 %d 次时发送）",
		result.TotalTriggerCount,
		result.TriggerCountThreshold,
	)
}

func stageWindow(start, end time.Time, timezone string) string {
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

func stageEventTimeRange(
	first, last time.Time,
	timezone string,
	english bool,
) string {
	firstText := notificationfmt.LocalTime(first, timezone, "15:04")
	lastText := notificationfmt.LocalTime(last, timezone, "15:04")
	switch {
	case firstText == "":
		return lastText
	case lastText == "":
		return firstText
	case first.Equal(last):
		return firstText
	default:
		return firstText + " / " + lastText
	}
}

func stageDurationLabel(start, end time.Time, english bool) string {
	duration := end.Sub(start)
	if duration <= 0 {
		if english {
			return "this window"
		}
		return "本周期"
	}
	minutes := int64(duration / time.Minute)
	if english {
		if minutes%1440 == 0 {
			return fmt.Sprintf("%dd", minutes/1440)
		}
		if minutes%60 == 0 {
			return fmt.Sprintf("%dh", minutes/60)
		}
		return fmt.Sprintf("%dm", minutes)
	}
	if minutes%1440 == 0 {
		return fmt.Sprintf("%d天内", minutes/1440)
	}
	if minutes%60 == 0 {
		return fmt.Sprintf("%d小时内", minutes/60)
	}
	return fmt.Sprintf("%d分钟内", minutes)
}
