package assetcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
)

var canonicalAssetIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,199}$`)

type strictIdentityResolution struct {
	contract  normalizedManualContract
	metadata  chain.TokenMetadata
	candidate Candidate
	err       error
}

// VerifyCrossChainIdentity is the final, fail-closed identity check used before
// a multi-chain asset may be created. It intentionally ignores DexScreener,
// user confirmation, names, logos, and pools as sources of identity authority.
func (c *Catalog) VerifyCrossChainIdentity(
	ctx context.Context,
	input CrossChainVerifyInput,
) (CrossChainVerificationResult, error) {
	if c.tokenMetadata == nil {
		return CrossChainVerificationResult{}, ErrUnavailable
	}
	canonicalAssetID := strings.ToLower(strings.TrimSpace(input.CanonicalAssetID))
	if !canonicalAssetIDPattern.MatchString(canonicalAssetID) {
		return CrossChainVerificationResult{}, ErrInvalidCrossChainRequest
	}
	contracts, err := normalizeManualContracts(ManualResolveInput{
		Contracts: input.Contracts,
	})
	if err != nil || len(contracts) < 2 {
		return CrossChainVerificationResult{}, ErrInvalidCrossChainRequest
	}

	resolutions := make([]strictIdentityResolution, len(contracts))
	var wait sync.WaitGroup
	for index, contract := range contracts {
		wait.Add(1)
		go func(index int, contract normalizedManualContract) {
			defer wait.Done()
			resolutions[index] = c.resolveStrictIdentity(
				ctx,
				canonicalAssetID,
				contract,
			)
		}(index, contract)
	}
	wait.Wait()
	for _, resolution := range resolutions {
		if resolution.err != nil {
			return CrossChainVerificationResult{}, resolution.err
		}
	}
	reference := resolutions[0].candidate
	for _, resolution := range resolutions[1:] {
		if resolution.candidate.CanonicalAssetID != reference.CanonicalAssetID ||
			comparableIdentityText(resolution.candidate.Name) !=
				comparableIdentityText(reference.Name) ||
			comparableIdentityText(resolution.candidate.Symbol) !=
				comparableIdentityText(reference.Symbol) {
			return CrossChainVerificationResult{},
				ErrCrossChainIdentityConflict
		}
	}

	verifiedAt := time.Now().UTC()
	result := CrossChainVerificationResult{
		CanonicalAssetID:   canonicalAssetID,
		CanonicalName:      strings.TrimSpace(resolutions[0].candidate.Name),
		Symbol:             strings.ToUpper(strings.TrimSpace(resolutions[0].candidate.Symbol)),
		IdentitySource:     SourceCoinGecko,
		VerificationStatus: VerificationStatusVerified,
		Contracts:          make([]VerifiedContract, len(resolutions)),
		Evidence:           make([]IdentityEvidenceRecord, len(resolutions)),
		VerifiedAt:         verifiedAt,
	}
	for index, resolution := range resolutions {
		contract := resolution.contract
		metadata := resolution.metadata
		result.Contracts[index] = VerifiedContract{
			ChainKey:        contract.profile.ChainKey,
			ChainID:         contract.profile.ChainID,
			ChainName:       contract.profile.ChainName,
			PlatformID:      contract.profile.PlatformID,
			ContractAddress: contract.address,
			TokenName:       metadata.Name,
			TokenSymbol:     metadata.Symbol,
			TokenDecimals:   metadata.Decimals,
			TotalSupplyRaw:  metadata.TotalSupplyRaw,
		}
		payload, marshalErr := json.Marshal(map[string]any{
			"chain_key":                 contract.profile.ChainKey,
			"chain_id":                  contract.profile.ChainID,
			"platform_id":               contract.profile.PlatformID,
			"contract_address":          contract.address,
			"canonical_name":            resolution.candidate.Name,
			"canonical_symbol":          resolution.candidate.Symbol,
			"authoritative_deployments": resolution.candidate.Deployments,
			"onchain_metadata":          metadata,
			"provider_identity_status":  resolution.candidate.IdentityStatus,
		})
		if marshalErr != nil {
			return CrossChainVerificationResult{}, ErrUnavailable
		}
		result.Evidence[index] = IdentityEvidenceRecord{
			EvidenceKey: fmt.Sprintf(
				"coingecko:%s:eip155:%d:erc20:%s",
				canonicalAssetID,
				contract.profile.ChainID,
				contract.address,
			),
			ChainKey:        contract.profile.ChainKey,
			ChainID:         contract.profile.ChainID,
			ContractAddress: contract.address,
			Source:          SourceCoinGecko,
			EvidenceType:    EvidenceTypeCanonicalAsset,
			ExternalAssetID: canonicalAssetID,
			Verdict:         EvidenceVerdictSupports,
			Confidence:      "1.0000",
			Payload:         payload,
			ObservedAt:      verifiedAt,
		}
	}
	return result, nil
}

func (c *Catalog) resolveStrictIdentity(
	ctx context.Context,
	expectedAssetID string,
	contract normalizedManualContract,
) strictIdentityResolution {
	metadata, err := c.tokenMetadata.TokenMetadata(
		ctx,
		contract.address,
		contract.profile.ChainKey,
		"",
	)
	if err != nil {
		if errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			return strictIdentityResolution{err: err}
		}
		return strictIdentityResolution{err: fmt.Errorf(
			"%w: item %d",
			ErrContractUnreadable,
			contract.inputIndex+1,
		)}
	}
	metadata, ok := normalizeTokenMetadata(contract.address, metadata)
	if !ok {
		return strictIdentityResolution{err: fmt.Errorf(
			"%w: item %d",
			ErrContractUnreadable,
			contract.inputIndex+1,
		)}
	}

	candidate, err := c.primary.ResolveContractAuthoritative(
		ctx,
		contract.profile.ChainKey,
		contract.address,
	)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled),
			errors.Is(err, context.DeadlineExceeded):
			return strictIdentityResolution{err: err}
		case errors.Is(err, ErrNotFound):
			return strictIdentityResolution{err: fmt.Errorf(
				"%w: item %d",
				ErrCrossChainIdentityUnverified,
				contract.inputIndex+1,
			)}
		default:
			return strictIdentityResolution{err: ErrUnavailable}
		}
	}
	if candidate == nil ||
		candidate.IdentitySource != SourceCoinGecko ||
		candidate.IdentityStatus != IdentityVerified ||
		strings.TrimSpace(candidate.CanonicalAssetID) == "" {
		return strictIdentityResolution{err: fmt.Errorf(
			"%w: item %d",
			ErrCrossChainIdentityUnverified,
			contract.inputIndex+1,
		)}
	}
	if candidate.CanonicalAssetID != expectedAssetID {
		return strictIdentityResolution{err: fmt.Errorf(
			"%w: item %d",
			ErrCrossChainIdentityConflict,
			contract.inputIndex+1,
		)}
	}
	if !candidateContainsDeployment(*candidate, contract) ||
		metadataClearlyConflicts(metadata, *candidate) {
		return strictIdentityResolution{err: fmt.Errorf(
			"%w: item %d",
			ErrCrossChainIdentityConflict,
			contract.inputIndex+1,
		)}
	}
	return strictIdentityResolution{
		contract: contract, metadata: metadata, candidate: *candidate,
	}
}

func candidateContainsDeployment(
	candidate Candidate,
	contract normalizedManualContract,
) bool {
	for _, deployment := range candidate.Deployments {
		if deployment.ChainID == contract.profile.ChainID &&
			deployment.ChainKey == contract.profile.ChainKey &&
			deployment.ContractAddress == contract.address {
			return true
		}
	}
	return false
}

func metadataClearlyConflicts(
	metadata chain.TokenMetadata,
	candidate Candidate,
) bool {
	nameMatches := comparableIdentityText(metadata.Name) ==
		comparableIdentityText(candidate.Name)
	symbolMatches := comparableIdentityText(metadata.Symbol) ==
		comparableIdentityText(candidate.Symbol)
	return !nameMatches && !symbolMatches
}

func comparableIdentityText(value string) string {
	var normalized strings.Builder
	for _, char := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(char) || unicode.IsNumber(char) {
			normalized.WriteRune(char)
		}
	}
	return normalized.String()
}
