package chain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type testBlockPairDoer func(*http.Request) (*http.Response, error)

func (d testBlockPairDoer) Do(r *http.Request) (*http.Response, error) { return d(r) }

const testBlockPairHash = "0x0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestMarketBlockPairCombinesAdmissionAndCountsItems(t *testing.T) {
	var charged []NoditUsage
	var requests int
	transport := testBlockPairDoer(func(r *http.Request) (*http.Response, error) {
		requests++
		var items []struct {
			Version string `json:"jsonrpc"`
			ID      int    `json:"id"`
			Method  string `json:"method"`
			Params  []any  `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&items); err != nil || len(items) != 2 ||
			items[0].Version != "2.0" || items[1].Version != "2.0" || items[0].ID != 1 || items[1].ID != 2 ||
			items[0].Method != "eth_getBlockByNumber" || items[1].Method != "eth_getBlockReceipts" ||
			len(items[0].Params) != 2 || items[0].Params[0] != "0x2a" || items[0].Params[1] != true ||
			len(items[1].Params) != 1 || items[1].Params[0] != testBlockPairHash || r.URL.Host != "base-mainnet.nodit.io" {
			t.Fatalf("unexpected pair request: %v %v", items, err)
		}
		body := fmt.Sprintf(`[{"jsonrpc":"2.0","id":2,"result":[{"blockHash":%q}]},{"jsonrpc":"2.0","id":1,"result":{"hash":%q,"number":"0x2a","transactions":[{}]}}]`, testBlockPairHash, testBlockPairHash)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	client, err := NewClient("in-memory-only", "", WithHTTPClient(transport), WithCURateLimit(1), WithUsageObserver(func(u NoditUsage) { charged = append(charged, u) }))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	block, receipts, err := client.MarketBlockAndReceipts(ctx, 42, testBlockPairHash, "base", "")
	if err != nil || requests != 1 || len(receipts) != 1 || block["hash"] != testBlockPairHash {
		t.Fatalf("pair: %v %v %v, requests=%d", block, receipts, err, requests)
	}
	if len(charged) != 2 || charged[0].Operation != "eth_getBlockByNumber" || charged[0].CU != 31 || charged[1].Operation != "eth_getBlockReceipts" || charged[1].CU != 28 || client.cuLimiter.next.Sub(start) < 59*time.Second {
		t.Fatalf("incorrect combined admission: %v %v", charged, client.cuLimiter.next.Sub(start))
	}
}

func TestMarketBlockPairDecodesStrictObjectsAndBounds(t *testing.T) {
	const id1 = `{"jsonrpc":"2.0","id":1,"result":{"transactions":[]}}`
	const id2 = `{"jsonrpc":"2.0","id":2,"result":[]}`
	for _, test := range []struct {
		name, wire string
		wantError  error
		accepted   bool
	}{
		{name: "empty block", wire: `[` + id1 + `,` + id2 + `]`, accepted: true},
		{name: "top-level error", wire: `{"jsonrpc":"2.0","id":null,"error":{"code":-32600}}`},
		{name: "unknown item", wire: `[` + id1 + `,{"jsonrpc":"2.0","id":2,"error":{"code":-32601}}]`, wantError: ErrMarketBlockBatchUnsupported},
		{name: "duplicate IDs", wire: `[` + id1 + `,` + id1 + `]`},
		{name: "string ID", wire: `[` + id1 + `,` + strings.Replace(id2, `"id":2`, `"id":"2"`, 1) + `]`},
		{name: "unsupported version", wire: `[` + id1 + `,` + strings.Replace(id2, `"2.0"`, `"1.0"`, 1) + `]`},
		{name: "third reply", wire: `[` + id1 + `,` + id2 + `,{}]`},
		{name: "trailing reply", wire: `[` + id1 + `,` + id2 + `][]`},
		{name: "invalid receipt", wire: `[` + id1 + `,` + strings.Replace(id2, `[]`, `[null]`, 1) + `]`},
		{name: "oversize one item", wire: `[{"jsonrpc":"2.0","id":1,"result":{"transactions":[],"pad":"` + strings.Repeat("x", int(maxMarketBlockReadResponseBytes)) + `"}},` + id2 + `]`, wantError: ErrMarketRPCResponseTooLarge},
		{name: "oversize wire", wire: `[` + id1 + `,` + id2 + `]` + strings.Repeat(" ", int(2*maxMarketBlockReadResponseBytes)), wantError: ErrMarketRPCResponseTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			var receipts []any
			block, err := decodeMarketBlockPairInto(strings.NewReader(test.wire), &receipts)
			if test.accepted {
				if err != nil || block == nil || receipts == nil || len(receipts) != 0 {
					t.Fatalf("empty block: %v/%v %v", block, receipts, err)
				}
				return
			}
			if err == nil || block != nil || test.wantError != nil && !errors.Is(err, test.wantError) {
				t.Fatalf("unexpected result: %v/%v %v", block, receipts, err)
			}
		})
	}
}

func TestMarketBlockPairReturnsFailuresWithoutRetry(t *testing.T) {
	for _, status := range []int{http.StatusMethodNotAllowed, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			requests := 0
			var charged int64
			transport := testBlockPairDoer(func(*http.Request) (*http.Response, error) {
				requests++
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{"Retry-After": []string{"1"}}}, nil
			})
			client, err := NewClient("in-memory-only", "", WithHTTPClient(transport), WithCURateLimit(550), WithUsageObserver(func(u NoditUsage) { charged += u.CU }))
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = client.MarketBlockAndReceipts(context.Background(), 42, testBlockPairHash, "base", "")
			if err == nil || requests != 1 || charged != 59 || status == http.StatusMethodNotAllowed && !errors.Is(err, ErrMarketBlockBatchUnsupported) || status == http.StatusTooManyRequests && client.cuLimiter.blockedUntil.Before(time.Now()) {
				t.Fatalf("failure route: calls=%d charged=%d %v", requests, charged, err)
			}
		})
	}
}

func TestMarketSingletonPairUsesOne47CUAdmission(t *testing.T) {
	const txHash = "0xabcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	blockJSON := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":{"hash":%q,"number":"0x2a","transactions":[{}]}}`, testBlockPairHash)
	receiptJSON := fmt.Sprintf(`{"transactionHash":%q,"blockHash":%q,"blockNumber":"0x2a"}`, txHash, testBlockPairHash)
	for _, test := range []struct {
		name, receipt string
		success       bool
	}{
		{"valid out-of-order pair", receiptJSON, true},
		{"array instead of receipt object", `[]`, false},
		{"null receipt", `null`, false},
		{"wrong transaction", strings.Replace(receiptJSON, txHash, testBlockPairHash, 1), false},
		{"wrong block hash", strings.Replace(receiptJSON, testBlockPairHash, txHash, 1), false},
		{"wrong block number", strings.Replace(receiptJSON, `"0x2a"`, `"0x29"`, 1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var called int
			var usage []NoditUsage
			transport := testBlockPairDoer(func(r *http.Request) (*http.Response, error) {
				called++
				var request []struct {
					ID     int    `json:"id"`
					Method string `json:"method"`
					Params []any  `json:"params"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request) != 2 ||
					request[0].ID != 1 || request[0].Method != "eth_getBlockByNumber" ||
					request[1].ID != 2 || request[1].Method != "eth_getTransactionReceipt" ||
					len(request[1].Params) != 1 || request[1].Params[0] != txHash {
					t.Fatalf("incorrect singleton pair request: %v %v", request, err)
				}
				wire := `[{"jsonrpc":"2.0","id":2,"result":` + test.receipt + `},` + blockJSON + `]`
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(wire))}, nil
			})
			client, err := NewClient("in-memory-only", "", WithHTTPClient(transport), WithCURateLimit(1), WithUsageObserver(func(u NoditUsage) { usage = append(usage, u) }))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			started := time.Now()
			block, receipt, err := client.MarketBlockAndTransactionReceipt(ctx, 42, testBlockPairHash, txHash, "base", "")
			if test.success {
				if err != nil || block["hash"] != testBlockPairHash || receipt["transactionHash"] != txHash {
					t.Fatalf("unexpected pair: %v %v %v", block, receipt, err)
				}
			} else if err == nil || block != nil || receipt != nil {
				t.Fatalf("invalid result did not cleanly fall back: %v %v %v", block, receipt, err)
			}
			if called != 1 || len(usage) != 2 || usage[0].Operation != "eth_getBlockByNumber" || usage[0].CU != 31 ||
				usage[1].Operation != "eth_getTransactionReceipt" || usage[1].CU != 16 || client.cuLimiter.next.Sub(started) < 47*time.Second {
				t.Fatalf("incorrect singleton counts: %v %v, next=%v", called, usage, client.cuLimiter.next.Sub(started))
			}
		})
	}
}
