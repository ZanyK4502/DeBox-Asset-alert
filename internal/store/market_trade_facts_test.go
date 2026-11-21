package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestMarketCollectionTargetsCapacityFailsClosed(t *testing.T) {
	for _, count := range []int{10000, 10001} {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		rows := pgxmock.NewRows([]string{"market_project_id"})
		for i := range count {
			rows.AddRow(int64(i + 1))
		}
		mock.ExpectQuery(`(?s)SELECT.*LIMIT 10001`).WithArgs(int64(56)).WillReturnRows(rows)
		targets, err := newWithDB(mock).ListMarketCollectionTargets(context.Background(), 56)
		if count == 10000 && (err != nil || len(targets) != count) {
			t.Fatalf("boundary targets=%d,%v", len(targets), err)
		}
		if count > 10000 && (err == nil || targets != nil) {
			t.Fatalf("truncated targets escaped: %d,%v", len(targets), err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		mock.Close()
	}
}

func TestMarketTransactionInvalidFactsNeverReachDatabase(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*MarketTransactionParams)
	}{
		{"missing receipt logs", func(p *MarketTransactionParams) {
			var body map[string]json.RawMessage
			_ = json.Unmarshal(p.ReceiptPayload, &body)
			delete(body, "logs")
			p.ReceiptPayload, _ = json.Marshal(body)
		}},
		{"different receipt hash", func(p *MarketTransactionParams) {
			p.BlockHash = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}},
		{"duplicate leg log", func(p *MarketTransactionParams) { p.Events[0].Legs[1].LogIndex = p.Events[0].Legs[0].LogIndex }},
		{"incomplete raw total", func(p *MarketTransactionParams) { p.Events[0].Event.TokenAmountRaw = pointerOf("4") }},
		{"DEX trade without legs", func(p *MarketTransactionParams) { p.Events[0].Legs = nil }},
		{"invalid pool scope", func(p *MarketTransactionParams) { p.ScopePoolIDs = []int64{-1} }},
		{"oversized exponent", func(p *MarketTransactionParams) { p.Events[0].Legs[0].USDValue = pointerOf("1e1000000000") }},
		{"oversized leg metadata", func(p *MarketTransactionParams) {
			p.Events[0].Legs[0].Metadata = []byte(`{"value":"` + strings.Repeat("x", 2048) + `"}`)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			params := tradeReceiptFixture(t, []int64{1, 2})
			test.mutate(&params)
			if _, err := newWithDB(nil).PersistMarketTransaction(context.Background(), params); !errors.Is(err, ErrInvalidMarketEvent) {
				t.Fatalf("invalid input error=%v", err)
			}
		})
	}
}
