package marketcollector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func (*fakeRepository) PersistMarketTransaction(context.Context, store.MarketTransactionParams) ([]store.MarketEvent, error) {
	return nil, nil
}

func (*fakeRepository) UpdateMarketFourMemeFromEvent(context.Context, int64) (int64, error) {
	return 0, nil
}

type receiptFactRepository struct {
	*fakeRepository
	params        store.MarketTransactionParams
	writes        int
	valuationHits int
	err           error
	snapshot      *store.MarketSnapshot
}

func (r *receiptFactRepository) PersistMarketTransaction(_ context.Context, params store.MarketTransactionParams) ([]store.MarketEvent, error) {
	r.writes++
	r.params = params
	return nil, r.err
}

func (r *receiptFactRepository) CreateMarketEvent(context.Context, store.CreateMarketEventParams) (store.MarketEvent, bool, error) {
	panic("hydration must not write individual events before the receipt batch")
}

func (r *receiptFactRepository) LatestMarketSnapshot(context.Context, int64, string, *int64) (*store.MarketSnapshot, error) {
	r.valuationHits++
	if r.snapshot != nil {
		return r.snapshot, nil
	}
	price := "1"
	fetched := time.Unix(1700000000, 0).UTC()
	return &store.MarketSnapshot{ID: 7, PriceUSD: &price, CapturedAt: fetched,
		QuoteQuality: marketquality.Quote{Status: marketquality.StatusLive, FetchedAt: &fetched}}, nil
}

type receiptFactChain struct {
	*fakeChain
	transaction map[string]any
	receipt     map[string]any
}

func (c *receiptFactChain) RPCTransactionByHash(context.Context, string, string, string) (map[string]any, error) {
	return c.transaction, nil
}

func (c *receiptFactChain) TransactionReceipt(context.Context, string, string, string) (map[string]any, error) {
	return c.receipt, nil
}

func receiptFactFixture(t *testing.T) (*Service, *receiptFactRepository, *receiptFactChain, parserContext) {
	t.Helper()
	const token = "0x1111111111111111111111111111111111111111"
	const quote = "0x2222222222222222222222222222222222222222"
	const sender = "0x4444444444444444444444444444444444444444"
	const router = "0x5555555555555555555555555555555555555555"
	pools := []marketparse.Pool{}
	logs := []chain.RPCLog{}
	for i, address := range []string{"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"} {
		pools = append(pools, marketparse.Pool{
			ID: int64(7 + i), ChainID: 56, Protocol: "pancakeswap", Version: "v2", Adapter: marketparse.AdapterV2,
			PoolKey: address, LogAddress: address,
			Token0: marketparse.Token{Address: quote}, Token1: marketparse.Token{Address: token},
		})
		logs = append(logs, chain.RPCLog{
			Address: address, Topics: []string{
				"0xd78ad95fa46c994b6551d0da85fc275fe613ce37657fb8d5e3d130840159d822",
				"0x" + strings.Repeat("0", 24) + router[2:], "0x" + strings.Repeat("0", 24) + sender[2:],
			},
			Data:            fmt.Sprintf("0x%064x%064x%064x%064x", 6000, 0, 0, 6000),
			TransactionHash: testHashA, BlockHash: testHashB, BlockNumber: "0x64", TransactionIndex: "0x1",
			LogIndex: fmt.Sprintf("0x%x", i+1),
		})
	}
	parser, err := marketparse.NewParser(pools, nil)
	if err != nil {
		t.Fatal(err)
	}
	chainClient := &receiptFactChain{
		fakeChain: &fakeChain{blocks: map[uint64]map[string]any{100: {
			"number": "0x64", "hash": testHashB, "parentHash": testHashC, "timestamp": "0x6553f100",
		}}},
		transaction: map[string]any{"hash": testHashA, "blockHash": testHashB, "blockNumber": "0x64", "transactionIndex": "0x1", "from": sender, "to": router},
		receipt:     map[string]any{"transactionHash": testHashA, "blockHash": testHashB, "blockNumber": "0x64", "transactionIndex": "0x1", "status": "0x1", "from": sender, "to": router, "logs": logs},
	}
	repository := &receiptFactRepository{fakeRepository: &fakeRepository{}}
	service := New(Dependencies{Repository: repository, Chain: chainClient}, Settings{ChainKey: "bsc", ChainID: 56})
	ctx := parserContext{parser: parser, pools: pools, targetTokens: []string{token, quote}, tokenDecimals: map[string]int{token: 0, quote: 0},
		poolByID:        map[int64]store.MarketCollectionTarget{7: {MarketPoolID: 7}, 8: {MarketPoolID: 8}},
		projectsByToken: map[string][]store.MarketProject{},
	}
	return service, repository, chainClient, ctx
}

