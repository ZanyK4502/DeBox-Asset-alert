package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
)

func quoteFailureTarget(pool int64) MarketCollectionTarget {
	return MarketCollectionTarget{ChainID: 56, TokenAddress: tradeFixtureToken, MarketPoolID: pool}
}

func TestPostgresQuoteFailureIsAppendOnlyBoundedAndRecoversOnNewSuccess(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	sourceAt := at.Add(-time.Second)
	proof := marketquality.Quote{Status: marketquality.StatusCached, FetchedAt: &at, SourceTimestamp: &sourceAt}
	largeRaw, _ := json.Marshal(map[string]string{"payload": strings.Repeat("x", 100000)})
	original, err := database.CreateMarketSnapshot(ctx, CreateMarketSnapshotParams{ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, MarketPoolID: pools[0],
		PriceUSD: pointerOf("1.25"), LiquidityUSD: pointerOf("250"), Volume5mUSD: pointerOf("10"), Buys5m: pointerOf(int64(3)),
		Source: "dexscreener", SourceTimestamp: &sourceAt, QuoteQuality: proof, CapturedAt: at, RawPayload: largeRaw})
	if err != nil {
		t.Fatal(err)
	}
	var originalCTID, originalHash string
	if err := pool.QueryRow(ctx, `SELECT ctid::text,md5(raw_payload::text) FROM market_snapshots WHERE id=$1`, original.ID).Scan(&originalCTID, &originalHash); err != nil {
		t.Fatal(err)
	}
	failureAt := at.Add(30 * time.Second)
	target := quoteFailureTarget(pools[0])
	count, err := database.RecordMarketQuoteFailure(ctx, 56, []MarketCollectionTarget{target, target}, failureAt)
	if err != nil || count != 1 {
		t.Fatalf("first failure count=%d err=%v", count, err)
	}
	latest, err := database.LatestMarketSnapshot(ctx, 56, tradeFixtureToken, &pools[0])
	if err != nil || latest == nil || latest.ID == original.ID || latest.Source != "quote_availability" || latest.QuoteQuality.Status != "stale_fallback" ||
		!latest.CapturedAt.Equal(failureAt) || latest.QuoteQuality.FetchedAt == nil || !latest.QuoteQuality.FetchedAt.Equal(at) ||
		latest.QuoteQuality.SourceTimestamp == nil || !latest.QuoteQuality.SourceTimestamp.Equal(sourceAt) || latest.QuoteQuality.AvailableAt(failureAt) {
		t.Fatalf("failure was not frozen unavailable with original times: %#v,%v", latest, err)
	}
	requireTradeAmount(t, latest.PriceUSD, "1.25")
	requireTradeAmount(t, latest.LiquidityUSD, "250")
	if latest.Buys5m == nil || *latest.Buys5m != 3 || latest.SourceTimestamp == nil || !latest.SourceTimestamp.Equal(sourceAt) {
		t.Fatalf("marker lost fields: %#v", latest)
	}
	var audit struct {
		Failure        bool   `json:"quote_failure"`
		PreviousID     int64  `json:"previous_snapshot_id"`
		OriginalSource string `json:"original_source"`
	}
	if err := json.Unmarshal(latest.RawPayload, &audit); err != nil || !audit.Failure || audit.PreviousID != original.ID || audit.OriginalSource != "dexscreener" || len(latest.RawPayload) > 300 {
		t.Fatalf("audit copied/lost original data: %s,%v", latest.RawPayload, err)
	}
	var afterCTID, afterHash string
	if err := pool.QueryRow(ctx, `SELECT ctid::text,md5(raw_payload::text) FROM market_snapshots WHERE id=$1`, original.ID).Scan(&afterCTID, &afterHash); err != nil {
		t.Fatal(err)
	}
	if afterCTID != originalCTID || afterHash != originalHash {
		t.Fatal("failure mutated the original historical quote")
	}
	if count, err := database.RecordMarketQuoteFailure(ctx, 56, []MarketCollectionTarget{target}, failureAt.Add(time.Second)); err != nil || count != 0 {
		t.Fatalf("repeated failure inserted again: %d,%v", count, err)
	}
	before, err := database.MarketValuationSnapshot(ctx, 56, tradeFixtureToken, &pools[0], failureAt.Add(-time.Second))
	if err != nil || before == nil || before.ID != original.ID || !before.QuoteQuality.AvailableAt(failureAt.Add(-time.Second)) {
		t.Fatalf("as-of before fault: %#v,%v", before, err)
	}
	after, err := database.MarketValuationSnapshot(ctx, 56, tradeFixtureToken, &pools[0], failureAt.Add(time.Second))
	if err != nil || after == nil || after.ID != latest.ID || after.QuoteQuality.AvailableAt(failureAt.Add(time.Second)) {
		t.Fatalf("as-of skipped failure: %#v,%v", after, err)
	}
	successAt := at.Add(time.Minute)
	success, err := database.CreateMarketSnapshot(ctx, CreateMarketSnapshotParams{ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, MarketPoolID: pools[0],
		PriceUSD: pointerOf("2"), Source: "dexscreener", QuoteQuality: marketquality.Quote{Status: "live", FetchedAt: &successAt}, CapturedAt: successAt})
	if err != nil {
		t.Fatal(err)
	}
	if count, err := database.RecordMarketQuoteFailure(ctx, 56, []MarketCollectionTarget{target}, at.Add(40*time.Second)); err != nil || count != 0 {
		t.Fatalf("late old failure overrode newer success: %d,%v", count, err)
	}
	latest, err = database.LatestMarketSnapshot(ctx, 56, tradeFixtureToken, &pools[0])
	if err != nil || latest == nil || latest.ID != success.ID || !latest.QuoteQuality.AvailableAt(successAt) {
		t.Fatalf("new success did not restore quote: %#v,%v", latest, err)
	}
	if count, err := database.RecordMarketQuoteFailure(ctx, 56, []MarketCollectionTarget{target}, successAt.Add(time.Second)); err != nil || count != 1 {
		t.Fatalf("later failure after recovery was ignored: %d,%v", count, err)
	}
}

