package marketdomain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
)

type VerificationStatus string

const (
	VerificationUnverified  VerificationStatus = "unverified"
	VerificationSingleChain VerificationStatus = "single_chain"
	VerificationVerified    VerificationStatus = "verified"
	VerificationConflicted  VerificationStatus = "conflicted"
	VerificationRejected    VerificationStatus = "rejected"
)

type EvidenceVerdict string

const (
	EvidenceSupports     EvidenceVerdict = "supports"
	EvidenceConflicts    EvidenceVerdict = "conflicts"
	EvidenceInconclusive EvidenceVerdict = "inconclusive"
)

type DeploymentStatus string

const (
	DeploymentActive  DeploymentStatus = "active"
	DeploymentPaused  DeploymentStatus = "paused"
	DeploymentRemoved DeploymentStatus = "removed"
)

type TargetMode string

const (
	TargetAll      TargetMode = "all"
	TargetSelected TargetMode = "selected"
)

type CooldownScope string

const (
	CooldownPerChain CooldownScope = "chain"
	CooldownProject  CooldownScope = "project"
)

var (
	ErrInvalidAsset              = errors.New("invalid market asset")
	ErrInvalidDeployment         = errors.New("invalid market asset deployment")
	ErrInvalidEvidence           = errors.New("invalid market identity evidence")
	ErrInvalidProject            = errors.New("invalid multi-chain market project")
	ErrInvalidRuleScope          = errors.New("invalid multi-chain market rule scope")
	ErrUnverifiedCrossChainAsset = errors.New("cross-chain deployments require verified identity")
)

