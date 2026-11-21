package chain

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type walletRPCRequest struct {
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
}

func walletTestWord(value string) string {
	number, ok := new(big.Int).SetString(value, 10)
	if !ok {
		panic("invalid test integer")
	}
	return fmt.Sprintf("0x%064x", number)
}

func walletTestSymbol(value string, dynamic bool) string {
	if !dynamic {
		return "0x" + hex.EncodeToString(append([]byte(value), make([]byte, 32-len(value))...))
	}
	raw := append([]byte(value), make([]byte, (32-len(value)%32)%32)...)
	return walletTestWord("32") + strings.TrimPrefix(walletTestWord(fmt.Sprint(len(value))), "0x") + hex.EncodeToString(raw)
}

func walletTestCall(t *testing.T, r *http.Request) (string, string) {
	t.Helper()
	var request walletRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		t.Errorf("decode RPC: %v", err)
		return "", ""
	}
	if request.Method != "eth_call" || len(request.Params) != 2 {
		t.Errorf("unexpected RPC: %#v", request)
		return "", ""
	}
	var call map[string]string
	if err := json.Unmarshal(request.Params[0], &call); err != nil {
		t.Errorf("decode call: %v", err)
	}
	if string(request.Params[1]) != `"latest"` {
		t.Errorf("block param=%s", request.Params[1])
	}
	return call["data"], call["to"]
}

func walletTestReply(w http.ResponseWriter, result any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
}

func TestWalletTokenRPCBalancesAcrossAllChainsAndPrecisions(t *testing.T) {
	for _, profile := range SupportedChains() {
		for _, decimals := range []int{0, 6, 8, 18, 255} {
			t.Run(fmt.Sprintf("%s/%d", profile.Key, decimals), func(t *testing.T) {
				var calls atomic.Int64
				var units atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.URL.Path != "/" || r.Header.Get("X-API-KEY") != "wallet-test" {
						t.Errorf("wrong transport path or key")
					}
					data, to := walletTestCall(t, r)
					if to != testToken {
						t.Errorf("to=%s", to)
					}
					switch data {
					case "0x313ce567":
						walletTestReply(w, walletTestWord(fmt.Sprint(decimals)))
					case "0x95d89b41":
						walletTestReply(w, walletTestSymbol("USDT", true))
					case "0x70a08231" + strings.Repeat("0", 24) + testWallet[2:]:
						walletTestReply(w, walletTestWord("12345678"))
					default:
						t.Errorf("unexpected data=%s", data)
						w.WriteHeader(http.StatusBadRequest)
					}
				}))
				defer server.Close()
				primary, _ := NewClient("wallet-test", "http://data.invalid", WithRPCBaseURL(server.URL), WithUsageObserver(func(u NoditUsage) {
					if u.Product != "evm_node" || u.Operation != "eth_call" {
						t.Errorf("usage=%#v", u)
					}
					units.Add(u.CU)
				}))
				wallet := NewWalletReadClient(primary)
				got, err := wallet.Balance(context.Background(), testWallet, testToken, profile.Key, "")
				want, _ := FormatUnits("12345678", decimals)
				if err != nil || got.Value != want || got.Symbol != "USDT" || got.Decimals != decimals || got.ChainID != profile.ChainID || got.WalletAddress != testWallet || got.TokenAddress == nil || *got.TokenAddress != testToken || got.ReadSource != WalletBalanceReadSource || got.ReadDecimals == nil || *got.ReadDecimals != decimals {
					t.Fatalf("balance=%#v err=%v want=%s", got, err, want)
				}
				if calls.Load() != 3 || units.Load() != 69 {
					t.Fatalf("calls=%d CU=%d", calls.Load(), units.Load())
				}
			})
		}
	}
}

