package store

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"
)

func TestPostgresMarketRecommendationSamplesHaveBoundedQueryDeadline(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	project := recommendationProjectFixture(t, database, pools[:1])
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(ctx, `LOCK TABLE market_events IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	started := time.Now()
	samples, err := database.ListMarketRecommendationSamples(ctx, project.ID, "recommend-owner", MarketSnapshot{ChainID: 56, TokenAddress: tradeFixtureToken}, now.Add(-24*time.Hour), now, 100)
	if !errors.Is(err, ErrMarketRecommendationUnavailable) || !errors.Is(err, context.DeadlineExceeded) || len(samples) != 0 {
		t.Fatalf("query budget=%+v %v", samples, err)
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("query exceeded bounded cancellation latency: %s", elapsed)
	}
}

func recommendationProjectFixture(t *testing.T, database *Store, pools []int64) MarketProject {
	t.Helper()
	ctx := context.Background()
	project, err := createMarketProject(ctx, database.db, CreateMarketProjectParams{DeBoxUserID: "recommend-owner", ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, TokenDecimals: 24, FourMemeStatus: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range pools {
		if _, err := database.db.Exec(ctx, `INSERT INTO market_project_pools(market_project_id,market_pool_id,selected) VALUES($1,$2,1)`, project.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	return project
}

func recommendationLegacyFixture(t *testing.T, database *Store, poolID int64, key string, at time.Time, valid bool) MarketEvent {
	t.Helper()
	params := CreateMarketEventParams{ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, MarketPoolID: &poolID, EventType: "buy", EventKey: key, USDValue: pointerOf("9"), PriceUSD: pointerOf("1"), Confirmed: true, Source: "fixture", OccurredAt: at}
	if valid {
		params.Metadata = summaryValuationProofFixture(t, at)
	}
	event, created, err := database.CreateMarketEvent(context.Background(), params)
	if err != nil || !created {
		t.Fatalf("legacy fixture=%v,%v", created, err)
	}
	return event
}

func TestPostgresMarketRecommendationSamplesFilterBeforeLimitAndSortOccurrence(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	project := recommendationProjectFixture(t, database, pools[:1])
	now := time.Now().UTC().Truncate(time.Microsecond)
	older := recommendationLegacyFixture(t, database, pools[0], "eligible-older", now.Add(-2*time.Hour), true)
	newer := recommendationLegacyFixture(t, database, pools[0], "eligible-newer", now.Add(-time.Hour), true)
	for i := 0; i < 105; i++ {
		recommendationLegacyFixture(t, database, pools[0], fmt.Sprintf("invalid-new-%d", i), now.Add(-time.Duration(i)*time.Second), false)
	}
	recommendationLegacyFixture(t, database, pools[0], "late-old-high-id", now.Add(-25*time.Hour), true)
	recommendationLegacyFixture(t, database, pools[0], "future", now.Add(time.Second), true)
	recommendationLegacyFixture(t, database, pools[1], "unselected", now.Add(-time.Second), true)
	invalid := recommendationLegacyFixture(t, database, pools[0], "unconfirmed", now.Add(-time.Second), true)
	if _, err := pool.Exec(ctx, `UPDATE market_events SET confirmed=0 WHERE id=$1`, invalid.ID); err != nil {
		t.Fatal(err)
	}
	invalid = recommendationLegacyFixture(t, database, pools[0], "reorged", now.Add(-time.Second), true)
	if _, err := pool.Exec(ctx, `UPDATE market_events SET reorged=1 WHERE id=$1`, invalid.ID); err != nil {
		t.Fatal(err)
	}
	got, err := database.ListMarketRecommendationSamples(ctx, project.ID, "recommend-owner", MarketSnapshot{ChainID: 56, TokenAddress: tradeFixtureToken}, now.Add(-24*time.Hour), now, 2)
	if err != nil || len(got) != 2 || got[0].MarketEventID != newer.ID || got[1].MarketEventID != older.ID || got[0].ReceiptID != nil || len(got[0].PoolIDs) != 1 || got[0].PoolIDs[0] != pools[0] {
		t.Fatalf("eligible occurred ordering=%+v,%v", got, err)
	}
	wrongOwner, err := database.ListMarketRecommendationSamples(ctx, project.ID, "other", MarketSnapshot{ChainID: 56, TokenAddress: tradeFixtureToken}, now.Add(-24*time.Hour), now, 100)
	if err != nil || len(wrongOwner) != 0 {
		t.Fatalf("ownership scope=%+v,%v", wrongOwner, err)
	}
	wrongChain, err := database.ListMarketRecommendationSamples(ctx, project.ID, "recommend-owner", MarketSnapshot{ChainID: 1, TokenAddress: tradeFixtureToken}, now.Add(-24*time.Hour), now, 100)
	if err != nil || len(wrongChain) != 0 {
		t.Fatalf("chain scope=%+v,%v", wrongChain, err)
	}
}

func TestPostgresMarketRecommendationSamplesUseOnlySelectedCompleteFactSums(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	project := recommendationProjectFixture(t, database, pools[:1])
	params := tradeReceiptFixture(t, pools[:2])
	params.OccurredAt = time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	for i := range params.Events[0].Legs {
		params.Events[0].Legs[i].Metadata = summaryValuationProofFixture(t, params.OccurredAt)
	}
	tradeCanonicalFixture(t, database, params)
	events, err := database.PersistMarketTransaction(ctx, params)
	if err != nil || len(events) != 1 {
		t.Fatalf("persist=%+v,%v", events, err)
	}
	now := time.Now().UTC()
	read := func() []MarketRecommendationSample {
		t.Helper()
		samples, err := database.ListMarketRecommendationSamples(ctx, project.ID, "recommend-owner", MarketSnapshot{ChainID: 56, TokenAddress: tradeFixtureToken}, now.Add(-24*time.Hour), now, 100)
		if err != nil {
			t.Fatal(err)
		}
		return samples
	}
	got := read()
	if len(got) != 1 || got[0].ReceiptID == nil || got[0].TradeReadyID == nil || len(got[0].PoolIDs) != 1 || got[0].PoolIDs[0] != pools[0] {
		t.Fatalf("fact sources=%+v", got)
	}
	amount, _ := new(big.Rat).SetString(got[0].USDValue)
	if amount == nil || amount.Cmp(big.NewRat(2, 1)) != 0 {
		t.Fatalf("global parent amount leaked=%+v", got)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO market_project_pools(market_project_id,market_pool_id,selected) VALUES($1,$2,1)`, project.ID, pools[1]); err != nil {
		t.Fatal(err)
	}
	got = read()
	if len(got) != 1 {
		t.Fatalf("two selected pools=%+v", got)
	}
	amount, _ = new(big.Rat).SetString(got[0].USDValue)
	if amount.Cmp(big.NewRat(6, 1)) != 0 {
		t.Fatalf("selected sum=%+v", got)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM market_trade_pool_summaries WHERE pool_id=$1`, pools[1]); err != nil {
		t.Fatal(err)
	}
	if got = read(); len(got) != 0 {
		t.Fatalf("partial summary fell back to parent=%+v", got)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM market_trade_parent_summaries`); err != nil {
		t.Fatal(err)
	}
	if got = read(); len(got) != 0 {
		t.Fatalf("old missing summaries fell back to parent=%+v", got)
	}
}

