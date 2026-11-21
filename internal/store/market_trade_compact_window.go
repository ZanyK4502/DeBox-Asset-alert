package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
)

func (s *Store) listCompactMarketTradeWindow(ctx context.Context, rule MarketRule, target MarketRuleTarget, maxReadyID int64, windows []MarketTradeScanWindow, liquidity *string, liquidityKnown bool, limit int) ([]MarketTradeWindowCandidate, error) {
	address, err := normalizeMarketAddress(target.TokenAddress)
	pools := uniquePositiveIDs(target.TradePoolIDs)
	if err != nil || target.ChainID <= 0 || len(pools) == 0 || len(pools) > 10000 || maxReadyID < 1 || len(windows) == 0 || limit < 1 || limit > MarketTradeWindowCandidateLimit {
		return nil, ErrInvalidMarketRule
	}
	froms, throughs := mergedMarketTradeTimes(windows)
	from, through := froms[0], throughs[len(throughs)-1]
	direction := "buy"
	if rule.RuleType == plans.MarketConsecutiveLargeSell {
		direction = "sell"
	}
	unit := strings.ToLower(strings.TrimSpace(rule.ThresholdUnit))
	// Missing groups and invalid proof facts are unknown, not zero. The parent
	// pool set makes partial pool-summary loss detectable without reading legs.
	// For nonnegative percent values, round(ratio,8)>=threshold is equivalent to
	// USD*2*100*1e8 >= liquidity*(2*ceil(threshold*1e8)-1). Cross multiplication
	// preserves Rat.FloatString(8) semantics without SQL division double rounding.
	unknown := `COALESCE((NOT c.covered OR c.parent_summary_version IS DISTINCT FROM 1
		OR expected.total=0 OR amounts.total<>expected.total OR NOT amounts.versions_complete
		OR CASE WHEN $10::text='token' THEN amounts.token_amount IS NULL
			ELSE amounts.usd_value IS NULL OR NOT amounts.usd_complete
				OR ($10::text='percent' AND NOT $12::boolean) END),TRUE)`
	query := `
		WITH candidates AS MATERIALIZED (
			SELECT r.id AS ready_id,r.receipt_id,r.parent_event_id,parent.occurred_at,
				($3::bigint[] <@ scope.pool_ids AND $2=ANY(scope.token_addresses)) AS covered,
				ps.summary_version AS parent_summary_version,ps.pool_ids
			FROM market_trade_ready_events r
			JOIN market_transaction_receipts receipt ON receipt.id=r.receipt_id AND receipt.revision=r.receipt_revision
			JOIN market_trade_scopes scope ON scope.id=receipt.scope_id
			JOIN market_events parent ON parent.id=r.parent_event_id
			LEFT JOIN market_trade_parent_summaries ps ON ps.receipt_id=r.receipt_id AND ps.parent_event_id=r.parent_event_id
			WHERE r.chain_id=$1 AND r.token_address=$2 AND r.id<=$4 AND r.valid
				AND parent.event_type=$5 AND parent.occurred_at BETWEEN $6 AND $7
				AND receipt.complete AND receipt.confirmed AND NOT receipt.reorged
				AND parent.reorged=0 AND parent.confirmed=1
				AND parent.block_hash=receipt.block_hash AND parent.block_number=receipt.block_number
				AND EXISTS(SELECT 1 FROM market_scanned_blocks b WHERE b.chain_id=receipt.chain_id
					AND b.block_number=receipt.block_number AND b.block_hash=receipt.block_hash AND b.canonical=1)
				AND EXISTS(SELECT 1 FROM unnest($13::timestamptz[],$14::timestamptz[]) w(from_at,through_at)
					WHERE parent.occurred_at BETWEEN w.from_at AND w.through_at)
				AND (NOT ($3::bigint[] <@ scope.pool_ids AND $2=ANY(scope.token_addresses))
					OR (ps.parent_event_id IS NULL AND EXISTS(SELECT 1 FROM market_trade_legs legacy
						WHERE legacy.receipt_id=r.receipt_id AND legacy.parent_event_id=r.parent_event_id AND legacy.pool_id=ANY($3::bigint[])))
					OR EXISTS(SELECT 1 FROM unnest(ps.pool_ids) actual(pool_id) WHERE actual.pool_id=ANY($3::bigint[])))
			ORDER BY r.id LIMIT $8
		), projected AS MATERIALIZED (
			SELECT c.ready_id,c.parent_event_id,c.occurred_at,amounts.token_amount,amounts.usd_value,` + unknown + ` AS is_unknown
			FROM candidates c
			CROSS JOIN LATERAL(SELECT COUNT(*) AS total FROM unnest(c.pool_ids) actual(pool_id)
				WHERE actual.pool_id=ANY($3::bigint[])) expected
			CROSS JOIN LATERAL(
				SELECT COUNT(*) AS total,BOOL_AND(s.summary_version=1) AS versions_complete,
					CASE WHEN COUNT(s.token_amount)=COUNT(*) THEN SUM(s.token_amount) END AS token_amount,
					CASE WHEN COUNT(s.usd_value)=COUNT(*) THEN SUM(s.usd_value) END AS usd_value,
					BOOL_AND(s.usd_valuation_complete AND s.valuation_at=c.occurred_at) AS usd_complete
				FROM market_trade_pool_summaries s
				WHERE s.receipt_id=c.receipt_id AND s.parent_event_id=c.parent_event_id
					AND s.pool_id=ANY($3::bigint[]) AND s.pool_id=ANY(c.pool_ids)
			) amounts
		)
		SELECT ready_id AS trade_ready_id,parent_event_id AS parent_id,occurred_at,is_unknown AS unknown,
			CASE WHEN is_unknown THEN FALSE
				WHEN $10::text='token' THEN token_amount >= $9::numeric
				WHEN $10::text='percent' THEN usd_value*20000000000 >= $11::numeric*(2*CEIL($9::numeric*100000000)-1)
				ELSE usd_value >= $9::numeric END AS qualified
		FROM projected ORDER BY ready_id
	`
	values, err := collectMany[MarketTradeWindowCandidate](ctx, s.db, query, target.ChainID, address, pools, maxReadyID, direction, from, through, limit+1, rule.ThresholdValue, unit, liquidity, liquidityKnown, froms, throughs)
	if err != nil {
		return nil, fmt.Errorf("read compact complete trade window: %w", err)
	}
	if len(values) > limit {
		return nil, fmt.Errorf("%w: candidate_row_budget", ErrMarketTradeWindowUnavailable)
	}
	return values, nil
}