func TestWalletTokenRPCUsesChainNodeEndpoints(t *testing.T) {
	for _, profile := range SupportedChains() {
		t.Run(profile.Key, func(t *testing.T) {
			primary, _ := NewClient("test", "https://data.invalid", WithHTTPClient(walletBudgetDoer(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != "https://"+profile.Chain+"-mainnet.nodit.io" {
					t.Errorf("endpoint=%s", r.URL)
				}
				data, _ := walletTestCall(t, r)
				result := walletTestWord("0")
				if data == "0x95d89b41" {
					result = walletTestSymbol("T", false)
				}
				body, _ := json.Marshal(map[string]any{"result": result})
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})))
			got, err := NewWalletReadClient(primary).TokenBalance(context.Background(), testWallet, testToken, profile.Key, "")
			if err != nil || got.Value != "0" || got.ReadDecimals == nil || *got.ReadDecimals != 0 {
				t.Fatalf("zero=%#v err=%v", got, err)
			}
		})
	}
}

func TestWalletTokenRPCRejectsUnknownBalancesAndPrecision(t *testing.T) {
	for _, test := range []struct {
		name     string
		invalid  any
		decimals bool
	}{
		{"missing", nil, false}, {"empty", "0x", false}, {"short", "0x00", false},
		{"nonhex", "0x" + strings.Repeat("z", 64), false}, {"long", "0x" + strings.Repeat("0", 128), false},
		{"negative", "0x-" + strings.Repeat("0", 63), false}, {"number", 123, false},
		{"missing-decimals", nil, true}, {"decimals-over-255", walletTestWord("256"), true},
		{"nonstandard-decimals", "0x" + strings.Repeat("0", 192), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, _ := walletTestCall(t, r)
				if (data == "0x313ce567") == test.decimals && data != "0x95d89b41" {
					walletTestReply(w, test.invalid)
					return
				}
				if data == "0x313ce567" {
					walletTestReply(w, walletTestWord("18"))
				} else if data == "0x95d89b41" {
					walletTestReply(w, walletTestSymbol("T", false))
				} else {
					walletTestReply(w, walletTestWord("0"))
				}
			}))
			defer server.Close()
			primary, _ := NewClient("test", "http://data.invalid", WithRPCBaseURL(server.URL))
			got, err := NewWalletReadClient(primary).TokenBalance(context.Background(), testWallet, testToken, "bsc", "")
			if err == nil || got.Value != "" || got.ReadSource != "" {
				t.Fatalf("accepted invalid balance=%#v err=%v", got, err)
			}
		})
	}
}

func TestWalletTokenSymbolBoundsAndFallback(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  any
		want string
	}{
		{"bytes32", walletTestSymbol("USDT", false), "USDT"},
		{"dynamic", walletTestSymbol("币", true), "币"},
		{"max", walletTestSymbol(strings.Repeat("T", 128), true), strings.Repeat("T", 128)},
		{"missing", nil, "TOKEN"}, {"empty", "0x", "TOKEN"},
		{"too-long", walletTestSymbol(strings.Repeat("T", 129), true), "TOKEN"},
		{"control", walletTestSymbol("T\nX", true), "TOKEN"},
		{"bad-utf8", walletTestSymbol(string([]byte{0xff}), true), "TOKEN"},
		{"invalid-offset", "0x" + strings.TrimPrefix(walletTestWord("64"), "0x") + strings.TrimPrefix(walletTestWord("1"), "0x") + strings.Repeat("0", 64), "TOKEN"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var symbolCalls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, _ := walletTestCall(t, r)
				if data == "0x313ce567" {
					walletTestReply(w, walletTestWord("0"))
				} else if data == "0x95d89b41" {
					symbolCalls.Add(1)
					walletTestReply(w, test.raw)
				} else {
					walletTestReply(w, walletTestWord("7"))
				}
			}))
			defer server.Close()
			primary, _ := NewClient("test", "http://data.invalid", WithRPCBaseURL(server.URL))
			wallet := NewWalletReadClient(primary)
			for i := 0; i < 2; i++ {
				got, err := wallet.TokenBalance(context.Background(), testWallet, testToken, "bsc", "")
				if err != nil || got.Symbol != test.want || got.Value != "7" {
					t.Fatalf("balance=%#v err=%v", got, err)
				}
			}
			if symbolCalls.Load() != 1 {
				t.Fatalf("optional metadata refetched %d times", symbolCalls.Load())
			}
		})
	}
}

