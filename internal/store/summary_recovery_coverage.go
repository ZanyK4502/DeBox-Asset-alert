package store

import (
	"context"
	"fmt"
	"time"
)

type SummaryRecoveryCoverage struct {
	Status   string                        `json:"status"`
	From     time.Time                     `json:"from"`
	Through  time.Time                     `json:"through"`
	Boundary *NotificationRecoveryBoundary `json:"boundary,omitempty"`
	Chains   []SummaryChainCoverage        `json:"chains"`
	// This proves the configured collection scope, not every trade on a chain.
	Scope string `json:"scope"`
}

type SummaryChainCoverage struct {
	ChainID  int64                     `json:"chain_id"`
	ChainKey string                    `json:"chain_key"`
	Status   string                    `json:"status"`
	Reason   string                    `json:"reason,omitempty"`
	Gaps     []NotificationRecoveryGap `json:"gaps,omitempty"`
}

// Called in the report's read-only transaction. A missing proof is unknown,
// never a declaration that a zero event count means no on-chain activity.
func (s *Store) LoadDailySummaryRecoveryCoverage(ctx context.Context, owner string, from, through time.Time) (SummaryRecoveryCoverage, error) {
	result := SummaryRecoveryCoverage{Status: "not_applicable", From: from.UTC(), Through: through.UTC(), Scope: "configured_collection", Chains: []SummaryChainCoverage{}}
	if owner == "" || from.IsZero() || !through.After(from) {
		return result, ErrInvalidMarketRule
	}
	boundary, err := s.GetNotificationRecoveryBoundary(ctx)
	if err != nil {
		return result, err
	}
	result.Boundary = boundary
	targets, err := collectMany[MarketRuleTarget](ctx, s.db, `WITH tokens AS (
	 SELECT mp.chain_key,mp.chain_id,mp.token_address FROM market_projects mp WHERE mp.debox_user_id=$1 AND mp.status='active'
	 AND NOT EXISTS(SELECT 1 FROM market_project_deployments d WHERE d.market_project_id=mp.id AND d.status='active')
	 UNION SELECT a.chain_key,a.chain_id,a.token_address FROM market_projects mp
	 JOIN market_project_deployments d ON d.market_project_id=mp.id AND d.status='active'
	 JOIN market_asset_deployments a ON a.id=d.market_asset_deployment_id
	 WHERE mp.debox_user_id=$1 AND mp.status='active'
	) SELECT t.chain_key,t.chain_id,t.token_address,ARRAY(SELECT DISTINCT p.id FROM market_pools p
	 WHERE p.chain_id=t.chain_id AND p.supports_event_parsing=1 AND (p.token0_address=t.token_address OR p.token1_address=t.token_address)
	 ORDER BY p.id LIMIT 10001) AS trade_pool_ids FROM tokens t ORDER BY t.chain_id,t.token_address LIMIT 201`, owner)
	if err != nil {
		return result, fmt.Errorf("read summary collection targets: %w", err)
	}
	if len(targets) == 0 {
		return result, nil
	}
	indices := map[int64]int{}
	chains := []int64{}
	totalPools := 0
	for _, target := range targets {
		index, exists := indices[target.ChainID]
		if !exists {
			index = len(result.Chains)
			indices[target.ChainID] = index
			chains = append(chains, target.ChainID)
			result.Chains = append(result.Chains, SummaryChainCoverage{ChainID: target.ChainID, ChainKey: target.ChainKey, Status: "complete"})
		}
		coverage := &result.Chains[index]
		totalPools += len(target.TradePoolIDs)
		if len(targets) > 200 || totalPools > 10000 || len(target.TradePoolIDs) == 0 {
			coverage.Status = "unknown"
			coverage.Reason = "collection_scope_unavailable"
			continue
		}
		var block int64
		err := s.db.QueryRow(ctx, `SELECT block_number FROM market_scanned_blocks WHERE chain_id=$1 AND canonical=1
		 AND block_timestamp >= $2 ORDER BY block_number,id LIMIT 1`, target.ChainID, through.UTC()).Scan(&block)
		if isNoRows(err) {
			coverage.Status = "unknown"
			coverage.Reason = "scan_has_not_proved_report_end"
			continue
		}
		if err != nil {
			return result, err
		}
		complete, err := marketTradeWindowScanCoverageComplete(ctx, s.db, target, []MarketTradeScanWindow{{From: from.UTC(), Through: through.UTC(), ThroughBlock: block}})
		if err != nil {
			return result, err
		}
		if !complete {
			coverage.Status = "unknown"
			coverage.Reason = "collection_window_not_proven_complete"
		}
	}
	gaps, err := collectMany[NotificationRecoveryGap](ctx, s.db, `SELECT id,chain_id,chain_key,from_block,through_block,from_at,through_at,cutoff_at,reason
	 FROM notification_recovery_gaps WHERE chain_id=ANY($1::bigint[]) AND through_at>$2
	 AND (from_at IS NULL OR from_at<$3) ORDER BY chain_id,through_at,id LIMIT 1001`, chains, from.UTC(), through.UTC())
	if err != nil {
		return result, err
	}
	for _, gap := range gaps {
		c := &result.Chains[indices[gap.ChainID]]
		c.Status = "recorded_gap"
		c.Reason = "explicit_history_gap"
		c.Gaps = append(c.Gaps, gap)
	}
	result.Status = "complete"
	for index := range result.Chains {
		c := &result.Chains[index]
		if len(gaps) > 1000 {
			c.Status = "unknown"
			c.Reason = "gap_report_capacity_exceeded"
		}
		if c.Status != "complete" {
			result.Status = "incomplete"
		}
	}
	return result, nil
}
