package marketdomain

import (
	"errors"
	"testing"
)

func TestLegacyCanonicalAssetID(t *testing.T) {
	t.Parallel()

	got, err := LegacyCanonicalAssetID(
		56,
		"0x0E09FaBB73Bd3Ade0a17ECC321fD13a19e81cE82",
	)
	if err != nil {
		t.Fatalf("LegacyCanonicalAssetID(): %v", err)
	}
	want := "eip155:56/erc20:0x0e09fabb73bd3ade0a17ecc321fd13a19e81ce82"
	if got != want {
		t.Fatalf("LegacyCanonicalAssetID() = %q, want %q", got, want)
	}
}

func TestNormalizeDeploymentRequiresMatchingSupportedChain(t *testing.T) {
	t.Parallel()

	supply := "001000"
	got, err := NormalizeDeployment(Deployment{
		ChainKey:           "bnb",
		ChainID:            56,
		TokenAddress:       "0x0E09FaBB73Bd3Ade0a17ECC321fD13a19e81cE82",
		TokenDecimals:      18,
		TotalSupplyRaw:     &supply,
		VerificationStatus: VerificationSingleChain,
	})
	if err != nil {
		t.Fatalf("NormalizeDeployment(): %v", err)
	}
	if got.ChainKey != "bsc" ||
		got.TokenAddress != "0x0e09fabb73bd3ade0a17ecc321fd13a19e81ce82" ||
		got.TotalSupplyRaw == nil ||
		*got.TotalSupplyRaw != "1000" {
		t.Fatalf("normalized deployment = %#v", got)
	}

	_, err = NormalizeDeployment(Deployment{
		ChainKey:           "base",
		ChainID:            56,
		TokenAddress:       got.TokenAddress,
		TokenDecimals:      18,
		VerificationStatus: VerificationVerified,
	})
	if !errors.Is(err, ErrInvalidDeployment) {
		t.Fatalf("mismatched chain error = %v, want ErrInvalidDeployment", err)
	}
}

func TestProjectAllowsOneSingleChainDeployment(t *testing.T) {
	t.Parallel()

	project := projectFixture([]Deployment{
		deploymentFixture(1, "bsc", 56, VerificationSingleChain),
	})
	if err := project.ValidateForCreation(); err != nil {
		t.Fatalf("ValidateForCreation(): %v", err)
	}
}

func TestProjectRejectsConflictedSingleChainDeployment(t *testing.T) {
	t.Parallel()

	project := projectFixture([]Deployment{
		deploymentFixture(1, "bsc", 56, VerificationConflicted),
	})
	if err := project.ValidateForCreation(); !errors.Is(err, ErrInvalidProject) {
		t.Fatalf("conflicted deployment error = %v, want ErrInvalidProject", err)
	}
}

func TestProjectRequiresAuthoritativeIdentityForMultipleChains(t *testing.T) {
	t.Parallel()

	project := projectFixture([]Deployment{
		deploymentFixture(1, "bsc", 56, VerificationVerified),
		deploymentFixture(2, "ethereum", 1, VerificationSingleChain),
	})
	if err := project.ValidateForCreation(); !errors.Is(err, ErrUnverifiedCrossChainAsset) {
		t.Fatalf(
			"ValidateForCreation() error = %v, want ErrUnverifiedCrossChainAsset",
			err,
		)
	}

	project.Deployments[1].Deployment.VerificationStatus = VerificationVerified
	project.Deployments[1].Deployment.VerificationSource = authoritativeIdentitySource
	project.IdentityEvidence = identityEvidenceFixture(project.Deployments)
	if err := project.ValidateForCreation(); err != nil {
		t.Fatalf("verified multi-chain project: %v", err)
	}
}

func TestProjectRejectsVerifiedFlagsWithoutAuthoritativeEvidence(t *testing.T) {
	t.Parallel()

	project := projectFixture([]Deployment{
		deploymentFixture(1, "bsc", 56, VerificationVerified),
		deploymentFixture(2, "ethereum", 1, VerificationVerified),
	})
	project.IdentityEvidence = nil
	if err := project.ValidateForCreation(); !errors.Is(
		err,
		ErrUnverifiedCrossChainAsset,
	) {
		t.Fatalf(
			"missing evidence error = %v, want ErrUnverifiedCrossChainAsset",
			err,
		)
	}

	project = projectFixture([]Deployment{
		deploymentFixture(1, "bsc", 56, VerificationVerified),
		deploymentFixture(2, "ethereum", 1, VerificationVerified),
	})
	project.Deployments[1].Deployment.VerificationSource = "dexscreener"
	if err := project.ValidateForCreation(); !errors.Is(
		err,
		ErrUnverifiedCrossChainAsset,
	) {
		t.Fatalf("non-authoritative deployment source error = %v", err)
	}
}

