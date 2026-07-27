package assetcatalog

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
)

func TestCrossChainVerifierAcceptsExactCoinGeckoMappings(t *testing.T) {
	t.Parallel()

	bscAddress := testAddress(501)
	baseAddress := testAddress(502)
	candidate := verifiedIdentityCandidate("project-token", []Deployment{
		deploymentForTest("bsc", bscAddress),
		deploymentForTest("base", baseAddress),
	})
	var poolCalls atomic.Int64
	catalog := newIdentityVerifierForTest(
		t,
		func(_ string, _ string) (*Candidate, error) {
			copy := candidate
			return &copy, nil
		},
		map[string]chain.TokenMetadata{
			"bsc:" + bscAddress:   tokenMetadata(bscAddress),
			"base:" + baseAddress: tokenMetadata(baseAddress),
		},
		fakePoolDiscovery{calls: &poolCalls},
	)
	result, err := catalog.VerifyCrossChainIdentity(
		context.Background(),
		CrossChainVerifyInput{
			CanonicalAssetID: "project-token",
			Contracts: []ManualContractInput{
				{ChainKey: "bsc", ContractAddress: bscAddress},
				{ChainKey: "base", ContractAddress: baseAddress},
			},
		},
	)
	if err != nil {
		t.Fatalf("VerifyCrossChainIdentity() error = %v", err)
	}
	if result.CanonicalAssetID != "project-token" ||
		result.IdentitySource != SourceCoinGecko ||
		result.VerificationStatus != VerificationStatusVerified ||
		len(result.Contracts) != 2 ||
		len(result.Evidence) != 2 ||
		result.VerifiedAt.IsZero() {
		t.Fatalf("verification result = %#v", result)
	}
	for index, evidence := range result.Evidence {
		if evidence.ChainKey != result.Contracts[index].ChainKey ||
			evidence.ContractAddress != result.Contracts[index].ContractAddress ||
			evidence.Source != SourceCoinGecko ||
			evidence.EvidenceType != EvidenceTypeCanonicalAsset ||
			evidence.ExternalAssetID != "project-token" ||
			evidence.Verdict != EvidenceVerdictSupports ||
			evidence.Confidence != "1.0000" ||
			!evidence.ObservedAt.Equal(result.VerifiedAt) ||
			!strings.Contains(
				evidence.EvidenceKey,
				result.Contracts[index].ContractAddress,
			) {
			t.Fatalf("evidence %d = %#v", index, evidence)
		}
	}
	if poolCalls.Load() != 0 {
		t.Fatalf("identity verifier called DexScreener %d times", poolCalls.Load())
	}
}

func TestCrossChainVerifierSupportsAllSixChains(t *testing.T) {
	t.Parallel()

	chainKeys := []string{
		"bsc", "ethereum", "base", "polygon", "arbitrum", "optimism",
	}
	deployments := make([]Deployment, 0, len(chainKeys))
	inputs := make([]ManualContractInput, 0, len(chainKeys))
	metadata := make(map[string]chain.TokenMetadata, len(chainKeys))
	for index, chainKey := range chainKeys {
		address := testAddress(600 + index)
		deployments = append(
			deployments,
			deploymentForTest(chainKey, address),
		)
		inputs = append(inputs, ManualContractInput{
			ChainKey: chainKey, ContractAddress: address,
		})
		metadata[chainKey+":"+address] = tokenMetadata(address)
	}
	candidate := verifiedIdentityCandidate("project-token", deployments)
	catalog := newIdentityVerifierForTest(
		t,
		func(_ string, _ string) (*Candidate, error) {
			copy := candidate
			return &copy, nil
		},
		metadata,
		fakePoolDiscovery{},
	)
	result, err := catalog.VerifyCrossChainIdentity(
		context.Background(),
		CrossChainVerifyInput{
			CanonicalAssetID: "project-token",
			Contracts:        inputs,
		},
	)
	if err != nil {
		t.Fatalf("six-chain verification error = %v", err)
	}
	if len(result.Contracts) != 6 || len(result.Evidence) != 6 {
		t.Fatalf("six-chain result = %#v", result)
	}
}

