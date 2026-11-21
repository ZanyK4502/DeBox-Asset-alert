package marketcollector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
)

type scanReceiptTestChain struct {
	*scannerPipelineChain
	byBlock            map[string][]any
	failBatch          bool
	badIdentity        bool
	batches            atomic.Int32
	individual         atomic.Int32
	transactionsByHash atomic.Int32
}

func (c *scanReceiptTestChain) MarketFullBlock(ctx context.Context, number uint64, key, fallback string) (map[string]any, error) {
	block, err := c.BlockByNumber(ctx, number, true, key, fallback)
	if err != nil {
		return nil, err
	}
	var transactions []any
	for _, receipt := range c.byBlock[rpcString(block, "hash")] {
		identity := c.transactions[rpcString(receipt.(map[string]any), "transactionHash")]
		if c.badIdentity {
			wrong := make(map[string]any, len(identity))
			for key, value := range identity {
				wrong[key] = value
			}
			wrong["from"] = initializedToken
			identity = wrong
		}
		transactions = append(transactions, identity)
	}
	block["transactions"] = transactions
	return block, nil
}

func (c *scanReceiptTestChain) MarketBlockReceipts(_ context.Context, hash, _, _ string) ([]any, error) {
	c.batches.Add(1)
	if c.failBatch {
		return nil, errors.New("block receipt endpoint is unavailable")
	}
	return c.byBlock[hash], nil
}

func (c *scanReceiptTestChain) RPCTransactionByHash(ctx context.Context, hash, key, fallback string) (map[string]any, error) {
	c.transactionsByHash.Add(1)
	return c.scannerPipelineChain.RPCTransactionByHash(ctx, hash, key, fallback)
}

func (c *scanReceiptTestChain) TransactionReceipt(ctx context.Context, hash, key, fallback string) (map[string]any, error) {
	c.individual.Add(1)
	return c.scannerPipelineChain.TransactionReceipt(ctx, hash, key, fallback)
}

func newScanReceiptTestChain(counts []int, padding string) (*scanReceiptTestChain, []uint64) {
	c := &scanReceiptTestChain{scannerPipelineChain: &scannerPipelineChain{
		fakeChain:    &fakeChain{blocks: make(map[uint64]map[string]any)},
		transactions: make(map[string]map[string]any), receipts: make(map[string]map[string]any),
		headerCalls: make(map[uint64]int),
	}, byBlock: make(map[string][]any)}
	var numbers []uint64
	for i, count := range counts {
		number := uint64(100 + i)
		block := fmt.Sprintf("0x%x", number)
		blockHash := fmt.Sprintf("0x%064x", number)
		numbers = append(numbers, number)
		c.blocks[number] = map[string]any{
			"number": block, "hash": blockHash, "parentHash": fmt.Sprintf("0x%064x", number-1), "timestamp": fmt.Sprintf("0x%x", 1_700_000_000+number),
		}
		for j := range count {
			// Global hash order deliberately runs backwards across blocks.
			hash := fmt.Sprintf("0x%064x", 1000-i*64+j)
			index := fmt.Sprintf("0x%x", j)
			log := chain.RPCLog{
				Address: initializedToken, Data: "0x00", BlockNumber: block,
				TransactionHash: hash, TransactionIndex: index, BlockHash: blockHash, LogIndex: index,
			}
			c.logs = append(c.logs, log)
			c.transactions[hash] = map[string]any{
				"hash": hash, "blockHash": blockHash, "blockNumber": block, "transactionIndex": index, "from": initializedQuote,
			}
			c.receipts[hash] = map[string]any{
				"transactionHash": hash, "blockHash": blockHash, "blockNumber": block, "transactionIndex": index,
				"from": initializedQuote, "to": initializedToken, "status": "0x1", "logs": []chain.RPCLog{log}, "fixturePadding": padding,
			}
			c.byBlock[blockHash] = append(c.byBlock[blockHash], c.receipts[hash])
		}
	}
	return c, numbers
}

