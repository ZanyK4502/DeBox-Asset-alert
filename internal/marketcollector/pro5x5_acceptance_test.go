package marketcollector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketrules"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Opt-in, one bounded workload, one isolated PG schema and loopback HTTP only.
// Virtual minutes do not certify a provider, a monthly envelope or production.
type pro5RPCFixture struct {
	*basic10RPCFixture
	retryOnce atomic.Bool
	served    map[string]int64
}

func (f *pro5RPCFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
	if err != nil || len(body) > 64<<10 {
		http.Error(w, "invalid bounded fixture request", http.StatusBadRequest)
		return
	}
	var request struct{ Method string }
	if json.Unmarshal(body, &request) != nil {
		http.Error(w, "invalid fixture JSON", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.served[request.Method]++
	f.mu.Unlock()
	if request.Method == "eth_getLogs" && f.retryOnce.CompareAndSwap(false, true) {
		w.Header().Set("Retry-After", "0")
		http.Error(w, "bounded fixture rate limit", http.StatusTooManyRequests)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	f.basic10RPCFixture.ServeHTTP(w, r)
}

type pro5PoolProbe interface{ Pro5AcceptancePoolStats() map[string]int64 }

func pro5PoolRead(db *store.Store) map[string]int64 {
	if probe, ok := any(db).(pro5PoolProbe); ok {
		return probe.Pro5AcceptancePoolStats()
	}
	return nil
}

type pro5AcceptanceRepository struct {
	*store.Store
	pool        *pgxpool.Pool
	pendingPeak int64
}

func (r *pro5AcceptanceRepository) ClaimMarketDeliveries(ctx context.Context, limit int) ([]store.MarketDeliveryClaim, error) {
	var pending int64
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_rule_events WHERE notification_status='pending'`).Scan(&pending); err != nil {
		return nil, err
	}
	if pending > r.pendingPeak {
		r.pendingPeak = pending
	}
	return r.Store.ClaimMarketDeliveries(ctx, limit)
}

type pro5AcceptanceCycle struct {
	Cycle, RulePolls                                                int
	Safe, SafeGrowth, Processed, Backlog                            int64
	ScanMS, RuleMS                                                  int64
	Receipts, Ready, Legs, CaughtUpRules                            int64
	Triggers, DecisionRows, Pending, Sent, CorrectAggregateSentRows int64
	Usage                                                           map[string]basic10Usage
	Pool                                                            map[string]int64
	AfterGC                                                         basic10Runtime
}

type pro5AcceptanceReport struct {
	Scenario               map[string]any
	Cycles                 []pro5AcceptanceCycle
	Start, AfterHTTPClosed basic10Runtime
	AfterSchemaClosed      basic10Runtime
	PeakHeap, PeakG        uint64
	HTTPPeak               int64
	PoolProbe              bool
	PendingPeak            int64
	NotificationMessages   int
	Webhook                map[string]any
	TotalUsage             map[string]basic10Usage
	HTTPPhysicalCalls      map[string]int64
	WallMS                 int64
}

func pro5Address(n int) string { return fmt.Sprintf("0x%040x", n) }

func pro5CreateFixture(t *testing.T, ctx context.Context, db *store.Store, pool *pgxpool.Pool) *pro5RPCFixture {
	t.Helper()
	const owner = "pro5-local-scanner-owner"
	if _, err := db.ActivateSubscription(ctx, owner, plans.Professional, 30); err != nil {
		t.Fatal(err)
	}
	policy := store.QuotaPolicy{PlanCode: plans.Professional, RuleLimit: 10, MarketProjectLimit: 5, MarketPoolLimit: 5,
		AllowedMarketChainKeys: []string{"bsc"}, MultiPoolMonitoring: true, AllowedMarketRuleTypes: []string{plans.MarketLargeBuy}}
	f := &pro5RPCFixture{basic10RPCFixture: &basic10RPCFixture{usage: map[string]basic10Usage{}, transactions: map[string]map[string]any{}, receipts: map[string]map[string]any{}, baseAt: time.Now().UTC().Truncate(time.Second).Add(-20 * time.Minute)}, served: map[string]int64{}}
	_, _, template, _ := receiptFactFixture(t)
	var projectPools [5][]int64
	for projectIndex := 0; projectIndex < 5; projectIndex++ {
		token := pro5Address(0x100000 + projectIndex)
		var refs []store.CreateMultiChainMarketPoolParams
		for poolIndex := 0; poolIndex < 5; poolIndex++ {
			index := projectIndex*5 + poolIndex
			address, quote := pro5Address(0x300000+index), pro5Address(0x200000+index)
			p, err := db.UpsertMarketPool(ctx, store.UpsertMarketPoolParams{ChainKey: "bsc", ChainID: 56, Protocol: "pancakeswap", ProtocolVersion: "v2", PoolKey: address, PoolAddress: &address,
				Token0Address: quote, Token1Address: token, Token0DecimalsVerified: true, Token1DecimalsVerified: true,
				SupportsEventParsing: true, ParserAdapter: marketparse.AdapterV2, VerificationStatus: "verified", LiquidityUSD: "1000000"})
			if err != nil {
				t.Fatal(err)
			}
			projectPools[projectIndex] = append(projectPools[projectIndex], p.ID)
			refs = append(refs, store.CreateMultiChainMarketPoolParams{MarketPoolID: p.ID, Selected: true, IsPrimary: poolIndex == 0})
		}
		project, err := db.CreateMultiChainMarketProjectWithinQuota(ctx, store.CreateMultiChainMarketProjectParams{DeBoxUserID: owner, CanonicalName: fmt.Sprintf("Local project %d", projectIndex), Symbol: fmt.Sprintf("P%d", projectIndex), IdentitySource: "local_fixture", CanonicalAssetID: fmt.Sprintf("local-pro5-%d", projectIndex), VerificationStatus: "single_chain",
			Deployments: []store.CreateMultiChainMarketDeploymentParams{{ChainKey: "bsc", ChainID: 56, TokenAddress: token, TokenSymbol: fmt.Sprintf("P%d", projectIndex), TokenDecimals: 0, VerificationStatus: "verified", VerificationSource: "fixture", Pools: refs}}}, policy)
		if err != nil {
			t.Fatal(err)
		}
		for ruleIndex := 0; ruleIndex < 2; ruleIndex++ {
			threshold := "10000"
			if ruleIndex == 1 {
				threshold = "1000000000"
			}
			if _, err := db.CreateMarketRuleWithinQuota(ctx, store.CreateMarketRuleParams{DeBoxUserID: owner, MarketProjectID: project.ID,
				RuleType: plans.MarketLargeBuy, ThresholdUnit: "token", ThresholdValue: threshold, PoolScope: "all", DeploymentScope: "all", CooldownSeconds: 3600,
				NotificationChatID: owner, NotificationChatType: "private"}, policy); err != nil {
				t.Fatal(err)
			}
		}
	}
	// This isolated fixture represents configurations activated before its
	// synthetic chain history. Runtime readiness still checks real provenance.
	// No legacy lane is seeded, and no production cutover is performed.
	activation := f.baseAt.Add(30 * time.Second)
	for _, query := range []string{
		`UPDATE market_rules SET trade_activation_after=$1`,
		`UPDATE market_projects SET created_at=$1`,
		`UPDATE market_project_deployments SET created_at=$1`,
		`UPDATE market_project_trade_scope_changes SET changed_at=$1,pool_boundaries=(SELECT jsonb_object_agg(key,value||jsonb_build_object('selected_at',$1::timestamptz,'primary_at',$1::timestamptz)) FROM jsonb_each(pool_boundaries))`,
	} {
		if _, err := pool.Exec(ctx, query, activation); err != nil {
			t.Fatal(err)
		}
	}
	for n, transactionIndex := uint64(100), 0; n <= 1700; n, transactionIndex = n+5, transactionIndex+1 {
		projectIndex := transactionIndex % 5
		hash := fmt.Sprintf("0x%064x", 0x500000+n)
		var logs []chain.RPCLog
		for i := 0; i < 5; i++ {
			l := template.receipt["logs"].([]chain.RPCLog)[0]
			l.Address, l.TransactionHash, l.BlockHash = pro5Address(0x300000+projectIndex*5+i), hash, basic10BlockHash(n)
			l.BlockNumber, l.TransactionIndex, l.LogIndex = fmt.Sprintf("0x%x", n), "0x0", fmt.Sprintf("0x%x", i)
			logs = append(logs, l)
			f.logs = append(f.logs, l)
			quoteAt := f.baseAt.Add(time.Duration(n) * 450 * time.Millisecond).Truncate(time.Second).Add(-time.Second)
			price, liquidity := "1", "1000000"
			if _, err := db.CreateMarketSnapshot(ctx, store.CreateMarketSnapshotParams{ChainKey: "bsc", ChainID: 56, TokenAddress: pro5Address(0x100000 + projectIndex), MarketPoolID: projectPools[projectIndex][i], PriceUSD: &price, LiquidityUSD: &liquidity,
				Source: "local_fixture", CapturedAt: quoteAt, QuoteQuality: marketquality.Quote{Status: marketquality.StatusLive, FetchedAt: &quoteAt}}); err != nil {
				t.Fatal(err)
			}
		}
		f.transactions[hash] = map[string]any{"hash": hash, "blockHash": basic10BlockHash(n), "blockNumber": fmt.Sprintf("0x%x", n), "transactionIndex": "0x0", "from": template.transaction["from"], "to": template.transaction["to"]}
		f.receipts[hash] = map[string]any{"transactionHash": hash, "blockHash": basic10BlockHash(n), "blockNumber": fmt.Sprintf("0x%x", n), "transactionIndex": "0x0", "status": "0x1", "from": template.transaction["from"], "to": template.transaction["to"], "logs": logs}
	}
	ancestorAt := f.baseAt.Add(99 * 450 * time.Millisecond).Truncate(time.Second)
	if _, err := db.UpsertMarketScannedBlock(ctx, store.UpsertMarketScannedBlockParams{ChainKey: "bsc", ChainID: 56, CursorKey: DefaultCursorKey, BlockNumber: 99, BlockHash: basic10BlockHash(99), ParentHash: basic10BlockHash(98), BlockTimestamp: &ancestorAt}); err != nil {
		t.Fatal(err)
	}
	hash := basic10BlockHash(99)
	if _, err := db.AdvanceMarketChainCursor(ctx, store.AdvanceMarketChainCursorParams{ChainKey: "bsc", ChainID: 56, CursorKey: DefaultCursorKey, NextBlockNumber: 100, SafeBlockNumber: 899, LastBlockHash: &hash, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	return f
}

func pro5RuleProgress(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var n int64
	err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_rule_target_states s JOIN market_rules r ON r.id=s.market_rule_id JOIN market_projects p ON p.id=r.market_project_id
 WHERE s.target_key LIKE '%/trades:v2' AND (s.state->>'last_trade_ready_id')::bigint=(SELECT COALESCE(MAX(e.id),0) FROM market_trade_ready_events e WHERE e.valid AND e.chain_id=56 AND e.token_address=p.token_address)`).Scan(&n)
	return n, err
}

func runPro5Acceptance(t *testing.T, parent context.Context) pro5AcceptanceReport {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 210*time.Second)
	defer cancel()
	db, pool := scannerPerformanceStore(t)
	f := pro5CreateFixture(t, ctx, db, pool)
	repo := &pro5AcceptanceRepository{Store: db, pool: pool}
	server := httptest.NewServer(f)
	defer server.Close()
	transport := &http.Transport{MaxIdleConns: 4, MaxIdleConnsPerHost: 4}
	defer transport.CloseIdleConnections()
	client, err := chain.NewClient("loopback-only-key", server.URL, chain.WithRPCBaseURL(server.URL), chain.WithCURateLimit(400), chain.WithUsageObserver(f.observe),
		chain.WithHTTPClient(&http.Client{Timeout: 5 * time.Second, Transport: basic10LoopbackTransport{host: strings.TrimPrefix(server.URL, "http://"), base: transport}}))
	if err != nil {
		t.Fatal(err)
	}
	reads, err := chain.NewMarketReadClient(client, nil)
	if err != nil {
		t.Fatal(err)
	}
	collector := New(Dependencies{Repository: repo, Chain: reads, TryLock: func(ctx context.Context, key string) (Lock, bool, error) { return db.TryMarketTaskLock(ctx, key) }},
		Settings{Enabled: true, ChainKey: "bsc", ChainID: 56, ScanBatchSize: 500, HydrationConcurrency: 2, ConfirmationDepth: 15, WebhookSigningKey: "local-fixture-secret"})
	notifier := &localTradeNotifier{}
	engine := marketrules.New(marketrules.Dependencies{Repository: repo, Notifications: notifier, TryLock: func(ctx context.Context, key string) (marketrules.Lock, bool, error) {
		return db.TryMarketTaskLock(ctx, key)
	}},
		marketrules.Settings{Enabled: true, TradeFactsEnabled: true, MonitoringChainKeys: []string{"bsc"}, Batch: 200})
	runtime.GC()
	report := pro5AcceptanceReport{Start: basic10RuntimeRead(), Scenario: map[string]any{
		"projects": 5, "selected_pools": 25, "unknown_quote_tokens": 25, "project_tokens": 5, "shared_rules": 10,
		"rule_mix": "one10000token and one1Btoken large-buy rule/project; cooldown3600s", "transactions_per5blocks": 1, "project_rotation": 5, "pool_legs_per_transaction": 5,
		"virtual_minutes": 6, "safe_growth_per_following_cycle": 134, "batch_blocks": 500, "hydration_workers": 2, "mock_delay_ms": 25, "test_CU_per_second": 400,
		"excluded": "wallet/holder/discovery/independent health, real provider billing, production, monthly upper-bound certification", "temporal_setup": "fixture-only provenance before synthetic history; real readiness/CAS, no legacy lane shortcut"}}
	_, report.PoolProbe = any(db).(pro5PoolProbe)
	if os.Getenv("BASIC10_REQUIRE_POOL_PROBE") == "1" && !report.PoolProbe {
		t.Fatal("required test-only Store stats overlay missing")
	}
	var peakHeap, peakG atomic.Uint64
	peakHeap.Store(report.Start.HeapAlloc)
	peakG.Store(uint64(report.Start.Goroutines))
	monitorCtx, stop := context.WithCancel(ctx)
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
				s := basic10RuntimeRead()
				for old := peakHeap.Load(); s.HeapAlloc > old && !peakHeap.CompareAndSwap(old, s.HeapAlloc); old = peakHeap.Load() {
				}
				for old := peakG.Load(); uint64(s.Goroutines) > old && !peakG.CompareAndSwap(old, uint64(s.Goroutines)); old = peakG.Load() {
				}
			}
		}
	}()
	defer func() { stop(); monitor.Wait() }()
	started := time.Now()
	for cycle := 0; cycle < 6; cycle++ {
		f.safe.Store(int64(899 + cycle*134))
		before, err := db.GetMarketChainCursor(ctx, 56, DefaultCursorKey)
		if err != nil {
			t.Fatal(err)
		}
		usageBefore, poolBefore := f.usageSnapshot(), pro5PoolRead(db)
		scanAt := time.Now()
		if err := collector.ScanLogs(ctx); err != nil {
			t.Fatalf("cycle%d scanner: %v", cycle+1, err)
		}
		entry := pro5AcceptanceCycle{Cycle: cycle + 1, Safe: f.safe.Load(), ScanMS: time.Since(scanAt).Milliseconds()}
		if cycle > 0 {
			entry.SafeGrowth = 134
		}
		ruleAt := time.Now()
		for entry.RulePolls < 12 {
			result, err := engine.RunCycle(ctx)
			entry.RulePolls++
			entry.Triggers += result.TriggersCreated
			if err != nil || result.RulesEvaluated != 10 || result.TradeRulesAwaitingActivation != 0 {
				t.Fatalf("cycle%d real rules: %+v / %v", cycle+1, result, err)
			}
			entry.CaughtUpRules, err = pro5RuleProgress(ctx, pool)
			if err != nil {
				t.Fatal(err)
			}
			if entry.CaughtUpRules == 10 {
				break
			}
		}
		entry.RuleMS = time.Since(ruleAt).Milliseconds()
		if cycle == 0 {
			var beforeReady, beforeLegs, beforeTriggers int64
			if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM market_trade_ready_events WHERE valid),(SELECT COUNT(*) FROM market_trade_legs),(SELECT COUNT(*) FROM market_rule_events)`).Scan(&beforeReady, &beforeLegs, &beforeTriggers); err != nil {
				t.Fatal(err)
			}
			body := []byte(fmt.Sprintf(`{"transactions":[{"transactionHash":"0x%064x"},{"transactionHash":"0x%064x"}]}`, 0x500000+100, 0x500000+650))
			webhookUsageBefore := f.usageSnapshot()
			headers := map[string][]string{"X-Signature": {signWebhook(body, "local-fixture-secret")}, "X-Nodit-Delivery-Id": {"pro5-local-duplicate"}}
			first, err := collector.AcceptWebhook(ctx, "v2-v3", headers, body)
			if err != nil || !first.Created {
				t.Fatalf("signed local webhook: %+v / %v", first, err)
			}
			second, err := collector.AcceptWebhook(ctx, "v2-v3", headers, body)
			if err != nil || !second.Duplicate || second.InboxID != first.InboxID {
				t.Fatalf("redelivery dedup: %+v / %v", second, err)
			}
			if err := collector.ProcessInbox(ctx); err != nil {
				t.Fatal(err)
			}
			duplicate, err := engine.RunCycle(ctx)
			if err != nil || duplicate.TriggersCreated != 0 || duplicate.DeliveriesSent != 0 {
				t.Fatalf("duplicate path retriggered: %+v / %v", duplicate, err)
			}
			var ready, legs, triggers, processed int64
			if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM market_trade_ready_events WHERE valid),(SELECT COUNT(*) FROM market_trade_legs),(SELECT COUNT(*) FROM market_rule_events),(SELECT COUNT(*) FROM webhook_inbox WHERE processing_status='processed')`).Scan(&ready, &legs, &triggers, &processed); err != nil {
				t.Fatal(err)
			}
			if ready != beforeReady || legs != beforeLegs+5 || triggers != beforeTriggers || processed != 1 {
				t.Fatalf("scan/webhook duplicate changed facts: before=%d/%d/%d after=%d/%d/%d inbox=%d", beforeReady, beforeLegs, beforeTriggers, ready, legs, triggers, processed)
			}
			report.Webhook = map[string]any{"accepted_deliveries": 2, "unique_inbox_processed": processed, "scanned_duplicate_not_republished": true, "early_transaction_staged_before_scan": true, "staged_new_legs": legs - beforeLegs, "physical_usage_delta": basic10UsageDelta(webhookUsageBefore, f.usageSnapshot()), "bytes_per_delivery": len(body), "estimated_bandwidth_CU": float64(2*len(body)) * .03}
		}
		after, err := db.GetMarketChainCursor(ctx, 56, DefaultCursorKey)
		if err != nil {
			t.Fatal(err)
		}
		entry.Processed, entry.Backlog = after.NextBlockNumber-before.NextBlockNumber, entry.Safe-after.NextBlockNumber+1
		if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM market_transaction_receipts),(SELECT COUNT(*) FROM market_trade_ready_events WHERE valid),(SELECT COUNT(*) FROM market_trade_legs),
 (SELECT COUNT(*) FROM market_rule_events),(SELECT COUNT(*) FROM market_rule_events WHERE notification_status='pending'),(SELECT COUNT(*) FROM market_rule_events WHERE notification_status='sent'),(SELECT COUNT(*) FROM market_rule_events WHERE notification_status='sent' AND current_value='30000')`).Scan(&entry.Receipts, &entry.Ready, &entry.Legs, &entry.DecisionRows, &entry.Pending, &entry.Sent, &entry.CorrectAggregateSentRows); err != nil {
			t.Fatal(err)
		}
		if entry.Processed < 1 || entry.Processed > 500 || entry.CaughtUpRules != 10 || (cycle > 0 && entry.Ready != entry.Receipts || cycle == 0 && entry.Receipts != entry.Ready+1) || entry.Legs != 5*entry.Receipts || (cycle == 0 && entry.Triggers != 5 || cycle > 0 && entry.Triggers != 0) || entry.Sent != 5 || entry.CorrectAggregateSentRows != 5 || entry.Pending != 0 {
			t.Fatalf("cycle%d incomplete actual chain: %+v", cycle+1, entry)
		}
		entry.Usage, entry.Pool = basic10UsageDelta(usageBefore, f.usageSnapshot()), basic10PoolDelta(poolBefore, pro5PoolRead(db))
		runtime.GC()
		entry.AfterGC = basic10RuntimeRead()
		report.Cycles = append(report.Cycles, entry)
		t.Logf("cycle=%d processed=%d growth=%d backlog=%d ready=%d legs=%d rules=%d sent=%d scan_ms=%d", entry.Cycle, entry.Processed, entry.SafeGrowth, entry.Backlog, entry.Ready, entry.Legs, entry.CaughtUpRules, entry.Sent, entry.ScanMS)
	}
	stop()
	monitor.Wait()
	transport.CloseIdleConnections()
	server.Close()
	runtime.GC()
	report.AfterHTTPClosed = basic10RuntimeRead()
	report.PeakHeap, report.PeakG, report.HTTPPeak = peakHeap.Load(), peakG.Load(), f.peak.Load()
	report.PendingPeak, report.NotificationMessages = repo.pendingPeak, len(notifier.messages)
	report.TotalUsage, report.HTTPPhysicalCalls = f.usageSnapshot(), f.served
	report.WallMS = time.Since(started).Milliseconds()
	if report.Cycles[5].Backlog != 0 || report.PendingPeak != 5 || report.NotificationMessages != 5 || report.HTTPPhysicalCalls["eth_getLogs"] != 67 {
		t.Fatalf("sample did not meet specified case: %+v", report)
	}
	for method, count := range report.HTTPPhysicalCalls {
		if u := report.TotalUsage["evm_node:"+method]; u.Calls != count {
			t.Fatalf("physical attempt metering mismatch %s: HTTP%d observer%d", method, count, u.Calls)
		}
	}
	return report
}

func TestPostgresPro5x5ScannerRuleWebhookLocalSample(t *testing.T) {
	if os.Getenv("RUN_PRO5X5_SAMPLE") != "1" {
		t.Skip("explicit bounded isolated Pro5x5 acceptance only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 230*time.Second)
	defer cancel()
	var report pro5AcceptanceReport
	t.Run("one_bounded_sample", func(t *testing.T) { report = runPro5Acceptance(t, ctx) })
	if t.Failed() {
		return
	}
	runtime.GC()
	report.AfterSchemaClosed = basic10RuntimeRead()
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	output := os.Getenv("PRO5X5_SAMPLE_REPORT")
	if output == "" || !filepath.IsAbs(output) {
		t.Fatal("explicit absolute scratch report path required")
	}
	if err := os.WriteFile(output, append(body, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("bounded Pro5x5 sample written to %s; total_ms=%d", output, report.WallMS)
}
