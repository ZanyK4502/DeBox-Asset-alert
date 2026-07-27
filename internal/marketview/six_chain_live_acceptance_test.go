package marketview

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/assetcatalog"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketprotocol"
)

const (
	liveChainlinkAssetID = "chainlink"
	liveV2SwapTopic      = "0xd78ad95fa46c994b6551d0da85fc275fe613ce37657fb8d5e3d130840159d822"
	liveV3SwapTopic      = "0xc42079f94a6350d7e6235f29174924f928cc2ac818eb64fed8004e115fbcca67"
)

var liveChainlinkContracts = []assetcatalog.ManualContractInput{
	{ChainKey: "bsc", ContractAddress: "0xf8a0bf9cf54bb92f17374d9e9a321e6a111a51bd"},
	{ChainKey: "ethereum", ContractAddress: "0x514910771af9ca656af840dff83e8264ecf986ca"},
	{ChainKey: "base", ContractAddress: "0x88fb150bdc53a65fe94dea0c9ba0a6daf8c6e196"},
	{ChainKey: "polygon", ContractAddress: "0x53e0bca35ec356bd5dddfebbd1fc0fd03fabad39"},
	{ChainKey: "arbitrum", ContractAddress: "0xf97f4df75117a78c1a5a0dbb814af92458539fb4"},
	{ChainKey: "optimism", ContractAddress: "0x350a791bfc2c21f9ed5d10980dad2e2638ffa7f6"},
}

// TestLiveSixChainMarketAcceptance is an opt-in, read-only production-data
// acceptance test. It never creates a project, rule, webhook, or notification.
// CoinGecko is the identity authority, DexScreener supplies current pools and
// quotes, and Nodit verifies contracts, factories, and recent swap logs.
func TestLiveSixChainMarketAcceptance(t *testing.T) {
	if os.Getenv("RUN_LIVE_SIX_CHAIN_ACCEPTANCE") != "1" {
		t.Skip("set RUN_LIVE_SIX_CHAIN_ACCEPTANCE=1 for read-only six-chain validation")
	}
	coinGeckoKey := strings.TrimSpace(os.Getenv("COINGECKO_API_KEY"))
	noditKey := strings.TrimSpace(os.Getenv("NODIT_API_KEY"))
	if coinGeckoKey == "" || noditKey == "" {
		t.Fatal("COINGECKO_API_KEY and NODIT_API_KEY are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	coinGecko, err := assetcatalog.NewCoinGeckoClient(assetcatalog.CoinGeckoSettings{
		Tier:    strings.TrimSpace(os.Getenv("COINGECKO_API_TIER")),
		APIKey:  coinGeckoKey,
		BaseURL: strings.TrimSpace(os.Getenv("COINGECKO_BASE_URL")),
	})
	if err != nil {
		t.Fatalf("create CoinGecko client: %v", err)
	}
	dexScreener, err := marketdata.NewDexScreenerClient(
		strings.TrimSpace(os.Getenv("DEXSCREENER_BASE_URL")),
	)
	if err != nil {
		t.Fatalf("create DexScreener client: %v", err)
	}
	nodit, err := chain.NewClient(
		noditKey,
		strings.TrimSpace(os.Getenv("NODIT_BASE_URL")),
		chain.WithCURateLimit(300),
	)
	if err != nil {
		t.Fatalf("create Nodit client: %v", err)
	}
	catalog, err := assetcatalog.NewCatalog(
		coinGecko,
		dexScreener,
		nil,
		assetcatalog.WithManualProviders(nodit, dexScreener),
	)
	if err != nil {
		t.Fatalf("create asset catalog: %v", err)
	}

	candidate := acceptLiveAssetSearchAndLogo(ctx, t, catalog)
	acceptLiveManualAndStrictIdentity(ctx, t, catalog)
	acceptLiveSixChainPoolsAndSwaps(ctx, t, nodit, dexScreener)
	t.Logf(
		"accepted %s (%s) from %s across %d deployments",
		candidate.Name,
		candidate.Symbol,
		candidate.IdentitySource,
		len(candidate.Deployments),
	)
}

