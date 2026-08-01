package monitor

import (
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestAddressCombinationNotificationExplainsSignalProgressAndOrder(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 7, 31, 1, 0, 0, 0, time.UTC)
	outgoingReached := start.Add(2 * time.Minute)
	thresholdReached := start.Add(5 * time.Minute)
	hundred, eighty, seventy, fifty, forty := "100", "80", "70", "50", "40"
	text := combinationAlertText(store.CombinationTriggerResult{
		WindowStartsAt: start,
		WindowEndsAt:   start.Add(time.Hour),
		Timezone:       "Asia/Shanghai",
		Notification: &store.AggregateNotification{
			NotificationLanguage: "zh",
			Note:                 "金库安全联动",
		},
		MemberProgress: []store.CombinationMemberProgress{
			{
				RuleType: plans.BalanceThreshold, TriggerCount: 1,
				RequiredTriggerCount: 1, ReachedAt: &thresholdReached,
				RecentNotes: []string{
					"钱包 0x1111111111111111111111111111111111111111 低于阈值",
				},
				Events: []store.StageTriggerEvent{{
					PreviousValue: &fifty, CurrentValue: &forty,
					TokenSymbol: "USDT", OccurredAt: thresholdReached,
				}},
			},
			{
				RuleType: plans.Outgoing, TriggerCount: 2,
				RequiredTriggerCount: 2, ReachedAt: &outgoingReached,
				RecentNotes: []string{
					"交易 0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				},
				Events: []store.StageTriggerEvent{
					{
						PreviousValue: &hundred, CurrentValue: &eighty,
						TokenSymbol: "USDT", OccurredAt: start.Add(time.Minute),
					},
					{
						PreviousValue: &eighty, CurrentValue: &seventy,
						TokenSymbol: "USDT", OccurredAt: outgoingReached,
					},
				},
			},
		},
	})
	plainText := plainNotificationText(text)
	for _, expected := range []string{
		"🔴 资金转出与低余额警戒同时出现",
		"组合：金库安全联动",
		"① ✅ 低余额阈值 · 1/1 · 最近余额 40 USDT",
		"② ✅ 转出提醒 · 2/2 · 累计转出 30 USDT",
		"触发顺序：① 09:02 转出提醒 → ② 09:05 低余额阈值",
		"不代表因果关系",
	} {
		if !strings.Contains(plainText, expected) {
			t.Fatalf("address combination missing %q:\n%s", expected, text)
		}
	}
	for _, forbidden := range []string{
		"0x1111111111111111111111111111111111111111",
		"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"钱包 ",
		"交易 ",
		"\n",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("address combination contains %q:\n%s", forbidden, text)
		}
	}
	if lines := notificationBlockCount(text); lines != 7 {
		t.Fatalf("address combination lines = %d:\n%s", lines, text)
	}
}

func TestAddressCombinationNotificationEnglishIsNaturalAndEscaped(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 31, 12, 30, 0, 0, time.UTC)
	zero, allowance, before, after := "0", "5000", "10", "5"
	text := combinationAlertText(store.CombinationTriggerResult{
		WindowStartsAt: at.Add(-time.Hour),
		WindowEndsAt:   at,
		Timezone:       "UTC",
		Notification: &store.AggregateNotification{
			NotificationLanguage: "en",
			Note:                 "<Team & Treasury>",
		},
		MemberProgress: []store.CombinationMemberProgress{
			{
				RuleType: plans.ApprovalChange, TriggerCount: 1,
				RequiredTriggerCount: 1, ReachedAt: &at,
				Events: []store.StageTriggerEvent{{
					PreviousValue: &zero, CurrentValue: &allowance,
					TokenSymbol: "USDT", OccurredAt: at,
				}},
			},
			{
				RuleType: plans.Outgoing, TriggerCount: 1,
				RequiredTriggerCount: 1, ReachedAt: &at,
				Events: []store.StageTriggerEvent{{
					PreviousValue: &before, CurrentValue: &after,
					TokenSymbol: "USDT", OccurredAt: at,
				}},
			},
		},
	})
	plainText := plainNotificationText(text)
	for _, expected := range []string{
		"An allowance change and outgoing funds appeared together",
		"Combination: &lt;Team &amp; Treasury&gt;",
		"Approval change · 1/1 · allowance 0 USDT → 5,000 USDT",
		"Outgoing transfer · 1/1 · total sent 5 USDT",
		"not causation",
	} {
		if !strings.Contains(plainText, expected) {
			t.Fatalf("English address combination missing %q:\n%s", expected, text)
		}
	}
	for _, character := range text {
		if unicode.Is(unicode.Han, character) {
			t.Fatalf("English address combination contains Chinese %q:\n%s", character, text)
		}
	}
	if strings.Contains(text, "<Team") || strings.Contains(text, "\n") {
		t.Fatalf("English address combination is unsafe or malformed:\n%s", text)
	}
}

func TestAddressCombinationCoversEveryAddressRuleLabel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		ruleType string
		chinese  string
		english  string
	}{
		{plans.BalanceChange, "余额变化", "Balance change"},
		{plans.Incoming, "转入提醒", "Incoming transfer"},
		{plans.Outgoing, "转出提醒", "Outgoing transfer"},
		{plans.BalanceThreshold, "低余额阈值", "Low balance threshold"},
		{plans.HighBalanceThreshold, "高余额阈值", "High balance threshold"},
		{plans.ApprovalChange, "授权 / Approve 监控", "Approval change"},
		{plans.AddressInteraction, "指定地址交互提醒", "Specified address interaction"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.ruleType, func(t *testing.T) {
			t.Parallel()
			member := store.CombinationMemberProgress{
				RuleType: test.ruleType, TriggerCount: 1, RequiredTriggerCount: 1,
			}
			chinese := addressCombinationMemberLine(0, member, false)
			english := addressCombinationMemberLine(0, member, true)
			if !strings.Contains(chinese, test.chinese) ||
				!strings.Contains(english, test.english) {
				t.Fatalf("labels = %q / %q", chinese, english)
			}
			if strings.Contains(chinese, test.ruleType) ||
				strings.Contains(english, test.ruleType) {
				t.Fatalf("member exposes internal rule code: %q / %q", chinese, english)
			}
		})
	}
}
