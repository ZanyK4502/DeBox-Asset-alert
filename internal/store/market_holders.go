package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const marketHolderColumns = `
	id, market_asset_deployment_id,
	chain_key, chain_id, token_address, holder_address,
	balance_raw::text AS balance_raw,
	balance::text AS balance,
	supply_percent::text AS supply_percent,
	rank, address_kind, excluded, exclusion_reason, source,
	first_seen_at, last_seen_at, updated_at
`

const marketHolderSnapshotColumns = `
	id, market_asset_deployment_id,
	chain_key, chain_id, token_address, holder_address,
	balance_raw::text AS balance_raw,
	balance::text AS balance,
	supply_percent::text AS supply_percent,
	rank, source, captured_at
`

const marketAddressLabelColumns = `
	id, debox_user_id, market_project_id, market_project_deployment_id,
	chain_key, chain_id,
	address, label_type, label, excluded, created_at, updated_at
`

type UpsertMarketHolderParams struct {
	ChainKey        string
	ChainID         int64
	TokenAddress    string
	HolderAddress   string
	BalanceRaw      string
	Balance         string
	SupplyPercent   *string
	Rank            *int32
	AddressKind     string
	Excluded        bool
	ExclusionReason string
	Source          string
	SeenAt          time.Time
	RecordSnapshot  bool
}

type UpsertMarketAddressLabelParams struct {
	DeBoxUserID     string
	MarketProjectID int64
	Address         string
	LabelType       string
	Label           string
	Excluded        bool
}

func (s *Store) UpsertMarketHolder(
	ctx context.Context,
	params UpsertMarketHolderParams,
) (MarketHolder, error) {
	params.ChainKey = strings.ToLower(strings.TrimSpace(params.ChainKey))
	tokenAddress, err := normalizeMarketAddress(params.TokenAddress)
	if err != nil || params.ChainKey == "" || params.ChainID <= 0 {
		return MarketHolder{}, ErrInvalidMarketHolder
	}
	params.TokenAddress = tokenAddress
	address, err := normalizeMarketAddress(params.HolderAddress)
	if err != nil {
		return MarketHolder{}, ErrInvalidMarketHolder
	}
	params.HolderAddress = address
	if strings.TrimSpace(params.BalanceRaw) == "" {
		params.BalanceRaw = "0"
	}
	if strings.TrimSpace(params.Balance) == "" {
		params.Balance = "0"
	}
	if strings.TrimSpace(params.AddressKind) == "" {
		params.AddressKind = "wallet"
	}
	params.Source = strings.ToLower(strings.TrimSpace(params.Source))
	seenAt := params.SeenAt.UTC()
	if seenAt.IsZero() {
		seenAt = time.Now().UTC()
	}
	return withTxValue(ctx, s.db, func(tx DBTX) (MarketHolder, error) {
		holder, err := collectOne[MarketHolder](ctx, tx, `
			INSERT INTO market_holders (
				chain_key, chain_id, token_address, holder_address, balance_raw, balance,
				supply_percent, rank, address_kind, excluded,
				exclusion_reason, source, first_seen_at, last_seen_at
			)
			VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $13
			)
			ON CONFLICT (chain_id, token_address, holder_address) DO UPDATE
			SET balance_raw = EXCLUDED.balance_raw,
			    balance = EXCLUDED.balance,
			    supply_percent = EXCLUDED.supply_percent,
			    rank = EXCLUDED.rank,
			    address_kind = EXCLUDED.address_kind,
			    excluded = EXCLUDED.excluded,
			    exclusion_reason = EXCLUDED.exclusion_reason,
			    source = EXCLUDED.source,
			    last_seen_at = EXCLUDED.last_seen_at,
			    updated_at = NOW()
			RETURNING `+marketHolderColumns,
			params.ChainKey,
			params.ChainID,
			params.TokenAddress,
			params.HolderAddress,
			params.BalanceRaw,
			params.Balance,
			params.SupplyPercent,
			params.Rank,
			params.AddressKind,
			boolInt(params.Excluded),
			params.ExclusionReason,
			params.Source,
			seenAt,
		)
		if err != nil {
			return MarketHolder{}, fmt.Errorf("upsert market holder: %w", err)
		}
		if params.RecordSnapshot {
			if _, err := tx.Exec(ctx, `
				INSERT INTO market_holder_snapshots (
					chain_key, chain_id, token_address, holder_address, balance_raw, balance,
					supply_percent, rank, source, captured_at
				)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
				ON CONFLICT (
					chain_id, token_address, holder_address, captured_at
				) DO NOTHING
			`,
				params.ChainKey,
				params.ChainID,
				params.TokenAddress,
				params.HolderAddress,
				params.BalanceRaw,
				params.Balance,
				params.SupplyPercent,
				params.Rank,
				params.Source,
				seenAt,
			); err != nil {
				return MarketHolder{}, fmt.Errorf("record market holder snapshot: %w", err)
			}
		}
		return holder, nil
	})
}

