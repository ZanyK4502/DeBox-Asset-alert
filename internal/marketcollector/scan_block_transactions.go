package marketcollector

import (
	"context"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
)

const (
	minFullBlockTrackedTransactions = 4
	maxFullBlockReadsPerScan        = 16
	maxScanTransactionIdentities    = 256
	scanFullBlockTimeout            = 3 * time.Second
)

type scanBlockTransactions struct {
	expected   map[string]receiptExpectation
	counts     map[uint64]int
	identities map[string]map[string]any
	reads      int
}

func newScanBlockTransactions(chainKey string, expected map[string]receiptExpectation) *scanBlockTransactions {
	if (chainKey != "ethereum" && chainKey != "polygon") || len(expected) < minFullBlockTrackedTransactions {
		return nil
	}
	blocks := &scanBlockTransactions{
		expected: expected, counts: make(map[uint64]int), identities: make(map[string]map[string]any),
	}
	for _, receipt := range expected {
		blocks.counts[receipt.blockNumber]++
	}
	return blocks
}

func (blocks *scanBlockTransactions) identity(hash string) map[string]any {
	if blocks == nil {
		return nil
	}
	return blocks.identities[hash]
}

func (service *Service) fetchScanBlockHeader(ctx context.Context, number uint64, blocks *scanBlockTransactions) (blockHeader, error) {
	if blocks != nil && blocks.shouldReadFullBlock(number) {
		blocks.reads++
		readCtx, cancel := context.WithTimeout(ctx, scanFullBlockTimeout)
		block, err := service.chain.BlockByNumber(readCtx, number, true, service.settings.ChainKey, service.settings.ChainFallback)
		cancel()
		if err == nil {
			header, err := decodeRequestedBlockHeader(number, block)
			if err == nil {
				blocks.remember(header, block["transactions"])
				return header, nil
			}
		}
		// A slow, unavailable or oversized full block can still have a usable
		// header. Keep the original header and individual transaction path.
		if err := ctx.Err(); err != nil {
			return blockHeader{}, err
		}
	}
	return service.fetchBlockHeader(ctx, number)
}

func (blocks *scanBlockTransactions) shouldReadFullBlock(number uint64) bool {
	count := blocks.counts[number]
	return count >= minFullBlockTrackedTransactions && blocks.reads < maxFullBlockReadsPerScan &&
		count <= maxScanTransactionIdentities-len(blocks.identities)
}

func (blocks *scanBlockTransactions) remember(header blockHeader, rawTransactions any) {
	for hash, identity := range scanBlockIdentities(header, rawTransactions, blocks.expected, blocks.counts[header.Number]) {
		blocks.identities[hash] = identity
	}
}

func scanBlockIdentities(header blockHeader, rawTransactions any, expectedReceipts map[string]receiptExpectation, count int) map[string]map[string]any {
	transactions, ok := rawTransactions.([]any)
	if !ok {
		return nil
	}
	// Keep a small, complete set for the selected hashes only. Nothing from
	// the full payload survives this read except the compact identities.
	found := make(map[string]map[string]any, count)
	for _, raw := range transactions {
		transaction, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		hash := rpcString(transaction, "hash")
		expected, selected := expectedReceipts[hash]
		if !selected || expected.blockNumber != header.Number || expected.blockHash != header.Hash {
			continue
		}
		if _, duplicate := found[hash]; duplicate {
			return nil
		}
		identity, err := compactMarketTransactionIdentity(transaction)
		if err != nil || !isCompleteBlockTransactionIdentity(identity) {
			return nil
		}
		found[hash] = identity
	}
	if len(found) != count {
		return nil
	}
	return found
}

func isCompleteBlockTransactionIdentity(identity map[string]any) bool {
	_, hashErr := chain.ValidateTransactionHash(rpcString(identity, "blockHash"))
	_, numberErr := parseHexQuantity(rpcString(identity, "blockNumber"))
	_, indexErr := parseHexQuantity(rpcString(identity, "transactionIndex"))
	_, senderErr := chain.ValidateAddress(rpcString(identity, "from"))
	return hashErr == nil && numberErr == nil && indexErr == nil && senderErr == nil
}
