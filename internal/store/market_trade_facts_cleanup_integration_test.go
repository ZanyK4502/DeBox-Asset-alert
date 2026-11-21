package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPostgresMarketTradeFactsCleanupPreservesReferencesAndRetention(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	ctx := context.Background()
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	old := cutoff.Add(-time.Hour)
	referenced := tradeReceiptFixture(t, pools[:2])
	referenced.OccurredAt = old
	referenced.Confirmed = true
	tradeCanonicalFixture(t, database, referenced)
	parents, err := database.PersistMarketTransaction(ctx, referenced)
	if err != nil {
		t.Fatal(err)
	}
	rule := tradeRuleFixture(t, database)
	if _, _, err := database.CreateMarketRuleEvent(ctx, CreateMarketRuleEventParams{MarketRuleID: rule.ID, MarketEventID: parents[0].ID, TriggerKey: "retained-reference"}); err != nil {
		t.Fatal(err)
	}
	plain := tradeReceiptFixture(t, pools[:2])
	plain.TransactionHash = "0x" + strings.Repeat("c", 64)
	plain.OccurredAt = old
	plain.Confirmed = false
	plain.ScopePoolIDs = pools[1:2]
	plain.Events[0].Legs = plain.Events[0].Legs[1:]
	plain.Events[0].Event.TokenAmountRaw = pointerOf("2")
	refreshTradeReceiptPayload(t, &plain)
	if _, err := database.PersistMarketTransaction(ctx, plain); err != nil {
		t.Fatal(err)
	}
	orphan := tradeReceiptFixture(t, pools[:2])
	orphan.TransactionHash = "0x" + strings.Repeat("d", 64)
	orphan.OccurredAt = old
	orphan.Confirmed = false
	orphan.ScopePoolIDs = pools[2:]
	orphan.Events = nil
	refreshTradeReceiptPayload(t, &orphan)
	if _, err := database.PersistMarketTransaction(ctx, orphan); err != nil {
		t.Fatal(err)
	}
	recent := tradeReceiptFixture(t, pools[:2])
	recent.TransactionHash = "0x" + strings.Repeat("e", 64)
	recent.ScopePoolIDs = []int64{}
	recent.Events = nil
	recent.Confirmed = false
	refreshTradeReceiptPayload(t, &recent)
	if _, err := database.PersistMarketTransaction(ctx, recent); err != nil {
		t.Fatal(err)
	}
	phase := tradeReceiptFixture(t, pools[:2])
	phase.TransactionHash = "0x" + strings.Repeat("f", 64)
	phase.OccurredAt = old
	phase.Confirmed = false
	phase.ScopePoolIDs = []int64{}
	phase.Events = []MarketTransactionEvent{{Event: CreateMarketEventParams{TokenAddress: tradeFixtureToken, EventType: "migrated", EventKey: "retained-four-meme", LogIndex: pointerOf(int32(1)), Source: "fixture", Metadata: []byte(`{"adapter":"four_meme_v2"}`)}}}
	refreshTradeReceiptPayload(t, &phase)
	if _, err := database.PersistMarketTransaction(ctx, phase); err != nil {
		t.Fatal(err)
	}
	result, err := database.CleanupOrphanMarketTradeData(ctx, cutoff)
	if err != nil || result.ReceiptsDeleted != 1 || result.ScopesDeleted != 1 {
		t.Fatalf("orphan-only cleanup=%+v,%v", result, err)
	}
	// The ordinary event policy runs first. MRE- and current Four.meme-source
	// parents survive; only the unreferenced old parent releases its receipt.
	removed, err := database.CleanupMarketCollectionData(ctx, cutoff, cutoff, cutoff)
	if err != nil || removed != 3 {
		t.Fatalf("full cleanup removed=%d,%v", removed, err)
	}
	var receipts, scopes, events, phaseSources, notificationRefs int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM market_transaction_receipts),(SELECT count(*) FROM market_trade_scopes),(SELECT count(*) FROM market_events),(SELECT count(*) FROM market_four_meme_states),(SELECT count(*) FROM market_rule_events)`).Scan(&receipts, &scopes, &events, &phaseSources, &notificationRefs); err != nil || receipts != 3 || scopes != 2 || events != 2 || phaseSources != 1 || notificationRefs != 1 {
		t.Fatalf("retention counts=%d/%d/%d/%d/%d,%v", receipts, scopes, events, phaseSources, notificationRefs, err)
	}
}

func TestPostgresMarketTradeFactsCleanupIsBoundedWithoutBusyLoop(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	var shared int64
	if err := pool.QueryRow(ctx, `INSERT INTO market_trade_scopes(scope_key,parser_version,pool_ids,token_addresses) VALUES(repeat('0',64),1,'{}','{}') RETURNING id`).Scan(&shared); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO market_transaction_receipts(chain_key,chain_id,transaction_hash,block_number,block_hash,transaction_index,occurred_at,scope_id,receipt_payload)
		SELECT 'bsc',56,'0x'||lpad(to_hex(i),64,'0'),i,'0x'||repeat('a',64),0,NOW()-INTERVAL '2 days',$1,'{}'::jsonb FROM generate_series(1,10001)i`, shared); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO market_trade_scopes(scope_key,parser_version,pool_ids,token_addresses) SELECT '1'||lpad(to_hex(i),63,'0'),1,'{}','{}' FROM generate_series(1,10001)i`); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	result, err := database.CleanupOrphanMarketTradeData(ctx, cutoff)
	if err != nil || result.ReceiptsDeleted != 10000 || result.ScopesDeleted != 10000 {
		t.Fatalf("bounded cleanup=%+v,%v", result, err)
	}
	var receipts, scopes int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM market_transaction_receipts),(SELECT count(*) FROM market_trade_scopes)`).Scan(&receipts, &scopes); err != nil || receipts != 1 || scopes != 2 {
		t.Fatalf("cleanup looped beyond cap: %d/%d,%v", receipts, scopes, err)
	}
	result, err = database.CleanupOrphanMarketTradeData(ctx, cutoff)
	if err != nil || result.ReceiptsDeleted != 1 || result.ScopesDeleted != 2 {
		t.Fatalf("next bounded cycle=%+v,%v", result, err)
	}
}