func (s *Store) ListMarketHolders(
	ctx context.Context,
	projectID int64,
	deboxUserID string,
	includeExcluded bool,
	limit int,
) ([]MarketHolder, error) {
	limit = clamp(limit, 1, 500)
	query := `
		SELECT
			mh.id, mh.chain_key, mh.chain_id, mh.token_address, mh.holder_address,
			mh.balance_raw::text AS balance_raw,
			mh.balance::text AS balance,
			mh.supply_percent::text AS supply_percent,
			mh.rank, mh.address_kind, mh.excluded, mh.exclusion_reason,
			mh.source, mh.first_seen_at, mh.last_seen_at, mh.updated_at
		FROM market_holders mh
		JOIN market_projects mp ON mp.id = $1
		WHERE mp.debox_user_id = $2
		  AND (
			(mp.chain_id = mh.chain_id AND mp.token_address = mh.token_address)
			OR EXISTS (
				SELECT 1
				FROM market_project_deployments mpd
				JOIN market_asset_deployments mad
				  ON mad.id = mpd.market_asset_deployment_id
				WHERE mpd.market_project_id = mp.id
				  AND mpd.status <> 'removed'
				  AND mad.chain_id = mh.chain_id
				  AND mad.token_address = mh.token_address
			)
		  )
	`
	if !includeExcluded {
		query += " AND mh.excluded = 0"
	}
	query += `
		ORDER BY mh.rank NULLS LAST, mh.balance_raw DESC, mh.holder_address
		LIMIT $3
	`
	holders, err := collectMany[MarketHolder](ctx, s.db, query, projectID, deboxUserID, limit)
	if err != nil {
		return nil, fmt.Errorf("list market holders: %w", err)
	}
	return holders, nil
}

func (s *Store) ListMarketHolderViews(
	ctx context.Context,
	projectID int64,
	deboxUserID string,
	includeExcluded bool,
	limit int,
) ([]MarketHolderView, error) {
	limit = clamp(limit, 1, 500)
	query := `
		SELECT
			mh.id, mh.market_asset_deployment_id,
			mh.chain_key, mh.chain_id, mh.token_address, mh.holder_address,
			mh.balance_raw::text AS balance_raw,
			mh.balance::text AS balance,
			mh.supply_percent::text AS supply_percent,
			mh.rank, mh.address_kind, mh.excluded, mh.exclusion_reason,
			mh.source, mh.first_seen_at, mh.last_seen_at, mh.updated_at,
			previous.balance::text AS previous_balance,
			previous.rank AS previous_rank,
			CASE
				WHEN previous.rank IS NULL AND mh.rank IS NOT NULL THEN 'entered'
				WHEN previous.rank IS NOT NULL AND mh.rank IS NULL THEN 'exited'
				WHEN previous.balance IS NOT NULL AND mh.balance > previous.balance THEN 'increased'
				WHEN previous.balance IS NOT NULL AND mh.balance < previous.balance THEN 'decreased'
				ELSE 'unchanged'
			END AS change_type
		FROM market_holders mh
		JOIN market_projects mp ON mp.id = $1
		LEFT JOIN LATERAL (
			SELECT mhs.balance, mhs.rank
			FROM market_holder_snapshots mhs
			WHERE mhs.chain_id = mh.chain_id
			  AND mhs.token_address = mh.token_address
			  AND mhs.holder_address = mh.holder_address
			ORDER BY mhs.captured_at DESC, mhs.id DESC
			OFFSET 1
			LIMIT 1
		) previous ON TRUE
		WHERE mp.debox_user_id = $2
		  AND (
			(mp.chain_id = mh.chain_id AND mp.token_address = mh.token_address)
			OR EXISTS (
				SELECT 1
				FROM market_project_deployments mpd
				JOIN market_asset_deployments mad
				  ON mad.id = mpd.market_asset_deployment_id
				WHERE mpd.market_project_id = mp.id
				  AND mpd.status <> 'removed'
				  AND mad.chain_id = mh.chain_id
				  AND mad.token_address = mh.token_address
			)
		  )
	`
	if !includeExcluded {
		query += " AND mh.excluded = 0"
	}
	query += `
		ORDER BY mh.chain_id, mh.rank NULLS LAST, mh.balance_raw DESC, mh.holder_address
		LIMIT $3
	`
	values, err := collectMany[MarketHolderView](
		ctx, s.db, query, projectID, deboxUserID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list market holder views: %w", err)
	}
	return values, nil
}

