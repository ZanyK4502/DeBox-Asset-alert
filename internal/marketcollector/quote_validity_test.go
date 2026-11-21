package marketcollector

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type snapshotEvidenceRepository struct {
	*fakeRepository
	written store.CreateMarketSnapshotParams
}

func (r *snapshotEvidenceRepository) CreateMarketSnapshot(_ context.Context, params store.CreateMarketSnapshotParams) (store.MarketSnapshot, error) {
	r.written = params
	return store.MarketSnapshot{}, nil
}

func TestSnapshotRetainsFallbackEvidenceAndDistinguishesMissingCountsFromZero(t *testing.T) {
	now := time.Date(2026, 11, 14, 11, 0, 0, 0, time.UTC)
	fetched := now.Add(-90 * time.Second)
	r := &snapshotEvidenceRepository{fakeRepository: &fakeRepository{}}
	s := New(Dependencies{Repository: r, Chain: &fakeChain{}}, Settings{ChainID: 56, ChainKey: "bsc"})
	pair := marketdata.Pair{
		BaseToken: marketdata.Token{Address: "0x1111111111111111111111111111111111111111"},
		PriceUSD:  marketdata.Decimal("1"),
		Transactions: map[string]marketdata.TransactionCounts{
			"m5": {Buys: 0, Sells: 0},
		},
		QuoteQuality: marketquality.Quote{Status: marketquality.StatusStaleFallback, FetchedAt: &fetched},
	}
	if err := s.createPairSnapshot(context.Background(), store.MarketCollectionTarget{
		ChainKey: "bsc", ChainID: 56, TokenAddress: pair.BaseToken.Address, MarketPoolID: 7,
	}, pair, now); err != nil {
		t.Fatal(err)
	}
	p := r.written
	if !p.CapturedAt.Equal(now) || p.SourceTimestamp != nil || p.QuoteQuality.FetchedAt == nil || !p.QuoteQuality.FetchedAt.Equal(fetched) || p.QuoteQuality.Status != marketquality.StatusStaleFallback {
		t.Fatalf("fallback time was washed or mistaken for a source timestamp: %+v", p)
	}
	if p.PriceUSD == nil || *p.PriceUSD != "1" || p.LiquidityUSD != nil {
		t.Fatal("a displayable old value was removed, or missing liquidity became zero")
	}
	if p.Buys5m == nil || p.Sells5m == nil || *p.Buys5m != 0 || *p.Sells5m != 0 || p.Buys1h != nil || p.Sells1h != nil {
		t.Fatal("known zero and missing transaction buckets were mixed")
	}
	var stored marketdata.Pair
	if err := json.Unmarshal(p.RawPayload, &stored); err != nil || stored.QuoteQuality.Status != marketquality.StatusStaleFallback {
		t.Fatalf("audit payload lost quote evidence: %v", err)
	}
}

func TestUnreliableQuoteKeepsRawTransactionFactsWithoutForgedUSD(t *testing.T) {
	for _, name := range []string{"unknown", "fallback", "expired", "future", "future observation", "missing price"} {
		t.Run(name, func(t *testing.T) {
			s, r, _, ctx := receiptFactFixture(t)
			at := time.Unix(1700000000, 0).UTC()
			fetched := at
			price := "1"
			r.snapshot = &store.MarketSnapshot{ID: 8, CapturedAt: at, PriceUSD: &price,
				QuoteQuality: marketquality.Quote{Status: marketquality.StatusLive, FetchedAt: &fetched}}
			switch name {
			case "unknown":
				r.snapshot.QuoteQuality = marketquality.Quote{}
			case "fallback":
				r.snapshot.QuoteQuality.Status = marketquality.StatusStaleFallback
			case "expired":
				fetched = at.Add(-time.Hour)
			case "future":
				fetched = at.Add(time.Second)
			case "future observation":
				r.snapshot.CapturedAt = at.Add(time.Second)
			case "missing price":
				r.snapshot.PriceUSD = nil
			}
			if err := s.hydrateParseAndPersist(context.Background(), testHashA, ctx, "webhook", false); err != nil {
				t.Fatal(err)
			}
			if r.writes != 1 || len(r.params.Events) != 2 || r.valuationHits != 4 {
				t.Fatal("missing quote discarded a receipt or caused redundant lookups")
			}
			for _, event := range r.params.Events {
				if event.Event.USDValue != nil || event.Event.PriceUSD != nil || event.Event.TokenAmountRaw == nil || *event.Event.TokenAmountRaw != "12000" || len(event.Legs) != 2 {
					t.Fatalf("missing price changed raw facts or forged a dollar amount: %+v", event.Event)
				}
				for _, leg := range event.Legs {
					if leg.USDValue != nil || leg.PriceUSD != nil || leg.TokenAmountRaw != "6000" {
						t.Fatal("invalid quote valued a trade leg")
					}
					var proof struct {
						Complete bool      `json:"valuation_complete"`
						At       time.Time `json:"valuation_at"`
					}
					if err := json.Unmarshal(leg.Metadata, &proof); err != nil || proof.Complete || !proof.At.Equal(at) {
						t.Fatalf("unavailable valuation lacks an explicit audit boundary: %v", err)
					}
				}
			}
		})
	}
}