func TestCrossChainVerifierRejectsSameMetadataWithDifferentIDs(t *testing.T) {
	t.Parallel()

	bscAddress := testAddress(701)
	baseAddress := testAddress(702)
	catalog := newIdentityVerifierForTest(
		t,
		func(chainKey string, address string) (*Candidate, error) {
			id := "project-a"
			if chainKey == "base" {
				id = "project-b"
			}
			value := verifiedIdentityCandidate(id, []Deployment{
				deploymentForTest(chainKey, address),
				deploymentForTest("ethereum", testAddress(799)),
			})
			return &value, nil
		},
		map[string]chain.TokenMetadata{
			"bsc:" + bscAddress:   tokenMetadata(bscAddress),
			"base:" + baseAddress: tokenMetadata(baseAddress),
		},
		fakePoolDiscovery{},
	)
	_, err := catalog.VerifyCrossChainIdentity(
		context.Background(),
		CrossChainVerifyInput{
			CanonicalAssetID: "project-a",
			Contracts: []ManualContractInput{
				{ChainKey: "bsc", ContractAddress: bscAddress},
				{ChainKey: "base", ContractAddress: baseAddress},
			},
		},
	)
	if !errors.Is(err, ErrCrossChainIdentityConflict) {
		t.Fatalf("different CoinGecko IDs error = %v", err)
	}
}

func TestCrossChainVerifierFailsClosedForMissingOrUnavailableIdentity(t *testing.T) {
	t.Parallel()

	bscAddress := testAddress(801)
	baseAddress := testAddress(802)
	metadata := map[string]chain.TokenMetadata{
		"bsc:" + bscAddress:   tokenMetadata(bscAddress),
		"base:" + baseAddress: tokenMetadata(baseAddress),
	}
	input := CrossChainVerifyInput{
		CanonicalAssetID: "project-token",
		Contracts: []ManualContractInput{
			{ChainKey: "bsc", ContractAddress: bscAddress},
			{ChainKey: "base", ContractAddress: baseAddress},
		},
	}
	tests := []struct {
		name string
		err  error
		want error
	}{
		{
			name: "not listed",
			err:  ErrNotFound,
			want: ErrCrossChainIdentityUnverified,
		},
		{
			name: "authority unavailable",
			err:  errors.New("private upstream diagnostics"),
			want: ErrUnavailable,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			catalog := newIdentityVerifierForTest(
				t,
				func(_ string, _ string) (*Candidate, error) {
					return nil, test.err
				},
				metadata,
				fakePoolDiscovery{},
			)
			_, err := catalog.VerifyCrossChainIdentity(
				context.Background(),
				input,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("VerifyCrossChainIdentity() error = %v", err)
			}
			if strings.Contains(err.Error(), "private upstream diagnostics") {
				t.Fatalf("provider diagnostics leaked: %v", err)
			}
		})
	}
}

func TestCrossChainVerifierNeverAcceptsDexScreenerAsAuthority(t *testing.T) {
	t.Parallel()

	bscAddress := testAddress(851)
	baseAddress := testAddress(852)
	candidate := verifiedIdentityCandidate("project-token", []Deployment{
		deploymentForTest("bsc", bscAddress),
		deploymentForTest("base", baseAddress),
	})
	candidate.IdentitySource = SourceDexScreener
	catalog := newIdentityVerifierForTest(
		t,
		func(_ string, _ string) (*Candidate, error) {
			copy := candidate
			return &copy, nil
		},
		map[string]chain.TokenMetadata{
			"bsc:" + bscAddress:   tokenMetadata(bscAddress),
			"base:" + baseAddress: tokenMetadata(baseAddress),
		},
		fakePoolDiscovery{},
	)
	_, err := catalog.VerifyCrossChainIdentity(
		context.Background(),
		CrossChainVerifyInput{
			CanonicalAssetID: "project-token",
			Contracts: []ManualContractInput{
				{ChainKey: "bsc", ContractAddress: bscAddress},
				{ChainKey: "base", ContractAddress: baseAddress},
			},
		},
	)
	if !errors.Is(err, ErrCrossChainIdentityUnverified) {
		t.Fatalf("non-authoritative source error = %v", err)
	}
}

func TestCrossChainVerifierRejectsUnreadableContract(t *testing.T) {
	t.Parallel()

	catalog, err := NewCatalog(
		fakePrimary{},
		nil,
		nil,
		WithManualProviders(
			fakeTokenMetadata{err: errors.New("execution reverted")},
			fakePoolDiscovery{},
		),
	)
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	_, err = catalog.VerifyCrossChainIdentity(
		context.Background(),
		CrossChainVerifyInput{
			CanonicalAssetID: "project-token",
			Contracts: []ManualContractInput{
				{ChainKey: "bsc", ContractAddress: testAddress(861)},
				{ChainKey: "base", ContractAddress: testAddress(862)},
			},
		},
	)
	if !errors.Is(err, ErrContractUnreadable) ||
		strings.Contains(err.Error(), "execution reverted") {
		t.Fatalf("unreadable contract error = %v", err)
	}
}

