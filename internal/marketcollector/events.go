package marketcollector

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketprotocol"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type parserContext struct {
	parser          *marketparse.Parser
	pools           []marketparse.Pool
	targetTokens    []string
	tokenDecimals   map[string]int
	poolByID        map[int64]store.MarketCollectionTarget
	projectsByToken map[string][]store.MarketProject
}

const maxParserCollectionProjects = 5000

func (service *Service) loadParserContext(ctx context.Context) (parserContext, error) {
	targets, err := service.repository.ListMarketCollectionTargets(
		ctx,
		service.settings.ChainID,
	)
	if err != nil {
		return parserContext{}, err
	}
	projects, err := service.repository.ListActiveMarketProjectsForCollection(
		ctx,
		service.settings.ChainID,
		maxParserCollectionProjects,
	)
	if err != nil {
		return parserContext{}, err
	}
	// The shared collection query is capped at this limit. A full page cannot
	// prove that it contains every monitored token, so do not publish a receipt
	// with a silently truncated parser scope. Scaling this boundary needs an
	// explicit paginated collection implementation.
	if len(projects) >= maxParserCollectionProjects {
		return parserContext{}, fmt.Errorf("market parser project scope reached bounded capacity; complete scope cannot be established")
	}
	targetSet := make(map[string]struct{}, len(projects))
	decimals := make(map[string]int, len(projects)+len(targets)*2)
	// Every supported EVM chain uses 18 native units. This is an explicit
	// native-asset type, not a fallback for unknown ERC-20 metadata.
	decimals[marketparse.NativeTokenAddress] = 18
	projectsByToken := make(map[string][]store.MarketProject)
	for _, project := range projects {
		targetSet[project.TokenAddress] = struct{}{}
		decimals[project.TokenAddress] = int(project.TokenDecimals)
		projectsByToken[project.TokenAddress] = append(
			projectsByToken[project.TokenAddress],
			project,
		)
	}
	pools := make([]marketparse.Pool, 0, len(targets))
	poolByID := make(map[int64]store.MarketCollectionTarget)
	for _, target := range targets {
		if _, exists := poolByID[target.MarketPoolID]; exists {
			continue
		}
		if target.ParserAdapter == "" {
			continue
		}
		logAddress := ""
		switch target.ParserAdapter {
		case marketparse.AdapterInfinityCL:
			logAddress = marketparse.BSCInfinityCLManager
		case marketparse.AdapterInfinityBin:
			logAddress = marketparse.BSCInfinityBinManager
		case marketparse.AdapterV2, marketparse.AdapterV3,
			marketparse.AdapterAlgebra, marketparse.AdapterSolidly:
			if target.PoolAddress == nil {
				continue
			}
			logAddress = *target.PoolAddress
		default:
			continue
		}
		// Only actually registered adapters contribute receipt coverage. Quote
		// display-only pools do not cause metadata RPC work or claim parsed logs.
		// Verified project precision must not be replaced by a legacy pool guess.
		if _, err := service.resolveTokenDecimals(ctx, target.Token0Address, decimals); err != nil {
			return parserContext{}, fmt.Errorf("resolve pool token0 precision: %w", err)
		}
		if _, err := service.resolveTokenDecimals(ctx, target.Token1Address, decimals); err != nil {
			return parserContext{}, fmt.Errorf("resolve pool token1 precision: %w", err)
		}
		pools = append(pools, marketparse.Pool{
			ID:         target.MarketPoolID,
			ChainID:    uint64(target.ChainID),
			Protocol:   target.Protocol,
			Version:    target.ProtocolVersion,
			Adapter:    target.ParserAdapter,
			PoolKey:    target.PoolKey,
			LogAddress: logAddress,
			Token0: marketparse.Token{
				Address:  target.Token0Address,
				Symbol:   target.Token0Symbol,
				Decimals: uint8(decimals[target.Token0Address]),
			},
			Token1: marketparse.Token{
				Address:  target.Token1Address,
				Symbol:   target.Token1Symbol,
				Decimals: uint8(decimals[target.Token1Address]),
			},
		})
		poolByID[target.MarketPoolID] = target
	}
	parser, err := marketparse.NewParser(
		pools,
		marketprotocol.FactoryEmitters(service.settings.ChainKey),
	)
	if err != nil {
		return parserContext{}, fmt.Errorf(
			"create %s market parser: %w",
			service.settings.ChainKey,
			err,
		)
	}
	targetTokens := make([]string, 0, len(targetSet))
	for token := range targetSet {
		targetTokens = append(targetTokens, token)
	}
	sortStrings(targetTokens)
	return parserContext{
		parser:          parser,
		pools:           pools,
		targetTokens:    targetTokens,
		tokenDecimals:   decimals,
		poolByID:        poolByID,
		projectsByToken: projectsByToken,
	}, nil
}

