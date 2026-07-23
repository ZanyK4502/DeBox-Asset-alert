package bot

import (
	"context"
	"strings"
	"testing"

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

func TestMonitoringCopyExplainsThresholdAndSummaryBehavior(t *testing.T) {
	chinese := featuresText("zh")
	english := featuresText("en")

	for _, text := range []string{
		"创建规则时余额已达到或低于阈值会立即提醒一次",
		"首次统计此前 24 小时",
		"通知失败次数",
		"私聊确认失败",
	} {
		if !strings.Contains(chinese, text) {
			t.Fatalf("Chinese monitoring copy is missing %q", text)
		}
	}
	for _, text := range []string{
		"already at or below the threshold when created",
		"previous 24 hours",
		"notification failures",
		"private confirmation fails",
	} {
		if !strings.Contains(english, text) {
			t.Fatalf("English monitoring copy is missing %q", text)
		}
	}
}

func TestPlanCopyExplainsPaymentAndSwitchingRules(t *testing.T) {
	service, _, _, _ := newTestService(t)
	chinese := service.plansText("zh")
	english := service.plansText("en")

	for _, text := range []string{"套餐到期后才能选择其他套餐", "3 个区块确认", "不支持退款"} {
		if !strings.Contains(chinese, text) {
			t.Fatalf("Chinese plan copy is missing %q", text)
		}
	}
	for _, text := range []string{
		"choose another plan after it expires",
		"3 block confirmations",
		"non-refundable",
	} {
		if !strings.Contains(english, text) {
			t.Fatalf("English plan copy is missing %q", text)
		}
	}
}
