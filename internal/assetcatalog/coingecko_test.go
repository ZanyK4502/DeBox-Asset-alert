package assetcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type noWaitLimiter struct{}

func (noWaitLimiter) Wait(context.Context) error { return nil }

func TestCoinGeckoSearchMapsSixChainsAndRanksExactMatch(t *testing.T) {
	var listCalls atomic.Int64
	var searchCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.Header.Get("x-cg-pro-api-key") != "secret" {
			t.Errorf("missing pro API header")
		}
		switch request.URL.Path {
		case "/coins/list":
			listCalls.Add(1)
			if request.URL.Query().Get("include_platform") != "true" {
				t.Errorf("include_platform = %q", request.URL.RawQuery)
			}
			writeTestJSON(t, writer, []map[string]any{
				{
					"id": "cake-old", "name": "Cake Old", "symbol": "cake",
					"platforms": map[string]string{
						"binance-smart-chain": testAddress(1),
					},
				},
				{
					"id": "pancakeswap-token", "name": "Cake", "symbol": "cake",
					"platforms": map[string]string{
						"binance-smart-chain": testAddress(2),
						"ethereum":            testAddress(3),
						"base":                testAddress(4),
						"polygon-pos":         testAddress(5),
						"arbitrum-one":        testAddress(6),
						"optimistic-ethereum": testAddress(7),
						"solana":              "not-an-evm-address",
					},
				},
			})
		case "/search":
			searchCalls.Add(1)
			if request.URL.Query().Get("query") != "cake" {
				t.Errorf("query = %q", request.URL.Query().Get("query"))
			}
			writeTestJSON(t, writer, map[string]any{
				"coins": []map[string]any{
					{
						"id": "cake-old", "name": "Cake Old", "symbol": "CAKE",
						"market_cap_rank": 2,
					},
					{
						"id": "pancakeswap-token", "name": "Cake",
						"symbol": "CAKE", "market_cap_rank": 999,
						"large": "https://coin-images.coingecko.com/cake.png",
					},
				},
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := NewCoinGeckoClient(CoinGeckoSettings{
		Tier: "pro", APIKey: "secret", BaseURL: server.URL,
		HTTPClient: server.Client(), Limiter: noWaitLimiter{},
	})
	if err != nil {
		t.Fatalf("NewCoinGeckoClient() error = %v", err)
	}
	candidates, err := client.Search(context.Background(), " cake ", 10)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidate count = %d, want 2", len(candidates))
	}
	if candidates[0].CanonicalAssetID != "pancakeswap-token" {
		t.Fatalf("first candidate = %q", candidates[0].CanonicalAssetID)
	}
	if candidates[0].IdentityStatus != IdentityVerified ||
		len(candidates[0].Deployments) != 6 {
		t.Fatalf("multi-chain candidate = %#v", candidates[0])
	}
	for index, want := range []string{
		"bsc", "ethereum", "base", "polygon", "arbitrum", "optimism",
	} {
		if candidates[0].Deployments[index].ChainKey != want {
			t.Fatalf(
				"deployment %d = %q, want %q",
				index, candidates[0].Deployments[index].ChainKey, want,
			)
		}
	}

	if _, err := client.Search(context.Background(), "cake", 10); err != nil {
		t.Fatalf("cached Search() error = %v", err)
	}
	if listCalls.Load() != 1 || searchCalls.Load() != 1 {
		t.Fatalf(
			"calls list/search = %d/%d, want 1/1",
			listCalls.Load(), searchCalls.Load(),
		)
	}
}

func TestCoinGeckoResolveContractUsesCanonicalIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		writeTestJSON(t, writer, []map[string]any{{
			"id": "usd-coin", "name": "USD Coin", "symbol": "usdc",
			"platforms": map[string]string{
				"ethereum": testAddress(11),
				"base":     testAddress(12),
			},
		}})
	}))
	defer server.Close()
	client, err := NewCoinGeckoClient(CoinGeckoSettings{
		BaseURL: server.URL, HTTPClient: server.Client(),
		Limiter: noWaitLimiter{},
	})
	if err != nil {
		t.Fatalf("NewCoinGeckoClient() error = %v", err)
	}
	candidate, err := client.ResolveContract(
		context.Background(), "BASE", testAddress(12),
	)
	if err != nil {
		t.Fatalf("ResolveContract() error = %v", err)
	}
	if candidate.CanonicalAssetID != "usd-coin" ||
		candidate.Symbol != "USDC" ||
		len(candidate.Deployments) != 2 {
		t.Fatalf("candidate = %#v", candidate)
	}
	if _, err := client.ResolveContract(
		context.Background(), "base", testAddress(99),
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing contract error = %v", err)
	}
}

func TestCoinGeckoAuthoritativeResolveNeverUsesStaleFallback(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		calls.Add(1)
		writeTestJSON(t, writer, []map[string]any{{
			"id": "project", "name": "Project", "symbol": "prj",
			"platforms": map[string]string{
				"ethereum": testAddress(31),
				"base":     testAddress(32),
			},
		}})
	}))
	defer server.Close()
	client, err := NewCoinGeckoClient(CoinGeckoSettings{
		BaseURL: server.URL, HTTPClient: server.Client(),
		Limiter: noWaitLimiter{},
	})
	if err != nil {
		t.Fatalf("NewCoinGeckoClient() error = %v", err)
	}
	if _, err := client.ResolveContract(
		context.Background(), "base", testAddress(32),
	); err != nil {
		t.Fatalf("prime identity cache: %v", err)
	}

	path := "/coins/list?include_platform=true&status=active"
	client.indexMu.Lock()
	client.indexExpires = time.Now().Add(-time.Minute)
	client.indexMu.Unlock()
	client.mu.Lock()
	cached := client.cache[path]
	cached.expiresAt = time.Now().Add(-time.Minute)
	cached.staleUntil = time.Now().Add(time.Hour)
	client.cache[path] = cached
	client.circuitOpenUntil = time.Now().Add(time.Minute)
	client.mu.Unlock()

	if _, err := client.ResolveContract(
		context.Background(), "base", testAddress(32),
	); err != nil {
		t.Fatalf("ordinary stale fallback failed: %v", err)
	}
	if _, err := client.ResolveContractAuthoritative(
		context.Background(), "base", testAddress(32),
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("authoritative stale fallback error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("HTTP calls = %d, want 1", calls.Load())
	}
}

