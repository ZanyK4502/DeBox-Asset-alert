package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
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

type CreateMultiChainMarketProjectParams struct {
	DeBoxUserID        string
	CanonicalName      string
	Symbol             string
	LogoURL            string
	IdentitySource     string
	CanonicalAssetID   string
	VerificationStatus string
	Metadata           json.RawMessage
	Deployments        []CreateMultiChainMarketDeploymentParams
}

type CreateMultiChainMarketDeploymentParams struct {
	ChainKey             string
	ChainID              int64
	TokenAddress         string
	TokenName            string
	TokenSymbol          string
	TokenDecimals        int32
	TotalSupplyRaw       *string
	VerificationStatus   string
	VerificationSource   string
	VerificationEvidence json.RawMessage
	VerifiedAt           *time.Time
	Metadata             json.RawMessage
	Evidence             *CreateMarketAssetEvidenceParams
	Pools                []CreateMultiChainMarketPoolParams
}

type CreateMarketAssetEvidenceParams struct {
	EvidenceKey     string
	Source          string
	EvidenceType    string
	ExternalAssetID string
	Verdict         string
	Confidence      string
	Payload         json.RawMessage
	ObservedAt      time.Time
}

type CreateMultiChainMarketPoolParams struct {
	MarketPoolID int64
	Selected     bool
	IsPrimary    bool
}