func requireScanReceiptCache(t *testing.T, service *Service, c *scanReceiptTestChain, numbers []uint64) {
	t.Helper()
	expectations, err := expectedScannerReceipts(c.logs)
	if err != nil {
		t.Fatal(err)
	}
	headers := make(map[uint64]blockHeader)
	ctx := context.Background()
	cache, err := service.fetchBatchedScanHeaders(ctx, service.blockReceiptReader(), numbers, headers, expectations)
	if err != nil {
		t.Fatal(err)
	}
	hashes := uniqueLogTransactionHashes(c.logs)
	written := 0
	err = service.consumeBatchedScanTransactions(ctx, hashes, headers, expectations, cache, func(_ context.Context, item hydratedMarketTransaction) error {
		hash := hashes[written]
		want, err := json.Marshal(c.receipts[hash])
		if err != nil {
			return err
		}
		if item.receipt.TransactionHash != hash || !bytes.Equal(item.receiptPayload, want) {
			return fmt.Errorf("receipt order or payload changed at index %d", written)
		}
		written++
		return nil
	})
	if err != nil || written != len(hashes) {
		t.Fatalf("consumed %d/%d receipts: %v", written, len(hashes), err)
	}
	if cache.bytes != 0 || len(cache.receipts) != 0 || len(cache.identities) != 0 {
		t.Fatal("consumed scan retained its cache")
	}
}

func TestBatchedScanCachePreservesGlobalWriteOrderBeyondRawWindowLimit(t *testing.T) {
	const size = 240 << 10
	c, numbers := newScanReceiptTestChain([]int{4, 4, 4, 4, 4, 4, 4, 4, 4}, strings.Repeat("r", size))
	if len(c.logs)*size <= maxScanSelectedReceiptBytes {
		t.Fatal("fixture does not exceed old uncompressed window limit")
	}
	service := New(Dependencies{Chain: c}, Settings{ChainKey: "bsc", ChainID: 56, HydrationConcurrency: 2})
	requireScanReceiptCache(t, service, c, numbers)
	if c.batches.Load() != int32(len(numbers)) || c.individual.Load() != 0 || c.transactionsByHash.Load() != 0 {
		t.Fatal("compressed window fell back to individual reads")
	}
}

func TestBatchedBaseSparseBlocksReuseTransactionIdentities(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch_endpoint_failure_%t", fail), func(t *testing.T) {
			c, numbers := newScanReceiptTestChain([]int{1, 2, 3}, "")
			c.failBatch = fail
			service := New(Dependencies{Chain: c}, Settings{ChainKey: "base", ChainID: 8453, HydrationConcurrency: 2})
			requireScanReceiptCache(t, service, c, numbers)
			wantIndividual := int32(1)
			if fail {
				wantIndividual = 6
			} else if c.batches.Load() != 2 {
				t.Fatalf("got %d block receipt reads, want two", c.batches.Load())
			}
			if c.individual.Load() != wantIndividual || c.transactionsByHash.Load() != 0 {
				t.Fatalf("individual receipts=%d transactions=%d", c.individual.Load(), c.transactionsByHash.Load())
			}
		})
	}
}

func TestBaseSparseBlockChecksMismatchedIdentityWithoutReadingReceiptTwice(t *testing.T) {
	c, numbers := newScanReceiptTestChain([]int{1, 2}, "")
	c.badIdentity = true
	service := New(Dependencies{Chain: c}, Settings{ChainKey: "base", ChainID: 8453, HydrationConcurrency: 2})
	requireScanReceiptCache(t, service, c, numbers)
	if c.batches.Load() != 1 || c.individual.Load() != 3 || c.transactionsByHash.Load() != 3 {
		t.Fatalf("batch=%d individual receipt=%d individual transaction=%d", c.batches.Load(), c.individual.Load(), c.transactionsByHash.Load())
	}
}

func TestBatchedScanCachePreservesExactReceiptFieldNames(t *testing.T) {
	c, numbers := newScanReceiptTestChain([]int{2}, "")
	for _, receipt := range c.receipts {
		receipt["transactionhash"] = "invalid alias"
	}
	service := New(Dependencies{Chain: c}, Settings{ChainKey: "base", ChainID: 8453, HydrationConcurrency: 2})
	requireScanReceiptCache(t, service, c, numbers)
	if c.batches.Load() != 1 || c.individual.Load() != 0 {
		t.Fatal("expected the cache to preserve the exactly named fields")
	}
}

func TestScanReceiptCacheCanRetainSmallerBlockAfterOversize(t *testing.T) {
	cache := &scanReceiptCache{receipts: make(map[string][]byte), bytes: maxScanSelectedReceiptBytes - 7}
	if cache.retain(map[string][]byte{"too_large": make([]byte, 8)}, 8) {
		t.Fatal("accepted block exceeding cache byte limit")
	}
	if !cache.retain(map[string][]byte{"fits": make([]byte, 7)}, 7) || cache.bytes != maxScanSelectedReceiptBytes || len(cache.receipts) != 1 {
		t.Fatal("oversize block prevented a later fitting block from being cached")
	}
}
