package marketcollector

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketprotocol"
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
	started := time.Now()
	if service.settings.ScanBatchSize < 1 || service.settings.ScanBatchSize > MaxScanBatchSize ||
		service.settings.HydrationConcurrency < 1 || service.settings.HydrationConcurrency > DefaultHydrationConcurrency {
		return fmt.Errorf("market scan batch must be 1..1000 and hydration concurrency 1..2")
	}
	profile, err := chain.ChainProfile(service.settings.ChainKey, "")
	if err != nil || service.settings.ChainID != profile.ChainID {
		return ErrUnsupportedChain
	}
	// Read the lifecycle revision before either parser query. Activation's
	// final compare-and-swap can then detect a committed idle/active transition
	// even when the number of targets returns to its original value.
	collectionState, err := service.observeScanCollection(ctx)
	if err != nil {
		return err
	}
	if collectionState != nil && collectionState.ActiveTargetCount == 0 {
		return nil
	}
	parserContext, err := service.loadParserContext(ctx)
	if err != nil {
		return err
	}
	if collectionState != nil {
		var projectCount int64
		for _, projects := range parserContext.projectsByToken {
			projectCount += int64(len(projects))
		}
		if collectionState.ActiveTargetCount != projectCount {
			return store.ErrMarketCollectionStateChanged
		}
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
	safeObservedAt := time.Now().UTC()
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
	activationInitialized := false
	if cursor == nil && collectionState != nil && collectionState.PendingIdle != nil {
		var deferred bool
		cursor, _, deferred, err = service.prepareCollectionActivation(ctx, *collectionState, nil, nil, safeBlock)
		if err != nil || deferred {
			return err
		}
		activationInitialized = true
	}
	if cursor == nil {
		cursor, err = service.initializeCursor(ctx, safeBlock)
		if err != nil {
			return err
		}
	}
	if cursor.NextBlockNumber > safeBlock {
		// A cursor position is not canonical evidence. In particular, a later
		// controlled forward move must not confirm an unverified old queue.
		return nil
	}
	cursor, previousHeader, err := service.verifyCursorContinuityWithHeader(ctx, *cursor)
	if err != nil {
		return err
	}
	fromBlock := cursor.NextBlockNumber
	if fromBlock < 1 {
		return store.ErrInvalidMarketCursor
	}
	if previousHeader == nil {
		header, fetchErr := service.fetchBlockHeader(ctx, uint64(fromBlock-1))
		if fetchErr != nil {
			return fetchErr
		}
		previousHeader = &header
	}
	if !activationInitialized && collectionState != nil && collectionState.PendingIdle != nil {
		var deferred bool
		cursor, previousHeader, deferred, err = service.prepareCollectionActivation(ctx, *collectionState, cursor, previousHeader, safeBlock)
		if err != nil || deferred {
			return err
		}
		fromBlock = cursor.NextBlockNumber
	}
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
	expectedReceipts, err := expectedScannerReceipts(logs)
	if err != nil {
		return err
	}
	denseBlocks := newScanBlockTransactions(service.settings.ChainKey, expectedReceipts)

	numbers := []uint64{uint64(toBlock)}
	seen := map[uint64]struct{}{uint64(toBlock): {}}
	for _, log := range logs {
		blockNumber, parseErr := parseHexQuantity(log.BlockNumber)
		if parseErr != nil {
			return parseErr
		}
		if blockNumber < uint64(fromBlock) || blockNumber > uint64(toBlock) {
			return fmt.Errorf("market log lies outside the requested scan range")
		}
		if _, exists := seen[blockNumber]; exists {
			continue
		}
		seen[blockNumber] = struct{}{}
		numbers = append(numbers, blockNumber)
	}
	headers := make(map[uint64]blockHeader, len(numbers)+1)
	headers[previousHeader.Number] = *previousHeader
	var batchedReceipts *scanReceiptCache
	if reader := service.blockReceiptReader(); reader != nil {
		batchedReceipts, err = service.fetchBatchedScanHeaders(ctx, reader, numbers, headers, expectedReceipts)
		if err != nil {
			_ = service.recordCursorError(ctx, *cursor, safeBlock, err)
			return err
		}
	} else {
		for i, number := range numbers {
			header, fetchErr := service.fetchScanBlockHeader(ctx, number, denseBlocks)
			if fetchErr != nil {
				if i == 0 {
					_ = service.recordCursorError(ctx, *cursor, safeBlock, fetchErr)
				}
				return fetchErr
			}
			headers[number] = header
		}
	}
	endHeader := headers[uint64(toBlock)]
	for _, log := range logs {
		blockNumber, _ := parseHexQuantity(log.BlockNumber)
		if header := headers[blockNumber]; header.Hash != strings.ToLower(log.BlockHash) {
			return fmt.Errorf("market log block %d changed during scan", blockNumber)
		}
	}
	changed, err := service.reconcilePartialScanRange(ctx, *cursor, *previousHeader, headers, fromBlock, toBlock)
	if err != nil || changed {
		return err
	}
	// A re-included logical parent requires canonical inclusion evidence before
	// it may change placement. These rows only stage the checked headers: every
	// scanner receipt below stays unconfirmed until the fresh end check passes.
	if err := service.storeScanHeaders(ctx, headers); err != nil {
		return err
	}

	transactionHashes := uniqueLogTransactionHashes(logs)
	persist := func(writeCtx context.Context, hydrated hydratedMarketTransaction) error {
		// Fresh end-hash validation and bounded confirmation below remain the
		// only publication point for the scanner.
		return service.parseAndPersistMarketTransaction(writeCtx, hydrated, parserContext, "nodit_log_scanner", false)
	}
	if batchedReceipts != nil {
		err = service.consumeBatchedScanTransactions(ctx, transactionHashes, headers, expectedReceipts, batchedReceipts, persist)
	} else {
		err = consumeOrderedMarketReads(ctx, len(transactionHashes), service.settings.HydrationConcurrency,
			func(readCtx context.Context, index int) (hydratedMarketTransaction, error) {
				hash := transactionHashes[index]
				expected := expectedReceipts[hash]
				header, exists := headers[expected.blockNumber]
				if !exists {
					return hydratedMarketTransaction{}, fmt.Errorf("scanner receipt lacks a verified range header")
				}
				return service.readMarketTransactionWithIdentity(readCtx, hash, &header, []receiptExpectation{expected}, denseBlocks.identity(hash))
			}, func(writeCtx context.Context, _ int, hydrated hydratedMarketTransaction) error {
				return persist(writeCtx, hydrated)
			})
	}
	if err != nil {
		if errors.Is(err, ErrParserScopeChanged) {
			// A newly selected pool can have more trades later in this range.
			// All read workers have been cancelled and joined before returning.
			return nil
		}
		_ = service.recordCursorError(ctx, *cursor, safeBlock, err)
		return err
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
	additionalHeaders := make(map[uint64]blockHeader)
	for _, blockNumber := range unconfirmedBlocks {
		if _, exists := headers[uint64(blockNumber)]; !exists {
			header, fetchErr := service.fetchBlockHeader(ctx, uint64(blockNumber))
			if fetchErr != nil {
				return fetchErr
			}
			headers[header.Number] = header
			additionalHeaders[header.Number] = header
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
	freshEnd, err := service.fetchBlockHeader(ctx, uint64(toBlock))
	if err != nil {
		return err
	}
	if freshEnd.Hash != endHeader.Hash {
		return service.rewindChangedScanRange(ctx, *cursor, *previousHeader)
	}
	if !freshEnd.Timestamp.Equal(endHeader.Timestamp) || freshEnd.ParentHash != endHeader.ParentHash {
		return fmt.Errorf("market end header changed without a corresponding hash change")
	}
	// Headers first discovered in the unconfirmed queue must also have their
	// canonical evidence stored before the bounded confirmation call.
	if err := service.storeScanHeaders(ctx, additionalHeaders); err != nil {
		return err
	}
	// Publish only after the stored canonical hashes have been checked, and do
	// so before advancing. A failed confirmation must leave the range available
	// for an idempotent retry rather than strand its receipts behind the cursor.
	if _, err := service.repository.MarkMarketBlockConfirmed(ctx, service.settings.ChainID, toBlock); err != nil {
		if errors.Is(err, store.ErrMarketTradeConfirmationPending) {
			// The bounded batch committed, but verified receipts still remain.
			// Retry on the next scheduled cycle without losing this range.
			return nil
		}
		return err
	}
	poolIDs := make([]int64, 0, len(parserContext.poolByID))
	for id := range parserContext.poolByID {
		poolIDs = append(poolIDs, id)
	}
	tokens := append([]string(nil), parserContext.targetTokens...)
	if err := service.repository.RecordMarketScanCoverage(ctx, store.RecordMarketScanCoverageParams{
		ChainID: service.settings.ChainID, CursorKey: service.settings.CursorKey, ParserVersion: 1,
		ScopePoolIDs: poolIDs, ScopeTokenAddresses: tokens, FromBlock: fromBlock, ThroughBlock: toBlock,
		AfterAt: previousHeader.Timestamp, AfterHash: previousHeader.Hash,
		ThroughAt: endHeader.Timestamp, ThroughHash: endHeader.Hash,
	}); err != nil {
		if errors.Is(err, store.ErrMarketTradeConfirmationPending) {
			// A webhook may have completed another receipt between confirmation
			// and coverage publication. Keep this range for the next bounded pass.
			return nil
		}
		return err
	}
	advanced, err := service.repository.AdvanceMarketChainCursor(
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
	)
	if err != nil {
		return err
	}
	// Emit only after canonical verification, coverage publication and the
	// cursor write all succeed. Empty or deferred rounds are not scan progress.
	// This is one stdout record per completed range, with no extra reads.
	if service.logger != nil {
		service.logger.InfoContext(ctx, "market scan completed",
			"chain_key", service.settings.ChainKey,
			"chain_id", service.settings.ChainID,
			"cursor_key", service.settings.CursorKey,
			"from_block", fromBlock,
			"through_block", toBlock,
			"next_block", advanced.NextBlockNumber,
			"safe_block", safeBlock,
			"safe_observed_at", safeObservedAt,
			"completed_at", time.Now().UTC(),
			"work_duration_ms", time.Since(started).Milliseconds(),
			"backlog_blocks", safeBlock-toBlock,
		)
	}
	return nil
}

func (service *Service) storeScanHeaders(ctx context.Context, headers map[uint64]blockHeader) error {
	numbers := make([]uint64, 0, len(headers))
	for number := range headers {
		numbers = append(numbers, number)
	}
	sort.Slice(numbers, func(i, j int) bool { return numbers[i] < numbers[j] })
	for _, number := range numbers {
		header := headers[number]
		timestamp := header.Timestamp
		if _, err := service.repository.UpsertMarketScannedBlock(ctx, store.UpsertMarketScannedBlockParams{
			ChainKey: service.settings.ChainKey, ChainID: service.settings.ChainID, CursorKey: service.settings.CursorKey,
			BlockNumber: int64(header.Number), BlockHash: header.Hash, ParentHash: header.ParentHash,
			BlockTimestamp: &timestamp, ScannedAt: service.now().UTC(),
		}); err != nil {
			return err
		}
	}
	return nil
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
	value, _, err := service.verifyCursorContinuityWithHeader(ctx, cursor)
	return value, err
}

func (service *Service) verifyCursorContinuityWithHeader(
	ctx context.Context,
	cursor store.MarketChainCursor,
) (*store.MarketChainCursor, *blockHeader, error) {
	if cursor.NextBlockNumber <= 0 || cursor.LastBlockHash == nil {
		return &cursor, nil, nil
	}
	previousHeader, err := service.fetchBlockHeader(
		ctx,
		uint64(cursor.NextBlockNumber-1),
	)
	if err != nil {
		return nil, nil, err
	}
	if previousHeader.Hash == *cursor.LastBlockHash {
		return &cursor, &previousHeader, nil
	}
	candidates, err := service.repository.ListCanonicalMarketScannedBlocks(
		ctx,
		service.settings.ChainID,
		service.settings.CursorKey,
		cursor.NextBlockNumber-1,
		service.settings.ReorgLookback,
	)
	if err != nil {
		return nil, nil, err
	}
	for _, candidate := range candidates {
		header, fetchErr := service.fetchBlockHeader(ctx, uint64(candidate.BlockNumber))
		if fetchErr != nil {
			return nil, nil, fetchErr
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
			return nil, nil, reconcileErr
		}
		return &reconciled.Cursor, &header, nil
	}
	return nil, nil, ErrNoCanonicalAncestor
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
		if len(logs) > maxMarketScannerLogs-len(result) {
			return nil, fmt.Errorf("scanner log batch exceeds bounded capacity")
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
		if len(logs) > maxMarketScannerLogs-len(result) {
			return fmt.Errorf("scanner log batch exceeds bounded capacity")
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
	targetTopics := make([]string, 0, len(parserContext.targetTokens))
	for _, token := range parserContext.targetTokens {
		targetTopics = append(targetTopics, addressTopic(token))
	}
	factoriesByTopic := make(map[string][]string)
	for _, deployment := range marketprotocol.Deployments(service.settings.ChainKey) {
		topic := ""
		switch deployment.Adapter {
		case marketparse.AdapterV2:
			topic = marketparse.V2FactoryEventTopic()
		case marketparse.AdapterV3:
			topic = marketparse.V3FactoryEventTopic()
		case marketparse.AdapterAlgebra:
			topic = marketparse.AlgebraFactoryEventTopic()
		case marketparse.AdapterSolidly:
			topic = marketparse.SolidlyFactoryEventTopic()
		}
		if topic != "" {
			factoriesByTopic[topic] = append(
				factoriesByTopic[topic],
				deployment.Factory,
			)
		}
	}
	factoryTopics := make([]string, 0, len(factoriesByTopic))
	for topic := range factoriesByTopic {
		factoryTopics = append(factoryTopics, topic)
	}
	sort.Strings(factoryTopics)
	for _, topic := range factoryTopics {
		factories := factoriesByTopic[topic]
		for _, batch := range stringBatches(
			targetTopics,
			logAddressBatchSize,
		) {
			if err := appendLogs(
				uniqueStrings(factories),
				[]chain.LogTopic{{topic}, chain.LogTopic(batch)},
			); err != nil {
				return nil, err
			}
			if err := appendLogs(
				uniqueStrings(factories),
				[]chain.LogTopic{{topic}, nil, chain.LogTopic(batch)},
			); err != nil {
				return nil, err
			}
		}
	}
	isBSC := strings.EqualFold(service.settings.ChainKey, "bsc")
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
	for _, batch := range stringBatches(targetTopics, logAddressBatchSize) {
		if !isBSC {
			break
		}
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
	if isBSC {
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
		relevantFourLogs, err := relevantEmitterLogs(
			fourLogs,
			parserContext,
			service.settings.ChainID,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, relevantFourLogs...)
	}
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
	return decodeRequestedBlockHeader(blockNumber, block)
}

func decodeRequestedBlockHeader(blockNumber uint64, block map[string]any) (blockHeader, error) {
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
	chainID int64,
) ([]chain.RPCLog, error) {
	result := make([]chain.RPCLog, 0, len(logs))
	for _, rawLog := range logs {
		log, err := marketparse.LogFromRPC(rawLog)
		if err != nil {
			return nil, err
		}
		events, err := parserContext.parser.Parse(
			marketparse.Receipt{
				ChainID:          uint64(chainID),
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
