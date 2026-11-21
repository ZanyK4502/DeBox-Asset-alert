package marketcollector

import (
	"fmt"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
)

// These bounds apply to a single in-memory receipt, never to a historical range.
// Exceeding them fails the batch rather than saving a partial transaction.
const (
	maxMarketReceiptLogs = 10000
	maxMarketScannerLogs = 100000
)

type receiptExpectation struct {
	blockNumber uint64
	blockHash   string
	logIndices  map[uint64]struct{}
	logContents map[uint64]string
}

type marketReceiptIdentityMismatch string

func (err marketReceiptIdentityMismatch) Error() string { return string(err) }

func validateHydratedReceipt(requested string, transaction map[string]any, receipt marketparse.Receipt, expected []receiptExpectation) error {
	hash, err := chain.ValidateTransactionHash(requested)
	if err != nil || receipt.TransactionHash != hash || transaction == nil {
		return fmt.Errorf("receipt does not identify the requested transaction")
	}
	if rpcString(transaction, "hash") != hash || rpcString(transaction, "blockHash") != receipt.BlockHash {
		return marketReceiptIdentityMismatch("transaction and receipt inclusion differ")
	}
	number, err := parseHexQuantity(rpcString(transaction, "blockNumber"))
	if err != nil || number != receipt.BlockNumber {
		return marketReceiptIdentityMismatch("transaction and receipt block numbers differ")
	}
	index, err := parseHexQuantity(rpcString(transaction, "transactionIndex"))
	if err != nil || index != receipt.TransactionIndex {
		return marketReceiptIdentityMismatch("transaction and receipt indices differ")
	}
	from, err := chain.ValidateAddress(rpcString(transaction, "from"))
	if err != nil || from != receipt.From {
		return marketReceiptIdentityMismatch("transaction and receipt senders differ")
	}
	if len(expected) > 0 {
		e := expected[0]
		if receipt.BlockNumber != e.blockNumber || receipt.BlockHash != strings.ToLower(e.blockHash) {
			return fmt.Errorf("receipt moved outside the scanner's verified inclusion")
		}
		seen := make(map[uint64]struct{}, len(receipt.Logs))
		for _, log := range receipt.Logs {
			seen[log.LogIndex] = struct{}{}
			if content, required := e.logContents[log.LogIndex]; required && content != receiptLogContent(log.Address, log.Topics, log.Data) {
				return fmt.Errorf("receipt contents differ from scanner log %d", log.LogIndex)
			}
		}
		for logIndex := range e.logIndices {
			if _, exists := seen[logIndex]; !exists {
				return fmt.Errorf("receipt is missing scanner log %d", logIndex)
			}
		}
	}
	return nil
}

func receiptLogContent(address string, topics []string, data string) string {
	return strings.ToLower(address + "|" + strings.Join(topics, ",") + "|" + data)
}

func expectedScannerReceipts(logs []chain.RPCLog) (map[string]receiptExpectation, error) {
	if len(logs) > maxMarketScannerLogs {
		return nil, fmt.Errorf("scanner log batch exceeds bounded capacity")
	}
	result := make(map[string]receiptExpectation)
	for _, log := range logs {
		hash, err := chain.ValidateTransactionHash(log.TransactionHash)
		if err != nil || log.Removed {
			return nil, fmt.Errorf("scanner received an invalid transaction inclusion")
		}
		number, err := parseHexQuantity(log.BlockNumber)
		if err != nil {
			return nil, err
		}
		index, err := parseHexQuantity(log.LogIndex)
		if err != nil {
			return nil, err
		}
		blockHash, err := chain.ValidateTransactionHash(log.BlockHash)
		if err != nil {
			return nil, err
		}
		e, exists := result[hash]
		if !exists {
			e = receiptExpectation{blockNumber: number, blockHash: blockHash, logIndices: map[uint64]struct{}{}, logContents: map[uint64]string{}}
		} else if e.blockNumber != number || e.blockHash != blockHash {
			return nil, fmt.Errorf("scanner returned conflicting transaction inclusions")
		}
		content := receiptLogContent(log.Address, log.Topics, log.Data)
		if old, exists := e.logContents[index]; exists && old != content {
			return nil, fmt.Errorf("scanner returned conflicting duplicate log %d", index)
		}
		e.logIndices[index] = struct{}{}
		e.logContents[index] = content
		if len(e.logIndices) > maxMarketReceiptLogs {
			return nil, fmt.Errorf("scanner transaction exceeds bounded receipt capacity")
		}
		result[hash] = e
	}
	return result, nil
}
