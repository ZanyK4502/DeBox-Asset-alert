package chain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBoundedMarketDecoderRejectsOversizeAndTrailingValues(t *testing.T) {
	for _, body := range []string{`{"value":"` + strings.Repeat("x", 200) + `"}`, `{"value":1}` + strings.Repeat(" ", 200)} {
		if _, err := decodeBoundedRPCObject(strings.NewReader(body), 100); !errors.Is(err, ErrMarketRPCResponseTooLarge) {
			t.Fatalf("oversize: %v", err)
		}
	}
	if _, err := decodeBoundedRPCObject(strings.NewReader(`{"x":1}{"y":2}`), 100); err == nil {
		t.Fatal("accepted trailing object")
	}
	object, err := decodeBoundedRPCObject(strings.NewReader(`{"x":9007199254740993}`), 100)
	if err != nil || object["x"].(json.Number).String() != "9007199254740993" {
		t.Fatalf("number precision: %v %v", object, err)
	}
}

func TestMarketFallbackReadIdentityAndCapabilityBoundary(t *testing.T) {
	var primaryCalls, fallbackCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		// JSON-RPC failure avoids a retry of the primary HTTP status.
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"unavailable"}}`)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls.Add(1)
		if r.Header.Get("X-API-KEY") != "" {
			t.Error("forwarded Nodit key")
		}
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request["method"] == "eth_chainId" {
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x38"}`)
			return
		}
		if request["method"] != "eth_getTransactionReceipt" {
			t.Errorf("unexpected fallback method %v", request["method"])
		}
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"transactionHash":"ok"}}`)
	}))
	defer fallback.Close()
	client, _ := NewClient("secret-key", "", WithRPCBaseURL(primary.URL))
	market, err := NewMarketReadClient(client, map[string][]string{"bsc": {fallback.URL}})
	if err != nil {
		t.Fatal(err)
	}
	market.limiter = nil // deterministic local fixture, production remains two/sec
	result, err := market.MarketTransactionReceipt(context.Background(), "0x"+strings.Repeat("1", 64), "bsc", "")
	if err != nil || result["transactionHash"] != "ok" {
		t.Fatalf("fallback: %v %v", result, err)
	}
	if _, err = market.LatestBlockNumber(context.Background(), "bsc", ""); err == nil {
		t.Fatal("expected primary failure")
	}
	if fallbackCalls.Load() != 2 || primaryCalls.Load() != 2 {
		t.Fatalf("wrong capability routing: primary=%d fallback=%d", primaryCalls.Load(), fallbackCalls.Load())
	}
	// Embedding the wrapper does not mutate the primary/payment client.
	if client.maxResponseBytes != 0 {
		t.Fatal("primary client mutated")
	}
}

func TestMarketFallbackRejectsWrongChainAndNoSecretsInError(t *testing.T) {
	var reads atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"error":{"message":"down"}}`) }))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["method"] != "eth_chainId" {
			reads.Add(1)
		}
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x1"}`)
	}))
	defer fallback.Close()
	client, _ := NewClient("private-key", "", WithRPCBaseURL(primary.URL))
	market, _ := NewMarketReadClient(client, map[string][]string{"bsc": {fallback.URL + "/secret-path?token=private-token"}})
	market.limiter = nil
	_, err := market.BlockByNumber(context.Background(), 10, false, "bsc", "")
	if err == nil || reads.Load() != 0 {
		t.Fatalf("wrong chain read permitted: %v %d", err, reads.Load())
	}
	if strings.Contains(err.Error(), "private-") || strings.Contains(err.Error(), "secret-") {
		t.Fatalf("URL leaked: %v", err)
	}
}

func TestMarketFallbackDoesNotOverridePrimaryNullOrInvalidSuccess(t *testing.T) {
	for _, result := range []string{"null", `"unexpected"`} {
		t.Run(result, func(t *testing.T) {
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"result":%s}`, result) }))
			defer primary.Close()
			var calls atomic.Int32
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
			defer fallback.Close()
			client, _ := NewClient("key", "", WithRPCBaseURL(primary.URL))
			market, _ := NewMarketReadClient(client, map[string][]string{"bsc": {fallback.URL}})
			_, err := market.MarketTransactionReceipt(context.Background(), "0x"+strings.Repeat("1", 64), "bsc", "")
			if result != "null" && err == nil {
				t.Fatal("invalid object accepted")
			}
			if calls.Load() != 0 {
				t.Fatal("success activated fallback")
			}
		})
	}
}

func TestMarketPrimaryBodyIsCappedBeforeUnmarshal(t *testing.T) {
	client, _ := NewClient("key", "", WithHTTPClient(marketOversizeDoer{}))
	_, err := client.MarketTransactionReceipt(context.Background(), "0x"+strings.Repeat("1", 64), "bsc", "")
	if !errors.Is(err, ErrMarketRPCResponseTooLarge) {
		t.Fatalf("bound: %v", err)
	}
}

type marketOversizeDoer struct{}

func (marketOversizeDoer) Do(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(io.MultiReader(strings.NewReader(`{"result":{"logs":"`), io.LimitReader(repeatingMarketReader{}, MaxMarketRPCResponseBytes+1), strings.NewReader(`"}}`)))}, nil
}

type repeatingMarketReader struct{}

func (repeatingMarketReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestMarketFallbackCancellationBoundAndNoRedirectReplay(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"error":{"message":"down"}}`) }))
	defer primary.Close()
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer target.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer fallback.Close()
	client, _ := NewClient("key", "", WithRPCBaseURL(primary.URL))
	market, _ := NewMarketReadClient(client, map[string][]string{"bsc": {fallback.URL}})
	market.limiter = nil
	if _, err := market.BlockByNumber(context.Background(), 1, false, "bsc", ""); err == nil {
		t.Fatal("redirect accepted")
	}
	if redirected.Load() != 0 {
		t.Fatal("redirect replayed")
	}
	market.slots <- struct{}{}
	market.slots <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := market.BlockByNumber(ctx, 1, false, "bsc", "")
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("cancel not bounded: %v", err)
	}
}
