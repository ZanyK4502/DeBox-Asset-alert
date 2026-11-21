package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPostgresMarketTradeFactsProjectionBudgetPagesCompleteParentsAndDetectsWindowGap(t *testing.T) {
	database, _, pools := tradeFactsFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)
	var readyIDs []int64
	for group := range 3 {
		params := tradeReceiptFixture(t, pools[:2])
		params.ScopePoolIDs = pools[:1]
		params.TransactionHash = fmt.Sprintf("0x%064x", group+1)
		params.BlockNumber = int64(102 - group)
		params.BlockHash = fmt.Sprintf("0x%064x", 100+group)
		params.OccurredAt = base.Add(-time.Duration(group) * time.Minute)
		params.Events[0].Event.TokenAmountRaw = pointerOf("4000")
		params.Events[0].Event.RawPayload = []byte(`{"global":"` + strings.Repeat("x", 65536) + `"}`)
		params.Events[0].Event.Metadata = []byte(`{"adapter":"pancake_v2","unused":"` + strings.Repeat("y", 65536) + `"}`)
		params.Events[0].Legs = make([]MarketTradeLegParams, 4000)
		logs := make([]map[string]string, 4000)
		for index := range logs {
			params.Events[0].Legs[index] = MarketTradeLegParams{PoolID: pools[0], LogIndex: int64(index + 1), QuoteAddress: tradeFixtureQuote, TokenAmountRaw: "1", QuoteAmountRaw: "1"}
			logs[index] = map[string]string{"logIndex": fmt.Sprintf("0x%x", index+1)}
		}
		params.ReceiptPayload, _ = json.Marshal(map[string]any{"transactionHash": params.TransactionHash, "blockHash": params.BlockHash, "blockNumber": fmt.Sprintf("0x%x", params.BlockNumber), "transactionIndex": "0x2", "logs": logs, "status": "0x1"})
		refresh := params.Events[0].Event
		refresh.TransactionHash = &params.TransactionHash
		refresh.BlockHash = &params.BlockHash
		refresh.BlockNumber = &params.BlockNumber
		refresh.TransactionIndex = &params.TransactionIndex
		params.Events[0].Event = refresh
		tradeCanonicalFixture(t, database, params)
		events, err := database.PersistMarketTransaction(ctx, params)
		if err != nil || len(events) != 1 {
			t.Fatalf("large receipt %d=%+v,%v", group, events, err)
		}
		readyIDs = append(readyIDs, events[0].TradeReadyID)
	}
	target := MarketRuleTarget{ChainID: 56, TokenAddress: tradeFixtureToken, TradePoolIDs: pools[:1]}
	first, err := database.ListMarketTradeEventsForTarget(ctx, target, 0, true, 2000)
	if err != nil || len(first) != 2 {
		t.Fatalf("bounded first page=%d,%v", len(first), err)
	}
	for _, event := range first {
		requireTradeAmount(t, event.TokenAmountRaw, "4000")
		if string(event.RawPayload) != "{}" || strings.Contains(string(event.Metadata), "unused") {
			t.Fatal("global parent payload bypassed projection budget")
		}
	}
	known := []int64{first[0].TradeReadyID, first[1].TradeReadyID}
	complete, err := database.MarketTradeWindowContextComplete(ctx, target, readyIDs[2], base.Add(-3*time.Minute), base, known)
	if err != nil || complete {
		t.Fatalf("partial historical window accepted: %v,%v", complete, err)
	}
	second, err := database.ListMarketTradeEventsForTarget(ctx, target, first[1].TradeReadyID, true, 2000)
	if err != nil || len(second) != 1 || second[0].TradeReadyID != readyIDs[2] {
		t.Fatalf("continuation lost parent: %+v,%v", second, err)
	}
	requireTradeAmount(t, second[0].TokenAmountRaw, "4000")
	known = append(known, second[0].TradeReadyID)
	complete, err = database.MarketTradeWindowContextComplete(ctx, target, readyIDs[2], base.Add(-3*time.Minute), base, known)
	if err != nil || !complete {
		t.Fatalf("complete window rejected: %v,%v", complete, err)
	}
	// Publication times run opposite to ready order here. An empty ID set may
	// never be mistaken for SQL ANY(NULL), or inferred complete from one time.
	complete, err = database.MarketTradeWindowContextComplete(ctx, target, readyIDs[2], base.Add(-3*time.Minute), base, nil)
	if err != nil || complete {
		t.Fatalf("empty history accepted: %v,%v", complete, err)
	}
	reverse, err := database.ListMarketTradeEventsForTarget(ctx, target, 0, false, 2000)
	if err != nil || len(reverse) != 2 || reverse[0].TradeReadyID != readyIDs[2] || reverse[1].TradeReadyID != readyIDs[1] {
		t.Fatalf("reverse prefix=%+v,%v", reverse, err)
	}
}

