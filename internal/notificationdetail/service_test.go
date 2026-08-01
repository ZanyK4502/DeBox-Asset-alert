package notificationdetail

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const (
	testNotificationID = "nd_0123456789abcdef0123456789abcdef01234567"
	testWallet         = "0x1111111111111111111111111111111111111111"
	testTarget         = "0x2222222222222222222222222222222222222222"
	testToken          = "0x3333333333333333333333333333333333333333"
	testPool           = "0x4444444444444444444444444444444444444444"
	testTransaction    = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

type fakeRepository struct {
	snapshot *store.NotificationDetailSnapshot
	err      error
	gotID    string
}

func (f *fakeRepository) GetNotificationDetailSnapshot(
	_ context.Context,
	publicID string,
) (*store.NotificationDetailSnapshot, error) {
	f.gotID = publicID
	if f.err != nil || f.snapshot == nil {
		return nil, f.err
	}
	value := *f.snapshot
	return &value, nil
}

func TestPrivateNotificationDetailUsesSnapshotAndBuildsActions(t *testing.T) {
	t.Parallel()
	ruleID := int64(7)
	repository := &fakeRepository{snapshot: testSnapshot(
		store.NotificationKindAddressRealtime,
		"private",
		"user-1",
		json.RawMessage(`{
			"schema_version":1,
			"rule":{
				"id":7,
				"debox_user_id":"user-1",
				"chain_key":"bsc",
				"wallet_address":"`+testWallet+`",
				"token_address":"`+testToken+`",
				"target_address":"`+testTarget+`",
				"notification_chat_id":"user-1"
			},
			"alert_event":{"transaction_hash":"`+testTransaction+`"}
		}`),
	)}
	repository.snapshot.RuleID = &ruleID
	service := New(repository, Settings{
		PublicAppURL: "https://alerts.example/app#old",
		Now:          func() time.Time { return time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC) },
	})

	detail, err := service.Detail(context.Background(), "user-1", testNotificationID)
	if err != nil {
		t.Fatalf("Detail(): %v", err)
	}
	if repository.gotID != testNotificationID ||
		detail.SchemaVersion != 1 ||
		detail.NotificationKind != store.NotificationKindAddressRealtime ||
		detail.Domain != "address" || detail.DeliveryMode != "realtime" ||
		detail.AccessScope != "private" || detail.Rule.ID == nil ||
		*detail.Rule.ID != ruleID || detail.Rule.Threshold != "100" {
		t.Fatalf("detail = %#v", detail)
	}
	data := string(detail.Data)
	for _, expected := range []string{testWallet, testTarget, testToken, testTransaction} {
		if !strings.Contains(data, expected) {
			t.Fatalf("private data missing %q: %s", expected, data)
		}
	}
	for _, hidden := range []string{"debox_user_id", "notification_chat_id"} {
		if strings.Contains(data, hidden) {
			t.Fatalf("internal identity field leaked: %s", data)
		}
	}
	for _, expected := range []string{testWallet, testTarget, testToken, testTransaction} {
		if !containsCopyValue(detail.CopyValues, expected) {
			t.Fatalf("copy values missing %q: %#v", expected, detail.CopyValues)
		}
	}
	if !containsLink(detail.Links, "transaction", "https://bscscan.com/tx/"+testTransaction) {
		t.Fatalf("transaction links = %#v", detail.Links)
	}
	managementURL := "https://alerts.example/app?rule_id=7&rule_type=address#activeRulesSection"
	if !containsLink(detail.Links, "manage_rule", managementURL) {
		t.Fatalf("management links = %#v", detail.Links)
	}
}

func TestPrivateNotificationDetailHidesExistenceFromOtherUsers(t *testing.T) {
	t.Parallel()
	repository := &fakeRepository{snapshot: testSnapshot(
		store.NotificationKindAddressStage,
		"private",
		"owner",
		json.RawMessage(`{"schema_version":1}`),
	)}
	_, err := New(repository, Settings{}).Detail(
		context.Background(),
		"other-user",
		testNotificationID,
	)
	if !errors.Is(err, ErrNotificationNotFound) {
		t.Fatalf("error = %v", err)
	}
}

