package marketcollector

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
)

const (
	maxBatchSelectedTransactions = 64
	maxBatchSelectedReceiptBytes = 1 << 20
	maxScanSelectedReceiptBytes  = 8 << 20
	maxScanSelectedReceipts      = 4096
	maxScanFallbackIdentities    = 8192
	scanBlockReceiptTimeout      = 6 * time.Second
)

type marketBlockReceiptReader interface {
	MarketFullBlock(context.Context, uint64, string, string) (map[string]any, error)
	MarketBlockReceipts(context.Context, string, string, string) ([]any, error)
}

type marketBlockPairReader interface {
	MarketBlockAndReceipts(context.Context, uint64, string, string, string) (map[string]any, []any, error)
	MarketBlockAndTransactionReceipt(context.Context, uint64, string, string, string, string) (map[string]any, map[string]any, error)
}

func (service *Service) blockReceiptReader() marketBlockReceiptReader {
	if service.settings.ChainKey == "bsc" || service.settings.ChainKey == "base" {
		reader, _ := service.chain.(marketBlockReceiptReader)
		return reader
	}
	return nil
}

type batchedScanHeader struct {
	header     blockHeader
	receipts   map[string][]byte
	identities map[string]map[string]any
	bytes      int
}

type scanBlockMatches struct {
	count           int
	blockHash       string
	transactionHash string // Used only when the block has one match.
}

// Store only the lossless encoding between the header check and the write
// pass. Block-order writes can change which initialized pool is auto-selected.
func (service *Service) fetchBatchedScanHeaders(ctx context.Context, reader marketBlockReceiptReader, numbers []uint64, headers map[uint64]blockHeader, expected map[string]receiptExpectation) (*scanReceiptCache, error) {
	matches := make(map[uint64]scanBlockMatches, len(numbers))
	minimum := minFullBlockTrackedTransactions
	var paired marketBlockPairReader
	if service.settings.ChainKey == "base" {
		minimum = 1
		paired, _ = reader.(marketBlockPairReader)
	}
	for hash, receipt := range expected {
		entry := matches[receipt.blockNumber]
		if entry.count == 0 {
			entry.blockHash = receipt.blockHash
			entry.transactionHash = hash
		} else if entry.blockHash != receipt.blockHash {
			// Conflicting logs cannot supply the hash for a paired request.
			entry.blockHash = ""
		}
		entry.count++
		matches[receipt.blockNumber] = entry
	}
	var stopPairs, stopFullBlocks, stopBlockReceipts atomic.Bool
	var encoders scanReceiptEncoders
	cache := &scanReceiptCache{receipts: make(map[string][]byte), identities: make(map[string]map[string]any)}
	err := consumeOrderedMarketReads(ctx, len(numbers), service.settings.HydrationConcurrency,
		func(readCtx context.Context, index int) (batchedScanHeader, error) {
			number := numbers[index]
			entry := matches[number]
			if entry.count >= minimum && entry.count <= maxBatchSelectedTransactions && !stopFullBlocks.Load() {
				if paired != nil && entry.blockHash != "" && !stopPairs.Load() && !stopBlockReceipts.Load() {
					if batch, ok := service.fetchScanBlockPair(readCtx, paired, number, entry, expected, &encoders); ok {
						return batch, nil
					}
					// An invalid pair cannot change the original read path.
					// At most one other pair can already be in flight.
					stopPairs.Store(true)
				}
				if batch, ok := service.fetchScanBlockReceipts(readCtx, reader, number, entry.count, expected, &stopFullBlocks, &stopBlockReceipts, &encoders); ok {
					return batch, nil
				}
			}
			header, err := service.fetchBlockHeader(readCtx, number)
			return batchedScanHeader{header: header}, err
		}, func(_ context.Context, _ int, result batchedScanHeader) error {
			headers[result.header.Number] = result.header
			if len(result.receipts) > 0 && cache.retain(result.receipts, result.bytes) {
				if len(cache.receipts) == maxScanSelectedReceipts || cache.bytes == maxScanSelectedReceiptBytes {
					stopBlockReceipts.Store(true)
				}
				return nil
			}
			if len(result.identities) <= maxScanFallbackIdentities-len(cache.identities) {
				for hash, identity := range result.identities {
					cache.identities[hash] = identity
				}
			}
			return nil
		})
	return cache, err
}

