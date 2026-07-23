package chain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultNoditBaseURL = "https://web3.nodit.io/v1"
	defaultHTTPTimeout  = 20 * time.Second
	maxErrorBodyLength  = 300
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Client struct {
	apiKey     string
	baseURL    string
	rpcBaseURL string
	httpClient HTTPDoer
}

type ClientOption func(*Client)

func WithHTTPClient(client HTTPDoer) ClientOption {
	return func(target *Client) {
		if client != nil {
			target.httpClient = client
		}
	}
}

func WithRPCBaseURL(baseURL string) ClientOption {
	return func(target *Client) {
		target.rpcBaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	}
}

func NewClient(apiKey, baseURL string, options ...ClientOption) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("NODIT_API_KEY is required")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultNoditBaseURL
	}
	client := &Client{
		apiKey:     strings.TrimSpace(apiKey),
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		httpClient: &http.Client{Timeout: defaultHTTPTimeout},
	}
	for _, option := range options {
		option(client)
	}
	return client, nil
}

func (c *Client) post(ctx context.Context, profile Profile, path string, payload any) (map[string]any, error) {
	endpoint := fmt.Sprintf(
		"%s/%s/%s/%s",
		c.baseURL,
		profile.Chain,
		profile.Network,
		strings.TrimLeft(path, "/"),
	)
	return c.postObject(ctx, endpoint, payload, "Nodit API")
}

func (c *Client) rpc(ctx context.Context, profile Profile, method string, params []any) (any, error) {
	endpoint := c.rpcBaseURL
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://%s-%s.nodit.io", profile.Chain, profile.Network)
	}
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	}
	data, err := c.postObject(ctx, endpoint, payload, "Nodit Node API")
	if err != nil {
		return nil, err
	}
	if rpcError := data["error"]; rpcError != nil {
		if detail, ok := rpcError.(map[string]any); ok {
			if message := valueString(detail["message"]); message != "" {
				return nil, fmt.Errorf("%s", message)
			}
		}
		return nil, fmt.Errorf("%v", rpcError)
	}
	return data["result"], nil
}

func (c *Client) postObject(
	ctx context.Context,
	endpoint string,
	payload any,
	serviceName string,
) (map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode %s request: %w", serviceName, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create %s request: %w", serviceName, err)
	}
	request.Header.Set("X-API-KEY", c.apiKey)
	request.Header.Set("Content-Type", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%s request: %w", serviceName, err)
	}
	defer response.Body.Close()

	if response.StatusCode >= http.StatusBadRequest {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyLength))
		return nil, fmt.Errorf("%s error %d: %s", serviceName, response.StatusCode, string(detail))
	}
	var data map[string]any
	decoder := json.NewDecoder(response.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&data); err != nil {
		return nil, fmt.Errorf("unexpected %s response: %w", serviceName, err)
	}
	if data == nil {
		return nil, fmt.Errorf("unexpected %s response", serviceName)
	}
	return data, nil
}

type BalanceResult struct {
	Value         string  `json:"value"`
	Symbol        string  `json:"symbol"`
	Decimals      int     `json:"decimals"`
	ChainKey      string  `json:"chain_key"`
	ChainID       int64   `json:"chain_id"`
	ChainName     string  `json:"chain_name"`
	WalletAddress string  `json:"wallet_address"`
	TokenAddress  *string `json:"token_address"`
}

func (c *Client) NativeBalance(ctx context.Context, address, chainKey, fallback string) (BalanceResult, error) {
	profile, err := ChainProfile(chainKey, fallback)
	if err != nil {
		return BalanceResult{}, err
	}
	wallet, err := ValidateAddress(address)
	if err != nil {
		return BalanceResult{}, err
	}
	data, err := c.post(ctx, profile, "native/getNativeBalanceByAccount", map[string]any{
		"accountAddress": wallet,
	})
	if err != nil {
		return BalanceResult{}, err
	}
	raw := valueString(data["balance"])
	if raw == "" {
		raw = "0"
	}
	value, err := FormatUnits(raw, 18)
	if err != nil {
		return BalanceResult{}, err
	}
	return BalanceResult{
		Value: value, Symbol: profile.NativeSymbol, Decimals: 18,
		ChainKey: profile.Key, ChainID: profile.ChainID, ChainName: profile.Name,
		WalletAddress: wallet,
	}, nil
}

