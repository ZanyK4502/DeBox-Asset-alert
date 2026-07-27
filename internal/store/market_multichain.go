package store

import (
	"context"
	"fmt"
)

const marketAssetColumns = `
	id, canonical_name, symbol, logo_url, identity_source,
	canonical_asset_id, verification_status, metadata, created_at, updated_at
`

const marketAssetDeploymentColumns = `
	id, market_asset_id, chain_key, chain_id, token_address,
	token_name, token_symbol, token_decimals,
	total_supply_raw::text AS total_supply_raw,
	verification_status, verification_source, verification_evidence,
	default_market_pool_id, metadata, verified_at, created_at, updated_at
`

const marketAssetIdentityEvidenceColumns = `
	id, market_asset_id, market_asset_deployment_id, evidence_key,
	source, evidence_type, external_asset_id, verdict,
	confidence::text AS confidence, payload, observed_at, created_at
`

const marketProjectDeploymentColumns = `
	id, market_project_id, market_asset_id, market_asset_deployment_id,
	status, pause_reason, default_market_pool_id, metadata, created_at, updated_at
`

func (s *Store) GetMarketAsset(
	ctx context.Context,
	assetID int64,
) (*MarketAsset, error) {
	value, err := collectOptional[MarketAsset](ctx, s.db, `
		SELECT `+marketAssetColumns+`
		FROM market_assets
		WHERE id = $1
	`, assetID)
	if err != nil {
		return nil, fmt.Errorf("get market asset: %w", err)
	}
	return value, nil
}

func (s *Store) GetMarketAssetByIdentity(
	ctx context.Context,
	identitySource string,
	canonicalAssetID string,
) (*MarketAsset, error) {
	value, err := collectOptional[MarketAsset](ctx, s.db, `
		SELECT `+marketAssetColumns+`
		FROM market_assets
		WHERE identity_source = $1 AND canonical_asset_id = $2
	`, identitySource, canonicalAssetID)
	if err != nil {
		return nil, fmt.Errorf("get market asset by identity: %w", err)
	}
	return value, nil
}

func (s *Store) ListMarketAssetDeployments(
	ctx context.Context,
	assetID int64,
) ([]MarketAssetDeployment, error) {
	values, err := collectMany[MarketAssetDeployment](ctx, s.db, `
		SELECT `+marketAssetDeploymentColumns+`
		FROM market_asset_deployments
		WHERE market_asset_id = $1
		ORDER BY chain_id, id
	`, assetID)
	if err != nil {
		return nil, fmt.Errorf("list market asset deployments: %w", err)
	}
	return values, nil
}

func (s *Store) GetMarketAssetDeploymentByContract(
	ctx context.Context,
	chainID int64,
	tokenAddress string,
) (*MarketAssetDeployment, error) {
	address, err := normalizeMarketAddress(tokenAddress)
	if err != nil || chainID <= 0 {
		return nil, ErrInvalidMarketProject
	}
	value, err := collectOptional[MarketAssetDeployment](ctx, s.db, `
		SELECT `+marketAssetDeploymentColumns+`
		FROM market_asset_deployments
		WHERE chain_id = $1 AND token_address = $2
	`, chainID, address)
	if err != nil {
		return nil, fmt.Errorf("get market asset deployment by contract: %w", err)
	}
	return value, nil
}

func (s *Store) ListMarketProjectDeployments(
	ctx context.Context,
	projectID int64,
	deboxUserID string,
) ([]MarketProjectDeployment, error) {
	values, err := collectMany[MarketProjectDeployment](ctx, s.db, `
		SELECT
			mpd.id,
			mpd.market_project_id,
			mpd.market_asset_id,
			mpd.market_asset_deployment_id,
			mpd.status,
			mpd.pause_reason,
			mpd.default_market_pool_id,
			mpd.metadata,
			mpd.created_at,
			mpd.updated_at
		FROM market_project_deployments mpd
		JOIN market_projects mp ON mp.id = mpd.market_project_id
		WHERE mpd.market_project_id = $1 AND mp.debox_user_id = $2
		ORDER BY mpd.id
	`, projectID, deboxUserID)
	if err != nil {
		return nil, fmt.Errorf("list market project deployments: %w", err)
	}
	return values, nil
}

func (s *Store) ListMarketAssetIdentityEvidence(
	ctx context.Context,
	assetID int64,
) ([]MarketAssetIdentityEvidence, error) {
	values, err := collectMany[MarketAssetIdentityEvidence](ctx, s.db, `
		SELECT `+marketAssetIdentityEvidenceColumns+`
		FROM market_asset_identity_evidence
		WHERE market_asset_id = $1
		ORDER BY observed_at DESC, id DESC
	`, assetID)
	if err != nil {
		return nil, fmt.Errorf("list market asset identity evidence: %w", err)
	}
	return values, nil
}