func (service *Service) hydrateParseAndPersist(
	ctx context.Context,
	transactionHash string,
	parserContext parserContext,
	source string,
	confirmed bool,
	expected ...receiptExpectation,
) error {
	hydrated, err := service.readMarketTransaction(ctx, transactionHash, nil, expected)
	if err != nil {
		return err
	}
	return service.parseAndPersistMarketTransaction(ctx, hydrated, parserContext, source, confirmed)
}

func (service *Service) parseAndPersistMarketTransaction(
	ctx context.Context,
	hydrated hydratedMarketTransaction,
	parserContext parserContext,
	source string,
	confirmed bool,
) error {
	receipt, receiptPayload, header := hydrated.receipt, hydrated.receiptPayload, hydrated.header
	transactionHash := receipt.TransactionHash
	blockNumber, transactionIndex := int64Pointer(receipt.BlockNumber), int32Pointer(receipt.TransactionIndex)
	if blockNumber == nil || transactionIndex == nil {
		return fmt.Errorf("receipt position exceeds supported integer bounds")
	}
	events, err := parserContext.parser.Parse(receipt, parserContext.targetTokens)
	if err != nil {
		return fmt.Errorf("parse market transaction %s: %w", transactionHash, err)
	}
	parserContext, events, _, scopeNeedsReload, err := service.prepareInitializedReceipt(ctx, receipt, parserContext, events)
	if err != nil {
		return fmt.Errorf("complete initialized receipt: %w", err)
	}
	prepared := make([]store.MarketTransactionEvent, 0, len(events))
	valuationCache := make(map[eventSnapshotKey]*store.MarketSnapshot)
	scopePoolIDs := make([]int64, 0, len(parserContext.poolByID))
	for id := range parserContext.poolByID {
		scopePoolIDs = append(scopePoolIDs, id)
	}
	for _, event := range events {
		preparedEvent, err := service.prepareParsedEvent(
			ctx,
			event,
			parserContext,
			header.Timestamp,
			source,
			confirmed,
			valuationCache,
		)
		if err != nil {
			return err
		}
		prepared = append(prepared, preparedEvent)
	}
	_, err = service.repository.PersistMarketTransaction(ctx, store.MarketTransactionParams{
		ChainKey: service.settings.ChainKey, ChainID: service.settings.ChainID,
		TransactionHash: receipt.TransactionHash, BlockNumber: *blockNumber,
		BlockHash: receipt.BlockHash, TransactionIndex: *transactionIndex,
		OccurredAt: header.Timestamp, Confirmed: confirmed, ParserVersion: 1,
		ScopePoolIDs: scopePoolIDs, ScopeTokenAddresses: parserContext.targetTokens,
		ReceiptPayload: receiptPayload, Events: prepared,
	})
	if err != nil {
		return fmt.Errorf("persist complete market transaction: %w", err)
	}
	if scopeNeedsReload {
		// The receipt is complete, but later transactions in the scanner's
		// current range need a fresh address set before that range can advance.
		return ErrParserScopeChanged
	}
	return nil
}

func (service *Service) ensureInitializedPool(ctx context.Context, event marketparse.Event, parserContext parserContext) (int64, error) {
	id, _, err := service.ensureInitializedPoolDetailed(ctx, event, parserContext)
	return id, err
}

