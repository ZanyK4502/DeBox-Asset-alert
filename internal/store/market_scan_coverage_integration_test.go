package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestPostgresMarketScanCoverageRequiresActualCompleteScopedRanges(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 14, 1, 0, 0, 0, time.UTC)
	hash := func(block int64) string { return fmt.Sprintf("0x%064x", block) }
	boundary := func(block int64) time.Time { return at.Add(time.Duration(block-99) * time.Second) }
	put := func(block int64) {
		t.Helper()
		when := boundary(block)
		if _, err := database.UpsertMarketScannedBlock(ctx, UpsertMarketScannedBlockParams{ChainKey: "bsc", ChainID: 56, CursorKey: "coverage-test",
			BlockNumber: block, BlockHash: hash(block), ParentHash: hash(block - 1), BlockTimestamp: &when}); err != nil {
			t.Fatal(err)
		}
	}
	record := func(from, through int64, selected []int64) {
		t.Helper()
		put(from - 1)
		put(through)
		if err := database.RecordMarketScanCoverage(ctx, RecordMarketScanCoverageParams{ChainID: 56, CursorKey: "coverage-test", ScopePoolIDs: selected,
			ScopeTokenAddresses: []string{tradeFixtureToken}, FromBlock: from, ThroughBlock: through,
			AfterAt: boundary(from - 1), ThroughAt: boundary(through), AfterHash: hash(from - 1), ThroughHash: hash(through)}); err != nil {
			t.Fatal(err)
		}
	}
	target := MarketRuleTarget{ChainID: 56, TokenAddress: tradeFixtureToken, TradePoolIDs: pools[:2]}
	check := func(from, through time.Time, block int64, want bool) {
		t.Helper()
		got, err := database.MarketTradeWindowScanCoverageComplete(ctx, target, []MarketTradeScanWindow{{From: from, Through: through, ThroughBlock: block}})
		if err != nil || got != want {
			t.Fatalf("coverage [%s,%s] block=%d = %t,%v want %t", from, through, block, got, err, want)
		}
	}
	check(at.Add(time.Second), boundary(110), 110, false)
	if _, err := database.AdvanceMarketChainCursor(ctx, AdvanceMarketChainCursorParams{ChainKey: "bsc", ChainID: 56, CursorKey: "coverage-test", NextBlockNumber: 1000000, SafeBlockNumber: 1000000, LastBlockHash: pointerOf(hash(110)), Status: "active"}); err != nil {
		t.Fatal(err)
	}
	check(at.Add(time.Second), boundary(110), 110, false) // advancing is no scan proof
	record(100, 110, pools[:2])
	check(at, boundary(110), 110, false) // previous unscanned block at the inclusive lower bound
	check(at.Add(time.Second), boundary(110), 110, true)
	check(at.Add(time.Second), boundary(110), 99, false)
	target.TradePoolIDs = pools
	check(at.Add(time.Second), boundary(110), 110, false)
	target.TradePoolIDs = pools[:2]
	record(111, 120, pools[:2])
	check(at.Add(time.Second), boundary(120), 120, true)
	var rows int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_scan_coverage`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("adjacent ranges did not coalesce: %d,%v", rows, err)
	}
	record(122, 130, pools[:2])
	check(at.Add(time.Second), boundary(130), 130, false) // real one-block gap
	record(121, 121, pools[:1])
	target.TradePoolIDs = pools[:1]
	check(at.Add(time.Second), boundary(130), 130, true) // changing supersets can prove only this selected scope
	target.TradePoolIDs = pools[:2]
	check(at.Add(time.Second), boundary(130), 130, false)
	// Cleanup cannot delete a parser scope while its completed coverage survives.
	if _, err := database.CleanupOrphanMarketTradeData(ctx, at.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	check(at.Add(time.Second), boundary(120), 120, true)
	// Historical fact deletion invalidates old windows before the deletion, even
	// when this fixture has no raw trades to remove.
	if _, err := database.CleanupMarketCollectionData(ctx, at, at.Add(5*time.Second), at); err != nil {
		t.Fatal(err)
	}
	check(at.Add(4*time.Second), boundary(120), 120, false)
	check(at.Add(5*time.Second), boundary(120), 120, true)
	// An eventless fork must also remove the coverage claim, not just trade rows.
	put(109)
	if _, err := database.MarkMarketBlockReorged(ctx, 56, 110, hash(10000)); err != nil {
		t.Fatal(err)
	}
	check(at.Add(5*time.Second), boundary(109), 109, true)
	check(at.Add(5*time.Second), boundary(110), 110, false)
}

func TestPostgresMarketScanCoverageWaitsForReceiptConfirmation(t *testing.T) {
	database, _, pools := tradeFactsFixture(t)
	ctx := context.Background()
	params := tradeReceiptFixture(t, pools[:2])
	params.Confirmed = false
	params.OccurredAt = params.OccurredAt.Truncate(time.Microsecond)
	tradeCanonicalFixture(t, database, params)
	previousAt := params.OccurredAt.Add(-time.Second)
	previousHash := fmt.Sprintf("0x%064x", 99)
	if _, err := database.UpsertMarketScannedBlock(ctx, UpsertMarketScannedBlockParams{ChainKey: "bsc", ChainID: 56, CursorKey: "market_logs", BlockNumber: 99, BlockHash: previousHash, ParentHash: fmt.Sprintf("0x%064x", 98), BlockTimestamp: &previousAt}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.PersistMarketTransaction(ctx, params); err != nil {
		t.Fatal(err)
	}
	coverage := RecordMarketScanCoverageParams{ChainID: 56, CursorKey: "market_logs", FromBlock: 100, ThroughBlock: 100, ScopePoolIDs: pools[:2], ScopeTokenAddresses: []string{tradeFixtureToken}, AfterAt: previousAt, ThroughAt: params.OccurredAt, AfterHash: previousHash, ThroughHash: params.BlockHash}
	if err := database.RecordMarketScanCoverage(ctx, coverage); err == nil {
		t.Fatal("unconfirmed receipt published complete scan coverage")
	}
	if _, err := database.MarkMarketBlockConfirmed(ctx, 56, 100); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordMarketScanCoverage(ctx, coverage); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresMarketScanCoverageJoinsDifferentScopesWithEqualBlockTimes(t *testing.T) {
	database, _, pools := tradeFactsFixture(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 14, 1, 0, 0, 0, time.UTC)
	hash := func(block int64) string { return fmt.Sprintf("0x%064x", block) }
	for _, block := range []int64{99, 110, 120} {
		when := at
		if block > 99 {
			when = when.Add(time.Second)
		}
		if _, err := database.UpsertMarketScannedBlock(ctx, UpsertMarketScannedBlockParams{ChainKey: "bsc", ChainID: 56, CursorKey: "equal-times", BlockNumber: block, BlockHash: hash(block), ParentHash: hash(block - 1), BlockTimestamp: &when}); err != nil {
			t.Fatal(err)
		}
	}
	for i, span := range [][2]int64{{100, 110}, {111, 120}} {
		after := at
		if i > 0 {
			after = after.Add(time.Second)
		}
		if err := database.RecordMarketScanCoverage(ctx, RecordMarketScanCoverageParams{ChainID: 56, CursorKey: "equal-times", ScopePoolIDs: pools[:i+1], ScopeTokenAddresses: []string{tradeFixtureToken}, FromBlock: span[0], ThroughBlock: span[1], AfterAt: after, ThroughAt: at.Add(time.Second), AfterHash: hash(span[0] - 1), ThroughHash: hash(span[1])}); err != nil {
			t.Fatal(err)
		}
	}
	complete, err := database.MarketTradeWindowScanCoverageComplete(ctx, MarketRuleTarget{ChainID: 56, TokenAddress: tradeFixtureToken, TradePoolIDs: pools[:1]}, []MarketTradeScanWindow{{From: at.Add(500 * time.Millisecond), Through: at.Add(time.Second), ThroughBlock: 120}})
	if err != nil || !complete {
		t.Fatalf("same-second contiguous coverage rejected: %t,%v", complete, err)
	}
}
