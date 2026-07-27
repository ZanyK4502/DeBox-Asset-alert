package assetcatalog

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
)

const maxManualContracts = 6

type normalizedManualContract struct {
	inputIndex int
	profile    supportedPlatform
	address    string
}

type manualResolution struct {
	entry     ManualContractResult
	candidate Candidate
	err       error
}

// ResolveManualContracts validates one chain+contract input per supported
// chain. It returns a merge recommendation but never persists or merges
// identities. Step 6 remains the final authority before project creation.
func (c *Catalog) ResolveManualContracts(
	ctx context.Context,
	input ManualResolveInput,
) (ManualResolveResult, error) {
	if c.tokenMetadata == nil || c.poolDiscovery == nil {
		return ManualResolveResult{}, ErrUnavailable
	}
	contracts, err := normalizeManualContracts(input)
	if err != nil {
		return ManualResolveResult{}, err
	}
	resolutions := make([]manualResolution, len(contracts))
	var wait sync.WaitGroup
	for index, contract := range contracts {
		wait.Add(1)
		go func(index int, contract normalizedManualContract) {
			defer wait.Done()
			resolutions[index] = c.resolveManualContract(ctx, contract)
		}(index, contract)
	}
	wait.Wait()
	for _, resolution := range resolutions {
		if resolution.err != nil {
			return ManualResolveResult{}, resolution.err
		}
	}

	result := ManualResolveResult{
		Contracts: make([]ManualContractResult, len(resolutions)),
	}
	candidatesByID := make(map[string]Candidate, len(resolutions))
	candidateOrder := make([]string, 0, len(resolutions))
	for index, resolution := range resolutions {
		result.Contracts[index] = resolution.entry
		id := resolution.candidate.CanonicalAssetID
		existing, exists := candidatesByID[id]
		if !exists {
			candidatesByID[id] = resolution.candidate
			candidateOrder = append(candidateOrder, id)
			continue
		}
		if existing.remoteLogoURL == "" &&
			resolution.candidate.remoteLogoURL != "" {
			existing.remoteLogoURL = resolution.candidate.remoteLogoURL
			candidatesByID[id] = existing
		}
	}
	result.Candidates = make([]Candidate, 0, len(candidateOrder))
	for _, id := range candidateOrder {
		result.Candidates = append(result.Candidates, candidatesByID[id])
	}
	c.decorateLogos(result.Candidates)

	if len(result.Contracts) == 1 {
		result.CanMerge = true
		result.MergeStatus = MergeStatusSingleChain
		return result, nil
	}
	firstID := result.Contracts[0].CanonicalAssetID
	canMerge := true
	for _, contract := range result.Contracts {
		if contract.IdentitySource != SourceCoinGecko ||
			contract.IdentityLookupStatus != LookupMatched ||
			contract.CanonicalAssetID != firstID {
			canMerge = false
			break
		}
	}
	result.CanMerge = canMerge
	if canMerge {
		result.MergeStatus = MergeStatusVerified
	} else {
		result.MergeStatus = MergeStatusSeparateProjects
	}
	return result, nil
}

func normalizeManualContracts(
	input ManualResolveInput,
) ([]normalizedManualContract, error) {
	if len(input.Contracts) == 0 || len(input.Contracts) > maxManualContracts {
		return nil, ErrInvalidManualRequest
	}
	result := make([]normalizedManualContract, len(input.Contracts))
	seenChains := make(map[string]struct{}, len(input.Contracts))
	seenContracts := make(map[string]struct{}, len(input.Contracts))
	for index, value := range input.Contracts {
		profile, ok := profileByChainKey(value.ChainKey)
		if !ok {
			return nil, fmt.Errorf(
				"%w: unsupported chain at item %d",
				ErrInvalidManualRequest,
				index+1,
			)
		}
		address, err := chain.ValidateAddress(value.ContractAddress)
		if err != nil {
			return nil, fmt.Errorf(
				"%w: invalid contract at item %d",
				ErrInvalidManualRequest,
				index+1,
			)
		}
		if _, exists := seenChains[profile.ChainKey]; exists {
			return nil, fmt.Errorf(
				"%w: only one contract per chain is allowed",
				ErrInvalidManualRequest,
			)
		}
		key := profile.ChainKey + ":" + address
		if _, exists := seenContracts[key]; exists {
			return nil, fmt.Errorf(
				"%w: duplicate contract",
				ErrInvalidManualRequest,
			)
		}
		seenChains[profile.ChainKey] = struct{}{}
		seenContracts[key] = struct{}{}
		result[index] = normalizedManualContract{
			inputIndex: index,
			profile:    profile,
			address:    address,
		}
	}
	return result, nil
}