func TestWalletTokenMetadataCacheIsSharedOnlyByChainAndTokenAndExpires(t *testing.T) {
	var decimalsCalls, balanceCalls atomic.Int64
	var currentDecimals atomic.Int64
	currentDecimals.Store(6)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := walletTestCall(t, r)
		switch data {
		case "0x313ce567":
			decimalsCalls.Add(1)
			walletTestReply(w, walletTestWord(fmt.Sprint(currentDecimals.Load())))
		case "0x95d89b41":
			walletTestReply(w, walletTestSymbol("USDT", false))
		default:
			balanceCalls.Add(1)
			walletTestReply(w, walletTestWord("12345678"))
		}
	}))
	defer server.Close()
	primary, _ := NewClient("test", "http://data.invalid", WithRPCBaseURL(server.URL))
	wallet := NewWalletReadClient(primary)
	now := time.Now()
	wallet.now = func() time.Time { return now }
	first, err := wallet.TokenBalance(context.Background(), testWallet, testToken, "bnb", "")
	if err != nil {
		t.Fatal(err)
	}
	// A caller cannot mutate cached precision through returned evidence.
	*first.ReadDecimals = 99
	second, err := wallet.TokenBalance(context.Background(), testTarget, strings.ToUpper(testToken[:2])+testToken[2:], "bsc", "")
	// Address validation intentionally requires lowercase 0x, as the existing API does.
	if err == nil {
		t.Fatal("accepted uppercase 0X prefix")
	}
	second, err = wallet.TokenBalance(context.Background(), testTarget, testToken, "bsc", "")
	if err != nil || second.Decimals != 6 || *second.ReadDecimals != 6 {
		t.Fatalf("shared=%#v err=%v", second, err)
	}
	if _, err = wallet.TokenBalance(context.Background(), testWallet, testToken, "base", ""); err != nil {
		t.Fatal(err)
	}
	if decimalsCalls.Load() != 2 || balanceCalls.Load() != 3 {
		t.Fatalf("cache metadata=%d balance=%d", decimalsCalls.Load(), balanceCalls.Load())
	}
	currentDecimals.Store(8)
	now = now.Add(walletTokenMetadataTTL)
	expired, err := wallet.TokenBalance(context.Background(), testWallet, testToken, "bsc", "")
	if err != nil || expired.Decimals != 8 || *expired.ReadDecimals != 8 || decimalsCalls.Load() != 3 {
		t.Fatalf("expired=%#v err=%v reads=%d", expired, err, decimalsCalls.Load())
	}
}

func TestWalletTokenMetadataCacheHasFiniteKeysBytesAndTTL(t *testing.T) {
	primary, _ := NewClient("test", "http://invalid")
	wallet := NewWalletReadClient(primary)
	now := time.Now()
	wallet.now = func() time.Time { return now }
	for i := 0; i < walletTokenMetadataMaxKeys+37; i++ {
		wallet.storeMetadata(walletTokenMetadataKey{chainID: 56, token: fmt.Sprintf("0x%040x", i)}, walletTokenMetadata{decimals: 18, symbol: strings.Repeat("T", 128), expiresAt: now.Add(time.Minute)})
	}
	if len(wallet.metadata) > walletTokenMetadataMaxKeys || wallet.metadataBytes > walletTokenMetadataMaxBytes {
		t.Fatalf("unbounded keys=%d bytes=%d", len(wallet.metadata), wallet.metadataBytes)
	}
	now = now.Add(2 * time.Minute)
	wallet.storeMetadata(walletTokenMetadataKey{chainID: 56, token: testToken}, walletTokenMetadata{symbol: "T", expiresAt: now.Add(time.Minute)})
	if len(wallet.metadata) != 1 || wallet.metadataBytes != metadataBytes(walletTokenMetadataKey{chainID: 56, token: testToken}, walletTokenMetadata{symbol: "T"}) {
		t.Fatalf("expired metadata retained: keys=%d bytes=%d", len(wallet.metadata), wallet.metadataBytes)
	}
}

