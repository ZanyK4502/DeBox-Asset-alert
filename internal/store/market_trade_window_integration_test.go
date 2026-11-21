package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/jackc/pgx/v5"
)

func compactTradeProofFixture(t *testing.T, params *MarketTransactionParams) {
	t.Helper()
	params.OccurredAt = params.OccurredAt.UTC().Truncate(time.Microsecond)
	quality := marketquality.Quote{Status: marketquality.StatusLive, FetchedAt: &params.OccurredAt}
	proof, err := json.Marshal(map[string]any{"valuation_schema_version": 1, "valuation_complete": true, "valuation_at": params.OccurredAt, "valuation_quote_quality": quality})
	if err != nil {
		t.Fatal(err)
	}
	for i := range params.Events {
		for j := range params.Events[i].Legs {
			params.Events[i].Legs[j].Metadata = proof
		}
	}
	refreshTradeReceiptPayload(t, params)
}

func compactTradeRuleFixture() MarketRule {
	window := int32(15)
	return MarketRule{RuleType: plans.MarketConsecutiveLargeBuy, WindowMinutes: &window, ThresholdUnit: "usd", ThresholdValue: "4"}
}

func TestPostgresCompactTradeOldOnlyBoundaryNeedsNoDiscardedWindowCoverage(t *testing.T) {
	db, _, pools := tradeFactsFixture(t)
	ctx := context.Background()
	params := tradeReceiptFixture(t, pools[:2])
	tradeCanonicalFixture(t, db, params)
	if _, err := db.PersistMarketTransaction(ctx, params); err != nil {
		t.Fatal(err)
	}
	target := MarketRuleTarget{ChainID: 56, TokenAddress: tradeFixtureToken, TradePoolIDs: pools[:2]}
	cutoff := params.OccurredAt.Add(time.Second)
	batch, err := db.LoadMarketTradeEvaluationBatchAfter(ctx, compactTradeRuleFixture(), target, 0, cutoff, &cutoff)
	if err != nil || len(batch.Current) != 1 || len(batch.Candidates) != 0 {
		t.Fatalf("discarded old window demanded proof=%+v err=%v", batch, err)
	}
	if _, err := db.LoadMarketTradeEvaluationBatch(ctx, compactTradeRuleFixture(), target, 0, cutoff); !errors.Is(err, ErrMarketTradeWindowUnavailable) {
		t.Fatalf("nil boundary relaxed window evidence=%v", err)
	}
	stage := compactTradeRuleFixture()
	stage.DeliveryMode = "stage"
	stage.CycleMinutes = 1
	batch, err = db.LoadMarketTradeEvaluationBatch(ctx, stage, target, 0, params.OccurredAt.Add(2*time.Minute))
	if err != nil || len(batch.Current) != 1 || len(batch.Candidates) != 0 {
		t.Fatalf("existing stage-expired source still required discarded coverage=%+v err=%v", batch, err)
	}
}