func acceptLiveAssetSearchAndLogo(
	ctx context.Context,
	t *testing.T,
	catalog *assetcatalog.Catalog,
) assetcatalog.Candidate {
	t.Helper()
	result, err := catalog.Search(ctx, "Chainlink", 10)
	if err != nil {
		t.Fatalf("search Chainlink: %v", err)
	}
	var candidate *assetcatalog.Candidate
	for index := range result.Candidates {
		if result.Candidates[index].CanonicalAssetID == liveChainlinkAssetID {
			candidate = &result.Candidates[index]
			break
		}
	}
	if candidate == nil {
		t.Fatalf("Chainlink missing from CoinGecko search: %#v", result.Candidates)
	}
	if result.Source != assetcatalog.SourceCoinGecko ||
		candidate.IdentitySource != assetcatalog.SourceCoinGecko ||
		candidate.IdentityStatus != assetcatalog.IdentityVerified {
		t.Fatalf("Chainlink is not authoritative: %#v", candidate)
	}
	assertExactLiveDeployments(t, candidate.Deployments)
	if strings.TrimSpace(candidate.LogoURL) == "" {
		t.Fatal("Chainlink search result has no proxied logo")
	}
	logoURL, err := url.Parse(candidate.LogoURL)
	if err != nil {
		t.Fatalf("parse proxied logo URL: %v", err)
	}
	source := strings.TrimSpace(logoURL.Query().Get("source"))
	if source == "" {
		t.Fatalf("proxied logo URL has no source: %s", candidate.LogoURL)
	}
	logo, err := catalog.Logo(ctx, source)
	if err != nil {
		t.Fatalf("load Chainlink logo through proxy: %v", err)
	}
	if len(logo.Body) == 0 || !strings.HasPrefix(logo.ContentType, "image/") {
		t.Fatalf("invalid Chainlink logo: type=%q bytes=%d", logo.ContentType, len(logo.Body))
	}
	return *candidate
}

func acceptLiveManualAndStrictIdentity(
	ctx context.Context,
	t *testing.T,
	catalog *assetcatalog.Catalog,
) {
	t.Helper()
	manual, err := catalog.ResolveManualContracts(ctx, assetcatalog.ManualResolveInput{
		Contracts: liveChainlinkContracts,
	})
	if err != nil {
		t.Fatalf("resolve six manual Chainlink contracts: %v", err)
	}
	if len(manual.Contracts) != len(liveChainlinkContracts) ||
		!manual.CanMerge ||
		manual.MergeStatus != assetcatalog.MergeStatusVerified {
		t.Fatalf("unexpected manual merge result: %#v", manual)
	}
	for _, contract := range manual.Contracts {
		if !strings.EqualFold(contract.TokenSymbol, "LINK") ||
			contract.IdentitySource != assetcatalog.SourceCoinGecko ||
			contract.IdentityLookupStatus != assetcatalog.LookupMatched ||
			strings.TrimSpace(contract.MarketLookupStatus) == "" {
			t.Fatalf("unexpected manual contract result: %#v", contract)
		}
	}

	verified, err := catalog.VerifyCrossChainIdentity(
		ctx,
		assetcatalog.CrossChainVerifyInput{
			CanonicalAssetID: liveChainlinkAssetID,
			Contracts:        liveChainlinkContracts,
		},
	)
	if err != nil {
		t.Fatalf("strict six-chain identity verification: %v", err)
	}
	if verified.VerificationStatus != assetcatalog.VerificationStatusVerified ||
		len(verified.Contracts) != len(liveChainlinkContracts) ||
		len(verified.Evidence) != len(liveChainlinkContracts) {
		t.Fatalf("unexpected strict identity result: %#v", verified)
	}

	mismatched := []assetcatalog.ManualContractInput{
		liveChainlinkContracts[0],
		{
			ChainKey:        "ethereum",
			ContractAddress: "0xc02aaa39b223fe8d0a0e5c4f27ead9083c756cc2",
		},
	}
	_, err = catalog.VerifyCrossChainIdentity(
		ctx,
		assetcatalog.CrossChainVerifyInput{
			CanonicalAssetID: liveChainlinkAssetID,
			Contracts:        mismatched,
		},
	)
	if !errors.Is(err, assetcatalog.ErrCrossChainIdentityConflict) &&
		!errors.Is(err, assetcatalog.ErrCrossChainIdentityUnverified) {
		t.Fatalf("mismatched LINK/WETH contracts were not rejected: %v", err)
	}
}

