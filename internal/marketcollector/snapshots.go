package marketcollector

import (
	"context"
	"encoding/json"
	"errors"
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
		var discoveryErr error
		if service.discoveryPending() || lastDiscovery.IsZero() || now.Sub(lastDiscovery) >= service.settings.DiscoveryInterval {
			discoveryErr = service.discoverPools(ctx)
			if !service.discoveryPending() {
				service.lastDiscoveryUnix.Store(now.Unix())
			}
		}
		// Discovery failure must not stop refreshing pools already selected by
		// users. Both jobs retain their existing schedule and health reporting.
		return errors.Join(discoveryErr, service.captureSnapshots(ctx))
	})
}

func (service *Service) persistDiscoveredPair(
	ctx context.Context,
	project store.MarketProject,
	pair marketdata.Pair,
	tokenOrder map[string][2]string,
	knownDecimals map[string]int,
) error {
	facts := marketprotocol.ReadPoolFacts(ctx, service.chain, service.settings.ChainKey, pair)
	return service.persistDiscoveredPairFacts(ctx, project, pair, facts, knownDecimals, nil, nil)
}

func (service *Service) persistDiscoveredPairFacts(ctx context.Context, project store.MarketProject, pair marketdata.Pair, facts marketprotocol.PoolFacts, knownDecimals map[string]int, upserts map[[32]byte]discoveryPoolResult, decimalFailures map[string]error) error {
	if facts.ReadFailed {
		// An unavailable read is not a new quote-only classification. In
		// particular it must not downgrade a previously verified shared pool.
		return fmt.Errorf("read discovered pool facts: %s", facts.FailureReason)
	}
	if chain.NormalizeChainKey(pair.ChainID, "") != service.settings.ChainKey {
		return fmt.Errorf("discovered pool belongs to another chain")
	}
	token, err := chain.ValidateAddress(project.TokenAddress)
	if err != nil {
		return err
	}
	if facts.Token0Address != "" && facts.Token1Address != "" && token != facts.Token0Address && token != facts.Token1Address {
		return fmt.Errorf("discovered pool does not contain project token")
	}
	base, baseErr := chain.ValidateAddress(pair.BaseToken.Address)
	quote, quoteErr := chain.ValidateAddress(pair.QuoteToken.Address)
	if baseErr == nil && quoteErr == nil {
		if token != base && token != quote {
			return fmt.Errorf("discovered provider pair does not contain project token")
		}
		if facts.Token0Address != "" && facts.Token1Address != "" && !((base == facts.Token0Address && quote == facts.Token1Address) || (base == facts.Token1Address && quote == facts.Token0Address)) {
			return fmt.Errorf("discovered provider pair conflicts with on-chain tokens")
		}
	}
	classification := marketprotocol.ClassifyPairWithFacts(service.settings.ChainKey, token, pair, facts)
	protocol := classification.Protocol
	version := classification.ProtocolVersion
	adapter := classification.ParserAdapter
	verified := classification.FactoryVerified
	token0Address := pair.BaseToken.Address
	token1Address := pair.QuoteToken.Address
	if facts.Token0Address != "" && facts.Token1Address != "" {
		token0Address, token1Address = facts.Token0Address, facts.Token1Address
	}
	baseDecimals, err := service.discoveryTokenDecimals(ctx, token0Address, knownDecimals, decimalFailures)
	if err != nil {
		// A quote-only candidate must not prevent usable verified pools from
		// being collected, and must not be stored with invented metadata.
		if !classification.Supported {
			return nil
		}
		return err
	}
	quoteDecimals, err := service.discoveryTokenDecimals(ctx, token1Address, knownDecimals, decimalFailures)
	if err != nil {
		if !classification.Supported {
			return nil
		}
		return err
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
	params := store.UpsertMarketPoolParams{
		ChainKey:               service.settings.ChainKey,
		ChainID:                service.settings.ChainID,
		Protocol:               protocol,
		ProtocolVersion:        version,
		PoolKey:                pair.PairAddress,
		PoolAddress:            pairAddress,
		FactoryAddress:         factoryAddress,
		FactoryVerified:        verified,
		Token0Address:          token0Address,
		Token0Symbol:           token0Symbol,
		Token0Decimals:         int32(baseDecimals),
		Token0DecimalsVerified: true,
		Token1Address:          token1Address,
		Token1Symbol:           token1Symbol,
		Token1Decimals:         int32(quoteDecimals),
		Token1DecimalsVerified: true,
		LiquidityUSD:           decimalOrZero(pair.Liquidity.USD),
		SupportsEventParsing:   classification.Supported,
		ParserAdapter:          adapter,
		VerificationStatus:     verificationStatus,
		Metadata:               rawPair,
		SeenAt:                 service.now().UTC(),
	}
	poolID, err := service.upsertDiscoveredPool(ctx, params, upserts)
	if err != nil {
		return err
	}
	_, err = service.repository.EnsureMarketProjectPool(
		ctx,
		store.EnsureMarketProjectPoolParams{
			DeBoxUserID:     project.DeBoxUserID,
			MarketProjectID: project.ID,
			MarketPoolID:    poolID,
			SelectIfNone:    classification.Supported,
			DiscoverySource: marketdata.SourceDexScreener,
		},
	)
	return err
}

func pairTokenSymbol(pair marketdata.Pair, address string) string {
	switch {
	case strings.EqualFold(address, pair.BaseToken.Address):
		return pair.BaseToken.Symbol
	case strings.EqualFold(address, pair.QuoteToken.Address):
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
	requestedTargets := make([]store.MarketCollectionTarget, 0, len(targets))
	for _, target := range targets {
		if target.PoolAddress != nil {
			addressSet[*target.PoolAddress] = struct{}{}
			requestedTargets = append(requestedTargets, target)
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
		_, markErr := service.repository.RecordMarketQuoteFailure(ctx, service.settings.ChainID, requestedTargets, service.now().UTC())
		if markErr != nil {
			markErr = fmt.Errorf("record unavailable market quote observations: %w", markErr)
		}
		return errors.Join(fmt.Errorf("refresh market pair quotes: %w", requestErr), markErr)
	}
	pairByAddress := make(map[string]marketdata.Pair, len(pairs))
	for _, pair := range pairs {
		pairByAddress[strings.ToLower(pair.PairAddress)] = pair
	}
	capturedAt := service.now().UTC()
	captured := make(map[string]struct{})
	missingTargets := make([]store.MarketCollectionTarget, 0)
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
		pair, exists := pairByAddress[strings.ToLower(*target.PoolAddress)]
		if !exists || !strings.EqualFold(pair.ChainID, target.ChainKey) ||
			!strings.EqualFold(pair.BaseToken.Address, target.TokenAddress) && !strings.EqualFold(pair.QuoteToken.Address, target.TokenAddress) {
			missingTargets = append(missingTargets, target)
			captured[snapshotKey] = struct{}{}
			continue
		}
		if err := service.createPairSnapshot(ctx, target, pair, capturedAt); err != nil {
			return err
		}
		captured[snapshotKey] = struct{}{}
	}
	if len(missingTargets) > 0 {
		if _, err := service.repository.RecordMarketQuoteFailure(ctx, service.settings.ChainID, missingTargets, service.now().UTC()); err != nil {
			return fmt.Errorf("record missing market quote observations: %w", err)
		}
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
	counts5m, has5m := pair.Transactions["m5"]
	counts1h, has1h := pair.Transactions["h1"]
	counts24h, has24h := pair.Transactions["h24"]
	buys5m, sells5m := transactionPointers(counts5m, has5m)
	buys1h, sells1h := transactionPointers(counts1h, has1h)
	buys24h, sells24h := transactionPointers(counts24h, has24h)
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
		SourceTimestamp: pair.QuoteQuality.SourceTimestamp,
		QuoteQuality:    pair.QuoteQuality.Clone(),
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

func transactionPointers(value marketdata.TransactionCounts, present bool) (*int64, *int64) {
	if !present || value.Buys < 0 || value.Sells < 0 {
		return nil, nil
	}
	buys := value.Buys
	sells := value.Sells
	return &buys, &sells
}