func TestProjectRejectsConflictingOrMismatchedIdentityEvidence(t *testing.T) {
	t.Parallel()

	project := projectFixture([]Deployment{
		deploymentFixture(1, "bsc", 56, VerificationVerified),
		deploymentFixture(2, "base", 8453, VerificationVerified),
	})
	project.IdentityEvidence[1].ExternalAssetID = "different-token"
	if err := project.ValidateForCreation(); !errors.Is(
		err,
		ErrUnverifiedCrossChainAsset,
	) {
		t.Fatalf("mismatched evidence error = %v", err)
	}

	project.IdentityEvidence = identityEvidenceFixture(project.Deployments)
	project.IdentityEvidence = append(project.IdentityEvidence, IdentityEvidence{
		MarketAssetID:   10,
		EvidenceKey:     "coingecko:conflict",
		Source:          authoritativeIdentitySource,
		EvidenceType:    canonicalAssetEvidenceType,
		ExternalAssetID: "different-token",
		Verdict:         EvidenceConflicts,
		Confidence:      "1",
	})
	if err := project.ValidateForCreation(); !errors.Is(
		err,
		ErrUnverifiedCrossChainAsset,
	) {
		t.Fatalf("conflicting evidence error = %v", err)
	}
}

func TestProjectRejectsDuplicateChain(t *testing.T) {
	t.Parallel()

	project := projectFixture([]Deployment{
		deploymentFixture(1, "bsc", 56, VerificationVerified),
		deploymentFixture(2, "bsc", 56, VerificationVerified),
	})
	project.Deployments[1].Deployment.TokenAddress =
		"0x2222222222222222222222222222222222222222"
	if err := project.ValidateForCreation(); !errors.Is(err, ErrInvalidProject) {
		t.Fatalf("duplicate chain error = %v, want ErrInvalidProject", err)
	}
}

func TestIdentityEvidenceValidatesExactDecimalConfidence(t *testing.T) {
	t.Parallel()

	valid := IdentityEvidence{
		MarketAssetID: 1,
		EvidenceKey:   "coingecko:pancakeswap-token",
		Source:        "coingecko",
		EvidenceType:  "canonical_asset_id",
		Verdict:       EvidenceSupports,
		Confidence:    "1.0000",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}
	valid.Confidence = "1.0001"
	if err := valid.Validate(); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("out-of-range confidence error = %v, want ErrInvalidEvidence", err)
	}
}

func TestRuleScopeValidation(t *testing.T) {
	t.Parallel()

	valid := RuleScope{
		DeploymentMode:       TargetSelected,
		PoolMode:             TargetSelected,
		CooldownScope:        CooldownPerChain,
		ProjectDeploymentIDs: []int64{10, 11},
		ProjectPoolIDs:       []int64{20, 21},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}
	valid.ProjectPoolIDs = []int64{20, 20}
	if err := valid.Validate(); !errors.Is(err, ErrInvalidRuleScope) {
		t.Fatalf("duplicate pool error = %v, want ErrInvalidRuleScope", err)
	}

	valid = RuleScope{
		DeploymentMode:       TargetAll,
		PoolMode:             TargetAll,
		CooldownScope:        CooldownProject,
		ProjectDeploymentIDs: []int64{10},
	}
	if err := valid.Validate(); !errors.Is(err, ErrInvalidRuleScope) {
		t.Fatalf("all-mode target error = %v, want ErrInvalidRuleScope", err)
	}
}

func projectFixture(deployments []Deployment) Project {
	project := Project{
		DeBoxUserID: "test-user",
		Asset: Asset{
			ID:                 10,
			IdentitySource:     "coingecko",
			CanonicalAssetID:   "pancakeswap-token",
			VerificationStatus: VerificationVerified,
		},
	}
	for _, deployment := range deployments {
		project.Deployments = append(project.Deployments, ProjectDeployment{
			MarketAssetID:           10,
			MarketAssetDeploymentID: deployment.ID,
			Status:                  DeploymentActive,
			Deployment:              deployment,
		})
	}
	project.IdentityEvidence = identityEvidenceFixture(project.Deployments)
	return project
}

func deploymentFixture(
	id int64,
	chainKey string,
	chainID int64,
	status VerificationStatus,
) Deployment {
	return Deployment{
		ID:                 id,
		MarketAssetID:      10,
		ChainKey:           chainKey,
		ChainID:            chainID,
		TokenAddress:       "0x1111111111111111111111111111111111111111",
		TokenName:          "Project Token",
		TokenSymbol:        "PT",
		TokenDecimals:      18,
		VerificationStatus: status,
		VerificationSource: authoritativeIdentitySource,
	}
}

func identityEvidenceFixture(
	deployments []ProjectDeployment,
) []IdentityEvidence {
	result := make([]IdentityEvidence, 0, len(deployments))
	for _, deployment := range deployments {
		deploymentID := deployment.MarketAssetDeploymentID
		result = append(result, IdentityEvidence{
			MarketAssetID:           10,
			MarketAssetDeploymentID: &deploymentID,
			EvidenceKey: "coingecko:pancakeswap-token:" +
				deployment.Deployment.ChainKey,
			Source:          authoritativeIdentitySource,
			EvidenceType:    canonicalAssetEvidenceType,
			ExternalAssetID: "pancakeswap-token",
			Verdict:         EvidenceSupports,
			Confidence:      "1.0000",
		})
	}
	return result
}
