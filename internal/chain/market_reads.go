package chain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Two in-flight hydration payloads are allowed by the collector. Each response
// has a separate wire-size cap, including unexpected trailing JSON/whitespace.
const MaxMarketRPCResponseBytes int64 = 16 << 20

// Full blocks and block receipts include unrelated transactions. Keep their
// transient footprint bounded so the scanner can fall back to smaller reads.
const maxMarketBlockReadResponseBytes int64 = 4 << 20

var ErrMarketRPCResponseTooLarge = errors.New("market RPC response exceeds bounded capacity")

func decodeBoundedRPCObject(body io.Reader, limit int64) (map[string]any, error) {
	r := &io.LimitedReader{R: body, N: limit + 1}
	decoder := json.NewDecoder(r)
	decoder.UseNumber()
	var object map[string]any
	err := decoder.Decode(&object)
	if err == nil {
		var trailing any
		if trailingErr := decoder.Decode(&trailing); trailingErr != io.EOF {
			if trailingErr == nil {
				err = errors.New("multiple RPC response values")
			} else {
				err = trailingErr
			}
		}
	}
	if r.N == 0 {
		return nil, ErrMarketRPCResponseTooLarge
	}
	if err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("RPC response must be an object")
	}
	return object, nil
}

func (c *Client) boundedMarketClient() *Client {
	clone := *c
	clone.maxResponseBytes = MaxMarketRPCResponseBytes
	if client, ok := c.httpClient.(*http.Client); ok {
		noRedirect := *client
		noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		clone.httpClient = &noRedirect
	}
	return &clone
}
func (c *Client) MarketRPCTransactionByHash(ctx context.Context, hash, key, fallback string) (map[string]any, error) {
	return c.boundedMarketClient().RPCTransactionByHash(ctx, hash, key, fallback)
}
func (c *Client) MarketTransactionReceipt(ctx context.Context, hash, key, fallback string) (map[string]any, error) {
	return c.boundedMarketClient().TransactionReceipt(ctx, hash, key, fallback)
}

// MarketBlockReceipts reads the Nodit block identified by the scanner's
// verified hash. The collector validates the receipts it needs for that block.
func (c *Client) MarketBlockReceipts(ctx context.Context, blockHash, key, fallback string) ([]any, error) {
	blockHash, err := ValidateTransactionHash(blockHash)
	if err != nil {
		return nil, fmt.Errorf("invalid market receipt block hash: %w", err)
	}
	result, err := c.marketBlockResult(ctx, key, fallback, "eth_getBlockReceipts", []any{blockHash})
	if err != nil {
		return nil, err
	}
	receipts, ok := result.([]any)
	if !ok {
		return nil, errors.New("unexpected market block receipts result")
	}
	for _, raw := range receipts {
		if _, ok := raw.(map[string]any); !ok {
			return nil, errors.New("invalid market block receipt object")
		}
	}
	return receipts, nil
}

// MarketFullBlock reads full transaction identities from the primary Nodit
// node within the same tighter bound as a block of receipts.
func (c *Client) MarketFullBlock(ctx context.Context, number uint64, key, fallback string) (map[string]any, error) {
	result, err := c.marketBlockResult(ctx, key, fallback, "eth_getBlockByNumber", []any{fmt.Sprintf("0x%x", number), true})
	if err != nil {
		return nil, err
	}
	block, ok := result.(map[string]any)
	if !ok {
		return nil, errors.New("unexpected full market block result")
	}
	if _, ok := block["transactions"].([]any); !ok {
		return nil, errors.New("invalid full market block transactions")
	}
	return block, nil
}

func (c *Client) marketBlockResult(ctx context.Context, key, fallback, method string, params []any) (any, error) {
	profile, err := ChainProfile(key, fallback)
	if err != nil {
		return nil, err
	}
	bounded := c.boundedMarketClient()
	bounded.maxResponseBytes = maxMarketBlockReadResponseBytes
	response, err := bounded.rpcResponse(ctx, profile, method, params)
	if err != nil {
		return nil, err
	}
	id, ok := response["id"].(json.Number)
	if response["jsonrpc"] != "2.0" || !ok || id.String() != "1" || response["error"] != nil {
		return nil, fmt.Errorf("invalid market %s RPC acknowledgement", method)
	}
	return response["result"], nil
}

// MarketReadClient adds only verified read fallbacks to the collector. It is
// never supplied to payments. Logs, blockNumber and all Data APIs stay Nodit.
// There are no per-block/provider caches or background goroutines.
type MarketReadClient struct {
	*Client
	fallbacks    map[string][]string
	fallbackHTTP *http.Client
	slots        chan struct{}
	limiter      *cuRateLimiter
}

func NewMarketReadClient(primary *Client, fallbacks map[string][]string) (*MarketReadClient, error) {
	if primary == nil {
		return nil, errors.New("market primary client is required")
	}
	if len(fallbacks) > 6 {
		return nil, errors.New("market fallback chain capacity exceeded")
	}
	copyURLs := make(map[string][]string, len(fallbacks))
	for key, endpoints := range fallbacks {
		profile, err := ChainProfile(key, "")
		if err != nil || profile.Key != key || len(endpoints) > 2 {
			return nil, errors.New("invalid market fallback chain configuration")
		}
		seen := make(map[string]bool, len(endpoints))
		for _, endpoint := range endpoints {
			if err := validateMarketFallbackURL(endpoint); err != nil {
				return nil, err
			}
			if seen[endpoint] {
				return nil, errors.New("duplicate market fallback endpoint")
			}
			seen[endpoint] = true
		}
		copyURLs[key] = append([]string(nil), endpoints...)
	}
	return &MarketReadClient{Client: primary.boundedMarketClient(), fallbacks: copyURLs,
		fallbackHTTP: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		slots:        make(chan struct{}, 2), limiter: newCURateLimiter(2)}, nil
}
func validateMarketFallbackURL(endpoint string) error {
	if len(endpoint) == 0 || len(endpoint) > 4096 || strings.TrimSpace(endpoint) != endpoint {
		return errors.New("invalid market fallback URL length")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return errors.New("invalid market fallback URL")
	}
	loopback := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return errors.New("market fallback requires HTTPS except on loopback")
	}
	return nil
}