func (c *Client) TokenBalance(
	ctx context.Context,
	address, tokenAddress, chainKey, fallback string,
) (BalanceResult, error) {
	profile, err := ChainProfile(chainKey, fallback)
	if err != nil {
		return BalanceResult{}, err
	}
	wallet, err := ValidateAddress(address)
	if err != nil {
		return BalanceResult{}, err
	}
	token, err := ValidateAddress(tokenAddress)
	if err != nil {
		return BalanceResult{}, err
	}
	data, err := c.post(ctx, profile, "token/getTokensOwnedByAccount", map[string]any{
		"accountAddress":    wallet,
		"contractAddresses": []string{token},
		"rpp":               1,
	})
	if err != nil {
		return BalanceResult{}, err
	}
	item, err := firstTokenItem(data, token)
	if err != nil {
		return BalanceResult{}, err
	}
	contract, _ := objectValue(item["contract"])
	decimals := firstInteger(contract, "decimals", "decimal", "tokenDecimal")
	if decimals == 0 {
		decimals = 18
	}
	symbol := firstString(contract, "symbol", "tokenSymbol", "name")
	if symbol == "" {
		symbol = "TOKEN"
	}
	raw := valueString(item["balance"])
	if raw == "" {
		raw = "0"
	}
	value, err := FormatUnits(raw, decimals)
	if err != nil {
		return BalanceResult{}, err
	}
	return BalanceResult{
		Value: value, Symbol: symbol, Decimals: decimals,
		ChainKey: profile.Key, ChainID: profile.ChainID, ChainName: profile.Name,
		WalletAddress: wallet, TokenAddress: &token,
	}, nil
}

func (c *Client) Balance(
	ctx context.Context,
	address, tokenAddress, chainKey, fallback string,
) (BalanceResult, error) {
	if strings.TrimSpace(tokenAddress) != "" {
		return c.TokenBalance(ctx, address, tokenAddress, chainKey, fallback)
	}
	return c.NativeBalance(ctx, address, chainKey, fallback)
}

func (c *Client) TransactionByHash(
	ctx context.Context,
	txHash, chainKey, fallback string,
) (map[string]any, error) {
	profile, err := ChainProfile(chainKey, fallback)
	if err != nil {
		return nil, err
	}
	hash, err := ValidateTransactionHash(txHash)
	if err != nil {
		return nil, err
	}
	return c.post(ctx, profile, "blockchain/getTransactionByHash", map[string]any{
		"transactionHash":    hash,
		"withBalanceChanges": true,
	})
}

func (c *Client) RPCTransactionByHash(
	ctx context.Context,
	txHash, chainKey, fallback string,
) (map[string]any, error) {
	return c.rpcObjectByHash(ctx, "eth_getTransactionByHash", "transaction", txHash, chainKey, fallback)
}

func (c *Client) TransactionReceipt(
	ctx context.Context,
	txHash, chainKey, fallback string,
) (map[string]any, error) {
	return c.rpcObjectByHash(ctx, "eth_getTransactionReceipt", "transaction receipt", txHash, chainKey, fallback)
}

func (c *Client) rpcObjectByHash(
	ctx context.Context,
	method, responseName, txHash, chainKey, fallback string,
) (map[string]any, error) {
	profile, err := ChainProfile(chainKey, fallback)
	if err != nil {
		return nil, err
	}
	hash, err := ValidateTransactionHash(txHash)
	if err != nil {
		return nil, err
	}
	result, err := c.rpc(ctx, profile, method, []any{hash})
	if err != nil || result == nil {
		return nil, err
	}
	object, ok := result.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected %s response", responseName)
	}
	return object, nil
}

