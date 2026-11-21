package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketview"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestMarketScopeTypedErrorsPreserveOriginalResources(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{store.ErrMarketChainNotAllowed, http.StatusForbidden, "market_chain_not_allowed"},
		{store.ErrMarketPoolLimitReached, http.StatusConflict, "market_pool_limit_reached"},
		{store.ErrMarketPoolMismatch, http.StatusBadRequest, "market_pool_scope_not_allowed"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			h := New(testConfig(t), Dependencies{Auth: maintenanceSession(), Market: capacityMarketService{err: fmt.Errorf("scope validation: %w", tc.err)}})
			for _, path := range []string{"/api/market/projects", "/api/market/projects/7/restore"} {
				r := maintenanceRequest(h, http.MethodPost, path, `{}`)
				var body map[string]string
				if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil || r.Code != tc.status || body["code"] != tc.code {
					t.Fatalf("scope response=%d/%s, error=%v", r.Code, r.Body, err)
				}
				if !strings.Contains(r.Body.String(), "保留") || strings.Contains(r.Body.String(), "scope validation") {
					t.Fatalf("scope response did not explain retained original state: %s", r.Body)
				}
			}
		})
	}
}

func TestMarketScopeResponseMappingKeepsStateFailureAndProviderSemantics(t *testing.T) {
	r := httptest.NewRecorder()
	writeMarketError(r, errors.Join(store.ErrResourceCapacityState, store.ErrMarketChainNotAllowed))
	assertCapacityResponse(t, r, http.StatusInternalServerError, "")
	r = httptest.NewRecorder()
	writeMarketError(r, marketview.ErrQuoteUnavailable)
	if r.Code != http.StatusServiceUnavailable || !strings.Contains(r.Body.String(), `"code":"quote_unavailable"`) {
		t.Fatalf("scope mapping changed read-only quote failure: %d/%s", r.Code, r.Body)
	}
}
