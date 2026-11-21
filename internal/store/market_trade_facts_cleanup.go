package store

import (
	"context"
	"fmt"
	"time"
)

type MarketTradeCleanupResult struct {
	ReceiptsDeleted int64 `json:"receipts_deleted"`
	ScopesDeleted   int64 `json:"scopes_deleted"`
}

// CleanupOrphanMarketTradeData uses the existing event retention cutoff. Each
// invocation handles at most 10,000 receipts and 10,000 scopes, with no retry
// loop. Referenced parents (including MRE/Four.meme-protected parents) keep their
// receipt. SKIP LOCKED and the writer's scope KEY SHARE prevent reclamation of a
// scope between lookup and receipt insertion, without taking chain locks here.
func (s *Store) CleanupOrphanMarketTradeData(ctx context.Context, before time.Time) (MarketTradeCleanupResult, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (MarketTradeCleanupResult, error) {
		result := MarketTradeCleanupResult{}
		tag, err := tx.Exec(ctx, `
			WITH candidates AS (
				SELECT r.id FROM market_transaction_receipts r
				WHERE r.occurred_at<$1 AND NOT EXISTS (
					SELECT 1 FROM market_transaction_events x WHERE x.receipt_id=r.id
				)
				ORDER BY r.occurred_at,r.id LIMIT 10000 FOR UPDATE SKIP LOCKED
			)
			DELETE FROM market_transaction_receipts r USING candidates c
			WHERE r.id=c.id AND NOT EXISTS (
				SELECT 1 FROM market_transaction_events x WHERE x.receipt_id=r.id
			)
		`, before.UTC())
		if err != nil {
			return result, fmt.Errorf("cleanup expired orphan market receipts: %w", err)
		}
		result.ReceiptsDeleted = tag.RowsAffected()
		tag, err = tx.Exec(ctx, `
			WITH candidates AS (
				SELECT s.id FROM market_trade_scopes s
				WHERE NOT EXISTS(SELECT 1 FROM market_transaction_receipts r WHERE r.scope_id=s.id)
				 AND NOT EXISTS(SELECT 1 FROM market_scan_coverage c WHERE c.scope_id=s.id)
				ORDER BY s.id LIMIT 10000 FOR UPDATE SKIP LOCKED
			)
			DELETE FROM market_trade_scopes s USING candidates c
			WHERE s.id=c.id AND NOT EXISTS (
				SELECT 1 FROM market_transaction_receipts r WHERE r.scope_id=s.id
			)
			 AND NOT EXISTS(SELECT 1 FROM market_scan_coverage x WHERE x.scope_id=s.id)
		`)
		if err != nil {
			return result, fmt.Errorf("cleanup unreferenced market parser scopes: %w", err)
		}
		result.ScopesDeleted = tag.RowsAffected()
		return result, nil
	})
}