func compactTradeCoverageFixture(t *testing.T, database *Store, params MarketTransactionParams, pools []int64) {
	t.Helper()
	after := params.OccurredAt.Add(-16 * time.Minute)
	hash := "0x" + strings.Repeat("c", 64)
	if _, err := database.UpsertMarketScannedBlock(context.Background(), UpsertMarketScannedBlockParams{ChainKey: "bsc", ChainID: 56, CursorKey: "market_logs", BlockNumber: 0,
		BlockHash: hash, ParentHash: "0x" + strings.Repeat("0", 64), BlockTimestamp: &after}); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordMarketScanCoverage(context.Background(), RecordMarketScanCoverageParams{ChainID: 56, CursorKey: "market_logs", ParserVersion: 1, ScopePoolIDs: pools,
		ScopeTokenAddresses: []string{tradeFixtureToken}, FromBlock: 1, ThroughBlock: params.BlockNumber, AfterAt: after, ThroughAt: params.OccurredAt, AfterHash: hash, ThroughHash: params.BlockHash}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresCompactTradeSummariesAndBatchRequireRealCoverage(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	params := tradeReceiptFixture(t, pools[:2])
	compactTradeProofFixture(t, &params)
	tradeCanonicalFixture(t, database, params)
	parents, err := database.PersistMarketTransaction(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range pools[:2] {
		if _, err := database.CreateMarketSnapshot(ctx, CreateMarketSnapshotParams{ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, MarketPoolID: id,
			Source: "local_test", CapturedAt: params.OccurredAt, QuoteQuality: marketquality.Quote{Status: marketquality.StatusLive, FetchedAt: &params.OccurredAt}, LiquidityUSD: pointerOf("100"), PriceUSD: pointerOf("1")}); err != nil {
			t.Fatal(err)
		}
	}
	target := MarketRuleTarget{ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, TradePoolIDs: pools[:2]}
	rule := compactTradeRuleFixture()
	if batch, err := database.LoadMarketTradeEvaluationBatch(ctx, rule, target, 0, params.OccurredAt); !errors.Is(err, ErrMarketTradeWindowUnavailable) || len(batch.Current) != 0 {
		t.Fatalf("cursor/receipt alone pretended full window: %#v %v", batch, err)
	}
	compactTradeCoverageFixture(t, database, params, pools[:2])
	batch, err := database.LoadMarketTradeEvaluationBatch(ctx, rule, target, 0, params.OccurredAt)
	if err != nil || len(batch.Current) != 1 || len(batch.Candidates) != 1 || batch.Candidates[0].Unknown || !batch.Candidates[0].Qualified || batch.Candidates[0].ParentID != parents[0].ID {
		t.Fatalf("complete compact batch=%#v err=%v", batch, err)
	}
	var groups int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM market_trade_pool_summaries").Scan(&groups); err != nil || groups != 2 {
		t.Fatalf("pool summaries=%d err=%v", groups, err)
	}
	// A missing group must not quietly become a smaller selected-pool amount.
	if _, err := pool.Exec(ctx, "DELETE FROM market_trade_pool_summaries WHERE pool_id=$1", pools[1]); err != nil {
		t.Fatal(err)
	}
	batch, err = database.LoadMarketTradeEvaluationBatch(ctx, rule, target, 0, params.OccurredAt)
	if err != nil || len(batch.Candidates) != 1 || !batch.Candidates[0].Unknown || batch.Candidates[0].Qualified {
		t.Fatalf("partial summaries became zero: %#v %v", batch, err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM market_trade_parent_summaries"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.PersistMarketTransaction(ctx, params); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM market_trade_parent_summaries").Scan(&groups); err != nil || groups != 0 {
		t.Fatalf("duplicate source backfilled old summaries: %d %v", groups, err)
	}
	batch, err = database.LoadMarketTradeEvaluationBatch(ctx, rule, target, 0, params.OccurredAt)
	if err != nil || !batch.Candidates[0].Unknown {
		t.Fatalf("legacy missing summary fell back to global parent: %#v %v", batch, err)
	}
}

func TestPostgresCompactTradePercentMatchesGoEightDigitRounding(t *testing.T) {
	database, _, pools := tradeFactsFixture(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	target := MarketRuleTarget{ChainID: 56, TokenAddress: tradeFixtureToken, TradePoolIDs: pools[:1]}
	rule := compactTradeRuleFixture()
	rule.ThresholdUnit = "percent"
	windows := []MarketTradeScanWindow{{From: at.Add(-15 * time.Minute), Through: at, ThroughBlock: 100}}
	for index, test := range []struct{ amount, threshold string }{
		{"0.4999999999999999999999999999", "0.5"},
		{"0.4999999949999999999999999999", "0.5"},
		{"0.499999995", "0.5"},
		{"0.0000000049999999999999999999", "0.000000001"},
		{"0.000000005", "0.000000001"},
		{"1.234567895", "1.234567899"},
		{"999999999999999999999999999.49999999499999999999", "999999999999999999999999999.5"},
	} {
		params := tradeReceiptFixture(t, pools[:2])
		params.TransactionHash = fmt.Sprintf("0x%064x", index+1)
		params.TransactionIndex = int32(index)
		params.OccurredAt = at
		params.Events[0].Legs[0].USDValue = pointerOf(test.amount)
		amount, _ := new(big.Rat).SetString(test.amount)
		price := new(big.Rat).Mul(amount, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(24), nil))).FloatString(30)
		params.Events[0].Legs[0].PriceUSD = &price
		compactTradeProofFixture(t, &params)
		tradeCanonicalFixture(t, database, params)
		parents, err := database.PersistMarketTransaction(ctx, params)
		if err != nil {
			t.Fatal(err)
		}
		rule.ThresholdValue = test.threshold
		ready, err := database.LatestMarketTradeReadyID(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		values, err := database.listCompactMarketTradeWindow(ctx, rule, target, ready, windows, pointerOf("100"), true, 100)
		if err != nil {
			t.Fatal(err)
		}
		rounded, _ := new(big.Rat).SetString(amount.FloatString(8))
		threshold, _ := new(big.Rat).SetString(test.threshold)
		want := rounded.Cmp(threshold) >= 0
		found := false
		for _, value := range values {
			if value.ParentID == parents[0].ID {
				found = true
				if value.Unknown || value.Qualified != want {
					t.Fatalf("round8 amount=%s threshold=%s got=%#v want=%v", test.amount, test.threshold, value, want)
				}
			}
		}
		if !found {
			t.Fatalf("candidate missing for amount=%s", test.amount)
		}
	}
}

func TestPostgresCompactTradeCanonicalQualityAndRowBudgets(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	params := tradeReceiptFixture(t, pools[:2])
	compactTradeProofFixture(t, &params)
	tradeCanonicalFixture(t, database, params)
	params.Confirmed = false
	if _, err := database.PersistMarketTransaction(ctx, params); err != nil {
		t.Fatal(err)
	}
	target := MarketRuleTarget{ChainID: 56, TokenAddress: tradeFixtureToken, TradePoolIDs: pools[:2]}
	rule := compactTradeRuleFixture()
	windows := []MarketTradeScanWindow{{From: params.OccurredAt.Add(-15 * time.Minute), Through: params.OccurredAt, ThroughBlock: 100}}
	if values, err := database.listCompactMarketTradeWindow(ctx, rule, target, 1000, windows, nil, false, 100); err != nil || len(values) != 0 {
		t.Fatalf("unconfirmed parent entered count: %v %v", values, err)
	}
	if _, err := database.MarkMarketBlockConfirmed(ctx, 56, 100); err != nil {
		t.Fatal(err)
	}
	ready, err := database.LatestMarketTradeReadyID(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	values, err := database.listCompactMarketTradeWindow(ctx, rule, target, ready, windows, nil, false, 100)
	if err != nil || len(values) != 1 || !values[0].Qualified {
		t.Fatalf("confirmed facts unavailable: %v %v", values, err)
	}
	second := tradeReceiptFixture(t, pools[:2])
	second.TransactionHash = "0x" + strings.Repeat("d", 64)
	second.TransactionIndex = 3
	second.OccurredAt = params.OccurredAt
	compactTradeProofFixture(t, &second)
	// A timestamp without a valid complete valuation proof remains unknown.
	second.Events[0].Legs[0].Metadata = json.RawMessage(`{"valuation_schema_version":1,"valuation_complete":false}`)
	if _, err := database.PersistMarketTransaction(ctx, second); err != nil {
		t.Fatal(err)
	}
	ready, err = database.LatestMarketTradeReadyID(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if values, err := database.listCompactMarketTradeWindow(ctx, rule, target, ready, windows, nil, false, 1); !errors.Is(err, ErrMarketTradeWindowUnavailable) || values != nil {
		t.Fatalf("row budget silently truncated: %v %v", values, err)
	}
	values, err = database.listCompactMarketTradeWindow(ctx, rule, target, ready, windows, nil, false, 100)
	if err != nil || len(values) != 2 || !values[1].Unknown || values[1].Qualified {
		t.Fatalf("unknown valuation counted as known: %v %v", values, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE market_trade_ready_events SET valid=FALSE WHERE id=$1", ready); err != nil {
		t.Fatal(err)
	}
	values, err = database.listCompactMarketTradeWindow(ctx, rule, target, ready, windows, nil, false, 100)
	if err != nil || len(values) != 1 {
		t.Fatalf("retired ready revision entered count: %v %v", values, err)
	}
	if _, err := database.MarkMarketBlockReorged(ctx, 56, 100, "0x"+strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	values, err = database.listCompactMarketTradeWindow(ctx, rule, target, ready, windows, nil, false, 100)
	if err != nil || len(values) != 0 {
		t.Fatalf("reorged parent entered count: %v %v", values, err)
	}
}

func TestPostgresCompactTradeReadsMoreThan500HistoryWithoutHistoricalJSON(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	oldAt := now.Add(-time.Minute)
	for index := 1; index <= 600; index++ {
		params := tradeReceiptFixture(t, pools[:2])
		params.BlockNumber = 99
		params.BlockHash = "0x" + strings.Repeat("f", 64)
		params.TransactionHash = fmt.Sprintf("0x%064x", index)
		params.TransactionIndex = int32(index - 1)
		params.OccurredAt = oldAt
		compactTradeProofFixture(t, &params)
		if index == 1 {
			tradeCanonicalFixture(t, database, params)
		}
		if _, err := database.PersistMarketTransaction(ctx, params); err != nil {
			t.Fatal(err)
		}
	}
	target := MarketRuleTarget{ChainID: 56, TokenAddress: tradeFixtureToken, TradePoolIDs: pools[:2]}
	cursor, err := database.LatestMarketTradeReadyID(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	current := tradeReceiptFixture(t, pools[:2])
	current.TransactionHash = fmt.Sprintf("0x%064x", 601)
	current.OccurredAt = now
	compactTradeProofFixture(t, &current)
	tradeCanonicalFixture(t, database, current)
	if _, err := database.PersistMarketTransaction(ctx, current); err != nil {
		t.Fatal(err)
	}
	compactTradeCoverageFixture(t, database, current, pools[:2])
	batch, err := database.LoadMarketTradeEvaluationBatch(ctx, compactTradeRuleFixture(), target, cursor, now)
	if err != nil || len(batch.Current) != 1 || len(batch.Candidates) != 601 {
		t.Fatalf("full history was truncated: current=%d compact=%d err=%v", len(batch.Current), len(batch.Candidates), err)
	}
	for _, candidate := range batch.Candidates {
		if candidate.Unknown || !candidate.Qualified {
			t.Fatalf("known qualifying history lost: %#v", candidate)
		}
	}
	if _, err := pool.Exec(ctx, "ANALYZE market_trade_ready_events"); err != nil {
		t.Fatal(err)
	}
	plan := func() string {
		rows, err := pool.Query(ctx, "EXPLAIN (COSTS OFF) SELECT id FROM market_trade_ready_events WHERE parent_event_id=$1 AND valid", batch.Current[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var lines []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			lines = append(lines, line)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return strings.Join(lines, "\n")
	}
	before := plan()
	if _, err := pool.Exec(ctx, "CREATE INDEX IF NOT EXISTS idx_market_trade_ready_parent_valid ON market_trade_ready_events(parent_event_id,id) WHERE valid"); err != nil {
		t.Fatal(err)
	}
	after := plan()
	t.Logf("ready parent lookup before:\n%s\nafter:\n%s", before, after)
	if !strings.Contains(after, "idx_market_trade_ready_parent_valid") {
		t.Fatalf("bounded parent lookup lacked its index path: %s", after)
	}
}

type compactSnapshotDB struct {
	DBTX
	afterDetails func()
}

func (db compactSnapshotDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := db.DBTX.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &compactSnapshotTx{Tx: tx, afterDetails: db.afterDetails}, nil
}

type compactSnapshotTx struct {
	pgx.Tx
	afterDetails func()
	called       bool
}

func (tx *compactSnapshotTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	rows, err := tx.Tx.Query(ctx, query, args...)
	if err == nil && !tx.called && strings.Contains(query, "WITH RECURSIVE candidates AS MATERIALIZED") {
		tx.called = true
		tx.afterDetails()
	}
	return rows, err
}

func TestPostgresCompactTradeBatchUsesOneRepeatableReadFactSnapshot(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	params := tradeReceiptFixture(t, pools[:2])
	compactTradeProofFixture(t, &params)
	tradeCanonicalFixture(t, database, params)
	if _, err := database.PersistMarketTransaction(ctx, params); err != nil {
		t.Fatal(err)
	}
	compactTradeCoverageFixture(t, database, params, pools[:2])
	rule := compactTradeRuleFixture()
	rule.ThresholdValue = "100"
	var changeErr error
	wrapped := newWithDB(compactSnapshotDB{DBTX: database.db, afterDetails: func() {
		_, changeErr = pool.Exec(ctx, "UPDATE market_trade_pool_summaries SET usd_value=200")
	}})
	batch, err := wrapped.LoadMarketTradeEvaluationBatch(ctx, rule, MarketRuleTarget{ChainID: 56, TokenAddress: tradeFixtureToken, TradePoolIDs: pools[:2]}, 0, params.OccurredAt)
	if changeErr != nil {
		t.Fatal(changeErr)
	}
	if err != nil || len(batch.Current) != 1 || len(batch.Candidates) != 1 || batch.Candidates[0].Qualified || batch.Candidates[0].Unknown {
		t.Fatalf("facts changed between details and compact statistics: %#v %v", batch, err)
	}
	var changed string
	if err := pool.QueryRow(ctx, "SELECT SUM(usd_value)::text FROM market_trade_pool_summaries").Scan(&changed); err != nil || changed != "400" {
		t.Fatalf("fixture concurrent write did not commit: %s %v", changed, err)
	}
}

func TestPostgresCompactTradeRescanCannotClaimMissingDirectionAsZero(t *testing.T) {
	database, _, pools := tradeFactsFixture(t)
	ctx := context.Background()
	old := tradeReceiptFixture(t, pools[:2])
	old.ScopePoolIDs = append([]int64(nil), pools[:1]...)
	old.Events[0].Event.EventType = "sell"
	old.Events[0].Event.TokenAmountRaw = pointerOf("1")
	old.Events[0].Legs = old.Events[0].Legs[:1]
	compactTradeProofFixture(t, &old)
	tradeCanonicalFixture(t, database, old)
	original, err := database.PersistMarketTransaction(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	rescan := tradeReceiptFixture(t, pools[:2])
	rescan.OccurredAt = old.OccurredAt
	extra := rescan.Events[0]
	extra.Event.EventType, extra.Event.EventKey, extra.Event.TokenAmountRaw = "buy", "new-scope-buy", pointerOf("2")
	extra.Legs = extra.Legs[1:]
	rescan.Events = []MarketTransactionEvent{old.Events[0], extra}
	compactTradeProofFixture(t, &rescan)
	stillOld, err := database.PersistMarketTransaction(ctx, rescan)
	if err != nil || len(stillOld) != 1 || stillOld[0].ID != original[0].ID || stillOld[0].EventType != "sell" {
		t.Fatalf("rescan rewrote a published receipt/parent: %#v %v", stillOld, err)
	}
	// The new parser declaration alone is not proof that its newly discovered
	// buy direction was stored; the immutable receipt still captured pool A.
	compactTradeCoverageFixture(t, database, old, pools[:2])
	target := MarketRuleTarget{ChainID: 56, TokenAddress: tradeFixtureToken, TradePoolIDs: pools[:2]}
	windows := []MarketTradeScanWindow{{From: old.OccurredAt.Add(-15 * time.Minute), Through: old.OccurredAt, ThroughBlock: 100}}
	if covered, err := database.MarketTradeWindowScanCoverageComplete(ctx, target, windows); err != nil || covered {
		t.Fatalf("declared new scope hid a missing buy parent: covered=%v err=%v", covered, err)
	}
	fresh := tradeReceiptFixture(t, pools[:2])
	fresh.TransactionHash = "0x" + strings.Repeat("7", 64)
	fresh.BlockHash = "0x" + strings.Repeat("8", 64)
	fresh.BlockNumber = 101
	fresh.OccurredAt = old.OccurredAt.Add(time.Minute)
	compactTradeProofFixture(t, &fresh)
	if _, err := database.UpsertMarketScannedBlock(ctx, UpsertMarketScannedBlockParams{ChainKey: "bsc", ChainID: 56, CursorKey: "market_logs", BlockNumber: 101, BlockHash: fresh.BlockHash, ParentHash: old.BlockHash, BlockTimestamp: &fresh.OccurredAt}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.PersistMarketTransaction(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordMarketScanCoverage(ctx, RecordMarketScanCoverageParams{ChainID: 56, CursorKey: "market_logs", ParserVersion: 1, ScopePoolIDs: pools[:2], ScopeTokenAddresses: []string{tradeFixtureToken},
		FromBlock: 1, ThroughBlock: 101, AfterAt: old.OccurredAt.Add(-16 * time.Minute), ThroughAt: fresh.OccurredAt, AfterHash: "0x" + strings.Repeat("c", 64), ThroughHash: fresh.BlockHash}); err != nil {
		t.Fatal(err)
	}
	if batch, err := database.LoadMarketTradeEvaluationBatch(ctx, compactTradeRuleFixture(), target, 0, fresh.OccurredAt); !errors.Is(err, ErrMarketTradeWindowUnavailable) || len(batch.Current) != 0 {
		t.Fatalf("missing old buy side was silently counted as zero: %#v %v", batch, err)
	}
	// A past incomplete receipt must not poison a later complete time window.
	windows = []MarketTradeScanWindow{{From: old.OccurredAt.Add(time.Microsecond), Through: fresh.OccurredAt, ThroughBlock: 101}}
	if covered, err := database.MarketTradeWindowScanCoverageComplete(ctx, target, windows); err != nil || !covered {
		t.Fatalf("expired old-scope gap permanently poisoned later windows: %v %v", covered, err)
	}
}
