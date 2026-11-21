package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketwindow"
	"github.com/jackc/pgx/v5/pgconn"
)

// MarketRecommendationSample is a read-only projection whose scope and original
// valuation have already been verified by the recommendation reader. It keeps
// source identities instead of inventing a quote timestamp for an aggregate.
type MarketRecommendationSample struct {
	USDValue      string    `db:"usd_value"`
	MarketEventID int64     `db:"market_event_id"`
	ReceiptID     *int64    `db:"receipt_id"`
	TradeReadyID  *int64    `db:"trade_ready_id"`
	PoolIDs       []int64   `db:"pool_ids"`
	ChainID       int64     `db:"chain_id"`
	TokenAddress  string    `db:"token_address"`
	Direction     string    `db:"direction"`
	OccurredAt    time.Time `db:"occurred_at"`
}

const maxRecommendationScopePools = 10000

const marketRecommendationQueryBudget = 2 * time.Second

var ErrMarketRecommendationUnavailable = errors.New("market recommendation samples are temporarily unavailable")

// ListMarketRecommendationSamples filters by chain occurrence time, current
// selected-pool scope and original reliable valuations before the final LIMIT.
// Complete DEX amounts come only from the selected pool summaries, never from a
// shared parent amount. Missing old summaries remain unavailable. The legacy
// single-pool branch requires its original quote proof and cannot rescue a DEX
// parent whose new summaries are missing.
func (s *Store) ListMarketRecommendationSamples(ctx context.Context, projectID int64, deboxUserID string, reference MarketSnapshot, from, to time.Time, limit int) ([]MarketRecommendationSample, error) {
	address, err := normalizeMarketAddress(reference.TokenAddress)
	deboxUserID = strings.TrimSpace(deboxUserID)
	if err != nil || projectID <= 0 || deboxUserID == "" || reference.ChainID <= 0 || from.IsZero() || to.IsZero() ||
		!from.Before(to) || to.Sub(from) > marketwindow.RecommendationLookback {
		return nil, ErrInvalidMarketRule
	}
	limit = clamp(limit, 1, marketwindow.MaxRecommendationSamples)
	queryCtx, cancel := context.WithTimeout(ctx, marketRecommendationQueryBudget)
	defer cancel()
	if err := queryCtx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMarketRecommendationUnavailable, err)
	}
	tx, err := s.db.Begin(queryCtx)
	if err != nil {
		return nil, marketRecommendationReadError(queryCtx, err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cleanupCancel()
		_ = tx.Rollback(cleanupCtx)
	}()
	if _, err := tx.Exec(queryCtx, "SET TRANSACTION READ ONLY"); err != nil {
		return nil, marketRecommendationReadError(queryCtx, err)
	}
	if _, err := tx.Exec(queryCtx, "SET LOCAL statement_timeout = '2s'"); err != nil {
		return nil, marketRecommendationReadError(queryCtx, err)
	}
	query := `
		WITH selected_pools AS MATERIALIZED (
			SELECT DISTINCT p.id FROM market_projects mp
			JOIN market_project_pools link ON link.market_project_id=mp.id AND link.selected=1
			JOIN market_pools p ON p.id=link.market_pool_id AND p.chain_id=$3
			  AND (p.token0_address=$4 OR p.token1_address=$4)
			WHERE mp.id=$1 AND mp.debox_user_id=$2
			  AND (EXISTS (
			    SELECT 1 FROM market_project_deployments d JOIN market_asset_deployments a ON a.id=d.market_asset_deployment_id
			    WHERE d.market_project_id=mp.id AND d.status<>'removed' AND a.chain_id=$3 AND a.token_address=$4
			  ) OR (mp.chain_id=$3 AND mp.token_address=$4 AND NOT EXISTS (
			    SELECT 1 FROM market_project_deployments d WHERE d.market_project_id=mp.id
			  )))
			  AND (link.market_project_deployment_id IS NULL OR EXISTS (
			    SELECT 1 FROM market_project_deployments d JOIN market_asset_deployments a ON a.id=d.market_asset_deployment_id
			    WHERE d.id=link.market_project_deployment_id AND d.market_project_id=mp.id AND d.status<>'removed'
			      AND a.chain_id=$3 AND a.token_address=$4
			  ))
			ORDER BY p.id LIMIT 10001
		), scope AS MATERIALIZED (
			SELECT COALESCE(array_agg(id ORDER BY id),ARRAY[]::bigint[]) AS pool_ids,COUNT(*) AS pool_count FROM selected_pools
		), facts AS (
			SELECT projected.usd::text AS usd_value,e.id AS market_event_id,receipt.id AS receipt_id,
			       ready.id AS trade_ready_id,projected.pool_ids,e.chain_id,e.token_address,e.event_type AS direction,e.occurred_at
			FROM market_events e
			JOIN market_trade_parents parent ON parent.parent_event_id=e.id
			JOIN market_trade_ready_events ready ON ready.parent_event_id=e.id AND ready.valid
			JOIN market_transaction_receipts receipt ON receipt.id=ready.receipt_id AND receipt.revision=ready.receipt_revision
			JOIN market_trade_scopes captured ON captured.id=receipt.scope_id
			JOIN market_trade_parent_summaries complete ON complete.receipt_id=receipt.id AND complete.parent_event_id=e.id AND complete.summary_version=1
			CROSS JOIN scope
			CROSS JOIN LATERAL (
				SELECT COUNT(*) AS expected,COUNT(summary.pool_id) AS available,
				       SUM(summary.usd_value) AS usd,array_agg(contribution.id ORDER BY contribution.id) AS pool_ids,
				       COALESCE(BOOL_AND(summary.summary_version=1 AND summary.usd_valuation_complete
				         AND summary.usd_value IS NOT NULL AND summary.valuation_at=e.occurred_at),FALSE) AS valued
				FROM (SELECT DISTINCT unnest(complete.pool_ids) AS id) contribution
				JOIN selected_pools selected ON selected.id=contribution.id
				LEFT JOIN market_trade_pool_summaries summary ON summary.receipt_id=receipt.id AND summary.parent_event_id=e.id AND summary.pool_id=contribution.id
			) projected
			WHERE scope.pool_count BETWEEN 1 AND 10000 AND e.chain_id=$3 AND e.token_address=$4
			  AND e.occurred_at >= $5 AND e.occurred_at <= $6 AND e.event_type IN ('buy','sell') AND e.confirmed=1 AND e.reorged=0
			  AND parent.chain_id=e.chain_id AND parent.token_address=e.token_address AND parent.direction=e.event_type AND parent.transaction_hash=e.transaction_hash
			  AND receipt.complete AND receipt.confirmed AND NOT receipt.reorged
			  AND receipt.chain_id=e.chain_id AND receipt.transaction_hash=e.transaction_hash
			  AND receipt.block_number=e.block_number AND receipt.block_hash=e.block_hash
			  AND ready.chain_id=e.chain_id AND ready.token_address=e.token_address
			  AND scope.pool_ids <@ captured.pool_ids AND e.token_address=ANY(captured.token_addresses)
			  AND projected.expected>0 AND projected.available=projected.expected AND projected.valued AND projected.usd>0
			  AND EXISTS(SELECT 1 FROM market_scanned_blocks b WHERE b.chain_id=receipt.chain_id AND b.block_number=receipt.block_number AND b.block_hash=receipt.block_hash AND b.canonical=1)
			ORDER BY e.occurred_at DESC,e.id DESC LIMIT $7
		), legacy AS (
			SELECT e.usd_value::text AS usd_value,e.id AS market_event_id,NULL::bigint AS receipt_id,NULL::bigint AS trade_ready_id,
			       ARRAY[e.market_pool_id] AS pool_ids,e.chain_id,e.token_address,e.event_type AS direction,e.occurred_at
			FROM market_events e CROSS JOIN scope
			WHERE scope.pool_count BETWEEN 1 AND 10000 AND e.chain_id=$3 AND e.token_address=$4
			  AND e.occurred_at >= $5 AND e.occurred_at <= $6 AND e.event_type IN ('buy','sell') AND e.confirmed=1 AND e.reorged=0
			  AND e.market_pool_id=ANY(scope.pool_ids) AND e.usd_value>0 AND e.price_usd>0
			  AND NOT EXISTS(SELECT 1 FROM market_trade_parents p WHERE p.parent_event_id=e.id)
			  AND ` + marketValuationAvailableSQL("e", "$8::double precision") + `
			ORDER BY e.occurred_at DESC,e.id DESC LIMIT $7
		), eligible AS (
			SELECT * FROM facts UNION ALL SELECT * FROM legacy
		), picked AS (
			SELECT * FROM eligible ORDER BY occurred_at DESC,market_event_id DESC LIMIT $7
		)
		SELECT picked.*,scope.pool_count FROM picked CROSS JOIN scope
		UNION ALL
		SELECT ''::text,0::bigint,NULL::bigint,NULL::bigint,ARRAY[]::bigint[],$3::bigint,$4::text,''::text,$6::timestamptz,scope.pool_count
		FROM scope WHERE scope.pool_count>10000
		ORDER BY occurred_at DESC,market_event_id DESC
	`
	type sampleRow struct {
		MarketRecommendationSample
		PoolCount int64 `db:"pool_count"`
	}
	rows, err := collectMany[sampleRow](queryCtx, tx, query, projectID, deboxUserID, reference.ChainID, address,
		from.UTC(), to.UTC(), limit, float64(marketquality.MaxAge/time.Second))
	if err != nil {
		return nil, marketRecommendationReadError(queryCtx, err)
	}
	samples := make([]MarketRecommendationSample, 0, len(rows))
	for _, row := range rows {
		if row.PoolCount > maxRecommendationScopePools {
			return nil, fmt.Errorf("%w: recommendation pool scope exceeds bounded capacity", ErrInvalidMarketRule)
		}
		samples = append(samples, row.MarketRecommendationSample)
	}
	if err := tx.Commit(queryCtx); err != nil {
		return nil, marketRecommendationReadError(queryCtx, err)
	}
	return samples, nil
}

func marketRecommendationReadError(ctx context.Context, err error) error {
	if budgetErr := ctx.Err(); budgetErr != nil {
		return fmt.Errorf("%w: %w", ErrMarketRecommendationUnavailable, budgetErr)
	}
	var queryError *pgconn.PgError
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &queryError) && queryError.Code == "57014" {
		return fmt.Errorf("%w: %w", ErrMarketRecommendationUnavailable, err)
	}
	return fmt.Errorf("read scoped recent market recommendation samples: %w", err)
}