func TestCrossChainVerifierRejectsMissingMappingAndMetadataConflict(t *testing.T) {
	t.Parallel()

	bscAddress := testAddress(901)
	baseAddress := testAddress(902)
	input := CrossChainVerifyInput{
		CanonicalAssetID: "project-token",
		Contracts: []ManualContractInput{
			{ChainKey: "bsc", ContractAddress: bscAddress},
			{ChainKey: "base", ContractAddress: baseAddress},
		},
	}
	metadata := map[string]chain.TokenMetadata{
		"bsc:" + bscAddress:   tokenMetadata(bscAddress),
		"base:" + baseAddress: tokenMetadata(baseAddress),
	}
	tests := []struct {
		name      string
		candidate Candidate
		metadata  map[string]chain.TokenMetadata
	}{
		{
			name: "candidate omits exact contract",
			candidate: verifiedIdentityCandidate(
				"project-token",
				[]Deployment{
					deploymentForTest("bsc", bscAddress),
					deploymentForTest("base", testAddress(999)),
				},
			),
			metadata: metadata,
		},
		{
			name: "on-chain metadata clearly conflicts",
			candidate: verifiedIdentityCandidate(
				"project-token",
				[]Deployment{
					deploymentForTest("bsc", bscAddress),
					deploymentForTest("base", baseAddress),
				},
			),
			metadata: map[string]chain.TokenMetadata{
				"bsc:" + bscAddress: tokenMetadata(bscAddress),
				"base:" + baseAddress: {
					Address: baseAddress,
					Name:    "Different Token", Symbol: "OTHER", Decimals: 18,
					TotalSupplyRaw: "1000",
				},
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			catalog := newIdentityVerifierForTest(
				t,
				func(_ string, _ string) (*Candidate, error) {
					copy := test.candidate
					return &copy, nil
				},
				test.metadata,
				fakePoolDiscovery{},
			)
			_, err := catalog.VerifyCrossChainIdentity(
				context.Background(),
				input,
			)
			if !errors.Is(err, ErrCrossChainIdentityConflict) {
				t.Fatalf("strict conflict error = %v", err)
			}
		})
	}
}

func TestCrossChainVerifierValidatesBatchBeforeProviderCalls(t *testing.T) {
	t.Parallel()

	var metadataCalls atomic.Int64
	catalog, err := NewCatalog(
		fakePrimary{},
		nil,
		nil,
		WithManualProviders(
			fakeTokenMetadata{calls: &metadataCalls},
			fakePoolDiscovery{},
		),
	)
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	_, err = catalog.VerifyCrossChainIdentity(
		context.Background(),
		CrossChainVerifyInput{
			CanonicalAssetID: "project-token",
			Contracts: []ManualContractInput{{
				ChainKey: "bsc", ContractAddress: testAddress(1),
			}},
		},
	)
	if !errors.Is(err, ErrInvalidCrossChainRequest) {
		t.Fatalf("single-chain request error = %v", err)
	}
	if metadataCalls.Load() != 0 {
		t.Fatalf("provider called for invalid request %d times", metadataCalls.Load())
	}
}

func newIdentityVerifierForTest(
	t *testing.T,
	resolve func(string, string) (*Candidate, error),
	metadata map[string]chain.TokenMetadata,
	pools fakePoolDiscovery,
) *Catalog {
	t.Helper()
	catalog, err := NewCatalog(
		fakePrimary{resolve: resolve},
		nil,
		nil,
		WithManualProviders(
			fakeTokenMetadata{values: metadata},
			pools,
		),
	)
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	return catalog
}

func verifiedIdentityCandidate(
	id string,
	deployments []Deployment,
) Candidate {
	return Candidate{
		CanonicalAssetID: id,
		Name:             "Project Token",
		Symbol:           "PRJ",
		IdentitySource:   SourceCoinGecko,
		IdentityStatus:   IdentityVerified,
		Deployments:      deployments,
	}
}

func deploymentForTest(chainKey string, address string) Deployment {
	profile, ok := profileByChainKey(chainKey)
	if !ok {
		panic("unsupported test chain")
	}
	return Deployment{
		ChainKey:        profile.ChainKey,
		ChainID:         profile.ChainID,
		ChainName:       profile.ChainName,
		PlatformID:      profile.PlatformID,
		ContractAddress: address,
	}
}
