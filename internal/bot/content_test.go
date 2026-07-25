package bot

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	boxbotapi "github.com/debox-pro/debox-chat-go-sdk/boxbotapi"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/subscription"
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
	if !strings.Contains(menuText("zh"), "通过钱包签名完成安全登录") {
		t.Fatal("Chinese menu does not explain secure wallet sign-in")
	}
	if !strings.Contains(menuText("en"), "securely sign in with your wallet signature") {
		t.Fatal("English menu does not explain secure wallet sign-in")
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
		"没有剩余推送对象",
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
		"no targets remain",
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
		"14 USDT / 90 天",
		"150 USDT / 365 天",
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
		"14 USDT / 90 days",
		"150 USDT / 365 days",
		"3 block confirmations",
		"non-refundable",
	} {
		if !strings.Contains(english, text) {
			t.Fatalf("English plan copy is missing %q", text)
		}
	}
}

func TestSubscriptionCopyFormatsExpiryAsReadableUTC(t *testing.T) {
	service, _, _, _ := newTestService(t)
	service.deps.Subscriptions = fakeSubscriptions{value: subscription.Entitlement{
		Plan:          plans.Plan{Code: plans.Standard, Name: "标准版", RuleLimit: 10},
		Subscription:  &store.Subscription{ExpiresAt: time.Date(2026, 8, 20, 12, 0, 0, 0, time.FixedZone("test", 8*60*60))},
		DaysRemaining: 24,
	}}

	chinese, err := service.subscriptionText(context.Background(), "user-id", "zh")
	if err != nil {
		t.Fatalf("Chinese subscription text: %v", err)
	}
	english, err := service.subscriptionText(context.Background(), "user-id", "en")
	if err != nil {
		t.Fatalf("English subscription text: %v", err)
	}

	if !strings.Contains(chinese, "到期时间：2026-08-20 04:00:00（UTC）") {
		t.Fatalf("Chinese subscription expiry is not readable UTC: %q", chinese)
	}
	if !strings.Contains(english, "Expires at: 2026-08-20 04:00:00 (UTC)") {
		t.Fatalf("English subscription expiry is not readable UTC: %q", english)
	}
	if strings.Contains(chinese, "T04:00:00Z") || strings.Contains(english, "T04:00:00Z") {
		t.Fatal("subscription copy still exposes RFC3339 separators")
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

func TestMenuIncludesLocalizedSummaryDetailsEntry(t *testing.T) {
	service, _, _, _ := newTestService(t)
	tests := []struct {
		language string
		label    string
	}{
		{language: "zh", label: "汇总类通知详情"},
		{language: "en", label: "Summary"},
	}
	for _, test := range tests {
		t.Run(test.language, func(t *testing.T) {
			markup := service.menuMarkup(test.language)
			found := false
			for _, row := range markup.InlineKeyboard {
				for _, button := range row {
					if button.Text == test.label &&
						button.CallbackData != nil &&
						*button.CallbackData ==
							"alert:aggregate-details:"+test.language {
						found = true
					}
				}
			}
			if !found {
				t.Fatalf("summary details entry missing from %s menu", test.language)
			}
		})
	}
}

func TestSummaryDetailsCallbackIsLocalizedAndLinksToAggregateEvents(t *testing.T) {
	service, _, _, _ := newTestService(t)
	tests := []struct {
		language string
		text     string
		view     string
		back     string
	}{
		{language: "zh", text: "单条规则的阶段提醒事件与组合规则中的更多事件详情。", view: "查看", back: "返回主页"},
		{language: "en", text: "stage alerts from individual rules and combination rules", view: "View", back: "Home"},
	}
	for _, test := range tests {
		t.Run(test.language, func(t *testing.T) {
			text, err := service.callbackText(
				context.Background(),
				"alert:aggregate-details",
				"user-id",
				test.language,
			)
			if err != nil {
				t.Fatalf("summary details text: %v", err)
			}
			if !strings.Contains(text, test.text) {
				t.Fatalf("summary details text %q does not contain %q", text, test.text)
			}

			markup := service.callbackMarkup("alert:aggregate-details", test.language)
			if len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 2 {
				t.Fatalf("summary details buttons = %#v, want one row with two buttons", markup.InlineKeyboard)
			}
			view := markup.InlineKeyboard[0][0]
			if view.Text != test.view ||
				view.URL == nil ||
				*view.URL != "https://example.test#aggregateEventsSection" {
				t.Fatalf("view button = %#v", view)
			}
			back := markup.InlineKeyboard[0][1]
			if back.Text != test.back ||
				back.CallbackData == nil ||
				*back.CallbackData != "alert:intro:"+test.language {
				t.Fatalf("back button = %#v", back)
			}
		})
	}
}

func TestMenuCallbacksCarryDisplayedLanguage(t *testing.T) {
	service, _, _, _ := newTestService(t)
	for _, language := range []string{"zh", "en"} {
		t.Run(language, func(t *testing.T) {
			markup := service.menuMarkup(language)
			for _, row := range markup.InlineKeyboard {
				for _, button := range row {
					if button.CallbackData == nil {
						continue
					}
					data := *button.CallbackData
					if strings.HasPrefix(data, "alert:language:") {
						continue
					}
					if !strings.HasSuffix(data, ":"+language) {
						t.Fatalf("callback %q does not carry %s", data, language)
					}
				}
			}
		})
	}
}