func TestHydrationPersistsOneAtomicReceiptWithBothTokenSidesAndEveryPool(t *testing.T) {
	s, r, c, ctx := receiptFactFixture(t)
	expected, err := expectedScannerReceipts(c.receipt["logs"].([]chain.RPCLog))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.hydrateParseAndPersist(context.Background(), testHashA, ctx, "nodit_log_scanner", true, expected[testHashA]); err != nil {
		t.Fatal(err)
	}
	if r.writes != 1 || len(r.params.Events) != 2 || len(r.params.ScopePoolIDs) != 2 || !r.params.Confirmed {
		t.Fatalf("receipt batch = writes %d, events %d, pools %v", r.writes, len(r.params.Events), r.params.ScopePoolIDs)
	}
	if r.params.BlockHash != testHashB || r.params.BlockNumber != 100 || r.params.TransactionHash != testHashA || r.params.OccurredAt != time.Unix(1700000000, 0).UTC() {
		t.Fatalf("incorrect canonical receipt identity: %#v", r.params)
	}
	for _, event := range r.params.Events {
		if len(event.Legs) != 2 || event.Event.TokenAmount == nil || *event.Event.TokenAmount != "12000" {
			t.Fatalf("incomplete logical trade: %#v", event)
		}
		for _, leg := range event.Legs {
			if leg.TokenAmount == nil || *leg.TokenAmount != "6000" || leg.USDValue == nil || *leg.USDValue != "6000" {
				t.Fatalf("incorrect pool contribution: %#v", leg)
			}
		}
	}
	if r.valuationHits != 4 {
		t.Fatalf("snapshot lookups = %d, want one per token/pool or parent", r.valuationHits)
	}
}

func TestHydrationRetainsSub18DecimalTradeValuation(t *testing.T) {
	s, r, _, ctx := receiptFactFixture(t)
	const token = "0x1111111111111111111111111111111111111111"
	ctx.tokenDecimals[token] = 24
	if err := s.hydrateParseAndPersist(context.Background(), testHashA, ctx, "webhook", false); err != nil {
		t.Fatal(err)
	}
	for _, event := range r.params.Events {
		if event.Event.TokenAddress != token {
			continue
		}
		if event.Event.USDValue == nil || *event.Event.USDValue != "0.000000000000000000012" {
			t.Fatalf("small parent valuation was rounded away: %v", event.Event.USDValue)
		}
		for _, leg := range event.Legs {
			if leg.USDValue == nil || *leg.USDValue != "0.000000000000000000006" {
				t.Fatalf("small leg valuation was rounded away: %v", leg.USDValue)
			}
		}
		return
	}
	t.Fatal("monitored token trade missing")
}

func TestHydrationRejectsIncompleteOrConflictingReceiptBeforeAnyFactWrite(t *testing.T) {
	for _, test := range []string{"missing logs", "null logs", "missing status", "missing transaction", "wrong transaction", "removed log", "wrong log block", "wrong header", "failed with logs", "scanner missing log", "scanner content differs"} {
		t.Run(test, func(t *testing.T) {
			s, r, c, ctx := receiptFactFixture(t)
			logs := c.receipt["logs"].([]chain.RPCLog)
			expect, err := expectedScannerReceipts(logs)
			if err != nil {
				t.Fatal(err)
			}
			switch test {
			case "missing logs":
				delete(c.receipt, "logs")
			case "null logs":
				c.receipt["logs"] = nil
			case "missing status":
				delete(c.receipt, "status")
			case "missing transaction":
				c.transaction = nil
			case "wrong transaction":
				c.transaction["hash"] = testHashC
			case "removed log":
				logs[0].Removed = true
			case "wrong log block":
				logs[0].BlockHash = testHashC
			case "wrong header":
				c.blocks[100]["hash"] = testHashC
			case "failed with logs":
				c.receipt["status"] = "0x0"
			case "scanner missing log":
				c.receipt["logs"] = logs[:1]
			case "scanner content differs":
				logs[0].Data = fmt.Sprintf("0x%064x%064x%064x%064x", 6001, 0, 0, 6001)
			}
			if err := s.hydrateParseAndPersist(context.Background(), testHashA, ctx, "nodit_log_scanner", true, expect[testHashA]); err == nil {
				t.Fatal("incomplete receipt was accepted")
			}
			if r.writes != 0 {
				t.Fatal("invalid receipt wrote transaction facts")
			}
		})
	}
}

func TestHydrationAcceptsGenuineEmptyFailedReceiptAndPropagatesAtomicStoreFailure(t *testing.T) {
	s, r, c, ctx := receiptFactFixture(t)
	c.receipt["status"] = "0x0"
	c.receipt["logs"] = []chain.RPCLog{}
	if err := s.hydrateParseAndPersist(context.Background(), testHashA, ctx, "webhook", false); err != nil || r.writes != 1 || len(r.params.Events) != 0 {
		t.Fatalf("genuine failed transaction = %v, writes=%d", err, r.writes)
	}
	s, r, _, ctx = receiptFactFixture(t)
	r.err = errors.New("atomic persistence failed")
	if err := s.hydrateParseAndPersist(context.Background(), testHashA, ctx, "webhook", false); !errors.Is(err, r.err) || r.writes != 1 {
		t.Fatalf("atomic write failure = %v, writes=%d", err, r.writes)
	}
}

