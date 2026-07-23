package summary

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/testdb"
)

func TestAcceptanceDailySummaryReadsEventsAndMarksDelivery(t *testing.T) {
	database := testdb.Open(t)
	userID := "acceptance-summary-user"
	subscription, err := database.ActivateSubscription(
		context.Background(),
		userID,
		plans.Standard,
		30,
	)
	if err != nil {
		t.Fatalf("ActivateSubscription() error = %v", err)
	}
	previous := "1"
	current := "2"
	rule, err := database.CreateWatchRule(context.Background(), store.CreateWatchRuleParams{
		DeBoxUserID:          userID,
		ChainKey:             "bsc",
		ChainID:              56,
		WalletAddress:        "0x1111111111111111111111111111111111111111",
		RuleType:             plans.BalanceChange,
		Threshold:            "0",
		NotificationChatID:   userID,
		NotificationChatType: "private",
		NotificationLabel:    "Private",
		NotificationLanguage: "en",
		LastValue:            &previous,
	})
	if err != nil {
		t.Fatalf("CreateWatchRule() error = %v", err)
	}
	if _, err := database.CreateAlertEvent(context.Background(), store.CreateAlertEventParams{
		WatchRuleID:        rule.ID,
		EventType:          plans.BalanceChange,
		PreviousValue:      &previous,
		CurrentValue:       &current,
		NotificationStatus: "sent",
	}); err != nil {
		t.Fatalf("CreateAlertEvent() error = %v", err)
	}
	if _, err := database.UpdateDailySummarySettings(
		context.Background(),
		userID,
		store.DailySummarySettings{
			Enabled:      true,
			PushTime:     "23:58",
			TimezoneName: "UTC",
			ChatType:     "private",
			ChatID:       userID,
			Label:        "Acceptance",
			Language:     "en",
		},
	); err != nil {
		t.Fatalf("UpdateDailySummarySettings() error = %v", err)
	}

	now := time.Now().UTC()
	now = time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 0, 0, time.UTC)
	notifier := &fakeNotifier{}
	executor := New(Dependencies{
		Repository:    database,
		Notifications: notifier,
		TryLock: func(ctx context.Context, subscriptionID int64) (Lock, bool, error) {
			lock, acquired, lockErr := database.TryScheduledSummaryLock(ctx, subscriptionID)
			if lock == nil {
				return nil, acquired, lockErr
			}
			return lock, acquired, lockErr
		},
		Now: func() time.Time { return now },
	})
	result, err := executor.SendDue(context.Background(), 100)
	if err != nil {
		t.Fatalf("SendDue() error = %v", err)
	}
	if result.Sent != 1 || len(result.Errors) != 0 || len(notifier.messages) != 1 {
		t.Fatalf("SendDue() result/messages = %#v / %#v", result, notifier.messages)
	}
	message := notifier.messages[0]
	if message.chatID != userID ||
		message.chatType != "private" ||
		!strings.Contains(message.text, "Acceptance") ||
		!strings.Contains(message.text, "Balance change") {
		t.Fatalf("unexpected summary message: %#v", message)
	}

	active, err := database.GetActiveSubscription(context.Background(), userID)
	if err != nil || active == nil {
		t.Fatalf("GetActiveSubscription() = %#v, %v", active, err)
	}
	if active.ID != subscription.ID ||
		active.DailySummaryLastSentDate != now.Format("2006-01-02") ||
		active.ScheduledPushLastSentAt == nil {
		t.Fatalf("summary delivery was not persisted: %#v", active)
	}
}
