package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// LatestMarketTradeReadyID is an explicit initialization fence. It never creates
// target state, changes a rule, or advances a legacy event watermark.
func (s *Store) LatestMarketTradeReadyID(ctx context.Context, target MarketRuleTarget) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE((SELECT id FROM market_trade_ready_events
		WHERE chain_id=$1 AND token_address=$2 AND valid ORDER BY id DESC LIMIT 1),0)
	`, target.ChainID, strings.ToLower(target.TokenAddress)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("read market trade initialization fence: %w", err)
	}
	return id, nil
}

func (s *Store) InitializeTradeRuleState(ctx context.Context, ruleID int64, target MarketRuleTarget, state json.RawMessage) (bool, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (bool, error) {
		writer := newWithDB(tx)
		var id, projectID int64
		var poolScope string
		if err := tx.QueryRow(ctx, `SELECT id,market_project_id,pool_scope FROM market_rules WHERE id=$1 FOR UPDATE`, ruleID).Scan(&id, &projectID, &poolScope); err != nil {
			return false, err
		}
		var lockedProject int64
		if err := tx.QueryRow(ctx, `SELECT id FROM market_projects WHERE id=$1 FOR UPDATE`, projectID).Scan(&lockedProject); err != nil {
			return false, err
		}
		created, err := writer.initializeTradeRuleState(ctx, ruleID, target, state)
		if err != nil || !created {
			return created, err
		}
		sourceFingerprint, _, err := readMarketTradeScopeSource(ctx, tx, projectID, poolScope, target)
		if err != nil {
			return false, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO market_trade_rule_readiness(rule_id,target_key,scope_fingerprint,activation_origin,scope_source_fingerprint)
    VALUES($1,$2,$3,'explicit_review',$4)`, ruleID, target.TargetKey, tradeScopeFingerprint(target), sourceFingerprint)
		return created, err
	})
}

