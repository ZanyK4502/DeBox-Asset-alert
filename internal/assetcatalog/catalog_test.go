package assetcatalog

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
)

type fakePrimary struct {
	candidates []Candidate
	candidate  *Candidate
	err        error
	resolve    func(string, string) (*Candidate, error)
}

func (f fakePrimary) Search(
	context.Context,
	string,
	int,
) ([]Candidate, error) {
	return append([]Candidate(nil), f.candidates...), f.err
}

func (f fakePrimary) ResolveContract(
	_ context.Context,
	chainKey string,
	contract string,
) (*Candidate, error) {
	if f.resolve != nil {
		return f.resolve(chainKey, contract)
	}
	if f.candidate == nil {
		return nil, f.err
	}
	copy := *f.candidate
	return &copy, f.err
}

type fakeTokenMetadata struct {
	values map[string]chain.TokenMetadata
	err    error
	calls  *atomic.Int64
}

func (f fakeTokenMetadata) TokenMetadata(
	_ context.Context,
	address string,
	chainKey string,
	_ string,
) (chain.TokenMetadata, error) {
	if f.calls != nil {
		f.calls.Add(1)
	}
	if f.err != nil {
		return chain.TokenMetadata{}, f.err
	}
	value, ok := f.values[chainKey+":"+address]
	if !ok {
		return chain.TokenMetadata{}, errors.New("not found")
	}
	return value, nil
}

type fakePoolDiscovery struct {
	values map[string][]marketdata.Pair
	err    error
	calls  *atomic.Int64
}

func (f fakePoolDiscovery) DiscoverPools(
	_ context.Context,
	chainKey string,
	address string,
) ([]marketdata.Pair, error) {
	if f.calls != nil {
		f.calls.Add(1)
	}
	if f.err != nil {
		return nil, f.err
	}
	return append(
		[]marketdata.Pair(nil),
		f.values[chainKey+":"+address]...,
	), nil
}

type fakePairSearcher struct {
	pairs []marketdata.Pair
	err   error
}

func (f fakePairSearcher) SearchPairs(
	context.Context,
	string,
) ([]marketdata.Pair, error) {
	return append([]marketdata.Pair(nil), f.pairs...), f.err
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestCatalogUsesSingleChainDexScreenerFallback(t *testing.T) {
	catalog, err := NewCatalog(
		fakePrimary{err: ErrUnavailable},
		fakePairSearcher{pairs: []marketdata.Pair{
			{
				ChainID: "bsc",
				BaseToken: marketdata.Token{
					Address: testAddress(1), Name: "Project Coin", Symbol: "PRJ",
				},
				QuoteToken: marketdata.Token{
					Address: testAddress(2), Name: "Wrapped BNB", Symbol: "WBNB",
				},
				Liquidity: marketdata.Liquidity{USD: "25000"},
			},
			{
				ChainID: "ethereum",
				BaseToken: marketdata.Token{
					Address: testAddress(3), Name: "Project Coin", Symbol: "PRJ",
				},
				QuoteToken: marketdata.Token{
					Address: testAddress(4), Name: "Tether", Symbol: "USDT",
				},
				Liquidity: marketdata.Liquidity{USD: "10000"},
			},
		}},
		nil,
	)
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	result, err := catalog.Search(context.Background(), "PRJ", 10)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if !result.Degraded || result.Source != SourceDexScreener ||
		len(result.Candidates) != 2 {
		t.Fatalf("fallback result = %#v", result)
	}
	for _, candidate := range result.Candidates {
		if candidate.IdentityStatus != IdentitySingleChain ||
			len(candidate.Deployments) != 1 {
			t.Fatalf("fallback candidate can be merged: %#v", candidate)
		}
	}
	if result.Candidates[0].Deployments[0].ChainKey != "bsc" {
		t.Fatalf("liquidity order = %#v", result.Candidates)
	}
}

func TestCatalogDoesNotHideTwoProviderFailures(t *testing.T) {
	catalog, err := NewCatalog(
		fakePrimary{err: ErrUnavailable},
		fakePairSearcher{err: errors.New("rate limited")},
		nil,
	)
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	if _, err := catalog.Search(
		context.Background(), "coin", 10,
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Search() error = %v", err)
	}
}

func TestLogoProxyValidatesSourceTypeAndCaches(t *testing.T) {
	var calls atomic.Int64
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	}
	catalog, err := NewCatalog(
		fakePrimary{},
		nil,
		doerFunc(func(request *http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/octet-stream"},
				},
				Body: io.NopCloser(bytes.NewReader(png)),
			}, nil
		}),
	)
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	source := "https://coin-images.coingecko.com/coins/project.png"
	first, err := catalog.Logo(context.Background(), source)
	if err != nil {
		t.Fatalf("Logo() error = %v", err)
	}
	second, err := catalog.Logo(context.Background(), source)
	if err != nil {
		t.Fatalf("cached Logo() error = %v", err)
	}
	if first.ContentType != "image/png" ||
		first.ETag == "" ||
		!bytes.Equal(first.Body, second.Body) ||
		calls.Load() != 1 {
		t.Fatalf(
			"logos/calls = %#v/%#v/%d", first, second, calls.Load(),
		)
	}
	if _, err := catalog.Logo(
		context.Background(),
		"https://127.0.0.1/private.png",
	); !errors.Is(err, ErrInvalidLogo) {
		t.Fatalf("private logo error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("invalid source reached upstream")
	}
}

