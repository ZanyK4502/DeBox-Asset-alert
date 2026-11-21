package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/management"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketview"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/payment"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/subscription"
)

type capacityPaymentService struct {
	*fakePaymentService
	err error
}

func (f capacityPaymentService) Prepare(context.Context, string, string, string, string) (payment.PrepareResult, error) {
	return payment.PrepareResult{}, f.err
}

type capacityManagementService struct {
	*fakeManagementService
	err error
}

func (f capacityManagementService) CreateWatchRule(context.Context, string, management.WatchRuleInput) (management.WatchRuleCreation, error) {
	return management.WatchRuleCreation{}, f.err
}

func (f capacityManagementService) RestoreWatchRule(context.Context, string, int64) (subscription.Entitlement, error) {
	return subscription.Entitlement{}, f.err
}

func (f capacityManagementService) CreateCombinationRule(context.Context, string, management.CombinationRuleInput) (management.CombinationRuleCreation, error) {
	return management.CombinationRuleCreation{}, f.err
}

type capacityMarketService struct {
	MarketManagementService
	err error
}

func (f capacityMarketService) CreateProject(context.Context, string, marketview.CreateProjectInput) (marketview.ProjectDetail, error) {
	return marketview.ProjectDetail{}, f.err
}

func (f capacityMarketService) RestoreProject(context.Context, string, int64) (store.MarketProject, error) {
	return store.MarketProject{}, f.err
}

func (f capacityMarketService) CreateRule(context.Context, string, int64, marketview.CreateRuleInput) (store.MarketRule, error) {
	return store.MarketRule{}, f.err
}

func (f capacityMarketService) RestoreRule(context.Context, string, int64) (store.MarketRule, error) {
	return store.MarketRule{}, f.err
}

func TestFixedAdmissionLimitsReachPaymentAndMarketClients(t *testing.T) {
	h := New(testConfig(t), Dependencies{
		Auth:     maintenanceSession(),
		Payments: capacityPaymentService{fakePaymentService: &fakePaymentService{}, err: fmt.Errorf("admission: %w", store.ErrSubscriptionSeatsReached)},
		Market:   capacityMarketService{err: fmt.Errorf("admission: %w", store.ErrMarketHolderTargetsReached)},
	})
	for path, code := range map[string]string{
		"/api/payment/prepare":         "subscription_seats_full",
		"/api/market/projects/7/rules": "holder_targets_full",
		"/api/market/rules/7/restore":  "holder_targets_full",
	} {
		t.Run(path, func(t *testing.T) {
			r := maintenanceRequest(h, http.MethodPost, path, `{}`)
			var body map[string]string
			if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if r.Code != http.StatusConflict || body["code"] != code || body["detail"] == "" || strings.Contains(body["detail"], "admission:") {
				t.Fatalf("response=%d %v, expected a localized conflict with code %q", r.Code, body, code)
			}
		})
	}
}

func TestResourceCapacityTypedErrorsReachPaymentWalletAndMarketClients(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"not_ready", fmt.Errorf("admission: %w", store.ErrResourceCapacityUnavailable), http.StatusServiceUnavailable, "capacity_not_ready"},
		{"full", fmt.Errorf("admission: %w", store.ErrResourceCapacityReached), http.StatusConflict, "capacity_full"},
		{"invalid_state", fmt.Errorf("aggregate: %w", store.ErrResourceCapacityState), http.StatusInternalServerError, ""},
		{"wrapped_db_failure", errors.Join(store.ErrResourceCapacityState, errors.New("database unavailable")), http.StatusInternalServerError, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := New(testConfig(t), Dependencies{Auth: maintenanceSession(),
				Payments:   capacityPaymentService{fakePaymentService: &fakePaymentService{}, err: test.err},
				Management: capacityManagementService{fakeManagementService: &fakeManagementService{}, err: test.err},
				Market:     capacityMarketService{err: test.err}})
			for _, requestPath := range []string{"/api/payment/prepare", "/api/watch-rules", "/api/watch-rules/7/restore", "/api/combination-rules", "/api/market/projects", "/api/market/projects/7/restore"} {
				t.Run(requestPath, func(t *testing.T) {
					r := maintenanceRequest(h, http.MethodPost, requestPath, `{}`)
					assertCapacityResponse(t, r, test.status, test.code)
				})
			}
		})
	}
}

func assertCapacityResponse(t *testing.T, r *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if r.Code != status || body["code"] != code || body["detail"] == "" {
		t.Fatalf("capacity response=%d/%v, want=%d/%q", r.Code, body, status, code)
	}
	if code != "" && (!strings.Contains(body["detail"], "已有监控继续运行") || strings.Contains(body["detail"], "admission:")) {
		t.Fatalf("capacity message exposed internals or omitted existing monitoring: %v", body)
	}
	if code == "" {
		if _, exists := body["code"]; exists {
			t.Fatalf("state failure was disguised as an admission refusal: %v", body)
		}
	}
}

func TestResourceCapacityStateOverridesNormalCapacityAndQuoteErrors(t *testing.T) {
	for _, writer := range []func(http.ResponseWriter, error){
		func(w http.ResponseWriter, err error) { writeError(w, http.StatusBadRequest, err) },
		writeMarketError,
	} {
		r := httptest.NewRecorder()
		writer(r, errors.Join(store.ErrResourceCapacityState, store.ErrResourceCapacityUnavailable, marketview.ErrQuoteUnavailable))
		assertCapacityResponse(t, r, http.StatusInternalServerError, "")
	}
	r := httptest.NewRecorder()
	writeError(r, http.StatusBadRequest, errors.New("original business validation"))
	if r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), "original business validation") {
		t.Fatalf("ordinary business validation changed: %d/%s", r.Code, r.Body)
	}
}

func TestResourceCapacityResponseMappingDoesNotGateExistingPaymentVerification(t *testing.T) {
	payments := &fakePaymentService{}
	h := New(testConfig(t), Dependencies{Auth: maintenanceSession(), UserWriteMaintenance: true,
		Payments: capacityPaymentService{fakePaymentService: payments, err: store.ErrResourceCapacityUnavailable}})
	r := maintenanceRequest(h, http.MethodPost, "/api/payment/verify", `{"order_id":7,"tx_hash":"0x`+strings.Repeat("ab", 32)+`"}`)
	if r.Code != http.StatusOK || payments.verifyOrderID != 7 || !strings.Contains(r.Body.String(), `"payment_status":"confirming"`) {
		t.Fatalf("existing verification was blocked: %d/%s, order=%d", r.Code, r.Body, payments.verifyOrderID)
	}
}