func (s *Store) initializeTradeRuleState(ctx context.Context, ruleID int64, target MarketRuleTarget, state json.RawMessage) (bool, error) {
	var object map[string]json.RawMessage
	var readyID int64
	var fence int64
	if ruleID < 1 || !strings.HasSuffix(target.TargetKey, "/trades:v2") ||
		json.Unmarshal(state, &object) != nil || object == nil ||
		json.Unmarshal(object["last_trade_ready_id"], &readyID) != nil || readyID < 0 ||
		json.Unmarshal(object["activation_fence"], &fence) != nil || fence < 0 || fence != readyID {
		return false, ErrInvalidMarketRule
	}
	base := strings.TrimSuffix(target.TargetKey, "/trades:v2")
	parts := strings.Split(base, ":")
	if len(parts) != 2 || parts[0] != "deployment" && parts[0] != "legacy" {
		return false, ErrInvalidMarketRule
	}
	baseID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || baseID <= 0 || parts[0] == "deployment" && (target.MarketProjectDeploymentID == nil || *target.MarketProjectDeploymentID != baseID) {
		return false, ErrInvalidMarketRule
	}
	tag, err := s.db.Exec(ctx, `
		INSERT INTO market_rule_target_states (
			market_rule_id,target_key,market_project_deployment_id,market_project_pool_id,state,last_evaluated_at,last_triggered_at
		)
		SELECT mr.id,$2,$3,NULL,$4,NOW(),(
			SELECT MAX(previous.last_triggered_at) FROM market_rule_target_states previous
			WHERE previous.market_rule_id=mr.id AND starts_with(previous.target_key,$5||'/pool:')
		) FROM market_rules mr
		WHERE mr.id=$1 AND ($3::bigint IS NULL OR EXISTS (
			SELECT 1 FROM market_project_deployments d WHERE d.id=$3 AND d.market_project_id=mr.market_project_id
		)) AND ($6='deployment' OR mr.market_project_id=$7)
		ON CONFLICT (market_rule_id,target_key) DO NOTHING
	`, ruleID, target.TargetKey, target.MarketProjectDeploymentID, state, base, parts[0], baseID)
	if err != nil {
		return false, fmt.Errorf("initialize explicit market trade state: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ListMarketTradeEventsForTarget pages complete published parents, then sums all
// selected legs. LIMIT applies to ready parents, never to a transaction's legs.
func (s *Store) ListMarketTradeEventsForTarget(ctx context.Context, target MarketRuleTarget, afterReadyID int64, ascending bool, limit int) ([]MarketEvent, error) {
	address, err := normalizeMarketAddress(target.TokenAddress)
	if err != nil || target.ChainID <= 0 || afterReadyID < 0 {
		return nil, ErrInvalidMarketRule
	}
	pools := uniquePositiveIDs(target.TradePoolIDs)
	if len(pools) == 0 && target.MarketPoolID != nil && *target.MarketPoolID > 0 {
		pools = []int64{*target.MarketPoolID}
	}
	if len(pools) == 0 || len(pools) > 10000 || len(target.TradePoolIDs) > 10000 {
		return nil, ErrInvalidMarketRule
	}
	limit = clamp(limit, 1, 2000)
	if maximum := max(1, 10000/len(pools)); limit > maximum {
		limit = maximum
	}
	order, comparison := "DESC", "<"
	if ascending {
		order, comparison = "ASC", ">"
	}
	query := `
		WITH RECURSIVE candidates AS MATERIALIZED (
			SELECT r.id AS ready_id,r.receipt_id,r.parent_event_id,
			       ($3::bigint[] <@ scope.pool_ids AND $2=ANY(scope.token_addresses)) AS covered
			FROM market_trade_ready_events r
			JOIN market_transaction_receipts receipt ON receipt.id=r.receipt_id AND receipt.revision=r.receipt_revision
			JOIN market_trade_scopes scope ON scope.id=receipt.scope_id
			JOIN market_events parent ON parent.id=r.parent_event_id
			WHERE r.chain_id=$1 AND r.token_address=$2 AND r.valid
			  AND receipt.complete AND receipt.confirmed AND NOT receipt.reorged
			  AND parent.reorged=0 AND parent.block_hash=receipt.block_hash
			  AND parent.block_number=receipt.block_number
			  AND ($4::bigint=0 OR r.id ` + comparison + ` $4)
			  AND EXISTS(SELECT 1 FROM market_scanned_blocks b WHERE b.chain_id=receipt.chain_id
			    AND b.block_number=receipt.block_number AND b.block_hash=receipt.block_hash AND b.canonical=1)
			  AND (NOT ($3::bigint[] <@ scope.pool_ids AND $2=ANY(scope.token_addresses))
			    OR EXISTS(SELECT 1 FROM market_trade_legs l WHERE l.receipt_id=r.receipt_id
			      AND l.parent_event_id=r.parent_event_id AND l.pool_id=ANY($3::bigint[])))
			ORDER BY r.id ` + order + ` LIMIT $5
		), ranked AS MATERIALIZED (
			SELECT candidates.*,ROW_NUMBER() OVER(ORDER BY ready_id ` + order + `) AS ordinal FROM candidates
		), picked AS (
			SELECT c.*,cost.legs AS selected_legs FROM ranked c
			CROSS JOIN LATERAL(SELECT COUNT(*) AS legs FROM market_trade_legs l
			  WHERE l.receipt_id=c.receipt_id AND l.parent_event_id=c.parent_event_id AND l.pool_id=ANY($3::bigint[])) cost
			WHERE c.ordinal=1
			UNION ALL
			SELECT c.*,previous.selected_legs+cost.legs FROM picked previous
			JOIN ranked c ON c.ordinal=previous.ordinal+1
			CROSS JOIN LATERAL(SELECT COUNT(*) AS legs FROM market_trade_legs l
			  WHERE l.receipt_id=c.receipt_id AND l.parent_event_id=c.parent_event_id AND l.pool_id=ANY($3::bigint[])) cost
			WHERE previous.selected_legs<10000 AND previous.selected_legs+cost.legs<=10000
		), liquidity AS MATERIALIZED (
			SELECT COUNT(p.id)=cardinality($3::bigint[]) AND COUNT(s.id)=cardinality($3::bigint[])
			           AND COALESCE(BOOL_AND(s.liquidity_usd IS NOT NULL),FALSE) AS complete,
			       CASE WHEN COUNT(p.id)=cardinality($3::bigint[]) AND COUNT(s.liquidity_usd)=cardinality($3::bigint[])
			         THEN SUM(s.liquidity_usd)::text END AS usd,
			       COALESCE(array_agg(s.id ORDER BY requested.id) FILTER(WHERE s.id IS NOT NULL),ARRAY[]::bigint[]) AS snapshot_ids,
			       COALESCE(jsonb_agg(jsonb_build_object('id',s.id,'pool_id',requested.id,'captured_at',s.captured_at,'quote_quality',s.quote_quality)
			         ORDER BY requested.id) FILTER(WHERE s.id IS NOT NULL),'[]'::jsonb) AS snapshots
			FROM unnest($3::bigint[]) requested(id)
			LEFT JOIN market_pools p ON p.id=requested.id AND p.chain_id=$1
			  AND (p.token0_address=$2 OR p.token1_address=$2)
			LEFT JOIN LATERAL (
				SELECT id,liquidity_usd,captured_at,quote_quality FROM market_snapshots
				WHERE market_pool_id=p.id AND chain_id=$1 AND token_address=$2
				ORDER BY captured_at DESC,id DESC LIMIT 1
			) s ON TRUE
		)
		SELECT e.id,
		       CASE WHEN picked.covered AND cardinality(a.pool_ids)=1 THEN a.pool_ids[1] END AS market_pool_id,
		       e.market_asset_deployment_id,e.chain_key,e.chain_id,e.token_address,
		       e.event_type,e.event_key,e.transaction_hash,e.transaction_index,e.log_index,
		       e.block_number,e.block_hash,e.wallet_address,
		       CASE WHEN picked.covered THEN a.token_raw END AS token_amount_raw,
		       CASE WHEN picked.covered THEN a.quote_raw END AS quote_amount_raw,
		       CASE WHEN picked.covered THEN a.token_amount END AS token_amount,
		       CASE WHEN picked.covered THEN a.quote_amount END AS quote_amount,
		       CASE WHEN picked.covered THEN a.usd END AS usd_value,
		       CASE WHEN picked.covered THEN a.price END AS price_usd,
		       e.source,e.confidence::text AS confidence,e.confirmed,e.reorged,e.occurred_at,e.observed_at,'{}'::jsonb AS raw_payload,
		       jsonb_strip_nulls(jsonb_build_object(
		         'adapter',e.metadata->'adapter','protocol',e.metadata->'protocol',
		         'protocol_version',e.metadata->'protocol_version','recipient_address',e.metadata->'recipient_address',
		         'quote_address',e.metadata->'quote_address','trade_fact_version',e.metadata->'trade_fact_version',
		         'valuation_method',e.metadata->'valuation_method','price_impact_percent',e.metadata->'price_impact_percent'
		       )) || jsonb_build_object('trade_projection',jsonb_build_object(
		         'schema_version',1,'ready_id',picked.ready_id,'receipt_id',picked.receipt_id,
		         'pool_ids',COALESCE(a.pool_ids,ARRAY[]::bigint[]),'selected_pool_ids',$3::bigint[],
		         'source_leg_ids',COALESCE(a.leg_ids,ARRAY[]::bigint[]),
		         'token_amount_raw',CASE WHEN picked.covered THEN a.token_raw END,
		         'quote_amount_raw',CASE WHEN picked.covered THEN a.quote_raw END,
		         'token_amount',CASE WHEN picked.covered THEN a.token_amount END,
		         'quote_amount',CASE WHEN picked.covered THEN a.quote_amount END,
		         'usd_value',CASE WHEN picked.covered THEN a.usd END,
		         'price_usd',CASE WHEN picked.covered THEN a.price END,
		         'quote_address',CASE WHEN picked.covered THEN a.quote_address END,
		         'valuation_metadata',COALESCE(a.valuation_metadata,'[]'::jsonb),
		         'liquidity_usd',liquidity.usd,'liquidity_complete',liquidity.complete,
		         'snapshot_ids',liquidity.snapshot_ids,'liquidity_snapshots',liquidity.snapshots
		       )) || CASE WHEN picked.covered THEN '{}'::jsonb
		         ELSE jsonb_build_object('trade_projection_unavailable','coverage_incomplete') END AS metadata,
		       picked.ready_id AS trade_ready_id,COALESCE(a.pool_ids,ARRAY[]::bigint[]) AS trade_pool_ids
		FROM picked JOIN market_events e ON e.id=picked.parent_event_id
		CROSS JOIN liquidity
		LEFT JOIN LATERAL (
			SELECT array_agg(DISTINCT pool_id ORDER BY pool_id) AS pool_ids,
			       array_agg(id ORDER BY log_index,id) AS leg_ids,
			       SUM(token_amount_raw)::text AS token_raw,
			       CASE WHEN COUNT(quote_address)=COUNT(*) AND COUNT(DISTINCT quote_address)=1
			         THEN MIN(quote_address) END AS quote_address,
			       CASE WHEN COUNT(quote_address)=COUNT(*) AND COUNT(DISTINCT quote_address)=1 AND COUNT(quote_amount_raw)=COUNT(*)
			         THEN SUM(quote_amount_raw)::text END AS quote_raw,
			       CASE WHEN COUNT(token_amount)=COUNT(*) THEN SUM(token_amount)::text END AS token_amount,
			       CASE WHEN COUNT(quote_address)=COUNT(*) AND COUNT(DISTINCT quote_address)=1 AND COUNT(quote_amount)=COUNT(*)
			         THEN SUM(quote_amount)::text END AS quote_amount,
			       CASE WHEN COUNT(usd_value)=COUNT(*) THEN SUM(usd_value)::text END AS usd,
			       CASE WHEN COUNT(token_amount)=COUNT(*) AND COUNT(usd_value)=COUNT(*) AND COUNT(price_usd)=COUNT(*) AND SUM(token_amount)>0
			         THEN (SUM(usd_value)/SUM(token_amount))::text END AS price,
			       jsonb_agg(metadata ORDER BY log_index,id) AS valuation_metadata
			FROM market_trade_legs WHERE receipt_id=picked.receipt_id AND parent_event_id=picked.parent_event_id
			  AND pool_id=ANY($3::bigint[])
		) a ON TRUE
		ORDER BY picked.ready_id ` + order
	events, err := collectMany[MarketEvent](ctx, s.db, query, target.ChainID, address, pools, afterReadyID, limit)
	if err != nil {
		return nil, fmt.Errorf("project complete selected-pool market trades: %w", err)
	}
	return events, nil
}

// MarketTradeWindowContextComplete checks the entire conservative event-time
// range, not the time or length of one ready-ID page. Ready order is publication
// order; a delayed historical receipt can have a larger ID than a newer trade.
// The check returns no facts or metadata, and never advances state on a gap.
func (s *Store) MarketTradeWindowContextComplete(ctx context.Context, target MarketRuleTarget, lastReadyID int64, from, to time.Time, historicalReadyIDs []int64) (bool, error) {
	address, err := normalizeMarketAddress(target.TokenAddress)
	if err != nil || target.ChainID <= 0 || lastReadyID < 0 || from.IsZero() || to.Before(from) || len(historicalReadyIDs) > 10000 || len(target.TradePoolIDs) > 10000 {
		return false, ErrInvalidMarketRule
	}
	pools := uniquePositiveIDs(target.TradePoolIDs)
	if len(pools) == 0 && target.MarketPoolID != nil && *target.MarketPoolID > 0 {
		pools = []int64{*target.MarketPoolID}
	}
	if len(pools) == 0 {
		return false, ErrInvalidMarketRule
	}
	known := uniquePositiveIDs(historicalReadyIDs)
	if known == nil {
		known = []int64{}
	}
	var complete bool
	err = s.db.QueryRow(ctx, `
		SELECT NOT EXISTS (
			SELECT 1 FROM market_trade_ready_events r
			JOIN market_transaction_receipts receipt ON receipt.id=r.receipt_id AND receipt.revision=r.receipt_revision
			JOIN market_trade_scopes scope ON scope.id=receipt.scope_id
			JOIN market_events parent ON parent.id=r.parent_event_id
			WHERE r.chain_id=$1 AND r.token_address=$2 AND r.id<=$4 AND r.valid
			  AND receipt.complete AND receipt.confirmed AND NOT receipt.reorged
			  AND parent.reorged=0 AND parent.block_hash=receipt.block_hash AND parent.block_number=receipt.block_number
			  AND parent.occurred_at BETWEEN $5 AND $6 AND NOT (r.id=ANY($7::bigint[]))
			  AND EXISTS(SELECT 1 FROM market_scanned_blocks b WHERE b.chain_id=receipt.chain_id
			    AND b.block_number=receipt.block_number AND b.block_hash=receipt.block_hash AND b.canonical=1)
			  AND (NOT ($3::bigint[] <@ scope.pool_ids AND $2=ANY(scope.token_addresses))
			    OR EXISTS(SELECT 1 FROM market_trade_legs l WHERE l.receipt_id=r.receipt_id
			      AND l.parent_event_id=r.parent_event_id AND l.pool_id=ANY($3::bigint[])))
		)
	`, target.ChainID, address, pools, lastReadyID, from.UTC(), to.UTC(), known).Scan(&complete)
	if err != nil {
		return false, fmt.Errorf("verify complete market trade window context: %w", err)
	}
	return complete, nil
}