func (c *Client) LatestBlockNumber(ctx context.Context, chainKey, fallback string) (uint64, error) {
	profile, err := ChainProfile(chainKey, fallback)
	if err != nil {
		return 0, err
	}
	result, err := c.rpc(ctx, profile, "eth_blockNumber", []any{})
	if err != nil {
		return 0, err
	}
	value, ok := result.(string)
	if !ok || !strings.HasPrefix(value, "0x") {
		return 0, fmt.Errorf("unexpected latest block response")
	}
	number, err := strconv.ParseUint(value[2:], 16, 64)
	if err != nil {
		return 0, fmt.Errorf("unexpected latest block response: %w", err)
	}
	return number, nil
}

type AllowanceResult struct {
	Value          string `json:"value"`
	Raw            string `json:"raw"`
	Symbol         string `json:"symbol"`
	Decimals       int    `json:"decimals"`
	ChainKey       string `json:"chain_key"`
	ChainID        int64  `json:"chain_id"`
	ChainName      string `json:"chain_name"`
	WalletAddress  string `json:"wallet_address"`
	TokenAddress   string `json:"token_address"`
	SpenderAddress string `json:"spender_address"`
}

func (c *Client) TokenAllowance(
	ctx context.Context,
	ownerAddress, tokenAddress, spenderAddress, chainKey, fallback string,
) (AllowanceResult, error) {
	profile, err := ChainProfile(chainKey, fallback)
	if err != nil {
		return AllowanceResult{}, err
	}
	owner, err := ValidateAddress(ownerAddress)
	if err != nil {
		return AllowanceResult{}, err
	}
	token, err := ValidateAddress(tokenAddress)
	if err != nil {
		return AllowanceResult{}, err
	}
	spender, err := ValidateAddress(spenderAddress)
	if err != nil {
		return AllowanceResult{}, err
	}
	data, err := c.post(ctx, profile, "token/getTokenAllowance", map[string]any{
		"ownerAddress":    owner,
		"contractAddress": token,
		"spenderAddress":  spender,
	})
	if err != nil {
		return AllowanceResult{}, err
	}
	raw := firstValue(data, "allowance", "value", "amount", "balance")
	if raw == "" {
		raw = "0"
	}
	decimals := firstValueInt(data, "decimals", "decimal", "tokenDecimal")
	if decimals == 0 {
		decimals = 18
	}
	symbol := firstValue(data, "symbol", "tokenSymbol", "name")
	if symbol == "" {
		symbol = "TOKEN"
	}
	value, err := FormatUnits(raw, decimals)
	if err != nil {
		return AllowanceResult{}, err
	}
	return AllowanceResult{
		Value: value, Raw: raw, Symbol: symbol, Decimals: decimals,
		ChainKey: profile.Key, ChainID: profile.ChainID, ChainName: profile.Name,
		WalletAddress: owner, TokenAddress: token, SpenderAddress: spender,
	}, nil
}

func (c *Client) TransactionsByAccount(
	ctx context.Context,
	address, chainKey, fallback string,
	rpp int,
) ([]map[string]any, error) {
	profile, err := ChainProfile(chainKey, fallback)
	if err != nil {
		return nil, err
	}
	wallet, err := ValidateAddress(address)
	if err != nil {
		return nil, err
	}
	if rpp < 1 {
		rpp = 1
	}
	if rpp > 100 {
		rpp = 100
	}
	data, err := c.post(ctx, profile, "blockchain/getTransactionsByAccount", map[string]any{
		"accountAddress": wallet,
		"rpp":            rpp,
		"withLogs":       true,
		"withDecode":     true,
	})
	if err != nil {
		return nil, err
	}
	return items(data), nil
}

type InteractionResult struct {
	Cursor        string         `json:"cursor"`
	Matched       bool           `json:"matched"`
	Transaction   map[string]any `json:"transaction"`
	ChainKey      string         `json:"chain_key"`
	ChainID       int64          `json:"chain_id"`
	WalletAddress string         `json:"wallet_address"`
	TargetAddress string         `json:"target_address"`
}

