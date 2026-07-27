package assetcatalog

import (
	"errors"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
)

const (
	SourceCoinGecko   = "coingecko"
	SourceDexScreener = "dexscreener"

	IdentityVerified    = "verified"
	IdentitySingleChain = "single_chain"
)

var (
	ErrInvalidQuery = errors.New("invalid asset search query")
	ErrNotFound     = errors.New("asset not found")
	ErrUnavailable  = errors.New("asset catalog temporarily unavailable")
	ErrInvalidLogo  = errors.New("invalid asset logo")
)

// Candidate is a chain-independent asset identity returned to the creation
// wizard. Deployments are limited to the six EVM chains supported by this
// product. A DexScreener fallback candidate is deliberately single-chain and
// can never be treated as authoritative cross-chain identity evidence.
type Candidate struct {
	CanonicalAssetID string       `json:"canonical_asset_id"`
	Name             string       `json:"name"`
	Symbol           string       `json:"symbol"`
	LogoURL          string       `json:"logo_url,omitempty"`
	MarketCapRank    *int         `json:"market_cap_rank,omitempty"`
	IdentitySource   string       `json:"identity_source"`
	IdentityStatus   string       `json:"identity_status"`
	Deployments      []Deployment `json:"deployments"`

	remoteLogoURL string
	liquidityUSD  string
}

type Deployment struct {
	ChainKey        string `json:"chain_key"`
	ChainID         int64  `json:"chain_id"`
	ChainName       string `json:"chain_name"`
	PlatformID      string `json:"platform_id"`
	ContractAddress string `json:"contract_address"`
}

type SearchResult struct {
	Query      string      `json:"query"`
	Source     string      `json:"source"`
	Degraded   bool        `json:"degraded"`
	Candidates []Candidate `json:"candidates"`
}

type Logo struct {
	ContentType string
	Body        []byte
	ETag        string
}

func normalizeQuery(value string) (string, error) {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if len(value) < 2 || len(value) > 80 {
		return "", ErrInvalidQuery
	}
	return value, nil
}

func normalizeLimit(value int) int {
	if value <= 0 {
		return 12
	}
	if value > 25 {
		return 25
	}
	return value
}

func normalizeDeployment(platformID, address string) (Deployment, bool) {
	profile, ok := profileByPlatformID(platformID)
	if !ok {
		return Deployment{}, false
	}
	normalizedAddress, err := chain.ValidateAddress(address)
	if err != nil {
		return Deployment{}, false
	}
	return Deployment{
		ChainKey:        profile.ChainKey,
		ChainID:         profile.ChainID,
		ChainName:       profile.ChainName,
		PlatformID:      profile.PlatformID,
		ContractAddress: normalizedAddress,
	}, true
}