type aheadCursorRepository struct {
	*fakeRepository
	confirmCalls int
}

func (r *aheadCursorRepository) MarkMarketBlockConfirmed(context.Context, int64, int64) (int64, error) {
	r.confirmCalls++
	return 0, nil
}

type aheadCursorChain struct{ *fakeChain }

func (aheadCursorChain) LatestBlockNumber(context.Context, string, string) (uint64, error) {
	return 200, nil
}

func TestCursorAheadOfSafeHeightDoesNotConfirmAnOldQueue(t *testing.T) {
	r := &aheadCursorRepository{fakeRepository: &fakeRepository{
		activeProjects: []store.MarketProject{{TokenAddress: "0x1111111111111111111111111111111111111111", TokenDecimals: 0}},
		cursor:         &store.MarketChainCursor{ChainKey: "bsc", ChainID: 56, CursorKey: DefaultCursorKey, NextBlockNumber: 1000},
	}}
	s := New(Dependencies{Repository: r, Chain: aheadCursorChain{&fakeChain{}}}, Settings{Enabled: true, ChainKey: "bsc", ChainID: 56, ConfirmationDepth: 12})
	if err := s.ScanLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.confirmCalls != 0 || r.cursor.NextBlockNumber != 1000 {
		t.Fatal("cursor-only path confirmed events or moved the cursor")
	}
}

type parserScopeRepository struct {
	*fakeRepository
	targets []store.MarketCollectionTarget
}

func (r parserScopeRepository) ListMarketCollectionTargets(context.Context, int64) ([]store.MarketCollectionTarget, error) {
	return r.targets, nil
}

func TestParserCoverageContainsOnlyRegisteredPoolsAndKeepsVerifiedProjectPrecision(t *testing.T) {
	const token = "0x1111111111111111111111111111111111111111"
	const quote = "0x2222222222222222222222222222222222222222"
	a, b := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	r := parserScopeRepository{fakeRepository: &fakeRepository{activeProjects: []store.MarketProject{{TokenAddress: token, TokenDecimals: 0}}}, targets: []store.MarketCollectionTarget{
		{MarketPoolID: 7, ChainID: 56, Token0Address: token, Token1Address: quote, Token0Decimals: 18, Token1Decimals: 18, ParserAdapter: marketparse.AdapterV2, PoolKey: a, PoolAddress: &a},
		{MarketPoolID: 8, ChainID: 56, Token0Address: token, Token1Address: quote, Token0Decimals: 18, Token1Decimals: 18, ParserAdapter: marketparse.AdapterV2, PoolKey: b, PoolAddress: &b},
		{MarketPoolID: 9, ChainID: 56, Token0Address: "not-known", Token1Address: "not-known", ParserAdapter: ""},
		{MarketPoolID: 10, ChainID: 56, Token0Address: "not-known", Token1Address: "not-known", ParserAdapter: marketparse.AdapterV2},
	}}
	c := &precisionChain{fakeChain: &fakeChain{}, value: 6}
	s := New(Dependencies{Repository: r, Chain: c}, Settings{ChainKey: "bsc", ChainID: 56})
	scope, err := s.loadParserContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(scope.poolByID) != 2 || len(scope.pools) != 2 || scope.tokenDecimals[token] != 0 || scope.tokenDecimals[quote] != 6 || c.calls != 1 {
		t.Fatalf("invalid parser coverage or redundant metadata reads: %+v, RPCs=%d", scope, c.calls)
	}
	if scope.tokenDecimals[marketparse.NativeTokenAddress] != 18 {
		t.Fatal("native precision was treated as missing ERC20 metadata")
	}
	if _, exists := scope.poolByID[9]; exists {
		t.Fatal("a quote-only pool was declared completely parsed")
	}
}

func TestParserScopeAtCollectionCapDoesNotClaimCompleteness(t *testing.T) {
	r := parserScopeRepository{fakeRepository: &fakeRepository{
		activeProjects: make([]store.MarketProject, maxParserCollectionProjects),
	}}
	c := &precisionChain{fakeChain: &fakeChain{}, value: 6}
	s := New(Dependencies{Repository: r, Chain: c}, Settings{ChainKey: "bsc", ChainID: 56})
	if _, err := s.loadParserContext(context.Background()); err == nil || !strings.Contains(err.Error(), "complete scope cannot be established") {
		t.Fatalf("capped parser scope was accepted: %v", err)
	}
	if c.calls != 0 {
		t.Fatal("an incomplete scope performed token metadata work")
	}
}