func TestWalletTokenRPCCancelResponseCapAndPrimaryIsolation(t *testing.T) {
	var dataCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "getTokensOwnedByAccount") {
			dataCalls.Add(1)
			_, _ = io.WriteString(w, `{"items":[]}`)
			return
		}
		data, _ := walletTestCall(t, r)
		if data == "0x313ce567" {
			walletTestReply(w, walletTestWord("18"))
			return
		}
		_, _ = io.WriteString(w, `{"result":"`+strings.Repeat("0", int(walletTokenRPCResponseBytes))+`"}`)
	}))
	defer server.Close()
	primary, _ := NewClient("test", server.URL, WithRPCBaseURL(server.URL))
	wallet := NewWalletReadClient(primary)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := wallet.TokenBalance(ctx, testWallet, testToken, "bsc", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	if _, err := wallet.TokenBalance(context.Background(), testWallet, testToken, "bsc", ""); !errors.Is(err, ErrMarketRPCResponseTooLarge) {
		t.Fatalf("oversize=%v", err)
	}
	legacy, err := primary.TokenBalance(context.Background(), testWallet, testToken, "bsc", "")
	if err != nil || legacy.Value != "0" || legacy.ReadSource != "" || legacy.ReadDecimals != nil || dataCalls.Load() != 1 || primary.maxResponseBytes != 0 {
		t.Fatalf("primary changed: %#v err=%v calls=%d", legacy, err, dataCalls.Load())
	}
}

func TestWalletTokenRPCFailureNeverFallsBackAndSharesAttemptMeter(t *testing.T) {
	var calls, units atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/" {
			t.Errorf("Data fallback=%s", r.URL.Path)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	primary, _ := NewClient("test", server.URL, WithRPCBaseURL(server.URL), WithUsageObserver(func(u NoditUsage) { units.Add(u.CU) }))
	_, err := NewWalletReadClient(primary).TokenBalance(context.Background(), testWallet, testToken, "bsc", "")
	if err == nil || calls.Load() != 3 || units.Load() != 69 {
		t.Fatalf("err=%v calls=%d CU=%d", err, calls.Load(), units.Load())
	}
}

func TestWalletTokenRPCMetadataConcurrency(t *testing.T) {
	var decimalsCalls, symbolCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := walletTestCall(t, r)
		if data == "0x95d89b41" {
			symbolCalls.Add(1)
			walletTestReply(w, walletTestSymbol("T", false))
		} else {
			if data == "0x313ce567" {
				decimalsCalls.Add(1)
			}
			walletTestReply(w, walletTestWord("0"))
		}
	}))
	defer server.Close()
	primary, _ := NewClient("test", "http://data.invalid", WithRPCBaseURL(server.URL))
	wallet := NewWalletReadClient(primary)
	var workers sync.WaitGroup
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 3; j++ {
				got, err := wallet.TokenBalance(context.Background(), testWallet, testToken, "bsc", "")
				if err != nil || got.Value != "0" {
					t.Errorf("concurrent=%#v err=%v", got, err)
				}
			}
		}()
	}
	workers.Wait()
	if len(wallet.metadata) != 1 || wallet.metadataBytes > walletTokenMetadataMaxBytes || len(wallet.metadataInFlight) != 0 || decimalsCalls.Load() != 1 || symbolCalls.Load() != 1 {
		t.Fatalf("cache=%d bytes=%d", len(wallet.metadata), wallet.metadataBytes)
	}
}

func TestWalletTokenMetadataCancellationErrorAndPanicReleaseInflight(t *testing.T) {
	for _, kind := range []string{"cancel-decimals", "cancel-symbol", "rpc-error", "panic"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			primary, _ := NewClient("test", "http://data.invalid", WithHTTPClient(walletBudgetDoer(func(r *http.Request) (*http.Response, error) {
				data, _ := walletTestCall(t, r)
				if kind == "panic" {
					panic("test transport panic")
				}
				body := `{"result":"` + walletTestWord("18") + `"}`
				if kind == "rpc-error" {
					body = `{"error":{"message":"method unavailable"}}`
				}
				if kind == "cancel-decimals" || kind == "cancel-symbol" && data == "0x95d89b41" {
					cancel()
				}
				if data == "0x95d89b41" {
					body = `{"result":"` + walletTestSymbol("T", false) + `"}`
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})))
			wallet := NewWalletReadClient(primary)
			if kind == "panic" {
				panicked := false
				func() {
					defer func() { panicked = recover() != nil }()
					_, _ = wallet.TokenBalance(ctx, testWallet, testToken, "bsc", "")
				}()
				if !panicked {
					t.Fatal("transport panic unexpectedly swallowed")
				}
			} else {
				got, err := wallet.TokenBalance(ctx, testWallet, testToken, "bsc", "")
				if err == nil || got.Value != "" {
					t.Fatalf("balance=%#v err=%v", got, err)
				}
				if strings.HasPrefix(kind, "cancel") && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel=%v", err)
				}
			}
			if len(wallet.metadataInFlight) != 0 || len(wallet.metadata) != 0 {
				t.Fatalf("retained cache=%d flights=%d", len(wallet.metadata), len(wallet.metadataInFlight))
			}
		})
	}
}

