package marketview

import (
	"context"
	"errors"
	"strings"
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
	plan                     plans.Plan
	err                      error
	createdRuleParams        *store.CreateMarketRuleParams
	createdCombinationParams *store.CreateMarketCombinationParams
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
func (f fakeEntitlements) CreateMarketRule(_ context.Context, params store.CreateMarketRuleParams) (store.MarketRule, error) {
	if f.createdRuleParams != nil {
		*f.createdRuleParams = params
		return store.MarketRule{RuleType: params.RuleType, RepeatWhileActive: params.RepeatWhileActive}, nil
	}
	return store.MarketRule{}, errors.New("not used")
}
func (fakeEntitlements) RestoreMarketRule(context.Context, string, int64) (store.MarketRule, error) {
	return store.MarketRule{}, errors.New("not used")
}
func (f fakeEntitlements) CreateMarketCombination(_ context.Context, params store.CreateMarketCombinationParams) (store.MarketCombinationRule, error) {
	if f.createdCombinationParams != nil {
		*f.createdCombinationParams = params
		return store.MarketCombinationRule{Note: params.Note}, nil
	}
	return store.MarketCombinationRule{}, errors.New("not used")
}

func TestCreateCombinationRequiresAndTrimsNote(t *testing.T) {
	t.Parallel()

	var captured store.CreateMarketCombinationParams
	service := New(Dependencies{Entitlements: fakeEntitlements{
		createdCombinationParams: &captured,
	}})
	firstRuleID, secondRuleID := int64(11), int64(12)
	input := CreateCombinationInput{
		Note:         "   ",
		CycleMinutes: 15,
		Members: []CreateCombinationMemberInput{
			{SourceType: "market", MarketRuleID: &firstRuleID, RequiredTriggerCount: 1},
			{SourceType: "market", MarketRuleID: &secondRuleID, RequiredTriggerCount: 1},
		},
	}
	if _, err := service.CreateCombination(
		context.Background(), "user", input,
	); err == nil || !strings.Contains(err.Error(), "组合备注不能为空") {
		t.Fatalf("blank combination note error = %v", err)
	}
	input.Note = "  放量上涨组合  "
	combination, err := service.CreateCombination(
		context.Background(), "user", input,
	)
	if err != nil {
		t.Fatalf("CreateCombination() error = %v", err)
	}
	if captured.Note != "放量上涨组合" || combination.Note != captured.Note {
		t.Fatalf("combination note = %q, captured = %q", combination.Note, captured.Note)
	}
}

type fakeChain struct {
	metadata  chain.TokenMetadata
	err       error
	token0    string
	token1    string
	factory   string
	factories map[string]string
}

func (f fakeChain) TokenMetadata(context.Context, string, string, string) (chain.TokenMetadata, error) {
	return f.metadata, f.err
}
func (f fakeChain) PoolTokens(context.Context, string, string, string) (string, string, error) {
	token0, token1 := f.token0, f.token1
	if token0 == "" {
		token0 = testToken
	}
	if token1 == "" {
		token1 = testQuote
	}
	return token0, token1, nil
}
func (f fakeChain) PoolFactory(_ context.Context, pool, _, _ string) (string, error) {
	if factory := f.factories[pool]; factory != "" {
		return factory, nil
	}
	if f.factory != "" {
		return f.factory, nil
	}
	return marketparse.BSCPancakeV3Factory, nil
}

type fakeMarket struct {
	pairs        []marketdata.Pair
	pairsByChain map[string][]marketdata.Pair
	err          error
}

func (f fakeMarket) DiscoverPools(_ context.Context, chainKey, _ string) ([]marketdata.Pair, error) {
	if f.pairsByChain != nil {
		return f.pairsByChain[chainKey], f.err
	}
	return f.pairs, f.err
}
func (fakeMarket) PairsByTokens(context.Context, string, []string) ([]marketdata.Pair, error) {
	return nil, errors.New("not used")
}
func (f fakeMarket) PairsByAddresses(_ context.Context, chainKey string, _ []string) ([]marketdata.Pair, error) {
	if f.pairsByChain != nil {
		return f.pairsByChain[chainKey], f.err
	}
	return f.pairs, f.err
}

func TestCreateRuleLimitsRepeatWhileActiveToSupportedRules(t *testing.T) {
	var captured store.CreateMarketRuleParams
	service := New(Dependencies{
		Entitlements: fakeEntitlements{createdRuleParams: &captured},
	})
	for _, test := range []struct {
		name     string
		ruleType string
		want     bool
	}{
		{name: "price increase", ruleType: plans.MarketPriceIncrease, want: true},
		{name: "price decrease", ruleType: plans.MarketPriceDecrease, want: true},
		{name: "liquidity decrease", ruleType: plans.MarketLiquidityDecrease, want: true},
		{name: "volume above", ruleType: plans.MarketVolumeAbove, want: true},
		{name: "volume spike", ruleType: plans.MarketVolumeSpike, want: true},
		{name: "trade imbalance", ruleType: plans.MarketTradeImbalance, want: true},
		{name: "price above", ruleType: plans.MarketPriceAbove, want: false},
		{name: "liquidity below", ruleType: plans.MarketLiquidityBelow, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			captured = store.CreateMarketRuleParams{}
			_, err := service.CreateRule(context.Background(), "user", 1, CreateRuleInput{
				RuleType:          test.ruleType,
				RepeatWhileActive: true,
			})
			if err != nil {
				t.Fatalf("create rule: %v", err)
			}
			if captured.RepeatWhileActive != test.want {
				t.Fatalf("repeat while active = %v, want %v", captured.RepeatWhileActive, test.want)
			}
		})
	}
}