func TestCoinGeckoAuthoritativeResolveCachesFreshIndex(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		calls.Add(1)
		writeTestJSON(t, writer, []map[string]any{{
			"id": "project", "name": "Project", "symbol": "prj",
			"platforms": map[string]string{
				"ethereum": testAddress(41),
				"base":     testAddress(42),
			},
		}})
	}))
	defer server.Close()
	client, err := NewCoinGeckoClient(CoinGeckoSettings{
		BaseURL: server.URL, HTTPClient: server.Client(),
		Limiter: noWaitLimiter{},
	})
	if err != nil {
		t.Fatalf("NewCoinGeckoClient() error = %v", err)
	}
	for _, value := range []struct {
		chain   string
		address string
	}{
		{chain: "ethereum", address: testAddress(41)},
		{chain: "base", address: testAddress(42)},
	} {
		if _, err := client.ResolveContractAuthoritative(
			context.Background(),
			value.chain,
			value.address,
		); err != nil {
			t.Fatalf("authoritative resolve %s: %v", value.chain, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("authoritative identity calls = %d, want 1", calls.Load())
	}
}

func TestCoinGeckoCoalescesConcurrentIdentityRequests(t *testing.T) {
	var calls atomic.Int64
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		calls.Add(1)
		<-release
		writeTestJSON(t, writer, []map[string]any{})
	}))
	defer server.Close()
	client, err := NewCoinGeckoClient(CoinGeckoSettings{
		BaseURL: server.URL, HTTPClient: server.Client(),
		Limiter: noWaitLimiter{},
	})
	if err != nil {
		t.Fatalf("NewCoinGeckoClient() error = %v", err)
	}
	var wait sync.WaitGroup
	wait.Add(2)
	errorsSeen := make(chan error, 2)
	for range 2 {
		go func() {
			defer wait.Done()
			_, err := client.ResolveContract(
				context.Background(), "bsc", testAddress(1),
			)
			errorsSeen <- err
		}()
	}
	for calls.Load() == 0 {
	}
	close(release)
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("ResolveContract() error = %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("identity calls = %d, want 1", calls.Load())
	}
}

func TestCoinGeckoConfigurationValidation(t *testing.T) {
	if _, err := NewCoinGeckoClient(CoinGeckoSettings{
		Tier: "enterprise",
	}); err == nil {
		t.Fatal("expected invalid tier error")
	}
	if _, err := NewCoinGeckoClient(CoinGeckoSettings{
		Tier: "pro",
	}); err == nil {
		t.Fatal("expected missing pro key error")
	}
	if _, err := NewCoinGeckoClient(CoinGeckoSettings{
		Tier: "demo", BaseURL: "://invalid",
	}); err == nil {
		t.Fatal("expected invalid URL error")
	}
}

func TestCoinGeckoRetriesRateLimitWithoutLeakingKey(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.Header.Get("x-cg-demo-api-key") != "private-key" {
			t.Errorf("missing demo API header")
		}
		if calls.Add(1) == 1 {
			writer.Header().Set("Retry-After", "0")
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = writer.Write([]byte("private upstream diagnostic"))
			return
		}
		writeTestJSON(t, writer, []map[string]any{{
			"id": "project", "name": "Project", "symbol": "prj",
			"platforms": map[string]string{"base": testAddress(1)},
		}})
	}))
	defer server.Close()
	client, err := NewCoinGeckoClient(CoinGeckoSettings{
		Tier: "demo", APIKey: "private-key", BaseURL: server.URL,
		HTTPClient: server.Client(), Limiter: noWaitLimiter{},
	})
	if err != nil {
		t.Fatalf("NewCoinGeckoClient() error = %v", err)
	}
	candidate, err := client.ResolveContract(
		context.Background(), "base", testAddress(1),
	)
	if err != nil {
		t.Fatalf("ResolveContract() error = %v", err)
	}
	if candidate.CanonicalAssetID != "project" || calls.Load() != 2 {
		t.Fatalf("candidate/calls = %#v/%d", candidate, calls.Load())
	}
	apiError := (&CoinGeckoHTTPError{StatusCode: 429}).Error()
	if strings.Contains(apiError, "private-key") ||
		strings.Contains(apiError, "diagnostic") {
		t.Fatalf("CoinGecko error leaked provider details: %q", apiError)
	}
}

func TestBuildIdentityIndexMarksConflictingContractAmbiguous(t *testing.T) {
	address := testAddress(42)
	index := buildIdentityIndex([]coinListItem{
		{
			ID:        "first",
			Platforms: map[string]string{"ethereum": address},
		},
		{
			ID:        "second",
			Platforms: map[string]string{"ethereum": address},
		},
	})
	if value, exists := index.byContract["ethereum:"+address]; !exists ||
		value != "" {
		t.Fatalf("conflicting identity = %q/%v", value, exists)
	}
}

func writeTestJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func testAddress(value int) string {
	return fmt.Sprintf("0x%040x", value)
}
