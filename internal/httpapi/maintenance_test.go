package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/auth"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/management"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketview"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func (f *fakeManagementService) DisableSummarySettings(_ context.Context, userID string, input management.SummarySettingsInput) (management.SummarySettingsResult, error) {
	f.record("disable-summary", userID)
	f.summaryInput = input
	return management.SummarySettingsResult{}, nil
}

type unreadMaintenanceBody struct{}

func (unreadMaintenanceBody) Read([]byte) (int, error) {
	panic("a blocked configuration request must not read its body")
}

type unusedMaintenanceAuth struct{ AuthService }

func (unusedMaintenanceAuth) AuthenticatedSession(context.Context, string) (*store.AuthSession, error) {
	panic("a blocked configuration request must not access the session store")
}

func TestMaintenanceBlocksConfigurationRoutesBeforeSideEffects(t *testing.T) {
	h := New(testConfig(t), Dependencies{UserWriteMaintenance: true, Auth: unusedMaintenanceAuth{}})
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/payment/prepare"},
		{http.MethodPost, "/api/watch-rules"},
		{http.MethodPost, "/api/watch-rules/7/free-monitor"},
		{http.MethodPost, "/api/watch-rules/7/restore"},
		{http.MethodPost, "/api/combination-rules"},
		{http.MethodPost, "/api/combination-rules/7/restore"},
		{http.MethodPost, "/api/notification-groups"},
		{http.MethodPost, "/api/market/projects"},
		{http.MethodPost, "/api/market/projects/7/restore"},
		{http.MethodPatch, "/api/market/projects/7/pool"},
		{http.MethodPost, "/api/market/projects/7/rules"},
		{http.MethodPost, "/api/market/rules/7/restore"},
		{http.MethodPost, "/api/market/combinations"},
		{http.MethodPost, "/api/market/combinations/7/restore"},
		{http.MethodPost, "/api/market/projects/7/labels"},
		{http.MethodDelete, "/api/market/labels/7"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			request := httptest.NewRequest(route.method, route.path, unreadMaintenanceBody{})
			request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "session-token"})
			response := httptest.NewRecorder()
			h.ServeHTTP(response, request)
			assertMaintenanceResponse(t, response)
		})
	}
}

func assertMaintenanceResponse(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode maintenance response: %v", err)
	}
	if response.Code != http.StatusServiceUnavailable || body["code"] != "maintenance" || body["detail"] == "" {
		t.Fatalf("maintenance response = %d/%v", response.Code, body)
	}
}

func maintenanceSession() *fakeAuthService {
	return &fakeAuthService{session: &store.AuthSession{
		DeBoxUserID: "session-user", WalletAddress: "0x1111111111111111111111111111111111111111",
		ExpiresAt: time.Now().Add(time.Hour),
	}}
}

func maintenanceRequest(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "session-token"})
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	return response
}

func TestMaintenancePreservesPaymentVerificationAndReadStopRoutes(t *testing.T) {
	payments := &fakePaymentService{}
	managementService := &fakeManagementService{}
	subscriptions := &fakeSubscriptionService{}
	webhook := &marketWebhookStub{}
	h := New(testConfig(t), Dependencies{
		UserWriteMaintenance: true, Auth: maintenanceSession(), Payments: payments,
		Management: managementService, Subscriptions: subscriptions, MarketWebhook: webhook,
	})
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/ready", ""},
		{http.MethodGet, "/api/health", ""},
		{http.MethodGet, "/api/auth/session", ""},
		{http.MethodPost, "/api/auth/challenge", `{"wallet_address":"0x1111111111111111111111111111111111111111"}`},
		{http.MethodPost, "/api/auth/verify", `{"challenge_id":"challenge-1","wallet_address":"0x1111111111111111111111111111111111111111","signature":"signed"}`},
		{http.MethodGet, "/api/subscription/current", ""},
		{http.MethodPost, "/api/subscription/free-trial", `{}`},
		{http.MethodGet, "/api/watch-rules", ""},
		{http.MethodGet, "/api/combination-rules", ""},
		{http.MethodGet, "/api/notification-groups", ""},
		{http.MethodGet, "/api/payment/config", ""},
		{http.MethodPost, "/api/payment/verify", `{"order_id":7,"tx_hash":"existing-transaction"}`},
		{http.MethodDelete, "/api/watch-rules/7", ""},
		{http.MethodDelete, "/api/watch-rules/paused", ""},
		{http.MethodDelete, "/api/combination-rules/7", ""},
		{http.MethodDelete, "/api/notification-groups/7", ""},
		{http.MethodPatch, "/api/watch-rules/7/notification-language", `{"language":"en"}`},
		{http.MethodPost, "/api/market/webhook/bsc/swap", `{"events":[]}`},
		{http.MethodPost, "/api/auth/logout", ""},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			response := maintenanceRequest(h, route.method, route.path, route.body)
			if response.Code != http.StatusOK {
				t.Fatalf("allowed route = %d/%s", response.Code, response.Body)
			}
		})
	}
	if payments.verifyOrderID != 7 || payments.verifyInput[1] != "session-user" || subscriptions.bindingInput[0] != "session-user" || webhook.chain != "bsc" {
		t.Fatal("maintenance bypassed the existing identity/payment/subscription/webhook handlers")
	}
}