func (c *MarketReadClient) MarketRPCTransactionByHash(ctx context.Context, hash, key, fallback string) (map[string]any, error) {
	return c.marketObjectByHash(ctx, "eth_getTransactionByHash", hash, key, fallback)
}
func (c *MarketReadClient) MarketTransactionReceipt(ctx context.Context, hash, key, fallback string) (map[string]any, error) {
	return c.marketObjectByHash(ctx, "eth_getTransactionReceipt", hash, key, fallback)
}
func (c *MarketReadClient) RPCTransactionByHash(ctx context.Context, hash, key, fallback string) (map[string]any, error) {
	return c.MarketRPCTransactionByHash(ctx, hash, key, fallback)
}
func (c *MarketReadClient) TransactionReceipt(ctx context.Context, hash, key, fallback string) (map[string]any, error) {
	return c.MarketTransactionReceipt(ctx, hash, key, fallback)
}
func (c *MarketReadClient) marketObjectByHash(ctx context.Context, method, hash, key, fallback string) (map[string]any, error) {
	profile, err := ChainProfile(key, fallback)
	if err != nil {
		return nil, err
	}
	hash, err = ValidateTransactionHash(hash)
	if err != nil {
		return nil, err
	}
	result, err := c.Client.rpc(ctx, profile, method, []any{hash})
	return c.withFallback(ctx, profile, method, []any{hash}, result, err)
}
func (c *MarketReadClient) BlockByNumber(ctx context.Context, number uint64, full bool, key, fallback string) (map[string]any, error) {
	profile, err := ChainProfile(key, fallback)
	if err != nil {
		return nil, err
	}
	params := []any{fmt.Sprintf("0x%x", number), full}
	result, err := c.Client.rpc(ctx, profile, "eth_getBlockByNumber", params)
	return c.withFallback(ctx, profile, "eth_getBlockByNumber", params, result, err)
}
func (c *MarketReadClient) withFallback(ctx context.Context, profile Profile, method string, params []any, result any, primaryErr error) (map[string]any, error) {
	if primaryErr == nil {
		return rpcObject(result, nil, "market read")
	}
	// A capacity failure is not permission to download the same oversized body
	// from every provider. Semantic validation after a successful read also never
	// activates a fallback.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if errors.Is(primaryErr, ErrMarketRPCResponseTooLarge) {
		return nil, primaryErr
	}
	endpoints := c.fallbacks[profile.Key]
	if len(endpoints) == 0 {
		return nil, primaryErr
	}
	for _, endpoint := range endpoints {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		result, err := c.verifiedFallback(ctx, endpoint, profile, method, params)
		if err == nil {
			return rpcObject(result, nil, "market fallback read")
		}
		if errors.Is(err, ErrMarketRPCResponseTooLarge) {
			return nil, err
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// Do not include URL paths, query credentials, provider bodies or url.Errors.
	return nil, fmt.Errorf("market %s read failed on primary and configured %s fallback endpoints", method, profile.Key)
}
func (c *MarketReadClient) verifiedFallback(ctx context.Context, endpoint string, profile Profile, method string, params []any) (any, error) {
	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.slots }()
	identity, err := c.fallbackRPC(ctx, endpoint, "eth_chainId", nil)
	if err != nil {
		return nil, err
	}
	raw, ok := identity.(string)
	if !ok || !strings.HasPrefix(raw, "0x") {
		return nil, errors.New("invalid fallback chain identity")
	}
	id, err := strconv.ParseInt(raw[2:], 16, 64)
	if err != nil || id != profile.ChainID {
		return nil, errors.New("fallback chain identity mismatch")
	}
	return c.fallbackRPC(ctx, endpoint, method, params)
}
func (c *MarketReadClient) fallbackRPC(ctx context.Context, endpoint, method string, params []any) (any, error) {
	if err := c.limiter.wait(ctx, 1); err != nil {
		return nil, err
	}
	if params == nil {
		params = []any{}
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid fallback request")
	}
	request.Header.Set("Content-Type", "application/json") // no Nodit key forwarding
	response, err := c.fallbackHTTP.Do(request)
	if err != nil {
		return nil, errors.New("fallback transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("fallback HTTP request rejected")
	}
	object, err := decodeBoundedRPCObject(response.Body, MaxMarketRPCResponseBytes)
	if err != nil {
		return nil, err
	}
	id, ok := object["id"].(json.Number)
	if object["jsonrpc"] != "2.0" || !ok || id.String() != "1" || object["error"] != nil {
		return nil, errors.New("invalid fallback RPC acknowledgement")
	}
	result, exists := object["result"]
	if !exists {
		return nil, errors.New("missing fallback RPC result")
	}
	return result, nil
}