// Asset is the chain-independent identity of one project token. A user project
// points to one Asset and may select one deployment from each supported chain.
type Asset struct {
	ID                 int64
	CanonicalName      string
	Symbol             string
	LogoURL            string
	IdentitySource     string
	CanonicalAssetID   string
	VerificationStatus VerificationStatus
	Metadata           json.RawMessage
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Deployment is one ERC-20 contract through which an Asset exists on one EVM
// chain. The same contract can belong to only one Asset.
type Deployment struct {
	ID                   int64
	MarketAssetID        int64
	ChainKey             string
	ChainID              int64
	TokenAddress         string
	TokenName            string
	TokenSymbol          string
	TokenDecimals        int32
	TotalSupplyRaw       *string
	VerificationStatus   VerificationStatus
	VerificationSource   string
	VerificationEvidence json.RawMessage
	DefaultMarketPoolID  *int64
	Metadata             json.RawMessage
	VerifiedAt           *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// IdentityEvidence records why contracts on different chains are considered
// the same asset. Names and symbols alone must never be supporting authority.
type IdentityEvidence struct {
	ID                      int64
	MarketAssetID           int64
	MarketAssetDeploymentID *int64
	EvidenceKey             string
	Source                  string
	EvidenceType            string
	ExternalAssetID         string
	Verdict                 EvidenceVerdict
	Confidence              string
	Payload                 json.RawMessage
	ObservedAt              time.Time
	CreatedAt               time.Time
}

// ProjectDeployment is a user's selection of one Asset deployment. Its
// default pool is user-project-specific and therefore is not inferred from a
// different user's choice.
type ProjectDeployment struct {
	ID                      int64
	MarketProjectID         int64
	MarketAssetID           int64
	MarketAssetDeploymentID int64
	Status                  DeploymentStatus
	PauseReason             string
	DefaultMarketPoolID     *int64
	Metadata                json.RawMessage
	Deployment              Deployment
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

type Project struct {
	ID          int64
	DeBoxUserID string
	Asset       Asset
	Deployments []ProjectDeployment
}

// RuleScope describes the deployment, pool, and cooldown boundaries of one
// market rule. Empty target lists are valid only when the corresponding mode
// is TargetAll.
type RuleScope struct {
	DeploymentMode       TargetMode
	PoolMode             TargetMode
	CooldownScope        CooldownScope
	ProjectDeploymentIDs []int64
	ProjectPoolIDs       []int64
}

func LegacyCanonicalAssetID(chainID int64, tokenAddress string) (string, error) {
	if chainID <= 0 {
		return "", fmt.Errorf("%w: chain id must be positive", ErrInvalidAsset)
	}
	address, err := chain.ValidateAddress(tokenAddress)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidAsset, err)
	}
	return fmt.Sprintf("eip155:%d/erc20:%s", chainID, address), nil
}

func (asset Asset) Validate() error {
	if strings.TrimSpace(asset.IdentitySource) == "" ||
		strings.TrimSpace(asset.CanonicalAssetID) == "" ||
		!asset.VerificationStatus.Valid() {
		return ErrInvalidAsset
	}
	return nil
}

func NormalizeDeployment(value Deployment) (Deployment, error) {
	profile, err := chain.ChainProfile(value.ChainKey, "")
	if err != nil || profile.ChainID != value.ChainID {
		return Deployment{}, fmt.Errorf(
			"%w: chain key and chain id do not match",
			ErrInvalidDeployment,
		)
	}
	address, err := chain.ValidateAddress(value.TokenAddress)
	if err != nil {
		return Deployment{}, fmt.Errorf("%w: %v", ErrInvalidDeployment, err)
	}
	if value.TokenDecimals < 0 || value.TokenDecimals > 255 ||
		!value.VerificationStatus.Valid() {
		return Deployment{}, ErrInvalidDeployment
	}
	if value.TotalSupplyRaw != nil {
		supply := new(big.Int)
		if _, ok := supply.SetString(strings.TrimSpace(*value.TotalSupplyRaw), 10); !ok ||
			supply.Sign() < 0 {
			return Deployment{}, fmt.Errorf(
				"%w: total supply must be a non-negative integer",
				ErrInvalidDeployment,
			)
		}
		normalized := supply.String()
		value.TotalSupplyRaw = &normalized
	}
	value.ChainKey = profile.Key
	value.TokenAddress = address
	value.TokenName = strings.TrimSpace(value.TokenName)
	value.TokenSymbol = strings.TrimSpace(value.TokenSymbol)
	value.VerificationSource = strings.TrimSpace(value.VerificationSource)
	return value, nil
}

func (evidence IdentityEvidence) Validate() error {
	if evidence.MarketAssetID <= 0 ||
		strings.TrimSpace(evidence.EvidenceKey) == "" ||
		strings.TrimSpace(evidence.Source) == "" ||
		strings.TrimSpace(evidence.EvidenceType) == "" ||
		!evidence.Verdict.Valid() {
		return ErrInvalidEvidence
	}
	confidence, ok := new(big.Rat).SetString(strings.TrimSpace(evidence.Confidence))
	if !ok || confidence.Sign() < 0 || confidence.Cmp(big.NewRat(1, 1)) > 0 {
		return fmt.Errorf("%w: confidence must be between zero and one", ErrInvalidEvidence)
	}
	return nil
}

// ValidateForCreation enforces the frozen identity decision: a single-chain
// project may use a contract verified on that chain, but two or more contracts
// can be merged only when every deployment has authoritative verified status.
func (project Project) ValidateForCreation() error {
	if strings.TrimSpace(project.DeBoxUserID) == "" ||
		len(project.Deployments) == 0 ||
		project.Asset.Validate() != nil {
		return ErrInvalidProject
	}
	if len(project.Deployments) > 1 &&
		project.Asset.VerificationStatus != VerificationVerified {
		return ErrUnverifiedCrossChainAsset
	}
	seenChains := make(map[int64]struct{}, len(project.Deployments))
	seenContracts := make(map[string]struct{}, len(project.Deployments))
	for _, selected := range project.Deployments {
		deployment, err := NormalizeDeployment(selected.Deployment)
		if err != nil ||
			selected.Status != DeploymentActive ||
			selected.MarketAssetDeploymentID <= 0 ||
			(project.Asset.ID > 0 && selected.MarketAssetID != project.Asset.ID) ||
			(deployment.MarketAssetID > 0 &&
				selected.MarketAssetID != deployment.MarketAssetID) {
			return ErrInvalidProject
		}
		if _, exists := seenChains[deployment.ChainID]; exists {
			return fmt.Errorf("%w: duplicate chain deployment", ErrInvalidProject)
		}
		contractKey := fmt.Sprintf("%d:%s", deployment.ChainID, deployment.TokenAddress)
		if _, exists := seenContracts[contractKey]; exists {
			return fmt.Errorf("%w: duplicate contract deployment", ErrInvalidProject)
		}
		seenChains[deployment.ChainID] = struct{}{}
		seenContracts[contractKey] = struct{}{}
		if deployment.VerificationStatus != VerificationSingleChain &&
			deployment.VerificationStatus != VerificationVerified {
			return ErrInvalidProject
		}
		if len(project.Deployments) > 1 &&
			deployment.VerificationStatus != VerificationVerified {
			return ErrUnverifiedCrossChainAsset
		}
	}
	return nil
}

func (scope RuleScope) Validate() error {
	if !scope.DeploymentMode.Valid() ||
		!scope.PoolMode.Valid() ||
		!scope.CooldownScope.Valid() ||
		(scope.DeploymentMode == TargetSelected &&
			len(scope.ProjectDeploymentIDs) == 0) ||
		(scope.DeploymentMode == TargetAll &&
			len(scope.ProjectDeploymentIDs) != 0) ||
		(scope.PoolMode == TargetSelected && len(scope.ProjectPoolIDs) == 0) ||
		(scope.PoolMode == TargetAll && len(scope.ProjectPoolIDs) != 0) ||
		hasInvalidOrDuplicateID(scope.ProjectDeploymentIDs) ||
		hasInvalidOrDuplicateID(scope.ProjectPoolIDs) {
		return ErrInvalidRuleScope
	}
	return nil
}

func (status VerificationStatus) Valid() bool {
	switch status {
	case VerificationUnverified,
		VerificationSingleChain,
		VerificationVerified,
		VerificationConflicted,
		VerificationRejected:
		return true
	default:
		return false
	}
}

func (verdict EvidenceVerdict) Valid() bool {
	switch verdict {
	case EvidenceSupports, EvidenceConflicts, EvidenceInconclusive:
		return true
	default:
		return false
	}
}

func (mode TargetMode) Valid() bool {
	return mode == TargetAll || mode == TargetSelected
}

func (scope CooldownScope) Valid() bool {
	return scope == CooldownPerChain || scope == CooldownProject
}

func hasInvalidOrDuplicateID(values []int64) bool {
	seen := make(map[int64]struct{}, len(values))
	for _, value := range values {
		if value <= 0 {
			return true
		}
		if _, exists := seen[value]; exists {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}