type maintenanceMarketService struct {
	MarketManagementService
	users []string
}

func (f *maintenanceMarketService) ListProjects(_ context.Context, user string, _ bool) ([]store.MarketProject, error) {
	f.users = append(f.users, user)
	return []store.MarketProject{}, nil
}
func (f *maintenanceMarketService) ArchiveProject(_ context.Context, user string, id int64) (store.MarketProject, error) {
	f.users = append(f.users, user)
	return store.MarketProject{ID: id}, nil
}
func (f *maintenanceMarketService) DeleteArchivedProject(_ context.Context, user string, _ int64) error {
	f.users = append(f.users, user)
	return nil
}
func (f *maintenanceMarketService) DeleteRule(_ context.Context, user string, _ int64) error {
	f.users = append(f.users, user)
	return nil
}
func (f *maintenanceMarketService) ArchiveCombination(_ context.Context, user string, _ int64) error {
	f.users = append(f.users, user)
	return nil
}
func (f *maintenanceMarketService) DeletePausedCombination(_ context.Context, user string, _ int64) error {
	f.users = append(f.users, user)
	return nil
}
func (f *maintenanceMarketService) QueryToken(_ context.Context, user string, _ marketview.TokenQueryInput) (marketview.TokenQueryResult, error) {
	f.users = append(f.users, user)
	return marketview.TokenQueryResult{}, nil
}

func TestMaintenancePreservesMarketViewingAndStopDeleteRoutes(t *testing.T) {
	market := &maintenanceMarketService{}
	h := New(testConfig(t), Dependencies{UserWriteMaintenance: true, Auth: maintenanceSession(), Market: market})
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/market/projects", ""},
		{http.MethodPost, "/api/market/query", `{}`},
		{http.MethodDelete, "/api/market/projects/7", ""},
		{http.MethodDelete, "/api/market/projects/7/permanent", ""},
		{http.MethodDelete, "/api/market/rules/7", ""},
		{http.MethodDelete, "/api/market/combinations/7", ""},
		{http.MethodDelete, "/api/market/combinations/7/permanent", ""},
	} {
		response := maintenanceRequest(h, route.method, route.path, route.body)
		if response.Code != http.StatusOK && response.Code != http.StatusNoContent {
			t.Fatalf("%s %s = %d/%s", route.method, route.path, response.Code, response.Body)
		}
	}
	if len(market.users) != 7 {
		t.Fatalf("market calls = %d, want 7", len(market.users))
	}
	for _, user := range market.users {
		if user != "session-user" {
			t.Fatalf("market actor = %q", user)
		}
	}
}

