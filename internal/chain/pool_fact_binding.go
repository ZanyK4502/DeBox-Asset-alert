package chain

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// PoolFactsCacheIdentity identifies the actual immutable RPC client instance,
// including when Client is embedded by a collector-only read wrapper.
func (c *Client) PoolFactsCacheIdentity() any { return c }

// ImmutablePoolBinding is an optional cache-admission proof, not a change to
// protocol classification. The collector separately limits callers to known
// Uniswap/Pancake V2/V3 factories. A finalized factory mapping must point back
// to the supplied pool before its immutable identity can outlive one task.
func (c *Client) ImmutablePoolBinding(ctx context.Context, pool, token0, token1, factory, version, chainKey string) (bool, error) {
	profile, err := ChainProfile(chainKey, "")
	if err != nil {
		return false, err
	}
	addresses := []*string{&pool, &token0, &token1, &factory}
	for _, value := range addresses {
		*value, err = ValidateAddress(*value)
		if err != nil {
			return false, err
		}
	}
	zero := "0x" + strings.Repeat("0", 40)
	if token0 == token1 || token0 == zero || token1 == zero || pool == zero || factory == zero {
		return false, fmt.Errorf("invalid immutable pool identity")
	}
	if version != "v2" && version != "v3" {
		return false, fmt.Errorf("unsupported immutable pool binding")
	}
	client := *c.boundedMarketClient()
	if client.maxResponseBytes > 64<<10 {
		client.maxResponseBytes = 64 << 10
	}
	call := func(to, data string) ([]byte, error) {
		result, err := client.rpc(ctx, profile, "eth_call", []any{map[string]any{"to": to, "data": data}, "finalized"})
		if err != nil {
			return nil, err
		}
		encoded, ok := result.(string)
		if !ok || len(encoded) != 66 || !strings.HasPrefix(encoded, "0x") {
			return nil, fmt.Errorf("invalid immutable pool ABI word")
		}
		word, err := hex.DecodeString(encoded[2:])
		if err != nil {
			return nil, fmt.Errorf("invalid immutable pool ABI word")
		}
		return word, nil
	}
	data := "0xe6a43905" + strings.Repeat("0", 24) + token0[2:] + strings.Repeat("0", 24) + token1[2:]
	if version == "v3" {
		word, err := call(pool, "0xddca3f43")
		if err != nil {
			return false, err
		}
		fee := new(big.Int).SetBytes(word)
		if fee.BitLen() > 24 {
			return false, fmt.Errorf("invalid immutable pool uint24 fee")
		}
		data = "0x1698ee82" + strings.Repeat("0", 24) + token0[2:] + strings.Repeat("0", 24) + token1[2:] + fmt.Sprintf("%064x", fee)
	}
	word, err := call(factory, data)
	if err != nil {
		return false, err
	}
	for _, high := range word[:12] {
		if high != 0 {
			return false, fmt.Errorf("invalid immutable pool ABI address")
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return "0x"+hex.EncodeToString(word[12:]) == pool, nil
}
