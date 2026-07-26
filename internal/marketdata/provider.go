package marketdata

import (
	"context"
	"encoding/json"
)

const (
	ChainBSC          = "bsc"
	SourceDexScreener = "dexscreener"
)

// Provider is the market-data boundary used by discovery and collection jobs.
// Implementations must return decimal values as strings so callers never lose
// precision by passing monetary data through float64.
type Provider interface {
	DiscoverPools(ctx context.Context, chainID, tokenAddress string) ([]Pair, error)
	PairsByTokens(ctx context.Context, chainID string, tokenAddresses []string) ([]Pair, error)
	PairsByAddresses(ctx context.Context, chainID string, pairAddresses []string) ([]Pair, error)
}

type Pair struct {
	ChainID       string                       `json:"chainId"`
	DexID         string                       `json:"dexId"`
	URL           string                       `json:"url"`
	PairAddress   string                       `json:"pairAddress"`
	Labels        []string                     `json:"labels,omitempty"`
	BaseToken     Token                        `json:"baseToken"`
	QuoteToken    Token                        `json:"quoteToken"`
	PriceNative   Decimal                      `json:"priceNative"`
	PriceUSD      Decimal                      `json:"priceUsd"`
	Transactions  map[string]TransactionCounts `json:"txns,omitempty"`
	Volume        map[string]Decimal           `json:"volume,omitempty"`
	PriceChange   map[string]Decimal           `json:"priceChange,omitempty"`
	Liquidity     Liquidity                    `json:"liquidity"`
	FDV           Decimal                      `json:"fdv"`
	MarketCap     Decimal                      `json:"marketCap"`
	PairCreatedAt *int64                       `json:"pairCreatedAt,omitempty"`
	Info          json.RawMessage              `json:"info,omitempty"`
	Boosts        json.RawMessage              `json:"boosts,omitempty"`
}

type Token struct {
	Address string `json:"address"`
	Name    string `json:"name"`
	Symbol  string `json:"symbol"`
}

type TransactionCounts struct {
	Buys  int64 `json:"buys"`
	Sells int64 `json:"sells"`
}

type Liquidity struct {
	USD   Decimal `json:"usd"`
	Base  Decimal `json:"base"`
	Quote Decimal `json:"quote"`
}
