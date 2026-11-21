package chain

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	WalletBalanceReadSource    = "nodit_rpc_erc20_balance:v1"
	walletTokenMetadataTTL     = 5 * time.Minute
	walletTokenMetadataMaxKeys = 4096
	// The in-flight ceiling is independent of retained-cache capacity. Raising
	// the working set must not authorize thousands of concurrent RPC callers.
	walletTokenMetadataMaxInFlight = 512
	// This bounds estimated entry payload, not the Go runtime's total RSS.
	walletTokenMetadataMaxBytes       = 1 << 20
	walletTokenRPCResponseBytes int64 = 64 << 10
	walletTokenSymbolMaxBytes         = 128
)

type walletTokenMetadataKey struct {
	chainID int64
	token   string
}

type walletTokenMetadata struct {
	decimals  int
	symbol    string
	expiresAt time.Time
}

type walletTokenMetadataRead struct {
	done  chan struct{}
	value walletTokenMetadata
	err   error
}

func (c *WalletReadClient) Balance(ctx context.Context, address, tokenAddress, chainKey, fallback string) (BalanceResult, error) {
	if strings.TrimSpace(tokenAddress) == "" {
		return c.Client.NativeBalance(ctx, address, chainKey, fallback)
	}
	return c.TokenBalance(ctx, address, tokenAddress, chainKey, fallback)
}

// TokenBalance uses balanceOf as the authority for a standard ERC-20 wallet
// observation. Data API omissions and malformed RPC results never become zero.
// Metadata can be reused for a short, bounded period; balances are not cached
// here. The monitor separately bounds reuse within its current observation run.
func (c *WalletReadClient) TokenBalance(ctx context.Context, address, tokenAddress, chainKey, fallback string) (BalanceResult, error) {
	if err := ctx.Err(); err != nil {
		return BalanceResult{}, err
	}
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
	// ERC-20 replies contain small ABI words/strings. Keep their wire budget
	// smaller than the native/transaction list budget without mutating either
	// the primary client or the shared wallet transport/limiter.
	rpcClient := *c.Client
	if rpcClient.maxResponseBytes <= 0 || rpcClient.maxResponseBytes > walletTokenRPCResponseBytes {
		rpcClient.maxResponseBytes = walletTokenRPCResponseBytes
	}
	metadata, err := c.tokenMetadata(ctx, &rpcClient, profile, token)
	if err != nil {
		return BalanceResult{}, err
	}
	raw, err := walletRPCUint(ctx, &rpcClient, profile, token, "0x70a08231"+strings.Repeat("0", 24)+wallet[2:])
	if err != nil {
		return BalanceResult{}, fmt.Errorf("read wallet token balance: %w", err)
	}
	value, err := FormatUnits(raw.String(), metadata.decimals)
	if err != nil {
		return BalanceResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return BalanceResult{}, err
	}
	decimals := metadata.decimals
	return BalanceResult{
		Value: value, Symbol: metadata.symbol, Decimals: decimals,
		ChainKey: profile.Key, ChainID: profile.ChainID, ChainName: profile.Name,
		WalletAddress: wallet, TokenAddress: &token,
		ReadSource: WalletBalanceReadSource, ReadDecimals: &decimals,
	}, nil
}

func walletRPCUint(ctx context.Context, client *Client, profile Profile, token, data string) (*big.Int, error) {
	result, err := client.rpc(ctx, profile, "eth_call", []any{map[string]any{"to": token, "data": data}, "latest"})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	encoded, ok := result.(string)
	if !ok || len(encoded) != 66 || !strings.HasPrefix(encoded, "0x") {
		return nil, fmt.Errorf("invalid ERC-20 uint256 response")
	}
	word, err := hex.DecodeString(encoded[2:])
	if err != nil {
		return nil, fmt.Errorf("invalid ERC-20 uint256 response")
	}
	return new(big.Int).SetBytes(word), nil
}

func (c *WalletReadClient) tokenMetadata(ctx context.Context, rpcClient *Client, profile Profile, token string) (walletTokenMetadata, error) {
	key := walletTokenMetadataKey{chainID: profile.ChainID, token: token}
	now := c.now()
	c.metadataMu.Lock()
	if cached, ok := c.metadata[key]; ok {
		if now.Before(cached.expiresAt) {
			c.metadataMu.Unlock()
			return cached, nil
		}
		c.removeMetadataLocked(key)
	}
	if pending := c.metadataInFlight[key]; pending != nil {
		c.metadataMu.Unlock()
		select {
		case <-ctx.Done():
			return walletTokenMetadata{}, ctx.Err()
		case <-pending.done:
			if err := ctx.Err(); err != nil {
				return walletTokenMetadata{}, err
			}
			return pending.value, pending.err
		}
	}
	if len(c.metadataInFlight) >= walletTokenMetadataMaxInFlight {
		c.metadataMu.Unlock()
		return walletTokenMetadata{}, fmt.Errorf("wallet token metadata read capacity busy")
	}
	pending := &walletTokenMetadataRead{
		done: make(chan struct{}),
		err:  fmt.Errorf("wallet token metadata read interrupted"),
	}
	c.metadataInFlight[key] = pending
	c.metadataMu.Unlock()
	// Followers create no background tasks. Always release them even if a
	// transport implementation panics and the caller's boundary recovers it.
	defer func() {
		c.metadataMu.Lock()
		delete(c.metadataInFlight, key)
		close(pending.done)
		c.metadataMu.Unlock()
	}()
	metadata, err := c.fetchTokenMetadata(ctx, rpcClient, profile, token)
	if err == nil {
		c.storeMetadata(key, metadata)
	}
	pending.value, pending.err = metadata, err
	return metadata, err
}