func TestExpiredPrivateNotificationDetailStillHidesExistenceFromOtherUsers(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	snapshot := testSnapshot(
		store.NotificationKindAddressStage,
		"private",
		"owner",
		json.RawMessage(`{"schema_version":1}`),
	)
	snapshot.ExpiresAt = now
	_, err := New(&fakeRepository{snapshot: snapshot}, Settings{
		Now: func() time.Time { return now },
	}).Detail(context.Background(), "other-user", testNotificationID)
	if !errors.Is(err, ErrNotificationNotFound) {
		t.Fatalf("error = %v", err)
	}
}

func TestGroupNotificationDetailIsCapabilityScopedAndPrivacyFiltered(t *testing.T) {
	t.Parallel()
	ruleID := int64(9)
	repository := &fakeRepository{snapshot: testSnapshot(
		store.NotificationKindMarketRealtime,
		"group",
		"owner",
		json.RawMessage(`{
			"schema_version":1,
			"note":"private wallet `+testWallet+`",
			"delivery":{
				"debox_user_id":"owner",
				"notification_chat_id":"group-secret",
				"address_label":"project treasury",
				"project":{"id":8,"chain_key":"base","token_address":"`+testToken+`"},
				"rule":{"id":9,"wallet_address":"`+testWallet+`","target_address":"`+testTarget+`"},
				"event":{
					"chain_key":"base",
					"wallet_address":"`+testWallet+`",
					"transaction_hash":"`+testTransaction+`",
					"metadata":{"holder_address":"`+testTarget+`","progress_percent":"85"}
				},
				"pool":{"pool_address":"`+testPool+`"}
			}
		}`),
	)}
	repository.snapshot.RuleID = &ruleID
	service := New(repository, Settings{
		PublicAppURL: "https://alerts.example",
		Now:          func() time.Time { return time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC) },
	})

	detail, err := service.Detail(context.Background(), "group-viewer", testNotificationID)
	if err != nil {
		t.Fatalf("Detail(): %v", err)
	}
	if detail.AccessScope != "group" || detail.Rule.ID != nil {
		t.Fatalf("group detail = %#v", detail)
	}
	data := string(detail.Data)
	for _, hidden := range []string{
		"debox_user_id", "notification_chat_id", "wallet_address",
		"target_address", "holder_address", "address_label", "private wallet",
		"\"id\":",
		testWallet, testTarget,
	} {
		if strings.Contains(data, hidden) {
			t.Fatalf("group data leaked %q: %s", hidden, data)
		}
	}
	for _, allowed := range []string{testToken, testPool, testTransaction, "progress_percent"} {
		if !strings.Contains(data, allowed) {
			t.Fatalf("group data removed public value %q: %s", allowed, data)
		}
	}
	if containsCopyValue(detail.CopyValues, testWallet) ||
		containsCopyValue(detail.CopyValues, testTarget) {
		t.Fatalf("group copy values leaked a private address: %#v", detail.CopyValues)
	}
	for _, allowed := range []string{testToken, testPool, testTransaction} {
		if !containsCopyValue(detail.CopyValues, allowed) {
			t.Fatalf("group copy values missing %q: %#v", allowed, detail.CopyValues)
		}
	}
	if !containsLink(detail.Links, "transaction", "https://basescan.org/tx/"+testTransaction) {
		t.Fatalf("group transaction links = %#v", detail.Links)
	}
	for _, link := range detail.Links {
		if strings.HasPrefix(link.Kind, "manage_") {
			t.Fatalf("group detail exposed owner management link: %#v", detail.Links)
		}
	}
}

