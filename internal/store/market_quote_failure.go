package store

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
)

// RecordMarketQuoteFailure appends a small unavailable observation for known
// quotes that a failed provider request would otherwise leave apparently live.
// Collector calls run under the existing per-chain quote task lock. Latest-row
// selection is bounded by the supplied exact chain/token/pool targets; it never
// skips a fallback, creates missing prices, or copies the previous raw payload.
// The explicit quote_availability source preserves a distinct observation even
// when failure and success share a persisted microsecond; audit retains the
// original quote source without presenting the marker as a new provider quote.
func (s *Store) RecordMarketQuoteFailure(ctx context.Context, chainID int64, targets []MarketCollectionTarget, at time.Time) (int64, error) {
	if chainID <= 0 || at.IsZero() || len(targets) > 10000 {
		return 0, ErrInvalidMarketProject
	}
	if len(targets) == 0 {
		return 0, nil
	}
	type key struct {
		token string
		pool  int64
	}
	seen := make(map[key]struct{}, len(targets))
	keys := make([]key, 0, len(targets))
	for _, target := range targets {
		token, err := normalizeMarketAddress(target.TokenAddress)
		if err != nil || target.ChainID != chainID || target.MarketPoolID <= 0 {
			return 0, ErrInvalidMarketProject
		}
		entry := key{token: token, pool: target.MarketPoolID}
		if _, exists := seen[entry]; exists {
			continue
		}
		seen[entry] = struct{}{}
		keys = append(keys, entry)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].token != keys[j].token {
			return keys[i].token < keys[j].token
		}
		return keys[i].pool < keys[j].pool
	})
	tokens, pools := make([]string, len(keys)), make([]int64, len(keys))
	for i, entry := range keys {
		tokens[i], pools[i] = entry.token, entry.pool
	}
	at = at.UTC().Truncate(time.Microsecond)
	return withTxValue(ctx, s.db, func(tx DBTX) (int64, error) {
		tag, err := tx.Exec(ctx, `
			WITH requested AS MATERIALIZED (
				SELECT * FROM unnest($2::text[],$3::bigint[]) AS target(token_address,pool_id)
			), previous AS MATERIALIZED (
				SELECT last.* FROM requested target
				CROSS JOIN LATERAL (
					SELECT id,market_asset_deployment_id,chain_key,chain_id,token_address,market_pool_id,
						price_usd,liquidity_usd,fdv_usd,market_cap_usd,
						volume_5m_usd,volume_15m_usd,volume_1h_usd,volume_6h_usd,volume_24h_usd,
						buys_5m,sells_5m,buys_1h,sells_1h,buys_24h,sells_24h,
						source,source_timestamp,captured_at,quote_quality
					FROM market_snapshots
					WHERE chain_id=$1 AND token_address=target.token_address AND market_pool_id=target.pool_id
					ORDER BY captured_at DESC,id DESC LIMIT 1
				) last
			), eligible AS MATERIALIZED (
				SELECT * FROM previous
				WHERE captured_at<=$4 AND `+quoteAvailableSQL("quote_quality", "$4::timestamptz", "$5::double precision")+`
			)
				INSERT INTO market_snapshots (
					market_asset_deployment_id,chain_key,chain_id,token_address,market_pool_id,
					price_usd,liquidity_usd,fdv_usd,market_cap_usd,
					volume_5m_usd,volume_15m_usd,volume_1h_usd,volume_6h_usd,volume_24h_usd,
					buys_5m,sells_5m,buys_1h,sells_1h,buys_24h,sells_24h,
					source,source_timestamp,captured_at,raw_payload,quote_quality
				)
				SELECT market_asset_deployment_id,chain_key,chain_id,token_address,market_pool_id,
					price_usd,liquidity_usd,fdv_usd,market_cap_usd,
					volume_5m_usd,volume_15m_usd,volume_1h_usd,volume_6h_usd,volume_24h_usd,
					buys_5m,sells_5m,buys_1h,sells_1h,buys_24h,sells_24h,
					'quote_availability',source_timestamp,$4,jsonb_build_object('quote_failure',TRUE,'previous_snapshot_id',id,'original_source',source),
					jsonb_set(quote_quality,'{status}','"stale_fallback"'::jsonb)
				FROM eligible
				ON CONFLICT(chain_id,token_address,market_pool_id,source,captured_at) DO NOTHING
		`, chainID, tokens, pools, at, marketquality.MaxAge.Seconds())
		if err != nil {
			return 0, fmt.Errorf("record market quote failure observations: %w", err)
		}
		return tag.RowsAffected(), nil
	})
}