func (service *Service) ensureInitializedPoolDetailed(
	ctx context.Context,
	event marketparse.Event,
	parserContext parserContext,
) (int64, bool, error) {
	var poolAddress *string
	var factoryAddress *string
	token0Address := event.Metadata["token0_address"]
	token1Address := event.Metadata["token1_address"]
	if token0Address == "" || token1Address == "" {
		token0Address = event.TokenAddress
		token1Address = event.QuoteAddress
	}
	switch event.Adapter {
	case marketparse.AdapterV2, marketparse.AdapterV3,
		marketparse.AdapterAlgebra, marketparse.AdapterSolidly:
		value := event.PoolKey
		poolAddress = &value
		if factory := event.Metadata["factory_address"]; factory != "" {
			factoryAddress = &factory
		}
	case marketparse.AdapterInfinityCL, marketparse.AdapterInfinityBin:
		if manager := event.Metadata["manager_address"]; manager != "" {
			factoryAddress = &manager
		}
	default:
		return 0, false, fmt.Errorf("unsupported initialized pool adapter %s", event.Adapter)
	}
	token0Symbol, _ := projectTokenMetadata(parserContext, token0Address)
	token1Symbol, _ := projectTokenMetadata(parserContext, token1Address)
	token0Decimals, err := service.resolveTokenDecimals(ctx, token0Address, parserContext.tokenDecimals)
	if err != nil {
		return 0, false, fmt.Errorf("resolve initialized token0 precision: %w", err)
	}
	token1Decimals, err := service.resolveTokenDecimals(ctx, token1Address, parserContext.tokenDecimals)
	if err != nil {
		return 0, false, fmt.Errorf("resolve initialized token1 precision: %w", err)
	}
	metadata, _ := json.Marshal(event.Metadata)
	pool, err := service.repository.UpsertMarketPool(ctx, store.UpsertMarketPoolParams{
		ChainKey:               service.settings.ChainKey,
		ChainID:                service.settings.ChainID,
		Protocol:               event.Protocol,
		ProtocolVersion:        event.Version,
		PoolKey:                event.PoolKey,
		PoolAddress:            poolAddress,
		FactoryAddress:         factoryAddress,
		FactoryVerified:        true,
		Token0Address:          token0Address,
		Token0Symbol:           token0Symbol,
		Token0Decimals:         int32(token0Decimals),
		Token0DecimalsVerified: true,
		Token1Address:          token1Address,
		Token1Symbol:           token1Symbol,
		Token1Decimals:         int32(token1Decimals),
		Token1DecimalsVerified: true,
		LiquidityUSD:           "0",
		SupportsEventParsing:   true,
		ParserAdapter:          event.Adapter,
		VerificationStatus:     "verified",
		Metadata:               metadata,
		SeenAt:                 service.now().UTC(),
	})
	if err != nil {
		return 0, false, err
	}
	linkedProjects := make(map[int64]struct{})
	selected := false
	for _, token := range []string{token0Address, token1Address} {
		for _, project := range parserContext.projectsByToken[token] {
			if _, exists := linkedProjects[project.ID]; exists {
				continue
			}
			linkedProjects[project.ID] = struct{}{}
			if link, err := service.repository.EnsureMarketProjectPool(
				ctx,
				store.EnsureMarketProjectPoolParams{
					DeBoxUserID:     project.DeBoxUserID,
					MarketProjectID: project.ID,
					MarketPoolID:    pool.ID,
					SelectIfNone:    true,
					DiscoverySource: "onchain_factory",
				},
			); err != nil {
				return 0, false, err
			} else if link.Selected == 1 {
				selected = true
			}
		}
	}
	return pool.ID, selected, nil
}

func projectTokenMetadata(
	parserContext parserContext,
	tokenAddress string,
) (string, int) {
	projects := parserContext.projectsByToken[tokenAddress]
	if len(projects) > 0 {
		return projects[0].TokenSymbol, int(projects[0].TokenDecimals)
	}
	if decimals, exists := parserContext.tokenDecimals[tokenAddress]; exists {
		return "", decimals
	}
	return "", -1
}

type eventSnapshotKey struct {
	token  string
	poolID int64
}

type historicalValuationRepository interface {
	MarketValuationSnapshot(context.Context, int64, string, *int64, time.Time) (*store.MarketSnapshot, error)
}