func (c *Client) LatestInteraction(
	ctx context.Context,
	address, targetAddress, chainKey, fallback string,
) (InteractionResult, error) {
	profile, err := ChainProfile(chainKey, fallback)
	if err != nil {
		return InteractionResult{}, err
	}
	wallet, err := ValidateAddress(address)
	if err != nil {
		return InteractionResult{}, err
	}
	target, err := ValidateAddress(targetAddress)
	if err != nil {
		return InteractionResult{}, err
	}
	transactions, err := c.TransactionsByAccount(ctx, wallet, profile.Key, fallback, 30)
	if err != nil {
		return InteractionResult{}, err
	}
	for _, transaction := range transactions {
		if containsAddress(transaction, target) {
			cursor := firstString(transaction, "transactionHash", "hash", "txHash", "id")
			if cursor == "" {
				cursor = "matched"
			}
			return InteractionResult{
				Cursor: cursor, Matched: true, Transaction: transaction,
				ChainKey: profile.Key, ChainID: profile.ChainID,
				WalletAddress: wallet, TargetAddress: target,
			}, nil
		}
	}
	return InteractionResult{
		Cursor: "none", Matched: false, Transaction: nil,
		ChainKey: profile.Key, ChainID: profile.ChainID,
		WalletAddress: wallet, TargetAddress: target,
	}, nil
}

func firstTokenItem(data map[string]any, tokenAddress string) (map[string]any, error) {
	rawItems, _ := data["items"].([]any)
	for _, rawItem := range rawItems {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		contract, _ := objectValue(item["contract"])
		address := firstString(contract, "address", "contractAddress")
		if address == "" {
			address = firstString(item, "contractAddress")
		}
		if address == "" {
			continue
		}
		normalized, err := ValidateAddress(address)
		if err != nil {
			return nil, err
		}
		if normalized == tokenAddress {
			return item, nil
		}
	}
	return map[string]any{}, nil
}

func items(payload any) []map[string]any {
	switch value := payload.(type) {
	case []any:
		result := make([]map[string]any, 0, len(value))
		for _, item := range value {
			if object, ok := item.(map[string]any); ok {
				result = append(result, object)
			}
		}
		return result
	case map[string]any:
		for _, key := range []string{"items", "transactions", "data", "result"} {
			if nested, ok := value[key]; ok {
				if result := items(nested); len(result) > 0 {
					return result
				}
			}
		}
	}
	return nil
}

func firstValue(payload any, keys ...string) string {
	switch value := payload.(type) {
	case map[string]any:
		for _, key := range keys {
			if item, ok := value[key]; ok && item != nil {
				return valueString(item)
			}
		}
		for _, item := range value {
			if result := firstValue(item, keys...); result != "" {
				return result
			}
		}
	case []any:
		for _, item := range value {
			if result := firstValue(item, keys...); result != "" {
				return result
			}
		}
	}
	return ""
}

func firstValueInt(payload any, keys ...string) int {
	value, _ := strconv.Atoi(firstValue(payload, keys...))
	return value
}

func containsAddress(payload any, expected string) bool {
	switch value := payload.(type) {
	case map[string]any:
		for _, item := range value {
			if containsAddress(item, expected) {
				return true
			}
		}
	case []any:
		for _, item := range value {
			if containsAddress(item, expected) {
				return true
			}
		}
	case string:
		candidate := strings.TrimSpace(value)
		if addressPattern.MatchString(candidate) {
			normalized, _ := ValidateAddress(candidate)
			return normalized == expected
		}
		return strings.Contains(strings.ToLower(candidate), expected[2:])
	}
	return false
}

func objectValue(value any) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	return object, ok
}

func firstString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := valueString(object[key]); value != "" {
			return value
		}
	}
	return ""
}

func firstInteger(object map[string]any, keys ...string) int {
	for _, key := range keys {
		switch value := object[key].(type) {
		case float64:
			return int(value)
		case json.Number:
			result, _ := strconv.Atoi(value.String())
			return result
		case string:
			result, _ := strconv.Atoi(value)
			return result
		}
	}
	return 0
}

func valueString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprint(typed)
	}
}
