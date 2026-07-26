package marketcollector

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const logAddressBatchSize = 50

func (service *Service) ScanLogs(ctx context.Context) error {
	return service.withTaskLock(ctx, "log-scanner", func(ctx context.Context) error {
		started := time.Now()
		err := service.scanLogsOnce(ctx)
		_ = service.recordHealth(ctx, "nodit", "log_scanner", started, err, nil)
		return err
	})
}

func (service *Service) scanLogsOnce(ctx context.Context) error {
	if service.settings.ChainID != DefaultChainID ||
		!strings.EqualFold(service.settings.ChainKey, "bsc") {
		return ErrUnsupportedChain
	}
	parserContext, err := service.loadParserContext(ctx)
	if err != nil {
		return err
	}
	if len(parserContext.targetTokens) == 0 {
		return nil
	}
	latest, err := service.chain.LatestBlockNumber(
		ctx,
		service.settings.ChainKey,
		service.settings.ChainFallback,
	)
	if err != nil {
		return fmt.Errorf("get latest market block: %w", err)
	}
	if latest <= uint64(service.settings.ConfirmationDepth) {
		return nil
	}
	safeBlock := int64(latest) - service.settings.ConfirmationDepth
	cursor, err := service.repository.GetMarketChainCursor(
		ctx,
		service.settings.ChainID,
		service.settings.CursorKey,
	)
	if err != nil {
		return err
	}
	if cursor == nil {
		cursor, err = service.initializeCursor(ctx, safeBlock)
		if err != nil {
			return err
		}
	}
	if cursor.NextBlockNumber > safeBlock {
		_, err := service.repository.MarkMarketBlockConfirmed(
			ctx,
			service.settings.ChainID,
			safeBlock,
		)
		return err
	}
	cursor, err = service.verifyCursorContinuity(ctx, *cursor)
	if err != nil {
		return err
	}
	fromBlock := cursor.NextBlockNumber
	toBlock := fromBlock + service.settings.ScanBatchSize - 1
	if toBlock > safeBlock {
		toBlock = safeBlock
	}
	logs, err := service.fetchCollectionLogs(
		ctx,
		uint64(fromBlock),
		uint64(toBlock),
		parserContext,
	)
	if err != nil {
		_ = service.recordCursorError(ctx, *cursor, safeBlock, err)
		return err
	}

	headers := make(map[uint64]blockHeader)
	endHeader, err := service.fetchBlockHeader(ctx, uint64(toBlock))
	if err != nil {
		_ = service.recordCursorError(ctx, *cursor, safeBlock, err)
		return err
	}
	headers[endHeader.Number] = endHeader
	for _, log := range logs {
		blockNumber, parseErr := parseHexQuantity(log.BlockNumber)
		if parseErr != nil {
			return parseErr
		}
		if _, exists := headers[blockNumber]; exists {
			continue
		}
		header, fetchErr := service.fetchBlockHeader(ctx, blockNumber)
		if fetchErr != nil {
			return fetchErr
		}
		headers[blockNumber] = header
	}
	for _, log := range logs {
		blockNumber, _ := parseHexQuantity(log.BlockNumber)
		if header := headers[blockNumber]; header.Hash != strings.ToLower(log.BlockHash) {
			return fmt.Errorf("market log block %d changed during scan", blockNumber)
		}
	}

	transactionHashes := uniqueLogTransactionHashes(logs)
	for _, transactionHash := range transactionHashes {
		if err := service.hydrateParseAndPersist(
			ctx,
			transactionHash,
			parserContext,
			"nodit_log_scanner",
			true,
		); err != nil {
			_ = service.recordCursorError(ctx, *cursor, safeBlock, err)
			return err
		}
	}
	unconfirmedBlocks, err := service.repository.ListUnconfirmedMarketEventBlocks(
		ctx,
		service.settings.ChainID,
		fromBlock,
		toBlock,
	)
	if err != nil {
		return err
	}
	for _, blockNumber := range unconfirmedBlocks {
		if _, exists := headers[uint64(blockNumber)]; !exists {
			header, fetchErr := service.fetchBlockHeader(ctx, uint64(blockNumber))
			if fetchErr != nil {
				return fetchErr
			}
			headers[header.Number] = header
		}
		header := headers[uint64(blockNumber)]
		if _, err := service.repository.MarkMarketBlockReorged(
			ctx,
			service.settings.ChainID,
			blockNumber,
			header.Hash,
		); err != nil {
			return err
		}
	}
	headerNumbers := make([]uint64, 0, len(headers))
	for number := range headers {
		headerNumbers = append(headerNumbers, number)
	}
	sort.Slice(headerNumbers, func(left, right int) bool {
		return headerNumbers[left] < headerNumbers[right]
	})
	for _, number := range headerNumbers {
		header := headers[number]
		timestamp := header.Timestamp
		if _, err := service.repository.UpsertMarketScannedBlock(
			ctx,
			store.UpsertMarketScannedBlockParams{
				ChainKey:       service.settings.ChainKey,
				ChainID:        service.settings.ChainID,
				CursorKey:      service.settings.CursorKey,
				BlockNumber:    int64(header.Number),
				BlockHash:      header.Hash,
				ParentHash:     header.ParentHash,
				BlockTimestamp: &timestamp,
				ScannedAt:      service.now().UTC(),
			},
		); err != nil {
			return err
		}
	}
	if _, err := service.repository.AdvanceMarketChainCursor(
		ctx,
		store.AdvanceMarketChainCursorParams{
			ChainKey:        service.settings.ChainKey,
			ChainID:         service.settings.ChainID,
			CursorKey:       service.settings.CursorKey,
			NextBlockNumber: toBlock + 1,
			SafeBlockNumber: safeBlock,
			LastBlockHash:   &endHeader.Hash,
			Status:          "active",
			ScannedAt:       service.now().UTC(),
		},
	); err != nil {
		return err
	}
	_, err = service.repository.MarkMarketBlockConfirmed(
		ctx,
		service.settings.ChainID,
		toBlock,
	)
	return err
}