func acceptLiveSixChainPoolsAndSwaps(
	ctx context.Context,
	t *testing.T,
	nodit *chain.Client,
	dexScreener *marketdata.DexScreenerClient,
) {
	t.Helper()
	for _, deployment := range liveChainlinkContracts {
		profile, err := chain.ChainProfile(deployment.ChainKey, "")
		if err != nil {
			t.Fatalf("%s profile: %v", deployment.ChainKey, err)
		}
		pairs, err := dexScreener.DiscoverPools(
			ctx,
			deployment.ChainKey,
			deployment.ContractAddress,
		)
		if err != nil {
			t.Fatalf("%s discover pools: %v", deployment.ChainKey, err)
		}
		if len(pairs) == 0 {
			t.Fatalf("%s returned no Chainlink pools", deployment.ChainKey)
		}
		classified := classifyLivePairs(
			ctx,
			nodit,
			deployment,
			pairs,
		)
		if len(classified) == 0 {
			t.Fatalf("%s has no factory-verified supported Chainlink pool", deployment.ChainKey)
		}
		sort.SliceStable(classified, func(left, right int) bool {
			return livePairActivity(classified[left].pair) >
				livePairActivity(classified[right].pair)
		})

		var accepted *liveClassifiedPair
		var parsed marketparse.Event
		for index := range classified {
			event, found, parseErr := findRecentLiveSwap(
				ctx,
				nodit,
				profile,
				deployment.ContractAddress,
				classified[index],
			)
			if parseErr != nil {
				t.Fatalf(
					"%s parse live %s/%s pool %s: %v",
					deployment.ChainKey,
					classified[index].classification.Protocol,
					classified[index].classification.ProtocolVersion,
					classified[index].pair.PairAddress,
					parseErr,
				)
			}
			if found {
				accepted = &classified[index]
				parsed = event
				break
			}
		}
		if accepted == nil {
			t.Fatalf(
				"%s supported Chainlink pools had no recent parsable swap",
				deployment.ChainKey,
			)
		}
		if !accepted.pair.PriceUSD.Valid() ||
			!accepted.pair.Liquidity.USD.Valid() ||
			(parsed.Type != marketparse.EventBuy &&
				parsed.Type != marketparse.EventSell) ||
			!strings.EqualFold(parsed.TokenAddress, deployment.ContractAddress) {
			t.Fatalf(
				"%s invalid live market acceptance: pair=%#v event=%#v",
				deployment.ChainKey,
				accepted.pair,
				parsed,
			)
		}
		t.Logf(
			"%s accepted %s %s pool=%s price_usd=%s liquidity_usd=%s tx=%s event=%s",
			deployment.ChainKey,
			accepted.classification.Protocol,
			accepted.classification.ProtocolVersion,
			accepted.pair.PairAddress,
			accepted.pair.PriceUSD,
			accepted.pair.Liquidity.USD,
			parsed.TransactionHash,
			parsed.Type,
		)
	}
}

type liveClassifiedPair struct {
	pair           marketdata.Pair
	classification marketprotocol.Classification
}

func classifyLivePairs(
	ctx context.Context,
	nodit *chain.Client,
	deployment assetcatalog.ManualContractInput,
	pairs []marketdata.Pair,
) []liveClassifiedPair {
	const maxCandidates = 15
	result := make([]liveClassifiedPair, 0)
	for index, pair := range pairs {
		if index >= maxCandidates {
			break
		}
		classification := marketprotocol.VerifyPair(
			ctx,
			nodit,
			deployment.ChainKey,
			deployment.ContractAddress,
			pair,
		)
		if !classification.Supported {
			continue
		}
		result = append(result, liveClassifiedPair{
			pair:           pair,
			classification: classification,
		})
	}
	return result
}

