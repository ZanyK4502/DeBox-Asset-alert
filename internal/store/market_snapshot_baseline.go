package store

import (
	"context"
	"fmt"
	"time"
)

// MarketSnapshotBaselineForTarget returns the last recorded observation before
// the target. Quality is checked by the evaluator: a fallback marker must not
// be skipped in favor of an older healthy value. The existing time indexes
// bound this to one row, including for a 24-hour custom window.
func (s *Store) MarketSnapshotBaselineForTarget(ctx context.Context, target MarketRuleTarget, at time.Time) (*MarketSnapshot, error) {
	value, err := collectOne[MarketSnapshot](ctx, s.db, `SELECT `+marketSnapshotColumns+`
	 FROM market_snapshots WHERE chain_id=$1 AND token_address=$2
	 AND ($3::bigint IS NULL OR market_pool_id=$3) AND captured_at<=$4
	 ORDER BY captured_at DESC,id DESC LIMIT 1`, target.ChainID, target.TokenAddress, target.MarketPoolID, at.UTC())
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load exact market snapshot baseline: %w", err)
	}
	return &value, nil
}
