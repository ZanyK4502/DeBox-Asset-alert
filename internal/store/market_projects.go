package store

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
)

var marketAddressPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

const marketProjectColumns = `
	id, debox_user_id, market_asset_id, chain_key, chain_id, token_address,
	token_name, token_symbol, token_decimals,
	total_supply_raw::text AS total_supply_raw,
	status, pause_reason, four_meme_status, main_pool_id,
	metadata, last_discovered_at, frozen_at, created_at, updated_at
`

const marketPoolColumns = `
	id, chain_key, chain_id, protocol, protocol_version, pool_key,
	pool_address, factory_address, factory_verified,
	token0_address, token0_symbol, token0_decimals,
	token1_address, token1_symbol, token1_decimals,
	liquidity_usd::text AS liquidity_usd,
	supports_event_parsing, parser_adapter, verification_status,
	metadata, COALESCE(metadata->'quote_quality','{}'::jsonb) AS quote_quality,
	first_seen_at, last_seen_at, created_at, updated_at
`

const marketProjectPoolColumns = `
	id, market_project_id, market_project_deployment_id,
	market_pool_id, selected, is_primary,
	discovery_source, created_at, updated_at
`

const marketSnapshotColumns = `
	id, market_asset_deployment_id,
	chain_key, chain_id, token_address, market_pool_id,
	price_usd::text AS price_usd,
	liquidity_usd::text AS liquidity_usd,
	fdv_usd::text AS fdv_usd,
	market_cap_usd::text AS market_cap_usd,
	volume_5m_usd::text AS volume_5m_usd,
	volume_15m_usd::text AS volume_15m_usd,
	volume_1h_usd::text AS volume_1h_usd,
	volume_6h_usd::text AS volume_6h_usd,
	volume_24h_usd::text AS volume_24h_usd,
	buys_5m, sells_5m, buys_1h, sells_1h, buys_24h, sells_24h,
	source, source_timestamp, quote_quality, captured_at, raw_payload
`

type CreateMarketProjectParams struct {
	DeBoxUserID    string
	ChainKey       string
	ChainID        int64
	TokenAddress   string
	TokenName      string
	TokenSymbol    string
	TokenDecimals  int32
	TotalSupplyRaw *string
	FourMemeStatus string
	Metadata       json.RawMessage
}

type UpsertMarketPoolParams struct {
	ChainKey               string
	ChainID                int64
	Protocol               string
	ProtocolVersion        string
	PoolKey                string
	PoolAddress            *string
	FactoryAddress         *string
	FactoryVerified        bool
	Token0Address          string
	Token0Symbol           string
	Token0Decimals         int32
	Token0DecimalsVerified bool
	Token1Address          string
	Token1Symbol           string
	Token1Decimals         int32
	Token1DecimalsVerified bool
	LiquidityUSD           string
	SupportsEventParsing   bool
	ParserAdapter          string
	VerificationStatus     string
	Metadata               json.RawMessage
	SeenAt                 time.Time
}

type LinkMarketProjectPoolParams struct {
	DeBoxUserID     string
	MarketProjectID int64
	MarketPoolID    int64
	Selected        bool
	IsPrimary       bool
	DiscoverySource string
}

type CreateMarketSnapshotParams struct {
	ChainKey        string
	ChainID         int64
	TokenAddress    string
	MarketPoolID    int64
	PriceUSD        *string
	LiquidityUSD    *string
	FDVUSD          *string
	MarketCapUSD    *string
	Volume5mUSD     *string
	Volume15mUSD    *string
	Volume1hUSD     *string
	Volume6hUSD     *string
	Volume24hUSD    *string
	Buys5m          *int64
	Sells5m         *int64
	Buys1h          *int64
	Sells1h         *int64
	Buys24h         *int64
	Sells24h        *int64
	Source          string
	SourceTimestamp *time.Time
	QuoteQuality    marketquality.Quote
	CapturedAt      time.Time
	RawPayload      json.RawMessage
}

