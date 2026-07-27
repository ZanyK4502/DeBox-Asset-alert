package assetcatalog

import "strings"

type supportedPlatform struct {
	ChainKey      string
	ChainID       int64
	ChainName     string
	PlatformID    string
	DexScreenerID string
}

var supportedPlatforms = []supportedPlatform{
	{
		ChainKey: "bsc", ChainID: 56, ChainName: "BNB Chain",
		PlatformID: "binance-smart-chain", DexScreenerID: "bsc",
	},
	{
		ChainKey: "ethereum", ChainID: 1, ChainName: "Ethereum",
		PlatformID: "ethereum", DexScreenerID: "ethereum",
	},
	{
		ChainKey: "base", ChainID: 8453, ChainName: "Base",
		PlatformID: "base", DexScreenerID: "base",
	},
	{
		ChainKey: "polygon", ChainID: 137, ChainName: "Polygon",
		PlatformID: "polygon-pos", DexScreenerID: "polygon",
	},
	{
		ChainKey: "arbitrum", ChainID: 42161, ChainName: "Arbitrum",
		PlatformID: "arbitrum-one", DexScreenerID: "arbitrum",
	},
	{
		ChainKey: "optimism", ChainID: 10, ChainName: "Optimism",
		PlatformID: "optimistic-ethereum", DexScreenerID: "optimism",
	},
}

func profileByPlatformID(value string) (supportedPlatform, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, profile := range supportedPlatforms {
		if profile.PlatformID == value {
			return profile, true
		}
	}
	return supportedPlatform{}, false
}

func profileByChainKey(value string) (supportedPlatform, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "bnb", "bnbchain", "bnb_chain":
		value = "bsc"
	}
	for _, profile := range supportedPlatforms {
		if profile.ChainKey == value {
			return profile, true
		}
	}
	return supportedPlatform{}, false
}

func profileByDexScreenerID(value string) (supportedPlatform, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, profile := range supportedPlatforms {
		if profile.DexScreenerID == value {
			return profile, true
		}
	}
	return supportedPlatform{}, false
}
