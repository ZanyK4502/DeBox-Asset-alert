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
)

func TestMarketBlockReceiptsUsesHashAndMetersPhysicalRetries(t *testing.T) {
	hash := "0x" + strings.Repeat("a", 64)
	var calls, usage atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Version string   `json:"jsonrpc"`
			ID      int      `json:"id"`
			Method  string   `json:"method"`
			Params  []string `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || r.Method != http.MethodPost ||
			request.Version != "2.0" || request.ID != 1 || request.Method != "eth_getBlockReceipts" ||
			len(request.Params) != 1 || request.Params[0] != hash || r.Header.Get("X-API-KEY") != "local-only" {
			t.Errorf("unexpected receipt request: %+v %v", request, err)
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":[{"blockHash":%q}]}`, hash)
	}))
	defer server.Close()
	primary, err := NewClient("local-only", "", WithRPCBaseURL(server.URL), WithUsageObserver(func(value NoditUsage) {
		if value.Operation != "eth_getBlockReceipts" || value.Product != "evm_node" || value.CU != 28 {
			t.Errorf("incorrect block receipt CU: %+v", value)
		}
		usage.Add(value.CU)
	}))
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewMarketReadClient(primary, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.MarketBlockReceipts(context.Background(), strings.ToUpper(hash[2:]), "ethereum", ""); err == nil {
		t.Fatal("accepted hash without 0x prefix")
	}
	receipts, err := client.MarketBlockReceipts(context.Background(), "0x"+strings.ToUpper(hash[2:]), "ethereum", "")
	if err != nil || len(receipts) != 1 || receipts[0].(map[string]any)["blockHash"] != hash {
		t.Fatalf("block receipts: %v %v", receipts, err)
	}
	if calls.Load() != 2 || usage.Load() != 56 || primary.maxResponseBytes != 0 || client.Client.maxResponseBytes != MaxMarketRPCResponseBytes {
		t.Fatalf("calls=%d usage=%d primary bound=%d market bound=%d", calls.Load(), usage.Load(), primary.maxResponseBytes, client.Client.maxResponseBytes)
	}
}

func TestMarketBlockReceiptsValidatesResultEnvelope(t *testing.T) {
	for _, test := range []struct {
		name   string
		body   string
		accept bool
	}{
		{"empty block", `{"jsonrpc":"2.0","id":1,"result":[]}`, true},
		{"unavailable block", `{"jsonrpc":"2.0","id":1,"result":null}`, false},
		{"missing result", `{"jsonrpc":"2.0","id":1}`, false},
		{"unexpected result object", `{"jsonrpc":"2.0","id":1,"result":{}}`, false},
		{"unavailable receipt", `{"jsonrpc":"2.0","id":1,"result":[null]}`, false},
		{"incorrect ID", `{"jsonrpc":"2.0","id":2,"result":[]}`, false},
		{"missing protocol", `{"id":1,"result":[]}`, false},
		{"RPC error", `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method unavailable"}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			client, err := NewClient("local-only", "", WithRPCBaseURL(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			receipts, err := client.MarketBlockReceipts(context.Background(), "0x"+strings.Repeat("b", 64), "bsc", "")
			if test.accept {
				if err != nil || receipts == nil || len(receipts) != 0 {
					t.Fatalf("empty block: %#v %v", receipts, err)
				}
			} else if err == nil || receipts != nil {
				t.Fatalf("unexpected accepted block receipts: %#v %v", receipts, err)
			}
		})
	}
}

func TestMarketBlockReceiptsUsesSmallerWireBoundAndNoExternalFallback(t *testing.T) {
	primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":[{"logs":"`)
		_, _ = io.CopyN(w, repeatingMarketReader{}, maxMarketBlockReadResponseBytes+1)
		fmt.Fprint(w, `"}]}`)
	}))
	defer primaryServer.Close()
	var fallbackCalls atomic.Int64
	backup := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { fallbackCalls.Add(1) }))
	defer backup.Close()
	primary, err := NewClient("local-only", "", WithRPCBaseURL(primaryServer.URL))
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewMarketReadClient(primary, map[string][]string{"bsc": {backup.URL}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.MarketBlockReceipts(context.Background(), "0x"+strings.Repeat("b", 64), "bsc", "")
	if !errors.Is(err, ErrMarketRPCResponseTooLarge) || fallbackCalls.Load() != 0 {
		t.Fatalf("wire bound or fallback routing: %v %d", err, fallbackCalls.Load())
	}
	_, err = client.MarketFullBlock(context.Background(), 42, "bsc", "")
	if !errors.Is(err, ErrMarketRPCResponseTooLarge) || fallbackCalls.Load() != 0 {
		t.Fatalf("full block wire bound or fallback routing: %v %d", err, fallbackCalls.Load())
	}
}

func TestMarketFullBlockUsesPrimaryFullTransactionRequest(t *testing.T) {
	var calls, usage atomic.Int64
	primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Method != "eth_getBlockByNumber" ||
			len(request.Params) != 2 || request.Params[0] != "0x2a" || request.Params[1] != true {
			t.Errorf("unexpected full block request: %+v %v", request, err)
		}
		if calls.Add(1) == 1 {
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x2a","transactions":[]}}`)
			return
		}
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x2a"}}`)
	}))
	defer primaryServer.Close()
	var fallbackCalls atomic.Int64
	backup := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { fallbackCalls.Add(1) }))
	defer backup.Close()
	primary, err := NewClient("local-only", "", WithRPCBaseURL(primaryServer.URL), WithUsageObserver(func(value NoditUsage) {
		if value.Operation != "eth_getBlockByNumber" || value.Product != "evm_node" || value.CU != 31 {
			t.Errorf("incorrect full block CU: %+v", value)
		}
		usage.Add(value.CU)
	}))
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewMarketReadClient(primary, map[string][]string{"bsc": {backup.URL}})
	if err != nil {
		t.Fatal(err)
	}
	block, err := client.MarketFullBlock(context.Background(), 42, "bsc", "")
	if err != nil || block["number"] != "0x2a" {
		t.Fatalf("full block: %v %v", block, err)
	}
	if block, err = client.MarketFullBlock(context.Background(), 42, "bsc", ""); err == nil || block != nil {
		t.Fatalf("accepted full block without transactions: %v %v", block, err)
	}
	if calls.Load() != 2 || usage.Load() != 62 || fallbackCalls.Load() != 0 {
		t.Fatalf("calls=%d usage=%d fallback=%d", calls.Load(), usage.Load(), fallbackCalls.Load())
	}
}
