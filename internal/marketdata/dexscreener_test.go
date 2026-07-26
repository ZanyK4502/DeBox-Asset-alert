package marketdata

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	marketTokenA = "0x1111111111111111111111111111111111111111"
	marketTokenB = "0x2222222222222222222222222222222222222222"
	marketPairA  = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	marketPairB  = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type noWaitLimiter struct{}

func (noWaitLimiter) Wait(context.Context) error { return nil }

func TestDexScreenerDiscoverPoolsPreservesDecimalsAndCaches(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.URL.Path != "/token-pairs/v1/bsc/"+marketTokenA {
			t.Errorf("path = %s", request.URL.Path)
		}
		_, _ = io.WriteString(writer, pairArrayJSON(marketPairA, marketTokenA, marketTokenB))
	}))
	defer server.Close()

	client := newTestDexScreenerClient(t, server)
	first, err := client.DiscoverPools(
		context.Background(),
		"BNB",
		"0x"+strings.ToUpper(marketTokenA[2:]),
	)
	if err != nil {
		t.Fatalf("DiscoverPools() error = %v", err)
	}
	second, err := client.DiscoverPools(context.Background(), "bsc", marketTokenA)
	if err != nil {
		t.Fatalf("cached DiscoverPools() error = %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
	if len(first) != 1 || first[0].PriceUSD.String() != "0.000000123456789123456789" ||
		first[0].Liquidity.USD.String() != "123456.789123456789" ||
		first[0].FDV.String() != "987654321.123456789" {
		t.Fatalf("unexpected pair: %#v", first)
	}
	first[0].Volume["h24"] = "changed"
	if second[0].Volume["h24"].String() != "999.125" {
		t.Fatalf("cache value was mutated: %#v", second[0].Volume)
	}
}

func TestDexScreenerBatchesTokensAndPairs(t *testing.T) {
	var tokenRequests atomic.Int32
	var pairRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasPrefix(request.URL.Path, "/tokens/v1/bsc/"):
			tokenRequests.Add(1)
			addresses := strings.Split(strings.TrimPrefix(request.URL.Path, "/tokens/v1/bsc/"), ",")
			if len(addresses) > maxDexScreenerBatchSize {
				t.Errorf("token batch size = %d", len(addresses))
			}
			_, _ = io.WriteString(writer, pairArrayJSON(pairAddressFor(addresses[0]), addresses[0], marketTokenB))
		case strings.HasPrefix(request.URL.Path, "/latest/dex/pairs/bsc/"):
			pairRequests.Add(1)
			addresses := strings.Split(strings.TrimPrefix(request.URL.Path, "/latest/dex/pairs/bsc/"), ",")
			if len(addresses) > maxDexScreenerBatchSize {
				t.Errorf("pair batch size = %d", len(addresses))
			}
			items := make([]string, 0, len(addresses))
			for _, address := range addresses {
				items = append(items, strings.TrimSuffix(strings.TrimPrefix(
					pairArrayJSON(address, marketTokenA, marketTokenB), "[",
				), "]"))
			}
			_, _ = fmt.Fprintf(writer, `{"schemaVersion":"1.0.0","pairs":[%s]}`, strings.Join(items, ","))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client := newTestDexScreenerClient(t, server)
	tokens := make([]string, 31)
	for index := range tokens {
		tokens[index] = fmt.Sprintf("0x%040x", index+100)
	}
	tokenPairs, err := client.PairsByTokens(context.Background(), "bsc", tokens)
	if err != nil {
		t.Fatalf("PairsByTokens() error = %v", err)
	}
	if tokenRequests.Load() != 2 || len(tokenPairs) != 2 {
		t.Fatalf("token requests/pairs = %d/%d", tokenRequests.Load(), len(tokenPairs))
	}

	pairAddresses := append([]string(nil), tokens...)
	pairs, err := client.PairsByAddresses(context.Background(), "bsc", pairAddresses)
	if err != nil {
		t.Fatalf("PairsByAddresses() error = %v", err)
	}
	if pairRequests.Load() != 2 || len(pairs) != 31 {
		t.Fatalf("pair requests/pairs = %d/%d", pairRequests.Load(), len(pairs))
	}
}

func TestDexScreenerCoalescesConcurrentRequests(t *testing.T) {
	var requests atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		<-release
		_, _ = io.WriteString(writer, pairArrayJSON(marketPairA, marketTokenA, marketTokenB))
	}))
	defer server.Close()
	client := newTestDexScreenerClient(t, server)

	var wait sync.WaitGroup
	errorsFound := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := client.DiscoverPools(context.Background(), "bsc", marketTokenA)
			errorsFound <- err
		}()
	}
	deadline := time.Now().Add(time.Second)
	for requests.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("DiscoverPools() error = %v", err)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestDexScreenerRetriesRateLimitsAndRedactsProviderBody(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if requests.Add(1) == 1 {
			writer.Header().Set("Retry-After", "0")
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(writer, "cloudflare diagnostic details")
			return
		}
		_, _ = io.WriteString(writer, pairArrayJSON(marketPairA, marketTokenA, marketTokenB))
	}))
	defer server.Close()

	client := newTestDexScreenerClient(t, server)
	client.maxRetries = 1
	client.retryDelay = time.Millisecond
	client.maxBackoff = time.Millisecond
	pairs, err := client.DiscoverPools(context.Background(), "bsc", marketTokenA)
	if err != nil {
		t.Fatalf("DiscoverPools() error = %v", err)
	}
	if requests.Load() != 2 || len(pairs) != 1 {
		t.Fatalf("requests/pairs = %d/%d, want 2/1", requests.Load(), len(pairs))
	}

	apiError := &DexScreenerHTTPError{
		StatusCode: http.StatusTooManyRequests,
		Body:       "must not reach a user-facing error",
	}
	if strings.Contains(apiError.Error(), apiError.Body) {
		t.Fatalf("provider response body leaked through error: %q", apiError.Error())
	}
}