func TestCatalogReturnsLocalLogoURLOnly(t *testing.T) {
	catalog, err := NewCatalog(
		fakePrimary{candidates: []Candidate{{
			CanonicalAssetID: "project",
			Name:             "Project",
			Symbol:           "PRJ",
			IdentitySource:   SourceCoinGecko,
			remoteLogoURL:    "https://coin-images.coingecko.com/project.png",
		}}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	result, err := catalog.Search(context.Background(), "project", 1)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Candidates) != 1 ||
		result.Candidates[0].LogoURL !=
			"/api/market/assets/logo?source=https%3A%2F%2Fcoin-images.coingecko.com%2Fproject.png" {
		t.Fatalf("candidate = %#v", result.Candidates)
	}
}

func TestManualResolveAllowsOnlySharedCoinGeckoIdentityToMerge(t *testing.T) {
	bscAddress := testAddress(101)
	baseAddress := testAddress(102)
	verified := Candidate{
		CanonicalAssetID: "project-token",
		Name:             "Project Token",
		Symbol:           "PRJ",
		IdentitySource:   SourceCoinGecko,
		IdentityStatus:   IdentityVerified,
		Deployments: []Deployment{
			{
				ChainKey: "bsc", ChainID: 56, ChainName: "BNB Chain",
				PlatformID:      "binance-smart-chain",
				ContractAddress: bscAddress,
			},
			{
				ChainKey: "base", ChainID: 8453, ChainName: "Base",
				PlatformID: "base", ContractAddress: baseAddress,
			},
		},
	}
	catalog, err := NewCatalog(
		fakePrimary{resolve: func(
			_ string,
			_ string,
		) (*Candidate, error) {
			copy := verified
			return &copy, nil
		}},
		nil,
		nil,
		WithManualProviders(
			fakeTokenMetadata{values: map[string]chain.TokenMetadata{
				"bsc:" + bscAddress:   tokenMetadata(bscAddress),
				"base:" + baseAddress: tokenMetadata(baseAddress),
			}},
			fakePoolDiscovery{values: map[string][]marketdata.Pair{
				"bsc:" + bscAddress: {
					manualPair("bsc", bscAddress, "1000"),
				},
				"base:" + baseAddress: {
					manualPair("base", baseAddress, "2000"),
				},
			}},
		),
	)
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	result, err := catalog.ResolveManualContracts(
		context.Background(),
		ManualResolveInput{Contracts: []ManualContractInput{
			{ChainKey: "bsc", ContractAddress: bscAddress},
			{ChainKey: "base", ContractAddress: baseAddress},
		}},
	)
	if err != nil {
		t.Fatalf("ResolveManualContracts() error = %v", err)
	}
	if !result.CanMerge ||
		result.MergeStatus != MergeStatusVerified ||
		len(result.Contracts) != 2 ||
		len(result.Candidates) != 1 {
		t.Fatalf("manual result = %#v", result)
	}
	for _, contract := range result.Contracts {
		if contract.CanonicalAssetID != "project-token" ||
			contract.IdentitySource != SourceCoinGecko ||
			contract.IdentityLookupStatus != LookupMatched ||
			contract.MarketLookupStatus != MarketAvailable ||
			contract.DiscoveredPoolCount != 1 {
			t.Fatalf("manual contract = %#v", contract)
		}
	}
}

func TestManualResolveNeverMergesMatchingLongTailNames(t *testing.T) {
	bscAddress := testAddress(201)
	baseAddress := testAddress(202)
	catalog, err := NewCatalog(
		fakePrimary{err: ErrNotFound},
		nil,
		nil,
		WithManualProviders(
			fakeTokenMetadata{values: map[string]chain.TokenMetadata{
				"bsc:" + bscAddress:   tokenMetadata(bscAddress),
				"base:" + baseAddress: tokenMetadata(baseAddress),
			}},
			fakePoolDiscovery{},
		),
	)
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	result, err := catalog.ResolveManualContracts(
		context.Background(),
		ManualResolveInput{Contracts: []ManualContractInput{
			{ChainKey: "bsc", ContractAddress: bscAddress},
			{ChainKey: "base", ContractAddress: baseAddress},
		}},
	)
	if err != nil {
		t.Fatalf("ResolveManualContracts() error = %v", err)
	}
	if result.CanMerge ||
		result.MergeStatus != MergeStatusSeparateProjects ||
		len(result.Candidates) != 2 {
		t.Fatalf("long-tail contracts were merged: %#v", result)
	}
	for _, contract := range result.Contracts {
		if contract.TokenName != "Project Token" ||
			contract.TokenSymbol != "PRJ" ||
			contract.IdentitySource != SourceOnChain ||
			contract.IdentityLookupStatus != LookupNotListed ||
			contract.MarketLookupStatus != MarketEmpty {
			t.Fatalf("long-tail contract = %#v", contract)
		}
	}
}

func TestManualResolveSingleContractUsesOnChainMetadataAndPoolLogo(t *testing.T) {
	address := testAddress(301)
	pair := manualPair("polygon", address, "5000")
	pair.Info = []byte(
		`{"imageUrl":"https://cdn.dexscreener.com/project.png"}`,
	)
	catalog, err := NewCatalog(
		fakePrimary{err: ErrNotFound},
		nil,
		nil,
		WithManualProviders(
			fakeTokenMetadata{values: map[string]chain.TokenMetadata{
				"polygon:" + address: tokenMetadata(address),
			}},
			fakePoolDiscovery{values: map[string][]marketdata.Pair{
				"polygon:" + address: {pair},
			}},
		),
	)
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	result, err := catalog.ResolveManualContracts(
		context.Background(),
		ManualResolveInput{Contracts: []ManualContractInput{{
			ChainKey: "polygon", ContractAddress: address,
		}}},
	)
	if err != nil {
		t.Fatalf("ResolveManualContracts() error = %v", err)
	}
	if !result.CanMerge ||
		result.MergeStatus != MergeStatusSingleChain ||
		len(result.Candidates) != 1 ||
		result.Candidates[0].LogoURL !=
			"/api/market/assets/logo?source=https%3A%2F%2Fcdn.dexscreener.com%2Fproject.png" {
		t.Fatalf("single-chain result = %#v", result)
	}
}

func TestManualResolveValidatesEntireBatchBeforeProviderCalls(t *testing.T) {
	var metadataCalls atomic.Int64
	var poolCalls atomic.Int64
	catalog, err := NewCatalog(
		fakePrimary{},
		nil,
		nil,
		WithManualProviders(
			fakeTokenMetadata{calls: &metadataCalls},
			fakePoolDiscovery{calls: &poolCalls},
		),
	)
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	_, err = catalog.ResolveManualContracts(
		context.Background(),
		ManualResolveInput{Contracts: []ManualContractInput{
			{ChainKey: "base", ContractAddress: testAddress(1)},
			{ChainKey: "base", ContractAddress: testAddress(2)},
		}},
	)
	if !errors.Is(err, ErrInvalidManualRequest) {
		t.Fatalf("duplicate chain error = %v", err)
	}
	if metadataCalls.Load() != 0 || poolCalls.Load() != 0 {
		t.Fatalf(
			"providers called for invalid batch: %d/%d",
			metadataCalls.Load(),
			poolCalls.Load(),
		)
	}
}

func TestManualResolveRejectsUnreadableContract(t *testing.T) {
	catalog, err := NewCatalog(
		fakePrimary{},
		nil,
		nil,
		WithManualProviders(
			fakeTokenMetadata{err: errors.New("execution reverted")},
			fakePoolDiscovery{},
		),
	)
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	_, err = catalog.ResolveManualContracts(
		context.Background(),
		ManualResolveInput{Contracts: []ManualContractInput{{
			ChainKey: "optimism", ContractAddress: testAddress(1),
		}}},
	)
	if !errors.Is(err, ErrContractUnreadable) ||
		strings.Contains(err.Error(), "execution reverted") {
		t.Fatalf("unreadable contract error = %v", err)
	}
}

func TestManualResolveKeepsValidContractWhenMarketLookupFails(t *testing.T) {
	address := testAddress(401)
	catalog, err := NewCatalog(
		fakePrimary{err: ErrUnavailable},
		nil,
		nil,
		WithManualProviders(
			fakeTokenMetadata{values: map[string]chain.TokenMetadata{
				"arbitrum:" + address: tokenMetadata(address),
			}},
			fakePoolDiscovery{err: errors.New("rate limited")},
		),
	)
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	result, err := catalog.ResolveManualContracts(
		context.Background(),
		ManualResolveInput{Contracts: []ManualContractInput{{
			ChainKey: "arbitrum", ContractAddress: address,
		}}},
	)
	if err != nil {
		t.Fatalf("ResolveManualContracts() error = %v", err)
	}
	if len(result.Contracts) != 1 ||
		result.Contracts[0].IdentityLookupStatus != LookupUnavailable ||
		result.Contracts[0].MarketLookupStatus != MarketUnavailable ||
		result.Contracts[0].DiscoveredPoolCount != 0 {
		t.Fatalf("degraded manual result = %#v", result)
	}
}

func tokenMetadata(address string) chain.TokenMetadata {
	return chain.TokenMetadata{
		Address: address,
		Name:    "Project Token", Symbol: "PRJ", Decimals: 18,
		TotalSupplyRaw: "1000000000000000000000000",
	}
}

func manualPair(
	chainKey string,
	address string,
	liquidity string,
) marketdata.Pair {
	return marketdata.Pair{
		ChainID: chainKey,
		BaseToken: marketdata.Token{
			Address: address, Name: "Project Token", Symbol: "PRJ",
		},
		QuoteToken: marketdata.Token{
			Address: testAddress(999), Name: "USD", Symbol: "USD",
		},
		Liquidity: marketdata.Liquidity{USD: marketdata.Decimal(liquidity)},
	}
}
