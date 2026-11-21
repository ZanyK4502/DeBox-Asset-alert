package marketcollector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type pro5DiscoveryNoWait struct{}

func (pro5DiscoveryNoWait) Wait(ctx context.Context) error { return ctx.Err() }

// Cost-only fixture: actual Chain/DexScreener methods and cache behavior use
// local HTTP. The repository is the existing bounded discovery test adapter;
// selected-pool/transaction correctness is verified separately with real PG.
func TestPro5DiscoveryCandidateAndSelectedQuoteAttemptCosts(t *testing.T) {
	for _, candidatesPerToken := range []int{5, 8} {
		t.Run(fmt.Sprint(candidatesPerToken), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var projects []store.MarketProject
			pairsByToken := map[string][]marketdata.Pair{}
			poolTokens := map[string][2]string{}
			bindingResults := map[string]string{}
			var selectedPairs []marketdata.Pair
			var selectedAddresses []string
			for tokenIndex := range 5 {
				token := fmt.Sprintf("0x%040x", 1000+tokenIndex)
				projects = append(projects, discoveryProject(int64(tokenIndex+1), token))
				for poolIndex := range candidatesPerToken {
					pool := fmt.Sprintf("0x%040x", 20000+16*tokenIndex+poolIndex)
					quote := fmt.Sprintf("0x%040x", 30000+16*tokenIndex+poolIndex)
					pair := marketdata.Pair{ChainID: "bsc", DexID: "pancakeswap", PairAddress: pool, Labels: []string{"v2"},
						BaseToken: marketdata.Token{Address: token, Symbol: "BASE"}, QuoteToken: marketdata.Token{Address: quote, Symbol: "QUOTE"},
						PriceUSD: "1", Liquidity: marketdata.Liquidity{USD: "1000000"}}
					pairsByToken[token] = append(pairsByToken[token], pair)
					poolTokens[pool] = [2]string{token, quote}
					bindingResults["0xe6a43905"+strings.Repeat("0", 24)+token[2:]+strings.Repeat("0", 24)+quote[2:]] = pool
					if poolIndex < 5 {
						selectedAddresses = append(selectedAddresses, pool)
						selectedPairs = append(selectedPairs, pair)
					}
				}
			}
			var mu sync.Mutex
			var rpcHTTP, discoveryHTTP, quoteHTTP, observedAttempts, observedCU int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/rpc" {
					var request struct {
						ID     json.RawMessage   `json:"id"`
						Method string            `json:"method"`
						Params []json.RawMessage `json:"params"`
					}
					if json.NewDecoder(r.Body).Decode(&request) != nil || request.Method != "eth_call" || len(request.Params) != 2 {
						t.Error("unexpected discovery RPC")
						http.Error(w, "unexpected RPC", http.StatusBadRequest)
						return
					}
					var call map[string]string
					_ = json.Unmarshal(request.Params[0], &call)
					mu.Lock()
					rpcHTTP++
					mu.Unlock()
					result := ""
					switch call["data"] {
					case "0x0dfe1681":
						result = poolTokens[call["to"]][0]
					case "0xd21220a7":
						result = poolTokens[call["to"]][1]
					case "0xc45a0155":
						result = marketparse.BSCPancakeV2Factory
					case "0x313ce567":
						_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": fmt.Sprintf("0x%064x", 6)})
						return
					default:
						result = bindingResults[call["data"]]
						if call["to"] != marketparse.BSCPancakeV2Factory || string(request.Params[1]) != `"finalized"` {
							t.Error("immutable admission did not use finalized factory")
						}
					}
					if len(result) != 42 {
						t.Error("unrecognized fixture pool identity", call)
						http.Error(w, "missing fixture identity", http.StatusBadRequest)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": "0x" + strings.Repeat("0", 24) + result[2:]})
					return
				}
				if token := strings.TrimPrefix(r.URL.Path, "/token-pairs/v1/bsc/"); token != r.URL.Path {
					mu.Lock()
					discoveryHTTP++
					mu.Unlock()
					_ = json.NewEncoder(w).Encode(pairsByToken[token])
					return
				}
				if strings.HasPrefix(r.URL.Path, "/latest/dex/pairs/bsc/") {
					mu.Lock()
					quoteHTTP++
					mu.Unlock()
					_ = json.NewEncoder(w).Encode(map[string]any{"pairs": selectedPairs})
					return
				}
				t.Error("unexpected market HTTP path", r.URL.Path)
				http.Error(w, "unexpected path", http.StatusNotFound)
			}))
			defer server.Close()
			client, err := chain.NewClient("local-fixture", server.URL, chain.WithHTTPClient(server.Client()), chain.WithRPCBaseURL(server.URL+"/rpc"), chain.WithCURateLimit(1_000_000),
				chain.WithUsageObserver(func(u chain.NoditUsage) {
					mu.Lock()
					defer mu.Unlock()
					observedAttempts++
					observedCU += u.CU
				}))
			if err != nil {
				t.Fatal(err)
			}
			market, err := marketdata.NewDexScreenerClient(server.URL, marketdata.WithDexScreenerHTTPClient(server.Client()), marketdata.WithDexScreenerLimiter(pro5DiscoveryNoWait{}))
			if err != nil {
				t.Fatal(err)
			}
			repo := &discoveryTestRepository{fakeRepository: &fakeRepository{}, projects: projects, selected: map[int64]int64{1: 901, 2: 902, 3: 903, 4: 904, 5: 905}}
			service := New(Dependencies{Repository: repo, Market: market, Chain: client}, Settings{Enabled: true, ChainKey: "bsc", ChainID: 56})
			now := time.Now()
			service.now = func() time.Time { return now }
			if err := service.discoverPools(ctx); err != nil {
				t.Fatal(err)
			}
			coldQuotes, err := market.PairsByAddresses(ctx, "bsc", selectedAddresses)
			if err != nil || len(coldQuotes) != 25 || coldQuotes[0].QuoteQuality.FetchedAt == nil {
				t.Fatal(len(coldQuotes), err)
			}
			mu.Lock()
			coldRPC, coldCU, coldDiscovery, coldQuote := rpcHTTP, observedCU, discoveryHTTP, quoteHTTP
			mu.Unlock()
			// Three identity calls plus one finalized binding proof and one
			// quote-token decimals call per candidate, including unselected ones.
			wantCold := int64(5 * 5 * candidatesPerToken)
			if coldRPC != wantCold || coldCU != 23*wantCold || coldDiscovery != 5 || coldQuote != 1 || len(repo.writes) != 5*candidatesPerToken {
				t.Fatalf("cold cost mismatch: RPC=%d CU=%d discovery=%d quote=%d writes=%d", coldRPC, coldCU, coldDiscovery, coldQuote, len(repo.writes))
			}
			if err := service.discoverPools(ctx); err != nil {
				t.Fatal(err)
			}
			warmQuotes, err := market.PairsByAddresses(ctx, "bsc", selectedAddresses)
			if err != nil || len(warmQuotes) != 25 || warmQuotes[0].QuoteQuality.FetchedAt == nil || !warmQuotes[0].QuoteQuality.FetchedAt.Equal(*coldQuotes[0].QuoteQuality.FetchedAt) {
				t.Fatal("warm quote did not preserve cached observation", err)
			}
			mu.Lock()
			warmRPC, warmCU := rpcHTTP-coldRPC, observedCU-coldCU
			if warmRPC != 0 || warmCU != 0 || discoveryHTTP != 5 || quoteHTTP != 1 || observedAttempts != rpcHTTP {
				mu.Unlock()
				t.Fatalf("warm/physical-attempt cost mismatch: RPC=%d CU=%d", warmRPC, warmCU)
			}
			mu.Unlock()
			// Advance only collector metadata/fact time. The provider's own cache
			// uses wall time; this is not a changed discovery/quote schedule.
			now = now.Add(5 * time.Minute)
			if err := service.discoverPools(ctx); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			expiredRPC, expiredCU := rpcHTTP-coldRPC-warmRPC, observedCU-coldCU-warmCU
			if expiredRPC != int64(5*candidatesPerToken) || expiredCU != 23*expiredRPC || discoveryHTTP != 5 || observedAttempts != rpcHTTP {
				mu.Unlock()
				t.Fatalf("metadata expiry failed or immutable facts reread early: RPC=%d CU=%d", expiredRPC, expiredCU)
			}
			mu.Unlock()
			for project, selected := range repo.selected {
				if selected != 900+project {
					t.Fatal("discovery replaced the existing primary", project)
				}
			}
			report, _ := json.Marshal(map[string]any{"projects": 5, "selected_quote_pools": 25, "candidate_pools_per_project": candidatesPerToken,
				"cold_rpc_attempts": coldRPC, "cold_nodit_cu": coldCU, "warm_rpc_attempts": warmRPC, "warm_nodit_cu": warmCU,
				"metadata_expiry_minutes": 5, "expiry_rpc_attempts": expiredRPC, "expiry_nodit_cu": expiredCU,
				"cold_discovery_http": coldDiscovery, "warm_discovery_http": 0, "cold_selected_quote_http": coldQuote, "warm_selected_quote_http": 0,
				"pool_fact_cache_entries": len(service.poolFacts.entries), "provider_rate_fixture": 1_000_000, "throughput_claim": false, "repository_fixture": true})
			t.Logf("PRO5_DISCOVERY_COST %s", report)
		})
	}
}
