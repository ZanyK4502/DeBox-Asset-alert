package marketrules

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type holderQuoteRepository struct {
	Repository
	event store.CreateMarketEventParams
}

func (r *holderQuoteRepository) CreateMarketEvent(_ context.Context, params store.CreateMarketEventParams) (store.MarketEvent, bool, error) {
	r.event = params
	return store.MarketEvent{}, true, nil
}

func TestHolderBalanceFactsSurviveMissingQuoteWhileUSDEstimateRequiresProof(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	price := "5"
	for _, test := range []struct {
		name     string
		snapshot *store.MarketSnapshot
		wantUSD  bool
	}{
		{"missing snapshot", nil, false},
		{"unknown quote", &store.MarketSnapshot{PriceUSD: &price}, false},
		{"stale fallback", &store.MarketSnapshot{PriceUSD: &price, QuoteQuality: marketquality.Quote{Status: marketquality.StatusStaleFallback, FetchedAt: &now}}, false},
		{"live quote", &store.MarketSnapshot{PriceUSD: &price, QuoteQuality: liveQuoteFixture(now)}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &holderQuoteRepository{}
			service := New(Dependencies{Repository: repository}, Settings{})
			project := store.MarketProject{ID: 1, ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFactsTestToken, TokenDecimals: 0}
			old := store.MarketHolder{HolderAddress: "0x2222222222222222222222222222222222222222", BalanceRaw: "100", Balance: "100"}
			if err := service.recordHolderBalanceChange(context.Background(), project, old, "200", "200", 1, test.snapshot, now); err != nil {
				t.Fatal(err)
			}
			event := repository.event
			if event.TokenAmountRaw == nil || *event.TokenAmountRaw != "100" || event.TokenAmount == nil || *event.TokenAmount != "100" || event.EventType != "holder_increase" {
				t.Fatalf("quote failure discarded holder facts: %#v", event)
			}
			if test.wantUSD {
				if event.USDValue == nil || *event.USDValue != "500" {
					t.Fatalf("valid USD estimate=%v", event.USDValue)
				}
			} else if event.USDValue != nil {
				t.Fatalf("unusable quote produced USD estimate=%s", *event.USDValue)
			}
			if available := eventValuationAvailable(store.MarketEvent{USDValue: event.USDValue, Metadata: event.Metadata, OccurredAt: event.OccurredAt}); available != test.wantUSD {
				t.Fatalf("holder valuation proof available=%v want=%v metadata=%s", available, test.wantUSD, event.Metadata)
			}
			var proof eventValuationProof
			if json.Unmarshal(event.Metadata, &proof) != nil || proof.At == nil || !proof.At.Equal(now) || proof.SchemaVersion != 1 {
				t.Fatalf("missing original holder valuation time: %s", event.Metadata)
			}
		})
	}
}

func TestHolderValuationTimeMatchesPostgresMicrosecondPrecision(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 123456789, time.UTC)
	repository := &holderQuoteRepository{}
	service := New(Dependencies{Repository: repository}, Settings{})
	project := store.MarketProject{ChainID: 56, TokenDecimals: 0}
	old := store.MarketHolder{HolderAddress: "0x2222222222222222222222222222222222222222", BalanceRaw: "100"}
	snapshot := &store.MarketSnapshot{PriceUSD: stringPointer("5"), QuoteQuality: liveQuoteFixture(now)}
	if err := service.recordHolderBalanceChange(context.Background(), project, old, "200", "200", 1, snapshot, now); err != nil {
		t.Fatal(err)
	}
	var proof eventValuationProof
	_ = json.Unmarshal(repository.event.Metadata, &proof)
	storedAt := now.UTC().Truncate(time.Microsecond)
	if proof.At == nil || !proof.At.Equal(storedAt) {
		t.Fatalf("valuation_at retained unsupported nanoseconds: %s", repository.event.Metadata)
	}
	event := store.MarketEvent{OccurredAt: storedAt, USDValue: repository.event.USDValue, Metadata: repository.event.Metadata}
	if !eventValuationAvailable(event) {
		t.Fatal("valid proof failed after PostgreSQL timestamp truncation")
	}
	event.OccurredAt = storedAt.Add(time.Microsecond)
	if eventValuationAvailable(event) {
		t.Fatal("proof certified another event microsecond")
	}
}
