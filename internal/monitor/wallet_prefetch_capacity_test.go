package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
)

// Opt-in local capacity acceptance; the ordinary suite does not repeatedly
// spend40seconds sleeping. Every endpoint is a local httptest server.
func TestWalletPrefetchHundredColdTokensWithHTTP200ms(t *testing.T) {
	if os.Getenv("DEBOX_WALLET_PREFETCH_CAPACITY_RUN") != "1" {
		t.Skip("opt-in local latency acceptance")
	}
	var requests, decimals, symbols, balances, active, peak, units atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); n > previous; previous = peak.Load() {
			if peak.CompareAndSwap(previous, n) {
				break
			}
		}
		requests.Add(1)
		var request struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Method != "eth_call" || len(request.Params) != 2 {
			http.Error(w, "only local RPC allowed", 400)
			return
		}
		var call map[string]string
		_ = json.Unmarshal(request.Params[0], &call)
		value := ""
		switch {
		case call["data"] == "0x313ce567":
			decimals.Add(1)
			value = fmt.Sprintf("0x%064x", 6)
		case call["data"] == "0x95d89b41":
			symbols.Add(1)
			value = "0x545354" + strings.Repeat("0", 58)
		case strings.HasPrefix(call["data"], "0x70a08231"):
			balances.Add(1)
			value = "0x" + strings.Repeat("0", 64)
		default:
			http.Error(w, "unexpected method", 400)
			return
		}
		timer := time.NewTimer(200 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return
		case <-timer.C:
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": value})
	}))
	defer server.Close()
	primary, err := chain.NewClient("local-only", server.URL, chain.WithRPCBaseURL(server.URL), chain.WithHTTPClient(server.Client()), chain.WithCURateLimit(400), chain.WithUsageObserver(func(u chain.NoditUsage) { units.Add(u.CU) }))
	if err != nil {
		t.Fatal(err)
	}
	rules := prefetchRules(100)
	zero := "0"
	for i := range rules {
		rules[i].LastValue = &zero
		rules[i].WalletAddress = fmt.Sprintf("0x%040x", i%20+1)
	}
	repo := &cycleCommitRepository{fakeRepository: &fakeRepository{rules: rules}}
	executor := newTestExecutor(t, repo, chain.NewWalletReadClient(primary), &fakeNotifier{})
	stats := map[string]any{}
	for _, phase := range []string{"cold", "warm"} {
		beforeRequests, beforeUnits := requests.Load(), units.Load()
		started := time.Now()
		result, err := executor.CheckAll(context.Background(), 200)
		elapsed := time.Since(started)
		if err != nil || result.Checked != 100 || len(result.Errors) != 0 || elapsed >= 55*time.Second || result.DeliveriesAttempted != 0 {
			t.Fatalf("phase%s did not finish safely within60s: result=%+v elapsed=%s err=%v", phase, result, elapsed, err)
		}
		stats[phase] = map[string]any{"elapsed_ms": elapsed.Milliseconds(), "physical_local_http": requests.Load() - beforeRequests, "estimated_cu": units.Load() - beforeUnits, "independent_commits": result.Checked}
	}
	if requests.Load() != 400 || decimals.Load() != 100 || symbols.Load() != 100 || balances.Load() != 200 || len(repo.observations) != 200 || peak.Load() > 2 || active.Load() != 0 || len(repo.walletQueue) != 0 {
		t.Fatalf("cache/prefetch/serialcommit contract failed: HTTP%d metadata%d/%d balances%d commits%d peak%d", requests.Load(), decimals.Load(), symbols.Load(), balances.Load(), len(repo.observations), peak.Load())
	}
	stats["http_latency_ms"] = 200
	stats["peak_http"] = peak.Load()
	stats["real_provider_requests"] = 0
	encoded, _ := json.Marshal(stats)
	t.Log(string(encoded))
}