func TestPostgresMarketRecommendationSamplesRequireOriginalQualityAndCanonicalReceipt(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	project := recommendationProjectFixture(t, database, pools[:1])
	params := tradeReceiptFixture(t, pools[:2])
	params.OccurredAt = time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	tradeCanonicalFixture(t, database, params)
	if _, err := database.PersistMarketTransaction(ctx, params); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ref := MarketSnapshot{ChainID: 56, TokenAddress: tradeFixtureToken}
	got, err := database.ListMarketRecommendationSamples(ctx, project.ID, "recommend-owner", ref, now.Add(-24*time.Hour), now, 100)
	if err != nil || len(got) != 0 {
		t.Fatalf("unproven USD sampled=%+v,%v", got, err)
	}
	// Controlled fixture evidence marks only the selected summary valid; canonical
	// and captured-scope checks still independently exclude invalid candidates.
	if _, err := pool.Exec(ctx, `UPDATE market_trade_pool_summaries SET usd_valuation_complete=true WHERE pool_id=$1`, pools[0]); err != nil {
		t.Fatal(err)
	}
	got, err = database.ListMarketRecommendationSamples(ctx, project.ID, "recommend-owner", ref, now.Add(-24*time.Hour), now, 100)
	if err != nil || len(got) != 1 {
		t.Fatalf("complete selected proof should be eligible=%+v,%v", got, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE market_scanned_blocks SET canonical=0`); err != nil {
		t.Fatal(err)
	}
	got, err = database.ListMarketRecommendationSamples(ctx, project.ID, "recommend-owner", ref, now.Add(-24*time.Hour), now, 100)
	if err != nil || len(got) != 0 {
		t.Fatalf("noncanonical receipt sampled=%+v,%v", got, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE market_scanned_blocks SET canonical=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO market_project_pools(market_project_id,market_pool_id,selected) VALUES($1,$2,1)`, project.ID, pools[2]); err != nil {
		t.Fatal(err)
	}
	got, err = database.ListMarketRecommendationSamples(ctx, project.ID, "recommend-owner", ref, now.Add(-24*time.Hour), now, 100)
	if err != nil || len(got) != 0 {
		t.Fatalf("later selected uncaptured pool treated as complete=%+v,%v", got, err)
	}
}
