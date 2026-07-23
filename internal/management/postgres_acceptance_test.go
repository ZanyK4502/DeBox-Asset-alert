package management

import (
	"context"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/subscription"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/testdb"
)

const acceptanceWallet = "0x1111111111111111111111111111111111111111"

func TestAcceptanceCreateRuleUsesRealQuotaState(t *testing.T) {
	database := testdb.Open(t)
	catalog := acceptanceCatalog(t)
	entitlements := subscription.New(database, catalog, "")
	service := New(Dependencies{
		Repository:   database,
		Entitlements: entitlements,
		Chain: &fakeChainService{balance: chain.BalanceResult{
			Value:         "1.5",
			Symbol:        "BNB",
			ChainKey:      "bsc",
			ChainID:       56,
			WalletAddress: acceptanceWallet,
		}},
		DefaultChainKey: "bsc",
	})

	input := DefaultWatchRuleInput()
	input.WalletAddress = acceptanceWallet
	created, err := service.CreateWatchRule(
		context.Background(),
		"acceptance-rules-user",
		input,
	)
	if err != nil {
		t.Fatalf("CreateWatchRule() error = %v", err)
	}
	if created.Rule.RunStatus != "active" ||
		created.Rule.NotificationChatID != "acceptance-rules-user" ||
		created.Rule.LastValue == nil || *created.Rule.LastValue != "1.5" {
		t.Fatalf("unexpected created rule: %#v", created.Rule)
	}
	rules, err := service.ListWatchRules(context.Background(), "acceptance-rules-user")
	if err != nil || len(rules) != 1 || rules[0].ID != created.Rule.ID {
		t.Fatalf("ListWatchRules() = %#v, %v", rules, err)
	}

	_, err = service.CreateWatchRule(
		context.Background(),
		"acceptance-rules-user",
		input,
	)
	if err == nil {
		t.Fatal("free plan accepted a second watch rule")
	}
	rules, listErr := service.ListWatchRules(context.Background(), "acceptance-rules-user")
	if listErr != nil || len(rules) != 1 {
		t.Fatalf("rules after rejected creation = %#v, %v", rules, listErr)
	}
}

func TestAcceptanceGroupUnbindFallsBackToPrivateSummary(t *testing.T) {
	database := testdb.Open(t)
	catalog := acceptanceCatalog(t)
	entitlements := subscription.New(database, catalog, "")
	userID := "acceptance-group-user"
	if _, err := database.ActivateSubscription(
		context.Background(),
		userID,
		plans.Professional,
		30,
	); err != nil {
		t.Fatalf("ActivateSubscription() error = %v", err)
	}
	notifier := &fakeNotificationService{}
	service := New(Dependencies{
		Repository:   database,
		Entitlements: entitlements,
		Groups: &fakeGroupService{
			info:   map[string]any{"name": "Acceptance Group"},
			joined: true,
		},
		Notifications: notifier,
	})

	creation, err := service.CreateNotificationGroup(
		context.Background(),
		userID,
		acceptanceWallet,
		NotificationGroupInput{
			Link: "https://m.debox.pro/group?id=acceptance-group&code=acceptance-user",
		},
	)
	if err != nil {
		t.Fatalf("CreateNotificationGroup() error = %v", err)
	}
	if _, err := service.SaveSummarySettings(
		context.Background(),
		userID,
		SummarySettingsInput{
			Enabled:  true,
			PushTime: "20:00",
			Timezone: "Asia/Shanghai",
			ChatType: "group",
			ChatID:   creation.Group.GID,
			Label:    "Acceptance Summary",
			Language: "en",
		},
	); err != nil {
		t.Fatalf("SaveSummarySettings() error = %v", err)
	}

	deletion, err := service.DeleteNotificationGroup(
		context.Background(),
		userID,
		creation.Group.ID,
	)
	if err != nil {
		t.Fatalf("DeleteNotificationGroup() error = %v", err)
	}
	if !deletion.SummaryTargetChanged ||
		!deletion.SummaryConfirmationSent ||
		deletion.SummaryDisabled {
		t.Fatalf("unexpected group deletion: %#v", deletion)
	}
	if notifier.chatID != userID || notifier.chatType != "private" {
		t.Fatalf("fallback notification target = %q/%q", notifier.chatID, notifier.chatType)
	}

	active, err := database.GetActiveSubscription(context.Background(), userID)
	if err != nil || active == nil {
		t.Fatalf("GetActiveSubscription() = %#v, %v", active, err)
	}
	if active.DailySummaryEnabled != 1 ||
		active.DailySummaryChatType != "private" ||
		active.DailySummaryChatID != userID {
		t.Fatalf("summary did not fall back to private: %#v", active)
	}
	group, err := database.GetNotificationGroup(
		context.Background(),
		userID,
		creation.Group.GID,
	)
	if err != nil || group != nil {
		t.Fatalf("deleted group = %#v, %v", group, err)
	}
}

func acceptanceCatalog(t *testing.T) *plans.Catalog {
	t.Helper()
	catalog, err := plans.NewCatalog("10", 30, "USDT")
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	return catalog
}
