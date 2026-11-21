package chain

import (
	"context"
	"fmt"
	"strings"
)

// TokenDecimals reads only decimals(), without requiring optional ERC-20
// name/symbol metadata. Zero is a valid decimal count, not an unknown value.
func (c *Client) TokenDecimals(ctx context.Context, tokenAddress, chainKey, fallback string) (int32, error) {
	profile, err := ChainProfile(chainKey, fallback)
	if err != nil {
		return 0, err
	}
	token, err := ValidateAddress(tokenAddress)
	if err != nil {
		return 0, err
	}
	// All supported EVM chains use 18 decimal places for their native currency.
	if token == "0x0000000000000000000000000000000000000000" {
		return 18, nil
	}
	result, err := c.rpc(ctx, profile, "eth_call", []any{
		map[string]any{"to": token, "data": "0x313ce567"}, "latest",
	})
	if err != nil {
		return 0, fmt.Errorf("read token decimals: %w", err)
	}
	encoded, ok := result.(string)
	if !ok || !strings.HasPrefix(encoded, "0x") || len(encoded) != 66 {
		return 0, fmt.Errorf("invalid token decimals response")
	}
	decimals, err := decodeABIUint(encoded)
	if err != nil || !decimals.IsInt64() || decimals.Sign() < 0 || decimals.Int64() > 255 {
		return 0, fmt.Errorf("invalid token decimals response")
	}
	return int32(decimals.Int64()), nil
}
