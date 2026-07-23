package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/auth"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/management"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/subscription"
)

type fakeManagementService struct {
	calls        []string
	users        []string
	groupWallet  string
	watchInput   management.WatchRuleInput
	summaryInput management.SummarySettingsInput
	groupInput   management.NotificationGroupInput
	language     string
	ids          []int64
}

func (f *fakeManagementService) record(call, userID string) {
	f.calls = append(f.calls, call)
	f.users = append(f.users, userID)
}

func (f *fakeManagementService) ListWatchRules(
	_ context.Context,
	userID string,
) ([]store.WatchRule, error) {
	f.record("list-rules", userID)
	return []store.WatchRule{}, nil
}

func (f *fakeManagementService) CreateWatchRule(
	_ context.Context,
	userID string,
	input management.WatchRuleInput,
) (management.WatchRuleCreation, error) {
	f.record("create-rule", userID)
	f.watchInput = input
	return management.WatchRuleCreation{}, nil
}

func (f *fakeManagementService) DeletePausedWatchRules(
	_ context.Context,
	userID string,
) (management.EntitlementResult, error) {
	f.record("delete-paused", userID)
	deleted := int64(0)
	return management.EntitlementResult{OK: true, Deleted: &deleted}, nil
}

func (f *fakeManagementService) DeleteWatchRule(
	_ context.Context,
	userID string,
	ruleID int64,
) (management.EntitlementResult, error) {
	f.record("delete-rule", userID)
	f.ids = append(f.ids, ruleID)
	return management.EntitlementResult{OK: true}, nil
}

func (f *fakeManagementService) ChooseFreeWatchRule(
	_ context.Context,
	userID string,
	ruleID int64,
) (subscription.Entitlement, error) {
	f.record("free-rule", userID)
	f.ids = append(f.ids, ruleID)
	return subscription.Entitlement{DeBoxUserID: userID}, nil
}

func (f *fakeManagementService) RestoreWatchRule(
	_ context.Context,
	userID string,
	ruleID int64,
) (subscription.Entitlement, error) {
	f.record("restore-rule", userID)
	f.ids = append(f.ids, ruleID)
	return subscription.Entitlement{DeBoxUserID: userID}, nil
}

func (f *fakeManagementService) UpdateWatchRuleLanguage(
	_ context.Context,
	userID string,
	ruleID int64,
	language string,
) (management.WatchRuleUpdate, error) {
	f.record("rule-language", userID)
	f.ids = append(f.ids, ruleID)
	f.language = language
	return management.WatchRuleUpdate{}, nil
}

func (f *fakeManagementService) SaveSummarySettings(
	_ context.Context,
	userID string,
	input management.SummarySettingsInput,
) (management.SummarySettingsResult, error) {
	f.record("summary", userID)
	f.summaryInput = input
	return management.SummarySettingsResult{}, nil
}

func (f *fakeManagementService) ListNotificationGroups(
	_ context.Context,
	userID string,
) ([]store.NotificationGroup, error) {
	f.record("list-groups", userID)
	return []store.NotificationGroup{}, nil
}

func (f *fakeManagementService) CreateNotificationGroup(
	_ context.Context,
	userID string,
	walletAddress string,
	input management.NotificationGroupInput,
) (management.NotificationGroupCreation, error) {
	f.record("create-group", userID)
	f.groupWallet = walletAddress
	f.groupInput = input
	return management.NotificationGroupCreation{}, nil
}

func (f *fakeManagementService) DeleteNotificationGroup(
	_ context.Context,
	userID string,
	groupID int64,
) (management.NotificationGroupDeletion, error) {
	f.record("delete-group", userID)
	f.ids = append(f.ids, groupID)
	return management.NotificationGroupDeletion{OK: true}, nil
}

func TestManagementRoutesUseAuthenticatedIdentityAndDefaults(t *testing.T) {
	wallet := "0x1111111111111111111111111111111111111111"
	authService := &fakeAuthService{session: &store.AuthSession{
		DeBoxUserID:   "user-1",
		WalletAddress: wallet,
		ExpiresAt:     time.Now().Add(time.Hour),
	}}
	managementService := &fakeManagementService{}
	handler := New(testConfig(t), Dependencies{
		Auth:       authService,
		Management: managementService,
	})

	tests := []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodPost, path: "/api/subscription/summary-settings", body: `{}`},
		{method: http.MethodGet, path: "/api/watch-rules"},
		{
			method: http.MethodPost,
			path:   "/api/watch-rules",
			body:   `{"wallet_address":"` + wallet + `"}`,
		},
		{method: http.MethodDelete, path: "/api/watch-rules/paused"},
		{method: http.MethodDelete, path: "/api/watch-rules/4"},
		{method: http.MethodPost, path: "/api/watch-rules/5/free-monitor"},
		{method: http.MethodPost, path: "/api/watch-rules/6/restore"},
		{
			method: http.MethodPatch,
			path:   "/api/watch-rules/7/notification-language",
			body:   `{"language":"en"}`,
		},
		{method: http.MethodGet, path: "/api/notification-groups"},
		{
			method: http.MethodPost,
			path:   "/api/notification-groups",
			body:   `{"gid":"https://m.debox.pro/group?id=group-1"}`,
		},
		{method: http.MethodDelete, path: "/api/notification-groups/8"},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "session-token"})
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s %s status = %d, body = %s",
				test.method,
				test.path,
				recorder.Code,
				recorder.Body,
			)
		}
	}
	if len(managementService.calls) != len(tests) {
		t.Fatalf("calls = %#v", managementService.calls)
	}
	for _, userID := range managementService.users {
		if userID != "user-1" {
			t.Fatalf("management user IDs = %#v", managementService.users)
		}
	}
	if managementService.groupWallet != wallet {
		t.Fatalf("group wallet = %q", managementService.groupWallet)
	}
	if managementService.watchInput.ChainKey != "bsc" ||
		managementService.watchInput.RuleType != "balance_change" ||
		managementService.watchInput.Threshold != "0" ||
		managementService.watchInput.NotificationChatType != "private" ||
		managementService.watchInput.NotificationLanguage != "zh" {
		t.Fatalf("watch defaults = %#v", managementService.watchInput)
	}
	if !managementService.summaryInput.Enabled ||
		managementService.summaryInput.PushTime != "20:00" ||
		managementService.summaryInput.Timezone != "Asia/Shanghai" ||
		managementService.summaryInput.ChatType != "private" ||
		managementService.summaryInput.Language != "zh" {
		t.Fatalf("summary defaults = %#v", managementService.summaryInput)
	}
	if managementService.language != "en" ||
		managementService.groupInput.Link != "https://m.debox.pro/group?id=group-1" {
		t.Fatalf("language/group input = %q / %#v", managementService.language, managementService.groupInput)
	}
}

func TestManagementRouteRejectsInvalidPathID(t *testing.T) {
	authService := &fakeAuthService{session: &store.AuthSession{
		DeBoxUserID:   "user-1",
		WalletAddress: "0x1111111111111111111111111111111111111111",
		ExpiresAt:     time.Now().Add(time.Hour),
	}}
	handler := New(testConfig(t), Dependencies{
		Auth:       authService,
		Management: &fakeManagementService{},
	})
	request := httptest.NewRequest(http.MethodDelete, "/api/watch-rules/not-a-number", nil)
	request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "session-token"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest ||
		!strings.Contains(recorder.Body.String(), "ID") {
		t.Fatalf("invalid ID response = %d/%s", recorder.Code, recorder.Body)
	}
}
