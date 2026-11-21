package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
)

func TestPostgresMarketValuationObservationDoesNotSkipFallbackOrUseFuture(t *testing.T) {
	database, _, pools := tradeFactsFixture(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	fetched := at.Add(-time.Minute)
	quality := marketquality.Quote{Status: marketquality.StatusLive, FetchedAt: &fetched}
	makeSnapshot := func(captured time.Time, proof marketquality.Quote, price string) MarketSnapshot {
		t.Helper()
		snapshot, err := database.CreateMarketSnapshot(ctx, CreateMarketSnapshotParams{
			ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, MarketPoolID: pools[0],
			PriceUSD: &price, QuoteQuality: proof, Source: "quote-quality-test", CapturedAt: captured,
		})
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	fresh := makeSnapshot(at.Add(-time.Minute), quality, "1")
	fallbackProof := quality.Clone()
	fallbackProof.Status = marketquality.StatusStaleFallback
	fallback := makeSnapshot(at.Add(-30*time.Second), fallbackProof, "1")
	makeSnapshot(at.Add(time.Minute), quality, "999")
	observed, err := database.MarketValuationSnapshot(ctx, 56, tradeFixtureToken, &pools[0], at)
	if err != nil || observed == nil || observed.ID != fallback.ID || observed.QuoteQuality.Status != marketquality.StatusStaleFallback ||
		observed.QuoteQuality.FetchedAt == nil || !observed.QuoteQuality.FetchedAt.Equal(fetched) || observed.QuoteQuality.AvailableAt(at) {
		t.Fatalf("as-of must return the last unavailable observation with original proof: %#v, %v", observed, err)
	}
	observed, err = database.MarketValuationSnapshot(ctx, 56, tradeFixtureToken, nil, fresh.CapturedAt)
	if err != nil || observed == nil || observed.ID != fresh.ID {
		t.Fatalf("exact as-of inclusion: %#v,%v", observed, err)
	}
	observed, err = database.MarketValuationSnapshot(ctx, 56, tradeFixtureToken, &pools[0], fresh.CapturedAt.Add(-time.Nanosecond))
	if err != nil || observed != nil {
		t.Fatalf("future quote selected: %#v,%v", observed, err)
	}
	unknown := makeSnapshot(at.Add(-time.Second), marketquality.Quote{}, "777")
	observed, err = database.MarketValuationSnapshot(ctx, 56, tradeFixtureToken, &pools[0], at)
	if err != nil || observed == nil || observed.ID != unknown.ID || observed.QuoteQuality.AvailableAt(at) {
		t.Fatalf("unknown provenance must not fall back to fresh: %#v,%v", observed, err)
	}
}

func TestPostgresMarketSnapshotQuoteTimesMatchTimestampPrecisionWithoutMutatingCaller(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	input := time.Date(2026, 11, 14, 16, 0, 0, 123456789, time.FixedZone("caller-zone", 8*60*60))
	fetched, source := input, input
	proof := marketquality.Quote{Status: marketquality.StatusLive, FetchedAt: &fetched, SourceTimestamp: &source}
	snapshot, err := database.CreateMarketSnapshot(ctx, CreateMarketSnapshotParams{
		ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, MarketPoolID: pools[0],
		PriceUSD: pointerOf("1"), QuoteQuality: proof, SourceTimestamp: &source,
		Source: "nanosecond-quality-test", CapturedAt: input,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := input.UTC().Truncate(time.Microsecond)
	if !snapshot.CapturedAt.Equal(want) || snapshot.SourceTimestamp == nil || !snapshot.SourceTimestamp.Equal(want) ||
		snapshot.QuoteQuality.FetchedAt == nil || !snapshot.QuoteQuality.FetchedAt.Equal(want) ||
		snapshot.QuoteQuality.SourceTimestamp == nil || !snapshot.QuoteQuality.SourceTimestamp.Equal(want) ||
		!snapshot.QuoteQuality.AvailableAt(snapshot.CapturedAt) {
		t.Fatalf("round trip turned an equal observation into a future quote: %#v", snapshot)
	}
	var storedFetched, storedSource string
	if err := pool.QueryRow(ctx, `SELECT quote_quality->>'fetched_at',quote_quality->>'source_timestamp' FROM market_snapshots WHERE id=$1`, snapshot.ID).Scan(&storedFetched, &storedSource); err != nil {
		t.Fatal(err)
	}
	if storedFetched != want.Format(time.RFC3339Nano) || storedSource != want.Format(time.RFC3339Nano) {
		t.Fatalf("JSON quality was not stored in UTC microseconds: %q/%q", storedFetched, storedSource)
	}
	if fetched != input || source != input || proof.FetchedAt != &fetched || proof.SourceTimestamp != &source ||
		fetched.Nanosecond() != 123456789 || source.Location().String() != "caller-zone" {
		t.Fatal("persisting the quote changed caller-owned timestamp pointers")
	}
}

func TestPostgresQuoteAvailabilityAggregateMatchesSharedPolicy(t *testing.T) {
	_, pool := openIntegrationStore(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	// PostgreSQL timestamps preserve microseconds, matching persisted proofs.
	recent, boundary, old, future, zero := at.Add(-time.Minute), at.Add(-5*time.Minute), at.Add(-5*time.Minute-time.Microsecond), at.Add(time.Microsecond), time.Time{}
	cases := []marketquality.Quote{
		{}, {Status: "live"}, {Status: "live", FetchedAt: &recent}, {Status: "cached", FetchedAt: &recent},
		{Status: "stale_fallback", FetchedAt: &recent}, {Status: "unknown", FetchedAt: &recent},
		{Status: "live", FetchedAt: &boundary}, {Status: "live", FetchedAt: &old}, {Status: "live", FetchedAt: &future},
		{Status: "live", FetchedAt: &recent, SourceTimestamp: &old}, {Status: "live", FetchedAt: &recent, SourceTimestamp: &future},
		{Status: "cached", FetchedAt: &recent, SourceTimestamp: &boundary}, {Status: "cached", FetchedAt: &recent, SourceTimestamp: &zero},
	}
	for _, proof := range cases {
		var got bool
		if err := pool.QueryRow(ctx, `SELECT `+quoteAvailableSQL("$1::jsonb", "$2::timestamptz", "$3::double precision"), proof, at, 300).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if want := proof.AvailableWithin(at, 5*time.Minute); got != want {
			t.Fatalf("SQL/shared mismatch for %#v: %v vs %v", proof, got, want)
		}
	}
	for _, raw := range []string{
		`{"status":"live","fetched_at":"not-a-date"}`,
		`{"status":"live","fetched_at":"2026-99-99T00:00:00Z"}`,
		`{"status":"live","fetched_at":"now"}`,
		`{"status":"live","fetched_at":"2026-11-14T07:59:00Z","source_timestamp":"not-a-date"}`,
		`{"status":"live","fetched_at":"2026-11-14T07:59:00Z","source_timestamp":""}`,
	} {
		var got bool
		if err := pool.QueryRow(ctx, `SELECT `+quoteAvailableSQL("$1::jsonb", "$2::timestamptz", "$3::double precision"), json.RawMessage(raw), at, 300).Scan(&got); err != nil || got {
			t.Fatalf("bad proof must be unknown, not abort/pass: %s,got=%v,err=%v", raw, got, err)
		}
	}
}

func TestPostgresFrozenPoolMissingEvidenceDoesNotUseCurrentLiquidity(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	frozenAt := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	currentFetched := frozenAt.Add(time.Hour)
	currentQuality, _ := json.Marshal(marketquality.Quote{Status: "live", FetchedAt: &currentFetched})
	if _, err := pool.Exec(ctx, `UPDATE market_pools SET liquidity_usd=999999,metadata=jsonb_build_object('quote_quality',$2::jsonb) WHERE id=$1`, pools[0], currentQuality); err != nil {
		t.Fatal(err)
	}
	var projectID int64
	if err := pool.QueryRow(ctx, `INSERT INTO market_projects(debox_user_id,chain_key,chain_id,token_address,token_name,token_symbol,token_decimals,frozen_at,main_pool_id) VALUES('frozen-quality-owner','bsc',56,$1,'Quality','QLT',24,$2,$3) RETURNING id`, tradeFixtureToken, frozenAt, pools[0]).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO market_project_pools(market_project_id,market_pool_id,selected,is_primary,created_at) VALUES($1,$2,1,1,$3)`, projectID, pools[0], frozenAt.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	assertUnknown := func() {
		t.Helper()
		values, err := database.ListMarketProjectPools(ctx, projectID, "frozen-quality-owner")
		if err != nil || len(values) != 1 || values[0].LiquidityUSD != "" || values[0].QuoteQuality.Status != "" {
			t.Fatalf("frozen pool fell back to current liquidity/proof: %#v,%v", values, err)
		}
		views, err := database.ListMarketProjectPoolViews(ctx, projectID, "frozen-quality-owner")
		if err != nil || len(views) != 1 || views[0].LiquidityUSD != "" || views[0].QuoteQuality.Status != "" {
			t.Fatalf("frozen pool view fell back: %#v,%v", views, err)
		}
	}
	assertUnknown()
	if _, err := database.CreateMarketSnapshot(ctx, CreateMarketSnapshotParams{ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, MarketPoolID: pools[0], Source: "old-unknown", CapturedAt: frozenAt.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	assertUnknown()
}

func TestPostgresSummaryPreservesEventCountsButDoesNotTreatLegacyUSDAsKnown(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	start := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	if _, err := pool.Exec(ctx, `INSERT INTO market_projects(debox_user_id,chain_key,chain_id,token_address,token_name,token_symbol,token_decimals,main_pool_id) VALUES('summary-quality-owner','bsc',56,$1,'Quality','QLT',24,$2)`, tradeFixtureToken, pools[0]); err != nil {
		t.Fatal(err)
	}
	createSummaryMarketEvent(t, database, pools[0], "bsc", 56, tradeFixtureToken, "buy", "legacy-usd-buy", "100", start.Add(time.Hour))
	createSummaryMarketEvent(t, database, pools[0], "bsc", 56, tradeFixtureToken, "sell", "legacy-usd-sell", "100", start.Add(2*time.Hour))
	statistics, err := database.DailySummaryStatistics(ctx, "summary-quality-owner", start, end)
	if err != nil {
		t.Fatal(err)
	}
	if statistics.ValuationSchemaVersion != 2 || statistics.MarketEventCount != 2 || statistics.MarketBuyCount != 1 || statistics.MarketSellCount != 1 || statistics.MarketBuyValuedCount != 0 || statistics.MarketSellValuedCount != 0 || statistics.MarketBuyUSD != "" || statistics.MarketSellUSD != "" || statistics.MarketNetBuyUSD != "" {
		t.Fatalf("legacy amounts were treated as known: %#v", statistics)
	}
	recent, err := database.ListSummaryRecentMarketEvents(ctx, "summary-quality-owner", start, end, 5)
	if err != nil || len(recent) != 2 || recent[0].USDValue != nil || recent[1].USDValue != nil || recent[0].ValuationStatus != "unknown" {
		t.Fatalf("legacy recent USD: %#v,%v", recent, err)
	}
	summaries, err := database.ListDailyMarketProjectChainSummaries(ctx, "summary-quality-owner", start, end)
	if err != nil || len(summaries) != 1 || summaries[0].BuyCount != 1 || summaries[0].SellCount != 1 || summaries[0].TradeVolumeUSD != "" {
		t.Fatalf("legacy digest aggregate: %#v,%v", summaries, err)
	}
}

func TestPostgresTradeProjectionFreezesLatestLiquidityQualityWithoutBlockingTokenAmounts(t *testing.T) {
	database, _, pools := tradeFactsFixture(t)
	ctx := context.Background()
	params := tradeReceiptFixture(t, pools[:2])
	tradeCanonicalFixture(t, database, params)
	if _, err := database.PersistMarketTransaction(ctx, params); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	fetched := at.Add(-time.Minute)
	for _, entry := range []struct {
		pool    int64
		at      time.Time
		quality marketquality.Quote
		usd     string
	}{
		{pools[0], at.Add(-time.Minute), marketquality.Quote{Status: "live", FetchedAt: &fetched}, "10"},
		{pools[0], at, marketquality.Quote{Status: "stale_fallback", FetchedAt: &fetched}, "10"},
		{pools[1], at, marketquality.Quote{}, "20"},
	} {
		if _, err := database.CreateMarketSnapshot(ctx, CreateMarketSnapshotParams{ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, MarketPoolID: entry.pool, LiquidityUSD: &entry.usd, QuoteQuality: entry.quality, Source: "projection-quality", CapturedAt: entry.at}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := database.ListMarketTradeEventsForTarget(ctx, MarketRuleTarget{ChainID: 56, TokenAddress: tradeFixtureToken, TradePoolIDs: pools[:2]}, 0, true, 1)
	if err != nil || len(events) != 1 {
		t.Fatalf("projection: %#v,%v", events, err)
	}
	requireTradeAmount(t, events[0].TokenAmountRaw, "3")
	var metadata struct {
		Projection struct {
			Snapshots []struct {
				PoolID       int64               `json:"pool_id"`
				CapturedAt   time.Time           `json:"captured_at"`
				QuoteQuality marketquality.Quote `json:"quote_quality"`
			} `json:"liquidity_snapshots"`
		} `json:"trade_projection"`
	}
	if err := json.Unmarshal(events[0].Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if len(metadata.Projection.Snapshots) != 2 || metadata.Projection.Snapshots[0].QuoteQuality.Status != "stale_fallback" || !metadata.Projection.Snapshots[0].CapturedAt.Equal(at) || metadata.Projection.Snapshots[1].QuoteQuality.Status != "" {
		t.Fatalf("quality was washed fresh or skipped to older quote: %s", events[0].Metadata)
	}
}

func TestPostgresSummaryPartialUSDUsesOnlyProvenAmountsAndHandlesBadDates(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	start := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	if _, err := pool.Exec(ctx, `INSERT INTO market_projects(debox_user_id,chain_key,chain_id,token_address,token_name,token_symbol,token_decimals,main_pool_id) VALUES('summary-partial-owner','bsc',56,$1,'Quality','QLT',24,$2)`, tradeFixtureToken, pools[0]); err != nil {
		t.Fatal(err)
	}
	at := start.Add(time.Hour)
	createSummaryMarketEvent(t, database, pools[0], "bsc", 56, tradeFixtureToken, "buy", "proven-buy", "100", at, summaryValuationProofFixture(t, at))
	createSummaryMarketEvent(t, database, pools[0], "bsc", 56, tradeFixtureToken, "buy", "legacy-buy", "500", at)
	createSummaryMarketEvent(t, database, pools[0], "bsc", 56, tradeFixtureToken, "sell", "bad-proof-sell", "700", at, json.RawMessage(`{"valuation_schema_version":1,"valuation_complete":true,"valuation_at":"not-a-date","valuation_quote_quality":{"status":"live","fetched_at":"2026-11-14T00:59:00Z"}}`))
	statistics, err := database.DailySummaryStatistics(ctx, "summary-partial-owner", start, end)
	if err != nil {
		t.Fatal(err)
	}
	if statistics.MarketBuyCount != 2 || statistics.MarketSellCount != 1 || statistics.MarketBuyValuedCount != 1 || statistics.MarketSellValuedCount != 0 || statistics.MarketBuyUSD != "100" || statistics.MarketSellUSD != "" || statistics.MarketNetBuyUSD != "" {
		t.Fatalf("partial estimates were zero-filled/summed unknown: %#v", statistics)
	}
	summaries, err := database.ListDailyMarketProjectChainSummaries(ctx, "summary-partial-owner", start, end)
	if err != nil || len(summaries) != 1 || summaries[0].BuyValuedCount != 1 || summaries[0].TradeVolumeUSD != "100" || summaries[0].BuyCount != 2 || summaries[0].SellCount != 1 || summaries[0].SellUSD != "" {
		t.Fatalf("partial chain totals: %#v,%v", summaries, err)
	}
}
