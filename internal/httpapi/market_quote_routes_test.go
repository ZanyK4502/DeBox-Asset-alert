package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketrules"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketview"
)

type legacyQuoteRecommendationService struct {
	MarketManagementService
	calls int
}

func (s *legacyQuoteRecommendationService) Recommendations(context.Context, string, int64) ([]marketrules.Recommendation, error) {
	s.calls++
	return []marketrules.Recommendation{}, nil
}
func (s *legacyQuoteRecommendationService) PreviewRecommendations(context.Context, string, marketview.RecommendationPreviewInput) ([]marketrules.Recommendation, error) {
	s.calls++
	return []marketrules.Recommendation{}, nil
}

type quoteRecommendationService struct {
	legacyQuoteRecommendationService
	result marketview.RecommendationResult
	err    error
	users  []string
}

func (s *quoteRecommendationService) RecommendationsWithQuote(_ context.Context, user string, id int64) (marketview.RecommendationResult, error) {
	if id != 7 {
		return marketview.RecommendationResult{}, errors.New("unexpected project id")
	}
	s.users = append(s.users, user)
	return s.result, s.err
}
func (s *quoteRecommendationService) PreviewRecommendationsWithQuote(_ context.Context, user string, _ marketview.RecommendationPreviewInput) (marketview.RecommendationResult, error) {
	s.users = append(s.users, user)
	return s.result, s.err
}

func TestMarketRecommendationRoutesCarryExactQuoteEvidence(t *testing.T) {
	fetched, generated := time.Now().UTC().Add(-time.Minute), time.Now().UTC()
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/market/projects/7/recommendations", ""},
		{http.MethodPost, "/api/market/recommendations/preview", `{"deployments":[]}`},
	} {
		t.Run(route.method, func(t *testing.T) {
			service := &quoteRecommendationService{result: marketview.RecommendationResult{
				Recommendations: []marketrules.Recommendation{}, GeneratedAt: generated, QuoteMaxAgeSeconds: 120,
				QuoteQuality: marketquality.Quote{Status: marketquality.StatusCached, FetchedAt: &fetched},
			}}
			h := New(testConfig(t), Dependencies{Auth: maintenanceSession(), Market: service})
			response := maintenanceRequest(h, route.method, route.path, route.body)
			var result marketview.RecommendationResult
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.QuoteQuality.FetchedAt == nil || !result.QuoteQuality.FetchedAt.Equal(fetched) || !result.GeneratedAt.Equal(generated) || result.QuoteQuality.Status != marketquality.StatusCached || result.QuoteMaxAgeSeconds != 120 {
				t.Fatalf("quote response=%d/%s", response.Code, response.Body)
			}
			if len(service.users) != 1 || service.users[0] != "session-user" || service.calls != 0 {
				t.Fatalf("quality adapter/identity=%+v", service)
			}
			service.err = fmt.Errorf("wrapped: %w", marketview.ErrQuoteUnavailable)
			response = maintenanceRequest(h, route.method, route.path, route.body)
			var failure map[string]string
			if response.Code != http.StatusServiceUnavailable || json.Unmarshal(response.Body.Bytes(), &failure) != nil || failure["code"] != "quote_unavailable" || failure["detail"] == "" {
				t.Fatalf("quote failure=%d/%s", response.Code, response.Body)
			}
		})
	}
}

func TestMarketQuoteUnavailableKeepsAuthenticationAndOtherErrors(t *testing.T) {
	service := &quoteRecommendationService{err: marketview.ErrQuoteUnavailable}
	h := New(testConfig(t), Dependencies{Auth: &fakeAuthService{}, Market: service})
	response := maintenanceRequest(h, http.MethodGet, "/api/market/projects/7/recommendations", "")
	if response.Code != http.StatusUnauthorized || len(service.users) != 0 {
		t.Fatalf("authentication=%d calls=%d", response.Code, len(service.users))
	}
	for _, failure := range []error{marketview.ErrMarketDataUnavailable, errors.New("ordinary failure")} {
		r := maintenanceRequest(New(testConfig(t), Dependencies{Auth: maintenanceSession(), Market: &quoteRecommendationService{err: failure}}), http.MethodGet, "/api/market/projects/7/recommendations", "")
		var body map[string]string
		_ = json.Unmarshal(r.Body.Bytes(), &body)
		if body["code"] == "quote_unavailable" {
			t.Fatalf("ordinary error acquired quote code: %s", r.Body)
		}
	}
}

func TestMarketRecommendationLegacyAdaptersRemainCompatible(t *testing.T) {
	service := &legacyQuoteRecommendationService{}
	h := New(testConfig(t), Dependencies{Auth: maintenanceSession(), Market: service})
	response := maintenanceRequest(h, http.MethodGet, "/api/market/projects/7/recommendations", "")
	if response.Code != http.StatusOK || service.calls != 1 {
		t.Fatalf("legacy adapter=%d calls=%d", response.Code, service.calls)
	}
}

var _ marketRecommendationQualityService = (*marketview.Service)(nil)
