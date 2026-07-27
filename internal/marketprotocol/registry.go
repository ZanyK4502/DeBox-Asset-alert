package marketprotocol

import (
	"sort"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
)

const (
	MonitoringFull      = "full"
	MonitoringQuoteOnly = "quote_only"
)

var FullMonitoringFeatures = []string{
	"price",
	"liquidity",
	"volume",
	"buy_sell",
	"large_trade",
	"liquidity_added",
	"liquidity_removed",
	"new_pool",
	"history",
}

// Deployment is an exact, chain-scoped protocol deployment. A provider label
// never grants parser support: a pool must return one of these factories from
// factory() on the selected chain.
type Deployment struct {
	ChainKey string `json:"chain_key"`
	ChainID  int64  `json:"chain_id"`
	DEX      string `json:"dex"`
	Protocol string `json:"protocol"`
	Version  string `json:"version"`
	Adapter  string `json:"adapter"`
	Factory  string `json:"factory"`
}

// Addresses are taken from the protocols' official deployment documentation.
// Keep every address lower-case so comparisons never depend on checksum form.
var deployments = []Deployment{
	// Uniswap deployments.json (all six production networks).
	deployment("bsc", "uniswap", "uniswap", "v2", marketparse.AdapterV2, "0x8909dc15e40173ff4699343b6eb8132c65e18ec6"),
	deployment("bsc", "uniswap", "uniswap", "v3", marketparse.AdapterV3, "0xdb1d10011ad0ff90774d0c6bb92e5c5c8b4461f7"),
	deployment("ethereum", "uniswap", "uniswap", "v2", marketparse.AdapterV2, "0x5c69bee701ef814a2b6a3edd4b1652cb9cc5aa6f"),
	deployment("ethereum", "uniswap", "uniswap", "v3", marketparse.AdapterV3, "0x1f98431c8ad98523631ae4a59f267346ea31f984"),
	deployment("base", "uniswap", "uniswap", "v2", marketparse.AdapterV2, "0x8909dc15e40173ff4699343b6eb8132c65e18ec6"),
	deployment("base", "uniswap", "uniswap", "v3", marketparse.AdapterV3, "0x33128a8fc17869897dce68ed026d694621f6fdfd"),
	deployment("polygon", "uniswap", "uniswap", "v2", marketparse.AdapterV2, "0x9e5a52f57b3038f1b8eee45f28b3c1967e22799c"),
	deployment("polygon", "uniswap", "uniswap", "v3", marketparse.AdapterV3, "0x1f98431c8ad98523631ae4a59f267346ea31f984"),
	deployment("arbitrum", "uniswap", "uniswap", "v2", marketparse.AdapterV2, "0xf1d7cc64fb4452f05c498126312ebe29f30fbcf9"),
	deployment("arbitrum", "uniswap", "uniswap", "v3", marketparse.AdapterV3, "0x1f98431c8ad98523631ae4a59f267346ea31f984"),
	deployment("optimism", "uniswap", "uniswap", "v2", marketparse.AdapterV2, "0x0c3c1c532f1e39edf36be9fe0be1410313e074bf"),
	deployment("optimism", "uniswap", "uniswap", "v3", marketparse.AdapterV3, "0x1f98431c8ad98523631ae4a59f267346ea31f984"),

	// PancakeSwap official BNB Chain factories.
	deployment("bsc", "pancakeswap", "pancakeswap", "v2", marketparse.AdapterV2, marketparse.BSCPancakeV2Factory),
	deployment("bsc", "pancakeswap", "pancakeswap", "v3", marketparse.AdapterV3, marketparse.BSCPancakeV3Factory),

	// QuickSwap official Polygon deployments.
	deployment("polygon", "quickswap", "quickswap", "v2", marketparse.AdapterV2, "0x5757371414417b8c6caad45baef941abc7d3ab32"),
	deployment("polygon", "quickswap", "quickswap", "algebra", marketparse.AdapterAlgebra, "0x411b0facc3489691f28ad58c47006af5e3ab3a28"),

	// Camelot official Arbitrum One deployments.
	deployment("arbitrum", "camelot", "camelot", "v2", marketparse.AdapterV2, "0x6eccab422d763ac031210895c81787e87b43a652"),
	deployment("arbitrum", "camelot", "camelot", "algebra-v3", marketparse.AdapterAlgebra, "0x1a3c9b1d2f0529d97f2afc5136cc23e58f1fd35b"),
	deployment("arbitrum", "camelot", "camelot", "algebra-integral", marketparse.AdapterAlgebra, "0xbefc4b405041c5833f53412ff997ed2f697a2f37"),

	// Aerodrome and Velodrome official classic Solidly deployments.
	deployment("base", "aerodrome", "aerodrome", "solidly", marketparse.AdapterSolidly, "0x420dd381b31aef6683db6b902084cb0ffece40da"),
	deployment("optimism", "velodrome", "velodrome", "solidly", marketparse.AdapterSolidly, "0xf1046053aa5682b4f9a81b5481394da16be5ff5a"),
}

