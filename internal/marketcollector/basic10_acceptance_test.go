package marketcollector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketrules"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

// This opt-in acceptance sample uses an isolated PG schema and loopback HTTP.
// Six sequential calls represent six scheduled minutes; they do not fast-forward
// a real runner clock or establish production/provider latency or capacity.
const basic10ScanCycles = 6

type basic10Usage struct{ Calls, CU int64 }
type basic10Runtime struct {
	HeapAlloc   uint64 `json:"heap_alloc_bytes"`
	TotalAlloc  uint64 `json:"total_alloc_bytes"`
	HeapObjects uint64 `json:"heap_objects"`
	Goroutines  int    `json:"goroutines"`
	PeakRSS     uint64 `json:"process_peak_rss_bytes"`
}
type basic10ScanCycle struct {
	Cycle                               int   `json:"cycle"`
	Safe                                int64 `json:"safe_height"`
	SafeGrowth                          int64 `json:"safe_growth_blocks"`
	Before, After                       int64
	Processed                           int64 `json:"processed_blocks"`
	Backlog                             int64 `json:"backlog_blocks"`
	ScanMS, RuleMS                      int64
	RulesEvaluated                      int64 `json:"rules_evaluated"`
	RulePolls                           int   `json:"rule_polls_within_simulated_minute"`
	ReadyTransactions, Legs, ReadyRules int64
	MinRuleWatermark, LatestReadyID     int64
	Usage                               map[string]basic10Usage `json:"cu_by_operation"`
	Pool                                map[string]int64        `json:"query_pool_delta"`
	AfterGC                             basic10Runtime          `json:"after_gc"`
}
type basic10ScanSample struct {
	Batch                 int64              `json:"batch_blocks"`
	Cycles                []basic10ScanCycle `json:"cycles"`
	Start, End            basic10Runtime
	SampledPeakHeap       uint64                  `json:"sampled_peak_heap_bytes"`
	SampledPeakGoroutines int64                   `json:"sampled_peak_goroutines"`
	HTTPPeak              int64                   `json:"peak_http_inflight"`
	TotalUsage            map[string]basic10Usage `json:"cu_by_operation"`
	WallMS                int64                   `json:"wall_ms"`
	PoolProbe             bool                    `json:"actual_store_pool_probe"`
}

func basic10RuntimeRead() basic10Runtime {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	var r syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &r)
	rss := uint64(r.Maxrss)
	if runtime.GOOS != "darwin" {
		rss *= 1024
	}
	return basic10Runtime{m.HeapAlloc, m.TotalAlloc, m.HeapObjects, runtime.NumGoroutine(), rss}
}

// The optional method exists only in the scratch Go overlay, keeping runtime
// observability out of the production Store API. Other test runs still compile.
type basic10PoolProbe interface{ Basic10AcceptancePoolStats() map[string]int64 }

func basic10PoolRead(db *store.Store) map[string]int64 {
	if probe, ok := any(db).(basic10PoolProbe); ok {
		return probe.Basic10AcceptancePoolStats()
	}
	return nil
}
func basic10PoolDelta(before, after map[string]int64) map[string]int64 {
	if after == nil {
		return nil
	}
	out := make(map[string]int64, len(after))
	for key, value := range after {
		if strings.HasSuffix(key, "_count") || strings.HasSuffix(key, "_ns") {
			out[key] = value - before[key]
		} else {
			out[key] = value
		}
	}
	return out
}

type basic10RPCFixture struct {
	safe                   atomic.Int64
	active, peak           atomic.Int64
	mu                     sync.Mutex
	usage                  map[string]basic10Usage
	logs                   []chain.RPCLog
	transactions, receipts map[string]map[string]any
	baseAt                 time.Time
}

func basic10BlockHash(n uint64) string {
	if n == 99 {
		return testHashC
	}
	return fmt.Sprintf("0x%064x", 100000+n)
}
func basic10Quantity(raw string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimPrefix(raw, "0x"), 16, 64)
	return n
}