func (service *Service) valuationSnapshot(ctx context.Context, token string, poolID *int64, at time.Time, cache map[eventSnapshotKey]*store.MarketSnapshot) (*store.MarketSnapshot, error) {
	key := eventSnapshotKey{token: token}
	if poolID != nil {
		key.poolID = *poolID
	}
	if snapshot, exists := cache[key]; exists {
		return snapshot, nil
	}
	var snapshot *store.MarketSnapshot
	var err error
	if repository, ok := service.repository.(historicalValuationRepository); ok {
		snapshot, err = repository.MarketValuationSnapshot(ctx, service.settings.ChainID, token, poolID, at)
	} else {
		// Lightweight test/adapter implementations may only expose latest. Its
		// timestamp still has to precede the transaction; never value old chain
		// history with a price first observed today.
		snapshot, err = service.repository.LatestMarketSnapshot(ctx, service.settings.ChainID, token, poolID)
	}
	if snapshot != nil && snapshot.CapturedAt.After(at) {
		snapshot = nil
	}
	if err == nil {
		cache[key] = snapshot
	}
	return snapshot, err
}

func quoteCanValue(snapshot *store.MarketSnapshot, at time.Time) bool {
	if snapshot == nil || !snapshot.QuoteQuality.AvailableAt(at) || snapshot.PriceUSD == nil {
		return false
	}
	value, ok := new(big.Rat).SetString(*snapshot.PriceUSD)
	return ok && value.Sign() > 0
}

func valuationEvidence(snapshot *store.MarketSnapshot, at time.Time) map[string]any {
	metadata := map[string]any{
		"valuation_at":             at,
		"valuation_quote_quality":  marketquality.Quote{Status: marketquality.StatusUnknown},
		"valuation_method":         "unavailable",
		"valuation_schema_version": 1,
		"valuation_complete":       false,
	}
	if snapshot == nil {
		return metadata
	}
	metadata["valuation_snapshot_id"] = snapshot.ID
	metadata["valuation_captured_at"] = snapshot.CapturedAt
	metadata["valuation_source_timestamp"] = snapshot.SourceTimestamp
	metadata["valuation_quote_quality"] = snapshot.QuoteQuality.Clone()
	if quoteCanValue(snapshot, at) {
		metadata["valuation_method"] = "historical_api_quote_estimate"
		metadata["valuation_complete"] = true
	}
	return metadata
}

func amountWithKnownDecimals(raw, token string, parserContext parserContext) *string {
	decimals, exists := parserContext.tokenDecimals[token]
	if !exists {
		return nil
	}
	return formatRawAmount(raw, decimals)
}