func TestNotificationDetailClassifiesEverySnapshotKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kind   string
		domain string
		mode   string
	}{
		{store.NotificationKindAddressRealtime, "address", "realtime"},
		{store.NotificationKindAddressStage, "address", "stage"},
		{store.NotificationKindAddressCombination, "address", "combination"},
		{store.NotificationKindMarketRealtime, "market", "realtime"},
		{store.NotificationKindMarketStage, "market", "stage"},
		{store.NotificationKindMarketCombination, "market", "combination"},
		{store.NotificationKindDailySummary, "daily_summary", "summary"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.kind, func(t *testing.T) {
			t.Parallel()
			repository := &fakeRepository{snapshot: testSnapshot(
				test.kind, "private", "user-1", json.RawMessage(`{"schema_version":1}`),
			)}
			detail, err := New(repository, Settings{
				Now: func() time.Time {
					return time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
				},
			}).Detail(context.Background(), "user-1", testNotificationID)
			if err != nil || detail.Domain != test.domain || detail.DeliveryMode != test.mode ||
				detail.CopyValues == nil || detail.Links == nil {
				t.Fatalf("Detail() = %#v, %v", detail, err)
			}
		})
	}
}

func TestNotificationDetailMissingExpiredAndInvalidStates(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		userID     string
		publicID   string
		repository *fakeRepository
		want       error
	}{
		{
			name: "invalid", userID: "user-1", publicID: "invalid",
			repository: &fakeRepository{}, want: ErrInvalidNotificationID,
		},
		{
			name: "missing", userID: "user-1", publicID: testNotificationID,
			repository: &fakeRepository{}, want: ErrNotificationNotFound,
		},
		{
			name: "expired", userID: "user-1", publicID: testNotificationID,
			repository: &fakeRepository{snapshot: func() *store.NotificationDetailSnapshot {
				value := testSnapshot(
					store.NotificationKindDailySummary,
					"private",
					"user-1",
					json.RawMessage(`{"schema_version":1}`),
				)
				value.ExpiresAt = now
				return value
			}()},
			want: ErrNotificationExpired,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := New(test.repository, Settings{Now: func() time.Time { return now }}).
				Detail(context.Background(), test.userID, test.publicID)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestMarketRuleManagementLinkIncludesProjectAndRule(t *testing.T) {
	t.Parallel()
	ruleID := int64(19)
	repository := &fakeRepository{snapshot: testSnapshot(
		store.NotificationKindMarketStage,
		"private",
		"user-1",
		json.RawMessage(`{"delivery":{"project":{"id":23}}}`),
	)}
	repository.snapshot.RuleID = &ruleID
	detail, err := New(repository, Settings{
		PublicAppURL: "https://alerts.example/",
		Now:          func() time.Time { return time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC) },
	}).Detail(context.Background(), "user-1", testNotificationID)
	if err != nil {
		t.Fatalf("Detail(): %v", err)
	}
	want := "https://alerts.example/?project_id=23&rule_id=19&rule_type=market#marketProjectsSection"
	if !containsLink(detail.Links, "manage_rule", want) {
		t.Fatalf("links = %#v", detail.Links)
	}
}

func testSnapshot(
	kind string,
	chatType string,
	deboxUserID string,
	details json.RawMessage,
) *store.NotificationDetailSnapshot {
	createdAt := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	return &store.NotificationDetailSnapshot{
		ID:                   1,
		PublicID:             testNotificationID,
		NotificationKind:     kind,
		DeBoxUserID:          deboxUserID,
		RuleType:             "outgoing",
		RuleName:             "转出提醒",
		RuleThreshold:        "100",
		ActualValue:          "125",
		NotificationChatID:   "target-id",
		NotificationChatType: chatType,
		NotificationLanguage: "zh",
		NotificationLabel:    "通知目标",
		NotificationText:     "notification body",
		Details:              details,
		CreatedAt:            createdAt,
		ExpiresAt:            createdAt.Add(30 * 24 * time.Hour),
	}
}

func containsCopyValue(values []CopyValue, want string) bool {
	for _, value := range values {
		if value.Value == want {
			return true
		}
	}
	return false
}

func containsLink(links []Link, kind, target string) bool {
	for _, link := range links {
		if link.Kind == kind && link.URL == target {
			return true
		}
	}
	return false
}