func deployment(
	chainKey, dex, protocol, version, adapter, factory string,
) Deployment {
	profile, err := chain.ChainProfile(chainKey, "")
	if err != nil {
		panic(err)
	}
	address, err := chain.ValidateAddress(factory)
	if err != nil {
		panic("invalid trusted DEX factory " + factory)
	}
	return Deployment{
		ChainKey: profile.Key,
		ChainID:  profile.ChainID,
		DEX:      dex,
		Protocol: protocol,
		Version:  version,
		Adapter:  adapter,
		Factory:  address,
	}
}

func Lookup(chainKey, factory string) (Deployment, bool) {
	profile, err := chain.ChainProfile(chainKey, "")
	if err != nil {
		return Deployment{}, false
	}
	address, err := chain.ValidateAddress(factory)
	if err != nil {
		return Deployment{}, false
	}
	for _, candidate := range deployments {
		if candidate.ChainKey == profile.Key && candidate.Factory == address {
			return candidate, true
		}
	}
	return Deployment{}, false
}

func Deployments(chainKey string) []Deployment {
	profile, err := chain.ChainProfile(chainKey, "")
	if err != nil {
		return nil
	}
	result := make([]Deployment, 0)
	for _, candidate := range deployments {
		if candidate.ChainKey == profile.Key {
			result = append(result, candidate)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].DEX != result[j].DEX {
			return result[i].DEX < result[j].DEX
		}
		if result[i].Version != result[j].Version {
			return result[i].Version < result[j].Version
		}
		return result[i].Factory < result[j].Factory
	})
	return result
}

func ProtocolNames() []string {
	seen := make(map[string]struct{})
	for _, value := range deployments {
		name := strings.TrimSpace(value.DEX) + " " + strings.TrimSpace(value.Version)
		seen[name] = struct{}{}
	}
	for _, name := range []string{
		"PancakeSwap Infinity CL",
		"PancakeSwap Infinity Bin",
		"Four.meme",
	} {
		seen[name] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func FactoryEmitters(chainKey string) []marketparse.Emitter {
	values := Deployments(chainKey)
	result := make([]marketparse.Emitter, 0, len(values)+4)
	for _, value := range values {
		result = append(result, marketparse.Emitter{
			Address:  value.Factory,
			Protocol: value.Protocol,
			Version:  value.Version,
			Adapter:  value.Adapter,
		})
	}
	if chain.NormalizeChainKey(chainKey, "") == "bsc" {
		result = append(result,
			marketparse.Emitter{
				Address: marketparse.BSCInfinityCLManager, Protocol: "pancakeswap_infinity",
				Version: "cl", Adapter: marketparse.AdapterInfinityCL,
			},
			marketparse.Emitter{
				Address: marketparse.BSCInfinityBinManager, Protocol: "pancakeswap_infinity",
				Version: "bin", Adapter: marketparse.AdapterInfinityBin,
			},
			marketparse.Emitter{
				Address: marketparse.BSCFourMemeTokenManager, Protocol: "four_meme",
				Version: "v2", Adapter: marketparse.AdapterFourMemeV2,
			},
			marketparse.Emitter{
				Address: marketparse.BSCFourMemeTokenManager, Protocol: "four_meme",
				Version: "v1", Adapter: marketparse.AdapterFourMemeV1,
			},
		)
	}
	return result
}
