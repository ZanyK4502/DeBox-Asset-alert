package marketview

import (
	"context"
	"errors"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/subscription"
)

const (
	testToken = "0x1111111111111111111111111111111111111111"
	testPairA = "0x2222222222222222222222222222222222222222"
	testPairB = "0x3333333333333333333333333333333333333333"
	testQuote = "0x4444444444444444444444444444444444444444"
)

type fakeEntitlements struct {
	plan plans.Plan
	err  error
}

func (f fakeEntitlements) ActivePlan(context.Context, string) (plans.Plan, error) {
	return f.plan, f.err
}
func (f fakeEntitlements) Entitlement(context.Context, string) (subscription.Entitlement, error) {
	return subscription.Entitlement{Plan: f.plan}, f.err
}
func (fakeEntitlements) CreateMarketProject(context.Context, store.CreateMarketProjectParams) (store.MarketProject, error) {
	return store.MarketProject{}, errors.New("not used")
}
func (fakeEntitlements) RestoreMarketProject(context.Context, string, int64) (store.MarketProject, error) {
	return store.MarketProject{}, errors.New("not used")
}
func (fakeEntitlements) LinkMarketProjectPool(context.Context, store.LinkMarketProjectPoolParams) (store.MarketProjectPool, error) {
	return store.MarketProjectPool{}, errors.New("not used")
}
func (fakeEntitlements) CreateMarketRule(context.Context, store.CreateMarketRuleParams) (store.MarketRule, error) {
	return store.MarketRule{}, errors.New("not used")
}
func (fakeEntitlements) RestoreMarketRule(context.Context, string, int64) (store.MarketRule, error) {
	return store.MarketRule{}, errors.New("not used")
}
func (fakeEntitlements) CreateMarketCombination(context.Context, store.CreateMarketCombinationParams) (store.MarketCombinationRule, error) {
	return store.MarketCombinationRule{}, errors.New("not used")
}

type fakeChain struct {
	metadata chain.TokenMetadata
	err      error
}

func (f fakeChain) TokenMetadata(context.Context, string, string, string) (chain.TokenMetadata, error) {
	return f.metadata, f.err
}
func (fakeChain) PoolTokens(context.Context, string, string, string) (string, string, error) {
	return "", "", errors.New("not used")
}

type fakeMarket struct {
	pairs []marketdata.Pair
	err   error
}

func (f fakeMarket) DiscoverPools(context.Context, string, string) ([]marketdata.Pair, error) {
	return f.pairs, f.err
}
func (fakeMarket) PairsByTokens(context.Context, string, []string) ([]marketdata.Pair, error) {
	return nil, errors.New("not used")
}
func (fakeMarket) PairsByAddresses(context.Context, string, []string) ([]marketdata.Pair, error) {
	return nil, errors.New("not used")
}

func TestQueryTokenSortsSupportedPancakePoolsFirst(t *testing.T) {
	catalog, err := plans.NewCatalog("10", 30, "USDT")
	if err != nil {
		t.Fatal(err)
	}
	free, _ := catalog.Get(plans.Free)
	service := New(Dependencies{
		Entitlements: fakeEntitlements{plan: free},
		Chain: fakeChain{metadata: chain.TokenMetadata{
			Address: testToken, Name: "Project", Symbol: "PRJ", Decimals: 18,
		}},
		Market: fakeMarket{pairs: []marketdata.Pair{
			pair(testPairA, "otherdex", nil, "100000"),
			pair(testPairB, "pancakeswap", []string{"v3"}, "50000"),
		}},
	})
	result, err := service.QueryToken(context.Background(), "user", TokenQueryInput{
		ChainKey: "bsc", TokenAddress: testToken,
	})
	if err != nil {
		t.Fatalf("QueryToken() error = %v", err)
	}
	if len(result.Pools) != 2 || !result.Pools[0].Supported ||
		result.Pools[0].ParserAdapter != "evm_v3" ||
		result.Pools[1].Supported {
		t.Fatalf("unexpected pool order: %#v", result.Pools)
	}
}

func TestQueryTokenRejectsUnsupportedChain(t *testing.T) {
	service := New(Dependencies{
		Entitlements: fakeEntitlements{plan: plans.Plan{MarketQuery: true}},
		Chain:        fakeChain{},
		Market:       fakeMarket{},
	})
	_, err := service.QueryToken(context.Background(), "user", TokenQueryInput{
		ChainKey: "ethereum", TokenAddress: testToken,
	})
	if err == nil {
		t.Fatal("QueryToken() accepted unsupported chain")
	}
}

func TestClassifyPairUsesDexIDAndDoesNotTreatInfinityAsV2(t *testing.T) {
	tests := []struct {
		name      string
		dexID     string
		labels    []string
		want      string
		supported bool
	}{
		{
			name: "v3 in dex id", dexID: "pancakeswap-v3",
			want: marketparse.AdapterV3, supported: true,
		},
		{
			name: "infinity in dex id", dexID: "pancakeswap-infinity-clmm",
			want: "", supported: false,
		},
		{
			name: "classic v2", dexID: "pancakeswap",
			want: marketparse.AdapterV2, supported: true,
		},
		{
			name: "legacy v1 label is not v2", dexID: "pancakeswap",
			labels: []string{"v1"}, want: "", supported: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, adapter, supported := classifyPair(marketdata.Pair{
				DexID: test.dexID, Labels: test.labels,
			})
			if adapter != test.want || supported != test.supported {
				t.Fatalf(
					"classifyPair() = %q/%v, want %q/%v",
					adapter,
					supported,
					test.want,
					test.supported,
				)
			}
		})
	}
}

func TestCatalogMarksStandardAndProfessionalRules(t *testing.T) {
	catalog, _ := plans.NewCatalog("10", 30, "USDT")
	standard, _ := catalog.Get(plans.Standard)
	service := New(Dependencies{Entitlements: fakeEntitlements{plan: standard}})
	result, err := service.Catalog(context.Background(), "user")
	if err != nil {
		t.Fatal(err)
	}
	byCode := make(map[string]RuleDefinition)
	for _, definition := range result.Rules {
		byCode[definition.Code] = definition
	}
	if !byCode[plans.MarketPriceAbove].Allowed ||
		byCode[plans.MarketPriceAbove].Professional {
		t.Fatalf("standard price definition = %#v", byCode[plans.MarketPriceAbove])
	}
	if byCode[plans.MarketLargeBuy].Allowed ||
		!byCode[plans.MarketLargeBuy].Professional {
		t.Fatalf("professional large-buy definition = %#v", byCode[plans.MarketLargeBuy])
	}
}

func pair(
	address string,
	dex string,
	labels []string,
	liquidity string,
) marketdata.Pair {
	return marketdata.Pair{
		ChainID:     "bsc",
		DexID:       dex,
		PairAddress: address,
		Labels:      labels,
		BaseToken:   marketdata.Token{Address: testToken, Name: "Project", Symbol: "PRJ"},
		QuoteToken:  marketdata.Token{Address: testQuote, Name: "Quote", Symbol: "USDT"},
		Liquidity:   marketdata.Liquidity{USD: marketdata.Decimal(liquidity)},
	}
}