func (service *Service) initializeCursor(
	ctx context.Context,
	safeBlock int64,
) (*store.MarketChainCursor, error) {
	nextBlock := safeBlock - service.settings.InitialLookback + 1
	if nextBlock < 1 {
		nextBlock = 1
	}
	previousHeader, err := service.fetchBlockHeader(ctx, uint64(nextBlock-1))
	if err != nil {
		return nil, err
	}
	timestamp := previousHeader.Timestamp
	if _, err := service.repository.UpsertMarketScannedBlock(
		ctx,
		store.UpsertMarketScannedBlockParams{
			ChainKey:       service.settings.ChainKey,
			ChainID:        service.settings.ChainID,
			CursorKey:      service.settings.CursorKey,
			BlockNumber:    int64(previousHeader.Number),
			BlockHash:      previousHeader.Hash,
			ParentHash:     previousHeader.ParentHash,
			BlockTimestamp: &timestamp,
			ScannedAt:      service.now().UTC(),
		},
	); err != nil {
		return nil, err
	}
	cursor, err := service.repository.AdvanceMarketChainCursor(
		ctx,
		store.AdvanceMarketChainCursorParams{
			ChainKey:        service.settings.ChainKey,
			ChainID:         service.settings.ChainID,
			CursorKey:       service.settings.CursorKey,
			NextBlockNumber: nextBlock,
			SafeBlockNumber: safeBlock,
			LastBlockHash:   &previousHeader.Hash,
			Status:          "active",
			ScannedAt:       service.now().UTC(),
		},
	)
	if err != nil {
		return nil, err
	}
	return &cursor, nil
}

func (service *Service) verifyCursorContinuity(
	ctx context.Context,
	cursor store.MarketChainCursor,
) (*store.MarketChainCursor, error) {
	if cursor.NextBlockNumber <= 0 || cursor.LastBlockHash == nil {
		return &cursor, nil
	}
	previousHeader, err := service.fetchBlockHeader(
		ctx,
		uint64(cursor.NextBlockNumber-1),
	)
	if err != nil {
		return nil, err
	}
	if previousHeader.Hash == *cursor.LastBlockHash {
		return &cursor, nil
	}
	candidates, err := service.repository.ListCanonicalMarketScannedBlocks(
		ctx,
		service.settings.ChainID,
		service.settings.CursorKey,
		cursor.NextBlockNumber-1,
		service.settings.ReorgLookback,
	)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		header, fetchErr := service.fetchBlockHeader(ctx, uint64(candidate.BlockNumber))
		if fetchErr != nil {
			return nil, fetchErr
		}
		if header.Hash != candidate.BlockHash {
			continue
		}
		reconciled, reconcileErr := service.repository.ReconcileMarketReorg(
			ctx,
			service.settings.ChainID,
			service.settings.CursorKey,
			candidate.BlockNumber,
			candidate.BlockHash,
			fmt.Sprintf(
				"chain reorganization detected before block %d; rewound to canonical ancestor %d",
				cursor.NextBlockNumber,
				candidate.BlockNumber,
			),
		)
		if reconcileErr != nil {
			return nil, reconcileErr
		}
		return &reconciled.Cursor, nil
	}
	return nil, ErrNoCanonicalAncestor
}