func (c *WalletReadClient) fetchTokenMetadata(ctx context.Context, rpcClient *Client, profile Profile, token string) (walletTokenMetadata, error) {
	decimals, err := walletRPCUint(ctx, rpcClient, profile, token, "0x313ce567")
	if err != nil {
		return walletTokenMetadata{}, fmt.Errorf("read wallet token decimals: %w", err)
	}
	if !decimals.IsInt64() || decimals.Int64() > 255 {
		return walletTokenMetadata{}, fmt.Errorf("invalid wallet token decimals")
	}
	metadata := walletTokenMetadata{decimals: int(decimals.Int64()), symbol: "TOKEN"}
	// Symbol is display metadata, not a prerequisite for a reliable balance.
	// An unsupported symbol is cached as TOKEN for the same finite TTL, rather
	// than repeatedly calling a known-incompatible method for each wallet.
	result, symbolErr := rpcClient.rpc(ctx, profile, "eth_call", []any{map[string]any{"to": token, "data": "0x95d89b41"}, "latest"})
	if err := ctx.Err(); err != nil {
		return walletTokenMetadata{}, err
	}
	if symbolErr == nil {
		if symbol, err := walletTokenSymbol(result); err == nil {
			metadata.symbol = symbol
		}
	}
	metadata.expiresAt = c.now().Add(walletTokenMetadataTTL)
	return metadata, nil
}

func walletTokenSymbol(result any) (string, error) {
	encoded, ok := result.(string)
	// A dynamic ABI string is offset + length + its padded content. The
	// legacy bytes32 form is accepted without permitting arbitrary tails.
	maxBytes := 64 + ((walletTokenSymbolMaxBytes+31)/32)*32
	if !ok || !strings.HasPrefix(encoded, "0x") || len(encoded) > 2+maxBytes*2 {
		return "", fmt.Errorf("invalid wallet token symbol")
	}
	raw, err := hex.DecodeString(encoded[2:])
	if err != nil {
		return "", fmt.Errorf("invalid wallet token symbol")
	}
	var content []byte
	if len(raw) == 32 {
		content = []byte(strings.TrimRight(string(raw), "\x00"))
	} else {
		if len(raw) < 64 || new(big.Int).SetBytes(raw[:32]).Cmp(big.NewInt(32)) != 0 {
			return "", fmt.Errorf("invalid wallet token symbol")
		}
		size := new(big.Int).SetBytes(raw[32:64])
		if !size.IsInt64() || size.Int64() < 1 || size.Int64() > walletTokenSymbolMaxBytes {
			return "", fmt.Errorf("invalid wallet token symbol")
		}
		length := int(size.Int64())
		if len(raw) != 64+((length+31)/32)*32 {
			return "", fmt.Errorf("invalid wallet token symbol")
		}
		content = raw[64 : 64+length]
		for _, padding := range raw[64+length:] {
			if padding != 0 {
				return "", fmt.Errorf("invalid wallet token symbol")
			}
		}
	}
	if !utf8.Valid(content) {
		return "", fmt.Errorf("invalid wallet token symbol")
	}
	symbol := strings.TrimSpace(string(content))
	if symbol == "" {
		return "", fmt.Errorf("invalid wallet token symbol")
	}
	for _, r := range symbol {
		if unicode.IsControl(r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return "", fmt.Errorf("invalid wallet token symbol")
		}
	}
	return symbol, nil
}

func metadataBytes(key walletTokenMetadataKey, value walletTokenMetadata) int {
	return len(key.token) + len(value.symbol) + 64
}

func (c *WalletReadClient) removeMetadataLocked(key walletTokenMetadataKey) {
	if value, ok := c.metadata[key]; ok {
		c.metadataBytes -= metadataBytes(key, value)
		delete(c.metadata, key)
	}
}

func (c *WalletReadClient) storeMetadata(key walletTokenMetadataKey, value walletTokenMetadata) {
	c.metadataMu.Lock()
	defer c.metadataMu.Unlock()
	now := c.now()
	for oldKey, oldValue := range c.metadata {
		if !now.Before(oldValue.expiresAt) {
			c.removeMetadataLocked(oldKey)
		}
	}
	c.removeMetadataLocked(key)
	size := metadataBytes(key, value)
	for len(c.metadata) >= walletTokenMetadataMaxKeys || c.metadataBytes+size > walletTokenMetadataMaxBytes {
		var oldestKey walletTokenMetadataKey
		var oldestTime time.Time
		found := false
		for candidate, entry := range c.metadata {
			if !found || entry.expiresAt.Before(oldestTime) {
				oldestKey, oldestTime, found = candidate, entry.expiresAt, true
			}
		}
		if !found {
			return
		}
		c.removeMetadataLocked(oldestKey)
	}
	c.metadata[key] = value
	c.metadataBytes += size
}