func TestQueryTokenSortsSupportedPancakePoolsFirst(t *testing.T) {
	catalog, err := plans.NewCatalog("10", 30, "USDT")
	if err != nil {
		t.Fatal(err)
	}
	free, _ := catalog.Get(plans.Free)
	service := New(Dependencies{
		Entitlements: fakeEntitlements{plan: free},
		Chain: fakeChain{
			metadata: chain.TokenMetadata{
				Address: testToken, Name: "Project", Symbol: "PRJ", Decimals: 18,
			},
			factories: map[string]string{
				testPairA: "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
		},
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

func TestQueryTokenRejectsChainOutsideSupportedSix(t *testing.T) {
	service := New(Dependencies{
		Entitlements: fakeEntitlements{plan: plans.Plan{MarketQuery: true}},
		Chain:        fakeChain{},
		Market:       fakeMarket{},
	})
	_, err := service.QueryToken(context.Background(), "user", TokenQueryInput{
		ChainKey: "avalanche", TokenAddress: testToken,
	})
	if err == nil {
		t.Fatal("QueryToken() accepted unsupported chain")
	}
}

func TestPreviewRecommendationsUsesLatestSelectedPoolQuote(t *testing.T) {
	livePair := pair(testPairA, "pancakeswap", []string{"v3"}, "100000")
	livePair.PriceUSD = marketdata.Decimal("2")
	livePair.Volume = map[string]marketdata.Decimal{
		"h1": marketdata.Decimal("12000"),
	}
	service := New(Dependencies{
		Entitlements: fakeEntitlements{plan: plans.Plan{MarketQuery: true}},
		Market:       fakeMarket{pairs: []marketdata.Pair{livePair}},
	})
	recommendations, err := service.PreviewRecommendations(
		context.Background(),
		"user",
		RecommendationPreviewInput{Deployments: []RecommendationPreviewDeployment{{
			ChainKey:      "bsc",
			TokenAddress:  testToken,
			PoolAddresses: []string{testPairA},
		}}},
	)
	if err != nil {
		t.Fatalf("PreviewRecommendations() error = %v", err)
	}
	if len(recommendations) != 63 {
		t.Fatalf("recommendations = %d, want 63", len(recommendations))
	}
	var sensitive, stable string
	for _, recommendation := range recommendations {
		if recommendation.RuleType != plans.MarketPriceAbove {
			continue
		}
		switch recommendation.Sensitivity {
		case "sensitive":
			sensitive = recommendation.Threshold
		case "stable":
			stable = recommendation.Threshold
		}
	}
	if sensitive != "2.04" || stable != "2.2" {
		t.Fatalf("price recommendations = %q/%q, want 2.04/2.2", sensitive, stable)
	}
}

func TestQueryTokenMapsTemporaryProviderFailure(t *testing.T) {
	service := New(Dependencies{
		Entitlements: fakeEntitlements{plan: plans.Plan{MarketQuery: true}},
		Chain: fakeChain{metadata: chain.TokenMetadata{
			Address: testToken, Name: "Project", Symbol: "PRJ", Decimals: 18,
		}},
		Market: fakeMarket{err: &marketdata.DexScreenerHTTPError{
			StatusCode: 429,
			Body:       "provider diagnostics must stay private",
		}},
	})
	_, err := service.QueryToken(context.Background(), "user", TokenQueryInput{
		ChainKey: "bsc", TokenAddress: testToken,
	})
	if !errors.Is(err, ErrMarketDataUnavailable) {
		t.Fatalf("QueryToken() error = %v, want ErrMarketDataUnavailable", err)
	}
	if strings.Contains(err.Error(), "provider diagnostics") {
		t.Fatalf("provider details leaked through service error: %v", err)
	}
}

func TestQueryTokensGroupsSixChainResultsAndSeparatesQuoteOnly(t *testing.T) {
	service := New(Dependencies{
		Entitlements: fakeEntitlements{
			plan: plans.Plan{MarketQuery: true},
		},
		Chain: fakeChain{
			metadata: chain.TokenMetadata{
				Address:  testToken,
				Name:     "Project",
				Symbol:   "PRJ",
				Decimals: 18,
			},
			factories: map[string]string{
				testPairA: marketparse.BSCPancakeV3Factory,
				testPairB: "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
		},
		Market: fakeMarket{pairsByChain: map[string][]marketdata.Pair{
			"bsc": {
				pair(testPairA, "wrong-provider-label", nil, "100000"),
				pair(testPairB, "pancakeswap", []string{"v3"}, "50000"),
			},
			"base": {},
		}},
	})
	result, err := service.QueryTokens(
		context.Background(),
		"user",
		MultiTokenQueryInput{Deployments: []TokenQueryInput{
			{ChainKey: "base", TokenAddress: testToken},
			{ChainKey: "bsc", TokenAddress: testToken},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Groups) != 2 ||
		result.Groups[0].ChainKey != "bsc" ||
		len(result.Groups[0].FullMonitoring) != 1 ||
		len(result.Groups[0].QuoteOnly) != 1 ||
		result.Groups[0].FullMonitoring[0].Protocol != "pancakeswap" ||
		result.Groups[1].ChainKey != "base" {
		t.Fatalf("groups = %#v", result.Groups)
	}
}

func TestQueryTokensRejectsDuplicateChain(t *testing.T) {
	service := New(Dependencies{
		Entitlements: fakeEntitlements{
			plan: plans.Plan{MarketQuery: true},
		},
	})
	_, err := service.QueryTokens(
		context.Background(),
		"user",
		MultiTokenQueryInput{Deployments: []TokenQueryInput{
			{ChainKey: "bnb", TokenAddress: testToken},
			{ChainKey: "bsc", TokenAddress: testToken},
		}},
	)
	if err == nil || !strings.Contains(err.Error(), "不能重复") {
		t.Fatalf("QueryTokens() error = %v", err)
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
