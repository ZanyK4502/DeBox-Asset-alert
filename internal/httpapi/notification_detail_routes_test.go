package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/auth"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdetail"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const testNotificationDetailID = "nd_0123456789abcdef0123456789abcdef01234567"

type fakeNotificationDetailService struct {
	detail  notificationdetail.Detail
	err     error
	gotUser string
	gotID   string
	calls   int
}

func (f *fakeNotificationDetailService) Detail(
	_ context.Context,
	deboxUserID string,
	notificationID string,
) (notificationdetail.Detail, error) {
	f.calls++
	f.gotUser = deboxUserID
	f.gotID = notificationID
	return f.detail, f.err
}

func TestGetNotificationDetailUsesAuthenticatedUserAndUnifiedContract(t *testing.T) {
	service := &fakeNotificationDetailService{detail: notificationdetail.Detail{
		SchemaVersion:    1,
		NotificationID:   testNotificationDetailID,
		NotificationKind: store.NotificationKindAddressRealtime,
		Domain:           "address",
		DeliveryMode:     "realtime",
		AccessScope:      "private",
		Language:         "zh",
		NotificationText: "通知正文",
		Data:             json.RawMessage(`{"schema_version":1}`),
		CopyValues:       []notificationdetail.CopyValue{},
		Links:            []notificationdetail.Link{},
		CreatedAt:        time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC),
		ExpiresAt:        time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC),
	}}
	handler := New(testConfig(t), Dependencies{
		Auth: &fakeAuthService{session: &store.AuthSession{
			DeBoxUserID: "user-1",
		}},
		NotificationDetails: service,
	})
	recorder := performNotificationDetailRequest(handler)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	if service.calls != 1 || service.gotUser != "user-1" || service.gotID != testNotificationDetailID {
		t.Fatalf("service calls/input = %d/%q/%q", service.calls, service.gotUser, service.gotID)
	}
	if recorder.Header().Get("Cache-Control") != "private, no-store" ||
		recorder.Header().Get("Vary") != "Cookie" {
		t.Fatalf("privacy headers = %#v", recorder.Header())
	}
	var response notificationdetail.Detail
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.SchemaVersion != 1 || response.NotificationID != testNotificationDetailID ||
		response.NotificationKind != store.NotificationKindAddressRealtime ||
		response.Domain != "address" || response.DeliveryMode != "realtime" {
		t.Fatalf("response = %#v", response)
	}
}

func TestGetNotificationDetailRequiresAuthentication(t *testing.T) {
	service := &fakeNotificationDetailService{}
	handler := New(testConfig(t), Dependencies{
		Auth:                &fakeAuthService{},
		NotificationDetails: service,
	})
	recorder := performNotificationDetailRequest(handler)
	if recorder.Code != http.StatusUnauthorized || service.calls != 0 {
		t.Fatalf("status/calls/body = %d/%d/%s", recorder.Code, service.calls, recorder.Body)
	}
}

func TestGetNotificationDetailMapsExpectedErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantDetail string
	}{
		{"invalid", notificationdetail.ErrInvalidNotificationID, http.StatusBadRequest, "通知 ID 格式不正确。"},
		{"missing", notificationdetail.ErrNotificationNotFound, http.StatusNotFound, "未找到这条通知详情。"},
		{"expired", notificationdetail.ErrNotificationExpired, http.StatusGone, "通知详情已过期。"},
		{"internal", errors.New("database password leaked"), http.StatusInternalServerError, "通知详情暂时无法读取。"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			service := &fakeNotificationDetailService{err: test.err}
			handler := New(testConfig(t), Dependencies{
				Auth: &fakeAuthService{session: &store.AuthSession{
					DeBoxUserID: "user-1",
				}},
				NotificationDetails: service,
			})
			recorder := performNotificationDetailRequest(handler)
			if recorder.Code != test.wantStatus || !strings.Contains(recorder.Body.String(), test.wantDetail) {
				t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body)
			}
			if strings.Contains(recorder.Body.String(), "database password") {
				t.Fatalf("internal error leaked: %s", recorder.Body)
			}
		})
	}
}

func TestGetNotificationDetailReportsUnavailableDependency(t *testing.T) {
	handler := New(testConfig(t), Dependencies{
		Auth: &fakeAuthService{session: &store.AuthSession{
			DeBoxUserID: "user-1",
		}},
	})
	recorder := performNotificationDetailRequest(handler)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
}

func performNotificationDetailRequest(handler http.Handler) *httptest.ResponseRecorder {
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/notification-details/"+testNotificationDetailID,
		nil,
	)
	request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "session-token"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