func (s *Store) CreateMultiChainMarketProjectWithinQuota(
	ctx context.Context,
	params CreateMultiChainMarketProjectParams,
	policy QuotaPolicy,
) (MarketProject, error) {
	if err := validateMultiChainMarketProjectParams(&params); err != nil {
		return MarketProject{}, err
	}
	return withTxValue(ctx, s.db, func(tx DBTX) (MarketProject, error) {
		if err := lockUser(ctx, tx, params.DeBoxUserID); err != nil {
			return MarketProject{}, err
		}
		if err := requirePolicyPlan(ctx, tx, params.DeBoxUserID, policy); err != nil {
			return MarketProject{}, err
		}
		if policy.MarketProjectLimit <= 0 {
			return MarketProject{}, ErrMarketMonitoringDenied
		}
		asset, err := upsertMarketAsset(ctx, tx, params)
		if err != nil {
			return MarketProject{}, err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM market_projects
				WHERE debox_user_id = $1 AND market_asset_id = $2
			)
		`, params.DeBoxUserID, asset.ID).Scan(&exists); err != nil {
			return MarketProject{}, fmt.Errorf("check existing multi-chain market project: %w", err)
		}
		if exists {
			return MarketProject{}, ErrMarketProjectExists
		}
		count, err := queryCount(ctx, tx, `
			SELECT COUNT(*)
			FROM market_projects
			WHERE debox_user_id = $1 AND status <> 'archived'
		`, params.DeBoxUserID)
		if err != nil {
			return MarketProject{}, fmt.Errorf("count market projects: %w", err)
		}
		if count >= int64(policy.MarketProjectLimit) {
			return MarketProject{}, ErrMarketProjectLimitReached
		}

		primaryDeployment := params.Deployments[0]
		project, err := collectOne[MarketProject](ctx, tx, `
			INSERT INTO market_projects (
				debox_user_id, market_asset_id,
				chain_key, chain_id, token_address,
				token_name, token_symbol, token_decimals, total_supply_raw,
				four_meme_status, metadata
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'unknown', $10)
			RETURNING `+marketProjectColumns,
			params.DeBoxUserID,
			asset.ID,
			primaryDeployment.ChainKey,
			primaryDeployment.ChainID,
			primaryDeployment.TokenAddress,
			primaryDeployment.TokenName,
			primaryDeployment.TokenSymbol,
			primaryDeployment.TokenDecimals,
			primaryDeployment.TotalSupplyRaw,
			normalizedJSON(params.Metadata),
		)
		if err != nil {
			return MarketProject{}, fmt.Errorf("create multi-chain market project: %w", err)
		}

		var compatibilityMainPoolID *int64
		for _, deploymentParams := range params.Deployments {
			deployment, err := upsertMarketAssetDeployment(
				ctx, tx, asset.ID, deploymentParams,
			)
			if err != nil {
				return MarketProject{}, err
			}
			projectDeployment, err := collectOne[MarketProjectDeployment](ctx, tx, `
				INSERT INTO market_project_deployments (
					market_project_id, market_asset_id, market_asset_deployment_id,
					status, metadata
				)
				VALUES ($1, $2, $3, 'active', $4)
				RETURNING `+marketProjectDeploymentColumns,
				project.ID,
				asset.ID,
				deployment.ID,
				normalizedJSON(deploymentParams.Metadata),
			)
			if err != nil {
				return MarketProject{}, fmt.Errorf(
					"create market project deployment: %w", err,
				)
			}
			if err := createMultiChainProjectPools(
				ctx,
				tx,
				project,
				projectDeployment,
				deployment,
				deploymentParams.Pools,
				policy.MultiPoolMonitoring,
				&compatibilityMainPoolID,
			); err != nil {
				return MarketProject{}, err
			}
			if deploymentParams.Evidence != nil {
				if err := upsertMarketAssetEvidence(
					ctx,
					tx,
					asset.ID,
					deployment.ID,
					*deploymentParams.Evidence,
				); err != nil {
					return MarketProject{}, err
				}
			}
		}
		if compatibilityMainPoolID != nil {
			project, err = collectOne[MarketProject](ctx, tx, `
				UPDATE market_projects
				SET main_pool_id = $1, updated_at = NOW()
				WHERE id = $2
				RETURNING `+marketProjectColumns,
				*compatibilityMainPoolID,
				project.ID,
			)
			if err != nil {
				return MarketProject{}, fmt.Errorf(
					"set compatibility main market pool: %w", err,
				)
			}
		}
		return project, nil
	})
}

func validateMultiChainMarketProjectParams(
	params *CreateMultiChainMarketProjectParams,
) error {
	params.DeBoxUserID = strings.TrimSpace(params.DeBoxUserID)
	params.CanonicalName = strings.TrimSpace(params.CanonicalName)
	params.Symbol = strings.ToUpper(strings.TrimSpace(params.Symbol))
	params.LogoURL = strings.TrimSpace(params.LogoURL)
	params.IdentitySource = strings.ToLower(strings.TrimSpace(params.IdentitySource))
	params.CanonicalAssetID = strings.ToLower(strings.TrimSpace(params.CanonicalAssetID))
	params.VerificationStatus = strings.ToLower(strings.TrimSpace(params.VerificationStatus))
	if params.DeBoxUserID == "" || params.IdentitySource == "" ||
		params.CanonicalAssetID == "" || len(params.Deployments) == 0 ||
		len(params.Deployments) > 6 {
		return ErrInvalidMarketProject
	}
	switch params.VerificationStatus {
	case "verified", "single_chain":
	default:
		return ErrInvalidMarketProject
	}
	seenChains := make(map[int64]struct{}, len(params.Deployments))
	for index := range params.Deployments {
		deployment := &params.Deployments[index]
		deployment.ChainKey = strings.ToLower(strings.TrimSpace(deployment.ChainKey))
		address, err := normalizeMarketAddress(deployment.TokenAddress)
		if err != nil || deployment.ChainKey == "" || deployment.ChainID <= 0 ||
			deployment.TokenDecimals < 0 || deployment.TokenDecimals > 255 {
			return ErrInvalidMarketProject
		}
		if _, exists := seenChains[deployment.ChainID]; exists {
			return ErrInvalidMarketProject
		}
		seenChains[deployment.ChainID] = struct{}{}
		deployment.TokenAddress = address
		deployment.TokenName = strings.TrimSpace(deployment.TokenName)
		deployment.TokenSymbol = strings.ToUpper(strings.TrimSpace(deployment.TokenSymbol))
		deployment.VerificationStatus = strings.ToLower(
			strings.TrimSpace(deployment.VerificationStatus),
		)
		deployment.VerificationSource = strings.ToLower(
			strings.TrimSpace(deployment.VerificationSource),
		)
		if len(deployment.Pools) == 0 {
			return ErrInvalidMarketProject
		}
		seenPools := make(map[int64]struct{}, len(deployment.Pools))
		selectedCount := 0
		primaryCount := 0
		for poolIndex := range deployment.Pools {
			pool := &deployment.Pools[poolIndex]
			if pool.MarketPoolID <= 0 {
				return ErrInvalidMarketProject
			}
			if _, exists := seenPools[pool.MarketPoolID]; exists {
				return ErrInvalidMarketProject
			}
			seenPools[pool.MarketPoolID] = struct{}{}
			if pool.IsPrimary {
				pool.Selected = true
				primaryCount++
			}
			if pool.Selected {
				selectedCount++
			}
		}
		if selectedCount == 0 || primaryCount != 1 {
			return ErrInvalidMarketProject
		}
	}
	return nil
}

func upsertMarketAsset(
	ctx context.Context,
	db DBTX,
	params CreateMultiChainMarketProjectParams,
) (MarketAsset, error) {
	var target *MarketAsset
	canonical, err := collectOptional[MarketAsset](ctx, db, `
		SELECT `+marketAssetColumns+`
		FROM market_assets
		WHERE identity_source = $1 AND canonical_asset_id = $2
		FOR UPDATE
	`, params.IdentitySource, params.CanonicalAssetID)
	if err != nil {
		return MarketAsset{}, fmt.Errorf("lock canonical market asset: %w", err)
	}
	target = canonical
	for _, deployment := range params.Deployments {
		existing, lookupErr := collectOptional[MarketAsset](ctx, db, `
			SELECT ma.*
			FROM market_asset_deployments mad
			JOIN market_assets ma ON ma.id = mad.market_asset_id
			WHERE mad.chain_id = $1 AND mad.token_address = $2
			FOR UPDATE OF ma
		`, deployment.ChainID, deployment.TokenAddress)
		if lookupErr != nil {
			return MarketAsset{}, fmt.Errorf(
				"lock market asset by deployment: %w", lookupErr,
			)
		}
		if existing == nil {
			continue
		}
		if target != nil && target.ID != existing.ID {
			if mergeErr := mergeMarketAssets(ctx, db, target.ID, existing.ID); mergeErr != nil {
				return MarketAsset{}, mergeErr
			}
			continue
		}
		target = existing
	}
	if target != nil {
		asset, updateErr := collectOne[MarketAsset](ctx, db, `
			UPDATE market_assets
			SET canonical_name = $1,
			    symbol = $2,
			    logo_url = CASE WHEN $3 <> '' THEN $3 ELSE logo_url END,
			    identity_source = $4,
			    canonical_asset_id = $5,
			    verification_status = $6,
			    metadata = $7,
			    updated_at = NOW()
			WHERE id = $8
			RETURNING `+marketAssetColumns,
			params.CanonicalName,
			params.Symbol,
			params.LogoURL,
			params.IdentitySource,
			params.CanonicalAssetID,
			params.VerificationStatus,
			normalizedJSON(params.Metadata),
			target.ID,
		)
		if updateErr != nil {
			if isUniqueViolation(updateErr) {
				return MarketAsset{}, ErrInvalidMarketProject
			}
			return MarketAsset{}, fmt.Errorf("update market asset identity: %w", updateErr)
		}
		return asset, nil
	}
	asset, err := collectOne[MarketAsset](ctx, db, `
		INSERT INTO market_assets (
			canonical_name, symbol, logo_url, identity_source,
			canonical_asset_id, verification_status, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (identity_source, canonical_asset_id) DO UPDATE
		SET canonical_name = EXCLUDED.canonical_name,
		    symbol = EXCLUDED.symbol,
		    logo_url = CASE
		        WHEN EXCLUDED.logo_url <> '' THEN EXCLUDED.logo_url
		        ELSE market_assets.logo_url
		    END,
		    verification_status = EXCLUDED.verification_status,
		    metadata = EXCLUDED.metadata,
		    updated_at = NOW()
		RETURNING `+marketAssetColumns,
		params.CanonicalName,
		params.Symbol,
		params.LogoURL,
		params.IdentitySource,
		params.CanonicalAssetID,
		params.VerificationStatus,
		normalizedJSON(params.Metadata),
	)
	if err != nil {
		return MarketAsset{}, fmt.Errorf("upsert market asset: %w", err)
	}
	return asset, nil
}

func mergeMarketAssets(
	ctx context.Context,
	db DBTX,
	targetAssetID int64,
	sourceAssetID int64,
) error {
	if targetAssetID <= 0 || sourceAssetID <= 0 || targetAssetID == sourceAssetID {
		return nil
	}
	var overlappingChain bool
	if err := db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM market_asset_deployments target
			JOIN market_asset_deployments source
			  ON source.chain_id = target.chain_id
			WHERE target.market_asset_id = $1
			  AND source.market_asset_id = $2
		)
	`, targetAssetID, sourceAssetID).Scan(&overlappingChain); err != nil {
		return fmt.Errorf("check market asset merge deployments: %w", err)
	}
	if overlappingChain {
		return ErrInvalidMarketProject
	}
	if _, err := db.Exec(ctx, `
		WITH moved_projects AS (
			UPDATE market_projects
			SET market_asset_id = $1, updated_at = NOW()
			WHERE market_asset_id = $2
			RETURNING id
		),
		moved_deployments AS (
			UPDATE market_asset_deployments
			SET market_asset_id = $1, updated_at = NOW()
			WHERE market_asset_id = $2
			RETURNING id
		)
		UPDATE market_project_deployments
		SET market_asset_id = $1, updated_at = NOW()
		WHERE market_asset_id = $2
		  AND (
			market_project_id IN (SELECT id FROM moved_projects)
			OR market_asset_deployment_id IN (SELECT id FROM moved_deployments)
		  )
	`, targetAssetID, sourceAssetID); err != nil {
		return fmt.Errorf("merge market asset hierarchy: %w", err)
	}
	if _, err := db.Exec(ctx, `
		DELETE FROM market_asset_identity_evidence source
		USING market_asset_identity_evidence target
		WHERE source.market_asset_id = $2
		  AND target.market_asset_id = $1
		  AND target.evidence_key = source.evidence_key
	`, targetAssetID, sourceAssetID); err != nil {
		return fmt.Errorf("deduplicate merged market asset evidence: %w", err)
	}
	if _, err := db.Exec(ctx, `
		UPDATE market_asset_identity_evidence
		SET market_asset_id = $1
		WHERE market_asset_id = $2
	`, targetAssetID, sourceAssetID); err != nil {
		return fmt.Errorf("merge market asset evidence: %w", err)
	}
	if _, err := db.Exec(ctx, `
		DELETE FROM market_assets
		WHERE id = $1
	`, sourceAssetID); err != nil {
		return fmt.Errorf("remove merged market asset: %w", err)
	}
	return nil
}

func upsertMarketAssetDeployment(
	ctx context.Context,
	db DBTX,
	assetID int64,
	params CreateMultiChainMarketDeploymentParams,
) (MarketAssetDeployment, error) {
	deployment, err := collectOne[MarketAssetDeployment](ctx, db, `
		INSERT INTO market_asset_deployments (
			market_asset_id, chain_key, chain_id, token_address,
			token_name, token_symbol, token_decimals, total_supply_raw,
			verification_status, verification_source, verification_evidence,
			metadata, verified_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (market_asset_id, chain_id) DO UPDATE
		SET token_address = EXCLUDED.token_address,
		    token_name = EXCLUDED.token_name,
		    token_symbol = EXCLUDED.token_symbol,
		    token_decimals = EXCLUDED.token_decimals,
		    total_supply_raw = EXCLUDED.total_supply_raw,
		    verification_status = EXCLUDED.verification_status,
		    verification_source = EXCLUDED.verification_source,
		    verification_evidence = EXCLUDED.verification_evidence,
		    metadata = EXCLUDED.metadata,
		    verified_at = EXCLUDED.verified_at,
		    updated_at = NOW()
		RETURNING `+marketAssetDeploymentColumns,
		assetID,
		params.ChainKey,
		params.ChainID,
		params.TokenAddress,
		params.TokenName,
		params.TokenSymbol,
		params.TokenDecimals,
		params.TotalSupplyRaw,
		params.VerificationStatus,
		params.VerificationSource,
		normalizedJSON(params.VerificationEvidence),
		normalizedJSON(params.Metadata),
		params.VerifiedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return MarketAssetDeployment{}, ErrInvalidMarketProject
		}
		return MarketAssetDeployment{}, fmt.Errorf(
			"upsert market asset deployment: %w", err,
		)
	}
	return deployment, nil
}

func createMultiChainProjectPools(
	ctx context.Context,
	db DBTX,
	project MarketProject,
	projectDeployment MarketProjectDeployment,
	deployment MarketAssetDeployment,
	pools []CreateMultiChainMarketPoolParams,
	multiPoolAllowed bool,
	compatibilityMainPoolID **int64,
) error {
	selectedCount := 0
	for _, poolParams := range pools {
		var poolChainID int64
		var token0Address, token1Address string
		if err := db.QueryRow(ctx, `
			SELECT chain_id, token0_address, token1_address
			FROM market_pools
			WHERE id = $1
		`, poolParams.MarketPoolID).Scan(
			&poolChainID, &token0Address, &token1Address,
		); err != nil {
			if isNoRows(err) {
				return ErrMarketPoolMismatch
			}
			return fmt.Errorf("validate multi-chain market pool: %w", err)
		}
		if poolChainID != deployment.ChainID ||
			(deployment.TokenAddress != token0Address &&
				deployment.TokenAddress != token1Address) {
			return ErrMarketPoolMismatch
		}
		selected := poolParams.Selected || poolParams.IsPrimary
		if selected {
			selectedCount++
			if !multiPoolAllowed && selectedCount > 1 {
				return ErrMarketPoolMismatch
			}
		}
		if _, err := collectOne[MarketProjectPool](ctx, db, `
			INSERT INTO market_project_pools (
				market_project_id, market_project_deployment_id,
				market_pool_id, selected, is_primary, discovery_source
			)
			VALUES ($1, $2, $3, $4, $5, 'dexscreener')
			RETURNING `+marketProjectPoolColumns,
			project.ID,
			projectDeployment.ID,
			poolParams.MarketPoolID,
			boolInt(selected),
			boolInt(poolParams.IsPrimary),
		); err != nil {
			return fmt.Errorf("link multi-chain market pool: %w", err)
		}
		if poolParams.IsPrimary {
			if _, err := db.Exec(ctx, `
				UPDATE market_project_deployments
				SET default_market_pool_id = $1, updated_at = NOW()
				WHERE id = $2
			`, poolParams.MarketPoolID, projectDeployment.ID); err != nil {
				return fmt.Errorf("set project deployment primary pool: %w", err)
			}
			if project.ChainID == deployment.ChainID && *compatibilityMainPoolID == nil {
				value := poolParams.MarketPoolID
				*compatibilityMainPoolID = &value
			}
		}
	}
	return nil
}

func upsertMarketAssetEvidence(
	ctx context.Context,
	db DBTX,
	assetID int64,
	deploymentID int64,
	params CreateMarketAssetEvidenceParams,
) error {
	observedAt := params.ObservedAt.UTC()
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO market_asset_identity_evidence (
			market_asset_id, market_asset_deployment_id, evidence_key,
			source, evidence_type, external_asset_id, verdict,
			confidence, payload, observed_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (market_asset_id, evidence_key) DO UPDATE
		SET market_asset_deployment_id = EXCLUDED.market_asset_deployment_id,
		    source = EXCLUDED.source,
		    evidence_type = EXCLUDED.evidence_type,
		    external_asset_id = EXCLUDED.external_asset_id,
		    verdict = EXCLUDED.verdict,
		    confidence = EXCLUDED.confidence,
		    payload = EXCLUDED.payload,
		    observed_at = EXCLUDED.observed_at
	`, assetID, deploymentID, strings.TrimSpace(params.EvidenceKey),
		strings.TrimSpace(params.Source), strings.TrimSpace(params.EvidenceType),
		strings.TrimSpace(params.ExternalAssetID), strings.TrimSpace(params.Verdict),
		strings.TrimSpace(params.Confidence), normalizedJSON(params.Payload), observedAt,
	); err != nil {
		return fmt.Errorf("upsert market asset identity evidence: %w", err)
	}
	return nil
}

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

func (s *Store) GetMarketProjectAsset(
	ctx context.Context,
	projectID int64,
	deboxUserID string,
) (*MarketAsset, error) {
	value, err := collectOptional[MarketAsset](ctx, s.db, `
		SELECT
			ma.id, ma.canonical_name, ma.symbol, ma.logo_url, ma.identity_source,
			ma.canonical_asset_id, ma.verification_status, ma.metadata,
			ma.created_at, ma.updated_at
		FROM market_projects mp
		JOIN market_assets ma ON ma.id = mp.market_asset_id
		WHERE mp.id = $1 AND mp.debox_user_id = $2
	`, projectID, strings.TrimSpace(deboxUserID))
	if err != nil {
		return nil, fmt.Errorf("get market project asset: %w", err)
	}
	return value, nil
}

func (s *Store) ListMarketProjectDeploymentViews(
	ctx context.Context,
	projectID int64,
	deboxUserID string,
) ([]MarketProjectDeploymentView, error) {
	values, err := collectMany[MarketProjectDeploymentView](ctx, s.db, `
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
			mpd.updated_at,
			mad.chain_key,
			mad.chain_id,
			mad.token_address,
			mad.token_name,
			mad.token_symbol,
			mad.token_decimals,
			mad.verification_status,
			mad.verification_source,
			mad.verification_evidence,
			mad.verified_at
		FROM market_project_deployments mpd
		JOIN market_projects mp ON mp.id = mpd.market_project_id
		JOIN market_asset_deployments mad
		  ON mad.id = mpd.market_asset_deployment_id
		WHERE mpd.market_project_id = $1 AND mp.debox_user_id = $2
		ORDER BY mad.chain_id, mpd.id
	`, projectID, strings.TrimSpace(deboxUserID))
	if err != nil {
		return nil, fmt.Errorf("list market project deployment views: %w", err)
	}
	return values, nil
}

func (s *Store) ListLatestMarketProjectSnapshots(
	ctx context.Context,
	projectID int64,
	deboxUserID string,
) ([]MarketSnapshot, error) {
	values, err := collectMany[MarketSnapshot](ctx, s.db, `
		SELECT DISTINCT ON (mpd.id)
			ms.id, ms.market_asset_deployment_id,
			ms.chain_key, ms.chain_id, ms.token_address, ms.market_pool_id,
			ms.price_usd::text AS price_usd,
			ms.liquidity_usd::text AS liquidity_usd,
			ms.fdv_usd::text AS fdv_usd,
			ms.market_cap_usd::text AS market_cap_usd,
			ms.volume_5m_usd::text AS volume_5m_usd,
			ms.volume_15m_usd::text AS volume_15m_usd,
			ms.volume_1h_usd::text AS volume_1h_usd,
			ms.volume_6h_usd::text AS volume_6h_usd,
			ms.volume_24h_usd::text AS volume_24h_usd,
			ms.buys_5m, ms.sells_5m, ms.buys_1h, ms.sells_1h,
			ms.buys_24h, ms.sells_24h,
			ms.source, ms.source_timestamp, ms.captured_at, ms.raw_payload
		FROM market_project_deployments mpd
		JOIN market_projects mp ON mp.id = mpd.market_project_id
		JOIN market_asset_deployments mad
		  ON mad.id = mpd.market_asset_deployment_id
		JOIN market_project_pools mpp
		  ON mpp.market_project_id = mpd.market_project_id
		 AND (
			mpp.market_project_deployment_id = mpd.id
			OR (
				mpp.market_project_deployment_id IS NULL
				AND EXISTS (
					SELECT 1
					FROM market_pools legacy_pool
					WHERE legacy_pool.id = mpp.market_pool_id
					  AND legacy_pool.chain_id = mad.chain_id
					  AND (
						legacy_pool.token0_address = mad.token_address
						OR legacy_pool.token1_address = mad.token_address
					  )
				)
			)
		 )
		 AND mpp.selected = 1
		 AND (mp.frozen_at IS NULL OR mpp.created_at <= mp.frozen_at)
		JOIN market_snapshots ms
		  ON ms.market_pool_id = mpp.market_pool_id
		 AND ms.chain_id = mad.chain_id
		 AND ms.token_address = mad.token_address
		WHERE mpd.market_project_id = $1
		  AND mp.debox_user_id = $2
		  AND mpd.status <> 'removed'
		  AND (mp.frozen_at IS NULL OR ms.captured_at <= mp.frozen_at)
		ORDER BY
			mpd.id,
			(mpp.market_pool_id = mpd.default_market_pool_id) DESC,
			mpp.is_primary DESC,
			ms.captured_at DESC,
			ms.id DESC
	`, projectID, strings.TrimSpace(deboxUserID))
	if err != nil {
		return nil, fmt.Errorf("list latest market project snapshots: %w", err)
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
