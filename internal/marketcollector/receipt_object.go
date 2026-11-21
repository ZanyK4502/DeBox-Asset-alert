package marketcollector

import (
	"encoding/json"
	"fmt"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
)

type marketReceiptEnvelope struct {
	TransactionHash  string          `json:"transactionHash"`
	TransactionIndex string          `json:"transactionIndex"`
	BlockHash        string          `json:"blockHash"`
	BlockNumber      string          `json:"blockNumber"`
	From             string          `json:"from"`
	To               string          `json:"to"`
	Status           string          `json:"status"`
	Logs             json.RawMessage `json:"logs"`
}

// The RPC client already decoded the JSON object. Reuse its immutable strings
// rather than encoding and decoding a second full log array. The one encoded
// receipt retained by the caller is still validated and persisted unchanged.
func decodeReceiptObject(object map[string]any, chainID int64) (marketparse.Receipt, error) {
	if object == nil {
		return marketparse.Receipt{}, fmt.Errorf("transaction receipt is not available")
	}
	var raw marketReceiptEnvelope
	for key, target := range map[string]*string{
		"transactionHash": &raw.TransactionHash, "transactionIndex": &raw.TransactionIndex,
		"blockHash": &raw.BlockHash, "blockNumber": &raw.BlockNumber,
		"from": &raw.From, "to": &raw.To, "status": &raw.Status,
	} {
		value, err := receiptObjectString(object, key)
		if err != nil {
			return marketparse.Receipt{}, err
		}
		*target = value
	}
	var logs []chain.RPCLog
	switch values := object["logs"].(type) {
	case []chain.RPCLog:
		if values == nil || len(values) > maxMarketReceiptLogs {
			return marketparse.Receipt{}, fmt.Errorf("receipt logs are missing or exceed bounded capacity")
		}
		logs = values
	case []any:
		if values == nil || len(values) > maxMarketReceiptLogs {
			return marketparse.Receipt{}, fmt.Errorf("receipt logs are missing or exceed bounded capacity")
		}
		logs = make([]chain.RPCLog, 0, len(values))
		for _, value := range values {
			log, ok := value.(map[string]any)
			if !ok {
				return marketparse.Receipt{}, fmt.Errorf("receipt log must be an object")
			}
			decoded, err := receiptObjectLog(log)
			if err != nil {
				return marketparse.Receipt{}, err
			}
			logs = append(logs, decoded)
		}
	default:
		return marketparse.Receipt{}, fmt.Errorf("receipt logs must be a complete array")
	}
	return decodedMarketReceipt(raw, logs, chainID)
}

func receiptObjectString(object map[string]any, key string) (string, error) {
	value := object[key]
	if value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("receipt %s must be a string", key)
	}
	return text, nil
}

func receiptObjectLog(object map[string]any) (chain.RPCLog, error) {
	var log chain.RPCLog
	for key, target := range map[string]*string{
		"address": &log.Address, "data": &log.Data, "blockNumber": &log.BlockNumber,
		"transactionHash": &log.TransactionHash, "transactionIndex": &log.TransactionIndex,
		"blockHash": &log.BlockHash, "logIndex": &log.LogIndex,
	} {
		value, err := receiptObjectString(object, key)
		if err != nil {
			return chain.RPCLog{}, err
		}
		*target = value
	}
	if value := object["removed"]; value != nil {
		removed, ok := value.(bool)
		if !ok {
			return chain.RPCLog{}, fmt.Errorf("receipt log removed must be a boolean")
		}
		log.Removed = removed
	}
	switch topics := object["topics"].(type) {
	case nil:
	case []string:
		log.Topics = topics
	case []any:
		log.Topics = make([]string, 0, len(topics))
		for _, value := range topics {
			text, ok := value.(string)
			if !ok {
				return chain.RPCLog{}, fmt.Errorf("receipt log topic must be a string")
			}
			log.Topics = append(log.Topics, text)
		}
	default:
		return chain.RPCLog{}, fmt.Errorf("receipt log topics must be an array")
	}
	return log, nil
}
