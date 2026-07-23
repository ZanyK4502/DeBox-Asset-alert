package chain

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	addressPattern = regexp.MustCompile(`^0x[a-fA-F0-9]{40}$`)
	txHashPattern  = regexp.MustCompile(`^0x[a-fA-F0-9]{64}$`)
)

type Profile struct {
	Key          string `json:"key"`
	Chain        string `json:"-"`
	Network      string `json:"-"`
	ChainID      int64  `json:"chain_id"`
	ChainIDHex   string `json:"chain_id_hex"`
	Name         string `json:"name"`
	NativeSymbol string `json:"native_symbol"`
}

var profiles = map[string]Profile{
	"bsc": {
		Key: "bsc", Chain: "bnb", Network: "mainnet", ChainID: 56,
		ChainIDHex: "0x38", Name: "BNB Chain", NativeSymbol: "BNB",
	},
	"ethereum": {
		Key: "ethereum", Chain: "ethereum", Network: "mainnet", ChainID: 1,
		ChainIDHex: "0x1", Name: "Ethereum", NativeSymbol: "ETH",
	},
	"base": {
		Key: "base", Chain: "base", Network: "mainnet", ChainID: 8453,
		ChainIDHex: "0x2105", Name: "Base", NativeSymbol: "ETH",
	},
	"polygon": {
		Key: "polygon", Chain: "polygon", Network: "mainnet", ChainID: 137,
		ChainIDHex: "0x89", Name: "Polygon", NativeSymbol: "POL",
	},
	"arbitrum": {
		Key: "arbitrum", Chain: "arbitrum", Network: "mainnet", ChainID: 42161,
		ChainIDHex: "0xa4b1", Name: "Arbitrum", NativeSymbol: "ETH",
	},
	"optimism": {
		Key: "optimism", Chain: "optimism", Network: "mainnet", ChainID: 10,
		ChainIDHex: "0xa", Name: "Optimism", NativeSymbol: "ETH",
	},
}

var profileOrder = []string{"bsc", "ethereum", "base", "polygon", "arbitrum", "optimism"}

func NormalizeChainKey(chainKey, fallback string) string {
	key := strings.ToLower(strings.TrimSpace(chainKey))
	if key == "" {
		key = strings.ToLower(strings.TrimSpace(fallback))
	}
	if key == "" {
		key = "bsc"
	}
	switch key {
	case "bnb", "bnbchain", "bnb_chain":
		return "bsc"
	default:
		return key
	}
}

func ChainProfile(chainKey, fallback string) (Profile, error) {
	key := NormalizeChainKey(chainKey, fallback)
	profile, ok := profiles[key]
	if ok {
		return profile, nil
	}
	allowed := make([]string, 0, len(profiles))
	for supported := range profiles {
		allowed = append(allowed, supported)
	}
	sort.Strings(allowed)
	return Profile{}, fmt.Errorf("unsupported chain: %s. supported chains: %s", key, strings.Join(allowed, ", "))
}

func SupportedChains() []Profile {
	result := make([]Profile, 0, len(profileOrder))
	for _, key := range profileOrder {
		result = append(result, profiles[key])
	}
	return result
}

func ValidateAddress(address string) (string, error) {
	value := strings.TrimSpace(address)
	if !addressPattern.MatchString(value) {
		return "", fmt.Errorf("invalid EVM address")
	}
	return "0x" + strings.ToLower(value[2:]), nil
}

func ValidateTransactionHash(txHash string) (string, error) {
	value := strings.TrimSpace(txHash)
	if !txHashPattern.MatchString(value) {
		return "", fmt.Errorf("invalid transaction hash")
	}
	return "0x" + strings.ToLower(value[2:]), nil
}