func (service *Service) prepareParsedEvent(
	ctx context.Context,
	event marketparse.Event,
	parserContext parserContext,
	occurredAt time.Time,
	source string,
	confirmed bool,
	valuationCache map[eventSnapshotKey]*store.MarketSnapshot,
) (store.MarketTransactionEvent, error) {
	if occurredAt.IsZero() {
		return store.MarketTransactionEvent{}, fmt.Errorf("canonical event timestamp is missing")
	}
	tokenAmount := amountWithKnownDecimals(event.TokenAmountRaw, event.TokenAddress, parserContext)
	quoteAmount := amountWithKnownDecimals(event.QuoteAmountRaw, event.QuoteAddress, parserContext)

	var marketPoolID *int64
	if event.PoolID > 0 {
		value := event.PoolID
		marketPoolID = &value
	}
	var priceUSD, usdValue *string
	var parentValuation map[string]any
	if len(event.TradeLegs) == 0 {
		snapshot, err := service.valuationSnapshot(ctx, event.TokenAddress, marketPoolID, occurredAt, valuationCache)
		if err != nil {
			return store.MarketTransactionEvent{}, err
		}
		parentValuation = valuationEvidence(snapshot, occurredAt)
		if quoteCanValue(snapshot, occurredAt) {
			priceUSD = snapshot.PriceUSD
			usdValue = multiplyDecimalPointers(tokenAmount, priceUSD)
		}
		parentValuation["valuation_complete"] = usdValue != nil
	}
	rawEvent := event
	rawEvent.TradeLegs = nil // The exact legs are stored once in their fact rows.
	rawPayload, err := json.Marshal(rawEvent)
	if err != nil {
		return store.MarketTransactionEvent{}, fmt.Errorf("encode parsed market event: %w", err)
	}
	metadata := make(map[string]any, len(event.Metadata)+6)
	for key, value := range event.Metadata {
		metadata[key] = value
	}
	for key, value := range parentValuation {
		metadata[key] = value
	}
	metadata["adapter"] = event.Adapter
	metadata["protocol"] = event.Protocol
	metadata["protocol_version"] = event.Version
	metadata["quote_address"] = event.QuoteAddress
	metadata["recipient_address"] = event.RecipientAddress
	metadata["log_indices"] = event.LogIndices
	if offersRaw := event.Metadata["offers_raw"]; offersRaw != "" {
		if progress := fourMemeProgress(
			offersRaw,
			parserContext.projectsByToken[event.TokenAddress],
		); progress != "" {
			metadata["progress_percent"] = progress
		}
	}
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		return store.MarketTransactionEvent{}, fmt.Errorf("encode parsed market event metadata: %w", err)
	}

	transactionHash := nullableString(event.TransactionHash)
	blockHash := nullableString(event.BlockHash)
	walletAddress := nullableString(event.WalletAddress)
	tokenAmountRaw := nullableString(event.TokenAmountRaw)
	quoteAmountRaw := nullableString(event.QuoteAmountRaw)
	transactionIndex := int32Pointer(event.TransactionIndex)
	logIndex := int32Pointer(event.LogIndex)
	blockNumber := int64Pointer(event.BlockNumber)
	eventKey := parsedEventKey(event)
	result := store.MarketTransactionEvent{Event: store.CreateMarketEventParams{
		MarketPoolID:     marketPoolID,
		ChainKey:         service.settings.ChainKey,
		ChainID:          service.settings.ChainID,
		TokenAddress:     event.TokenAddress,
		EventType:        event.Type,
		EventKey:         eventKey,
		TransactionHash:  transactionHash,
		TransactionIndex: transactionIndex,
		LogIndex:         logIndex,
		BlockNumber:      blockNumber,
		BlockHash:        blockHash,
		WalletAddress:    walletAddress,
		TokenAmountRaw:   tokenAmountRaw,
		QuoteAmountRaw:   quoteAmountRaw,
		TokenAmount:      tokenAmount,
		QuoteAmount:      quoteAmount,
		USDValue:         usdValue,
		PriceUSD:         priceUSD,
		Source:           source,
		Confidence:       event.Confidence,
		Confirmed:        confirmed,
		OccurredAt:       occurredAt,
		RawPayload:       rawPayload,
		Metadata:         encodedMetadata,
	}}
	var earliestFetchedAt, earliestSourceAt *time.Time
	allLegsValued := true
	for _, leg := range event.TradeLegs {
		if leg.PoolID <= 0 || leg.LogIndex > uint64(^uint64(0)>>1) {
			return store.MarketTransactionEvent{}, fmt.Errorf("parsed trade leg lacks a bounded pool/log identity")
		}
		tokenAmount := amountWithKnownDecimals(leg.TokenAmountRaw, event.TokenAddress, parserContext)
		quoteAmount := amountWithKnownDecimals(leg.QuoteAmountRaw, leg.QuoteAddress, parserContext)
		poolID := leg.PoolID
		quote, err := service.valuationSnapshot(ctx, event.TokenAddress, &poolID, occurredAt, valuationCache)
		if err != nil {
			return store.MarketTransactionEvent{}, err
		}
		var legPrice, legUSD *string
		legMetadata := map[string]any{
			"protocol": leg.Protocol, "protocol_version": leg.Version, "adapter": leg.Adapter,
			"pool_key": leg.PoolKey, "wallet_address": leg.WalletAddress,
			"recipient_address": leg.RecipientAddress,
			"amount0_delta_raw": leg.Amount0DeltaRaw, "amount1_delta_raw": leg.Amount1DeltaRaw,
		}
		for key, value := range valuationEvidence(quote, occurredAt) {
			legMetadata[key] = value
		}
		if quoteCanValue(quote, occurredAt) {
			legPrice = quote.PriceUSD
			legUSD = multiplyTradeDecimalPointers(tokenAmount, legPrice)
			earliestFetchedAt = earlierValuationTime(earliestFetchedAt, quote.QuoteQuality.FetchedAt)
			earliestSourceAt = earlierValuationTime(earliestSourceAt, quote.QuoteQuality.SourceTimestamp)
		}
		legMetadata["valuation_complete"] = legUSD != nil
		allLegsValued = allLegsValued && legUSD != nil
		encoded, err := json.Marshal(legMetadata)
		if err != nil {
			return store.MarketTransactionEvent{}, err
		}
		result.Legs = append(result.Legs, store.MarketTradeLegParams{
			PoolID: leg.PoolID, LogIndex: int64(leg.LogIndex), QuoteAddress: leg.QuoteAddress,
			TokenAmountRaw: leg.TokenAmountRaw, QuoteAmountRaw: leg.QuoteAmountRaw,
			TokenAmount: tokenAmount, QuoteAmount: quoteAmount, USDValue: legUSD, PriceUSD: legPrice,
			Metadata: encoded,
		})
	}
	if len(result.Legs) > 0 {
		total := new(big.Rat)
		complete := true
		for _, leg := range result.Legs {
			if leg.USDValue == nil {
				complete = false
				break
			}
			value, ok := new(big.Rat).SetString(*leg.USDValue)
			if !ok || value.Sign() < 0 {
				complete = false
				break
			}
			total.Add(total, value)
		}
		if complete {
			result.Event.USDValue = finiteTradeDecimal(total)
			if tokenAmount != nil {
				amount, ok := new(big.Rat).SetString(*tokenAmount)
				if ok && amount.Sign() > 0 {
					price := new(big.Rat).Quo(total, amount).FloatString(18)
					result.Event.PriceUSD = &price
				}
			}
		}
		metadata["trade_fact_version"] = 1
		metadata["valuation_method"] = "sum_pool_snapshot_estimates"
		metadata["valuation_schema_version"] = 1
		metadata["valuation_at"] = occurredAt
		metadata["valuation_complete"] = allLegsValued && result.Event.USDValue != nil
		aggregateQuality := marketquality.Quote{Status: marketquality.StatusUnknown}
		if allLegsValued && result.Event.USDValue != nil {
			aggregateQuality = marketquality.Quote{Status: marketquality.StatusLive,
				FetchedAt: earliestFetchedAt, SourceTimestamp: earliestSourceAt}
		}
		// Compact aggregate provenance is sufficient for existing parent sample
		// consumers. The exact evidence stays in each immutable leg; duplicating
		// all leg metadata into the parent would multiply memory and disk I/O.
		metadata["valuation_quote_quality"] = aggregateQuality
		encoded, err := json.Marshal(metadata)
		if err != nil {
			return store.MarketTransactionEvent{}, err
		}
		result.Event.Metadata = encoded
	}
	return result, nil
}

