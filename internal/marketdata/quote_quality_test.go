package marketdata

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
)

func TestQuoteQualityLiveAndCachedPreserveObservationAndIsolation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Retrieval provenance from untrusted provider JSON must be replaced.
		payload := strings.Replace(pairArrayJSON(marketPairA, marketTokenA, marketTokenB), `"chainId":"bsc"`, `"quote_quality":{"status":"live","fetched_at":"2000-01-01T00:00:00Z","source_timestamp":"2000-01-01T00:00:00Z"},"chainId":"bsc"`, 1)
		_, _ = io.WriteString(w, payload)
	}))
	defer server.Close()
	client := newTestDexScreenerClient(t, server)
	before := time.Now().UTC()
	live, err := client.DiscoverPools(context.Background(), "bsc", marketTokenA)
	if err != nil || len(live) != 1 {
		t.Fatalf("live=%v,%v", live, err)
	}
	quality := live[0].QuoteQuality.Clone()
	if quality.Status != marketquality.StatusLive || quality.FetchedAt == nil || quality.FetchedAt.Before(before.Truncate(time.Microsecond)) || quality.FetchedAt.Nanosecond()%1000 != 0 || quality.SourceTimestamp != nil {
		t.Fatalf("live quality=%+v", quality)
	}
	*live[0].QuoteQuality.FetchedAt = time.Time{}
	cached, err := client.DiscoverPools(context.Background(), "bsc", marketTokenA)
	if err != nil || cached[0].QuoteQuality.Status != marketquality.StatusCached || !cached[0].QuoteQuality.FetchedAt.Equal(*quality.FetchedAt) {
		t.Fatalf("cached quality=%+v,%v", cached, err)
	}
	*cached[0].QuoteQuality.FetchedAt = time.Time{}
	again, err := client.DiscoverPools(context.Background(), "bsc", marketTokenA)
	if err != nil || !again[0].QuoteQuality.FetchedAt.Equal(*quality.FetchedAt) {
		t.Fatalf("caller mutated cache provenance=%+v,%v", again, err)
	}
}

func TestQuoteQualityTemporaryFailureAndCooldownKeepOriginalTime(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	client := newTestDexScreenerClient(t, server)
	observed := time.Now().UTC().Add(-time.Minute)
	expires := time.Now().Add(-time.Second)
	deadline := time.Now().Add(time.Minute)
	key := "discover:bsc:" + marketTokenA
	client.cache[key] = cachedPairs{pairs: []Pair{{QuoteQuality: marketquality.Quote{Status: marketquality.StatusLive, FetchedAt: &observed}}}, expiresAt: expires, staleUntil: deadline}
	for range 2 {
		pairs, err := client.DiscoverPools(context.Background(), "bsc", marketTokenA)
		if err != nil || len(pairs) != 1 || pairs[0].QuoteQuality.Status != marketquality.StatusStaleFallback || !pairs[0].QuoteQuality.FetchedAt.Equal(observed) {
			t.Fatalf("fallback=%+v,%v", pairs, err)
		}
	}
	if requests != 1 || !client.cache[key].expiresAt.Equal(expires) || !client.cache[key].staleUntil.Equal(deadline) {
		t.Fatalf("fallback extended cache or ignored cooldown: requests=%d cache=%+v", requests, client.cache[key])
	}
}

func TestQuoteQualityFallbackExpiresWhileFetchIsInProgress(t *testing.T) {
	deadline := time.Now().Add(30 * time.Millisecond)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-time.After(time.Until(deadline) + time.Millisecond)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := newTestDexScreenerClient(t, server)
	observed := time.Now().Add(-time.Minute)
	client.cache["discover:bsc:"+marketTokenA] = cachedPairs{pairs: []Pair{{QuoteQuality: marketquality.Quote{Status: marketquality.StatusLive, FetchedAt: &observed}}}, expiresAt: time.Now().Add(-time.Second), staleUntil: deadline}
	pairs, err := client.DiscoverPools(context.Background(), "bsc", marketTokenA)
	if err == nil || len(pairs) != 0 {
		t.Fatalf("expired fallback escaped after slow fetch: pairs=%+v err=%v", pairs, err)
	}
}

type inflightQualityContext struct {
	context.Context
	joined chan struct{}
}