func (service *Service) fetchScanBlockPair(ctx context.Context, reader marketBlockPairReader, number uint64, entry scanBlockMatches, expected map[string]receiptExpectation, encoders *scanReceiptEncoders) (batchedScanHeader, bool) {
	readCtx, cancel := context.WithTimeout(ctx, scanBlockReceiptTimeout)
	defer cancel()
	key, fallback := service.settings.ChainKey, service.settings.ChainFallback
	var block map[string]any
	var receipts []any
	var err error
	if entry.count == 1 {
		var receipt map[string]any
		block, receipt, err = reader.MarketBlockAndTransactionReceipt(readCtx, number, entry.blockHash, entry.transactionHash, key, fallback)
		receipts = []any{receipt}
	} else {
		block, receipts, err = reader.MarketBlockAndReceipts(readCtx, number, entry.blockHash, key, fallback)
	}
	if err != nil {
		return batchedScanHeader{}, false
	}
	header, err := decodeRequestedBlockHeader(number, block)
	if err != nil || header.Hash != entry.blockHash {
		return batchedScanHeader{}, false
	}
	identities := verifiedScanBlockIdentities(header, block["transactions"], expected, entry.count)
	if len(identities) != entry.count {
		return batchedScanHeader{}, false
	}
	packed, size := encoders.pack(service.selectScanBlockReceipts(readCtx, header, identities, receipts, expected))
	if len(packed) != entry.count {
		return batchedScanHeader{}, false
	}
	return batchedScanHeader{header: header, receipts: packed, identities: identities, bytes: size}, true
}

func (service *Service) fetchScanBlockReceipts(ctx context.Context, reader marketBlockReceiptReader, number uint64, count int, expected map[string]receiptExpectation, stopFullBlocks, stopBlockReceipts *atomic.Bool, encoders *scanReceiptEncoders) (batchedScanHeader, bool) {
	readCtx, cancel := context.WithTimeout(ctx, scanBlockReceiptTimeout)
	defer cancel()
	header, identities, err := service.fetchScanBlockIdentityHeader(readCtx, reader, number, count, expected)
	if err != nil {
		if !errors.Is(err, chain.ErrMarketRPCResponseTooLarge) {
			stopFullBlocks.Store(true)
		}
		return batchedScanHeader{}, false
	}
	result := batchedScanHeader{header: header, identities: identities}
	if len(identities) == 0 || stopBlockReceipts.Load() {
		return result, true
	}
	// Fetch a single match alongside the full-block reads. Deferring every
	// singleton until after the header barrier creates a long serial tail.
	if len(identities) == 1 {
		for hash, identity := range identities {
			receipt, err := service.readMarketTransactionUsingIdentity(readCtx, hash, &header, []receiptExpectation{expected[hash]}, identity, true)
			if err == nil && len(receipt.receiptPayload) <= maxBatchSelectedReceiptBytes {
				result.receipts, result.bytes = encoders.pack(map[string]hydratedMarketTransaction{hash: receipt})
			}
		}
		return result, true
	}
	values, err := reader.MarketBlockReceipts(readCtx, header.Hash, service.settings.ChainKey, service.settings.ChainFallback)
	if err != nil {
		// Full-block identities remain usable for individual receipts.
		if !errors.Is(err, chain.ErrMarketRPCResponseTooLarge) {
			stopBlockReceipts.Store(true)
		}
		return result, true
	}
	result.receipts, result.bytes = encoders.pack(service.selectScanBlockReceipts(readCtx, header, identities, values, expected))
	return result, true
}

func (service *Service) selectScanBlockReceipts(ctx context.Context, header blockHeader, identities map[string]map[string]any, values []any, expected map[string]receiptExpectation) map[string]hydratedMarketTransaction {
	selected := make(map[string]hydratedMarketTransaction, len(identities))
	var bytes int
	for _, value := range values {
		receipt, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		hash := rpcString(receipt, "transactionHash")
		identity, tracked := identities[hash]
		if !tracked {
			continue
		}
		if _, duplicate := selected[hash]; duplicate || ctx.Err() != nil {
			return nil
		}
		hydrated, err := service.hydrateMarketReceipt(ctx, hash, &header, []receiptExpectation{expected[hash]}, identity, receipt)
		if err != nil || len(hydrated.receiptPayload) > maxBatchSelectedReceiptBytes-bytes {
			return nil
		}
		bytes += len(hydrated.receiptPayload)
		selected[hash] = hydrated
	}
	if len(selected) != len(identities) {
		return nil
	}
	return selected
}
