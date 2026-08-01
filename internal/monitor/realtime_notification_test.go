package monitor

import (
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestAddressRealtimeNotificationTemplates(t *testing.T) {
	fixedTime := time.Date(2026, 7, 31, 12, 14, 0, 0, time.UTC)
	transaction := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tests := []struct {
		name       string
		ruleType   string
		previous   *string
		current    string
		symbol     string
		threshold  string
		targetNote string
		expectedZH []string
		expectedEN []string
	}{
		{
			name:      "balance change",
			ruleType:  plans.BalanceChange,
			previous:  stringPointer("1000"),
			current:   "1500",
			symbol:    "USDT",
			threshold: "100",
			expectedZH: []string{
				"余额增加 500 USDT",
				"当前余额：1,500 USDT",
				"本次变化：+500 USDT",
				"变化幅度：+50%",
				"触发条件：余额变化 ≥ 100 USDT",
				"🟢 风险等级：低",
			},
			expectedEN: []string{
				"balance increased by 500 USDT",
				"Current balance: 1,500 USDT",
				"Change this time: +500 USDT",
				"Percentage change: +50%",
				"Trigger condition: balance change ≥ 100 USDT",
				"🟢 Risk level: Low",
			},
		},
		{
			name:      "incoming",
			ruleType:  plans.Incoming,
			previous:  stringPointer("1000"),
			current:   "1600",
			symbol:    "USDT",
			threshold: "500",
			expectedZH: []string{
				"收到转入 600 USDT",
				"当前余额：1,600 USDT",
				"余额增幅：+60%",
				"触发条件：单笔转入 ≥ 500 USDT",
				"🟢 风险等级：低",
			},
			expectedEN: []string{
				"received 600 USDT",
				"Current balance: 1,600 USDT",
				"Balance increase: +60%",
				"Trigger condition: incoming amount ≥ 500 USDT",
				"🟢 Risk level: Low",
			},
		},
		{
			name:      "outgoing",
			ruleType:  plans.Outgoing,
			previous:  stringPointer("2432.75"),
			current:   "1832.75",
			symbol:    "USDT",
			threshold: "500",
			expectedZH: []string{
				"转出 600 USDT",
				"当前余额：1,832.75 USDT",
				"转出前：2,432.75 USDT",
				"余额降幅：-24.66%",
				"🟠 风险等级：中",
				"请确认这笔资金变化是否为本人操作。",
			},
			expectedEN: []string{
				"sent 600 USDT",
				"Current balance: 1,832.75 USDT",
				"Balance before: 2,432.75 USDT",
				"Balance decrease: -24.66%",
				"🟠 Risk level: Medium",
				"Please confirm that you recognize this balance change.",
			},
		},
		{
			name:      "low balance",
			ruleType:  plans.BalanceThreshold,
			current:   "92.4",
			symbol:    "USDT",
			threshold: "100",
			expectedZH: []string{
				"余额低于 100 USDT",
				"当前余额：92.4 USDT",
				"低于阈值：7.6 USDT",
				"🔴 风险等级：高",
				"余额不足可能影响后续转账或合约操作。",
			},
			expectedEN: []string{
				"balance fell below 100 USDT",
				"Current balance: 92.4 USDT",
				"Below threshold by: 7.6 USDT",
				"🔴 Risk level: High",
				"A low balance may prevent future transfers or contract actions.",
			},
		},
		{
			name:      "high balance",
			ruleType:  plans.HighBalanceThreshold,
			current:   "1200",
			symbol:    "USDT",
			threshold: "1000",
			expectedZH: []string{
				"余额高于 1,000 USDT",
				"当前余额：1,200 USDT",
				"超过阈值：200 USDT",
				"🟢 风险等级：低",
			},
			expectedEN: []string{
				"balance rose above 1,000 USDT",
				"Current balance: 1,200 USDT",
				"Above threshold by: 200 USDT",
				"🟢 Risk level: Low",
			},
		},
		{
			name:       "approval",
			ruleType:   plans.ApprovalChange,
			previous:   stringPointer("0"),
			current:    "50000",
			symbol:     "USDT",
			targetNote: "PancakeSwap",
			expectedZH: []string{
				"🚨 授权风险 · PancakeSwap",
				"PancakeSwap 当前最多可以使用 50,000 USDT。",
				"当前授权：50,000 USDT",
				"授权对象：PancakeSwap · 0x3333…3333",
				"🔴 风险等级：高",
				"请确认是否为本人操作。",
			},
			expectedEN: []string{
				"🚨 Approval risk · PancakeSwap",
				"PancakeSwap can now use up to 50,000 USDT from this wallet.",
				"Current allowance: 50,000 USDT",
				"Approved address: PancakeSwap · 0x3333…3333",
				"🔴 Risk level: High",
				"Confirm that you authorized it.",
			},
		},
		{
			name:       "specified address interaction",
			ruleType:   plans.AddressInteraction,
			previous:   stringPointer("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
			current:    transaction,
			targetNote: "Risk Contract",
			expectedZH: []string{
				"⚠️ 指定地址交互 · Risk Contract",
				"监控钱包刚刚与该地址发生了一笔链上交易。",
				"目标地址：Risk Contract · 0x3333…3333",
				"交易：0xaaaa…aaaa",
				"🟠 风险等级：中",
			},
			expectedEN: []string{
				"⚠️ Watched-address interaction · Risk Contract",
				"The monitored wallet just made an on-chain transaction with this address.",
				"Target address: Risk Contract · 0x3333…3333",
				"Transaction: 0xaaaa…aaaa",
				"🟠 Risk level: Medium",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, language := range []string{"zh", "en"} {
				t.Run(language, func(t *testing.T) {
					rule := testRule(test.ruleType, test.previous, plans.Professional)
					rule.Threshold = test.threshold
					rule.TargetLabel = test.targetNote
					rule.NotificationLanguage = language
					message := alertText(
						rule,
						test.previous,
						test.current,
						test.symbol,
						"legacy note should not replace the dedicated template",
						fixedTime,
					)
					plainMessage := plainNotificationText(message)
					expected := test.expectedZH
					if language == "en" {
						expected = test.expectedEN
					}
					for _, fragment := range expected {
						if !strings.Contains(plainMessage, fragment) {
							t.Fatalf("notification missing %q:\n%s", fragment, message)
						}
					}
					assertRealtimeNotificationShape(t, message, rule, language)
					assertNotificationFieldFormatting(t, message)
				})
			}
		})
	}
}

func TestApprovalRealtimeNotificationHandlesRevokedAndUnlimitedAllowances(t *testing.T) {
	rule := testRule(plans.ApprovalChange, nil, plans.Standard)
	rule.TargetLabel = "Router"
	previous := "50000"
	revoked := alertText(
		rule,
		&previous,
		"0",
		"USDT",
		"",
		time.Date(2026, 7, 31, 12, 14, 0, 0, time.UTC),
	)
	plainRevoked := plainNotificationText(revoked)
	for _, expected := range []string{
		"✅ 授权已取消 · Router",
		"当前授权：0 USDT",
		"🟢 风险等级：低",
		"该授权额度现已归零。",
	} {
		if !strings.Contains(plainRevoked, expected) {
			t.Fatalf("revoked approval missing %q:\n%s", expected, revoked)
		}
	}

	unlimited := alertText(
		rule,
		stringPointer("0"),
		"1000000000000000000000000000000",
		"USDT",
		"",
		time.Date(2026, 7, 31, 12, 14, 0, 0, time.UTC),
	)
	plainUnlimited := plainNotificationText(unlimited)
	if !strings.Contains(plainUnlimited, "当前授权：无限额度 USDT") ||
		!strings.Contains(plainUnlimited, "🔴 风险等级：高") {
		t.Fatalf("unlimited approval = %s", unlimited)
	}
}

func TestThresholdRealtimeNotificationsDescribeEqualityAccurately(t *testing.T) {
	for _, test := range []struct {
		ruleType string
		zh       string
		en       string
	}{
		{
			ruleType: plans.BalanceThreshold,
			zh:       "余额达到低余额阈值 100 USDT",
			en:       "reached the low-balance threshold 100 USDT",
		},
		{
			ruleType: plans.HighBalanceThreshold,
			zh:       "余额达到高余额阈值 100 USDT",
			en:       "reached the high-balance threshold 100 USDT",
		},
	} {
		rule := testRule(test.ruleType, nil, plans.Standard)
		rule.Threshold = "100"
		for _, language := range []string{"zh", "en"} {
			rule.NotificationLanguage = language
			message := alertText(
				rule,
				nil,
				"100",
				"USDT",
				"",
				time.Date(2026, 7, 31, 12, 14, 0, 0, time.UTC),
			)
			expected := test.zh
			if language == "en" {
				expected = test.en
			}
			if !strings.Contains(message, expected) {
				t.Fatalf("%s equality notification missing %q: %s", language, expected, message)
			}
		}
	}
}

func TestRealtimeNotificationEnglishActionUsesExistingAddressSummary(t *testing.T) {
	notifier := &fakeNotifier{}
	executor := newTestExecutor(t, &fakeRepository{}, &fakeChain{}, notifier)
	rule := testRule(plans.Incoming, nil, plans.Standard)
	rule.NotificationLanguage = "en"

	if _, err := executor.sendRealtimeNotification(
		rule,
		"message",
		"nd_0123456789abcdef0123456789abcdef01234567",
	); err != nil {
		t.Fatalf("sendRealtimeNotification() error = %v", err)
	}
	if notifier.actionText != "View details" ||
		notifier.actionURL != "https://example.test?notification_id=nd_0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("action = %q / %q", notifier.actionText, notifier.actionURL)
	}
}

func TestRealtimeNotificationEscapesUserLabels(t *testing.T) {
	rule := testRule(plans.AddressInteraction, nil, plans.Professional)
	rule.TargetLabel = "<Risk & Contract>"
	message := alertText(
		rule,
		nil,
		"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"",
		"",
		time.Date(2026, 7, 31, 12, 14, 0, 0, time.UTC),
	)
	if strings.Contains(message, "<Risk") ||
		!strings.Contains(message, "&lt;Risk &amp; Contract&gt;") {
		t.Fatalf("notification did not safely escape the label: %s", message)
	}
}

func assertRealtimeNotificationShape(
	t *testing.T,
	message string,
	rule store.WatchRule,
	language string,
) {
	t.Helper()
	if strings.Contains(message, "\n") || !strings.Contains(message, "<br/>") {
		t.Fatalf("notification does not use DeBox HTML line breaks: %q", message)
	}
	if lineCount := notificationBlockCount(message); lineCount > 8 {
		t.Fatalf("notification has %d lines, want at most 8:\n%s", lineCount, message)
	}
	if strings.Contains(message, rule.WalletAddress) ||
		(rule.TargetAddress != nil && strings.Contains(message, *rule.TargetAddress)) {
		t.Fatalf("notification contains a full address:\n%s", message)
	}
	if strings.Contains(message, "legacy note should not replace") {
		t.Fatalf("notification used the legacy generic note:\n%s", message)
	}
	if language == "en" {
		plainMessage := plainNotificationText(message)
		if strings.Contains(message, "：") ||
			!strings.Contains(plainMessage, "Network: BNB Chain · Today 20:14") {
			t.Fatalf("English notification punctuation or context is incorrect:\n%s", message)
		}
		return
	}
	if !strings.Contains(plainNotificationText(message), "网络：BNB Chain · 今天 20:14") {
		t.Fatalf("Chinese notification context is incorrect:\n%s", message)
	}
}