func TestPostgresMarketTradeFactsConfirmationBudgetCommitsAndSignalsRemainder(t *testing.T) {
	for _, test := range []struct {
		name                             string
		receipts, parents, first, second int64
	}{
		{"receipt cap", 2001, 1, 2000, 1}, {"parent cap", 6, 2000, 10000, 2000},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, pool := openIntegrationStore(t)
			ctx := context.Background()
			var scopeID int64
			if err := pool.QueryRow(ctx, `INSERT INTO market_trade_scopes(scope_key,parser_version,pool_ids,token_addresses) VALUES(repeat('a',64),1,'{}',ARRAY(SELECT '0x'||lpad(to_hex(i),40,'0') FROM generate_series(1,$1::bigint)i)) RETURNING id`, test.parents).Scan(&scopeID); err != nil {
				t.Fatal(err)
			}
			blockHash := "0x" + strings.Repeat("b", 64)
			if _, err := pool.Exec(ctx, `INSERT INTO market_scanned_blocks(chain_key,chain_id,cursor_key,block_number,block_hash,parent_hash) VALUES('bsc',56,'market_logs',100,$1,$2)`, blockHash, "0x"+strings.Repeat("0", 64)); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO market_events(chain_key,chain_id,token_address,event_type,event_key,transaction_hash,transaction_index,log_index,block_number,block_hash,source,occurred_at)
			SELECT 'bsc',56,'0x'||lpad(to_hex(p),40,'0'),'token_created','budget-'||r||'-'||p,'0x'||lpad(to_hex(r),64,'0'),r,p,100,$3,'fixture',NOW()
			FROM generate_series(1,$1::bigint)r CROSS JOIN generate_series(1,$2::bigint)p`, test.receipts, test.parents, blockHash); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO market_transaction_receipts(chain_key,chain_id,transaction_hash,block_number,block_hash,transaction_index,occurred_at,scope_id,receipt_payload,complete,completed_at)
			SELECT 'bsc',56,'0x'||lpad(to_hex(r),64,'0'),100,$2,r,NOW(),$3,'{}',TRUE,NOW() FROM generate_series(1,$1::bigint)r`, test.receipts, blockHash, scopeID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO market_transaction_events(receipt_id,market_event_id) SELECT r.id,e.id FROM market_transaction_receipts r JOIN market_events e ON e.chain_id=r.chain_id AND e.transaction_hash=r.transaction_hash`); err != nil {
				t.Fatal(err)
			}
			count, err := database.MarkMarketBlockConfirmed(ctx, 56, 100)
			if !errors.Is(err, ErrMarketTradeConfirmationPending) || count != test.first {
				t.Fatalf("first batch count=%d,error=%v", count, err)
			}
			var confirmed int64
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM market_events WHERE confirmed=1`).Scan(&confirmed); err != nil || confirmed != test.first {
				t.Fatalf("first batch rolled back: %d,%v", confirmed, err)
			}
			count, err = database.MarkMarketBlockConfirmed(ctx, 56, 100)
			if err != nil || count != test.second {
				t.Fatalf("second bounded batch=%d,%v", count, err)
			}
			var pending int64
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM market_transaction_receipts WHERE NOT confirmed`).Scan(&pending); err != nil || pending != 0 {
				t.Fatalf("undrained receipts=%d,%v", pending, err)
			}
		})
	}
}

func TestPostgresMarketTradeFactsChecksGeneratedMetadataBudgetAtomically(t *testing.T) {
	database, pool, pools := tradeFactsFixture(t)
	params := tradeReceiptFixture(t, pools[:2])
	metadata := make(map[string]int, 200)
	for index := range 200 {
		metadata[fmt.Sprintf("k%03d", index)] = 0
	}
	encoded, _ := json.Marshal(metadata)
	if len(encoded) > 2048 {
		t.Fatal("fixture must pass the compact input byte guard")
	}
	params.Events[0].Legs[0].Metadata = encoded
	if _, err := database.PersistMarketTransaction(context.Background(), params); err == nil {
		t.Fatal("expanded JSONB metadata exceeded its budget without an error")
	}
	var parents, receipts int
	if err := pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM market_events),(SELECT count(*) FROM market_transaction_receipts)`).Scan(&parents, &receipts); err != nil || parents != 0 || receipts != 0 {
		t.Fatalf("oversized generated JSON partially committed: %d/%d,%v", parents, receipts, err)
	}
}