func TestProtocolEventMissingQuoteRetainsQuantityWithoutUSD(t *testing.T) {
	s, r, _, ctx := receiptFactFixture(t)
	at := time.Unix(1700000000, 0).UTC()
	price := "1"
	r.snapshot = &store.MarketSnapshot{ID: 9, CapturedAt: at, PriceUSD: &price,
		QuoteQuality: marketquality.Quote{Status: marketquality.StatusStaleFallback, FetchedAt: &at}}
	event := marketparse.Event{Type: marketparse.EventLiquidityAdded,
		TokenAddress: ctx.targetTokens[0], QuoteAddress: ctx.targetTokens[1],
		TokenAmountRaw: "100", TransactionHash: testHashA, BlockHash: testHashB,
		BlockNumber: 100, PoolID: 7, Metadata: map[string]string{"adapter": marketparse.AdapterV2}}
	result, err := s.prepareParsedEvent(context.Background(), event, ctx, at, "webhook", false, map[eventSnapshotKey]*store.MarketSnapshot{})
	if err != nil || result.Event.USDValue != nil || result.Event.PriceUSD != nil || result.Event.TokenAmountRaw == nil || *result.Event.TokenAmountRaw != "100" {
		t.Fatalf("protocol event used fallback dollars or lost quantity: %+v,%v", result, err)
	}
}

type quoteRefreshRepository struct {
	*fakeRepository
	targets []store.MarketCollectionTarget
	writes  []store.CreateMarketSnapshotParams
	failed  [][]store.MarketCollectionTarget
	markErr error
}

func (r *quoteRefreshRepository) ListMarketCollectionTargets(context.Context, int64) ([]store.MarketCollectionTarget, error) {
	return r.targets, nil
}

func (r *quoteRefreshRepository) CreateMarketSnapshot(_ context.Context, params store.CreateMarketSnapshotParams) (store.MarketSnapshot, error) {
	r.writes = append(r.writes, params)
	return store.MarketSnapshot{}, nil
}

func (r *quoteRefreshRepository) RecordMarketQuoteFailure(_ context.Context, _ int64, targets []store.MarketCollectionTarget, _ time.Time) (int64, error) {
	r.failed = append(r.failed, append([]store.MarketCollectionTarget(nil), targets...))
	return int64(len(targets)), r.markErr
}

type quoteRefreshProvider struct {
	pairs        []marketdata.Pair
	quoteErr     error
	discoveryErr error
	quoteCalls   int
}

func (p *quoteRefreshProvider) DiscoverPools(context.Context, string, string) ([]marketdata.Pair, error) {
	return nil, p.discoveryErr
}
func (*quoteRefreshProvider) PairsByTokens(context.Context, string, []string) ([]marketdata.Pair, error) {
	return nil, nil
}
func (p *quoteRefreshProvider) PairsByAddresses(context.Context, string, []string) ([]marketdata.Pair, error) {
	p.quoteCalls++
	return p.pairs, p.quoteErr
}

