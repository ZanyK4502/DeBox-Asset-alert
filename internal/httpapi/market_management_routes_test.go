package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/auth"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketview"
)

func TestMarketManagementRoutesRequireAuthenticatedSession(t *testing.T) {
	handler := New(testConfig(t), Dependencies{Auth: &fakeAuthService{}})
	tests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/market/catalog", ""},
		{http.MethodPost, "/api/market/query", `{}`},
		{http.MethodPost, "/api/market/recommendations/preview", `{}`},
		{http.MethodGet, "/api/market/projects", ""},
		{http.MethodPost, "/api/market/projects", `{}`},
		{http.MethodGet, "/api/market/projects/1", ""},
		{http.MethodDelete, "/api/market/projects/1", ""},
		{http.MethodDelete, "/api/market/projects/1/permanent", ""},
		{http.MethodPost, "/api/market/projects/1/restore", `{}`},
		{http.MethodPatch, "/api/market/projects/1/pool", `{}`},
		{http.MethodGet, "/api/market/projects/1/recommendations", ""},
		{http.MethodGet, "/api/market/projects/1/events", ""},
		{http.MethodPost, "/api/market/projects/1/rules", `{}`},
		{http.MethodDelete, "/api/market/rules/1", ""},
		{http.MethodPost, "/api/market/rules/1/restore", `{}`},
		{http.MethodGet, "/api/market/combinations", ""},
		{http.MethodPost, "/api/market/combinations", `{}`},
		{http.MethodDelete, "/api/market/combinations/1", ""},
		{http.MethodDelete, "/api/market/combinations/1/permanent", ""},
		{http.MethodPost, "/api/market/combinations/1/restore", `{}`},
		{http.MethodPost, "/api/market/projects/1/labels", `{}`},
		{http.MethodDelete, "/api/market/labels/1", ""},
	}

	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			request := httptest.NewRequest(
				test.method,
				test.path,
				strings.NewReader(test.body),
			)
			if test.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf(
					"status = %d, want %d, body = %s",
					recorder.Code,
					http.StatusUnauthorized,
					recorder.Body,
				)
			}
			if !strings.Contains(recorder.Body.String(), auth.CookieName) &&
				!strings.Contains(recorder.Body.String(), "登录状态已失效") {
				t.Fatalf("unexpected unauthorized response: %s", recorder.Body)
			}
		})
	}
}

func TestWriteMarketErrorMapsTemporaryProviderFailure(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeMarketError(recorder, marketview.ErrMarketDataUnavailable)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf(
			"status = %d, want %d",
			recorder.Code,
			http.StatusServiceUnavailable,
		)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "行情数据服务暂时繁忙") ||
		strings.Contains(strings.ToLower(body), "dexscreener") {
		t.Fatalf("unexpected response body: %s", body)
	}
}

func TestWriteMarketErrorMapsPaidMarketAccessToForbidden(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeMarketError(recorder, marketview.ErrPaidMarketAccessRequired)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf(
			"status = %d, want %d",
			recorder.Code,
			http.StatusForbidden,
		)
	}
}