func (service *Service) fetchLogsByAddressBatches(
	ctx context.Context,
	fromBlock uint64,
	toBlock uint64,
	addresses []string,
	topics []chain.LogTopic,
) ([]chain.RPCLog, error) {
	if len(addresses) == 0 {
		return nil, nil
	}
	result := make([]chain.RPCLog, 0)
	for start := 0; start < len(addresses); start += logAddressBatchSize {
		end := start + logAddressBatchSize
		if end > len(addresses) {
			end = len(addresses)
		}
		logs, err := service.chain.Logs(
			ctx,
			service.settings.ChainKey,
			service.settings.ChainFallback,
			chain.LogFilter{
				FromBlock: &fromBlock,
				ToBlock:   &toBlock,
				Addresses: addresses[start:end],
				Topics:    topics,
			},
		)
		if err != nil {
			return nil, fmt.Errorf(
				"scan market logs %d-%d: %w",
				fromBlock,
				toBlock,
				err,
			)
		}
		result = append(result, logs...)
	}
	return deduplicateRPCLogs(result), nil
}

func (service *Service) fetchCollectionLogs(
	ctx context.Context,
	fromBlock uint64,
	toBlock uint64,
	parserContext parserContext,
) ([]chain.RPCLog, error) {
	result := make([]chain.RPCLog, 0)
	appendLogs := func(addresses []string, topics []chain.LogTopic) error {
		logs, err := service.fetchLogsByAddressBatches(
			ctx,
			fromBlock,
			toBlock,
			addresses,
			topics,
		)
		if err != nil {
			return err
		}
		result = append(result, logs...)
		return nil
	}

	poolAddressSet := make(map[string]struct{})
	infinityCLPools := make([]string, 0)
	infinityBinPools := make([]string, 0)
	for _, target := range parserContext.poolByID {
		switch target.ParserAdapter {
		case marketparse.AdapterInfinityCL:
			infinityCLPools = append(infinityCLPools, target.PoolKey)
		case marketparse.AdapterInfinityBin:
			infinityBinPools = append(infinityBinPools, target.PoolKey)
		default:
			if target.PoolAddress != nil && target.ParserAdapter != "" {
				poolAddressSet[*target.PoolAddress] = struct{}{}
			}
		}
	}
	poolAddresses := make([]string, 0, len(poolAddressSet))
	for address := range poolAddressSet {
		poolAddresses = append(poolAddresses, address)
	}
	sort.Strings(poolAddresses)
	if err := appendLogs(poolAddresses, nil); err != nil {
		return nil, err
	}
	if err := appendLogs(
		[]string{marketparse.BSCPancakeV2Factory},
		[]chain.LogTopic{{marketparse.V2FactoryEventTopic()}},
	); err != nil {
		return nil, err
	}
	if err := appendLogs(
		[]string{marketparse.BSCPancakeV3Factory},
		[]chain.LogTopic{{marketparse.V3FactoryEventTopic()}},
	); err != nil {
		return nil, err
	}
	for _, batch := range stringBatches(uniqueStrings(infinityCLPools), logAddressBatchSize) {
		if err := appendLogs(
			[]string{marketparse.BSCInfinityCLManager},
			[]chain.LogTopic{nil, chain.LogTopic(batch)},
		); err != nil {
			return nil, err
		}
	}
	for _, batch := range stringBatches(uniqueStrings(infinityBinPools), logAddressBatchSize) {
		if err := appendLogs(
			[]string{marketparse.BSCInfinityBinManager},
			[]chain.LogTopic{nil, chain.LogTopic(batch)},
		); err != nil {
			return nil, err
		}
	}
	targetTopics := make([]string, 0, len(parserContext.targetTokens))
	for _, token := range parserContext.targetTokens {
		targetTopics = append(targetTopics, addressTopic(token))
	}
	for _, batch := range stringBatches(targetTopics, logAddressBatchSize) {
		for _, initializer := range []struct {
			address string
			topic   string
		}{
			{marketparse.BSCInfinityCLManager, marketparse.InfinityCLInitializeTopic()},
			{marketparse.BSCInfinityBinManager, marketparse.InfinityBinInitializeTopic()},
		} {
			if err := appendLogs(
				[]string{initializer.address},
				[]chain.LogTopic{{initializer.topic}, nil, chain.LogTopic(batch)},
			); err != nil {
				return nil, err
			}
			if err := appendLogs(
				[]string{initializer.address},
				[]chain.LogTopic{{initializer.topic}, nil, nil, chain.LogTopic(batch)},
			); err != nil {
				return nil, err
			}
		}
	}
	fourLogs, err := service.fetchLogsByAddressBatches(
		ctx,
		fromBlock,
		toBlock,
		[]string{marketparse.BSCFourMemeTokenManager},
		[]chain.LogTopic{chain.LogTopic(marketparse.FourMemeEventTopics())},
	)
	if err != nil {
		return nil, err
	}
	relevantFourLogs, err := relevantEmitterLogs(fourLogs, parserContext)
	if err != nil {
		return nil, err
	}
	result = append(result, relevantFourLogs...)
	if err := appendLogs(
		parserContext.targetTokens,
		[]chain.LogTopic{{marketparse.ERC20TransferTopic()}},
	); err != nil {
		return nil, err
	}
	return deduplicateRPCLogs(result), nil
}