func findRecentLiveSwap(
	ctx context.Context,
	nodit *chain.Client,
	profile chain.Profile,
	targetToken string,
	selected liveClassifiedPair,
) (marketparse.Event, bool, error) {
	token0, err := nodit.TokenMetadata(
		ctx,
		selected.classification.Token0Address,
		profile.Key,
		profile.Key,
	)
	if err != nil {
		return marketparse.Event{}, false, fmt.Errorf("token0 metadata: %w", err)
	}
	token1, err := nodit.TokenMetadata(
		ctx,
		selected.classification.Token1Address,
		profile.Key,
		profile.Key,
	)
	if err != nil {
		return marketparse.Event{}, false, fmt.Errorf("token1 metadata: %w", err)
	}
	parser, err := marketparse.NewParser(
		[]marketparse.Pool{{
			ChainID:    uint64(profile.ChainID),
			Protocol:   selected.classification.Protocol,
			Version:    selected.classification.ProtocolVersion,
			Adapter:    selected.classification.ParserAdapter,
			PoolKey:    selected.pair.PairAddress,
			LogAddress: selected.pair.PairAddress,
			Token0:     liveParserToken(token0),
			Token1:     liveParserToken(token1),
		}},
		nil,
	)
	if err != nil {
		return marketparse.Event{}, false, err
	}
	topics := liveSwapTopics(selected.classification.ParserAdapter)
	if len(topics) == 0 {
		return marketparse.Event{}, false, nil
	}
	latest, err := nodit.LatestBlockNumber(ctx, profile.Key, profile.Key)
	if err != nil {
		return marketparse.Event{}, false, fmt.Errorf("latest block: %w", err)
	}

	const (
		blockBatch  = uint64(500)
		maxLookback = uint64(10_000)
	)
	for offset := uint64(0); offset < maxLookback && offset < latest; offset += blockBatch {
		toBlock := latest - offset
		fromBlock := uint64(0)
		if toBlock >= blockBatch-1 {
			fromBlock = toBlock - blockBatch + 1
		}
		logs, err := nodit.Logs(ctx, profile.Key, profile.Key, chain.LogFilter{
			FromBlock: &fromBlock,
			ToBlock:   &toBlock,
			Addresses: []string{selected.pair.PairAddress},
			Topics:    []chain.LogTopic{chain.LogTopic(topics)},
		})
		if err != nil {
			return marketparse.Event{}, false, fmt.Errorf(
				"logs %d-%d: %w",
				fromBlock,
				toBlock,
				err,
			)
		}
		for index := len(logs) - 1; index >= 0; index-- {
			log, err := marketparse.LogFromRPC(logs[index])
			if err != nil {
				return marketparse.Event{}, false, err
			}
			events, err := parser.Parse(
				marketparse.Receipt{
					ChainID:          uint64(profile.ChainID),
					TransactionHash:  log.TransactionHash,
					BlockNumber:      log.BlockNumber,
					BlockHash:        log.BlockHash,
					TransactionIndex: log.TransactionIndex,
					Logs:             []marketparse.Log{log},
				},
				[]string{targetToken},
			)
			if err != nil {
				return marketparse.Event{}, false, err
			}
			for _, event := range events {
				if (event.Type == marketparse.EventBuy ||
					event.Type == marketparse.EventSell) &&
					strings.EqualFold(event.TokenAddress, targetToken) {
					return event, true, nil
				}
			}
		}
	}
	return marketparse.Event{}, false, nil
}

func liveParserToken(value chain.TokenMetadata) marketparse.Token {
	return marketparse.Token{
		Address:  value.Address,
		Symbol:   value.Symbol,
		Decimals: uint8(value.Decimals),
	}
}

func liveSwapTopics(adapter string) []string {
	switch adapter {
	case marketparse.AdapterV2:
		return []string{liveV2SwapTopic}
	case marketparse.AdapterV3:
		return []string{
			liveV3SwapTopic,
			marketparse.Topic(
				"Swap(address,address,int256,int256,uint160,uint128,int24,uint128,uint128)",
			),
		}
	case marketparse.AdapterAlgebra:
		return []string{
			liveV3SwapTopic,
			marketparse.Topic(
				"Swap(address,address,int256,int256,uint160,uint128,int24,uint24,uint24)",
			),
		}
	case marketparse.AdapterSolidly:
		return []string{
			marketparse.Topic(
				"Swap(address,address,uint256,uint256,uint256,uint256)",
			),
		}
	default:
		return nil
	}
}

func livePairActivity(pair marketdata.Pair) int64 {
	var result int64
	for _, value := range pair.Transactions {
		result += value.Buys + value.Sells
	}
	return result
}

func assertExactLiveDeployments(
	t *testing.T,
	deployments []assetcatalog.Deployment,
) {
	t.Helper()
	expected := make(map[string]string, len(liveChainlinkContracts))
	for _, deployment := range liveChainlinkContracts {
		expected[deployment.ChainKey] = strings.ToLower(deployment.ContractAddress)
	}
	if len(deployments) != len(expected) {
		t.Fatalf("Chainlink deployments = %d, want %d: %#v", len(deployments), len(expected), deployments)
	}
	for _, deployment := range deployments {
		address, exists := expected[deployment.ChainKey]
		if !exists || address != strings.ToLower(deployment.ContractAddress) {
			t.Fatalf("unexpected Chainlink deployment: %#v", deployment)
		}
	}
}
