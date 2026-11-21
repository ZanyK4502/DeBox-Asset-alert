package marketdata

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
)

func TestDexScreenerCachePayloadBudgetCountsOnceAndPreservesQuoteEvidence(t *testing.T) {
	client := &DexScreenerClient{cache: map[string]cachedPairs{}, inflight: map[string]*pairCall{}}
	calls := 0
	fetch := func(context.Context) ([]Pair, error) {
		calls++
		return []Pair{{PairAddress: marketPairA, Info: json.RawMessage(`{"test":"value"}`)}}, nil
	}
	first, err := client.cachedFetch(context.Background(), "test", time.Minute, time.Minute, fetch)
	if err != nil {
		t.Fatal(err)
	}
	observed := *first[0].QuoteQuality.FetchedAt
	item := client.cache["test"]
	encoded, _ := json.Marshal(item.pairs)
	if item.encodedBytes != len(encoded) || client.cacheBytes != len(encoded) {
		t.Fatalf("payload accounting=%d/%d expected=%d", item.encodedBytes, client.cacheBytes, len(encoded))
	}
	// A caller mutation cannot affect either retained evidence or accounting.
	first[0].Info[0] = 'x'
	*first[0].QuoteQuality.FetchedAt = time.Time{}
	hit, err := client.cachedFetch(context.Background(), "test", time.Minute, time.Minute, fetch)
	if err != nil || calls != 1 || hit[0].QuoteQuality.Status != marketquality.StatusCached || !hit[0].QuoteQuality.FetchedAt.Equal(observed) || client.cacheBytes != len(encoded) || !client.cache["test"].expiresAt.Equal(item.expiresAt) {
		t.Fatalf("cache hit changed evidence/budget calls=%d err=%v", calls, err)
	}
	client.mu.Lock()
	client.removeCacheEntry("test")
	client.mu.Unlock()
	if client.cacheBytes != 0 || len(client.cache) != 0 {
		t.Fatal("removal leaked retained-byte accounting")
	}
}

func TestDexScreenerCachePayloadEvictionUsesOriginalExpiryAndByteBudget(t *testing.T) {
	now := time.Now()
	observed := now.Add(-time.Minute)
	quality := marketquality.Quote{Status: marketquality.StatusLive, FetchedAt: &observed}
	client := &DexScreenerClient{cache: map[string]cachedPairs{
		"old": {pairs: []Pair{{QuoteQuality: quality.Clone()}}, expiresAt: now.Add(time.Minute), staleUntil: now.Add(2 * time.Minute), encodedBytes: 24 << 20},
		"new": {pairs: []Pair{{QuoteQuality: quality.Clone()}}, expiresAt: now.Add(2 * time.Minute), staleUntil: now.Add(3 * time.Minute), encodedBytes: 4 << 20},
	}, cacheBytes: 28 << 20}
	original := client.cache["new"]
	client.evictCacheEntries(now, 6<<20)
	if _, exists := client.cache["old"]; exists || len(client.cache) != 1 || client.cacheBytes != 4<<20 {
		t.Fatal("byte budget failed to evict oldest retained payload")
	}
	if !client.cache["new"].expiresAt.Equal(original.expiresAt) || !client.cache["new"].pairs[0].QuoteQuality.FetchedAt.Equal(observed) {
		t.Fatal("eviction refreshed surviving evidence")
	}
	client.evictCacheEntries(now.Add(4*time.Minute), maxDexScreenerCacheBytes)
	if client.cacheBytes != 0 || len(client.cache) != 0 {
		t.Fatal("expired eviction leaked byte accounting")
	}
}

func TestDexScreenerOversizedLiveResultIsReturnedWithoutRetentionOrInventedFallback(t *testing.T) {
	client := &DexScreenerClient{cache: map[string]cachedPairs{}, inflight: map[string]*pairCall{}}
	large := strings.Repeat("x", maxDexScreenerCacheBytes+1)
	pairs, err := client.cachedFetch(context.Background(), "large", time.Minute, time.Minute, func(context.Context) ([]Pair, error) { return []Pair{{URL: large}}, nil })
	if err != nil || len(pairs) != 1 || len(pairs[0].URL) != len(large) || pairs[0].QuoteQuality.Status != marketquality.StatusLive || len(client.cache) != 0 || client.cacheBytes != 0 {
		t.Fatal("large successful result was truncated/failed/retained", err)
	}
	failure := &DexScreenerHTTPError{StatusCode: 503}
	pairs, err = client.cachedFetch(context.Background(), "large", time.Minute, time.Minute, func(context.Context) ([]Pair, error) { return nil, failure })
	if err != failure || pairs != nil {
		t.Fatal("evicted/missing payload was invented as stale fallback")
	}
}
