package monitor

import (
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestAllAddressStageRulesUseStatisticalSummaries(t *testing.T) {
	start := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	result := store.StageTriggerResult{
		TotalTriggerCount:     3,
		TriggerCountThreshold: 3,
		WindowStartsAt:        start,
		WindowEndsAt:          start.Add(30 * time.Minute),
		Timezone:              "Asia/Shanghai",
		Events: []store.StageTriggerEvent{
			{
				PreviousValue: stringPointer("100"),
				CurrentValue:  stringPointer("110"),
				TokenSymbol:   "BOX",
				Note:          "this raw event must not appear",
				OccurredAt:    start.Add(5 * time.Minute),
			},
			{
				PreviousValue: stringPointer("110"),
				CurrentValue:  stringPointer("125"),
				TokenSymbol:   "BOX",
				OccurredAt:    start.Add(15 * time.Minute),
			},
			{
				PreviousValue: stringPointer("125"),
				CurrentValue:  stringPointer("120"),
				TokenSymbol:   "BOX",
				OccurredAt:    start.Add(25 * time.Minute),
			},
		},
	}
	target := "0x2222222222222222222222222222222222222222"
	tests := []struct {
		ruleType string
		expected string
	}{
		{plans.BalanceChange, "阶段余额净变化 +20 BOX"},
		{plans.Incoming, "30分钟内累计转入 30 BOX"},
		{plans.Outgoing, "30分钟内累计转出 30 BOX"},
		{plans.BalanceThreshold, "余额阈值阶段汇总"},
		{plans.HighBalanceThreshold, "余额阈值阶段汇总"},
		{plans.ApprovalChange, "授权额度阶段汇总"},
		{plans.AddressInteraction, "与&lt;Router&gt;发生 3 次交互"},
	}
	for _, test := range tests {
		t.Run(test.ruleType, func(t *testing.T) {
			rule := store.WatchRule{
				ChainKey:              "bsc",
				WalletAddress:         "0x1111111111111111111111111111111111111111",
				TargetAddress:         &target,
				TargetLabel:           "<Router>",
				RuleType:              test.ruleType,
				Threshold:             "50",
				NotificationLanguage:  "zh",
				TriggerCountThreshold: 3,
			}
			text := stageAlertText(rule, result)
			plainText := plainNotificationText(text)
			for _, expected := range []string{
				test.expected,
				"规则：",
				"统计周期：2026-07-31 20:00 → 2026-07-31 20:30",
				"触发次数：3 次（达到 3 次时发送）",
				"首次 / 最近：20:05 / 20:25",
				"网络：BNB Chain",
			} {
				if !strings.Contains(plainText, expected) {
					t.Fatalf("stage notification missing %q:\n%s", expected, text)
				}
			}
			for _, forbidden := range []string{
				"this raw event must not appear",
				"最近事件",
				"0x1111111111111111111111111111111111111111",
				"\n",
			} {
				if strings.Contains(text, forbidden) {
					t.Fatalf("stage notification contains %q:\n%s", forbidden, text)
				}
			}
			if lines := notificationBlockCount(text); lines > 10 {
				t.Fatalf("stage notification is too long (%d lines):\n%s", lines, text)
			}
		})
	}
}

func TestAllAddressStageRulesAreLocalizedInEnglish(t *testing.T) {
	start := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	result := store.StageTriggerResult{
		TotalTriggerCount:     2,
		TriggerCountThreshold: 2,
		WindowStartsAt:        start,
		WindowEndsAt:          start.Add(time.Hour),
		Timezone:              "UTC",
		Events: []store.StageTriggerEvent{
			{
				PreviousValue: stringPointer("100"),
				CurrentValue:  stringPointer("110"),
				TokenSymbol:   "BOX",
				OccurredAt:    start.Add(10 * time.Minute),
			},
			{
				PreviousValue: stringPointer("110"),
				CurrentValue:  stringPointer("120"),
				TokenSymbol:   "BOX",
				OccurredAt:    start.Add(20 * time.Minute),
			},
		},
	}
	target := "0x2222222222222222222222222222222222222222"
	for _, ruleType := range []string{
		plans.BalanceChange,
		plans.Incoming,
		plans.Outgoing,
		plans.BalanceThreshold,
		plans.HighBalanceThreshold,
		plans.ApprovalChange,
		plans.AddressInteraction,
	} {
		t.Run(ruleType, func(t *testing.T) {
			text := stageAlertText(store.WatchRule{
				ChainKey: "base", WalletAddress: "0x1111111111111111111111111111111111111111",
				TargetAddress: &target, TargetLabel: "Router",
				RuleType: ruleType, Threshold: "50",
				NotificationLanguage: "en", TriggerCountThreshold: 2,
			}, result)
			plainText := plainNotificationText(text)
			for _, expected := range []string{
				"Rule: ",
				"Window: 2026-07-31 12:00 → 2026-07-31 13:00",
				"Triggers: 2 (send at 2)",
				"First / latest: 12:10 / 12:20",
				"Network: Base",
			} {
				if !strings.Contains(plainText, expected) {
					t.Fatalf("English stage notification missing %q:\n%s", expected, text)
				}
			}
			for _, character := range text {
				if unicode.Is(unicode.Han, character) {
					t.Fatalf("English stage notification contains Chinese %q:\n%s", character, text)
				}
			}
		})
	}
}

func TestAddressStageNotificationOmitsUnavailableStatistics(t *testing.T) {
	text := stageAlertText(store.WatchRule{
		ChainKey: "bsc", WalletAddress: "0x1111111111111111111111111111111111111111",
		RuleType: plans.BalanceChange, NotificationLanguage: "zh",
	}, store.StageTriggerResult{
		TotalTriggerCount: 1, TriggerCountThreshold: 1,
	})
	for _, forbidden := range []string{
		"阶段余额：",
		"累计净变化：",
		"最大单次变化：",
		"首次 / 最近：",
		"：0",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("notification should omit %q:\n%s", forbidden, text)
		}
	}
}
