package marketcollector

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketprotocol"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func (service *Service) RefreshMarketData(ctx context.Context) error {
	return service.withTaskLock(ctx, "market-data", func(ctx context.Context) error {
		now := service.now().UTC()
		lastDiscovery := time.Unix(service.lastDiscoveryUnix.Load(), 0)
		if lastDiscovery.IsZero() || now.Sub(lastDiscovery) >= service.settings.DiscoveryInterval {
			if err := service.discoverPools(ctx); err != nil {
				return err
			}
			service.lastDiscoveryUnix.Store(now.Unix())
		}
		return service.captureSnapshots(ctx)
	})
}

func (service *Service) discoverPools(ctx context.Context) error {
	projects, err := service.repository.ListActiveMarketProjectsForCollection(
		ctx,
		service.settings.ChainID,
		5000,
	)
	if err != nil {
		return err
	}
	tokenOrder := make(map[string][2]string)
	for _, project := range projects {
		started := time.Now()
		pairs, requestErr := service.market.DiscoverPools(
			ctx,
			service.settings.ChainKey,
			project.TokenAddress,
		)
		_ = service.recordHealth(
			ctx,
			"dexscreener",
			"pool_discovery",
			started,
			requestErr,
			nil,
		)
		if requestErr != nil {
			return fmt.Errorf("discover pools for %s: %w", project.TokenAddress, requestErr)
		}
		sort.SliceStable(pairs, func(left, right int) bool {
			return decimalGreater(pairs[left].Liquidity.USD.String(), pairs[right].Liquidity.USD.String())
		})
		for _, pair := range pairs {
			if err := service.persistDiscoveredPair(ctx, project, pair, tokenOrder); err != nil {
				return err
			}
		}
	}
	return nil
}

func (service *Service) persistDiscoveredPair(
	ctx context.Context,
	project store.MarketProject,
	pair marketdata.Pair,
	tokenOrder map[string][2]string,
) error {
	classification := marketprotocol.VerifyPair(
		ctx,
		service.chain,
		service.settings.ChainKey,
		project.TokenAddress,
		pair,
	)
	protocol := classification.Protocol
	version := classification.ProtocolVersion
	adapter := classification.ParserAdapter
	verified := classification.FactoryVerified
	token0Address := pair.BaseToken.Address
	token1Address := pair.QuoteToken.Address
	if classification.Supported {
		order, exists := tokenOrder[pair.PairAddress]
		if !exists {
			order = [2]string{
				classification.Token0Address,
				classification.Token1Address,
			}
			tokenOrder[pair.PairAddress] = order
		}
		token0Address, token1Address = order[0], order[1]
	}
	baseDecimals := int32(18)
	quoteDecimals := int32(18)
	if token0Address == project.TokenAddress {
		baseDecimals = project.TokenDecimals
	}
	if token1Address == project.TokenAddress {
		quoteDecimals = project.TokenDecimals
	}
	token0Symbol := pairTokenSymbol(pair, token0Address)
	token1Symbol := pairTokenSymbol(pair, token1Address)
	var factoryAddress *string
	if classification.FactoryVerified {
		value := classification.FactoryAddress
		factoryAddress = &value
	}
	rawPair, err := marketprotocol.EncodePairMetadata(
		pair,
		classification.MonitoringLevel,
		classification.UnsupportedReason,
		classification.SupportedFeature,
		classification.FactoryAddress,
	)
	if err != nil {
		return fmt.Errorf("encode discovered market pair: %w", err)
	}
	var pairAddress *string
	if value, addressErr := chain.ValidateAddress(pair.PairAddress); addressErr == nil {
		pairAddress = &value
	}
	verificationStatus := "unsupported"
	if classification.Supported {
		verificationStatus = "verified"
	}
	pool, err := service.repository.UpsertMarketPool(ctx, store.UpsertMarketPoolParams{
		ChainKey:             service.settings.ChainKey,
		ChainID:              service.settings.ChainID,
		Protocol:             protocol,
		ProtocolVersion:      version,
		PoolKey:              pair.PairAddress,
		PoolAddress:          pairAddress,
		FactoryAddress:       factoryAddress,
		FactoryVerified:      verified,
		Token0Address:        token0Address,
		Token0Symbol:         token0Symbol,
		Token0Decimals:       baseDecimals,
		Token1Address:        token1Address,
		Token1Symbol:         token1Symbol,
		Token1Decimals:       quoteDecimals,
		LiquidityUSD:         decimalOrZero(pair.Liquidity.USD),
		SupportsEventParsing: classification.Supported,
		ParserAdapter:        adapter,
		VerificationStatus:   verificationStatus,
		Metadata:             rawPair,
		SeenAt:               service.now().UTC(),
	})
	if err != nil {
		return err
	}
	_, err = service.repository.EnsureMarketProjectPool(
		ctx,
		store.EnsureMarketProjectPoolParams{
			DeBoxUserID:     project.DeBoxUserID,
			MarketProjectID: project.ID,
			MarketPoolID:    pool.ID,
			SelectIfNone:    classification.Supported,
			DiscoverySource: marketdata.SourceDexScreener,
		},
	)
	return err
}

func pairTokenSymbol(pair marketdata.Pair, address string) string {
	switch address {
	case pair.BaseToken.Address:
		return pair.BaseToken.Symbol
	case pair.QuoteToken.Address:
		return pair.QuoteToken.Symbol
	default:
		return ""
	}
}