func (f *basic10RPCFixture) observe(u chain.NoditUsage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.usage[u.Product+":"+u.Operation]
	p.Calls++
	p.CU += u.CU
	f.usage[u.Product+":"+u.Operation] = p
}
func (f *basic10RPCFixture) usageSnapshot() map[string]basic10Usage {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]basic10Usage, len(f.usage))
	for key, value := range f.usage {
		out[key] = value
	}
	return out
}
func basic10UsageDelta(before, after map[string]basic10Usage) map[string]basic10Usage {
	out := make(map[string]basic10Usage, len(after))
	for key, value := range after {
		old := before[key]
		out[key] = basic10Usage{value.Calls - old.Calls, value.CU - old.CU}
	}
	return out
}

func newBasic10RPCFixture(t *testing.T) *basic10RPCFixture {
	t.Helper()
	_, _, base, _ := receiptFactFixture(t)
	f := &basic10RPCFixture{usage: map[string]basic10Usage{}, transactions: map[string]map[string]any{}, receipts: map[string]map[string]any{}, baseAt: time.Now().UTC().Truncate(time.Second).Add(-20 * time.Minute)}
	for n := uint64(100); n <= 1700; n += 20 {
		hash := fmt.Sprintf("0x%064x", 200000+n)
		var logs []chain.RPCLog
		for i, original := range base.receipt["logs"].([]chain.RPCLog) {
			l := original
			l.TransactionHash = hash
			l.BlockHash = basic10BlockHash(n)
			l.BlockNumber = fmt.Sprintf("0x%x", n)
			l.TransactionIndex = "0x0"
			l.LogIndex = fmt.Sprintf("0x%x", i)
			logs = append(logs, l)
			f.logs = append(f.logs, l)
		}
		f.transactions[hash] = map[string]any{"hash": hash, "blockHash": basic10BlockHash(n), "blockNumber": fmt.Sprintf("0x%x", n), "transactionIndex": "0x0", "from": base.transaction["from"], "to": base.transaction["to"]}
		f.receipts[hash] = map[string]any{"transactionHash": hash, "blockHash": basic10BlockHash(n), "blockNumber": fmt.Sprintf("0x%x", n), "transactionIndex": "0x0", "from": base.transaction["from"], "to": base.transaction["to"], "status": "0x1", "logs": logs}
	}
	return f
}

