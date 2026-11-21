package marketcollector

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

// The old fixtures model ordinary 18-decimal contracts. Dedicated metadata
// tests override this method to exercise true zero, other scales and failure.
func (*fakeChain) TokenDecimals(context.Context, string, string, string) (int32, error) {
	return 18, nil
}

type precisionChain struct {
	*fakeChain
	value          int32
	err            error
	calls          int
	token0, token1 string
}

func (c *precisionChain) TokenDecimals(context.Context, string, string, string) (int32, error) {
	c.calls++
	return c.value, c.err
}
func (c *precisionChain) PoolTokens(context.Context, string, string, string) (string, string, error) {
	return c.token0, c.token1, nil
}

func TestResolveTokenDecimalsUsesKnownZeroAndBoundedSuccessfulReads(t *testing.T) {
	ctx := context.Background()
	token := "0x1111111111111111111111111111111111111111"
	quote := "0x2222222222222222222222222222222222222222"
	client := &precisionChain{fakeChain: &fakeChain{}, value: 6}
	service := New(Dependencies{Chain: client}, Settings{ChainKey: "bsc", ChainID: 56})
	known := map[string]int{token: 0}
	if got, err := service.resolveTokenDecimals(ctx, token, known); err != nil || got != 0 || client.calls != 0 {
		t.Fatalf("known zero = %d, %v, calls %d", got, err, client.calls)
	}
	for i := 0; i < 2; i++ {
		if got, err := service.resolveTokenDecimals(ctx, quote, known); err != nil || got != 6 {
			t.Fatalf("quote = %d, %v", got, err)
		}
	}
	if client.calls != 1 {
		t.Fatalf("repeated metadata RPCs = %d", client.calls)
	}
	if got, err := service.resolveTokenDecimals(ctx, marketparse.NativeTokenAddress, known); err != nil || got != 18 || client.calls != 1 {
		t.Fatalf("native = %d, %v", got, err)
	}
	client.err = errors.New("unavailable")
	unknown := "0x3333333333333333333333333333333333333333"
	if _, err := service.resolveTokenDecimals(ctx, unknown, known); err == nil {
		t.Fatal("unknown precision accepted")
	}
	if _, ok := known[unknown]; ok {
		t.Fatal("failed metadata was cached as a real decimal value")
	}
}

func TestDiscoveredPairReadsActualPrecisionAndDoesNotPersistUnknownQuote(t *testing.T) {
	ctx := context.Background()
	token := "0x1111111111111111111111111111111111111111"
	quote := "0x2222222222222222222222222222222222222222"
	project := store.MarketProject{TokenAddress: token, TokenDecimals: 0}
	pair := marketdata.Pair{ChainID: "bsc", DexID: "fourmeme", PairAddress: token + ":4meme", BaseToken: marketdata.Token{Address: token}, QuoteToken: marketdata.Token{Address: quote}}
	repo := &fakeRepository{}
	client := &precisionChain{fakeChain: &fakeChain{}, value: 6}
	now := time.Now()
	service := New(Dependencies{Chain: client, Repository: repo, Now: func() time.Time { return now }}, Settings{ChainKey: "bsc", ChainID: 56})
	known := map[string]int{token: 0}
	for i := 0; i < 2; i++ {
		if err := service.persistDiscoveredPair(ctx, project, pair, map[string][2]string{}, known); err != nil {
			t.Fatal(err)
		}
	}
	if repo.lastPool.Token0Decimals != 0 || repo.lastPool.Token1Decimals != 6 || !repo.lastPool.Token0DecimalsVerified || !repo.lastPool.Token1DecimalsVerified || client.calls != 1 {
		t.Fatalf("pool metadata = %+v, RPCs %d", repo.lastPool, client.calls)
	}
	repo.lastPool = store.UpsertMarketPoolParams{}
	// A previously successful precision may be reused for five minutes. The
	// failure assertion concerns an expired value, which must never rescue RPC.
	now = now.Add(tokenDecimalsCacheTTL)
	client.err = errors.New("unavailable")
	if err := service.persistDiscoveredPair(ctx, project, pair, map[string][2]string{}, map[string]int{token: 0}); err != nil {
		t.Fatalf("quote-only candidate blocked collection: %v", err)
	}
	if repo.lastPool.PoolKey != "" {
		t.Fatal("unknown quote precision was persisted")
	}
	pair.PairAddress = "0x4444444444444444444444444444444444444444"
	client.token0, client.token1 = token, quote
	if err := service.persistDiscoveredPair(ctx, project, pair, map[string][2]string{}, map[string]int{token: 0}); err == nil {
		t.Fatal("fully monitored pool accepted unknown precision")
	}
}