func (service *Service) fetchBlockHeader(
	ctx context.Context,
	blockNumber uint64,
) (blockHeader, error) {
	block, err := service.chain.BlockByNumber(
		ctx,
		blockNumber,
		false,
		service.settings.ChainKey,
		service.settings.ChainFallback,
	)
	if err != nil {
		return blockHeader{}, fmt.Errorf("get market block %d: %w", blockNumber, err)
	}
	header, err := decodeBlockHeader(block)
	if err != nil {
		return blockHeader{}, err
	}
	if header.Number != blockNumber {
		return blockHeader{}, fmt.Errorf(
			"requested market block %d but provider returned %d",
			blockNumber,
			header.Number,
		)
	}
	return header, nil
}

func deduplicateRPCLogs(values []chain.RPCLog) []chain.RPCLog {
	seen := make(map[string]chain.RPCLog)
	for _, value := range values {
		key := strings.ToLower(value.TransactionHash) + ":" +
			strings.ToLower(value.LogIndex)
		if existing, exists := seen[key]; exists {
			if existing.BlockHash != value.BlockHash || existing.Data != value.Data ||
				strings.Join(existing.Topics, ",") != strings.Join(value.Topics, ",") {
				// Preserve both conflicts so the parser/continuity checks fail
				// instead of silently accepting an arbitrary copy.
				key += ":" + strings.ToLower(value.BlockHash)
			}
		}
		seen[key] = value
	}
	result := make([]chain.RPCLog, 0, len(seen))
	for _, value := range seen {
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool {
		leftBlock, _ := parseHexQuantity(result[left].BlockNumber)
		rightBlock, _ := parseHexQuantity(result[right].BlockNumber)
		if leftBlock != rightBlock {
			return leftBlock < rightBlock
		}
		leftIndex, _ := parseHexQuantity(result[left].LogIndex)
		rightIndex, _ := parseHexQuantity(result[right].LogIndex)
		return leftIndex < rightIndex
	})
	return result
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func stringBatches(values []string, size int) [][]string {
	if size < 1 {
		size = 1
	}
	result := make([][]string, 0, (len(values)+size-1)/size)
	for start := 0; start < len(values); start += size {
		end := start + size
		if end > len(values) {
			end = len(values)
		}
		result = append(result, values[start:end])
	}
	return result
}

func addressTopic(address string) string {
	address = strings.TrimPrefix(strings.ToLower(address), "0x")
	return "0x" + strings.Repeat("0", 24) + address
}

func relevantEmitterLogs(
	logs []chain.RPCLog,
	parserContext parserContext,
) ([]chain.RPCLog, error) {
	result := make([]chain.RPCLog, 0, len(logs))
	for _, rawLog := range logs {
		log, err := marketparse.LogFromRPC(rawLog)
		if err != nil {
			return nil, err
		}
		events, err := parserContext.parser.Parse(
			marketparse.Receipt{
				ChainID:          uint64(DefaultChainID),
				TransactionHash:  log.TransactionHash,
				BlockNumber:      log.BlockNumber,
				BlockHash:        log.BlockHash,
				TransactionIndex: log.TransactionIndex,
				Logs:             []marketparse.Log{log},
			},
			parserContext.targetTokens,
		)
		if err != nil {
			return nil, err
		}
		if len(events) > 0 {
			result = append(result, rawLog)
		}
	}
	return result, nil
}

func uniqueLogTransactionHashes(logs []chain.RPCLog) []string {
	set := make(map[string]struct{})
	for _, log := range logs {
		set[strings.ToLower(log.TransactionHash)] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for hash := range set {
		result = append(result, hash)
	}
	sort.Strings(result)
	return result
}

func (service *Service) recordCursorError(
	ctx context.Context,
	cursor store.MarketChainCursor,
	safeBlock int64,
	scanError error,
) error {
	_, err := service.repository.AdvanceMarketChainCursor(
		ctx,
		store.AdvanceMarketChainCursorParams{
			ChainKey:        cursor.ChainKey,
			ChainID:         cursor.ChainID,
			CursorKey:       cursor.CursorKey,
			NextBlockNumber: cursor.NextBlockNumber,
			SafeBlockNumber: safeBlock,
			LastBlockHash:   cursor.LastBlockHash,
			Status:          "error",
			LastError:       scanError.Error(),
			ScannedAt:       service.now().UTC(),
		},
	)
	return err
}
