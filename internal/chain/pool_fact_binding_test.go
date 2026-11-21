package chain

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestImmutablePoolBindingUsesFinalizedFactoryMapping(t *testing.T) {
	pool := "0x" + strings.Repeat("1", 40)
	token0 := "0x" + strings.Repeat("2", 40)
	token1 := "0x" + strings.Repeat("3", 40)
	factory := "0x" + strings.Repeat("4", 40)
	for _, version := range []string{"v2", "v3"} {
		t.Run(version, func(t *testing.T) {
			var calls, cu atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request walletRPCRequest
				if json.NewDecoder(r.Body).Decode(&request) != nil || request.Method != "eth_call" || len(request.Params) != 2 || string(request.Params[1]) != `"finalized"` {
					t.Error("binding must be readonly finalized eth_call")
					http.Error(w, "bad fixture request", 400)
					return
				}
				var call map[string]string
				_ = json.Unmarshal(request.Params[0], &call)
				calls.Add(1)
				if call["data"] == "0xddca3f43" {
					if version != "v3" || call["to"] != pool {
						t.Error("unexpected fee request")
					}
					walletTestReply(w, walletTestWord("3000"))
					return
				}
				selector := "0xe6a43905"
				if version == "v3" {
					selector = "0x1698ee82"
				}
				expected := selector + strings.Repeat("0", 24) + token0[2:] + strings.Repeat("0", 24) + token1[2:]
				if version == "v3" {
					expected += fmt.Sprintf("%064x", 3000)
				}
				if call["to"] != factory || call["data"] != expected {
					t.Error("factory binding ABI differs", call)
				}
				walletTestReply(w, "0x"+strings.Repeat("0", 24)+pool[2:])
			}))
			defer server.Close()
			client, _ := NewClient("local-only", server.URL, WithRPCBaseURL(server.URL), WithUsageObserver(func(u NoditUsage) { cu.Add(u.CU) }))
			bound, err := client.ImmutablePoolBinding(context.Background(), pool, token0, token1, factory, version, "bsc")
			want := int64(1)
			if version == "v3" {
				want = 2
			}
			if err != nil || !bound || calls.Load() != want || cu.Load() != 23*want || client.maxResponseBytes != 0 || client.PoolFactsCacheIdentity() != client {
				t.Fatalf("binding failed/isolation changed: bound=%t err=%v calls=%d CU=%d", bound, err, calls.Load(), cu.Load())
			}
		})
	}
}

func TestImmutablePoolBindingRejectsMalformedWordFeeOrMismatchedPool(t *testing.T) {
	pool := "0x" + strings.Repeat("1", 40)
	token0 := "0x" + strings.Repeat("2", 40)
	token1 := "0x" + strings.Repeat("3", 40)
	factory := "0x" + strings.Repeat("4", 40)
	for _, test := range []struct {
		name, version, response string
		wantErr                 bool
	}{
		{"short", "v2", "0x00", true}, {"long", "v2", "0x" + strings.Repeat("0", 128), true},
		{"dirty_high_address", "v2", "0x01" + strings.Repeat("0", 62), true},
		{"not_hex", "v2", "0x" + strings.Repeat("g", 64), true},
		{"different_pool", "v2", "0x" + strings.Repeat("0", 24) + factory[2:], false},
		{"excessive_fee", "v3", walletTestWord("16777216"), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { walletTestReply(w, test.response) }))
			defer server.Close()
			client, _ := NewClient("local-only", server.URL, WithRPCBaseURL(server.URL))
			bound, err := client.ImmutablePoolBinding(context.Background(), pool, token0, token1, factory, test.version, "bsc")
			if bound || (err != nil) != test.wantErr {
				t.Fatalf("invalid proof accepted: %t %v", bound, err)
			}
		})
	}
}