func (c *Catalog) resolveManualContract(
	ctx context.Context,
	contract normalizedManualContract,
) manualResolution {
	metadata, err := c.tokenMetadata.TokenMetadata(
		ctx,
		contract.address,
		contract.profile.ChainKey,
		"",
	)
	if err != nil {
		if errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			return manualResolution{err: err}
		}
		return manualResolution{err: fmt.Errorf(
			"%w: item %d",
			ErrContractUnreadable,
			contract.inputIndex+1,
		)}
	}
	if metadata.Address != contract.address ||
		strings.TrimSpace(metadata.Symbol) == "" ||
		metadata.Decimals < 0 ||
		metadata.Decimals > 255 {
		return manualResolution{err: fmt.Errorf(
			"%w: item %d",
			ErrContractUnreadable,
			contract.inputIndex+1,
		)}
	}
	totalSupply := new(big.Int)
	if _, ok := totalSupply.SetString(
		strings.TrimSpace(metadata.TotalSupplyRaw),
		10,
	); !ok || totalSupply.Sign() < 0 {
		return manualResolution{err: fmt.Errorf(
			"%w: item %d",
			ErrContractUnreadable,
			contract.inputIndex+1,
		)}
	}
	tokenSymbol := strings.ToUpper(strings.TrimSpace(metadata.Symbol))
	tokenName := strings.TrimSpace(metadata.Name)
	if tokenName == "" {
		tokenName = tokenSymbol
	}

	candidate, identityStatus := c.resolveManualIdentity(ctx, contract, metadata)
	pairs, marketStatus := c.resolveManualPools(ctx, contract)
	if imageURL := bestPairImage(pairs, contract.address); imageURL != "" {
		candidate.remoteLogoURL = imageURL
	}
	return manualResolution{
		entry: ManualContractResult{
			InputIndex:           contract.inputIndex,
			ChainKey:             contract.profile.ChainKey,
			ChainID:              contract.profile.ChainID,
			ChainName:            contract.profile.ChainName,
			PlatformID:           contract.profile.PlatformID,
			ContractAddress:      contract.address,
			TokenName:            tokenName,
			TokenSymbol:          tokenSymbol,
			TokenDecimals:        metadata.Decimals,
			TotalSupplyRaw:       totalSupply.String(),
			CanonicalAssetID:     candidate.CanonicalAssetID,
			IdentitySource:       candidate.IdentitySource,
			IdentityLookupStatus: identityStatus,
			MarketLookupStatus:   marketStatus,
			DiscoveredPoolCount:  len(pairs),
		},
		candidate: candidate,
	}
}

func (c *Catalog) resolveManualIdentity(
	ctx context.Context,
	contract normalizedManualContract,
	metadata chain.TokenMetadata,
) (Candidate, string) {
	candidate, err := c.primary.ResolveContract(
		ctx,
		contract.profile.ChainKey,
		contract.address,
	)
	if err == nil && candidate != nil {
		return *candidate, LookupMatched
	}
	lookupStatus := LookupUnavailable
	if errors.Is(err, ErrNotFound) {
		lookupStatus = LookupNotListed
	}
	tokenSymbol := strings.ToUpper(strings.TrimSpace(metadata.Symbol))
	tokenName := strings.TrimSpace(metadata.Name)
	if tokenName == "" {
		tokenName = tokenSymbol
	}
	return Candidate{
		CanonicalAssetID: fmt.Sprintf(
			"eip155:%d/erc20:%s",
			contract.profile.ChainID,
			contract.address,
		),
		Name:           tokenName,
		Symbol:         tokenSymbol,
		IdentitySource: SourceOnChain,
		IdentityStatus: IdentitySingleChain,
		Deployments: []Deployment{{
			ChainKey:        contract.profile.ChainKey,
			ChainID:         contract.profile.ChainID,
			ChainName:       contract.profile.ChainName,
			PlatformID:      contract.profile.PlatformID,
			ContractAddress: contract.address,
		}},
	}, lookupStatus
}

func (c *Catalog) resolveManualPools(
	ctx context.Context,
	contract normalizedManualContract,
) ([]marketdata.Pair, string) {
	pairs, err := c.poolDiscovery.DiscoverPools(
		ctx,
		contract.profile.DexScreenerID,
		contract.address,
	)
	if err != nil {
		return nil, MarketUnavailable
	}
	if len(pairs) == 0 {
		return []marketdata.Pair{}, MarketEmpty
	}
	return pairs, MarketAvailable
}

func bestPairImage(pairs []marketdata.Pair, tokenAddress string) string {
	type pairImage struct {
		url       string
		liquidity string
	}
	images := make([]pairImage, 0, len(pairs))
	for _, pair := range pairs {
		if pair.BaseToken.Address != tokenAddress &&
			pair.QuoteToken.Address != tokenAddress {
			continue
		}
		if imageURL := pairImageURL(pair.Info); imageURL != "" {
			images = append(images, pairImage{
				url: imageURL, liquidity: string(pair.Liquidity.USD),
			})
		}
	}
	sort.SliceStable(images, func(i, j int) bool {
		return compareDecimal(images[i].liquidity, images[j].liquidity) > 0
	})
	if len(images) == 0 {
		return ""
	}
	return images[0].url
}
