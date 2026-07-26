package chain

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

var topicPattern = regexp.MustCompile(`^0x[a-fA-F0-9]{64}$`)

type LogTopic []string

type LogFilter struct {
	FromBlock *uint64
	ToBlock   *uint64
	BlockHash string
	Addresses []string
	Topics    []LogTopic
}

type RPCLog struct {
	Address          string   `json:"address"`
	Topics           []string `json:"topics"`
	Data             string   `json:"data"`
	BlockNumber      string   `json:"blockNumber"`
	TransactionHash  string   `json:"transactionHash"`
	TransactionIndex string   `json:"transactionIndex"`
	BlockHash        string   `json:"blockHash"`
	LogIndex         string   `json:"logIndex"`
	Removed          bool     `json:"removed"`
}

type TokenMetadata struct {
	Address        string `json:"address"`
	Name           string `json:"name"`
	Symbol         string `json:"symbol"`
	Decimals       int32  `json:"decimals"`
	TotalSupplyRaw string `json:"total_supply_raw"`
}

// TokenMetadata reads the standard ERC-20 metadata directly from the token
// contract. It accepts both dynamic-string and bytes32 name/symbol responses,
// because both variants exist on BNB Chain.
func (c *Client) TokenMetadata(
	ctx context.Context,
	tokenAddress string,
	chainKey string,
	fallback string,
) (TokenMetadata, error) {
	profile, err := ChainProfile(chainKey, fallback)
	if err != nil {
		return TokenMetadata{}, err
	}
	token, err := ValidateAddress(tokenAddress)
	if err != nil {
		return TokenMetadata{}, err
	}
	call := func(selector string) (string, error) {
		result, err := c.rpc(ctx, profile, "eth_call", []any{
			map[string]any{"to": token, "data": selector},
			"latest",
		})
		if err != nil {
			return "", err
		}
		encoded, ok := result.(string)
		if !ok || !strings.HasPrefix(encoded, "0x") {
			return "", fmt.Errorf("unexpected token metadata response")
		}
		return encoded, nil
	}
	nameRaw, err := call("0x06fdde03")
	if err != nil {
		return TokenMetadata{}, fmt.Errorf("read token name: %w", err)
	}
	symbolRaw, err := call("0x95d89b41")
	if err != nil {
		return TokenMetadata{}, fmt.Errorf("read token symbol: %w", err)
	}
	decimalsRaw, err := call("0x313ce567")
	if err != nil {
		return TokenMetadata{}, fmt.Errorf("read token decimals: %w", err)
	}
	totalSupplyRaw, err := call("0x18160ddd")
	if err != nil {
		return TokenMetadata{}, fmt.Errorf("read token total supply: %w", err)
	}
	name, err := decodeABIString(nameRaw)
	if err != nil {
		return TokenMetadata{}, fmt.Errorf("decode token name: %w", err)
	}
	symbol, err := decodeABIString(symbolRaw)
	if err != nil {
		return TokenMetadata{}, fmt.Errorf("decode token symbol: %w", err)
	}
	decimals, err := decodeABIUint(decimalsRaw)
	if err != nil || !decimals.IsInt64() || decimals.Int64() < 0 || decimals.Int64() > 255 {
		return TokenMetadata{}, fmt.Errorf("decode token decimals")
	}
	totalSupply, err := decodeABIUint(totalSupplyRaw)
	if err != nil {
		return TokenMetadata{}, fmt.Errorf("decode token total supply: %w", err)
	}
	if strings.TrimSpace(symbol) == "" {
		return TokenMetadata{}, fmt.Errorf("token symbol is empty")
	}
	return TokenMetadata{
		Address:        token,
		Name:           strings.TrimSpace(name),
		Symbol:         strings.TrimSpace(symbol),
		Decimals:       int32(decimals.Int64()),
		TotalSupplyRaw: totalSupply.String(),
	}, nil
}

func decodeABIUint(value string) (*big.Int, error) {
	raw := strings.TrimPrefix(strings.TrimSpace(value), "0x")
	if raw == "" || len(raw)%2 != 0 {
		return nil, fmt.Errorf("invalid ABI integer")
	}
	number, ok := new(big.Int).SetString(raw, 16)
	if !ok {
		return nil, fmt.Errorf("invalid ABI integer")
	}
	return number, nil
}

