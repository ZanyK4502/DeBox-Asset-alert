package marketprotocol

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
)

type countedPoolReader struct {
	fakePoolReader
	tokens, factories int
}

func (r *countedPoolReader) PoolTokens(ctx context.Context, p, c, f string) (string, string, error) {
	r.tokens++
	return r.fakePoolReader.PoolTokens(ctx, p, c, f)
}
func (r *countedPoolReader) PoolFactory(ctx context.Context, p, c, f string) (string, error) {
	r.factories++
	return r.fakePoolReader.PoolFactory(ctx, p, c, f)
}

func TestPoolFactsAreSharedButTokenMembershipIsNeverShared(t *testing.T) {
	reader := &countedPoolReader{fakePoolReader: fakePoolReader{token0: testToken, token1: testQuote, factory: Deployments("bsc")[0].Factory}}
	pair := marketdata.Pair{ChainID: "bsc", PairAddress: testPool, BaseToken: marketdata.Token{Address: testToken}, QuoteToken: marketdata.Token{Address: testQuote}}
	facts := ReadPoolFacts(context.Background(), reader, "bsc", pair)
	for _, token := range []string{testToken, testQuote, "0x" + strings.ToUpper(testToken[2:])} {
		if got := ClassifyPairWithFacts("bsc", token, pair, facts); !got.Supported || !got.FactoryVerified {
			t.Fatalf("legitimate token %s rejected: %+v", token, got)
		}
	}
	third := "0x4444444444444444444444444444444444444444"
	if got := ClassifyPairWithFacts("bsc", third, pair, facts); got.Supported || got.FactoryVerified {
		t.Fatal("third token borrowed a valid token's classification")
	}
	if reader.tokens != 1 || reader.factories != 1 {
		t.Fatalf("facts unexpectedly re-read: %d/%d", reader.tokens, reader.factories)
	}
	// Features must not be a writable alias into a later consumer's result.
	first := ClassifyPairWithFacts("bsc", testToken, pair, facts)
	first.SupportedFeature[0] = "changed"
	if ClassifyPairWithFacts("bsc", testQuote, pair, facts).SupportedFeature[0] == "changed" {
		t.Fatal("shared classification was mutable")
	}
}

func TestPoolFactsRequireExactChainPoolVersionAndProviderComposition(t *testing.T) {
	pair := marketdata.Pair{ChainID: "bsc", PairAddress: testPool, BaseToken: marketdata.Token{Address: testToken}, QuoteToken: marketdata.Token{Address: testQuote}}
	facts := ReadPoolFacts(context.Background(), fakePoolReader{token0: testToken, token1: testQuote, factory: Deployments("bsc")[0].Factory}, "bsc", pair)
	for _, change := range []func(*PoolFacts){
		func(f *PoolFacts) { f.Version++ },
		func(f *PoolFacts) { f.ChainKey = "base" },
		func(f *PoolFacts) { f.ChainID = 8453 },
		func(f *PoolFacts) { f.PoolAddress = testToken },
		func(f *PoolFacts) { f.FactoryAddress = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" },
	} {
		bad := facts
		change(&bad)
		if ClassifyPairWithFacts("bsc", testToken, pair, bad).Supported {
			t.Fatal("wrong identity/version/whitelist reused")
		}
	}
	foreign := pair
	foreign.ChainID = "base"
	if ClassifyPairWithFacts("base", testToken, foreign, facts).Supported {
		t.Fatal("same address crossed chains")
	}
	conflict := pair
	conflict.QuoteToken.Address = "0x4444444444444444444444444444444444444444"
	if ClassifyPairWithFacts("bsc", testToken, conflict, facts).Supported {
		t.Fatal("provider composition conflict borrowed cached proof")
	}
	facts = ReadPoolFacts(context.Background(), fakePoolReader{err: errors.New("unavailable")}, "bsc", pair)
	if !facts.ReadFailed || ClassifyPairWithFacts("bsc", testToken, pair, facts).Supported {
		t.Fatal("failed read became supported")
	}
}

type failingFactoryReader struct{ fakePoolReader }

func (f failingFactoryReader) PoolFactory(context.Context, string, string, string) (string, error) {
	return "", errors.New("temporary factory read error")
}

func TestPoolFactsReadFailureDoesNotReplaceUnsupportedFactoryOrFourMemeClassification(t *testing.T) {
	pair := marketdata.Pair{ChainID: "bsc", PairAddress: testPool, BaseToken: marketdata.Token{Address: testToken}, QuoteToken: marketdata.Token{Address: testQuote}}
	facts := ReadPoolFacts(context.Background(), failingFactoryReader{fakePoolReader{token0: testToken, token1: testQuote}}, "bsc", pair)
	if !facts.ReadFailed || facts.Token0Address != testToken {
		t.Fatal("factory transport failure was not identified independently")
	}
	facts = ReadPoolFacts(context.Background(), fakePoolReader{token0: testToken, token1: testQuote, factory: "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, "bsc", pair)
	if facts.ReadFailed || ClassifyPairWithFacts("bsc", testToken, pair, facts).Supported {
		t.Fatal("legitimate unsupported factory was treated as RPC failure/full support")
	}
	pair.PairAddress = testToken + ":4meme"
	if ReadPoolFacts(context.Background(), nil, "bsc", pair).ReadFailed {
		t.Fatal("non-address Fourmeme quote key became a transport failure")
	}
}
