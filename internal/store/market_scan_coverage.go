package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type MarketTradeScanWindow struct {
	From         time.Time
	Through      time.Time
	ThroughBlock int64
}

type RecordMarketScanCoverageParams struct {
	ChainID             int64
	CursorKey           string
	ParserVersion       int32
	ScopePoolIDs        []int64
	ScopeTokenAddresses []string
	FromBlock           int64
	ThroughBlock        int64
	AfterAt             time.Time
	ThroughAt           time.Time
	AfterHash           string
	ThroughHash         string
}

type marketScanCoverageRow struct {
	ID                 int64      `db:"id"`
	FromBlock          int64      `db:"from_block"`
	ThroughBlock       int64      `db:"through_block"`
	AfterAt            time.Time  `db:"after_at"`
	ThroughAt          time.Time  `db:"through_at"`
	AfterHash          string     `db:"after_hash"`
	ThroughHash        string     `db:"through_hash"`
	FactsRetainedAfter *time.Time `db:"facts_retained_after"`
}

// RecordMarketScanCoverage is called only after the entire RPC log range has
// been hydrated, canonical hashes stored, and bounded confirmation drained.
// Adjacent ranges under the same scope coalesce into one small record.
func (s *Store) RecordMarketScanCoverage(ctx context.Context, params RecordMarketScanCoverageParams) error {
	params.CursorKey = strings.TrimSpace(params.CursorKey)
	params.AfterHash, params.ThroughHash = strings.ToLower(params.AfterHash), strings.ToLower(params.ThroughHash)
	if params.ParserVersion == 0 {
		params.ParserVersion = 1
	}
	if params.ChainID <= 0 || params.CursorKey == "" || params.ParserVersion < 1 || params.FromBlock < 1 ||
		params.ThroughBlock < params.FromBlock || params.AfterAt.IsZero() || params.ThroughAt.Before(params.AfterAt) ||
		!marketHashPattern.MatchString(params.AfterHash) || !marketHashPattern.MatchString(params.ThroughHash) ||
		len(params.ScopePoolIDs) > 10000 || len(params.ScopeTokenAddresses) > 10000 {
		return ErrInvalidMarketCursor
	}
	pools := append([]int64(nil), params.ScopePoolIDs...)
	for _, id := range pools {
		if id < 1 {
			return ErrInvalidMarketPool
		}
	}
	pools = uniquePositiveIDs(pools)
	sort.Slice(pools, func(i, j int) bool { return pools[i] < pools[j] })
	if pools == nil {
		pools = []int64{}
	}
	tokenSet := make(map[string]struct{}, len(params.ScopeTokenAddresses))
	for _, value := range params.ScopeTokenAddresses {
		token, err := normalizeMarketAddress(value)
		if err != nil {
			return err
		}
		tokenSet[token] = struct{}{}
	}
	tokens := make([]string, 0, len(tokenSet))
	for token := range tokenSet {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	if len(tokens) == 0 {
		return ErrInvalidMarketCursor
	}
	scopeJSON, _ := json.Marshal(struct {
		Version int32    `json:"version"`
		Pools   []int64  `json:"pools"`
		Tokens  []string `json:"tokens"`
	}{params.ParserVersion, pools, tokens})
	digest := sha256.Sum256(scopeJSON)
	_, err := withTxValue(ctx, s.db, func(tx DBTX) (bool, error) {
		if err := lockMarketTradeChain(ctx, tx, params.ChainID); err != nil {
			return false, err
		}
		var canonical bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM market_scanned_blocks WHERE chain_id=$1 AND cursor_key=$2
			 AND block_number=$3 AND block_hash=$4 AND block_timestamp=$5 AND canonical=1)
			 AND EXISTS(SELECT 1 FROM market_scanned_blocks WHERE chain_id=$1 AND cursor_key=$2
			 AND block_number=$6 AND block_hash=$7 AND block_timestamp=$8 AND canonical=1)
		`, params.ChainID, params.CursorKey, params.FromBlock-1, params.AfterHash, params.AfterAt,
			params.ThroughBlock, params.ThroughHash, params.ThroughAt).Scan(&canonical); err != nil {
			return false, err
		}
		if !canonical {
			return false, fmt.Errorf("%w: scan coverage lacks canonical boundaries", ErrInvalidMarketCursor)
		}
		var pending bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM market_transaction_receipts r
			WHERE r.chain_id=$1 AND r.block_number BETWEEN $2 AND $3 AND r.complete AND NOT r.reorged AND NOT r.confirmed
			AND EXISTS(SELECT 1 FROM market_scanned_blocks b WHERE b.chain_id=r.chain_id AND b.block_number=r.block_number
			AND b.block_hash=r.block_hash AND b.canonical=1))`, params.ChainID, params.FromBlock, params.ThroughBlock).Scan(&pending); err != nil {
			return false, err
		}
		if pending {
			return false, ErrMarketTradeConfirmationPending
		}
		var scopeID int64
		key := hex.EncodeToString(digest[:])
		err := tx.QueryRow(ctx, `SELECT id FROM market_trade_scopes WHERE scope_key=$1 FOR KEY SHARE`, key).Scan(&scopeID)
		if isNoRows(err) {
			err = tx.QueryRow(ctx, `INSERT INTO market_trade_scopes(scope_key,parser_version,pool_ids,token_addresses)
			 VALUES($1,$2,$3,$4) ON CONFLICT(scope_key) DO NOTHING RETURNING id`, key, params.ParserVersion, pools, tokens).Scan(&scopeID)
			if isNoRows(err) {
				err = tx.QueryRow(ctx, `SELECT id FROM market_trade_scopes WHERE scope_key=$1 FOR KEY SHARE`, key).Scan(&scopeID)
			}
		}
		if err != nil {
			return false, err
		}
		previous, err := collectMany[marketScanCoverageRow](ctx, tx, `
			SELECT id,from_block,through_block,after_at,through_at,after_hash,through_hash,
			 CASE WHEN isfinite(facts_retained_after) THEN facts_retained_after END AS facts_retained_after
			FROM market_scan_coverage WHERE chain_id=$1 AND cursor_key=$2 AND scope_id=$3
			 AND from_block<=$5+1 AND through_block>=$4-1 ORDER BY from_block,id LIMIT 1001 FOR UPDATE
		`, params.ChainID, params.CursorKey, scopeID, params.FromBlock, params.ThroughBlock)
		if err != nil {
			return false, err
		}
		if len(previous) > 1000 {
			return false, fmt.Errorf("%w: scan coverage merge capacity exceeded", ErrInvalidMarketCursor)
		}
		merged := marketScanCoverageRow{FromBlock: params.FromBlock, ThroughBlock: params.ThroughBlock, AfterAt: params.AfterAt,
			ThroughAt: params.ThroughAt, AfterHash: params.AfterHash, ThroughHash: params.ThroughHash}
		ids := make([]int64, 0, len(previous))
		for _, old := range previous {
			ids = append(ids, old.ID)
			if old.FromBlock < merged.FromBlock {
				merged.FromBlock, merged.AfterAt, merged.AfterHash = old.FromBlock, old.AfterAt, old.AfterHash
			}
			if old.ThroughBlock > merged.ThroughBlock {
				merged.ThroughBlock, merged.ThroughAt, merged.ThroughHash = old.ThroughBlock, old.ThroughAt, old.ThroughHash
			}
			if old.FactsRetainedAfter != nil && (merged.FactsRetainedAfter == nil || old.FactsRetainedAfter.After(*merged.FactsRetainedAfter)) {
				value := *old.FactsRetainedAfter
				merged.FactsRetainedAfter = &value
			}
		}
		if len(ids) == 0 {
			_, err = tx.Exec(ctx, `INSERT INTO market_scan_coverage(chain_id,cursor_key,scope_id,from_block,through_block,
			 after_at,through_at,after_hash,through_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, params.ChainID, params.CursorKey, scopeID,
				merged.FromBlock, merged.ThroughBlock, merged.AfterAt, merged.ThroughAt, merged.AfterHash, merged.ThroughHash)
		} else {
			_, err = tx.Exec(ctx, `UPDATE market_scan_coverage SET from_block=$2,through_block=$3,after_at=$4,through_at=$5,
			 after_hash=$6,through_hash=$7,facts_retained_after=COALESCE($8::timestamptz,'-infinity'),verified_at=NOW() WHERE id=$1`,
				ids[0], merged.FromBlock, merged.ThroughBlock, merged.AfterAt, merged.ThroughAt, merged.AfterHash, merged.ThroughHash, merged.FactsRetainedAfter)
			if err == nil && len(ids) > 1 {
				_, err = tx.Exec(ctx, `DELETE FROM market_scan_coverage WHERE id=ANY($1::bigint[])`, ids[1:])
			}
		}
		return err == nil, err
	})
	if err != nil {
		return fmt.Errorf("record completed market scan coverage: %w", err)
	}
	return nil
}

func (s *Store) MarketTradeWindowScanCoverageComplete(ctx context.Context, target MarketRuleTarget, windows []MarketTradeScanWindow) (bool, error) {
	return marketTradeWindowScanCoverageComplete(ctx, s.db, target, windows)
}

func marketTradeWindowScanCoverageComplete(ctx context.Context, db DBTX, target MarketRuleTarget, windows []MarketTradeScanWindow) (bool, error) {
	states, err := marketTradeWindowScanCoverageStates(ctx, db, target, windows)
	if errors.Is(err, ErrMarketTradeWindowUnavailable) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, state := range states {
		if state != "" {
			return false, nil
		}
	}
	return true, nil
}

func marketTradeWindowScanCoverageStates(ctx context.Context, db DBTX, target MarketRuleTarget, windows []MarketTradeScanWindow) ([]string, error) {
	states := make([]string, len(windows))
	if len(windows) == 0 {
		return states, nil
	}
	if len(windows) > 2000 || target.ChainID <= 0 || len(target.TradePoolIDs) == 0 || len(target.TradePoolIDs) > 10000 {
		return nil, ErrInvalidMarketRule
	}
	token, err := normalizeMarketAddress(target.TokenAddress)
	if err != nil {
		return nil, err
	}
	minFrom, maxThrough := windows[0].From, windows[0].Through
	for _, window := range windows {
		if window.From.IsZero() || window.Through.Before(window.From) || window.ThroughBlock < 1 {
			return nil, ErrInvalidMarketRule
		}
		if window.From.Before(minFrom) {
			minFrom = window.From
		}
		if window.Through.After(maxThrough) {
			maxThrough = window.Through
		}
	}
	rows, err := collectMany[marketScanCoverageRow](ctx, db, `
	 SELECT c.id,c.from_block,c.through_block,c.after_at,c.through_at,c.after_hash,c.through_hash,
	 CASE WHEN isfinite(c.facts_retained_after) THEN c.facts_retained_after END AS facts_retained_after
	 FROM market_scan_coverage c JOIN market_trade_scopes scope ON scope.id=c.scope_id
	 WHERE c.chain_id=$1 AND scope.parser_version=1 AND $2=ANY(scope.token_addresses) AND $3::bigint[]<@scope.pool_ids
	 AND c.through_at>=$4 AND c.after_at<=$5
	 AND EXISTS(SELECT 1 FROM market_scanned_blocks b WHERE b.chain_id=c.chain_id AND b.cursor_key=c.cursor_key
	  AND b.block_number=c.from_block-1 AND b.block_hash=c.after_hash AND b.canonical=1)
	 AND EXISTS(SELECT 1 FROM market_scanned_blocks b WHERE b.chain_id=c.chain_id AND b.cursor_key=c.cursor_key
	  AND b.block_number=c.through_block AND b.block_hash=c.through_hash AND b.canonical=1)
	 ORDER BY c.from_block,c.through_block LIMIT 10001`, target.ChainID, token, uniquePositiveIDs(target.TradePoolIDs), minFrom, maxThrough)
	if err != nil {
		return nil, fmt.Errorf("read market window scan evidence: %w", err)
	}
	if len(rows) > 10000 {
		return nil, fmt.Errorf("%w: coverage_row_budget", ErrMarketTradeWindowUnavailable)
	}
	for index, window := range windows {
		covered := false
		var start, end int64
		var endAt time.Time
		for _, row := range rows {
			if row.FactsRetainedAfter != nil && window.From.Before(*row.FactsRetainedAfter) {
				continue
			}
			if !covered {
				if !window.From.After(row.AfterAt) || window.From.After(row.ThroughAt) {
					continue
				}
				covered, start, end, endAt = true, row.FromBlock, row.ThroughBlock, row.ThroughAt
			} else if row.FromBlock <= end+1 && row.ThroughBlock > end {
				end, endAt = row.ThroughBlock, row.ThroughAt
			}
			if covered && window.ThroughBlock >= start && end >= window.ThroughBlock && !endAt.Before(window.Through) {
				break
			}
		}
		if !covered || window.ThroughBlock < start || end < window.ThroughBlock || endAt.Before(window.Through) {
			states[index] = "scan_coverage_incomplete"
		}
	}
	// Larger scan declarations cannot expand an immutable completed receipt.
	// Missing token/direction facts must not become a proven zero. Check only
	// these windows, so old narrow receipts cannot block later complete windows.
	froms, throughs := make([]time.Time, len(windows)), make([]time.Time, len(windows))
	blocks := make([]int64, len(windows))
	for i, window := range windows {
		froms[i], throughs[i], blocks[i] = window.From, window.Through, window.ThroughBlock
	}
	type receiptState struct {
		Index     int64 `db:"index"`
		Transient bool  `db:"transient"`
		Narrow    bool  `db:"narrow"`
	}
	receipts, err := collectMany[receiptState](ctx, db, `SELECT w.ordinality::bigint AS index,
  bool_or(NOT r.complete OR NOT r.confirmed OR scope.parser_version<>1) AS transient,
  bool_or(NOT COALESCE(($3::bigint[]<@scope.pool_ids AND $2=ANY(scope.token_addresses)),FALSE)) AS narrow
 FROM unnest($6::timestamptz[],$7::timestamptz[],$8::bigint[]) WITH ORDINALITY w(from_at,through_at,through_block,ordinality)
 JOIN market_transaction_receipts r ON r.chain_id=$1 AND NOT r.reorged
  AND r.occurred_at BETWEEN w.from_at AND w.through_at AND r.block_number<=w.through_block
 JOIN market_trade_scopes scope ON scope.id=r.scope_id
 WHERE r.occurred_at BETWEEN $4 AND $5
 AND (NOT r.complete OR NOT r.confirmed OR scope.parser_version<>1
  OR NOT COALESCE(($3::bigint[]<@scope.pool_ids AND $2=ANY(scope.token_addresses)),FALSE))
 AND EXISTS(SELECT 1 FROM market_scanned_blocks b WHERE b.chain_id=r.chain_id AND b.block_number=r.block_number
  AND b.block_hash=r.block_hash AND b.canonical=1)
 GROUP BY w.ordinality`, target.ChainID, token, uniquePositiveIDs(target.TradePoolIDs), minFrom, maxThrough, froms, throughs, blocks)
	if err != nil {
		return nil, fmt.Errorf("verify captured receipt window scope: %w", err)
	}
	for _, receipt := range receipts {
		if receipt.Transient {
			return nil, fmt.Errorf("%w: pending_or_invalid_receipt", ErrMarketTradeWindowUnavailable)
		}
		if receipt.Narrow && receipt.Index > 0 && receipt.Index <= int64(len(states)) {
			states[receipt.Index-1] = "historical_scope_unavailable"
		}
	}
	return states, nil
}

// Called under the existing same-chain transaction lock. A reorg trims only to
// a verified ancestor; if its timestamp is unknown, discard the affected claim.
func rewindMarketScanCoverage(ctx context.Context, db DBTX, chainID int64, cursorKey string, ancestor int64) error {
	var at *time.Time
	var hash *string
	err := db.QueryRow(ctx, `SELECT block_timestamp,block_hash FROM market_scanned_blocks WHERE chain_id=$1
	 AND ($2='' OR cursor_key=$2) AND block_number=$3 AND canonical=1 ORDER BY id DESC LIMIT 1`, chainID, cursorKey, ancestor).Scan(&at, &hash)
	if err != nil && !isNoRows(err) {
		return err
	}
	if at != nil && hash != nil {
		if _, err := db.Exec(ctx, `UPDATE market_scan_coverage SET through_block=$3,through_at=$4,through_hash=$5,
		 verified_at=NOW() WHERE chain_id=$1 AND ($2='' OR cursor_key=$2) AND from_block<=$3 AND through_block>$3`, chainID, cursorKey, ancestor, *at, *hash); err != nil {
			return err
		}
		_, err = db.Exec(ctx, `DELETE FROM market_scan_coverage WHERE chain_id=$1 AND ($2='' OR cursor_key=$2) AND from_block>$3`, chainID, cursorKey, ancestor)
	} else {
		_, err = db.Exec(ctx, `DELETE FROM market_scan_coverage WHERE chain_id=$1 AND ($2='' OR cursor_key=$2) AND through_block>$3`, chainID, cursorKey, ancestor)
	}
	return err
}
