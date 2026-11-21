package marketcollector

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

type scanBlockPairFixture struct {
	*scanReceiptTestChain
	failure string
	pairs   atomic.Int32
	singles atomic.Int32
}

func (fixture *scanBlockPairFixture) MarketBlockAndReceipts(ctx context.Context, number uint64, blockHash, key, fallback string) (map[string]any, []any, error) {
	fixture.pairs.Add(1)
	if fixture.failure == "unsupported" {
		return nil, nil, errors.New("test: provider rejected paired read")
	}
	block, err := fixture.MarketFullBlock(ctx, number, key, fallback)
	if err != nil {
		return nil, nil, err
	}
	receipts := fixture.byBlock[blockHash]
	switch fixture.failure {
	case "short":
		receipts = receipts[:len(receipts)-1]
	case "reorg":
		block["hash"] = "0x" + strings.Repeat("a", 64)
	}
	return block, receipts, nil
}

func (fixture *scanBlockPairFixture) MarketBlockAndTransactionReceipt(ctx context.Context, number uint64, _, txHash, key, fallback string) (map[string]any, map[string]any, error) {
	fixture.singles.Add(1)
	block, err := fixture.MarketFullBlock(ctx, number, key, fallback)
	if err != nil {
		return nil, nil, err
	}
	receipt := fixture.receipts[txHash]
	if fixture.failure == "wrong transaction" {
		copyReceipt := make(map[string]any, len(receipt))
		for key, value := range receipt {
			copyReceipt[key] = value
		}
		copyReceipt["transactionHash"] = "0x" + strings.Repeat("a", 64)
		receipt = copyReceipt
	}
	return block, receipt, nil
}

func TestBaseScanBlockPairsPreserveTheVerifiedReceipt(t *testing.T) {
	for _, test := range []struct {
		name, chain, failure string
		transactions         int
		pairs, singles       int32
		blockFallbacks       int32
		singleFallbacks      int32
	}{
		{name: "block", chain: "base", transactions: 2, pairs: 1},
		{name: "single", chain: "base", transactions: 1, singles: 1},
		{name: "unsupported", chain: "base", failure: "unsupported", transactions: 2, pairs: 1, blockFallbacks: 1},
		{name: "incomplete", chain: "base", failure: "short", transactions: 2, pairs: 1, blockFallbacks: 1},
		{name: "changed block", chain: "base", failure: "reorg", transactions: 2, pairs: 1, blockFallbacks: 1},
		{name: "wrong singleton", chain: "base", failure: "wrong transaction", transactions: 1, singles: 1, singleFallbacks: 1},
		{name: "BSC route", chain: "bsc", transactions: 4, blockFallbacks: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, numbers := newScanReceiptTestChain([]int{test.transactions}, "")
			provider := &scanBlockPairFixture{scanReceiptTestChain: fixture, failure: test.failure}
			chainID := int64(8453)
			if test.chain == "bsc" {
				chainID = 56
			}
			service := New(Dependencies{Chain: provider}, Settings{ChainKey: test.chain, ChainID: chainID, HydrationConcurrency: 2})
			requireScanReceiptCache(t, service, fixture, numbers)
			if provider.pairs.Load() != test.pairs || provider.singles.Load() != test.singles ||
				fixture.batches.Load() != test.blockFallbacks || fixture.individual.Load() != test.singleFallbacks ||
				fixture.transactionsByHash.Load() != 0 {
				t.Fatalf("read counts pairs=%d single pairs=%d block fallback=%d single fallback=%d individual transaction=%d",
					provider.pairs.Load(), provider.singles.Load(), fixture.batches.Load(), fixture.individual.Load(), fixture.transactionsByHash.Load())
			}
		})
	}
}
