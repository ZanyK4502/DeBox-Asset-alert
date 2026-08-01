package monitor

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type addressCombinationMoment struct {
	index int
	at    time.Time
	label string
}

func combinationAlertText(result store.CombinationTriggerResult) string {
	language := "zh"
	note := ""
	if result.Notification != nil {
		language = normalizeLanguage(result.Notification.NotificationLanguage)
		note = strings.TrimSpace(result.Notification.Note)
	}
	english := language == "en"
	lines := make([]string, 0, len(result.MemberProgress)+4)
	lines = append(lines,
		localizedField("组合", "Combination", note, english),
		localizedField(
			"统计周期",
			"Window",
			stageWindow(result.WindowStartsAt, result.WindowEndsAt, result.Timezone),
			english,
		),
	)
	for index, member := range result.MemberProgress {
		lines = append(lines, addressCombinationMemberLine(index, member, english))
	}
	lines = append(lines, localizedField(
		"触发顺序",
		"Signal order",
		addressCombinationTimeline(result.MemberProgress, result.Timezone, english),
		english,
	))
	if english {
		lines = append(
			lines,
			"These conditions were all met in the same window. This shows timing overlap, not causation; review the full events before acting.",
		)
	} else {
		lines = append(
			lines,
			"这些条件在同一周期内都已满足，说明它们在时间上出现联动；这不代表因果关系，建议结合完整事件谨慎确认。",
		)
	}
	return buildAlert(addressCombinationTitle(result.MemberProgress, english), lines...)
}

func addressCombinationMemberLine(
	index int,
	member store.CombinationMemberProgress,
	english bool,
) string {
	label := addressCombinationRuleLabel(member.RuleType, english)
	status := "✅"
	if member.TriggerCount < member.RequiredTriggerCount {
		status = "⏳"
	}
	progress := fmt.Sprintf("%d/%d", member.TriggerCount, member.RequiredTriggerCount)
	line := fmt.Sprintf(
		"%s %s %s · %s",
		combinationOrdinal(index),
		status,
		label,
		progress,
	)
	if result := addressCombinationKeyResult(member, english); result != "" {
		line += " · " + result
	}
	return line
}

func addressCombinationKeyResult(
	member store.CombinationMemberProgress,
	english bool,
) string {
	stats := addressStageStatistics(
		store.WatchRule{RuleType: member.RuleType},
		member.Events,
	)
	switch member.RuleType {
	case plans.BalanceChange:
		value := signedRatWithSymbol(stats.netChange, stats.symbol, stats.hasNumbers)
		return localizedCombinationResult("净变化", "net change", value, english)
	case plans.Incoming:
		value := addressStageMove(stats.totalMove, stats.symbol, stats.hasNumbers)
		return localizedCombinationResult("累计转入", "total received", value, english)
	case plans.Outgoing:
		value := addressStageMove(stats.totalMove, stats.symbol, stats.hasNumbers)
		return localizedCombinationResult("累计转出", "total sent", value, english)
	case plans.BalanceThreshold, plans.HighBalanceThreshold:
		value := pointerAmount(stats.lastValue, stats.symbol)
		return localizedCombinationResult("最近余额", "latest balance", value, english)
	case plans.ApprovalChange:
		value := addressStageValueRange(stats, true, english)
		return localizedCombinationResult("授权额度", "allowance", value, english)
	case plans.AddressInteraction:
		if len(member.Events) == 0 {
			return ""
		}
		return localizedCombinationResult(
			"交互次数",
			"interactions",
			fmt.Sprintf("%d", len(member.Events)),
			english,
		)
	default:
		return ""
	}
}

func localizedCombinationResult(
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

func addressCombinationTimeline(
	members []store.CombinationMemberProgress,
	timezone string,
	english bool,
) string {
	moments := make([]addressCombinationMoment, 0, len(members))
	for index, member := range members {
		at := time.Time{}
		if member.ReachedAt != nil {
			at = *member.ReachedAt
		}
		if at.IsZero() {
			at = latestAddressCombinationEvent(member)
		}
		moments = append(moments, addressCombinationMoment{
			index: index,
			at:    at,
			label: addressCombinationRuleLabel(member.RuleType, english),
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
	for position, moment := range moments {
		value := combinationOrdinal(position) + " "
		if clock := combinationClock(moment.at, timezone, english); clock != "" {
			value += clock + " "
		}
		parts = append(parts, value+moment.label)
	}
	return strings.Join(parts, " → ")
}

func latestAddressCombinationEvent(member store.CombinationMemberProgress) time.Time {
	var latest time.Time
	for _, event := range member.Events {
		if event.OccurredAt.After(latest) {
			latest = event.OccurredAt
		}
	}
	return latest
}

func combinationClock(value time.Time, timezone string, english bool) string {
	if value.IsZero() {
		return ""
	}
	return stageEventTimeRange(value, value, timezone, english)
}

func addressCombinationTitle(
	members []store.CombinationMemberProgress,
	english bool,
) string {
	types := make(map[string]bool, len(members))
	for _, member := range members {
		types[member.RuleType] = true
	}
	switch {
	case types[plans.Outgoing] && types[plans.BalanceThreshold]:
		if english {
			return "🔴 Outgoing funds and a low-balance warning appeared together"
		}
		return "🔴 资金转出与低余额警戒同时出现"
	case types[plans.ApprovalChange] && types[plans.Outgoing]:
		if english {
			return "🔴 An allowance change and outgoing funds appeared together"
		}
		return "🔴 授权变化与资金转出同时出现"
	case types[plans.AddressInteraction] && types[plans.Outgoing]:
		if english {
			return "⚠️ Watched-address activity and outgoing funds appeared together"
		}
		return "⚠️ 指定地址交互与资金转出同时出现"
	case types[plans.Incoming] && types[plans.HighBalanceThreshold]:
		if english {
			return "🟢 Incoming funds and a high-balance signal appeared together"
		}
		return "🟢 资金转入与高余额信号同时出现"
	default:
		if english {
			return "🔗 Multiple address signals appeared in the same window"
		}
		return "🔗 多项地址信号在同一周期内出现"
	}
}

func addressCombinationRuleLabel(ruleType string, english bool) string {
	language := "zh"
	if english {
		language = "en"
	}
	if label := ruleTypeLabels[language][ruleType]; label != "" {
		return label
	}
	return strings.ReplaceAll(ruleType, "_", " ")
}

func combinationOrdinal(index int) string {
	values := []string{"①", "②", "③", "④", "⑤", "⑥", "⑦", "⑧", "⑨", "⑩"}
	if index >= 0 && index < len(values) {
		return values[index]
	}
	return fmt.Sprintf("%d.", index+1)
}