func TestDexScreenerUsesStaleCacheDuringTemporaryFailure(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if requests.Add(1) == 1 {
			_, _ = io.WriteString(writer, pairArrayJSON(marketPairA, marketTokenA, marketTokenB))
			return
		}
		writer.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := newTestDexScreenerClient(t, server)
	client.discoveryCacheTTL = time.Millisecond
	client.discoveryStaleTTL = time.Minute
	first, err := client.DiscoverPools(context.Background(), "bsc", marketTokenA)
	if err != nil {
		t.Fatalf("first DiscoverPools() error = %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	second, err := client.DiscoverPools(context.Background(), "bsc", marketTokenA)
	if err != nil {
		t.Fatalf("stale DiscoverPools() error = %v", err)
	}
	if requests.Load() != 2 || len(first) != 1 || len(second) != 1 ||
		second[0].PairAddress != first[0].PairAddress {
		t.Fatalf(
			"requests/first/second = %d/%d/%d",
			requests.Load(),
			len(first),
			len(second),
		)
	}
	third, err := client.DiscoverPools(context.Background(), "bsc", marketTokenA)
	if err != nil || len(third) != 1 || requests.Load() != 2 {
		t.Fatalf(
			"cooldown cache requests/pairs/error = %d/%d/%v",
			requests.Load(),
			len(third),
			err,
		)
	}
}

func TestDexScreenerValidatesInputsAndHTTPFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Retry-After", "3")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(writer, strings.Repeat("x", 700))
	}))
	defer server.Close()
	client := newTestDexScreenerClient(t, server)

	if _, err := client.DiscoverPools(context.Background(), "ethereum", marketTokenA); err == nil {
		t.Fatal("DiscoverPools() accepted unsupported chain")
	}
	if _, err := client.PairsByTokens(context.Background(), "bsc", nil); err == nil {
		t.Fatal("PairsByTokens() accepted empty address list")
	}
	if _, err := client.DiscoverPools(context.Background(), "bsc", "invalid"); err == nil {
		t.Fatal("DiscoverPools() accepted invalid address")
	}
	_, err := client.DiscoverPools(context.Background(), "bsc", marketTokenA)
	var apiError *DexScreenerHTTPError
	if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusTooManyRequests ||
		apiError.RetryAfter != "3" || len(apiError.Body) != maxDexScreenerErrorBody {
		t.Fatalf("unexpected HTTP error: %#v (%v)", apiError, err)
	}
}