func (service *Service) captureSnapshots(ctx context.Context) error {
	targets, err := service.repository.ListMarketCollectionTargets(
		ctx,
		service.settings.ChainID,
	)
	if err != nil {
		return err
	}
	addressSet := make(map[string]struct{})
	for _, target := range targets {
		if target.PoolAddress != nil {
			addressSet[*target.PoolAddress] = struct{}{}
		}
	}
	if len(addressSet) == 0 {
		return nil
	}
	addresses := make([]string, 0, len(addressSet))
	for address := range addressSet {
		addresses = append(addresses, address)
	}
	sort.Strings(addresses)
	started := time.Now()
	pairs, requestErr := service.market.PairsByAddresses(
		ctx,
		service.settings.ChainKey,
		addresses,
	)
	_ = service.recordHealth(
		ctx,
		"dexscreener",
		"pair_quotes",
		started,
		requestErr,
		nil,
	)
	if requestErr != nil {
		return fmt.Errorf("refresh market pair quotes: %w", requestErr)
	}
	pairByAddress := make(map[string]marketdata.Pair, len(pairs))
	for _, pair := range pairs {
		pairByAddress[pair.PairAddress] = pair
	}
	capturedAt := service.now().UTC()
	captured := make(map[string]struct{})
	for _, target := range targets {
		if target.PoolAddress == nil {
			continue
		}
		snapshotKey := fmt.Sprintf(
			"%d:%s:%d",
			target.ChainID,
			target.TokenAddress,
			target.MarketPoolID,
		)
		if _, exists := captured[snapshotKey]; exists {
			continue
		}
		pair, exists := pairByAddress[*target.PoolAddress]
		if !exists {
			continue
		}
		if err := service.createPairSnapshot(ctx, target, pair, capturedAt); err != nil {
			return err
		}
		captured[snapshotKey] = struct{}{}
	}
	return nil
}

func (service *Service) createPairSnapshot(
	ctx context.Context,
	target store.MarketCollectionTarget,
	pair marketdata.Pair,
	capturedAt time.Time,
) error {
	rawPair, err := json.Marshal(pair)
	if err != nil {
		return fmt.Errorf("encode market snapshot pair: %w", err)
	}
	price := pairPriceForToken(pair, target.TokenAddress)
	var fdv, marketCap *string
	if pair.BaseToken.Address == target.TokenAddress {
		fdv = decimalPointer(pair.FDV)
		marketCap = decimalPointer(pair.MarketCap)
	}
	buys5m, sells5m := transactionPointers(pair.Transactions["m5"])
	buys1h, sells1h := transactionPointers(pair.Transactions["h1"])
	buys24h, sells24h := transactionPointers(pair.Transactions["h24"])
	_, err = service.repository.CreateMarketSnapshot(ctx, store.CreateMarketSnapshotParams{
		ChainKey:        target.ChainKey,
		ChainID:         target.ChainID,
		TokenAddress:    target.TokenAddress,
		MarketPoolID:    target.MarketPoolID,
		PriceUSD:        price,
		LiquidityUSD:    decimalPointer(pair.Liquidity.USD),
		FDVUSD:          fdv,
		MarketCapUSD:    marketCap,
		Volume5mUSD:     decimalPointer(pair.Volume["m5"]),
		Volume15mUSD:    decimalPointer(pair.Volume["m15"]),
		Volume1hUSD:     decimalPointer(pair.Volume["h1"]),
		Volume6hUSD:     decimalPointer(pair.Volume["h6"]),
		Volume24hUSD:    decimalPointer(pair.Volume["h24"]),
		Buys5m:          buys5m,
		Sells5m:         sells5m,
		Buys1h:          buys1h,
		Sells1h:         sells1h,
		Buys24h:         buys24h,
		Sells24h:        sells24h,
		Source:          marketdata.SourceDexScreener,
		SourceTimestamp: nil,
		CapturedAt:      capturedAt,
		RawPayload:      rawPair,
	})
	return err
}

func pairPriceForToken(pair marketdata.Pair, tokenAddress string) *string {
	if pair.BaseToken.Address == tokenAddress {
		return decimalPointer(pair.PriceUSD)
	}
	if pair.QuoteToken.Address != tokenAddress ||
		!pair.PriceUSD.Valid() || !pair.PriceNative.Valid() {
		return nil
	}
	baseUSD, ok := new(big.Rat).SetString(pair.PriceUSD.String())
	if !ok {
		return nil
	}
	basePerQuote, ok := new(big.Rat).SetString(pair.PriceNative.String())
	if !ok || basePerQuote.Sign() == 0 {
		return nil
	}
	value := new(big.Rat).Quo(baseUSD, basePerQuote)
	result := strings.TrimRight(strings.TrimRight(value.FloatString(18), "0"), ".")
	if result == "" {
		result = "0"
	}
	return &result
}

func decimalPointer(value marketdata.Decimal) *string {
	if !value.Valid() {
		return nil
	}
	result := value.String()
	return &result
}

func decimalOrZero(value marketdata.Decimal) string {
	if !value.Valid() {
		return "0"
	}
	return value.String()
}

func decimalGreater(left, right string) bool {
	leftRat, leftOK := new(big.Rat).SetString(left)
	rightRat, rightOK := new(big.Rat).SetString(right)
	if !leftOK {
		return false
	}
	if !rightOK {
		return true
	}
	return leftRat.Cmp(rightRat) > 0
}

func transactionPointers(value marketdata.TransactionCounts) (*int64, *int64) {
	buys := value.Buys
	sells := value.Sells
	return &buys, &sells
}