func decodeABIString(value string) (string, error) {
	raw := strings.TrimPrefix(strings.TrimSpace(value), "0x")
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) < 32 {
		return "", fmt.Errorf("invalid ABI string")
	}
	// Legacy tokens sometimes return bytes32 instead of a dynamic string.
	if len(decoded) == 32 {
		return strings.TrimRight(string(decoded), "\x00"), nil
	}
	offset := new(big.Int).SetBytes(decoded[:32])
	if !offset.IsInt64() || offset.Int64() < 0 {
		return "", fmt.Errorf("invalid ABI string offset")
	}
	start := int(offset.Int64())
	if start > len(decoded)-32 {
		return "", fmt.Errorf("invalid ABI string offset")
	}
	length := new(big.Int).SetBytes(decoded[start : start+32])
	if !length.IsInt64() || length.Int64() < 0 {
		return "", fmt.Errorf("invalid ABI string length")
	}
	textStart := start + 32
	textEnd := textStart + int(length.Int64())
	if textEnd < textStart || textEnd > len(decoded) {
		return "", fmt.Errorf("invalid ABI string length")
	}
	return string(decoded[textStart:textEnd]), nil
}

func (c *Client) Logs(
	ctx context.Context,
	chainKey, fallback string,
	filter LogFilter,
) ([]RPCLog, error) {
	profile, err := ChainProfile(chainKey, fallback)
	if err != nil {
		return nil, err
	}
	payload, err := normalizeLogFilter(filter)
	if err != nil {
		return nil, err
	}
	result, err := c.rpc(ctx, profile, "eth_getLogs", []any{payload})
	if err != nil {
		return nil, err
	}
	rawLogs, ok := result.([]any)
	if !ok {
		return nil, fmt.Errorf("unexpected logs response")
	}
	logs := make([]RPCLog, 0, len(rawLogs))
	for _, rawLog := range rawLogs {
		object, ok := rawLog.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("unexpected log response")
		}
		address, err := ValidateAddress(valueString(object["address"]))
		if err != nil {
			return nil, fmt.Errorf("invalid log address from Nodit: %w", err)
		}
		topics, err := normalizeResponseTopics(object["topics"])
		if err != nil {
			return nil, err
		}
		transactionHash, err := ValidateTransactionHash(valueString(object["transactionHash"]))
		if err != nil {
			return nil, fmt.Errorf("invalid log transaction hash from Nodit")
		}
		blockHash, err := ValidateTransactionHash(valueString(object["blockHash"]))
		if err != nil {
			return nil, fmt.Errorf("invalid log block hash from Nodit")
		}
		logs = append(logs, RPCLog{
			Address:          address,
			Topics:           topics,
			Data:             valueString(object["data"]),
			BlockNumber:      valueString(object["blockNumber"]),
			TransactionHash:  transactionHash,
			TransactionIndex: valueString(object["transactionIndex"]),
			BlockHash:        blockHash,
			LogIndex:         valueString(object["logIndex"]),
			Removed:          object["removed"] == true,
		})
	}
	return logs, nil
}

func (c *Client) BlockByNumber(
	ctx context.Context,
	blockNumber uint64,
	includeTransactions bool,
	chainKey, fallback string,
) (map[string]any, error) {
	return c.block(ctx, fmt.Sprintf("0x%x", blockNumber), includeTransactions, chainKey, fallback)
}

func (c *Client) LatestBlock(
	ctx context.Context,
	includeTransactions bool,
	chainKey, fallback string,
) (map[string]any, error) {
	return c.block(ctx, "latest", includeTransactions, chainKey, fallback)
}

func (c *Client) BlockByHash(
	ctx context.Context,
	blockHash string,
	includeTransactions bool,
	chainKey, fallback string,
) (map[string]any, error) {
	profile, err := ChainProfile(chainKey, fallback)
	if err != nil {
		return nil, err
	}
	hash, err := ValidateTransactionHash(blockHash)
	if err != nil {
		return nil, fmt.Errorf("invalid block hash")
	}
	result, err := c.rpc(
		ctx,
		profile,
		"eth_getBlockByHash",
		[]any{hash, includeTransactions},
	)
	return rpcObject(result, err, "block")
}

// PoolTokens returns token0/token1 in the immutable on-chain order used by
// V2/V3 event amount fields. Market-data providers expose base/quote order,
// which is not guaranteed to match the contract order.
func (c *Client) PoolTokens(
	ctx context.Context,
	poolAddress string,
	chainKey, fallback string,
) (string, string, error) {
	profile, err := ChainProfile(chainKey, fallback)
	if err != nil {
		return "", "", err
	}
	pool, err := ValidateAddress(poolAddress)
	if err != nil {
		return "", "", err
	}
	call := func(selector string) (string, error) {
		result, err := c.rpc(ctx, profile, "eth_call", []any{
			map[string]any{"to": pool, "data": selector},
			"latest",
		})
		if err != nil {
			return "", err
		}
		encoded, ok := result.(string)
		encoded = strings.ToLower(strings.TrimSpace(encoded))
		if !ok || len(encoded) != 66 || !strings.HasPrefix(encoded, "0x") {
			return "", fmt.Errorf("unexpected pool token response")
		}
		return ValidateAddress("0x" + encoded[len(encoded)-40:])
	}
	token0, err := call("0x0dfe1681")
	if err != nil {
		return "", "", fmt.Errorf("read pool token0: %w", err)
	}
	token1, err := call("0xd21220a7")
	if err != nil {
		return "", "", fmt.Errorf("read pool token1: %w", err)
	}
	if token0 == token1 {
		return "", "", fmt.Errorf("pool tokens must differ")
	}
	return token0, token1, nil
}