func quoteRefreshFixture() (*Service, *quoteRefreshRepository, *quoteRefreshProvider) {
	const token = "0x1111111111111111111111111111111111111111"
	const quote = "0x2222222222222222222222222222222222222222"
	a, b := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	at := time.Date(2026, 11, 14, 11, 0, 0, 0, time.UTC)
	r := &quoteRefreshRepository{fakeRepository: &fakeRepository{
		activeProjects: []store.MarketProject{{TokenAddress: token, TokenDecimals: 0}},
	}, targets: []store.MarketCollectionTarget{
		{ChainKey: "bsc", ChainID: 56, TokenAddress: token, MarketPoolID: 7, PoolAddress: &a},
		{ChainKey: "bsc", ChainID: 56, TokenAddress: token, MarketPoolID: 8, PoolAddress: &b},
		{ChainKey: "bsc", ChainID: 56, TokenAddress: token, MarketPoolID: 9},
	}}
	fetched := at.Add(-time.Second)
	p := &quoteRefreshProvider{pairs: []marketdata.Pair{{
		ChainID: "bsc", PairAddress: a, BaseToken: marketdata.Token{Address: token}, QuoteToken: marketdata.Token{Address: quote},
		PriceUSD: marketdata.Decimal("1"), QuoteQuality: marketquality.Quote{Status: marketquality.StatusLive, FetchedAt: &fetched},
	}, {
		ChainID: "bsc", PairAddress: b, BaseToken: marketdata.Token{Address: token}, QuoteToken: marketdata.Token{Address: quote},
		PriceUSD: marketdata.Decimal("1"), QuoteQuality: marketquality.Quote{Status: marketquality.StatusLive, FetchedAt: &fetched},
	}}}
	s := New(Dependencies{Repository: r, Chain: &fakeChain{}, Market: p, Now: func() time.Time { return at }},
		Settings{Enabled: true, ChainKey: "bsc", ChainID: 56})
	return s, r, p
}

func TestQuoteRequestFailureMarksOnlyRequestedTargetsInOneBatch(t *testing.T) {
	s, r, p := quoteRefreshFixture()
	p.quoteErr = errors.New("provider unavailable without a cached response")
	if err := s.captureSnapshots(context.Background()); !errors.Is(err, p.quoteErr) {
		t.Fatalf("provider failure lost: %v", err)
	}
	if len(r.failed) != 1 || len(r.failed[0]) != 2 || len(r.writes) != 0 {
		t.Fatalf("failure did not mark only requested pools in one batch: %+v", r.failed)
	}
	r.markErr = errors.New("failed to record the unavailable observation")
	if err := s.captureSnapshots(context.Background()); !errors.Is(err, p.quoteErr) || !errors.Is(err, r.markErr) {
		t.Fatalf("failure marker write error was hidden: %v", err)
	}
}

func TestMissingOrMismatchedQuoteMarksTheGapAndKeepsValidPools(t *testing.T) {
	for _, mismatch := range []string{"missing", "wrong token", "wrong chain"} {
		t.Run(mismatch, func(t *testing.T) {
			s, r, p := quoteRefreshFixture()
			switch mismatch {
			case "missing":
				p.pairs = p.pairs[:1]
			case "wrong token":
				p.pairs[1].BaseToken.Address = "0x3333333333333333333333333333333333333333"
			case "wrong chain":
				p.pairs[1].ChainID = "ethereum"
			}
			if err := s.captureSnapshots(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(r.writes) != 1 || r.writes[0].MarketPoolID != 7 || len(r.failed) != 1 || len(r.failed[0]) != 1 || r.failed[0][0].MarketPoolID != 8 {
				t.Fatalf("quote gap was hidden or healthy pool discarded: writes=%+v failed=%+v", r.writes, r.failed)
			}
		})
	}
}

func TestDiscoveryFailureStillRefreshesKnownPoolQuotes(t *testing.T) {
	s, r, p := quoteRefreshFixture()
	p.discoveryErr = errors.New("new pool discovery unavailable")
	if err := s.RefreshMarketData(context.Background()); !errors.Is(err, p.discoveryErr) {
		t.Fatalf("discovery failure lost: %v", err)
	}
	if p.quoteCalls != 1 || len(r.writes) != 2 || len(r.failed) != 0 {
		t.Fatal("unrelated discovery failure stopped known-pool quote collection")
	}
}