type confirmationFailureRepository struct {
	*fakeRepository
	failure         error
	confirmCalls    int
	coverageFailure error
	coverageCalls   int
}

func (r *confirmationFailureRepository) MarkMarketBlockConfirmed(context.Context, int64, int64) (int64, error) {
	r.confirmCalls++
	return 0, r.failure
}

func (r *confirmationFailureRepository) RecordMarketScanCoverage(_ context.Context, params store.RecordMarketScanCoverageParams) error {
	r.coverageCalls++
	if params.FromBlock != 100 || params.ThroughBlock != 100 || params.AfterHash != testHashC || params.ThroughHash != testHashB || len(params.ScopeTokenAddresses) != 1 {
		return errors.New("incorrect completed range evidence")
	}
	return r.coverageFailure
}

func TestScannerConfirmationFailureRetainsRangeForRetry(t *testing.T) {
	failure := errors.New("confirmation unavailable")
	r := &confirmationFailureRepository{fakeRepository: &fakeRepository{
		activeProjects: []store.MarketProject{{TokenAddress: "0x1111111111111111111111111111111111111111", TokenDecimals: 0}},
		cursor:         &store.MarketChainCursor{ChainKey: "bsc", ChainID: 56, CursorKey: DefaultCursorKey, NextBlockNumber: 100},
	}, failure: failure}
	c := aheadCursorChain{&fakeChain{blocks: map[uint64]map[string]any{99: {
		"number": "0x63", "hash": testHashC, "parentHash": testHashA, "timestamp": "0x6553f0ff",
	}, 100: {
		"number": "0x64", "hash": testHashB, "parentHash": testHashC, "timestamp": "0x6553f100",
	}}}}
	s := New(Dependencies{Repository: r, Chain: c}, Settings{Enabled: true, ChainKey: "bsc", ChainID: 56, ScanBatchSize: 1})
	if err := s.ScanLogs(context.Background()); !errors.Is(err, failure) || r.cursor.NextBlockNumber != 100 || r.confirmCalls != 1 {
		t.Fatalf("confirmation failure lost the retry range: err=%v cursor=%d calls=%d", err, r.cursor.NextBlockNumber, r.confirmCalls)
	}
	r.failure = store.ErrMarketTradeConfirmationPending
	if err := s.ScanLogs(context.Background()); err != nil || r.cursor.NextBlockNumber != 100 || r.confirmCalls != 2 {
		t.Fatalf("partial confirmation lost the retry range: err=%v cursor=%d calls=%d", err, r.cursor.NextBlockNumber, r.confirmCalls)
	}
	r.failure = nil
	if r.coverageCalls != 0 {
		t.Fatal("failed confirmation recorded complete scan coverage")
	}
	r.coverageFailure = errors.New("coverage write unavailable")
	if err := s.ScanLogs(context.Background()); !errors.Is(err, r.coverageFailure) || r.cursor.NextBlockNumber != 100 || r.coverageCalls != 1 {
		t.Fatalf("coverage failure lost retry range: %v cursor=%d calls=%d", err, r.cursor.NextBlockNumber, r.coverageCalls)
	}
	r.coverageFailure = nil
	if err := s.ScanLogs(context.Background()); err != nil || r.cursor.NextBlockNumber != 101 || r.confirmCalls != 4 || r.coverageCalls != 2 {
		t.Fatalf("retry did not complete: err=%v cursor=%d calls=%d", err, r.cursor.NextBlockNumber, r.confirmCalls)
	}
}

type overCapacityLogChain struct {
	*fakeChain
	logs  []chain.RPCLog
	calls int
}

func (c *overCapacityLogChain) Logs(context.Context, string, string, chain.LogFilter) ([]chain.RPCLog, error) {
	c.calls++
	return c.logs, nil
}

func TestScannerBoundsCumulativeLogBatchesBeforeAppending(t *testing.T) {
	c := &overCapacityLogChain{fakeChain: &fakeChain{}, logs: make([]chain.RPCLog, maxMarketScannerLogs/2+1)}
	s := New(Dependencies{Repository: &fakeRepository{}, Chain: c}, Settings{ChainKey: "bsc", ChainID: 56})
	addresses := make([]string, logAddressBatchSize+1)
	for index := range addresses {
		addresses[index] = fmt.Sprintf("0x%040x", index+1)
	}
	logs, err := s.fetchLogsByAddressBatches(context.Background(), 100, 100, addresses, nil)
	if err == nil || !strings.Contains(err.Error(), "bounded capacity") || logs != nil || c.calls != 2 {
		t.Fatalf("oversized combined log response accepted: err=%v len=%d calls=%d", err, len(logs), c.calls)
	}
}