func earlierValuationTime(current, candidate *time.Time) *time.Time {
	if candidate == nil || candidate.IsZero() {
		return current
	}
	if current == nil || candidate.Before(*current) {
		value := candidate.UTC()
		return &value
	}
	return current
}

func fourMemeProgress(offersRaw string, projects []store.MarketProject) string {
	offers, ok := new(big.Int).SetString(strings.TrimSpace(offersRaw), 10)
	if !ok || offers.Sign() < 0 {
		return ""
	}
	for _, project := range projects {
		if project.TotalSupplyRaw == nil {
			continue
		}
		total, ok := new(big.Int).SetString(*project.TotalSupplyRaw, 10)
		if !ok || total.Sign() <= 0 {
			continue
		}
		sold := new(big.Int).Sub(total, offers)
		if sold.Sign() < 0 {
			sold.SetInt64(0)
		}
		progress := new(big.Rat).SetFrac(sold, total)
		progress.Mul(progress, big.NewRat(100, 1))
		return strings.TrimRight(
			strings.TrimRight(progress.FloatString(6), "0"),
			".",
		)
	}
	return ""
}

func fourMemeProjectStatus(event marketparse.Event) string {
	if event.Adapter != marketparse.AdapterFourMemeV1 &&
		event.Adapter != marketparse.AdapterFourMemeV2 {
		return ""
	}
	switch event.Type {
	case marketparse.EventTokenCreated, marketparse.EventBuy, marketparse.EventSell:
		return "bonding"
	case marketparse.EventTradingStopped:
		return "graduating"
	case marketparse.EventMigrated:
		return "migrated"
	default:
		return ""
	}
}