func TestPostgresMarketTradeFactsCleanupAndScopeWritersAreRaceSafe(t *testing.T) {
	for _, mode := range []string{"writer key share", "cleanup deletes first"} {
		t.Run(mode, func(t *testing.T) {
			database, pool, pools := tradeFactsFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			params := tradeReceiptFixture(t, pools[:2])
			params.Confirmed = false
			empty := params
			empty.Events = nil
			if _, err := database.PersistMarketTransaction(ctx, empty); err != nil {
				t.Fatal(err)
			}
			var scopeID int64
			if err := pool.QueryRow(ctx, `DELETE FROM market_transaction_receipts RETURNING scope_id`).Scan(&scopeID); err != nil {
				t.Fatal(err)
			}
			holder, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Rollback(context.Background())
			if mode == "writer key share" {
				if err := lockMarketTradeChain(ctx, holder, 56); err != nil {
					t.Fatal(err)
				}
				var id int64
				if err := holder.QueryRow(ctx, `SELECT id FROM market_trade_scopes WHERE id=$1 FOR KEY SHARE`, scopeID).Scan(&id); err != nil {
					t.Fatal(err)
				}
				result, err := database.CleanupOrphanMarketTradeData(ctx, time.Now().Add(time.Hour))
				if err != nil || result.ReceiptsDeleted != 0 || result.ScopesDeleted != 0 {
					t.Fatalf("active writer scope reclaimed: %+v,%v", result, err)
				}
				if _, err := newWithDB(holder).PersistMarketTransaction(ctx, params); err != nil {
					t.Fatal(err)
				}
				if err := holder.Commit(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := holder.Exec(ctx, `DELETE FROM market_trade_scopes WHERE id=$1`, scopeID); err != nil {
					t.Fatal(err)
				}
				result := make(chan error, 1)
				go func() { _, err := database.PersistMarketTransaction(ctx, params); result <- err }()
				deadline := time.Now().Add(time.Second)
				for {
					var waiting bool
					if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks w JOIN pg_locks h ON h.locktype=w.locktype AND h.transactionid=w.transactionid WHERE h.pid=$1 AND h.granted AND NOT w.granted AND h.locktype='transactionid')`, holder.Conn().PgConn().PID()).Scan(&waiting); err != nil {
						t.Fatal(err)
					}
					if waiting {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("scope writer did not wait for cleanup")
					}
					time.Sleep(time.Millisecond)
				}
				if err := holder.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				if err := <-result; err != nil {
					t.Fatalf("scope lookup/delete race failed: %v", err)
				}
			}
			var receipts, scopes, validRefs int
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM market_transaction_receipts),(SELECT count(*) FROM market_trade_scopes),(SELECT count(*) FROM market_transaction_receipts r JOIN market_trade_scopes s ON s.id=r.scope_id)`).Scan(&receipts, &scopes, &validRefs); err != nil || receipts != 1 || scopes != 1 || validRefs != 1 {
				t.Fatalf("scope FK race=%s,%v", fmt.Sprint(receipts, scopes, validRefs), err)
			}
		})
	}
}