func (s *Store) CreateMarketProjectWithinQuota(
	ctx context.Context,
	params CreateMarketProjectParams,
	policy QuotaPolicy,
) (MarketProject, error) {
	if err := validateMarketProjectParams(&params); err != nil {
		return MarketProject{}, err
	}
	return withTxValue(ctx, s.db, func(tx DBTX) (MarketProject, error) {
		ctx, cancel, err := s.beginResourceCapacityCheck(ctx, tx, params.DeBoxUserID)
		if err != nil {
			return MarketProject{}, err
		}
		defer cancel()
		if err := lockUser(ctx, tx, params.DeBoxUserID); err != nil {
			return MarketProject{}, err
		}
		if err := requirePolicyPlan(ctx, tx, params.DeBoxUserID, policy); err != nil {
			return MarketProject{}, err
		}
		policy, err = s.marketScopePolicy(ctx, tx, params.DeBoxUserID, policy)
		if err != nil {
			return MarketProject{}, err
		}
		if policy.MarketProjectLimit <= 0 {
			return MarketProject{}, ErrMarketMonitoringDenied
		}
		if err := validateMarketScopeChain(policy, params.ChainKey, params.ChainID); err != nil {
			return MarketProject{}, err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM market_projects
				WHERE debox_user_id = $1
				  AND chain_id = $2
				  AND token_address = $3
			)
		`, params.DeBoxUserID, params.ChainID, params.TokenAddress).Scan(&exists); err != nil {
			return MarketProject{}, fmt.Errorf("check existing market project: %w", err)
		}
		if exists {
			return MarketProject{}, ErrMarketProjectExists
		}
		count, err := queryCount(ctx, tx, `
			SELECT COUNT(*)
			FROM market_projects
			WHERE debox_user_id = $1 AND status = 'active'
		`, params.DeBoxUserID)
		if err != nil {
			return MarketProject{}, fmt.Errorf("count market projects: %w", err)
		}
		if count >= int64(policy.MarketProjectLimit) {
			return MarketProject{}, ErrMarketProjectLimitReached
		}
		project, err := createMarketProject(ctx, tx, params)
		if isUniqueViolation(err) {
			return MarketProject{}, ErrMarketProjectExists
		}
		if err != nil {
			return MarketProject{}, err
		}
		if err := validateMarketProjectScope(ctx, tx, project.ID, params.DeBoxUserID, policy); err != nil {
			return MarketProject{}, err
		}
		if err := s.checkResourceCapacity(ctx, tx); err != nil {
			return MarketProject{}, err
		}
		return project, nil
	})
}

func createMarketProject(
	ctx context.Context,
	db DBTX,
	params CreateMarketProjectParams,
) (MarketProject, error) {
	project, err := collectOne[MarketProject](ctx, db, `
		INSERT INTO market_projects (
			debox_user_id, chain_key, chain_id, token_address,
			token_name, token_symbol, token_decimals, total_supply_raw,
			four_meme_status, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING `+marketProjectColumns,
		params.DeBoxUserID,
		params.ChainKey,
		params.ChainID,
		params.TokenAddress,
		params.TokenName,
		params.TokenSymbol,
		params.TokenDecimals,
		params.TotalSupplyRaw,
		params.FourMemeStatus,
		normalizedJSON(params.Metadata),
	)
	if err != nil {
		return MarketProject{}, fmt.Errorf("create market project: %w", err)
	}
	return project, nil
}

func (s *Store) GetMarketProject(
	ctx context.Context,
	projectID int64,
	deboxUserID string,
) (*MarketProject, error) {
	project, err := collectOptional[MarketProject](ctx, s.db, `
		SELECT `+marketProjectColumns+`
		FROM market_projects
		WHERE id = $1 AND debox_user_id = $2
	`, projectID, deboxUserID)
	if err != nil {
		return nil, fmt.Errorf("get market project: %w", err)
	}
	return project, nil
}

func (s *Store) ListMarketProjects(
	ctx context.Context,
	deboxUserID string,
	includeArchived bool,
) ([]MarketProject, error) {
	query := `
		SELECT listed.*,
		       COALESCE(asset.identity_source, '') AS identity_source,
		       COALESCE(asset.canonical_asset_id, '') AS canonical_asset_id
		FROM (
			SELECT ` + marketProjectColumns + `
			FROM market_projects
			WHERE debox_user_id = $1
	`
	if !includeArchived {
		query += " AND status <> 'archived'"
	}
	query += `
		) listed
		LEFT JOIN market_assets asset ON asset.id = listed.market_asset_id
		ORDER BY listed.created_at DESC, listed.id DESC
	`
	projects, err := collectMany[MarketProject](ctx, s.db, query, deboxUserID)
	if err != nil {
		return nil, fmt.Errorf("list market projects: %w", err)
	}
	return projects, nil
}

func (s *Store) CountMarketProjects(ctx context.Context, deboxUserID string) (int64, error) {
	return queryCount(ctx, s.db, `
		SELECT COUNT(*)
		FROM market_projects
		WHERE debox_user_id = $1 AND status = 'active'
	`, deboxUserID)
}

func (s *Store) RestoreMarketProjectWithinQuota(
	ctx context.Context,
	projectID int64,
	deboxUserID string,
	policy QuotaPolicy,
) (MarketProject, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (MarketProject, error) {
		ctx, cancel, err := s.beginResourceCapacityCheck(ctx, tx, deboxUserID)
		if err != nil {
			return MarketProject{}, err
		}
		defer cancel()
		if err := lockUser(ctx, tx, deboxUserID); err != nil {
			return MarketProject{}, err
		}
		if err := requirePolicyPlan(ctx, tx, deboxUserID, policy); err != nil {
			return MarketProject{}, err
		}
		policy, err = s.marketScopePolicy(ctx, tx, deboxUserID, policy)
		if err != nil {
			return MarketProject{}, err
		}
		if policy.MarketProjectLimit <= 0 {
			return MarketProject{}, ErrMarketMonitoringDenied
		}
		project, err := collectOne[MarketProject](ctx, tx, `
			SELECT `+marketProjectColumns+`
			FROM market_projects
			WHERE id = $1 AND debox_user_id = $2
			FOR UPDATE
		`, projectID, deboxUserID)
		if isNoRows(err) {
			return MarketProject{}, ErrNotFound
		}
		if err != nil {
			return MarketProject{}, fmt.Errorf("lock market project: %w", err)
		}
		if err := validateMarketProjectScope(ctx, tx, projectID, deboxUserID, policy); err != nil {
			return MarketProject{}, err
		}
		if project.Status == "active" {
			return project, nil
		}
		if project.PauseReason == "subscription_expired" || project.FrozenAt != nil {
			return MarketProject{}, ErrInvalidMarketStatus
		}
		count, err := queryCount(ctx, tx, `
			SELECT COUNT(*)
			FROM market_projects
			WHERE debox_user_id = $1 AND status = 'active' AND id <> $2
		`, deboxUserID, projectID)
		if err != nil {
			return MarketProject{}, fmt.Errorf("count active market projects: %w", err)
		}
		if count >= int64(policy.MarketProjectLimit) {
			return MarketProject{}, ErrMarketProjectLimitReached
		}
		holderTargets, err := s.beginProjectHolderTargetCheck(ctx, tx, projectID)
		if err != nil {
			return MarketProject{}, err
		}
		restored, err := collectOne[MarketProject](ctx, tx, `
			UPDATE market_projects
			SET status = 'active', pause_reason = '', frozen_at = NULL, updated_at = NOW()
			WHERE id = $1 AND debox_user_id = $2
			  AND NOT (
				status = 'paused' AND pause_reason = 'subscription_expired'
			  )
			RETURNING `+marketProjectColumns,
			projectID,
			deboxUserID,
		)
		if err != nil {
			return MarketProject{}, fmt.Errorf("restore market project: %w", err)
		}
		if err := s.checkMarketHolderTargetLimit(ctx, tx, holderTargets); err != nil {
			return MarketProject{}, err
		}
		if err := s.checkResourceCapacity(ctx, tx); err != nil {
			return MarketProject{}, err
		}
		return restored, nil
	})
}

func (s *Store) UpdateMarketProjectDiscovery(
	ctx context.Context,
	projectID int64,
	deboxUserID string,
	tokenName string,
	tokenSymbol string,
	tokenDecimals int32,
	totalSupplyRaw *string,
	fourMemeStatus string,
	metadata json.RawMessage,
) (MarketProject, error) {
	fourMemeStatus = normalizeFourMemeStatus(fourMemeStatus)
	project, err := collectOne[MarketProject](ctx, s.db, `
		UPDATE market_projects
		SET token_name = $1,
		    token_symbol = $2,
		    token_decimals = $3,
		    total_supply_raw = $4,
		    four_meme_status = $5,
		    metadata = $6,
		    last_discovered_at = NOW(),
		    updated_at = NOW()
		WHERE id = $7 AND debox_user_id = $8
		RETURNING `+marketProjectColumns,
		tokenName,
		tokenSymbol,
		tokenDecimals,
		totalSupplyRaw,
		fourMemeStatus,
		normalizedJSON(metadata),
		projectID,
		deboxUserID,
	)
	if isNoRows(err) {
		return MarketProject{}, ErrNotFound
	}
	if err != nil {
		return MarketProject{}, fmt.Errorf("update market project discovery: %w", err)
	}
	return project, nil
}

func (s *Store) SetMarketProjectStatus(
	ctx context.Context,
	projectID int64,
	deboxUserID string,
	status string,
	pauseReason string,
) (MarketProject, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if status != "active" && status != "paused" && status != "archived" {
		return MarketProject{}, ErrInvalidMarketStatus
	}
	update := func(ctx context.Context, db DBTX) (MarketProject, error) {
		project, err := collectOne[MarketProject](ctx, db, `
		UPDATE market_projects
		SET status = $1, pause_reason = $2, updated_at = NOW()
		WHERE id = $3 AND debox_user_id = $4
		RETURNING `+marketProjectColumns,
			status,
			pauseReason,
			projectID,
			deboxUserID,
		)
		if isNoRows(err) {
			return MarketProject{}, ErrNotFound
		}
		if err != nil {
			return MarketProject{}, fmt.Errorf("set market project status: %w", err)
		}
		return project, nil
	}
	if status == "active" {
		return withTxValue(ctx, s.db, func(tx DBTX) (MarketProject, error) {
			ctx, cancel, err := s.beginResourceCapacityCheck(ctx, tx, deboxUserID)
			if err != nil {
				return MarketProject{}, err
			}
			defer cancel()
			if err := lockUser(ctx, tx, deboxUserID); err != nil {
				return MarketProject{}, err
			}
			policy, err := s.marketScopePolicy(ctx, tx, deboxUserID, QuotaPolicy{})
			if err != nil {
				return MarketProject{}, err
			}
			if err := validateMarketProjectScope(ctx, tx, projectID, deboxUserID, policy); err != nil {
				return MarketProject{}, err
			}
			if s.marketScopePolicies != nil {
				count, err := queryCount(ctx, tx, `SELECT COUNT(*) FROM market_projects WHERE debox_user_id=$1 AND status='active' AND id<>$2`, deboxUserID, projectID)
				if err != nil {
					return MarketProject{}, err
				}
				if count >= int64(policy.MarketProjectLimit) {
					return MarketProject{}, ErrMarketProjectLimitReached
				}
			}
			holderTargets, err := s.beginProjectHolderTargetCheck(ctx, tx, projectID)
			if err != nil {
				return MarketProject{}, err
			}
			project, err := update(ctx, tx)
			if err != nil {
				return MarketProject{}, err
			}
			if err := s.checkMarketHolderTargetLimit(ctx, tx, holderTargets); err != nil {
				return MarketProject{}, err
			}
			if err := s.checkResourceCapacity(ctx, tx); err != nil {
				return MarketProject{}, err
			}
			return project, nil
		})
	}
	// Stopping or archiving frees capacity and needs no admission.
	return update(ctx, s.db)
}

func (s *Store) ArchiveMarketProject(
	ctx context.Context,
	projectID int64,
	deboxUserID string,
) (MarketProject, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (MarketProject, error) {
		project, err := collectOne[MarketProject](ctx, tx, `
			UPDATE market_projects
			SET status = 'archived',
			    pause_reason = 'user_archived',
			    updated_at = NOW()
			WHERE id = $1 AND debox_user_id = $2
			  AND NOT (
				status = 'paused' AND pause_reason = 'subscription_expired'
			  )
			RETURNING `+marketProjectColumns,
			projectID,
			deboxUserID,
		)
		if isNoRows(err) {
			return MarketProject{}, ErrNotFound
		}
		if err != nil {
			return MarketProject{}, fmt.Errorf("archive market project: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE market_rules
			SET run_status = 'paused',
			    pause_reason = 'project_archived',
			    updated_at = NOW()
			WHERE market_project_id = $1
			  AND debox_user_id = $2
			  AND enabled = 1
			  AND run_status = 'active'
		`, projectID, deboxUserID); err != nil {
			return MarketProject{}, fmt.Errorf("pause archived project rules: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE market_combination_rules mcr
			SET run_status = 'paused',
			    pause_reason = 'project_archived',
			    updated_at = NOW()
			WHERE mcr.debox_user_id = $2
			  AND mcr.enabled = 1
			  AND mcr.run_status = 'active'
			  AND EXISTS (
			    SELECT 1
			    FROM market_combination_members mcm
			    JOIN market_rules mr ON mr.id = mcm.market_rule_id
			    WHERE mcm.market_combination_rule_id = mcr.id
			      AND mr.market_project_id = $1
			  )
		`, projectID, deboxUserID); err != nil {
			return MarketProject{}, fmt.Errorf(
				"pause combinations for archived market project: %w",
				err,
			)
		}
		return project, nil
	})
}

func (s *Store) DeleteArchivedMarketProject(
	ctx context.Context,
	projectID int64,
	deboxUserID string,
) error {
	_, err := withTxValue(ctx, s.db, func(tx DBTX) (bool, error) {
		project, err := collectOne[MarketProject](ctx, tx, `
			SELECT `+marketProjectColumns+`
			FROM market_projects
			WHERE id = $1 AND debox_user_id = $2
			FOR UPDATE
		`, projectID, deboxUserID)
		if isNoRows(err) {
			return false, ErrNotFound
		}
		if err != nil {
			return false, fmt.Errorf("lock archived market project for deletion: %w", err)
		}
		deletable := project.Status == "archived" ||
			(project.Status == "paused" && project.PauseReason == "subscription_expired")
		if !deletable {
			return false, ErrMarketProjectNotArchived
		}

		if _, err := tx.Exec(ctx, `
			DELETE FROM market_combination_rules
			WHERE debox_user_id = $2
			  AND id IN (
				SELECT market_combination_rule_id
				FROM market_combination_rule_projects
				WHERE market_project_id = $1
			  )
		`, projectID, deboxUserID); err != nil {
			return false, fmt.Errorf("delete archived market project combinations: %w", err)
		}
		command, err := tx.Exec(ctx, `
			DELETE FROM market_projects
			WHERE id = $1 AND debox_user_id = $2
			  AND (
				status = 'archived'
				OR (status = 'paused' AND pause_reason = 'subscription_expired')
			  )
		`, projectID, deboxUserID)
		if err != nil {
			return false, fmt.Errorf("delete archived market project: %w", err)
		}
		if command.RowsAffected() != 1 {
			return false, ErrNotFound
		}
		return true, nil
	})
	return err
}

// UpdateMarketFourMemeFromEvent derives state from a stored protocol event. A
// caller cannot choose a phase or substitute an ingest timestamp for chain order.
func (s *Store) UpdateMarketFourMemeFromEvent(ctx context.Context, eventID int64) (int64, error) {
	return withTxValue(ctx, s.db, func(db DBTX) (int64, error) {
		return updateMarketFourMemeFromEvent(ctx, db, eventID)
	})
}

func updateMarketFourMemeFromEvent(ctx context.Context, db DBTX, eventID int64) (int64, error) {
	if eventID <= 0 {
		return 0, ErrInvalidMarketEvent
	}
	value, err := collectOptional[struct {
		ChainID      int64  `db:"chain_id"`
		TokenAddress string `db:"token_address"`
	}](ctx, db, `
		SELECT chain_id, token_address FROM market_events
		WHERE id = $1 AND chain_id = 56 AND reorged = 0
		  AND metadata->>'adapter' IN ('four_meme_v1', 'four_meme_v2')
		  AND event_type IN ('token_created', 'buy', 'sell', 'trading_stopped', 'migrated')
		  AND block_number IS NOT NULL AND block_hash IS NOT NULL
		  AND transaction_index IS NOT NULL AND log_index IS NOT NULL
	`, eventID)
	if err != nil {
		return 0, fmt.Errorf("get Four.meme state source: %w", err)
	}
	if value == nil {
		return 0, nil
	}
	return reconcileMarketFourMemeState(ctx, db, value.ChainID, value.TokenAddress)
}

func (s *Store) ReconcileMarketFourMemeState(ctx context.Context, chainID int64, tokenAddress string) (int64, error) {
	return withTxValue(ctx, s.db, func(db DBTX) (int64, error) {
		return reconcileMarketFourMemeState(ctx, db, chainID, tokenAddress)
	})
}

// This helper also runs inside the receipt/reorg transaction. Callers handling
// several tokens acquire these per-token locks in sorted token-address order.
func reconcileMarketFourMemeState(ctx context.Context, db DBTX, chainID int64, tokenAddress string) (int64, error) {
	tokenAddress, err := normalizeMarketAddress(tokenAddress)
	if err != nil || chainID <= 0 {
		return 0, ErrInvalidMarketProject
	}
	if chainID != 56 {
		return 0, nil
	}
	if _, err := db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		fmt.Sprintf("market-four-meme:%d:%s", chainID, tokenAddress)); err != nil {
		return 0, fmt.Errorf("lock Four.meme state: %w", err)
	}
	latest, err := collectOptional[struct {
		ID               int64     `db:"id"`
		BlockHash        string    `db:"block_hash"`
		BlockNumber      int64     `db:"block_number"`
		TransactionIndex int32     `db:"transaction_index"`
		LogIndex         int32     `db:"log_index"`
		OccurredAt       time.Time `db:"occurred_at"`
		Status           string    `db:"status"`
	}](ctx, db, `
		SELECT id, block_hash, block_number, transaction_index, log_index, occurred_at,
		       CASE event_type WHEN 'trading_stopped' THEN 'graduating'
		         WHEN 'migrated' THEN 'migrated' ELSE 'bonding' END AS status
		FROM market_events
		WHERE chain_id = $1 AND token_address = $2 AND reorged = 0
		  AND metadata->>'adapter' IN ('four_meme_v1', 'four_meme_v2')
		  AND event_type IN ('token_created', 'buy', 'sell', 'trading_stopped', 'migrated')
		  AND block_number IS NOT NULL AND block_hash IS NOT NULL
		  AND transaction_index IS NOT NULL AND log_index IS NOT NULL
		ORDER BY block_number DESC, transaction_index DESC, log_index DESC, id DESC
		LIMIT 1
	`, chainID, tokenAddress)
	if err != nil {
		return 0, fmt.Errorf("get latest effective Four.meme event: %w", err)
	}
	if latest == nil {
		tag, err := db.Exec(ctx, `DELETE FROM market_four_meme_states WHERE chain_id = $1 AND token_address = $2`, chainID, tokenAddress)
		if err != nil {
			return 0, fmt.Errorf("retire Four.meme state: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return 0, nil
		}
		tag, err = db.Exec(ctx, `
			UPDATE market_projects SET four_meme_status = 'unknown', updated_at = NOW()
			WHERE chain_id = $1 AND token_address = $2 AND status <> 'archived'
			  AND four_meme_status IS DISTINCT FROM 'unknown'
		`, chainID, tokenAddress)
		if err != nil {
			return 0, fmt.Errorf("reset reorged Four.meme project state: %w", err)
		}
		return tag.RowsAffected(), nil
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO market_four_meme_states (
			chain_id, token_address, source_event_id, source_block_hash,
			source_block_number, source_transaction_index, source_log_index, status, occurred_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (chain_id, token_address) DO UPDATE
		SET source_event_id = EXCLUDED.source_event_id,
		    source_block_hash = EXCLUDED.source_block_hash,
		    source_block_number = EXCLUDED.source_block_number,
		    source_transaction_index = EXCLUDED.source_transaction_index,
		    source_log_index = EXCLUDED.source_log_index,
		    status = EXCLUDED.status, occurred_at = EXCLUDED.occurred_at, updated_at = NOW()
		WHERE (market_four_meme_states.source_event_id, market_four_meme_states.source_block_hash,
		       market_four_meme_states.source_block_number, market_four_meme_states.source_transaction_index,
		       market_four_meme_states.source_log_index, market_four_meme_states.status)
		  IS DISTINCT FROM (EXCLUDED.source_event_id, EXCLUDED.source_block_hash,
		       EXCLUDED.source_block_number, EXCLUDED.source_transaction_index,
		       EXCLUDED.source_log_index, EXCLUDED.status)
	`, chainID, tokenAddress, latest.ID, latest.BlockHash, latest.BlockNumber,
		latest.TransactionIndex, latest.LogIndex, latest.Status, latest.OccurredAt); err != nil {
		return 0, fmt.Errorf("advance Four.meme state source: %w", err)
	}
	tag, err := db.Exec(ctx, `
		UPDATE market_projects SET four_meme_status = $1, updated_at = NOW()
		WHERE chain_id = $2 AND token_address = $3 AND status <> 'archived'
		  AND four_meme_status IS DISTINCT FROM $1
	`, latest.Status, chainID, tokenAddress)
	if err != nil {
		return 0, fmt.Errorf("update ordered Four.meme project state: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *Store) UpsertMarketPool(
	ctx context.Context,
	params UpsertMarketPoolParams,
) (MarketPool, error) {
	if err := validateMarketPoolParams(&params); err != nil {
		return MarketPool{}, err
	}
	seenAt := params.SeenAt.UTC()
	if seenAt.IsZero() {
		seenAt = time.Now().UTC()
	}
	pool, err := collectOne[MarketPool](ctx, s.db, `
		INSERT INTO market_pools (
			chain_key, chain_id, protocol, protocol_version, pool_key,
			pool_address, factory_address, factory_verified,
			token0_address, token0_symbol, token0_decimals, token0_decimals_verified,
			token1_address, token1_symbol, token1_decimals, token1_decimals_verified,
			liquidity_usd, supports_event_parsing, parser_adapter,
			verification_status, metadata, first_seen_at, last_seen_at
		)
		SELECT
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $21,
			$12, $13, $14, $22, $15, $16, $17, $18, $19, $20, $20
		WHERE ($21 AND $22) OR EXISTS (
			SELECT 1 FROM market_pools
			WHERE chain_id = $2 AND protocol = $3 AND pool_key = $5
		)
		ON CONFLICT (chain_id, protocol, pool_key) DO UPDATE
		SET chain_key = EXCLUDED.chain_key,
		    protocol_version = EXCLUDED.protocol_version,
		    pool_address = COALESCE(EXCLUDED.pool_address, market_pools.pool_address),
		    factory_address = COALESCE(EXCLUDED.factory_address, market_pools.factory_address),
		    factory_verified = EXCLUDED.factory_verified,
		    token0_address = EXCLUDED.token0_address,
		    token0_symbol = EXCLUDED.token0_symbol,
		    token0_decimals = CASE WHEN EXCLUDED.token0_decimals_verified
		      THEN EXCLUDED.token0_decimals ELSE market_pools.token0_decimals END,
		    token0_decimals_verified = market_pools.token0_decimals_verified OR EXCLUDED.token0_decimals_verified,
		    token1_address = EXCLUDED.token1_address,
		    token1_symbol = EXCLUDED.token1_symbol,
		    token1_decimals = CASE WHEN EXCLUDED.token1_decimals_verified
		      THEN EXCLUDED.token1_decimals ELSE market_pools.token1_decimals END,
		    token1_decimals_verified = market_pools.token1_decimals_verified OR EXCLUDED.token1_decimals_verified,
		    liquidity_usd = EXCLUDED.liquidity_usd,
		    supports_event_parsing = CASE WHEN EXCLUDED.supports_event_parsing = 1
		      AND (market_pools.token0_decimals_verified OR EXCLUDED.token0_decimals_verified)
		      AND (market_pools.token1_decimals_verified OR EXCLUDED.token1_decimals_verified)
		      THEN 1 ELSE 0 END,
		    parser_adapter = EXCLUDED.parser_adapter,
		    verification_status = EXCLUDED.verification_status,
		    metadata = EXCLUDED.metadata,
		    last_seen_at = EXCLUDED.last_seen_at,
		    updated_at = NOW()
		WHERE (market_pools.token0_address = EXCLUDED.token0_address
		       AND market_pools.token1_address = EXCLUDED.token1_address)
		   OR (EXCLUDED.token0_decimals_verified AND EXCLUDED.token1_decimals_verified)
		RETURNING `+marketPoolColumns,
		params.ChainKey,
		params.ChainID,
		params.Protocol,
		params.ProtocolVersion,
		params.PoolKey,
		params.PoolAddress,
		params.FactoryAddress,
		boolInt(params.FactoryVerified),
		params.Token0Address,
		params.Token0Symbol,
		params.Token0Decimals,
		params.Token1Address,
		params.Token1Symbol,
		params.Token1Decimals,
		params.LiquidityUSD,
		boolInt(params.SupportsEventParsing),
		params.ParserAdapter,
		params.VerificationStatus,
		normalizedJSON(params.Metadata),
		seenAt,
		params.Token0DecimalsVerified,
		params.Token1DecimalsVerified,
	)
	if err != nil {
		if isNoRows(err) {
			return MarketPool{}, ErrInvalidMarketPool
		}
		return MarketPool{}, fmt.Errorf("upsert market pool: %w", err)
	}
	return pool, nil
}

func (s *Store) GetMarketPool(ctx context.Context, poolID int64) (*MarketPool, error) {
	pool, err := collectOptional[MarketPool](ctx, s.db, `
		SELECT `+marketPoolColumns+`
		FROM market_pools
		WHERE id = $1
	`, poolID)
	if err != nil {
		return nil, fmt.Errorf("get market pool: %w", err)
	}
	return pool, nil
}

func (s *Store) LinkMarketProjectPool(
	ctx context.Context,
	params LinkMarketProjectPoolParams,
) (MarketProjectPool, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (MarketProjectPool, error) {
		checkedCtx := ctx
		var policy QuotaPolicy
		activating := params.Selected || params.IsPrimary
		if activating {
			var cancel context.CancelFunc
			var err error
			checkedCtx, cancel, err = s.beginResourceCapacityCheck(ctx, tx, params.DeBoxUserID)
			if err != nil {
				return MarketProjectPool{}, err
			}
			defer cancel()
			if s.resourceCapacity != nil || s.marketScopePolicies != nil {
				if err := lockUser(checkedCtx, tx, params.DeBoxUserID); err != nil {
					return MarketProjectPool{}, err
				}
			}
		}
		if activating {
			var err error
			policy, err = s.marketScopePolicy(checkedCtx, tx, params.DeBoxUserID, policy)
			if err != nil {
				return MarketProjectPool{}, err
			}
		}
		link, err := linkMarketProjectPool(checkedCtx, tx, params, policy.MarketPoolLimit == 1)
		if err != nil {
			return MarketProjectPool{}, err
		}
		if activating {
			if err := validateMarketProjectScope(checkedCtx, tx, params.MarketProjectID, params.DeBoxUserID, policy); err != nil {
				return MarketProjectPool{}, err
			}
			if err := s.checkResourceCapacity(checkedCtx, tx); err != nil {
				return MarketProjectPool{}, err
			}
		}
		return link, nil
	})
}

func (s *Store) LinkMarketProjectPoolWithinQuota(
	ctx context.Context,
	params LinkMarketProjectPoolParams,
	policy QuotaPolicy,
) (MarketProjectPool, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (MarketProjectPool, error) {
		checkedCtx := ctx
		activating := params.Selected || params.IsPrimary
		if activating {
			var cancel context.CancelFunc
			var err error
			checkedCtx, cancel, err = s.beginResourceCapacityCheck(ctx, tx, params.DeBoxUserID)
			if err != nil {
				return MarketProjectPool{}, err
			}
			defer cancel()
		}
		if err := lockUser(checkedCtx, tx, params.DeBoxUserID); err != nil {
			return MarketProjectPool{}, err
		}
		if err := requirePolicyPlan(checkedCtx, tx, params.DeBoxUserID, policy); err != nil {
			return MarketProjectPool{}, err
		}
		var scopeErr error
		policy, scopeErr = s.marketScopePolicy(checkedCtx, tx, params.DeBoxUserID, policy)
		if scopeErr != nil {
			return MarketProjectPool{}, scopeErr
		}
		if policy.MarketProjectLimit <= 0 {
			return MarketProjectPool{}, ErrMarketMonitoringDenied
		}
		link, err := linkMarketProjectPool(checkedCtx, tx, params, !policy.MultiPoolMonitoring || policy.MarketPoolLimit == 1)
		if err != nil {
			return MarketProjectPool{}, err
		}
		if activating {
			if err := validateMarketProjectScope(checkedCtx, tx, params.MarketProjectID, params.DeBoxUserID, policy); err != nil {
				return MarketProjectPool{}, err
			}
			if err := s.checkResourceCapacity(checkedCtx, tx); err != nil {
				return MarketProjectPool{}, err
			}
		}
		return link, nil
	})
}

func linkMarketProjectPool(
	ctx context.Context,
	db DBTX,
	params LinkMarketProjectPoolParams,
	singleSelectedPool bool,
) (MarketProjectPool, error) {
	var projectChainID, poolChainID int64
	var projectDeploymentID *int64
	var tokenAddress, token0Address, token1Address string
	if err := db.QueryRow(ctx, `
			SELECT
				mp.chain_id,
				mp.token_address,
				p.chain_id,
				p.token0_address,
				p.token1_address,
				mpd.id
			FROM market_projects mp
			JOIN market_pools p ON TRUE
			LEFT JOIN market_project_deployments mpd
			  ON mpd.market_project_id = mp.id
			 AND mpd.status <> 'removed'
			 AND EXISTS (
				SELECT 1
				FROM market_asset_deployments mad
				WHERE mad.id = mpd.market_asset_deployment_id
				  AND mad.chain_id = p.chain_id
				  AND (
					mad.token_address = p.token0_address
					OR mad.token_address = p.token1_address
				  )
			 )
			WHERE mp.id = $1 AND mp.debox_user_id = $2
			  AND mp.status = 'active' AND p.id = $3
			FOR UPDATE OF mp
		`, params.MarketProjectID, params.DeBoxUserID, params.MarketPoolID).Scan(
		&projectChainID,
		&tokenAddress,
		&poolChainID,
		&token0Address,
		&token1Address,
		&projectDeploymentID,
	); err != nil {
		if isNoRows(err) {
			return MarketProjectPool{}, ErrNotFound
		}
		return MarketProjectPool{}, fmt.Errorf("validate market project pool: %w", err)
	}
	if projectDeploymentID == nil &&
		(projectChainID != poolChainID ||
			(tokenAddress != token0Address && tokenAddress != token1Address)) {
		return MarketProjectPool{}, ErrMarketPoolMismatch
	}
	// Snapshot the previous effective selection before this transactional edit.
	if err := recordMarketProjectTradeScope(ctx, db, params.MarketProjectID); err != nil {
		return MarketProjectPool{}, err
	}
	selected := params.Selected || params.IsPrimary
	if selected && singleSelectedPool {
		params.IsPrimary = true
		if _, err := db.Exec(ctx, `
			UPDATE market_project_pools
			SET selected = 0, is_primary = 0, updated_at = NOW()
			WHERE market_project_id = $1
			  AND market_project_deployment_id IS NOT DISTINCT FROM $2
			  AND market_pool_id <> $3
		`, params.MarketProjectID, projectDeploymentID, params.MarketPoolID); err != nil {
			return MarketProjectPool{}, fmt.Errorf("clear standard plan market pools: %w", err)
		}
	}
	if params.IsPrimary {
		if _, err := db.Exec(ctx, `
				UPDATE market_project_pools
				SET is_primary = 0, updated_at = NOW()
				WHERE market_project_id = $1
				  AND market_project_deployment_id IS NOT DISTINCT FROM $2
				  AND is_primary = 1
			`, params.MarketProjectID, projectDeploymentID); err != nil {
			return MarketProjectPool{}, fmt.Errorf("clear primary market pool: %w", err)
		}
	}
	link, err := collectOne[MarketProjectPool](ctx, db, `
			INSERT INTO market_project_pools (
				market_project_id, market_project_deployment_id,
				market_pool_id, selected, is_primary, discovery_source
			)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (market_project_id, market_pool_id) DO UPDATE
			SET market_project_deployment_id = EXCLUDED.market_project_deployment_id,
			    selected = EXCLUDED.selected,
			    is_primary = EXCLUDED.is_primary,
			    discovery_source = EXCLUDED.discovery_source,
			    updated_at = NOW()
			RETURNING `+marketProjectPoolColumns,
		params.MarketProjectID,
		projectDeploymentID,
		params.MarketPoolID,
		boolInt(selected),
		boolInt(params.IsPrimary),
		params.DiscoverySource,
	)
	if err != nil {
		return MarketProjectPool{}, fmt.Errorf("link market project pool: %w", err)
	}
	if params.IsPrimary {
		if projectDeploymentID != nil {
			if _, err := db.Exec(ctx, `
				UPDATE market_project_deployments
				SET default_market_pool_id = $1, updated_at = NOW()
				WHERE id = $2 AND market_project_id = $3
			`, params.MarketPoolID, projectDeploymentID, params.MarketProjectID); err != nil {
				return MarketProjectPool{}, fmt.Errorf("set deployment primary market pool: %w", err)
			}
		}
		if projectChainID == poolChainID &&
			(tokenAddress == token0Address || tokenAddress == token1Address) {
			if _, err := db.Exec(ctx, `
				UPDATE market_projects
				SET main_pool_id = $1, updated_at = NOW()
				WHERE id = $2 AND debox_user_id = $3
			`, params.MarketPoolID, params.MarketProjectID, params.DeBoxUserID); err != nil {
				return MarketProjectPool{}, fmt.Errorf("set market project main pool: %w", err)
			}
		}
	} else if !selected {
		if projectDeploymentID != nil {
			if _, err := db.Exec(ctx, `
				UPDATE market_project_deployments
				SET default_market_pool_id = NULL, updated_at = NOW()
				WHERE id = $1 AND market_project_id = $2 AND default_market_pool_id = $3
			`, projectDeploymentID, params.MarketProjectID, params.MarketPoolID); err != nil {
				return MarketProjectPool{}, fmt.Errorf("clear deployment primary market pool: %w", err)
			}
		}
		if _, err := db.Exec(ctx, `
				UPDATE market_projects
				SET main_pool_id = NULL, updated_at = NOW()
				WHERE id = $1 AND debox_user_id = $2 AND main_pool_id = $3
			`, params.MarketProjectID, params.DeBoxUserID, params.MarketPoolID); err != nil {
			return MarketProjectPool{}, fmt.Errorf("clear market project main pool: %w", err)
		}
	}
	if err := recordMarketProjectTradeScope(ctx, db, params.MarketProjectID); err != nil {
		return MarketProjectPool{}, err
	}
	return link, nil
}

func (s *Store) ListMarketProjectPools(
	ctx context.Context,
	projectID int64,
	deboxUserID string,
) ([]MarketPool, error) {
	pools, err := collectMany[MarketPool](ctx, s.db, `
		SELECT
			p.id, p.chain_key, p.chain_id, p.protocol, p.protocol_version, p.pool_key,
			p.pool_address, p.factory_address, p.factory_verified,
			p.token0_address, p.token0_symbol, p.token0_decimals,
			p.token1_address, p.token1_symbol, p.token1_decimals,
			CASE WHEN mp.frozen_at IS NOT NULL THEN COALESCE(frozen_snapshot.liquidity_usd::text,'')
				ELSE p.liquidity_usd::text END AS liquidity_usd,
			CASE WHEN mp.frozen_at IS NOT NULL THEN COALESCE(frozen_snapshot.quote_quality,'{}'::jsonb)
				ELSE COALESCE(p.metadata->'quote_quality','{}'::jsonb) END AS quote_quality,
			p.supports_event_parsing, p.parser_adapter, p.verification_status,
			p.metadata, p.first_seen_at, p.last_seen_at, p.created_at, p.updated_at
		FROM market_project_pools mpp
		JOIN market_projects mp ON mp.id = mpp.market_project_id
		JOIN market_pools p ON p.id = mpp.market_pool_id
		LEFT JOIN LATERAL (
			SELECT ms.liquidity_usd, ms.quote_quality
			FROM market_snapshots ms
			WHERE mp.frozen_at IS NOT NULL
			  AND ms.market_pool_id = p.id
			  AND ms.captured_at <= mp.frozen_at
			ORDER BY ms.captured_at DESC, ms.id DESC
			LIMIT 1
		) frozen_snapshot ON TRUE
		WHERE mpp.market_project_id = $1 AND mp.debox_user_id = $2
		  AND (mp.frozen_at IS NULL OR mpp.created_at <= mp.frozen_at)
		ORDER BY mpp.is_primary DESC, mpp.selected DESC, p.liquidity_usd DESC, p.id
	`, projectID, deboxUserID)
	if err != nil {
		return nil, fmt.Errorf("list market project pools: %w", err)
	}
	return pools, nil
}

func (s *Store) ListMarketProjectPoolViews(
	ctx context.Context,
	projectID int64,
	deboxUserID string,
) ([]MarketPoolView, error) {
	pools, err := collectMany[MarketPoolView](ctx, s.db, `
		SELECT
			p.id, p.chain_key, p.chain_id, p.protocol, p.protocol_version, p.pool_key,
			p.pool_address, p.factory_address, p.factory_verified,
			p.token0_address, p.token0_symbol, p.token0_decimals,
			p.token1_address, p.token1_symbol, p.token1_decimals,
			CASE WHEN quote_snapshot.id IS NOT NULL OR mp.frozen_at IS NOT NULL THEN COALESCE(quote_snapshot.liquidity_usd::text,'')
				ELSE p.liquidity_usd::text END AS liquidity_usd,
			quote_snapshot.id AS quote_snapshot_id,
			CASE WHEN quote_snapshot.id IS NOT NULL OR mp.frozen_at IS NOT NULL THEN COALESCE(quote_snapshot.quote_quality,'{}'::jsonb)
				ELSE COALESCE(p.metadata->'quote_quality','{}'::jsonb) END AS quote_quality,
			p.supports_event_parsing, p.parser_adapter, p.verification_status,
			p.metadata, p.first_seen_at, p.last_seen_at, p.created_at, p.updated_at,
			mpp.selected, mpp.is_primary, mpp.discovery_source
		FROM market_project_pools mpp
		JOIN market_projects mp ON mp.id = mpp.market_project_id
		JOIN market_pools p ON p.id = mpp.market_pool_id
		LEFT JOIN market_project_deployments quote_deployment
		  ON quote_deployment.id = mpp.market_project_deployment_id
		 AND quote_deployment.market_project_id = mp.id
		LEFT JOIN market_asset_deployments quote_asset
		  ON quote_asset.id = quote_deployment.market_asset_deployment_id
		 AND quote_asset.chain_id = p.chain_id
		LEFT JOIN LATERAL (
			SELECT ms.id, ms.liquidity_usd, ms.quote_quality
			FROM market_snapshots ms
			WHERE ms.market_pool_id = p.id
			  AND ms.chain_id = p.chain_id
			  AND ms.token_address = CASE
				WHEN mpp.market_project_deployment_id IS NOT NULL THEN quote_asset.token_address
				WHEN mp.chain_id = p.chain_id AND mp.token_address IN (p.token0_address,p.token1_address) THEN mp.token_address
			  END
			  AND ms.captured_at <= COALESCE(mp.frozen_at,NOW())
			ORDER BY ms.captured_at DESC, ms.id DESC
			LIMIT 1
		) quote_snapshot ON TRUE
		WHERE mpp.market_project_id = $1 AND mp.debox_user_id = $2
		  AND (mp.frozen_at IS NULL OR mpp.created_at <= mp.frozen_at)
		ORDER BY mpp.is_primary DESC, mpp.selected DESC, p.liquidity_usd DESC, p.id
	`, projectID, deboxUserID)
	if err != nil {
		return nil, fmt.Errorf("list market project pool views: %w", err)
	}
	return pools, nil
}

func (s *Store) CreateMarketSnapshot(
	ctx context.Context,
	params CreateMarketSnapshotParams,
) (MarketSnapshot, error) {
	params.ChainKey = strings.ToLower(strings.TrimSpace(params.ChainKey))
	tokenAddress, err := normalizeMarketAddress(params.TokenAddress)
	if err != nil || params.ChainKey == "" || params.ChainID <= 0 || params.MarketPoolID <= 0 {
		return MarketSnapshot{}, ErrInvalidMarketProject
	}
	params.TokenAddress = tokenAddress
	capturedAt := params.CapturedAt.UTC()
	if capturedAt.IsZero() {
		capturedAt = time.Now().UTC()
	}
	// pgx persists timestamp columns at microsecond precision. Match that
	// precision in JSON evidence so an equal fetch/capture instant cannot turn
	// into an apparently future fetch after a database round trip.
	quality := params.QuoteQuality.Clone()
	if quality.FetchedAt != nil {
		value := quality.FetchedAt.UTC().Truncate(time.Microsecond)
		quality.FetchedAt = &value
	}
	if quality.SourceTimestamp != nil {
		value := quality.SourceTimestamp.UTC().Truncate(time.Microsecond)
		quality.SourceTimestamp = &value
	}
	snapshot, err := collectOne[MarketSnapshot](ctx, s.db, `
		INSERT INTO market_snapshots (
			chain_key, chain_id, token_address, market_pool_id,
			price_usd, liquidity_usd,
			fdv_usd, market_cap_usd, volume_5m_usd, volume_15m_usd,
			volume_1h_usd, volume_6h_usd, volume_24h_usd,
			buys_5m, sells_5m, buys_1h, sells_1h, buys_24h, sells_24h,
			source, source_timestamp, captured_at, raw_payload, quote_quality
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
			$14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24
		)
		RETURNING `+marketSnapshotColumns,
		params.ChainKey,
		params.ChainID,
		params.TokenAddress,
		params.MarketPoolID,
		params.PriceUSD,
		params.LiquidityUSD,
		params.FDVUSD,
		params.MarketCapUSD,
		params.Volume5mUSD,
		params.Volume15mUSD,
		params.Volume1hUSD,
		params.Volume6hUSD,
		params.Volume24hUSD,
		params.Buys5m,
		params.Sells5m,
		params.Buys1h,
		params.Sells1h,
		params.Buys24h,
		params.Sells24h,
		strings.TrimSpace(params.Source),
		params.SourceTimestamp,
		capturedAt,
		normalizedJSON(params.RawPayload),
		quality,
	)
	if err != nil {
		return MarketSnapshot{}, fmt.Errorf("create market snapshot: %w", err)
	}
	return snapshot, nil
}

func (s *Store) ListMarketSnapshots(
	ctx context.Context,
	projectID int64,
	deboxUserID string,
	limit int,
) ([]MarketSnapshot, error) {
	limit = clamp(limit, 1, 1000)
	snapshots, err := collectMany[MarketSnapshot](ctx, s.db, `
		SELECT
			ms.id, ms.chain_key, ms.chain_id, ms.token_address, ms.market_pool_id,
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
			ms.buys_24h, ms.sells_24h, ms.source, ms.source_timestamp,
			ms.quote_quality, ms.captured_at, ms.raw_payload
		FROM market_snapshots ms
		JOIN market_projects mp
		  ON mp.chain_id = ms.chain_id
		 AND mp.token_address = ms.token_address
		WHERE mp.id = $1 AND mp.debox_user_id = $2
		ORDER BY ms.captured_at DESC, ms.id DESC
		LIMIT $3
	`, projectID, deboxUserID, limit)
	if err != nil {
		return nil, fmt.Errorf("list market snapshots: %w", err)
	}
	return snapshots, nil
}

func (s *Store) LatestMarketSnapshot(
	ctx context.Context,
	chainID int64,
	tokenAddress string,
	marketPoolID *int64,
) (*MarketSnapshot, error) {
	tokenAddress, err := normalizeMarketAddress(tokenAddress)
	if err != nil || chainID <= 0 {
		return nil, ErrInvalidMarketProject
	}
	value, err := collectOptional[MarketSnapshot](ctx, s.db, `
		SELECT `+marketSnapshotColumns+`
		FROM market_snapshots
		WHERE chain_id = $1 AND token_address = $2
		  AND ($3::bigint IS NULL OR market_pool_id = $3)
		ORDER BY captured_at DESC, id DESC
		LIMIT 1
	`, chainID, tokenAddress, marketPoolID)
	if err != nil {
		return nil, fmt.Errorf("get latest market snapshot: %w", err)
	}
	return value, nil
}

// MarketValuationSnapshot returns the last observation available at the
// transaction time, including an unavailable fallback or unknown observation.
// The caller checks QuoteQuality.AvailableAt(at) before using its amounts. The
// quality predicate must not be pushed into this query: doing so would hide a
// newer failed observation behind an older successful one.
func (s *Store) MarketValuationSnapshot(
	ctx context.Context,
	chainID int64,
	tokenAddress string,
	marketPoolID *int64,
	at time.Time,
) (*MarketSnapshot, error) {
	tokenAddress, err := normalizeMarketAddress(tokenAddress)
	if err != nil || chainID <= 0 || at.IsZero() || marketPoolID != nil && *marketPoolID <= 0 {
		return nil, ErrInvalidMarketProject
	}
	value, err := collectOptional[MarketSnapshot](ctx, s.db, `
		SELECT `+marketSnapshotColumns+`
		FROM market_snapshots
		WHERE chain_id = $1 AND token_address = $2
		  AND ($3::bigint IS NULL OR market_pool_id = $3)
		  AND captured_at <= $4
		ORDER BY captured_at DESC, id DESC
		LIMIT 1
	`, chainID, tokenAddress, marketPoolID, at.UTC())
	if err != nil {
		return nil, fmt.Errorf("get market valuation observation: %w", err)
	}
	return value, nil
}

func validateMarketProjectParams(params *CreateMarketProjectParams) error {
	params.DeBoxUserID = strings.TrimSpace(params.DeBoxUserID)
	if params.DeBoxUserID == "" {
		return ErrInvalidMarketProject
	}
	params.ChainKey = strings.ToLower(strings.TrimSpace(params.ChainKey))
	if params.ChainKey == "" {
		params.ChainKey = "bsc"
	}
	if params.ChainID <= 0 {
		if params.ChainKey == "bsc" {
			params.ChainID = 56
		} else {
			return ErrInvalidMarketProject
		}
	}
	address, err := normalizeMarketAddress(params.TokenAddress)
	if err != nil {
		return ErrInvalidMarketProject
	}
	params.TokenAddress = address
	if params.TokenDecimals < 0 || params.TokenDecimals > 255 {
		return ErrInvalidMarketProject
	}
	params.FourMemeStatus = normalizeFourMemeStatus(params.FourMemeStatus)
	return nil
}

func validateMarketPoolParams(params *UpsertMarketPoolParams) error {
	params.ChainKey = strings.ToLower(strings.TrimSpace(params.ChainKey))
	params.Protocol = strings.ToLower(strings.TrimSpace(params.Protocol))
	params.PoolKey = strings.ToLower(strings.TrimSpace(params.PoolKey))
	if params.ChainKey == "" || params.ChainID <= 0 ||
		params.Protocol == "" || params.PoolKey == "" {
		return ErrInvalidMarketPool
	}
	var err error
	if params.Token0Address, err = normalizeMarketAddress(params.Token0Address); err != nil {
		return ErrInvalidMarketPool
	}
	if params.Token1Address, err = normalizeMarketAddress(params.Token1Address); err != nil {
		return ErrInvalidMarketPool
	}
	if params.PoolAddress, err = normalizeOptionalMarketAddress(params.PoolAddress); err != nil {
		return ErrInvalidMarketPool
	}
	if params.FactoryAddress, err = normalizeOptionalMarketAddress(params.FactoryAddress); err != nil {
		return ErrInvalidMarketPool
	}
	if params.Token0Decimals < 0 || params.Token0Decimals > 255 ||
		params.Token1Decimals < 0 || params.Token1Decimals > 255 {
		return ErrInvalidMarketPool
	}
	if strings.TrimSpace(params.LiquidityUSD) == "" {
		params.LiquidityUSD = "0"
	}
	params.VerificationStatus = strings.ToLower(strings.TrimSpace(params.VerificationStatus))
	switch params.VerificationStatus {
	case "verified", "unsupported", "invalid":
	default:
		params.VerificationStatus = "unverified"
	}
	return nil
}

func normalizeMarketAddress(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !marketAddressPattern.MatchString(value) {
		return "", ErrInvalidMarketAddress
	}
	return strings.ToLower(value), nil
}

func normalizeOptionalMarketAddress(value *string) (*string, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil, nil
	}
	normalized, err := normalizeMarketAddress(*value)
	if err != nil {
		return nil, err
	}
	return &normalized, nil
}

func normalizeFourMemeStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "not_applicable", "bonding", "graduating", "graduated", "migrated":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "unknown"
	}
}

func normalizedJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 || !json.Valid(value) {
		return json.RawMessage(`{}`)
	}
	return value
}

func boolInt(value bool) int32 {
	if value {
		return 1
	}
	return 0
}
