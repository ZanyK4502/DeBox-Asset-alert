package chain

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTokenDecimalsReadsOnlyStandardSelector(t *testing.T) {
	for _, value := range []int32{0, 6, 18, 255} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var request struct {
					Method string            `json:"method"`
					Params []json.RawMessage `json:"params"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				if len(request.Params) != 2 {
					t.Errorf("decimals params = %d, want 2", len(request.Params))
					return
				}
				var call map[string]string
				_ = json.Unmarshal(request.Params[0], &call)
				if request.Method != "eth_call" || call["to"] != testToken || call["data"] != "0x313ce567" || string(request.Params[1]) != `"latest"` {
					t.Errorf("unexpected decimals request: %s %+v", request.Method, call)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": fmt.Sprintf("0x%064x", value)})
			}))
			defer server.Close()
			client, err := NewClient("fixture", server.URL, WithRPCBaseURL(server.URL), WithHTTPClient(server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.TokenDecimals(context.Background(), strings.ToUpper(testToken[:2])+testToken[2:], "base", "")
			// ValidateAddress deliberately requires the conventional lowercase 0x prefix.
			if err == nil {
				t.Fatal("accepted invalid address prefix")
			}
			got, err = client.TokenDecimals(context.Background(), testToken, "base", "")
			if err != nil || got != value || calls != 1 {
				t.Fatalf("decimals = %d, error = %v, requests = %d", got, err, calls)
			}
		})
	}
}

func TestTokenDecimalsRejectsUnavailableOrInvalidResponses(t *testing.T) {
	for _, result := range []any{"0x", "0x06", "0x" + strings.Repeat("f", 64), fmt.Sprintf("0x%064x", 256), nil, 6} {
		t.Run(fmt.Sprintf("%v", result), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
			}))
			defer server.Close()
			client, _ := NewClient("fixture", server.URL, WithRPCBaseURL(server.URL), WithHTTPClient(server.Client()))
			if _, err := client.TokenDecimals(context.Background(), testToken, "bsc", ""); err == nil {
				t.Fatal("invalid decimals accepted")
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"message":"execution reverted"}}`)
	}))
	defer server.Close()
	client, _ := NewClient("fixture", server.URL, WithRPCBaseURL(server.URL), WithHTTPClient(server.Client()))
	if _, err := client.TokenDecimals(context.Background(), testToken, "bsc", ""); err == nil {
		t.Fatal("RPC failure accepted")
	}
}

func TestTokenDecimalsNativeCurrencyDoesNotCallRPC(t *testing.T) {
	client, _ := NewClient("fixture", "http://127.0.0.1:1", WithRPCBaseURL("http://127.0.0.1:1"))
	for _, profile := range SupportedChains() {
		got, err := client.TokenDecimals(context.Background(), "0x0000000000000000000000000000000000000000", profile.Key, "")
		if err != nil || got != 18 {
			t.Fatalf("native decimals on %s: %d, %v", profile.Key, got, err)
		}
	}
	if _, err := client.TokenDecimals(context.Background(), testToken, "unknown", ""); err == nil {
		t.Fatal("unsupported chain accepted")
	}
}