func (f *basic10RPCFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	active := f.active.Add(1)
	defer f.active.Add(-1)
	for old := f.peak.Load(); active > old && !f.peak.CompareAndSwap(old, active); old = f.peak.Load() {
	}
	timer := time.NewTimer(25 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-r.Context().Done():
		return
	case <-timer.C:
	}
	var request struct {
		ID     any               `json:"id"`
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&request); err != nil {
		http.Error(w, "invalid local request", 400)
		return
	}
	var result any
	switch request.Method {
	case "eth_blockNumber":
		result = fmt.Sprintf("0x%x", f.safe.Load()+15)
	case "eth_getBlockByNumber":
		var raw string
		_ = json.Unmarshal(request.Params[0], &raw)
		n := basic10Quantity(raw)
		result = map[string]any{"number": raw, "hash": basic10BlockHash(n), "parentHash": basic10BlockHash(n - 1), "timestamp": fmt.Sprintf("0x%x", f.baseAt.Add(time.Duration(n)*450*time.Millisecond).Unix())}
	case "eth_call":
		result = "0x" + strings.Repeat("0", 64)
	case "eth_getTransactionByHash", "eth_getTransactionReceipt":
		var hash string
		_ = json.Unmarshal(request.Params[0], &hash)
		if request.Method == "eth_getTransactionByHash" {
			result = f.transactions[hash]
		} else {
			result = f.receipts[hash]
		}
	case "eth_getLogs":
		var filter struct {
			FromBlock, ToBlock string
			Address            json.RawMessage
			Topics             []json.RawMessage
		}
		_ = json.Unmarshal(request.Params[0], &filter)
		var addresses []string
		if len(filter.Address) > 0 && filter.Address[0] == '"' {
			var one string
			_ = json.Unmarshal(filter.Address, &one)
			addresses = []string{one}
		} else {
			_ = json.Unmarshal(filter.Address, &addresses)
		}
		allowed := make(map[string]bool, len(addresses))
		for _, address := range addresses {
			allowed[address] = true
		}
		logs := make([]chain.RPCLog, 0)
		for _, l := range f.logs {
			n := basic10Quantity(l.BlockNumber)
			if n < basic10Quantity(filter.FromBlock) || n > basic10Quantity(filter.ToBlock) || !allowed[l.Address] {
				continue
			}
			match := true
			for i, raw := range filter.Topics {
				if string(raw) == "null" {
					continue
				}
				var choices []string
				if len(raw) > 0 && raw[0] == '"' {
					var one string
					_ = json.Unmarshal(raw, &one)
					choices = []string{one}
				} else {
					_ = json.Unmarshal(raw, &choices)
				}
				if len(choices) == 0 {
					continue
				}
				found := false
				for _, topic := range choices {
					if i < len(l.Topics) && l.Topics[i] == topic {
						found = true
					}
				}
				if !found {
					match = false
					break
				}
			}
			if match {
				logs = append(logs, l)
			}
		}
		result = logs
	default:
		http.Error(w, "unexpected local RPC operation", 400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
}

type basic10LoopbackTransport struct {
	host string
	base *http.Transport
}

func (t basic10LoopbackTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != t.host {
		return nil, fmt.Errorf("acceptance blocked non-fixture HTTP host")
	}
	return t.base.RoundTrip(r)
}

func runBasic10ScanSample(t *testing.T, parent context.Context, batch int64) basic10ScanSample {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	f := newScannerPGFixture(t, 0)
	fixture := newBasic10RPCFixture(t)
	if _, err := f.pool.Exec(ctx, `UPDATE market_scanned_blocks SET parent_hash=$1,block_timestamp=$2 WHERE chain_id=56 AND block_number=99`, basic10BlockHash(98), fixture.baseAt.Add(99*450*time.Millisecond).Truncate(time.Second)); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(fixture)
	defer server.Close()
	transport := &http.Transport{MaxIdleConns: 4, MaxIdleConnsPerHost: 4}
	defer transport.CloseIdleConnections()
	client, err := chain.NewClient("local-only-key", server.URL, chain.WithRPCBaseURL(server.URL), chain.WithHTTPClient(&http.Client{Timeout: 5 * time.Second, Transport: basic10LoopbackTransport{host: strings.TrimPrefix(server.URL, "http://"), base: transport}}), chain.WithCURateLimit(250), chain.WithUsageObserver(fixture.observe))
	if err != nil {
		t.Fatal(err)
	}
	reads, err := chain.NewMarketReadClient(client, nil)
	if err != nil {
		t.Fatal(err)
	}
	collector := New(Dependencies{Repository: f.db, Chain: reads, TryLock: func(ctx context.Context, key string) (Lock, bool, error) { return f.db.TryMarketTaskLock(ctx, key) }}, Settings{Enabled: true, ChainKey: "bsc", ChainID: 56, ScanBatchSize: batch, HydrationConcurrency: 2, ConfirmationDepth: 15, ScanInterval: time.Minute})
	notifier := &localTradeNotifier{}
	engine := marketrules.New(marketrules.Dependencies{Repository: f.db, Notifications: notifier, TryLock: func(ctx context.Context, key string) (marketrules.Lock, bool, error) {
		return f.db.TryMarketTaskLock(ctx, key)
	}}, marketrules.Settings{Enabled: true, TradeFactsEnabled: true, MonitoringChainKeys: []string{"bsc"}, Batch: 200})
	policy := store.QuotaPolicy{PlanCode: plans.Professional, RuleLimit: 10, MarketProjectLimit: 1, MarketPoolLimit: 2, AllowedMarketChainKeys: []string{"bsc"}, MultiPoolMonitoring: true, AllowedMarketRuleTypes: []string{plans.MarketLargeBuy}}
	for i := 0; i < 10; i++ {
		rule, err := f.db.CreateMarketRuleWithinQuota(ctx, store.CreateMarketRuleParams{DeBoxUserID: scannerFixtureOwner, MarketProjectID: f.project.ID, RuleType: plans.MarketLargeBuy, ThresholdUnit: "token", ThresholdValue: fmt.Sprint(1_000_000_000 + i), PoolScope: "all", NotificationChatID: scannerFixtureOwner}, policy)
		if err != nil {
			t.Fatal(err)
		}
		targets, err := f.db.ListMarketRuleTargets(ctx, rule, f.project)
		if err != nil {
			t.Fatal(err)
		}
		merged := marketrules.MergeTradeRuleTargets(targets)
		if len(merged) != 1 {
			t.Fatal("expected one BSC trade lane")
		}
		if created, err := engine.InitializeTradeRuleState(ctx, rule.ID, merged[0], 0); err != nil || !created {
			t.Fatal("local explicit activation", created, err)
		}
	}
	runtime.GC()
	report := basic10ScanSample{Batch: batch, Start: basic10RuntimeRead()}
	_, report.PoolProbe = any(f.db).(basic10PoolProbe)
	if os.Getenv("BASIC10_REQUIRE_POOL_PROBE") == "1" && !report.PoolProbe {
		t.Fatal("scratch Store stats overlay is required")
	}
	var peakHeap, peakG atomic.Uint64
	peakHeap.Store(report.Start.HeapAlloc)
	peakG.Store(uint64(report.Start.Goroutines))
	monitorCtx, stopMonitor := context.WithCancel(ctx)
	var monitor sync.WaitGroup
	monitor.Add(1)
	go func() {
		defer monitor.Done()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-monitorCtx.Done():
				return
			case <-ticker.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				for old := peakHeap.Load(); m.HeapAlloc > old && !peakHeap.CompareAndSwap(old, m.HeapAlloc); old = peakHeap.Load() {
				}
				g := uint64(runtime.NumGoroutine())
				for old := peakG.Load(); g > old && !peakG.CompareAndSwap(old, g); old = peakG.Load() {
				}
			}
		}
	}()
	defer func() { stopMonitor(); monitor.Wait() }()
	started := time.Now()
	for cycle := 0; cycle < basic10ScanCycles; cycle++ {
		safe := int64(899 + cycle*134)
		fixture.safe.Store(safe)
		before, err := f.db.GetMarketChainCursor(ctx, 56, DefaultCursorKey)
		if err != nil {
			t.Fatal(err)
		}
		usageBefore, poolBefore := fixture.usageSnapshot(), basic10PoolRead(f.db)
		scanStarted := time.Now()
		if err := collector.ScanLogs(ctx); err != nil {
			t.Fatalf("batch%d cycle%d scanner: %v", batch, cycle+1, err)
		}
		scanMS := time.Since(scanStarted).Milliseconds()
		ruleStarted := time.Now()
		var rulesEvaluated int64
		polls := 0
		for polls < int(time.Minute/marketrules.DefaultInterval) {
			result, err := engine.RunCycle(ctx)
			polls++
			if err != nil {
				t.Fatalf("batch%d cycle%d rules: %v", batch, cycle+1, err)
			}
			if result.RulesEvaluated != 10 || result.TriggersCreated != 0 || len(notifier.messages) != 0 {
				t.Fatalf("full rules did not evaluate the no-trigger scenario: %+v", result)
			}
			rulesEvaluated += result.RulesEvaluated
			var ready, watermark int64
			if err := f.pool.QueryRow(ctx, `SELECT (SELECT COALESCE(MAX(id),0) FROM market_trade_ready_events WHERE valid),(SELECT COALESCE(MIN((state->>'last_trade_ready_id')::bigint),0) FROM market_rule_target_states WHERE target_key LIKE '%/trades:v2')`).Scan(&ready, &watermark); err != nil {
				t.Fatal(err)
			}
			if ready == watermark {
				break
			}
		}
		ruleMS := time.Since(ruleStarted).Milliseconds()
		after, err := f.db.GetMarketChainCursor(ctx, 56, DefaultCursorKey)
		if err != nil {
			t.Fatal(err)
		}
		entry := basic10ScanCycle{Cycle: cycle + 1, Safe: safe, Before: before.NextBlockNumber, After: after.NextBlockNumber, Processed: after.NextBlockNumber - before.NextBlockNumber, Backlog: safe - after.NextBlockNumber + 1, ScanMS: scanMS, RuleMS: ruleMS, RulesEvaluated: rulesEvaluated, RulePolls: polls, Usage: basic10UsageDelta(usageBefore, fixture.usageSnapshot()), Pool: basic10PoolDelta(poolBefore, basic10PoolRead(f.db))}
		if cycle > 0 {
			entry.SafeGrowth = 134
		}
		if err := f.pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM market_trade_ready_events WHERE valid),(SELECT COUNT(*) FROM market_trade_legs),(SELECT COUNT(*) FROM market_trade_rule_readiness WHERE status='ready'),
 (SELECT COALESCE(MIN((state->>'last_trade_ready_id')::bigint),0) FROM market_rule_target_states WHERE target_key LIKE '%/trades:v2'),(SELECT COALESCE(MAX(id),0) FROM market_trade_ready_events WHERE valid)`).Scan(&entry.ReadyTransactions, &entry.Legs, &entry.ReadyRules, &entry.MinRuleWatermark, &entry.LatestReadyID); err != nil {
			t.Fatal(err)
		}
		if entry.Processed < 1 || entry.Processed > batch || entry.ReadyTransactions < 1 || entry.Legs != 2*entry.ReadyTransactions || entry.ReadyRules != 10 || entry.MinRuleWatermark != entry.LatestReadyID {
			t.Fatalf("actual receipt/leg/rule chain did not advance: %+v", entry)
		}
		runtime.GC()
		entry.AfterGC = basic10RuntimeRead()
		report.Cycles = append(report.Cycles, entry)
		t.Logf("batch=%d cycle=%d processed=%d safe_growth=%d backlog=%d scan_ms=%d rule_ms=%d rule_polls=%d ready_tx=%d", batch, cycle+1, entry.Processed, entry.SafeGrowth, entry.Backlog, scanMS, ruleMS, polls, entry.ReadyTransactions)
	}
	stopMonitor()
	monitor.Wait()
	transport.CloseIdleConnections()
	server.Close()
	runtime.GC()
	report.End = basic10RuntimeRead()
	report.SampledPeakHeap = peakHeap.Load()
	report.SampledPeakGoroutines = int64(peakG.Load())
	report.HTTPPeak = fixture.peak.Load()
	report.TotalUsage = fixture.usageSnapshot()
	report.WallMS = time.Since(started).Milliseconds()
	if batch == 100 && report.Cycles[5].Backlog <= report.Cycles[0].Backlog {
		t.Fatal("100/virtual-minute model unexpectedly kept up with +134 blocks")
	}
	if batch == 500 && report.Cycles[5].Backlog != 0 {
		t.Fatal("bounded 500-block sample failed to reach simulated safe height")
	}
	return report
}

func TestPostgresBasic10ScannerSustainedLocalSample(t *testing.T) {
	if os.Getenv("RUN_BASIC10_SCAN_SAMPLE") != "1" {
		t.Skip("explicit bounded scan acceptance sample only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	var samples []basic10ScanSample
	batches := []int64{100, 500}
	if configured := os.Getenv("BASIC10_SCAN_BATCHES"); configured != "" {
		batches = nil
		for _, raw := range strings.Split(configured, ",") {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || (n != 100 && n != 500) {
				t.Fatal("only the authorized 100/500 comparisons are supported")
			}
			batches = append(batches, n)
		}
	}
	for _, batch := range batches {
		t.Run(fmt.Sprintf("batch_%d", batch), func(t *testing.T) { samples = append(samples, runBasic10ScanSample(t, ctx, batch)) })
	}
	runtime.GC()
	result := map[string]any{"samples": samples, "after_schemas_closed": basic10RuntimeRead(), "scenario": map[string]any{"owners": 1, "pro_shared_market_rules": 10, "projects": 1, "chain": "bsc", "selected_pools": 2, "rule_type": plans.MarketLargeBuy, "threshold_unit": "token", "condition": "not reached; rule watermarks must process actual confirmed facts", "cycles_per_batch": 6, "simulated_cycle_seconds": 60, "safe_growth_per_following_cycle": 134, "initial_backlog_blocks": 800, "transactions_per_20_blocks": 1, "pool_legs_per_transaction": 2, "mock_http_delay_ms": 25, "cu_per_second_test_setting": 250, "source_default_cu_per_second": 400, "hydration_concurrency": 2, "scan_interval_seconds": 60, "no_external_provider_or_production": true, "not_production_capacity_or_memory_leak_certification": true}}
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if output := os.Getenv("BASIC10_SCAN_REPORT"); output != "" {
		if !filepath.IsAbs(output) {
			t.Fatal("report path must be absolute")
		}
		if err := os.WriteFile(output, append(body, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("bounded sample complete: %s", body)
}
