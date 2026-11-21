package marketcollector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const scannerFixtureOwner = "scanner-local-owner"

type scannerPipelineChain struct {
	*fakeChain
	mu                     sync.Mutex
	transactions, receipts map[string]map[string]any
	logs                   []chain.RPCLog
	headerCalls            map[uint64]int
	changedEnd             string
}

func (c *scannerPipelineChain) LatestBlockNumber(context.Context, string, string) (uint64, error) {
	return 115, nil
}
func (c *scannerPipelineChain) TokenDecimals(context.Context, string, string, string) (int32, error) {
	return 0, nil
}
func (c *scannerPipelineChain) RPCTransactionByHash(_ context.Context, hash, _, _ string) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transactions[hash], nil
}
func (c *scannerPipelineChain) TransactionReceipt(_ context.Context, hash, _, _ string) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.receipts[hash], nil
}
func (c *scannerPipelineChain) BlockByNumber(_ context.Context, n uint64, _ bool, _, _ string) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.headerCalls[n]++
	original := c.blocks[n]
	if original == nil {
		return nil, fmt.Errorf("missing local header")
	}
	out := map[string]any{}
	for k, v := range original {
		out[k] = v
	}
	if n == 100 && c.changedEnd != "" && c.headerCalls[n] >= 2 {
		out["hash"] = c.changedEnd
	}
	return out, nil
}
func (c *scannerPipelineChain) Logs(_ context.Context, _, _ string, f chain.LogFilter) ([]chain.RPCLog, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	addresses := map[string]bool{}
	for _, a := range f.Addresses {
		addresses[a] = true
	}
	var logs []chain.RPCLog
	for _, log := range c.logs {
		if !addresses[log.Address] {
			continue
		}
		match := true
		for i, choices := range f.Topics {
			if len(choices) == 0 {
				continue
			}
			if i >= len(log.Topics) {
				match = false
				break
			}
			found := false
			for _, v := range choices {
				found = found || v == log.Topics[i]
			}
			if !found {
				match = false
			}
		}
		if match {
			logs = append(logs, log)
		}
	}
	return logs, nil
}

type scannerPipelineRepository struct {
	*store.Store
	target           store.MarketRuleTarget
	t                *testing.T
	writes           []store.MarketTransactionParams
	confirmCalls     int
	failConfirmation error
}

func (r *scannerPipelineRepository) PersistMarketTransaction(ctx context.Context, p store.MarketTransactionParams) ([]store.MarketEvent, error) {
	if p.Confirmed {
		r.t.Fatal("scanner attempted to publish before the fresh end check")
	}
	r.writes = append(r.writes, p)
	events, err := r.Store.PersistMarketTransaction(ctx, p)
	if err == nil {
		ready, e := r.Store.LatestMarketTradeReadyID(ctx, r.target)
		if e != nil || ready != 0 {
			r.t.Fatalf("staging evidence prematurely published ready=%d err=%v", ready, e)
		}
	}
	return events, err
}
func (r *scannerPipelineRepository) MarkMarketBlockConfirmed(ctx context.Context, chainID, through int64) (int64, error) {
	r.confirmCalls++
	if r.failConfirmation != nil {
		return 0, r.failConfirmation
	}
	return r.Store.MarkMarketBlockConfirmed(ctx, chainID, through)
}

type scannerPGFixture struct {
	db      *store.Store
	pool    *pgxpool.Pool
	service *Service
	chain   *scannerPipelineChain
	repo    *scannerPipelineRepository
	project store.MarketProject
	target  store.MarketRuleTarget
	at      time.Time
	policy  store.QuotaPolicy
}