func decodeReceipt(
	transaction map[string]any,
	receiptObject map[string]any,
	chainID int64,
) (marketparse.Receipt, error) {
	if receiptObject == nil {
		return marketparse.Receipt{}, fmt.Errorf("transaction receipt is not available")
	}
	encoded, err := json.Marshal(receiptObject)
	if err != nil {
		return marketparse.Receipt{}, err
	}
	return decodeReceiptPayload(encoded, chainID)
}

func decodeReceiptPayload(encoded []byte, chainID int64) (marketparse.Receipt, error) {
	var raw marketReceiptEnvelope
	if err := json.Unmarshal(encoded, &raw); err != nil {
		return marketparse.Receipt{}, err
	}
	if len(raw.Logs) == 0 || strings.TrimSpace(string(raw.Logs)) == "null" {
		return marketparse.Receipt{}, fmt.Errorf("receipt logs are missing")
	}
	var rawLogs []chain.RPCLog
	if err := json.Unmarshal(raw.Logs, &rawLogs); err != nil || rawLogs == nil {
		return marketparse.Receipt{}, fmt.Errorf("receipt logs must be a complete array")
	}
	if len(rawLogs) > maxMarketReceiptLogs {
		return marketparse.Receipt{}, fmt.Errorf("receipt exceeds bounded log capacity")
	}
	return decodedMarketReceipt(raw, rawLogs, chainID)
}

func decodedMarketReceipt(raw marketReceiptEnvelope, rawLogs []chain.RPCLog, chainID int64) (marketparse.Receipt, error) {
	status, err := parseHexQuantity(raw.Status)
	if err != nil || status > 1 || status == 0 && len(rawLogs) != 0 {
		return marketparse.Receipt{}, fmt.Errorf("receipt execution status is invalid")
	}
	if _, err := chain.ValidateTransactionHash(raw.TransactionHash); err != nil {
		return marketparse.Receipt{}, fmt.Errorf("receipt transaction hash is invalid")
	}
	if _, err := chain.ValidateTransactionHash(raw.BlockHash); err != nil {
		return marketparse.Receipt{}, fmt.Errorf("receipt block hash is invalid")
	}
	blockNumber, err := parseHexQuantity(raw.BlockNumber)
	if err != nil {
		return marketparse.Receipt{}, fmt.Errorf("block number: %w", err)
	}
	transactionIndex, err := parseHexQuantity(raw.TransactionIndex)
	if err != nil {
		return marketparse.Receipt{}, fmt.Errorf("transaction index: %w", err)
	}
	from := strings.ToLower(strings.TrimSpace(raw.From))
	to := strings.ToLower(strings.TrimSpace(raw.To))
	if _, err := chain.ValidateAddress(from); err != nil {
		return marketparse.Receipt{}, fmt.Errorf("receipt sender is invalid")
	}
	logs := make([]marketparse.Log, 0, len(rawLogs))
	for _, rawLog := range rawLogs {
		if rawLog.Removed || !strings.EqualFold(rawLog.TransactionHash, raw.TransactionHash) ||
			!strings.EqualFold(rawLog.BlockHash, raw.BlockHash) || rawLog.BlockNumber != raw.BlockNumber ||
			rawLog.TransactionIndex != raw.TransactionIndex {
			return marketparse.Receipt{}, fmt.Errorf("receipt contains a missing or mismatched log inclusion")
		}
		log, err := marketparse.LogFromRPC(rawLog)
		if err != nil {
			return marketparse.Receipt{}, err
		}
		logs = append(logs, log)
	}
	return marketparse.Receipt{
		ChainID:          uint64(chainID),
		TransactionHash:  strings.ToLower(raw.TransactionHash),
		From:             from,
		To:               to,
		BlockNumber:      blockNumber,
		BlockHash:        strings.ToLower(raw.BlockHash),
		TransactionIndex: transactionIndex,
		Logs:             logs,
	}, nil
}

type blockHeader struct {
	Number     uint64
	Hash       string
	ParentHash string
	Timestamp  time.Time
}