func TestMaintenanceSummaryRequiresExplicitStopAndRejectsEnabledTargets(t *testing.T) {
	for _, body := range []string{
		`{}`, `null`, `{"enabled":null}`, `{"enabled":true}`, `{"targets":[]}`,
		`{"enabled":false,"targets":[{"chat_type":"private","enabled":true}]}`,
		`{"enabled":true,"targets":[{"chat_type":"private","enabled":false}]}`,
		`{"targets":[{"chat_type":"private"}]}`,
		`{"targets":[{"enabled":false},{"enabled":true}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			managementService := &fakeManagementService{}
			h := New(testConfig(t), Dependencies{UserWriteMaintenance: true, Auth: maintenanceSession(), Management: managementService})
			response := maintenanceRequest(h, http.MethodPost, "/api/subscription/summary-settings", body)
			assertMaintenanceResponse(t, response)
			if len(managementService.calls) != 0 {
				t.Fatal("a rejected settings request reached the management service")
			}
		})
	}
	for _, body := range []string{`{"enabled":false}`, `{"targets":[{"chat_type":"private","enabled":false}]}`} {
		managementService := &fakeManagementService{}
		h := New(testConfig(t), Dependencies{UserWriteMaintenance: true, Auth: maintenanceSession(), Management: managementService})
		response := maintenanceRequest(h, http.MethodPost, "/api/subscription/summary-settings", body)
		if response.Code != http.StatusOK || len(managementService.calls) != 1 || managementService.calls[0] != "disable-summary" || managementService.users[0] != "session-user" {
			t.Fatalf("explicit stop = %d/%v", response.Code, managementService.calls)
		}
		if managementService.summaryInput.PushTime != "" || managementService.summaryInput.Timezone != "" || managementService.summaryInput.Language != "" {
			t.Fatal("maintenance stop applied scheduling defaults")
		}
	}
}

type failingSummaryStop struct {
	*fakeManagementService
	err error
}

func (f failingSummaryStop) DisableSummarySettings(context.Context, string, management.SummarySettingsInput) (management.SummarySettingsResult, error) {
	return management.SummarySettingsResult{}, f.err
}

func TestMaintenanceSummaryPreservesAuthenticationAndMapsConfigurationChanges(t *testing.T) {
	managementService := &fakeManagementService{}
	h := New(testConfig(t), Dependencies{UserWriteMaintenance: true, Auth: &fakeAuthService{}, Management: managementService})
	response := maintenanceRequest(h, http.MethodPost, "/api/subscription/summary-settings", `{"enabled":false}`)
	if response.Code != http.StatusUnauthorized || len(managementService.calls) != 0 {
		t.Fatalf("unauthenticated stop = %d/%v", response.Code, managementService.calls)
	}
	for _, test := range []struct {
		err    error
		status int
	}{
		{management.ErrSummaryMaintenanceChange, http.StatusServiceUnavailable},
		{store.ErrNotFound, http.StatusBadRequest},
	} {
		h = New(testConfig(t), Dependencies{
			UserWriteMaintenance: true, Auth: maintenanceSession(),
			Management: failingSummaryStop{fakeManagementService: managementService, err: test.err},
		})
		response = maintenanceRequest(h, http.MethodPost, "/api/subscription/summary-settings", `{"enabled":false}`)
		if response.Code != test.status {
			t.Fatalf("summary error %v = %d/%s", test.err, response.Code, response.Body)
		}
		if test.status == http.StatusServiceUnavailable {
			assertMaintenanceResponse(t, response)
		}
	}
	h = New(testConfig(t), Dependencies{UserWriteMaintenance: true, Auth: maintenanceSession(), Management: managementService})
	response = maintenanceRequest(h, http.MethodPost, "/api/subscription/summary-settings", `{"enabled":false} {}`)
	if response.Code != http.StatusBadRequest || len(managementService.calls) != 0 {
		t.Fatalf("multiple JSON values = %d/%v", response.Code, managementService.calls)
	}
}

func TestMaintenanceDisabledRestoresOriginalWriteRoutes(t *testing.T) {
	payments := &fakePaymentService{}
	managementService := &fakeManagementService{}
	h := New(testConfig(t), Dependencies{Auth: maintenanceSession(), Management: managementService, Payments: payments})
	response := maintenanceRequest(h, http.MethodPost, "/api/payment/prepare", `{"plan_code":"professional","billing_cycle":"quarterly"}`)
	if response.Code != http.StatusOK || payments.prepareInput[0] != "session-user" || payments.prepareInput[3] != "quarterly" {
		t.Fatalf("original prepare route = %d/%v", response.Code, payments.prepareInput)
	}
	response = maintenanceRequest(h, http.MethodPost, "/api/subscription/summary-settings", `{}`)
	if response.Code != http.StatusOK || len(managementService.calls) != 1 || managementService.calls[0] != "summary" || !managementService.summaryInput.Enabled || managementService.summaryInput.PushTime != "20:00" {
		t.Fatalf("original summary route = %d/%+v", response.Code, managementService.summaryInput)
	}
}
