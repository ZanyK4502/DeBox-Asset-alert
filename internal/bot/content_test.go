package bot

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	boxbotapi "github.com/debox-pro/debox-chat-go-sdk/boxbotapi"
)

func TestMessageTextFallsBackToRawSDKField(t *testing.T) {
	service, client, _, _ := newTestService(t)
	message := testMessage("", "private", "user-id")
	message.TextRaw = " /start "

	if _, err := service.HandleUpdate(context.Background(), boxbotapi.Update{
		Message: message,
	}); err != nil {
		t.Fatalf("handle raw start message: %v", err)
	}
	if got := len(client.sentConfigs()); got != 1 {
		t.Fatalf("sent messages = %d, want 1", got)
	}
}

func TestSecureSignInCopyIsAvailableInBothLanguages(t *testing.T) {
	if !strings.Contains(menuText("zh"), "不会发起交易或消耗 Gas") {
		t.Fatal("Chinese menu does not explain the security of wallet signing")
	}
	if !strings.Contains(menuText("en"), "sends no transaction and uses no gas") {
		t.Fatal("English menu does not explain the security of wallet signing")
	}
}

func TestMonitoringCopyExplainsRulesAggregationAndSummaryBehavior(t *testing.T) {
	chinese := featuresText("zh")
	english := featuresText("en")

	for _, text := range []string{
		"低余额阈值",
		"高余额阈值",
		"阶段提醒（标准版、专业版）",
		"组合规则（专业版）",
		"个人监控面板保留 30 天",
		"首次统计此前 24 小时",
		"通知失败次数",
		"私聊确认失败",
	} {
		if !strings.Contains(chinese, text) {
			t.Fatalf("Chinese monitoring copy is missing %q", text)
		}
	}
	for _, text := range []string{
		"Low balance threshold",
		"High balance threshold",
		"Stage alert (Standard and Professional)",
		"Combination rule (Professional)",
		"dashboard for 30 days",
		"previous 24 hours",
		"notification failures",
		"private confirmation fails",
	} {
		if !strings.Contains(english, text) {
			t.Fatalf("English monitoring copy is missing %q", text)
		}
	}
}

func TestPlanCopyExplainsCapabilitiesPaymentAndSwitchingRules(t *testing.T) {
	service, _, _, _ := newTestService(t)
	chinese := service.plansText("zh")
	english := service.plansText("en")

	for _, text := range []string{
		"实时或阶段提醒",
		"组合规则",
		"组合成员会占用规则额度",
		"套餐到期后才能选择其他套餐",
		"3 个区块确认",
		"不支持退款",
	} {
		if !strings.Contains(chinese, text) {
			t.Fatalf("Chinese plan copy is missing %q", text)
		}
	}
	for _, text := range []string{
		"real-time or stage alerts",
		"combination rules",
		"Combination members use the rule quota",
		"choose another plan after it expires",
		"3 block confirmations",
		"non-refundable",
	} {
		if !strings.Contains(english, text) {
			t.Fatalf("English plan copy is missing %q", text)
		}
	}
}

func TestBotCopyFitsMessageLimitAndHasBalancedBoldTags(t *testing.T) {
	service, _, _, _ := newTestService(t)
	messages := map[string]string{
		"Chinese menu":     menuText("zh"),
		"English menu":     menuText("en"),
		"Chinese features": featuresText("zh"),
		"English features": featuresText("en"),
		"Chinese plans":    service.plansText("zh"),
		"English plans":    service.plansText("en"),
	}

	for name, message := range messages {
		if length := utf8.RuneCountInString(message); length > 4096 {
			t.Errorf("%s length = %d, want at most 4096", name, length)
		}
		if opens, closes := strings.Count(message, "<b>"), strings.Count(message, "</b>"); opens != closes {
			t.Errorf("%s has %d opening and %d closing bold tags", name, opens, closes)
		}
	}
}

func TestMenuIncludesLocalizedAggregateEventsEntry(t *testing.T) {
	service, _, _, _ := newTestService(t)
	tests := []struct {
		language string
		label    string
	}{
		{language: "zh", label: "汇总通知事件"},
		{language: "en", label: "Aggregate Events"},
	}
	for _, test := range tests {
		t.Run(test.language, func(t *testing.T) {
			markup := service.menuMarkup(test.language)
			found := false
			for _, row := range markup.InlineKeyboard {
				for _, button := range row {
					if button.Text == test.label &&
						button.URL != nil &&
						*button.URL == "https://example.test#aggregateEventsSection" {
						found = true
					}
				}
			}
			if !found {
				t.Fatalf("aggregate events entry missing from %s menu", test.language)
			}
		})
	}
}