func (s *Store) ListMarketHolderSnapshots(
	ctx context.Context,
	projectID int64,
	deboxUserID string,
	holderAddress string,
	limit int,
) ([]MarketHolderSnapshot, error) {
	address, err := normalizeMarketAddress(holderAddress)
	if err != nil {
		return nil, ErrInvalidMarketHolder
	}
	limit = clamp(limit, 1, 1000)
	snapshots, err := collectMany[MarketHolderSnapshot](ctx, s.db, `
		SELECT
			mhs.id, mhs.chain_key, mhs.chain_id, mhs.token_address,
			mhs.holder_address,
			mhs.balance_raw::text AS balance_raw,
			mhs.balance::text AS balance,
			mhs.supply_percent::text AS supply_percent,
			mhs.rank, mhs.source, mhs.captured_at
		FROM market_holder_snapshots mhs
		JOIN market_projects mp ON mp.id = $1
		WHERE mp.debox_user_id = $2
		  AND (
			(mp.chain_id = mhs.chain_id AND mp.token_address = mhs.token_address)
			OR EXISTS (
				SELECT 1
				FROM market_project_deployments mpd
				JOIN market_asset_deployments mad
				  ON mad.id = mpd.market_asset_deployment_id
				WHERE mpd.market_project_id = mp.id
				  AND mpd.status <> 'removed'
				  AND mad.chain_id = mhs.chain_id
				  AND mad.token_address = mhs.token_address
			)
		  )
		  AND mhs.holder_address = $3
		ORDER BY mhs.captured_at DESC, mhs.id DESC
		LIMIT $4
	`, projectID, deboxUserID, address, limit)
	if err != nil {
		return nil, fmt.Errorf("list market holder snapshots: %w", err)
	}
	return snapshots, nil
}

func (s *Store) ClearMarketHolderRanksOutsideSnapshot(
	ctx context.Context,
	chainID int64,
	tokenAddress string,
	currentAddresses []string,
	seenAt time.Time,
) (int64, error) {
	tokenAddress, err := normalizeMarketAddress(tokenAddress)
	if err != nil || chainID <= 0 {
		return 0, ErrInvalidMarketHolder
	}
	for index, address := range currentAddresses {
		currentAddresses[index], err = normalizeMarketAddress(address)
		if err != nil {
			return 0, ErrInvalidMarketHolder
		}
	}
	if seenAt.IsZero() {
		seenAt = time.Now().UTC()
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE market_holders
		SET rank = NULL,
		    last_seen_at = $4,
		    updated_at = NOW()
		WHERE chain_id = $1
		  AND token_address = $2
		  AND rank IS NOT NULL
		  AND NOT (holder_address = ANY($3::text[]))
	`, chainID, tokenAddress, currentAddresses, seenAt.UTC())
	if err != nil {
		return 0, fmt.Errorf("clear stale market holder ranks: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *Store) UpsertMarketAddressLabel(
	ctx context.Context,
	params UpsertMarketAddressLabelParams,
) (MarketAddressLabel, error) {
	params.DeBoxUserID = strings.TrimSpace(params.DeBoxUserID)
	params.LabelType = strings.ToLower(strings.TrimSpace(params.LabelType))
	if params.LabelType == "" {
		params.LabelType = "custom"
	}
	address, err := normalizeMarketAddress(params.Address)
	if err != nil || params.DeBoxUserID == "" {
		return MarketAddressLabel{}, ErrInvalidMarketAddressLabel
	}
	params.Address = address
	label, err := collectOne[MarketAddressLabel](ctx, s.db, `
		INSERT INTO market_address_labels (
			debox_user_id, market_project_id, chain_key, chain_id,
			address, label_type, label, excluded
		)
		SELECT $1, $2, mp.chain_key, mp.chain_id, $3, $4, $5, $6
		FROM market_projects mp
		WHERE mp.id = $2 AND mp.debox_user_id = $1
		ON CONFLICT (market_project_id, address) DO UPDATE
		SET label_type = EXCLUDED.label_type,
		    label = EXCLUDED.label,
		    excluded = EXCLUDED.excluded,
		    updated_at = NOW()
		RETURNING `+marketAddressLabelColumns,
		params.DeBoxUserID,
		params.MarketProjectID,
		params.Address,
		params.LabelType,
		params.Label,
		boolInt(params.Excluded),
	)
	if isNoRows(err) {
		return MarketAddressLabel{}, ErrNotFound
	}
	if err != nil {
		return MarketAddressLabel{}, fmt.Errorf("upsert market address label: %w", err)
	}
	return label, nil
}

func (s *Store) ListMarketAddressLabels(
	ctx context.Context,
	projectID int64,
	deboxUserID string,
) ([]MarketAddressLabel, error) {
	labels, err := collectMany[MarketAddressLabel](ctx, s.db, `
		SELECT `+marketAddressLabelColumns+`
		FROM market_address_labels
		WHERE market_project_id = $1 AND debox_user_id = $2
		ORDER BY excluded DESC, label_type, address
	`, projectID, deboxUserID)
	if err != nil {
		return nil, fmt.Errorf("list market address labels: %w", err)
	}
	return labels, nil
}

func (s *Store) DeleteMarketAddressLabel(
	ctx context.Context,
	labelID int64,
	deboxUserID string,
) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		DELETE FROM market_address_labels
		WHERE id = $1 AND debox_user_id = $2
	`, labelID, deboxUserID)
	if err != nil {
		return false, fmt.Errorf("delete market address label: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