func scannerPerformanceStore(t *testing.T) (*store.Store, *pgxpool.Pool) {
	t.Helper()
	if os.Getenv("RUN_POSTGRES_INTEGRATION") != "1" {
		t.Skip("requires explicitly isolated local PostgreSQL")
	}
	raw := os.Getenv("TEST_DATABASE_URL")
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if host := cfg.ConnConfig.Host; host != "localhost" && !net.ParseIP(host).IsLoopback() && !filepath.IsAbs(host) {
		t.Fatal("scanner integration requires an explicit local PostgreSQL endpoint")
	}
	cfg.MaxConns = 2
	admin, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("scanner_pipeline_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(context.Background(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	q.Set("pool_max_conns", "4")
	u.RawQuery = q.Encode()
	db, err := store.Open(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	verify, err := pgxpool.New(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		verify.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE")
		admin.Close()
		if err != nil {
			t.Error(err)
		}
	})
	return db, verify
}

func newScannerPGFixture(t *testing.T, count int) *scannerPGFixture {
	t.Helper()
	db, pool := scannerPerformanceStore(t)
	ctx := context.Background()
	ancestorAt := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	quoteAt := ancestorAt
	if _, err := db.ActivateSubscription(ctx, scannerFixtureOwner, plans.Professional, 30); err != nil {
		t.Fatal(err)
	}
	policy := store.QuotaPolicy{PlanCode: plans.Professional, MarketProjectLimit: 5, RuleLimit: 50, MultiPoolMonitoring: true, AllowedMarketRuleTypes: []string{plans.MarketLargeBuy}}
	var refs []store.CreateMultiChainMarketPoolParams
	var ids []int64
	for i, address := range []string{"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "0x" + strings.Repeat("b", 40)} {
		p, err := db.UpsertMarketPool(ctx, store.UpsertMarketPoolParams{ChainKey: "bsc", ChainID: 56, Protocol: "pancakeswap", ProtocolVersion: "v2", PoolKey: address, PoolAddress: &address, Token0Address: initializedQuote, Token1Address: initializedToken, Token0Decimals: 0, Token1Decimals: 0, Token0DecimalsVerified: true, Token1DecimalsVerified: true, SupportsEventParsing: true, ParserAdapter: marketparse.AdapterV2, VerificationStatus: "verified"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
		refs = append(refs, store.CreateMultiChainMarketPoolParams{MarketPoolID: p.ID, Selected: true, IsPrimary: i == 0})
		price, liquidity := "1", "1000000"
		if _, err := db.CreateMarketSnapshot(ctx, store.CreateMarketSnapshotParams{MarketPoolID: p.ID, ChainKey: "bsc", ChainID: 56, TokenAddress: initializedToken, PriceUSD: &price, LiquidityUSD: &liquidity, Source: "local_fixture", CapturedAt: quoteAt, QuoteQuality: marketquality.Quote{Status: marketquality.StatusLive, FetchedAt: &quoteAt}}); err != nil {
			t.Fatal(err)
		}
	}
	project, err := db.CreateMultiChainMarketProjectWithinQuota(ctx, store.CreateMultiChainMarketProjectParams{DeBoxUserID: scannerFixtureOwner, CanonicalName: "Local scanner fixture", Symbol: "FIX", IdentitySource: "local_fixture", CanonicalAssetID: "scanner-fixture", VerificationStatus: "single_chain", Deployments: []store.CreateMultiChainMarketDeploymentParams{{ChainKey: "bsc", ChainID: 56, TokenAddress: initializedToken, TokenSymbol: "FIX", TokenDecimals: 0, VerificationStatus: "verified", VerificationSource: "fixture", Pools: refs}}}, policy)
	if err != nil {
		t.Fatal(err)
	}
	// This fixture represents a worker already active at block 100. Its
	// simulated block time must follow the actual project activation.
	at := time.Now().UTC().Truncate(time.Second).Add(2 * time.Second)
	_, _, base, _ := receiptFactFixture(t)
	blocks := map[uint64]map[string]any{99: {"number": "0x63", "hash": testHashC, "parentHash": testHashA, "timestamp": fmt.Sprintf("0x%x", ancestorAt.Unix())}, 100: {"number": "0x64", "hash": testHashB, "parentHash": testHashC, "timestamp": fmt.Sprintf("0x%x", at.Unix())}}
	c := &scannerPipelineChain{fakeChain: &fakeChain{blocks: blocks}, transactions: map[string]map[string]any{}, receipts: map[string]map[string]any{}, headerCalls: map[uint64]int{}}
	original := base.receipt["logs"].([]chain.RPCLog)
	for i := 0; i < count; i++ {
		hash := fmt.Sprintf("0x%064x", 1000+i)
		var logs []chain.RPCLog
		for j, l := range original {
			log := l
			log.TransactionHash = hash
			log.TransactionIndex = fmt.Sprintf("0x%x", i)
			log.LogIndex = fmt.Sprintf("0x%x", i*2+j)
			logs = append(logs, log)
			c.logs = append(c.logs, log)
		}
		c.transactions[hash] = map[string]any{"hash": hash, "blockHash": testHashB, "blockNumber": "0x64", "transactionIndex": fmt.Sprintf("0x%x", i), "from": base.transaction["from"], "to": base.transaction["to"]}
		c.receipts[hash] = map[string]any{"transactionHash": hash, "blockHash": testHashB, "blockNumber": "0x64", "transactionIndex": fmt.Sprintf("0x%x", i), "from": base.transaction["from"], "to": base.transaction["to"], "status": "0x1", "logs": logs}
	}
	target := store.MarketRuleTarget{ChainID: 56, ChainKey: "bsc", TokenAddress: initializedToken, TradePoolIDs: ids}
	r := &scannerPipelineRepository{Store: db, target: target, t: t}
	service := New(Dependencies{Repository: r, Chain: c}, Settings{Enabled: true, ChainKey: "bsc", ChainID: 56, ScanBatchSize: 1, HydrationConcurrency: 2, ConfirmationDepth: 15})
	if _, err := db.UpsertMarketScannedBlock(ctx, store.UpsertMarketScannedBlockParams{ChainKey: "bsc", ChainID: 56, CursorKey: DefaultCursorKey, BlockNumber: 99, BlockHash: testHashC, ParentHash: testHashA, BlockTimestamp: &ancestorAt}); err != nil {
		t.Fatal(err)
	}
	hash := testHashC
	if _, err := db.AdvanceMarketChainCursor(ctx, store.AdvanceMarketChainCursorParams{ChainKey: "bsc", ChainID: 56, CursorKey: DefaultCursorKey, NextBlockNumber: 100, SafeBlockNumber: 100, LastBlockHash: &hash, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	state, err := db.ObserveMarketChainCollection(ctx, "bsc", 56)
	if err != nil || state.PendingIdle == nil {
		t.Fatalf("missing fixture activation: state=%+v err=%v", state, err)
	}
	if err := db.AcknowledgeMarketChainCollectionIdle(ctx, store.AcknowledgeMarketChainCollectionIdleParams{
		ChainKey: "bsc", ChainID: 56, CursorKey: DefaultCursorKey, StateRevision: state.Revision,
		IntervalID: state.PendingIdle.ID, ExpectedNextBlock: 100, NextBlockAt: &at,
	}); err != nil {
		t.Fatal(err)
	}
	return &scannerPGFixture{db: db, pool: pool, service: service, chain: c, repo: r, project: project, target: target, at: at, policy: policy}
}
func (f *scannerPGFixture) count(t *testing.T, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := f.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPostgresScannerStagingCanonicalHeadersDoesNotPublishAndRetriesCompleteReceipt(t *testing.T) {
	f := newScannerPGFixture(t, 2)
	ctx := context.Background()
	if _, err := f.db.UpsertMarketScannedBlock(ctx, store.UpsertMarketScannedBlockParams{ChainKey: "bsc", ChainID: 56, CursorKey: DefaultCursorKey, BlockNumber: 100, BlockHash: testHashB, ParentHash: testHashC, BlockTimestamp: &f.at}); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("local confirmation failure")
	f.repo.failConfirmation = failure
	if err := f.service.ScanLogs(ctx); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if f.count(t, `SELECT COUNT(*) FROM market_transaction_receipts WHERE complete AND NOT confirmed`) != 2 || f.count(t, `SELECT COUNT(*) FROM market_trade_ready_events WHERE valid`) != 0 {
		t.Fatal("existing canonical evidence prematurely published an unconfirmed receipt")
	}
	cursor, _ := f.db.GetMarketChainCursor(ctx, 56, DefaultCursorKey)
	if cursor.NextBlockNumber != 100 {
		t.Fatal("failed confirmation advanced")
	}
	f.repo.failConfirmation = nil
	if err := f.service.ScanLogs(ctx); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT COUNT(*) FROM market_trade_ready_events WHERE valid`) != 2 || f.count(t, `SELECT COUNT(*) FROM market_transaction_receipts`) != 2 || f.count(t, `SELECT COUNT(*) FROM market_trade_legs`) != 4 {
		t.Fatal("retry split or duplicated a complete transaction")
	}
	cursor, _ = f.db.GetMarketChainCursor(ctx, 56, DefaultCursorKey)
	if cursor.NextBlockNumber != 101 || f.chain.headerCalls[100] != 4 {
		t.Fatalf("header not shared or cursor wrong: calls=%v cursor=%+v", f.chain.headerCalls, cursor)
	}
	var order []string
	for _, p := range f.repo.writes {
		order = append(order, p.TransactionHash)
	}
	want := []string{fmt.Sprintf("0x%064x", 1000), fmt.Sprintf("0x%064x", 1001), fmt.Sprintf("0x%064x", 1000), fmt.Sprintf("0x%064x", 1001)}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("parallel reads changed persistence order: %v", order)
	}
}

func TestPostgresScannerFreshEndReorgRetiresUnpublishedPartialRange(t *testing.T) {
	f := newScannerPGFixture(t, 2)
	f.chain.changedEnd = fmt.Sprintf("0x%064x", 9999)
	if err := f.service.ScanLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.repo.confirmCalls != 0 || f.count(t, `SELECT COUNT(*) FROM market_trade_ready_events WHERE valid`) != 0 || f.count(t, `SELECT COUNT(*) FROM market_scan_coverage`) != 0 {
		t.Fatal("changed end hash published or covered the partial range")
	}
	if f.count(t, `SELECT COUNT(*) FROM market_transaction_receipts WHERE reorged AND NOT confirmed`) != 2 || f.count(t, `SELECT COUNT(*) FROM market_scanned_blocks WHERE block_number=100 AND canonical=1`) != 0 {
		t.Fatal("staged old inclusion survived a proven reorg")
	}
	cursor, _ := f.db.GetMarketChainCursor(context.Background(), 56, DefaultCursorKey)
	if cursor.NextBlockNumber != 100 {
		t.Fatal("mid-range reorg skipped a block")
	}
}

type receiptBatchScannerChain struct {
	*scannerPipelineChain
	mode                   string
	batchReads             int
	individualReceiptReads atomic.Int32
}

func (c *receiptBatchScannerChain) MarketFullBlock(ctx context.Context, number uint64, key, fallback string) (map[string]any, error) {
	if c.mode == "oversized_block" {
		return nil, chain.ErrMarketRPCResponseTooLarge
	}
	block, err := c.BlockByNumber(ctx, number, true, key, fallback)
	if err != nil {
		return nil, err
	}
	transactions := make([]any, 0, len(c.transactions))
	for _, tx := range c.transactions {
		transactions = append(transactions, tx)
	}
	block["transactions"] = transactions
	return block, nil
}

func (c *receiptBatchScannerChain) MarketBlockReceipts(context.Context, string, string, string) ([]any, error) {
	c.batchReads++
	if c.mode == "unavailable" {
		return nil, errors.New("local block receipt endpoint unavailable")
	}
	values := make([]any, 0, len(c.receipts))
	for _, receipt := range c.receipts {
		values = append(values, receipt)
	}
	switch c.mode {
	case "missing":
		return values[:len(values)-1], nil
	case "duplicate":
		return append(values, values[0]), nil
	case "mismatch":
		corrupt := map[string]any{}
		for key, value := range values[0].(map[string]any) {
			corrupt[key] = value
		}
		corrupt["from"] = "0x" + strings.Repeat("f", 40)
		values[0] = corrupt
	}
	return values, nil
}

func (c *receiptBatchScannerChain) TransactionReceipt(ctx context.Context, hash, key, fallback string) (map[string]any, error) {
	c.individualReceiptReads.Add(1)
	return c.scannerPipelineChain.TransactionReceipt(ctx, hash, key, fallback)
}

func TestPostgresScannerBlockReceiptsPreservePublishingAndFallback(t *testing.T) {
	for _, mode := range []string{"complete", "missing", "duplicate", "mismatch", "unavailable", "oversized_block", "reorg"} {
		t.Run(mode, func(t *testing.T) {
			const count = 4
			fixture := newScannerPGFixture(t, count)
			client := &receiptBatchScannerChain{scannerPipelineChain: fixture.chain, mode: mode}
			fixture.service.chain = client
			if mode == "reorg" {
				fixture.chain.changedEnd = fmt.Sprintf("0x%064x", 9999)
			}
			if err := fixture.service.ScanLogs(context.Background()); err != nil {
				t.Fatal(err)
			}
			wantBatchReads, wantIndividualReads := 1, int32(count)
			wantReady, wantCursor := int64(count), int64(101)
			switch mode {
			case "oversized_block":
				wantBatchReads = 0
			case "complete":
				wantIndividualReads = 0
			case "reorg":
				wantIndividualReads, wantReady, wantCursor = 0, 0, 100
			}
			if client.batchReads != wantBatchReads || client.individualReceiptReads.Load() != wantIndividualReads {
				t.Fatalf("batch=%d individual=%d, want %d/%d", client.batchReads, client.individualReceiptReads.Load(), wantBatchReads, wantIndividualReads)
			}
			cursor, err := fixture.db.GetMarketChainCursor(context.Background(), 56, DefaultCursorKey)
			if err != nil || cursor.NextBlockNumber != wantCursor || fixture.count(t, `SELECT COUNT(*) FROM market_trade_ready_events WHERE valid`) != wantReady {
				t.Fatalf("cursor=%+v want %d and %d ready trades, %v", cursor, wantCursor, wantReady, err)
			}
			if len(fixture.repo.writes) != count {
				t.Fatalf("persisted %d transactions, want %d", len(fixture.repo.writes), count)
			}
			for i, write := range fixture.repo.writes {
				if write.TransactionHash != fmt.Sprintf("0x%064x", 1000+i) {
					t.Fatal("block receipts changed transaction write order")
				}
			}
		})
	}
}

func TestPostgresScannerPartialReorgReplaysNewInclusionAndPreservesSentHistory(t *testing.T) {
	f := newScannerPGFixture(t, 1)
	ctx := context.Background()
	if _, err := f.db.UpsertMarketScannedBlock(ctx, store.UpsertMarketScannedBlockParams{ChainKey: "bsc", ChainID: 56, CursorKey: DefaultCursorKey, BlockNumber: 100, BlockHash: testHashB, ParentHash: testHashC, BlockTimestamp: &f.at}); err != nil {
		t.Fatal(err)
	}
	scope, err := f.service.loadParserContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("0x%064x", 1000)
	direct := New(Dependencies{Repository: f.db, Chain: f.chain}, Settings{ChainKey: "bsc", ChainID: 56})
	if err := direct.hydrateParseAndPersist(ctx, hash, scope, "local_fixture", true); err != nil {
		t.Fatal(err)
	}
	events, err := f.db.ListMarketTradeEventsForTarget(ctx, f.target, 0, true, 10)
	if err != nil || len(events) != 1 {
		t.Fatal("fixture did not create a published parent", err)
	}
	rule, err := f.db.CreateMarketRuleWithinQuota(ctx, store.CreateMarketRuleParams{DeBoxUserID: scannerFixtureOwner, MarketProjectID: f.project.ID, RuleType: plans.MarketLargeBuy, PoolScope: "all", DeploymentScope: "all", ThresholdUnit: "usd", ThresholdValue: "1", NotificationChatID: scannerFixtureOwner}, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	mre, _, err := f.db.CreateMarketRuleEventAndEnqueue(ctx, store.CreateMarketRuleEventParams{MarketRuleID: rule.ID, MarketEventID: events[0].ID, TriggerKey: events[0].EventKey, CurrentValue: events[0].USDValue, Details: events[0].Metadata, Note: "immutable sent analysis", NotificationStatus: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.db.CreateNotificationDetailSnapshot(ctx, store.CreateNotificationDetailSnapshotParams{SourceKey: fmt.Sprintf("market_realtime:%d", mre.ID), NotificationKind: store.NotificationKindMarketRealtime, SourceType: "market_notification_delivery", SourceID: &mre.ID, DeBoxUserID: scannerFixtureOwner, NotificationChatID: scannerFixtureOwner, NotificationChatType: "private", NotificationText: "immutable fixture sent text", Details: json.RawMessage(`{"immutable":"sent"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE market_rule_events SET notification_snapshot_id=$2 WHERE id=$1`, mre.ID, snapshot.ID); err != nil {
		t.Fatal(err)
	}
	message := "local-fixture-sent"
	if _, err := f.db.UpdateMarketRuleEventNotification(ctx, mre.ID, "sent", &message, ""); err != nil {
		t.Fatal(err)
	}
	sentRow := func() string {
		var row string
		if err := f.pool.QueryRow(ctx, `SELECT row_to_json(x)::text FROM(SELECT notification_status,delivery_outcome,notification_message_id,notification_error,notification_attempts,notification_snapshot_id,details,note FROM market_rule_events WHERE id=$1)x`, mre.ID).Scan(&row); err != nil {
			t.Fatal(err)
		}
		return row
	}
	before := sentRow()
	newHash := fmt.Sprintf("0x%064x", 9999)
	f.chain.blocks[100]["hash"] = newHash
	f.chain.transactions[hash]["blockHash"] = newHash
	f.chain.receipts[hash]["blockHash"] = newHash
	logs := f.chain.receipts[hash]["logs"].([]chain.RPCLog)
	for i := range logs {
		logs[i].BlockHash = newHash
	}
	f.chain.receipts[hash]["logs"] = logs
	f.chain.logs = logs
	f.chain.headerCalls = map[uint64]int{}
	if err := f.service.ScanLogs(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.repo.writes) != 0 || f.count(t, `SELECT COUNT(*) FROM market_trade_ready_events WHERE valid`) != 0 || sentRow() != before {
		t.Fatal("partial canonical conflict wrote new facts or altered sent history")
	}
	if err := f.service.ScanLogs(ctx); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT COUNT(*) FROM market_trade_ready_events WHERE valid`) != 1 || f.count(t, `SELECT COUNT(*) FROM market_trade_parents`) != 1 || sentRow() != before {
		t.Fatal("new inclusion did not recover the same logical parent or changed immutable sent evidence")
	}
	var text string
	if err := f.pool.QueryRow(ctx, `SELECT notification_text FROM notification_detail_snapshots WHERE id=$1`, snapshot.ID).Scan(&text); err != nil || text != "immutable fixture sent text" {
		t.Fatal("sent text changed", err)
	}
}

func TestPostgresScannerInitializedPoolScopeReloadDoesNotPublishOrPrewriteFutureReceipt(t *testing.T) {
	f := newScannerPGFixture(t, 2)
	ctx := context.Background()
	// An empty initial selection can exist before on-chain pool discovery. This
	// setup remains confined to this test schema; EnsureMarketProjectPool itself
	// performs the real selection and deployment linkage during parsing.
	for _, query := range []string{`UPDATE market_project_pools SET selected=0,is_primary=0`, `UPDATE market_projects SET main_pool_id=NULL`, `UPDATE market_project_deployments SET default_market_pool_id=NULL`, `UPDATE market_asset_deployments SET default_market_pool_id=NULL`} {
		if _, err := f.pool.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	_, _, _, initial, _ := initializedFixture(t, "bsc", marketparse.AdapterV2)
	for index, poolAddress := range []string{initializedPool, "0x" + strings.Repeat("6", 40)} {
		hash := fmt.Sprintf("0x%064x", 1000+index)
		factory := initial.Logs[0]
		factory.Data = "0x" + initializedAddressWord(poolAddress) + initializedWord(1)
		swap := initializedSwapLog()
		swap.Address = poolAddress
		var logs []chain.RPCLog
		for j, log := range []marketparse.Log{factory, swap} {
			logs = append(logs, chain.RPCLog{Address: log.Address, Topics: log.Topics, Data: log.Data, BlockNumber: "0x64", BlockHash: testHashB, TransactionHash: hash, TransactionIndex: fmt.Sprintf("0x%x", index), LogIndex: fmt.Sprintf("0x%x", index*2+j)})
		}
		f.chain.receipts[hash]["logs"] = logs
		if index == 0 {
			f.chain.logs = nil
		}
		f.chain.logs = append(f.chain.logs, logs...)
	}
	if err := f.service.ScanLogs(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.repo.writes) != 1 || f.repo.confirmCalls != 0 || f.count(t, `SELECT COUNT(*) FROM market_transaction_receipts`) != 1 || f.count(t, `SELECT COUNT(*) FROM market_trade_ready_events WHERE valid`) != 0 || f.count(t, `SELECT COUNT(*) FROM market_scan_coverage`) != 0 {
		t.Fatal("scope reload published or prewrote the next prefetched receipt")
	}
	cursor, _ := f.db.GetMarketChainCursor(ctx, 56, DefaultCursorKey)
	if cursor.NextBlockNumber != 100 {
		t.Fatal("scope reload advanced the cursor")
	}
	if err := f.service.ScanLogs(ctx); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT COUNT(*) FROM market_transaction_receipts`) != 2 || f.count(t, `SELECT COUNT(*) FROM market_trade_ready_events WHERE valid`) != 2 || f.count(t, `SELECT COUNT(*) FROM market_trade_legs`) != 2 {
		t.Fatal("reloaded scope did not keep both receipts complete without duplication")
	}
	cursor, _ = f.db.GetMarketChainCursor(ctx, 56, DefaultCursorKey)
	if cursor.NextBlockNumber != 101 {
		t.Fatal("complete reloaded range did not advance")
	}
}
