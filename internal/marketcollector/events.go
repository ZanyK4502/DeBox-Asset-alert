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
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type parserContext struct {
	parser          *marketparse.Parser
	targetTokens    []string
	tokenDecimals   map[string]int
	poolByID        map[int64]store.MarketCollectionTarget
	projectsByToken map[string][]store.MarketProject
}

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
		5000,
	)
	if err != nil {
		return parserContext{}, err
	}
	targetSet := make(map[string]struct{}, len(projects))
	decimals := make(map[string]int, len(projects)+len(targets)*2)
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
		decimals[target.Token0Address] = int(target.Token0Decimals)
		decimals[target.Token1Address] = int(target.Token1Decimals)
		if _, exists := poolByID[target.MarketPoolID]; exists {
			continue
		}
		poolByID[target.MarketPoolID] = target
		if target.ParserAdapter == "" {
			continue
		}
		logAddress := ""
		switch target.ParserAdapter {
		case marketparse.AdapterInfinityCL:
			logAddress = marketparse.BSCInfinityCLManager
		case marketparse.AdapterInfinityBin:
			logAddress = marketparse.BSCInfinityBinManager
		case marketparse.AdapterV2, marketparse.AdapterV3:
			if target.PoolAddress == nil {
				continue
			}
			logAddress = *target.PoolAddress
		default:
			continue
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
				Decimals: uint8(target.Token0Decimals),
			},
			Token1: marketparse.Token{
				Address:  target.Token1Address,
				Symbol:   target.Token1Symbol,
				Decimals: uint8(target.Token1Decimals),
			},
		})
	}
	parser, err := marketparse.NewBSCParser(pools)
	if err != nil {
		return parserContext{}, fmt.Errorf("create BSC market parser: %w", err)
	}
	targetTokens := make([]string, 0, len(targetSet))
	for token := range targetSet {
		targetTokens = append(targetTokens, token)
	}
	sortStrings(targetTokens)
	return parserContext{
		parser:          parser,
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
) error {
	transaction, err := service.chain.RPCTransactionByHash(
		ctx,
		transactionHash,
		service.settings.ChainKey,
		service.settings.ChainFallback,
	)
	if err != nil {
		return fmt.Errorf("get market transaction %s: %w", transactionHash, err)
	}
	receiptObject, err := service.chain.TransactionReceipt(
		ctx,
		transactionHash,
		service.settings.ChainKey,
		service.settings.ChainFallback,
	)
	if err != nil {
		return fmt.Errorf("get market receipt %s: %w", transactionHash, err)
	}
	receipt, err := decodeReceipt(transaction, receiptObject)
	if err != nil {
		return fmt.Errorf("decode market receipt %s: %w", transactionHash, err)
	}
	block, err := service.chain.BlockByNumber(
		ctx,
		receipt.BlockNumber,
		false,
		service.settings.ChainKey,
		service.settings.ChainFallback,
	)
	if err != nil {
		return fmt.Errorf("get market event block %d: %w", receipt.BlockNumber, err)
	}
	header, err := decodeBlockHeader(block)
	if err != nil {
		return fmt.Errorf("decode market event block %d: %w", receipt.BlockNumber, err)
	}
	if receipt.BlockHash != "" && receipt.BlockHash != header.Hash {
		return fmt.Errorf("transaction receipt block hash is no longer canonical")
	}
	events, err := parserContext.parser.Parse(receipt, parserContext.targetTokens)
	if err != nil {
		return fmt.Errorf("parse market transaction %s: %w", transactionHash, err)
	}
	initializedPools := make(map[string]int64)
	for _, event := range events {
		if event.Type == marketparse.EventPoolInitialized && event.PoolID == 0 {
			poolID, exists := initializedPools[event.Adapter+":"+event.PoolKey]
			if !exists {
				poolID, err = service.ensureInitializedPool(ctx, event, parserContext)
				if err != nil {
					return err
				}
				initializedPools[event.Adapter+":"+event.PoolKey] = poolID
			}
			event.PoolID = poolID
		}
		if err := service.persistParsedEvent(
			ctx,
			event,
			parserContext,
			header.Timestamp,
			source,
			confirmed,
		); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) ensureInitializedPool(
	ctx context.Context,
	event marketparse.Event,
	parserContext parserContext,
) (int64, error) {
	var poolAddress *string
	var factoryAddress *string
	token0Address := event.Metadata["token0_address"]
	token1Address := event.Metadata["token1_address"]
	if token0Address == "" || token1Address == "" {
		token0Address = event.TokenAddress
		token1Address = event.QuoteAddress
	}
	switch event.Adapter {
	case marketparse.AdapterV2, marketparse.AdapterV3:
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
		return 0, fmt.Errorf("unsupported initialized pool adapter %s", event.Adapter)
	}
	token0Symbol, token0Decimals := projectTokenMetadata(
		parserContext,
		token0Address,
	)
	token1Symbol, token1Decimals := projectTokenMetadata(
		parserContext,
		token1Address,
	)
	metadata, _ := json.Marshal(event.Metadata)
	pool, err := service.repository.UpsertMarketPool(ctx, store.UpsertMarketPoolParams{
		ChainKey:             service.settings.ChainKey,
		ChainID:              service.settings.ChainID,
		Protocol:             event.Protocol,
		ProtocolVersion:      event.Version,
		PoolKey:              event.PoolKey,
		PoolAddress:          poolAddress,
		FactoryAddress:       factoryAddress,
		FactoryVerified:      true,
		Token0Address:        token0Address,
		Token0Symbol:         token0Symbol,
		Token0Decimals:       int32(token0Decimals),
		Token1Address:        token1Address,
		Token1Symbol:         token1Symbol,
		Token1Decimals:       int32(token1Decimals),
		LiquidityUSD:         "0",
		SupportsEventParsing: true,
		ParserAdapter:        event.Adapter,
		VerificationStatus:   "verified",
		Metadata:             metadata,
		SeenAt:               service.now().UTC(),
	})
	if err != nil {
		return 0, err
	}
	for _, project := range parserContext.projectsByToken[event.TokenAddress] {
		if _, err := service.repository.EnsureMarketProjectPool(
			ctx,
			store.EnsureMarketProjectPoolParams{
				DeBoxUserID:     project.DeBoxUserID,
				MarketProjectID: project.ID,
				MarketPoolID:    pool.ID,
				SelectIfNone:    true,
				DiscoverySource: "onchain_factory",
			},
		); err != nil {
			return 0, err
		}
	}
	return pool.ID, nil
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
	return "", 18
}

func (service *Service) persistParsedEvent(
	ctx context.Context,
	event marketparse.Event,
	parserContext parserContext,
	occurredAt time.Time,
	source string,
	confirmed bool,
) error {
	if occurredAt.IsZero() {
		occurredAt = service.now().UTC()
	}
	tokenDecimals := parserContext.tokenDecimals[event.TokenAddress]
	tokenAmount := formatRawAmount(event.TokenAmountRaw, tokenDecimals)
	quoteDecimals := parserContext.tokenDecimals[event.QuoteAddress]
	quoteAmount := formatRawAmount(event.QuoteAmountRaw, quoteDecimals)

	var marketPoolID *int64
	if event.PoolID > 0 {
		value := event.PoolID
		marketPoolID = &value
	}
	var priceUSD, usdValue *string
	snapshot, err := service.repository.LatestMarketSnapshot(
		ctx,
		service.settings.ChainID,
		event.TokenAddress,
		marketPoolID,
	)
	if err != nil {
		return err
	}
	if snapshot != nil && snapshot.PriceUSD != nil {
		priceUSD = snapshot.PriceUSD
		usdValue = multiplyDecimalPointers(tokenAmount, priceUSD)
	}
	rawPayload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode parsed market event: %w", err)
	}
	metadata := make(map[string]any, len(event.Metadata)+6)
	for key, value := range event.Metadata {
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
		return fmt.Errorf("encode parsed market event metadata: %w", err)
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
	_, _, err = service.repository.CreateMarketEvent(ctx, store.CreateMarketEventParams{
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
	})
	if err != nil {
		return fmt.Errorf("persist parsed market event %s: %w", eventKey, err)
	}
	if status := fourMemeProjectStatus(event); status != "" {
		if _, err := service.repository.UpdateMarketProjectsFourMemeStatus(
			ctx,
			service.settings.ChainID,
			event.TokenAddress,
			status,
		); err != nil {
			return err
		}
	}
	return nil
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
) (marketparse.Receipt, error) {
	if receiptObject == nil {
		return marketparse.Receipt{}, fmt.Errorf("transaction receipt is not available")
	}
	encoded, err := json.Marshal(receiptObject)
	if err != nil {
		return marketparse.Receipt{}, err
	}
	var raw struct {
		TransactionHash  string         `json:"transactionHash"`
		TransactionIndex string         `json:"transactionIndex"`
		BlockHash        string         `json:"blockHash"`
		BlockNumber      string         `json:"blockNumber"`
		From             string         `json:"from"`
		To               string         `json:"to"`
		Logs             []chain.RPCLog `json:"logs"`
	}
	if err := json.Unmarshal(encoded, &raw); err != nil {
		return marketparse.Receipt{}, err
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
	if value := rpcString(transaction, "from"); value != "" {
		from = value
	}
	if value := rpcString(transaction, "to"); value != "" {
		to = value
	}
	logs := make([]marketparse.Log, 0, len(raw.Logs))
	for _, rawLog := range raw.Logs {
		log, err := marketparse.LogFromRPC(rawLog)
		if err != nil {
			return marketparse.Receipt{}, err
		}
		logs = append(logs, log)
	}
	return marketparse.Receipt{
		ChainID:          uint64(DefaultChainID),
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
	if left == nil || right == nil {
		return nil
	}
	leftRat, ok := new(big.Rat).SetString(*left)
	if !ok {
		return nil
	}
	rightRat, ok := new(big.Rat).SetString(*right)
	if !ok {
		return nil
	}
	value := new(big.Rat).Mul(leftRat, rightRat)
	result := value.FloatString(18)
	result = strings.TrimRight(strings.TrimRight(result, "0"), ".")
	if result == "" {
		result = "0"
	}
	return &result
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