func (c *Client) block(
	ctx context.Context,
	blockReference string,
	includeTransactions bool,
	chainKey, fallback string,
) (map[string]any, error) {
	profile, err := ChainProfile(chainKey, fallback)
	if err != nil {
		return nil, err
	}
	result, err := c.rpc(
		ctx,
		profile,
		"eth_getBlockByNumber",
		[]any{blockReference, includeTransactions},
	)
	return rpcObject(result, err, "block")
}

func rpcObject(result any, err error, responseName string) (map[string]any, error) {
	if err != nil || result == nil {
		return nil, err
	}
	object, ok := result.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected %s response", responseName)
	}
	return object, nil
}

func normalizeLogFilter(filter LogFilter) (map[string]any, error) {
	if strings.TrimSpace(filter.BlockHash) != "" &&
		(filter.FromBlock != nil || filter.ToBlock != nil) {
		return nil, fmt.Errorf("block hash cannot be combined with a block range")
	}
	payload := make(map[string]any)
	if strings.TrimSpace(filter.BlockHash) != "" {
		hash, err := ValidateTransactionHash(filter.BlockHash)
		if err != nil {
			return nil, fmt.Errorf("invalid block hash")
		}
		payload["blockHash"] = hash
	} else {
		if filter.FromBlock != nil {
			payload["fromBlock"] = fmt.Sprintf("0x%x", *filter.FromBlock)
		}
		if filter.ToBlock != nil {
			payload["toBlock"] = fmt.Sprintf("0x%x", *filter.ToBlock)
		}
		if filter.FromBlock != nil && filter.ToBlock != nil &&
			*filter.FromBlock > *filter.ToBlock {
			return nil, fmt.Errorf("from block must not be greater than to block")
		}
	}
	if len(filter.Addresses) == 1 {
		address, err := ValidateAddress(filter.Addresses[0])
		if err != nil {
			return nil, err
		}
		payload["address"] = address
	} else if len(filter.Addresses) > 1 {
		addresses := make([]string, 0, len(filter.Addresses))
		seen := make(map[string]struct{}, len(filter.Addresses))
		for _, address := range filter.Addresses {
			normalized, err := ValidateAddress(address)
			if err != nil {
				return nil, err
			}
			if _, exists := seen[normalized]; exists {
				continue
			}
			seen[normalized] = struct{}{}
			addresses = append(addresses, normalized)
		}
		payload["address"] = addresses
	}
	if len(filter.Topics) > 4 {
		return nil, fmt.Errorf("log filter supports at most 4 topic positions")
	}
	if len(filter.Topics) > 0 {
		topics := make([]any, len(filter.Topics))
		for index, candidates := range filter.Topics {
			switch len(candidates) {
			case 0:
				topics[index] = nil
			case 1:
				topic, err := normalizeTopic(candidates[0])
				if err != nil {
					return nil, err
				}
				topics[index] = topic
			default:
				values := make([]string, 0, len(candidates))
				for _, candidate := range candidates {
					topic, err := normalizeTopic(candidate)
					if err != nil {
						return nil, err
					}
					values = append(values, topic)
				}
				topics[index] = values
			}
		}
		payload["topics"] = topics
	}
	return payload, nil
}

func normalizeResponseTopics(value any) ([]string, error) {
	rawTopics, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("unexpected log topics response")
	}
	topics := make([]string, 0, len(rawTopics))
	for _, rawTopic := range rawTopics {
		topic, err := normalizeTopic(valueString(rawTopic))
		if err != nil {
			return nil, fmt.Errorf("invalid log topic from Nodit: %w", err)
		}
		topics = append(topics, topic)
	}
	return topics, nil
}

func normalizeTopic(topic string) (string, error) {
	value := strings.TrimSpace(topic)
	if !topicPattern.MatchString(value) {
		return "", fmt.Errorf("invalid log topic")
	}
	return "0x" + strings.ToLower(value[2:]), nil
}
