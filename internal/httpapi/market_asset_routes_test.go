package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/assetcatalog"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/auth"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type fakeAssetCatalog struct {
	searchQuery  string
	searchLimit  int
	resolve      [2]string
	result       assetcatalog.SearchResult
	candidate    *assetcatalog.Candidate
	manualInput  assetcatalog.ManualResolveInput
	manual       assetcatalog.ManualResolveResult
	verifyInput  assetcatalog.CrossChainVerifyInput
	verification assetcatalog.CrossChainVerificationResult
	logo         assetcatalog.Logo
	err          error
}

func (f *fakeAssetCatalog) Search(
	_ context.Context,
	query string,
	limit int,
) (assetcatalog.SearchResult, error) {
	f.searchQuery = query
	f.searchLimit = limit
	return f.result, f.err
}

func (f *fakeAssetCatalog) ResolveContract(
	_ context.Context,
	chainKey string,
	contract string,
) (*assetcatalog.Candidate, error) {
	f.resolve = [2]string{chainKey, contract}
	return f.candidate, f.err
}

func (f *fakeAssetCatalog) ResolveManualContracts(
	_ context.Context,
	input assetcatalog.ManualResolveInput,
) (assetcatalog.ManualResolveResult, error) {
	f.manualInput = input
	return f.manual, f.err
}

func (f *fakeAssetCatalog) VerifyCrossChainIdentity(
	_ context.Context,
	input assetcatalog.CrossChainVerifyInput,
) (assetcatalog.CrossChainVerificationResult, error) {
	f.verifyInput = input
	return f.verification, f.err
}

func (f *fakeAssetCatalog) Logo(
	context.Context,
	string,
) (assetcatalog.Logo, error) {
	return f.logo, f.err
}

func TestMarketAssetSearchRequiresSessionAndUsesCatalog(t *testing.T) {
	assets := &fakeAssetCatalog{result: assetcatalog.SearchResult{
		Query: "cake", Source: assetcatalog.SourceCoinGecko,
		Candidates: []assetcatalog.Candidate{{
			CanonicalAssetID: "pancakeswap-token",
		}},
	}}
	authService := &fakeAuthService{session: &store.AuthSession{
		DeBoxUserID: "user-1",
		ExpiresAt:   time.Now().Add(time.Hour),
	}}
	handler := New(testConfig(t), Dependencies{
		Auth: authService, Assets: assets,
	})

	unauthorized := httptest.NewRequest(
		http.MethodGet, "/api/market/assets/search?q=cake", nil,
	)
	unauthorizedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorizedRecorder.Code)
	}

	request := httptest.NewRequest(
		http.MethodGet, "/api/market/assets/search?q=cake&limit=8", nil,
	)
	request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "session-token"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK ||
		assets.searchQuery != "cake" ||
		assets.searchLimit != 8 {
		t.Fatalf(
			"status/query/limit = %d/%q/%d",
			recorder.Code, assets.searchQuery, assets.searchLimit,
		)
	}
}

func TestMarketAssetResolveAndErrorMapping(t *testing.T) {
	assets := &fakeAssetCatalog{candidate: &assetcatalog.Candidate{
		CanonicalAssetID: "usd-coin",
	}}
	handler := New(testConfig(t), Dependencies{
		Auth: &fakeAuthService{session: &store.AuthSession{
			DeBoxUserID: "user-1",
		}},
		Assets: assets,
	})
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/market/assets/resolve?chain=base&contract=0x1",
		nil,
	)
	request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "session-token"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK ||
		assets.resolve != [2]string{"base", "0x1"} {
		t.Fatalf("status/resolve = %d/%#v", recorder.Code, assets.resolve)
	}

	assets.err = errors.New("upstream included sensitive details")
	failed := httptest.NewRecorder()
	handler.ServeHTTP(failed, request)
	if failed.Code != http.StatusServiceUnavailable ||
		failed.Body.String() != `{"detail":"asset catalog temporarily unavailable"}`+"\n" {
		t.Fatalf("failed response = %d/%s", failed.Code, failed.Body.String())
	}
}

func TestMarketAssetLogoSetsSafeHeadersAndSupportsETag(t *testing.T) {
	assets := &fakeAssetCatalog{logo: assetcatalog.Logo{
		ContentType: "image/png", Body: []byte("png"), ETag: `"hash"`,
	}}
	handler := New(testConfig(t), Dependencies{
		Auth: &fakeAuthService{session: &store.AuthSession{
			DeBoxUserID: "user-1",
		}},
		Assets: assets,
	})
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/market/assets/logo?source=https%3A%2F%2Fcoin-images.coingecko.com%2Fcoin.png",
		nil,
	)
	request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "session-token"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK ||
		recorder.Header().Get("Content-Type") != "image/png" ||
		recorder.Header().Get("X-Content-Type-Options") != "nosniff" ||
		recorder.Header().Get("ETag") != `"hash"` {
		t.Fatalf("logo response = %d/%#v", recorder.Code, recorder.Header())
	}

	conditional := httptest.NewRequest(
		http.MethodGet, request.URL.String(), nil,
	)
	conditional.AddCookie(&http.Cookie{
		Name: auth.CookieName, Value: "session-token",
	})
	conditional.Header.Set("If-None-Match", `"hash"`)
	conditionalRecorder := httptest.NewRecorder()
	handler.ServeHTTP(conditionalRecorder, conditional)
	if conditionalRecorder.Code != http.StatusNotModified {
		t.Fatalf("conditional status = %d", conditionalRecorder.Code)
	}
}