func (ctx inflightQualityContext) Done() <-chan struct{} {
	select {
	case ctx.joined <- struct{}{}:
	default:
	}
	return nil
}

func TestQuoteQualityInflightSharesResultWithoutSharingPointers(t *testing.T) {
	client := &DexScreenerClient{cache: make(map[string]cachedPairs), inflight: make(map[string]*pairCall)}
	started, release, joined := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	type result struct {
		pairs []Pair
		err   error
	}
	results := make(chan result, 2)
	fetch := func(context.Context) ([]Pair, error) {
		close(started)
		<-release
		return []Pair{{PairAddress: marketPairA}}, nil
	}
	go func() {
		pairs, err := client.cachedFetch(context.Background(), "shared", time.Minute, time.Minute, fetch)
		results <- result{pairs, err}
	}()
	<-started
	go func() {
		pairs, err := client.cachedFetch(inflightQualityContext{context.Background(), joined}, "shared", time.Minute, time.Minute, func(context.Context) ([]Pair, error) {
			return nil, fmt.Errorf("inflight join issued a second fetch")
		})
		results <- result{pairs, err}
	}()
	select {
	case <-joined:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("second caller did not join inflight request")
	}
	close(release)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || len(first.pairs) != 1 || len(second.pairs) != 1 {
		t.Fatalf("shared results=%+v/%+v", first, second)
	}
	a, b := first.pairs[0].QuoteQuality, second.pairs[0].QuoteQuality
	if a.Status != marketquality.StatusLive || b.Status != marketquality.StatusLive || a.FetchedAt == b.FetchedAt || !a.FetchedAt.Equal(*b.FetchedAt) {
		t.Fatalf("inflight provenance=%+v/%+v", a, b)
	}
	*a.FetchedAt = time.Time{}
	if b.FetchedAt.IsZero() {
		t.Fatal("inflight callers share mutable timestamp")
	}
}

func TestQuoteQualityMixedBatchesKeepPerResponseProvenance(t *testing.T) {
	addresses := make([]string, 31)
	for i := range addresses {
		addresses[i] = fmt.Sprintf("0x%040x", i+100)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/latest/dex/pairs/bsc/"+addresses[30] {
			t.Errorf("unexpected live batch %s", r.URL.Path)
		}
		_, _ = fmt.Fprintf(w, `{"pairs":%s}`, pairArrayJSON(addresses[30], marketTokenA, marketTokenB))
	}))
	defer server.Close()
	client := newTestDexScreenerClient(t, server)
	observed := time.Now().UTC().Add(-10 * time.Second)
	client.cache["pairs:bsc:"+strings.Join(addresses[:30], ",")] = cachedPairs{
		pairs:     []Pair{{ChainID: "bsc", PairAddress: addresses[0], QuoteQuality: marketquality.Quote{Status: marketquality.StatusLive, FetchedAt: &observed}}},
		expiresAt: time.Now().Add(time.Minute), staleUntil: time.Now().Add(time.Hour),
	}
	pairs, err := client.PairsByAddresses(context.Background(), "bsc", addresses)
	if err != nil || len(pairs) != 2 {
		t.Fatalf("mixed=%+v,%v", pairs, err)
	}
	if pairs[0].QuoteQuality.Status != marketquality.StatusCached || !pairs[0].QuoteQuality.FetchedAt.Equal(observed) || pairs[1].QuoteQuality.Status != marketquality.StatusLive || !pairs[1].QuoteQuality.FetchedAt.After(observed) {
		t.Fatalf("batch times were flattened=%+v", pairs)
	}
}

func TestQuoteQualityCloneCopiesSourceTimestamp(t *testing.T) {
	fetched, source := time.Now().UTC(), time.Now().UTC().Add(-time.Second)
	original := []Pair{{QuoteQuality: marketquality.Quote{Status: marketquality.StatusLive, FetchedAt: &fetched, SourceTimestamp: &source}}}
	cloned := clonePairs(original)
	*cloned[0].QuoteQuality.SourceTimestamp = time.Time{}
	*cloned[0].QuoteQuality.FetchedAt = time.Time{}
	if original[0].QuoteQuality.SourceTimestamp.IsZero() || original[0].QuoteQuality.FetchedAt.IsZero() {
		t.Fatal("quality pointer mutation escaped clone")
	}
}