func TestDecimalAcceptsStringsNumbersAndNull(t *testing.T) {
	var values struct {
		String Decimal `json:"string"`
		Number Decimal `json:"number"`
		Null   Decimal `json:"null"`
	}
	if err := decodeJSON(
		[]byte(`{"string":"1.234567890123456789","number":2.5e-7,"null":null}`),
		&values,
	); err != nil {
		t.Fatalf("decodeJSON() error = %v", err)
	}
	if values.String.String() != "1.234567890123456789" ||
		values.Number.String() != "2.5e-7" || values.Null.Valid() {
		t.Fatalf("unexpected decimals: %#v", values)
	}
	if err := decodeJSON([]byte(`{"string":"not-a-number"}`), &values); err == nil {
		t.Fatal("decodeJSON() accepted invalid decimal")
	}
}

func TestNormalizePairsAcceptsOnlyVerifiedFourMemePseudoKeys(t *testing.T) {
	pairs, err := normalizePairs([]Pair{{
		ChainID:     "bsc",
		DexID:       "fourmeme",
		PairAddress: marketTokenA + ":4meme",
		BaseToken:   Token{Address: marketTokenA},
		QuoteToken:  Token{Address: marketTokenB},
	}})
	if err != nil || len(pairs) != 1 ||
		pairs[0].PairAddress != marketTokenA+":4meme" {
		t.Fatalf("normalize Four.meme pair = %#v, %v", pairs, err)
	}

	for name, pair := range map[string]Pair{
		"other DEX": {
			ChainID: "bsc", DexID: "unknown",
			PairAddress: marketTokenA + ":4meme",
			BaseToken:   Token{Address: marketTokenA},
			QuoteToken:  Token{Address: marketTokenB},
		},
		"mismatched token": {
			ChainID: "bsc", DexID: "fourmeme",
			PairAddress: marketTokenB + ":4meme",
			BaseToken:   Token{Address: marketTokenA},
			QuoteToken:  Token{Address: marketTokenA},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizePairs([]Pair{pair}); err == nil {
				t.Fatal("normalizePairs() accepted an unverified pseudo pair key")
			}
		})
	}
}

func newTestDexScreenerClient(t *testing.T, server *httptest.Server) *DexScreenerClient {
	t.Helper()
	client, err := NewDexScreenerClient(
		server.URL,
		WithDexScreenerHTTPClient(server.Client()),
		WithDexScreenerLimiter(noWaitLimiter{}),
	)
	if err != nil {
		t.Fatalf("NewDexScreenerClient() error = %v", err)
	}
	client.maxRetries = 0
	return client
}

func pairArrayJSON(pairAddress, baseAddress, quoteAddress string) string {
	return fmt.Sprintf(`[{
		"chainId":"bsc",
		"dexId":"pancakeswap",
		"url":"https://dexscreener.com/bsc/%s",
		"pairAddress":"%s",
		"labels":["v3"],
		"baseToken":{"address":"%s","name":"Base","symbol":"BASE"},
		"quoteToken":{"address":"%s","name":"Quote","symbol":"QUOTE"},
		"priceNative":"0.0001",
		"priceUsd":"0.000000123456789123456789",
		"txns":{"m5":{"buys":12,"sells":3}},
		"volume":{"h24":999.125},
		"priceChange":{"h24":-12.25},
		"liquidity":{"usd":123456.789123456789,"base":1000,"quote":2000},
		"fdv":987654321.123456789,
		"marketCap":null,
		"pairCreatedAt":1700000000000
	}]`, pairAddress, pairAddress, baseAddress, quoteAddress)
}

func pairAddressFor(token string) string {
	return "0x" + token[len(token)-40:]
}