func TestWalletTokenMetadataFollowerRespectsOwnCancellationAndCapacity(t *testing.T) {
	primary, _ := NewClient("test", "http://data.invalid", WithHTTPClient(walletBudgetDoer(func(*http.Request) (*http.Response, error) {
		t.Error("capacity/wait issued HTTP")
		return nil, errors.New("unexpected HTTP")
	})))
	wallet := NewWalletReadClient(primary)
	for i := 0; i < walletTokenMetadataMaxInFlight; i++ {
		wallet.metadataInFlight[walletTokenMetadataKey{chainID: 56, token: fmt.Sprintf("0x%040x", i)}] = &walletTokenMetadataRead{done: make(chan struct{})}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := wallet.TokenBalance(ctx, testWallet, "0x0000000000000000000000000000000000000001", "bsc", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("follower cancellation=%v", err)
	}
	if _, err := wallet.TokenBalance(context.Background(), testWallet, testToken, "bsc", ""); err == nil || !strings.Contains(err.Error(), "capacity busy") {
		t.Fatalf("capacity err=%v", err)
	}
	if len(wallet.metadataInFlight) != walletTokenMetadataMaxInFlight {
		t.Fatalf("unbounded flights=%d", len(wallet.metadataInFlight))
	}
}

func TestWalletTokenRPCOptionalSymbolErrorDoesNotInventBalanceOrRepeat(t *testing.T) {
	var symbols atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := walletTestCall(t, r)
		switch data {
		case "0x95d89b41":
			symbols.Add(1)
			_, _ = io.WriteString(w, `{"error":{"message":"symbol not implemented"}}`)
		case "0x313ce567":
			walletTestReply(w, walletTestWord("6"))
		default:
			walletTestReply(w, walletTestWord("12000000"))
		}
	}))
	defer server.Close()
	primary, _ := NewClient("test", "http://data.invalid", WithRPCBaseURL(server.URL))
	wallet := NewWalletReadClient(primary)
	for i := 0; i < 2; i++ {
		got, err := wallet.TokenBalance(context.Background(), testWallet, testToken, "bsc", "")
		if err != nil || got.Value != "12" || got.Symbol != "TOKEN" || got.ReadSource != WalletBalanceReadSource {
			t.Fatalf("balance=%#v err=%v", got, err)
		}
	}
	if symbols.Load() != 1 {
		t.Fatalf("retried optional metadata=%d", symbols.Load())
	}
}

func TestWalletTokenRPCRateLimitBackoffIsSharedWithPrimary(t *testing.T) {
	var calls, units atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	primary, _ := NewClient("test", server.URL, WithRPCBaseURL(server.URL), WithCURateLimit(1000000), WithUsageObserver(func(u NoditUsage) { units.Add(u.CU) }))
	wallet := NewWalletReadClient(primary)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	_, err := wallet.TokenBalance(ctx, testWallet, testToken, "bsc", "")
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wallet limit=%v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, err = primary.NativeBalance(ctx, testWallet, "bsc", "")
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 || units.Load() != 23 || len(wallet.metadataInFlight) != 0 {
		t.Fatalf("primary limit=%v HTTP=%d CU=%d flights=%d", err, calls.Load(), units.Load(), len(wallet.metadataInFlight))
	}
}
