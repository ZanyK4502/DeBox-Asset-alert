package store

import (
	"context"
	"fmt"
	"time"
)

// Only newly managed change history participates. Keep the latest two facts,
// frozen-project as-of facts and referenced event facts; never sweep old rows.
func (s *Store) CleanupManagedMarketHolderHistory(ctx context.Context, before time.Time) (int64, error) {
	if before.IsZero() {
		return 0, ErrInvalidMarketHolder
	}
	return withTxValue(ctx, s.db, func(tx DBTX) (int64, error) {
		if _, err := tx.Exec(ctx, `INSERT INTO market_holder_history_cleanup_cursor(singleton) VALUES(TRUE) ON CONFLICT DO NOTHING`); err != nil {
			return 0, err
		}
		var after *time.Time
		var afterID int64
		if err := tx.QueryRow(ctx, `SELECT after_at,after_id FROM market_holder_history_cleanup_cursor WHERE singleton=TRUE FOR UPDATE`).Scan(&after, &afterID); err != nil {
			return 0, err
		}
		var removed int64
		var nextAt *time.Time
		var nextID *int64
		err := tx.QueryRow(ctx, `
	 WITH scanned AS MATERIALIZED (
		SELECT old.* FROM market_holder_snapshots old
		WHERE old.managed_observation AND old.captured_at<$1 AND ($2::timestamptz IS NULL OR (old.captured_at,old.id)>($2,$3))
		ORDER BY old.captured_at,old.id LIMIT 1000 FOR UPDATE OF old SKIP LOCKED
	 ), candidates AS MATERIALIZED (
		SELECT old.id FROM scanned old WHERE TRUE
		 AND NOT EXISTS(SELECT 1 FROM market_holder_snapshot_refs ref WHERE ref.snapshot_id=old.id)
		 AND 2=(SELECT COUNT(*) FROM(SELECT 1 FROM market_holder_snapshots newer
			WHERE newer.chain_id=old.chain_id AND newer.token_address=old.token_address AND newer.holder_address=old.holder_address
			 AND (newer.captured_at,newer.id)>(old.captured_at,old.id) ORDER BY newer.captured_at,newer.id LIMIT 2) recent)
		 AND NOT EXISTS(
			SELECT 1 FROM (
			 SELECT mp.frozen_at FROM market_projects mp WHERE mp.chain_id=old.chain_id AND mp.token_address=old.token_address AND mp.frozen_at>=old.captured_at
			 UNION ALL
			 SELECT mp.frozen_at FROM market_asset_deployments a
			 JOIN market_project_deployments d ON d.market_asset_deployment_id=a.id AND d.status<>'removed'
			 JOIN market_projects mp ON mp.id=d.market_project_id
			 WHERE a.chain_id=old.chain_id AND a.token_address=old.token_address AND mp.frozen_at>=old.captured_at
			) frozen
			 WHERE NOT EXISTS(SELECT 1 FROM market_holder_snapshots newer
				WHERE newer.chain_id=old.chain_id AND newer.token_address=old.token_address AND newer.holder_address=old.holder_address
				 AND (newer.captured_at,newer.id)>(old.captured_at,old.id) AND newer.captured_at<=frozen.frozen_at)
		 )
	 ), deleted AS(DELETE FROM market_holder_snapshots old USING candidates c WHERE old.id=c.id RETURNING old.id)
	 SELECT (SELECT COUNT(*) FROM deleted),(SELECT captured_at FROM scanned ORDER BY captured_at DESC,id DESC LIMIT 1),(SELECT id FROM scanned ORDER BY captured_at DESC,id DESC LIMIT 1)
	`, before.UTC(), after, afterID).Scan(&removed, &nextAt, &nextID)
		if err != nil {
			return 0, fmt.Errorf("cleanup managed holder change history: %w", err)
		}
		id := int64(0)
		if nextID != nil {
			id = *nextID
		}
		if _, err := tx.Exec(ctx, `UPDATE market_holder_history_cleanup_cursor SET after_at=$1,after_id=$2 WHERE singleton=TRUE`, nextAt, id); err != nil {
			return 0, err
		}
		return removed, nil
	})
}
