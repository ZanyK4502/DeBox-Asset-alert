package assetcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"sort"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
)

type primaryProvider interface {
	Search(context.Context, string, int) ([]Candidate, error)
	ResolveContract(context.Context, string, string) (*Candidate, error)
}

type pairSearcher interface {
	SearchPairs(context.Context, string) ([]marketdata.Pair, error)
}

type Catalog struct {
	primary  primaryProvider
	fallback pairSearcher
	logos    *logoProxy
}

func NewCatalog(
	primary primaryProvider,
	fallback pairSearcher,
	logoHTTPClient HTTPDoer,
) (*Catalog, error) {
	if primary == nil {
		return nil, fmt.Errorf("asset identity provider is required")
	}
	logos, err := newLogoProxy(logoHTTPClient)
	if err != nil {
		return nil, err
	}
	return &Catalog{
		primary: primary, fallback: fallback, logos: logos,
	}, nil
}

func (c *Catalog) Search(
	ctx context.Context,
	query string,
	limit int,
) (SearchResult, error) {
	query, err := normalizeQuery(query)
	if err != nil {
		return SearchResult{}, err
	}
	limit = normalizeLimit(limit)
	candidates, primaryErr := c.primary.Search(ctx, query, limit)
	if primaryErr == nil && len(candidates) > 0 {
		c.decorateLogos(candidates)
		return SearchResult{
			Query: query, Source: SourceCoinGecko, Candidates: candidates,
		}, nil
	}
	if c.fallback == nil {
		if primaryErr != nil {
			return SearchResult{}, normalizeProviderError(primaryErr)
		}
		return SearchResult{
			Query: query, Source: SourceCoinGecko, Candidates: []Candidate{},
		}, nil
	}
	pairs, fallbackErr := c.fallback.SearchPairs(ctx, query)
	if fallbackErr != nil {
		if primaryErr != nil {
			return SearchResult{}, ErrUnavailable
		}
		return SearchResult{}, normalizeProviderError(fallbackErr)
	}
	candidates = candidatesFromPairs(pairs, query, limit)
	c.decorateLogos(candidates)
	return SearchResult{
		Query:      query,
		Source:     SourceDexScreener,
		Degraded:   primaryErr != nil,
		Candidates: candidates,
	}, nil
}

func (c *Catalog) ResolveContract(
	ctx context.Context,
	chainKey string,
	contractAddress string,
) (*Candidate, error) {
	candidate, err := c.primary.ResolveContract(ctx, chainKey, contractAddress)
	if err != nil {
		return nil, normalizeProviderError(err)
	}
	decorated := []Candidate{*candidate}
	c.decorateLogos(decorated)
	*candidate = decorated[0]
	return candidate, nil
}

func (c *Catalog) Logo(
	ctx context.Context,
	sourceURL string,
) (Logo, error) {
	return c.logos.Fetch(ctx, sourceURL)
}

func (c *Catalog) decorateLogos(candidates []Candidate) {
	for index := range candidates {
		sourceURL, err := normalizeLogoURL(candidates[index].remoteLogoURL)
		if err != nil {
			candidates[index].LogoURL = ""
			continue
		}
		candidates[index].LogoURL = "/api/market/assets/logo?source=" +
			url.QueryEscape(sourceURL)
	}
}

func candidatesFromPairs(
	pairs []marketdata.Pair,
	query string,
	limit int,
) []Candidate {
	query = strings.ToLower(strings.TrimSpace(query))
	grouped := make(map[string]Candidate)
	for _, pair := range pairs {
		profile, ok := profileByDexScreenerID(pair.ChainID)
		if !ok {
			continue
		}
		for _, token := range []marketdata.Token{pair.BaseToken, pair.QuoteToken} {
			if !tokenMatchesQuery(token, query) {
				continue
			}
			address, err := chain.ValidateAddress(token.Address)
			if err != nil {
				continue
			}
			key := profile.ChainKey + ":" + address
			candidate, exists := grouped[key]
			if !exists {
				candidate = Candidate{
					CanonicalAssetID: fmt.Sprintf(
						"eip155:%d/erc20:%s", profile.ChainID, address,
					),
					Name:           strings.TrimSpace(token.Name),
					Symbol:         strings.ToUpper(strings.TrimSpace(token.Symbol)),
					IdentitySource: SourceDexScreener,
					IdentityStatus: IdentitySingleChain,
					Deployments: []Deployment{{
						ChainKey:        profile.ChainKey,
						ChainID:         profile.ChainID,
						ChainName:       profile.ChainName,
						PlatformID:      profile.PlatformID,
						ContractAddress: address,
					}},
					liquidityUSD: string(pair.Liquidity.USD),
				}
				candidate.remoteLogoURL = pairImageURL(pair.Info)
				grouped[key] = candidate
				continue
			}
			if compareDecimal(
				string(pair.Liquidity.USD), candidate.liquidityUSD,
			) > 0 {
				candidate.liquidityUSD = string(pair.Liquidity.USD)
				if imageURL := pairImageURL(pair.Info); imageURL != "" {
					candidate.remoteLogoURL = imageURL
				}
				grouped[key] = candidate
			}
		}
	}
	result := make([]Candidate, 0, len(grouped))
	for _, candidate := range grouped {
		result = append(result, candidate)
	}
	sort.SliceStable(result, func(i, j int) bool {
		leftName := strings.EqualFold(result[i].Name, query)
		rightName := strings.EqualFold(result[j].Name, query)
		if leftName != rightName {
			return leftName
		}
		leftSymbol := strings.EqualFold(result[i].Symbol, query)
		rightSymbol := strings.EqualFold(result[j].Symbol, query)
		if leftSymbol != rightSymbol {
			return leftSymbol
		}
		if liquidityOrder := compareDecimal(
			result[i].liquidityUSD, result[j].liquidityUSD,
		); liquidityOrder != 0 {
			return liquidityOrder > 0
		}
		return result[i].CanonicalAssetID < result[j].CanonicalAssetID
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result
}

func tokenMatchesQuery(token marketdata.Token, query string) bool {
	name := strings.ToLower(strings.TrimSpace(token.Name))
	symbol := strings.ToLower(strings.TrimSpace(token.Symbol))
	return name == query || symbol == query ||
		strings.Contains(name, query) || strings.Contains(symbol, query)
}

func pairImageURL(info json.RawMessage) string {
	if len(info) == 0 {
		return ""
	}
	var decoded struct {
		ImageURL string `json:"imageUrl"`
	}
	if json.Unmarshal(info, &decoded) != nil {
		return ""
	}
	return strings.TrimSpace(decoded.ImageURL)
}

func compareDecimal(left, right string) int {
	leftValue := new(big.Rat)
	if _, ok := leftValue.SetString(strings.TrimSpace(left)); !ok {
		leftValue.SetInt64(0)
	}
	rightValue := new(big.Rat)
	if _, ok := rightValue.SetString(strings.TrimSpace(right)); !ok {
		rightValue.SetInt64(0)
	}
	return leftValue.Cmp(rightValue)
}

func normalizeProviderError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrInvalidQuery),
		errors.Is(err, ErrNotFound),
		errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded):
		return err
	default:
		return ErrUnavailable
	}
}