func decodeBlockHeader(block map[string]any) (blockHeader, error) {
	if block == nil {
		return blockHeader{}, fmt.Errorf("block is not available")
	}
	number, err := parseHexQuantity(rpcString(block, "number"))
	if err != nil {
		return blockHeader{}, fmt.Errorf("block number: %w", err)
	}
	timestamp, err := parseHexQuantity(rpcString(block, "timestamp"))
	if err != nil {
		return blockHeader{}, fmt.Errorf("block timestamp: %w", err)
	}
	hash, err := chain.ValidateTransactionHash(rpcString(block, "hash"))
	if err != nil {
		return blockHeader{}, fmt.Errorf("invalid block hash")
	}
	parentHash, err := chain.ValidateTransactionHash(rpcString(block, "parentHash"))
	if err != nil {
		return blockHeader{}, fmt.Errorf("invalid parent block hash")
	}
	return blockHeader{
		Number:     number,
		Hash:       hash,
		ParentHash: parentHash,
		Timestamp:  time.Unix(int64(timestamp), 0).UTC(),
	}, nil
}

func parseHexQuantity(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "0x") || len(value) < 3 {
		return 0, fmt.Errorf("invalid hex quantity")
	}
	result, err := strconv.ParseUint(value[2:], 16, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid hex quantity")
	}
	return result, nil
}

func rpcString(object map[string]any, key string) string {
	if object == nil {
		return ""
	}
	value, _ := object[key].(string)
	return strings.ToLower(strings.TrimSpace(value))
}

func parsedEventKey(event marketparse.Event) string {
	indices := event.LogIndices
	if len(indices) == 0 {
		indices = []uint64{event.LogIndex}
	}
	parts := make([]string, len(indices))
	for index, value := range indices {
		parts[index] = strconv.FormatUint(value, 10)
	}
	return strings.ToLower(event.TransactionHash) + ":" +
		strings.Join(parts, ",") + ":" + event.Type + ":" + event.TokenAddress
}

func formatRawAmount(raw string, decimals int) *string {
	if strings.TrimSpace(raw) == "" || decimals < 0 {
		return nil
	}
	value, err := chain.FormatUnits(raw, decimals)
	if err != nil {
		return nil
	}
	return &value
}

func multiplyDecimalPointers(left, right *string) *string {
	return multiplyTradeDecimalPointers(left, right)
}

// Trade facts retain small, valid values instead of rounding a token with more
// than 18 decimals down to zero. This is a bounded conversion of a terminating
// decimal, shared by new DEX and protocol-event valuations.
func multiplyTradeDecimalPointers(left, right *string) *string {
	if left == nil || right == nil || len(*left) > 1024 || len(*right) > 1024 {
		return nil
	}
	l, lok := new(big.Rat).SetString(*left)
	r, rok := new(big.Rat).SetString(*right)
	if !lok || !rok || l.Sign() < 0 || r.Sign() < 0 {
		return nil
	}
	return finiteTradeDecimal(new(big.Rat).Mul(l, r))
}

func finiteTradeDecimal(value *big.Rat) *string {
	denominator := new(big.Int).Set(value.Denom())
	counts := [2]int{}
	for index, factor := range []int64{2, 5} {
		divisor := big.NewInt(factor)
		for denominator.Cmp(big.NewInt(1)) > 0 {
			quotient, remainder := new(big.Int), new(big.Int)
			quotient.QuoRem(denominator, divisor, remainder)
			if remainder.Sign() != 0 {
				break
			}
			counts[index]++
			if counts[index] > 1024 {
				return nil
			}
			denominator = quotient
		}
	}
	if denominator.Cmp(big.NewInt(1)) != 0 {
		return nil
	}
	scale := counts[0]
	if counts[1] > scale {
		scale = counts[1]
	}
	text := value.FloatString(scale)
	if strings.Contains(text, ".") {
		text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	}
	return &text
}

func nullableString(value string) *string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return nil
	}
	return &value
}

func int32Pointer(value uint64) *int32 {
	if value > uint64(^uint32(0)>>1) {
		return nil
	}
	result := int32(value)
	return &result
}

func int64Pointer(value uint64) *int64 {
	if value > uint64(^uint64(0)>>1) {
		return nil
	}
	result := int64(value)
	return &result
}