func TestPostgresQuoteFailureSameMicrosecondInvalidatesWithoutChangingOriginal(t *testing.T) {
	database, _, pools := tradeFactsFixture(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 14, 8, 0, 0, 123456000, time.UTC)
	original, err := database.CreateMarketSnapshot(ctx, CreateMarketSnapshotParams{ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, MarketPoolID: pools[0],
		PriceUSD: pointerOf("1"), Source: "dexscreener", QuoteQuality: marketquality.Quote{Status: "live", FetchedAt: &at}, CapturedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	target := quoteFailureTarget(pools[0])
	if count, err := database.RecordMarketQuoteFailure(ctx, 56, []MarketCollectionTarget{target}, at); err != nil || count != 1 {
		t.Fatalf("same-microsecond fault not persisted: %d,%v", count, err)
	}
	latest, err := database.MarketValuationSnapshot(ctx, 56, tradeFixtureToken, &pools[0], at)
	if err != nil || latest == nil || latest.ID <= original.ID || !latest.CapturedAt.Equal(original.CapturedAt) || latest.QuoteQuality.AvailableAt(at) || latest.Source != "quote_availability" {
		t.Fatalf("latest ID did not invalidate equal timestamp: %#v,%v", latest, err)
	}
	if count, err := database.RecordMarketQuoteFailure(ctx, 56, []MarketCollectionTarget{target}, at); err != nil || count != 0 {
		t.Fatalf("same-microsecond retry not idempotent: %d,%v", count, err)
	}
}

func TestPostgresQuoteFailureDoesNotInventHistoryOrReviveExpiredUnknownValues(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	old := at.Add(-3 * time.Minute)
	for _, entry := range []struct {
		pool    int64
		quality marketquality.Quote
	}{
		{pools[1], marketquality.Quote{Status: "live", FetchedAt: &old}},
		{pools[2], marketquality.Quote{}},
	} {
		if _, err := database.CreateMarketSnapshot(ctx, CreateMarketSnapshotParams{ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, MarketPoolID: entry.pool,
			PriceUSD: pointerOf("1"), Source: "dexscreener", QuoteQuality: entry.quality, CapturedAt: old}); err != nil {
			t.Fatal(err)
		}
	}
	if count, err := database.RecordMarketQuoteFailure(ctx, 56, nil, at); err != nil || count != 0 {
		t.Fatalf("empty target failure: %d,%v", count, err)
	}
	targets := []MarketCollectionTarget{quoteFailureTarget(pools[0]), quoteFailureTarget(pools[1]), quoteFailureTarget(pools[2])}
	if count, err := database.RecordMarketQuoteFailure(ctx, 56, targets, at); err != nil || count != 0 {
		t.Fatalf("no-history/expired/unknown quote invented marker: %d,%v", count, err)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_snapshots`).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("unexpected inserted rows=%d,%v", rows, err)
	}
}