func TestMarketAssetManualResolveRouteAndErrorMapping(t *testing.T) {
	assets := &fakeAssetCatalog{manual: assetcatalog.ManualResolveResult{
		CanMerge: true, MergeStatus: assetcatalog.MergeStatusSingleChain,
	}}
	handler := New(testConfig(t), Dependencies{
		Auth: &fakeAuthService{session: &store.AuthSession{
			DeBoxUserID: "user-1",
		}},
		Assets: assets,
	})
	request := func() *http.Request {
		value := httptest.NewRequest(
			http.MethodPost,
			"/api/market/assets/manual-resolve",
			strings.NewReader(
				`{"contracts":[{"chain_key":"base","contract_address":"0x0000000000000000000000000000000000000001"}]}`,
			),
		)
		value.AddCookie(&http.Cookie{
			Name: auth.CookieName, Value: "session-token",
		})
		return value
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request())
	if recorder.Code != http.StatusOK ||
		len(assets.manualInput.Contracts) != 1 ||
		assets.manualInput.Contracts[0].ChainKey != "base" {
		t.Fatalf(
			"manual route = %d/%#v",
			recorder.Code,
			assets.manualInput,
		)
	}

	assets.err = fmt.Errorf(
		"%w: duplicate chain",
		assetcatalog.ErrInvalidManualRequest,
	)
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, request())
	if invalid.Code != http.StatusBadRequest ||
		invalid.Body.String() !=
			`{"detail":"invalid manual contract request"}`+"\n" {
		t.Fatalf("invalid response = %d/%s", invalid.Code, invalid.Body.String())
	}

	assets.err = fmt.Errorf(
		"%w: item 1",
		assetcatalog.ErrContractUnreadable,
	)
	unreadable := httptest.NewRecorder()
	handler.ServeHTTP(unreadable, request())
	if unreadable.Code != http.StatusUnprocessableEntity ||
		unreadable.Body.String() !=
			`{"detail":"token contract metadata is unavailable"}`+"\n" {
		t.Fatalf(
			"unreadable response = %d/%s",
			unreadable.Code,
			unreadable.Body.String(),
		)
	}
}

func TestMarketAssetVerifyCrossChainRouteAndErrorMapping(t *testing.T) {
	assets := &fakeAssetCatalog{
		verification: assetcatalog.CrossChainVerificationResult{
			CanonicalAssetID:   "project-token",
			IdentitySource:     assetcatalog.SourceCoinGecko,
			VerificationStatus: assetcatalog.VerificationStatusVerified,
		},
	}
	handler := New(testConfig(t), Dependencies{
		Auth: &fakeAuthService{session: &store.AuthSession{
			DeBoxUserID: "user-1",
		}},
		Assets: assets,
	})
	request := func() *http.Request {
		value := httptest.NewRequest(
			http.MethodPost,
			"/api/market/assets/verify-cross-chain",
			strings.NewReader(`{
				"canonical_asset_id":"project-token",
				"contracts":[
					{"chain_key":"bsc","contract_address":"0x0000000000000000000000000000000000000001"},
					{"chain_key":"base","contract_address":"0x0000000000000000000000000000000000000002"}
				]
			}`),
		)
		value.AddCookie(&http.Cookie{
			Name: auth.CookieName, Value: "session-token",
		})
		return value
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request())
	if recorder.Code != http.StatusOK ||
		assets.verifyInput.CanonicalAssetID != "project-token" ||
		len(assets.verifyInput.Contracts) != 2 {
		t.Fatalf(
			"verify route = %d/%#v",
			recorder.Code,
			assets.verifyInput,
		)
	}

	tests := []struct {
		err    error
		status int
		body   string
	}{
		{
			err:    assetcatalog.ErrInvalidCrossChainRequest,
			status: http.StatusBadRequest,
			body:   "invalid cross-chain identity request",
		},
		{
			err:    assetcatalog.ErrCrossChainIdentityUnverified,
			status: http.StatusUnprocessableEntity,
			body:   "cross-chain asset identity is unverified",
		},
		{
			err:    assetcatalog.ErrCrossChainIdentityConflict,
			status: http.StatusConflict,
			body:   "cross-chain asset identity conflicts",
		},
	}
	for _, test := range tests {
		assets.err = fmt.Errorf("private detail: %w", test.err)
		failed := httptest.NewRecorder()
		handler.ServeHTTP(failed, request())
		if failed.Code != test.status ||
			failed.Body.String() !=
				`{"detail":"`+test.body+`"}`+"\n" {
			t.Fatalf(
				"verify error = %d/%s",
				failed.Code,
				failed.Body.String(),
			)
		}
	}
}
