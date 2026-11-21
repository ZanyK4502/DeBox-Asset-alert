package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const tradeFixtureToken = "0x1111111111111111111111111111111111111111"
const tradeFixtureQuote = "0x2222222222222222222222222222222222222222"
const tradeFixtureOtherQuote = "0x3333333333333333333333333333333333333333"

func tradeFactsFixture(t *testing.T) (*Store, *pgxpool.Pool, []int64) {
	t.Helper()
	database, pool := openIntegrationStore(t)
	ids := make([]int64, 3)
	for i := range ids {
		quote := tradeFixtureQuote
		if i == 2 {
			quote = tradeFixtureOtherQuote
		}
		p, err := database.UpsertMarketPool(context.Background(), UpsertMarketPoolParams{
			ChainKey: "bsc", ChainID: 56, Protocol: "pancakeswap", ProtocolVersion: "v2", PoolKey: fmt.Sprintf("facts-pool-%d", i),
			Token0Address: tradeFixtureToken, Token1Address: quote, Token0Decimals: 24, Token1Decimals: 6,
			Token0DecimalsVerified: true, Token1DecimalsVerified: true, SupportsEventParsing: true, ParserAdapter: "pancake_v2",
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = p.ID
	}
	return database, pool, ids
}

func tradeReceiptFixture(t *testing.T, pools []int64) MarketTransactionParams {
	t.Helper()
	txHash := "0x" + strings.Repeat("a", 64)
	blockHash := "0x" + strings.Repeat("b", 64)
	blockNumber := int64(100)
	transactionIndex := int32(2)
	raw := "3"
	parent := CreateMarketEventParams{ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, EventType: "buy", EventKey: "parsed-parent",
		TransactionHash: &txHash, BlockHash: &blockHash, BlockNumber: &blockNumber, TransactionIndex: &transactionIndex,
		TokenAmountRaw: &raw, Source: "nodit_webhook", Metadata: []byte(`{"adapter":"pancake_v2"}`)}
	params := MarketTransactionParams{ChainKey: "bsc", ChainID: 56, TransactionHash: txHash, BlockNumber: blockNumber, BlockHash: blockHash, TransactionIndex: transactionIndex,
		OccurredAt: time.Now().UTC(), Confirmed: true, ParserVersion: 1, ScopePoolIDs: append([]int64(nil), pools...), ScopeTokenAddresses: []string{tradeFixtureToken},
		Events: []MarketTransactionEvent{{Event: parent, Legs: []MarketTradeLegParams{
			{PoolID: pools[0], LogIndex: 1, QuoteAddress: tradeFixtureQuote, TokenAmountRaw: "1", QuoteAmountRaw: "1000000", TokenAmount: pointerOf("0.000000000000000000000001"), QuoteAmount: pointerOf("1"), USDValue: pointerOf("2"), PriceUSD: pointerOf("2000000000000000000000000"), Metadata: []byte(`{"snapshot_id":11}`)},
			{PoolID: pools[1], LogIndex: 2, QuoteAddress: tradeFixtureQuote, TokenAmountRaw: "2", QuoteAmountRaw: "2000000", TokenAmount: pointerOf("0.000000000000000000000002"), QuoteAmount: pointerOf("2"), USDValue: pointerOf("4"), PriceUSD: pointerOf("2000000000000000000000000"), Metadata: []byte(`{"snapshot_id":12}`)},
		}}},
	}
	refreshTradeReceiptPayload(t, &params)
	return params
}

func refreshTradeReceiptPayload(t *testing.T, params *MarketTransactionParams) {
	t.Helper()
	params.ReceiptPayload = []byte(fmt.Sprintf(`{"transactionHash":%q,"blockHash":%q,"blockNumber":"0x%x","transactionIndex":"0x%x","logs":[{"logIndex":"0x1"},{"logIndex":"0x2"}],"status":"0x1"}`,
		params.TransactionHash, params.BlockHash, params.BlockNumber, params.TransactionIndex))
	params.Events = append([]MarketTransactionEvent(nil), params.Events...)
	for i := range params.Events {
		params.Events[i].Event.TransactionHash = &params.TransactionHash
		params.Events[i].Event.BlockHash = &params.BlockHash
		params.Events[i].Event.BlockNumber = &params.BlockNumber
		params.Events[i].Event.TransactionIndex = &params.TransactionIndex
	}
}

func tradeCanonicalFixture(t *testing.T, database *Store, params MarketTransactionParams) {
	t.Helper()
	_, err := database.UpsertMarketScannedBlock(context.Background(), UpsertMarketScannedBlockParams{
		ChainKey: params.ChainKey, ChainID: params.ChainID, CursorKey: "market_logs", BlockNumber: params.BlockNumber, BlockHash: params.BlockHash,
		ParentHash: "0x" + strings.Repeat("0", 64), BlockTimestamp: &params.OccurredAt,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func requireTradeAmount(t *testing.T, value *string, want string) {
	t.Helper()
	if value == nil {
		t.Fatalf("amount is nil, want %s", want)
	}
	actual, ok := new(big.Rat).SetString(*value)
	expected, _ := new(big.Rat).SetString(want)
	if !ok || actual.Cmp(expected) != 0 {
		t.Fatalf("amount %q, want %q", *value, want)
	}
}

func TestPostgresMarketTradeFactsAtomicProjectionAndDuplicateSources(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	params := tradeReceiptFixture(t, pools[:2])
	tradeCanonicalFixture(t, database, params)
	for i, id := range pools[:2] {
		_, err := database.CreateMarketSnapshot(ctx, CreateMarketSnapshotParams{
			ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, MarketPoolID: id, PriceUSD: pointerOf("1"), LiquidityUSD: pointerOf(fmt.Sprint((i + 1) * 100)), Source: "fixture",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	first, err := database.PersistMarketTransaction(ctx, params)
	if err != nil || len(first) != 1 || first[0].TradeReadyID <= 0 {
		t.Fatalf("Persist()=%+v,%v", first, err)
	}
	var before string
	if err := pool.QueryRow(ctx, `SELECT ctid::text FROM market_transaction_receipts`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errorsCh := make(chan error, 6)
	for i := range 6 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			duplicate := params
			duplicate.Events = append([]MarketTransactionEvent(nil), params.Events...)
			duplicate.Events[0].Event.Source = fmt.Sprintf("duplicate-%d", i)
			_, err := database.PersistMarketTransaction(ctx, duplicate)
			errorsCh <- err
		}()
	}
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	var receipts, parents, legs, ready int
	var after string
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM market_transaction_receipts),(SELECT count(*) FROM market_trade_parents),(SELECT count(*) FROM market_trade_legs),(SELECT count(*) FROM market_trade_ready_events),(SELECT ctid::text FROM market_transaction_receipts)`).Scan(&receipts, &parents, &legs, &ready, &after); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 || parents != 1 || legs != 2 || ready != 1 || before != after {
		t.Fatalf("duplicate writes: receipts=%d parents=%d legs=%d ready=%d ctid=%s/%s", receipts, parents, legs, ready, before, after)
	}
	target := MarketRuleTarget{ChainID: 56, TokenAddress: tradeFixtureToken, TradePoolIDs: pools[:2]}
	projected, err := database.ListMarketTradeEventsForTarget(ctx, target, 0, true, 1)
	if err != nil || len(projected) != 1 {
		t.Fatalf("projected=%+v,%v", projected, err)
	}
	requireTradeAmount(t, projected[0].TokenAmountRaw, "3")
	requireTradeAmount(t, projected[0].TokenAmount, "0.000000000000000000000003")
	requireTradeAmount(t, projected[0].USDValue, "6")
	var metadata struct {
		Projection struct {
			Liquidity *string `json:"liquidity_usd"`
			Complete  bool    `json:"liquidity_complete"`
			LegIDs    []int64 `json:"source_leg_ids"`
			Snapshots []int64 `json:"snapshot_ids"`
		} `json:"trade_projection"`
	}
	if err := json.Unmarshal(projected[0].Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	requireTradeAmount(t, metadata.Projection.Liquidity, "300")
	if !metadata.Projection.Complete || len(metadata.Projection.LegIDs) != 2 || len(metadata.Projection.Snapshots) != 2 {
		t.Fatalf("projection metadata=%s", projected[0].Metadata)
	}
	target.TradePoolIDs = pools[1:2]
	projected, err = database.ListMarketTradeEventsForTarget(ctx, target, 0, true, 1)
	if err != nil || len(projected) != 1 {
		t.Fatalf("one-pool projection=%+v,%v", projected, err)
	}
	requireTradeAmount(t, projected[0].TokenAmountRaw, "2")
	requireTradeAmount(t, projected[0].USDValue, "4")
	if projected[0].ID != first[0].ID || projected[0].TradeReadyID != first[0].TradeReadyID {
		t.Fatal("projection replaced stable parent identity")
	}
}

func TestPostgresMarketTradeFactsRollbackAndIncompleteReceipt(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	params := tradeReceiptFixture(t, pools[:2])
	params.ReceiptPayload = []byte(fmt.Sprintf(`{"transactionHash":%q,"blockHash":%q,"blockNumber":"0x64","transactionIndex":"0x2"}`, params.TransactionHash, params.BlockHash))
	if _, err := database.PersistMarketTransaction(ctx, params); !errors.Is(err, ErrInvalidMarketEvent) {
		t.Fatalf("missing logs error=%v", err)
	}
	refreshTradeReceiptPayload(t, &params)
	if _, err := pool.Exec(ctx, `ALTER TABLE market_trade_legs ADD CONSTRAINT fixture_reject_second_log CHECK(log_index<>2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.PersistMarketTransaction(ctx, params); err == nil {
		t.Fatal("expected late fact insert failure")
	}
	for _, table := range []string{"market_transaction_receipts", "market_trade_scopes", "market_events", "market_trade_parents", "market_transaction_events", "market_trade_legs", "market_trade_ready_events"} {
		var rows int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("partial %s rows=%d error=%v", table, rows, err)
		}
	}
}

func TestPostgresMarketTradeFactsCoverageMissingValuesAndMixedQuote(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	params := tradeReceiptFixture(t, pools[:2])
	params.Events[0].Legs[1].USDValue = nil
	tradeCanonicalFixture(t, database, params)
	if _, err := database.PersistMarketTransaction(ctx, params); err != nil {
		t.Fatal(err)
	}
	target := MarketRuleTarget{ChainID: 56, TokenAddress: tradeFixtureToken, TradePoolIDs: pools[:2]}
	items, err := database.ListMarketTradeEventsForTarget(ctx, target, 0, true, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("read=%+v,%v", items, err)
	}
	if items[0].USDValue != nil || items[0].PriceUSD != nil {
		t.Fatal("partial USD was summed")
	}
	var metadata map[string]json.RawMessage
	_ = json.Unmarshal(items[0].Metadata, &metadata)
	var projection map[string]json.RawMessage
	_ = json.Unmarshal(metadata["trade_projection"], &projection)
	if string(projection["liquidity_complete"]) != "false" || string(projection["liquidity_usd"]) != "null" {
		t.Fatal("missing selected-pool snapshot became partial liquidity")
	}
	// The old immutable scope cannot silently become complete when a new pool is selected.
	target.TradePoolIDs = pools
	items, err = database.ListMarketTradeEventsForTarget(ctx, target, 0, true, 10)
	if err != nil || len(items) != 1 || items[0].TokenAmountRaw != nil || items[0].TokenAmount != nil || items[0].USDValue != nil {
		t.Fatalf("coverage fallback=%+v,%v", items, err)
	}
	_ = json.Unmarshal(items[0].Metadata, &metadata)
	if string(metadata["trade_projection_unavailable"]) != `"coverage_incomplete"` {
		t.Fatalf("missing coverage audit: %s", items[0].Metadata)
	}
	duplicate := params
	duplicate.ScopePoolIDs = pools
	if _, err := database.PersistMarketTransaction(ctx, duplicate); err != nil {
		t.Fatal(err)
	}
	var scopes, ready int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM market_trade_scopes),(SELECT count(*) FROM market_trade_ready_events)`).Scan(&scopes, &ready); err != nil || scopes != 1 || ready != 1 {
		t.Fatalf("scope replay=%d/%d,%v", scopes, ready, err)
	}
	// A second transaction with two different quote tokens retains token totals,
	// but has no meaningful combined quote amount.
	mixed := tradeReceiptFixture(t, []int64{pools[0], pools[2]})
	mixed.TransactionHash = "0x" + strings.Repeat("c", 64)
	mixed.Events[0].Legs[1].QuoteAddress = tradeFixtureOtherQuote
	refreshTradeReceiptPayload(t, &mixed)
	if _, err := database.PersistMarketTransaction(ctx, mixed); err != nil {
		t.Fatal(err)
	}
	items, err = database.ListMarketTradeEventsForTarget(ctx, MarketRuleTarget{ChainID: 56, TokenAddress: tradeFixtureToken, TradePoolIDs: []int64{pools[0], pools[2]}}, 0, false, 1)
	if err != nil || len(items) != 1 || items[0].QuoteAmount != nil || items[0].QuoteAmountRaw != nil {
		t.Fatalf("mixed quote=%+v,%v", items, err)
	}
	requireTradeAmount(t, items[0].TokenAmountRaw, "3")
}

func TestPostgresMarketTradeFactsRequiresCanonicalReceiptBeforeConfirmation(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	params := tradeReceiptFixture(t, pools[:2])
	params.Confirmed = false
	items, err := database.PersistMarketTransaction(ctx, params)
	if err != nil || len(items) != 1 || items[0].Confirmed != 0 || items[0].TradeReadyID != 0 {
		t.Fatalf("unconfirmed receipt=%+v,%v", items, err)
	}
	legacyBlock := int64(1)
	legacyHash := "0x" + strings.Repeat("d", 64)
	if _, _, err := database.CreateMarketEvent(ctx, CreateMarketEventParams{ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, EventType: "buy", EventKey: "unverified-old", BlockNumber: &legacyBlock, BlockHash: &legacyHash, Source: "old-webhook"}); err != nil {
		t.Fatal(err)
	}
	if count, err := database.MarkMarketBlockConfirmed(ctx, 56, 1000000); err != nil || count != 0 {
		t.Fatalf("cursor-only confirm=%d,%v", count, err)
	}
	tradeCanonicalFixture(t, database, params)
	if count, err := database.MarkMarketBlockConfirmed(ctx, 56, 1000000); err != nil || count != 1 {
		t.Fatalf("verified confirm=%d,%v", count, err)
	}
	var oldConfirmed int
	if err := pool.QueryRow(ctx, `SELECT confirmed FROM market_events WHERE event_key='unverified-old'`).Scan(&oldConfirmed); err != nil || oldConfirmed != 0 {
		t.Fatalf("old event was bulk confirmed: %d,%v", oldConfirmed, err)
	}
	items, err = database.ListMarketTradeEventsForTarget(ctx, MarketRuleTarget{ChainID: 56, TokenAddress: tradeFixtureToken, TradePoolIDs: pools[:2]}, 0, true, 10)
	if err != nil || len(items) != 1 || items[0].TradeReadyID <= 0 || items[0].Confirmed != 1 {
		t.Fatalf("confirmed ready=%+v,%v", items, err)
	}
}

func TestPostgresMarketTradeFactsReorgReinclusionKeepsParentAndPublishesNewRevision(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	params := tradeReceiptFixture(t, pools[:2])
	tradeCanonicalFixture(t, database, params)
	first, err := database.PersistMarketTransaction(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	newFork := params
	newFork.BlockHash = "0x" + strings.Repeat("e", 64)
	refreshTradeReceiptPayload(t, &newFork)
	tradeCanonicalFixture(t, database, newFork)
	if _, err := database.MarkMarketBlockReorged(ctx, 56, 100, newFork.BlockHash); err != nil {
		t.Fatal(err)
	}
	if _, err := database.PersistMarketTransaction(ctx, params); !errors.Is(err, ErrInvalidMarketEvent) {
		t.Fatalf("stale fork reentered: %v", err)
	}
	second, err := database.PersistMarketTransaction(ctx, newFork)
	if err != nil || len(second) != 1 || second[0].ID != first[0].ID || second[0].TradeReadyID <= first[0].TradeReadyID {
		t.Fatalf("new inclusion=%+v,%v", second, err)
	}
	tradeCanonicalFixture(t, database, params)
	if _, err := database.MarkMarketBlockReorged(ctx, 56, 100, params.BlockHash); err != nil {
		t.Fatal(err)
	}
	third, err := database.PersistMarketTransaction(ctx, params)
	if err != nil || len(third) != 1 || third[0].ID != first[0].ID || third[0].TradeReadyID <= second[0].TradeReadyID {
		t.Fatalf("same-hash revival=%+v,%v", third, err)
	}
	var total, valid, revision int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM market_trade_ready_events),(SELECT count(*) FROM market_trade_ready_events WHERE valid),(SELECT revision FROM market_transaction_receipts WHERE block_hash=$1)`, params.BlockHash).Scan(&total, &valid, &revision); err != nil || total != 3 || valid != 1 || revision != 2 {
		t.Fatalf("reorg audit=%d/%d revision%d,%v", total, valid, revision, err)
	}
	projected, err := database.ListMarketTradeEventsForTarget(ctx, MarketRuleTarget{ChainID: 56, TokenAddress: tradeFixtureToken, TradePoolIDs: pools[:2]}, second[0].TradeReadyID, true, 10)
	if err != nil || len(projected) != 1 || projected[0].TradeReadyID != third[0].TradeReadyID {
		t.Fatalf("revived ready pagination=%+v,%v", projected, err)
	}
}

func tradeRuleFixture(t *testing.T, database *Store) MarketRule {
	t.Helper()
	ctx := context.Background()
	project, err := createMarketProject(ctx, database.db, CreateMarketProjectParams{DeBoxUserID: "trade-owner", ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, TokenDecimals: 24, FourMemeStatus: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	params := CreateMarketRuleParams{DeBoxUserID: "trade-owner", MarketProjectID: project.ID, RuleType: "market_large_buy", ThresholdValue: "1", ThresholdUnit: "token"}
	normalizeMarketRuleParams(&params)
	rule, err := createMarketRule(ctx, database.db, params)
	if err != nil {
		t.Fatal(err)
	}
	return rule
}

func TestPostgresMarketTradeFactsLegacyParentAndSentReferenceRemainUnchanged(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	params := tradeReceiptFixture(t, pools[:2])
	legacy := params.Events[0].Event
	legacy.EventKey = "legacy-original-key"
	legacy.Confirmed = true // This fixture represents an already-sent, confirmed legacy source.
	legacy.LogIndex = pointerOf(int32(0))
	legacy.TokenAmountRaw = pointerOf("99")
	legacy.TokenAmount = pointerOf("999")
	parent, _, err := database.CreateMarketEvent(ctx, legacy)
	if err != nil {
		t.Fatal(err)
	}
	rule := tradeRuleFixture(t, database)
	notification, _, err := database.CreateMarketRuleEvent(ctx, CreateMarketRuleEventParams{MarketRuleID: rule.ID, MarketEventID: parent.ID, TriggerKey: "original-sent-key", Details: []byte(`{"amount":"99"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateMarketRuleEventNotification(ctx, notification.ID, "sent", pointerOf("already-sent"), ""); err != nil {
		t.Fatal(err)
	}
	tradeCanonicalFixture(t, database, params)
	saved, err := database.PersistMarketTransaction(ctx, params)
	if err != nil || len(saved) != 1 || saved[0].ID != parent.ID || saved[0].EventKey != legacy.EventKey || saved[0].LogIndex == nil || *saved[0].LogIndex != 0 {
		t.Fatalf("legacy parent=%+v,%v", saved, err)
	}
	requireTradeAmount(t, saved[0].TokenAmountRaw, "99")
	requireTradeAmount(t, saved[0].TokenAmount, "999")
	projected, err := database.ListMarketTradeEventsForTarget(ctx, MarketRuleTarget{ChainID: 56, TokenAddress: tradeFixtureToken, TradePoolIDs: pools[:2]}, 0, true, 10)
	if err != nil || len(projected) != 1 || projected[0].ID != parent.ID {
		t.Fatalf("legacy projection=%+v,%v", projected, err)
	}
	requireTradeAmount(t, projected[0].TokenAmountRaw, "3")
	var status, key, amount string
	var eventID int64
	if err := pool.QueryRow(ctx, `SELECT notification_status,trigger_key,details->>'amount',market_event_id FROM market_rule_events WHERE id=$1`, notification.ID).Scan(&status, &key, &amount, &eventID); err != nil || status != "sent" || key != "original-sent-key" || amount != "99" || eventID != parent.ID {
		t.Fatalf("sent reference changed: %s/%s/%s/%d,%v", status, key, amount, eventID, err)
	}
}

func TestPostgresMarketTradeFactsAmbiguousLegacyParentsArePreserved(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	params := tradeReceiptFixture(t, pools[:2])
	for i := range 2 {
		legacy := params.Events[0].Event
		legacy.EventKey = fmt.Sprintf("legacy-%d", i)
		legacy.LogIndex = pointerOf(int32(i))
		if _, _, err := database.CreateMarketEvent(ctx, legacy); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.PersistMarketTransaction(ctx, params); !errors.Is(err, ErrInvalidMarketEvent) {
		t.Fatalf("ambiguous legacy parent error=%v", err)
	}
	var events, receipts int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM market_events),(SELECT count(*) FROM market_transaction_receipts)`).Scan(&events, &receipts); err != nil || events != 2 || receipts != 0 {
		t.Fatalf("legacy ambiguity changed data: %d/%d,%v", events, receipts, err)
	}
}

func TestPostgresMarketTradeFactsReadyOrderingAndExplicitActivationFence(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	early := tradeReceiptFixture(t, pools[:2])
	early.Confirmed = false
	tradeCanonicalFixture(t, database, early)
	first, err := database.PersistMarketTransaction(ctx, early)
	if err != nil {
		t.Fatal(err)
	}
	later := tradeReceiptFixture(t, pools[:2])
	later.TransactionHash = "0x" + strings.Repeat("f", 64)
	later.TransactionIndex = 3
	refreshTradeReceiptPayload(t, &later)
	second, err := database.PersistMarketTransaction(ctx, later)
	if err != nil || len(second) != 1 || second[0].ID <= first[0].ID || second[0].TradeReadyID <= 0 {
		t.Fatalf("later ready=%+v,%v", second, err)
	}
	if _, err := database.MarkMarketBlockConfirmed(ctx, 56, 100); err != nil {
		t.Fatal(err)
	}
	target := MarketRuleTarget{TargetKey: "legacy:1/trades:v2", ChainID: 56, TokenAddress: tradeFixtureToken, TradePoolIDs: pools[:2]}
	remaining, err := database.ListMarketTradeEventsForTarget(ctx, target, second[0].TradeReadyID, true, 1)
	if err != nil || len(remaining) != 1 || remaining[0].ID != first[0].ID || remaining[0].TradeReadyID <= second[0].TradeReadyID {
		t.Fatalf("late confirmation was skipped: %+v,%v", remaining, err)
	}
	fence, err := database.LatestMarketTradeReadyID(ctx, target)
	if err != nil || fence != remaining[0].TradeReadyID {
		t.Fatalf("fence=%d,%v", fence, err)
	}
	rule := tradeRuleFixture(t, database)
	old := target
	old.TargetKey = "legacy:1/pool:1"
	if err := database.UpdateMarketRuleTargetState(ctx, rule.ID, old, []byte(`{"last_event_id":777,"condition_active":true}`), true); err != nil {
		t.Fatal(err)
	}
	oldSecond := old
	oldSecond.TargetKey = "legacy:1/pool:2"
	if err := database.UpdateMarketRuleTargetState(ctx, rule.ID, oldSecond, []byte(`{"last_event_id":778}`), true); err != nil {
		t.Fatal(err)
	}
	unrelated := old
	unrelated.TargetKey = "legacy:10/pool:1"
	if err := database.UpdateMarketRuleTargetState(ctx, rule.ID, unrelated, []byte(`{}`), true); err != nil {
		t.Fatal(err)
	}
	older := time.Now().UTC().Truncate(time.Microsecond).Add(-2 * time.Hour)
	latest := older.Add(time.Hour)
	if _, err := pool.Exec(ctx, `UPDATE market_rule_target_states SET last_triggered_at=CASE target_key WHEN $2 THEN $4::timestamptz ELSE $5::timestamptz END WHERE market_rule_id=$1 AND target_key IN ($2,$3)`, rule.ID, old.TargetKey, oldSecond.TargetKey, older, latest); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{`{"last_trade_ready_id":0}`, `{"activation_fence":1,"last_trade_ready_id":0}`, `{"activation_fence":-1,"last_trade_ready_id":-1}`} {
		if _, err := database.InitializeTradeRuleState(ctx, rule.ID, target, []byte(state)); !errors.Is(err, ErrInvalidMarketRule) {
			t.Fatalf("invalid explicit fence accepted: %s,%v", state, err)
		}
	}
	state := []byte(fmt.Sprintf(`{"activation_fence":%d,"last_trade_ready_id":%d}`, fence, fence))
	if created, err := database.InitializeTradeRuleState(ctx, rule.ID, target, state); err != nil || !created {
		t.Fatalf("initialize=%v,%v", created, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE market_rule_target_states SET last_triggered_at=NOW() WHERE market_rule_id=$1 AND target_key=$2`, rule.ID, oldSecond.TargetKey); err != nil {
		t.Fatal(err)
	}
	if created, err := database.InitializeTradeRuleState(ctx, rule.ID, target, []byte(`{"activation_fence":0,"last_trade_ready_id":0}`)); err != nil || created {
		t.Fatalf("reinitialize=%v,%v", created, err)
	}
	loaded, found, err := database.LoadMarketRuleTargetState(ctx, rule.ID, target)
	if err != nil || !found || !strings.Contains(string(loaded.State), fmt.Sprint(fence)) || loaded.LastTriggeredAt == nil || !loaded.LastTriggeredAt.Equal(latest) {
		t.Fatalf("new lane changed=%s,%v", loaded.State, err)
	}
	loaded, found, err = database.LoadMarketRuleTargetState(ctx, rule.ID, old)
	if err != nil || !found || !strings.Contains(string(loaded.State), "777") || loaded.LastTriggeredAt == nil || !loaded.LastTriggeredAt.Equal(older) {
		t.Fatalf("legacy lane changed=%s,%v", loaded.State, err)
	}
}

func TestPostgresMarketTradeFactsPagesBothDirectionsByDistinctReadyID(t *testing.T) {
	database, _, pools := tradeFactsFixture(t)
	params := tradeReceiptFixture(t, pools[:2])
	buy := params.Events[0]
	buy.Event.TokenAmountRaw = pointerOf("1")
	buy.Legs = buy.Legs[:1]
	sell := params.Events[0]
	sell.Event.EventType = "sell"
	sell.Event.EventKey = "sell-parent"
	sell.Event.TokenAmountRaw = pointerOf("2")
	sell.Legs = sell.Legs[1:]
	params.Events = []MarketTransactionEvent{buy, sell}
	tradeCanonicalFixture(t, database, params)
	if _, err := database.PersistMarketTransaction(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	target := MarketRuleTarget{ChainID: 56, TokenAddress: tradeFixtureToken, TradePoolIDs: pools[:2]}
	first, err := database.ListMarketTradeEventsForTarget(context.Background(), target, 0, true, 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("first=%+v,%v", first, err)
	}
	second, err := database.ListMarketTradeEventsForTarget(context.Background(), target, first[0].TradeReadyID, true, 1)
	if err != nil || len(second) != 1 || second[0].TradeReadyID <= first[0].TradeReadyID || second[0].ID == first[0].ID {
		t.Fatalf("second direction lost=%+v,%v", second, err)
	}
}

func TestPostgresMarketTradeFactsRejectsStaleReceiptAfterWaitingForChainLock(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	old := tradeReceiptFixture(t, pools[:2])
	tradeCanonicalFixture(t, database, old)
	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.Background())
	if err := lockMarketTradeChain(ctx, holder, 56); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := database.PersistMarketTransaction(ctx, old); result <- err }()
	// Verify that a producer is actually queued behind this exact advisory key.
	deadline := time.Now().Add(time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks w JOIN pg_locks h ON h.locktype=w.locktype AND h.database=w.database AND h.classid=w.classid AND h.objid=w.objid AND h.objsubid=w.objsubid WHERE h.pid=$1 AND h.granted AND NOT w.granted AND h.locktype='advisory')`, holder.Conn().PgConn().PID()).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("producer did not wait for chain lock")
		}
		time.Sleep(time.Millisecond)
	}
	current := old
	current.BlockHash = "0x" + strings.Repeat("e", 64)
	refreshTradeReceiptPayload(t, &current)
	if _, err := holder.Exec(ctx, `UPDATE market_scanned_blocks SET canonical=0 WHERE chain_id=56 AND block_number=100 AND canonical=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, `INSERT INTO market_scanned_blocks(chain_key,chain_id,cursor_key,block_number,block_hash,parent_hash,canonical) VALUES('bsc',56,'market_logs',100,$1,$2,1)`, current.BlockHash, "0x"+strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	newEvents, err := newWithDB(holder).PersistMarketTransaction(ctx, current)
	if err != nil || len(newEvents) != 1 {
		t.Fatalf("current receipt=%+v,%v", newEvents, err)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrInvalidMarketEvent) {
		t.Fatalf("stale receipt accepted after lock: %v", err)
	}
	var receipts int
	var hash string
	var confirmed, reorged int32
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM market_transaction_receipts),block_hash,confirmed,reorged FROM market_events WHERE id=$1`, newEvents[0].ID).Scan(&receipts, &hash, &confirmed, &reorged); err != nil || receipts != 1 || hash != current.BlockHash || confirmed != 1 || reorged != 0 {
		t.Fatalf("current parent downgraded: %d,%s,%d/%d,%v", receipts, hash, confirmed, reorged, err)
	}
}

func TestPostgresMarketTradeFactsProjectsEveryLegWithoutRoundingOrPageTruncation(t *testing.T) {
	database, _, pools := tradeFactsFixture(t)
	params := tradeReceiptFixture(t, pools[:2])
	params.ScopePoolIDs = pools[:1]
	const count = 1001
	maximum := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	total := new(big.Int).Mul(maximum, big.NewInt(count)).String()
	params.Events[0].Event.TokenAmountRaw = &total
	params.Events[0].Legs = make([]MarketTradeLegParams, count)
	logs := make([]map[string]string, count)
	for index := range count {
		params.Events[0].Legs[index] = MarketTradeLegParams{PoolID: pools[0], LogIndex: int64(index + 1), QuoteAddress: tradeFixtureQuote, TokenAmountRaw: maximum.String(), QuoteAmountRaw: "1"}
		logs[index] = map[string]string{"logIndex": fmt.Sprintf("0x%x", index+1)}
	}
	params.ReceiptPayload, _ = json.Marshal(map[string]any{"transactionHash": params.TransactionHash, "blockHash": params.BlockHash, "blockNumber": "0x64", "transactionIndex": "0x2", "logs": logs, "status": "0x1"})
	tradeCanonicalFixture(t, database, params)
	parents, err := database.PersistMarketTransaction(context.Background(), params)
	if err != nil || len(parents) != 1 || parents[0].TokenAmountRaw != nil {
		t.Fatalf("wide logical parent=%+v,%v", parents, err)
	}
	events, err := database.ListMarketTradeEventsForTarget(context.Background(), MarketRuleTarget{ChainID: 56, TokenAddress: tradeFixtureToken, TradePoolIDs: pools[:1]}, 0, true, 1)
	if err != nil || len(events) != 1 {
		t.Fatalf("wide projection=%+v,%v", events, err)
	}
	requireTradeAmount(t, events[0].TokenAmountRaw, total)
	if events[0].TokenAmount != nil || events[0].USDValue != nil {
		t.Fatal("unknown normalization became zero/partial valuation")
	}
	var metadata struct {
		Projection struct {
			IDs []int64 `json:"source_leg_ids"`
		} `json:"trade_projection"`
	}
	if err := json.Unmarshal(events[0].Metadata, &metadata); err != nil || len(metadata.Projection.IDs) != count {
		t.Fatalf("legs truncated by parent page: %d,%v", len(metadata.Projection.IDs), err)
	}
}
