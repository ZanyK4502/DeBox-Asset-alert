package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrMarketTradeConfirmationPending = errors.New("market trade confirmation batch has remaining verified receipts")

// MarketTransactionParams is one fully hydrated and parsed receipt. Network
// retrieval and valuation happen before this atomic, chain-serialized write.
type MarketTransactionParams struct {
	ChainKey            string
	ChainID             int64
	TransactionHash     string
	BlockNumber         int64
	BlockHash           string
	TransactionIndex    int32
	OccurredAt          time.Time
	Confirmed           bool
	ParserVersion       int32
	ScopePoolIDs        []int64
	ScopeTokenAddresses []string
	ReceiptPayload      json.RawMessage
	Events              []MarketTransactionEvent
}

type MarketTransactionEvent struct {
	Event CreateMarketEventParams
	Legs  []MarketTradeLegParams
}

type MarketTradeLegParams struct {
	PoolID         int64
	LogIndex       int64
	QuoteAddress   string
	TokenAmountRaw string
	QuoteAmountRaw string
	TokenAmount    *string
	QuoteAmount    *string
	USDValue       *string
	PriceUSD       *string
	Metadata       json.RawMessage
}

// PersistMarketTransaction writes all logical parents and per-pool facts before
// publishing any of them. A committed receipt is immutable on subsequent reads.
func (s *Store) PersistMarketTransaction(ctx context.Context, params MarketTransactionParams) ([]MarketEvent, error) {
	return s.persistMarketTransaction(ctx, params)
}

func lockMarketTradeChain(ctx context.Context, db DBTX, chainID int64) error {
	_, err := db.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", fmt.Sprintf("debox-market-trade-chain:%d", chainID))
	if err != nil {
		return fmt.Errorf("lock market trade chain: %w", err)
	}
	return nil
}

type marketReceiptRecord struct {
	ID               int64 `db:"id"`
	BlockNumber      int64 `db:"block_number"`
	TransactionIndex int32 `db:"transaction_index"`
	Complete         bool  `db:"complete"`
	Confirmed        bool  `db:"confirmed"`
	Reorged          bool  `db:"reorged"`
	Revision         int32 `db:"revision"`
}

func (s *Store) persistMarketTransaction(ctx context.Context, params MarketTransactionParams) ([]MarketEvent, error) {
	if err := normalizeMarketTransaction(&params); err != nil {
		return nil, err
	}
	return withTxValue(ctx, s.db, func(tx DBTX) ([]MarketEvent, error) {
		if err := lockMarketTradeChain(ctx, tx, params.ChainID); err != nil {
			return nil, err
		}
		var canonicalKnown, canonicalConflict bool
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*)>0,COALESCE(BOOL_OR(block_hash<>$3),FALSE)
			FROM market_scanned_blocks WHERE chain_id=$1 AND block_number=$2 AND canonical=1
		`, params.ChainID, params.BlockNumber, params.BlockHash).Scan(&canonicalKnown, &canonicalConflict); err != nil {
			return nil, fmt.Errorf("check receipt canonical inclusion: %w", err)
		}
		if canonicalConflict {
			return nil, fmt.Errorf("%w: receipt conflicts with current canonical block", ErrInvalidMarketEvent)
		}
		existing, err := collectOptional[marketReceiptRecord](ctx, tx, `
			SELECT id, block_number, transaction_index, complete, confirmed, reorged, revision
			FROM market_transaction_receipts
			WHERE chain_id = $1 AND transaction_hash = $2 AND block_hash = $3
			FOR UPDATE
		`, params.ChainID, params.TransactionHash, params.BlockHash)
		if err != nil {
			return nil, fmt.Errorf("get complete market receipt: %w", err)
		}
		if existing != nil {
			if !existing.Complete || existing.BlockNumber != params.BlockNumber ||
				existing.TransactionIndex != params.TransactionIndex {
				return nil, fmt.Errorf("%w: conflicting receipt inclusion", ErrInvalidMarketEvent)
			}
			// A later parser scope cannot mutate a published transaction or replay
			// its parents. Coverage remains the scope of this original full parse.
			if existing.Reorged {
				if !canonicalKnown {
					return nil, fmt.Errorf("%w: re-inclusion lacks canonical evidence", ErrInvalidMarketEvent)
				}
				if err := restoreMarketReceipt(ctx, tx, existing.ID, params); err != nil {
					return nil, err
				}
			}
			if params.Confirmed && (!existing.Confirmed || existing.Reorged) {
				if err := confirmMarketReceipt(ctx, tx, existing.ID); err != nil {
					return nil, err
				}
			}
			return marketReceiptEvents(ctx, tx, existing.ID)
		}
		if canonicalKnown {
			if err := retireOtherMarketInclusions(ctx, tx, params); err != nil {
				return nil, err
			}
		}
		if err := validateMarketTradePools(ctx, tx, params); err != nil {
			return nil, err
		}
		scopeJSON, _ := json.Marshal(struct {
			Version int32    `json:"version"`
			Pools   []int64  `json:"pools"`
			Tokens  []string `json:"tokens"`
		}{params.ParserVersion, params.ScopePoolIDs, params.ScopeTokenAddresses})
		digest := sha256.Sum256(scopeJSON)
		var scopeID int64
		scopeKey := hex.EncodeToString(digest[:])
		err = tx.QueryRow(ctx, `SELECT id FROM market_trade_scopes WHERE scope_key=$1 FOR KEY SHARE`, scopeKey).Scan(&scopeID)
		if isNoRows(err) {
			err = tx.QueryRow(ctx, `
				INSERT INTO market_trade_scopes (scope_key, parser_version, pool_ids, token_addresses)
				VALUES ($1, $2, $3, $4) ON CONFLICT (scope_key) DO NOTHING RETURNING id
			`, scopeKey, params.ParserVersion, params.ScopePoolIDs, params.ScopeTokenAddresses).Scan(&scopeID)
			if isNoRows(err) {
				err = tx.QueryRow(ctx, `SELECT id FROM market_trade_scopes WHERE scope_key=$1 FOR KEY SHARE`, scopeKey).Scan(&scopeID)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("save market parser scope: %w", err)
		}
		var receiptID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO market_transaction_receipts (
				chain_key, chain_id, transaction_hash, block_number, block_hash,
				transaction_index, occurred_at, scope_id, receipt_payload
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			RETURNING id
		`, params.ChainKey, params.ChainID, params.TransactionHash, params.BlockNumber,
			params.BlockHash, params.TransactionIndex, params.OccurredAt, scopeID, params.ReceiptPayload).Scan(&receiptID); err != nil {
			return nil, fmt.Errorf("save market receipt: %w", err)
		}
		legs := make([]marketTradeLegRow, 0)
		savedEvents := make([]MarketEvent, 0, len(params.Events))
		for _, item := range params.Events {
			var event MarketEvent
			if len(item.Legs) > 0 {
				event, err = saveMarketTradeParent(ctx, tx, item.Event, canonicalKnown)
			} else {
				event, _, err = createMarketEvent(ctx, tx, item.Event)
			}
			if err != nil {
				return nil, err
			}
			savedEvents = append(savedEvents, event)
			if _, err := tx.Exec(ctx, `INSERT INTO market_transaction_events (receipt_id, market_event_id) VALUES ($1,$2)`, receiptID, event.ID); err != nil {
				return nil, fmt.Errorf("link receipt event: %w", err)
			}
			for _, leg := range item.Legs {
				legs = append(legs, marketTradeLegRow{
					ParentID: event.ID, PoolID: leg.PoolID, LogIndex: leg.LogIndex,
					QuoteAddress: nullableTradeString(leg.QuoteAddress), TokenAmountRaw: leg.TokenAmountRaw,
					QuoteAmountRaw: nullableTradeString(leg.QuoteAmountRaw), TokenAmount: leg.TokenAmount,
					QuoteAmount: leg.QuoteAmount, USDValue: leg.USDValue, PriceUSD: leg.PriceUSD,
					Metadata: normalizedJSON(leg.Metadata),
				})
			}
		}
		if len(legs) > 0 {
			encoded, err := json.Marshal(legs)
			if err != nil {
				return nil, fmt.Errorf("encode market trade facts: %w", err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO market_trade_legs (
					receipt_id,parent_event_id,pool_id,log_index,quote_address,
					token_amount_raw,quote_amount_raw,token_amount,quote_amount,usd_value,price_usd,metadata
				)
				SELECT $1,x.parent_id,x.pool_id,x.log_index,x.quote_address,
				       x.token_amount_raw,x.quote_amount_raw,x.token_amount,x.quote_amount,x.usd_value,x.price_usd,x.metadata
				FROM jsonb_to_recordset($2::jsonb) AS x(
					parent_id bigint,pool_id bigint,log_index bigint,quote_address text,
					token_amount_raw numeric,quote_amount_raw numeric,token_amount numeric,quote_amount numeric,
					usd_value numeric,price_usd numeric,metadata jsonb
				)
			`, receiptID, encoded); err != nil {
				return nil, fmt.Errorf("save atomic market trade legs: %w", err)
			}
		}
		if err := persistMarketTradeSummaries(ctx, tx, receiptID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE market_transaction_receipts SET complete=TRUE,completed_at=NOW() WHERE id=$1`, receiptID); err != nil {
			return nil, fmt.Errorf("complete market receipt: %w", err)
		}
		if err := reconcileFourMemeEventTokens(ctx, tx, savedEvents); err != nil {
			return nil, err
		}
		if params.Confirmed {
			if err := confirmMarketReceipt(ctx, tx, receiptID); err != nil {
				return nil, err
			}
		}
		return marketReceiptEvents(ctx, tx, receiptID)
	})
}

type marketTradeLegRow struct {
	ParentID       int64           `json:"parent_id"`
	PoolID         int64           `json:"pool_id"`
	LogIndex       int64           `json:"log_index"`
	QuoteAddress   *string         `json:"quote_address"`
	TokenAmountRaw string          `json:"token_amount_raw"`
	QuoteAmountRaw *string         `json:"quote_amount_raw"`
	TokenAmount    *string         `json:"token_amount"`
	QuoteAmount    *string         `json:"quote_amount"`
	USDValue       *string         `json:"usd_value"`
	PriceUSD       *string         `json:"price_usd"`
	Metadata       json.RawMessage `json:"metadata"`
}

func nullableTradeString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func saveMarketTradeParent(ctx context.Context, tx DBTX, params CreateMarketEventParams, canonicalKnown bool) (MarketEvent, error) {
	var parentID int64
	err := tx.QueryRow(ctx, `SELECT parent_event_id FROM market_trade_parents WHERE chain_id=$1 AND transaction_hash=$2 AND token_address=$3 AND direction=$4`,
		params.ChainID, *params.TransactionHash, params.TokenAddress, params.EventType).Scan(&parentID)
	if err != nil && !isNoRows(err) {
		return MarketEvent{}, fmt.Errorf("find logical market trade parent: %w", err)
	}
	if isNoRows(err) {
		// Old parser keys included the selected log set. Reuse a unique logical
		// parent without changing its amounts, key, representative log or ID.
		candidates, err := collectMany[MarketEvent](ctx, tx, `
			SELECT `+marketEventColumns+` FROM market_events
			WHERE chain_id=$1 AND token_address=$2 AND transaction_hash=$3
			  AND event_type=$4 AND log_index IS NOT NULL
			  AND COALESCE(metadata->>'adapter','') NOT IN ('four_meme_v1','four_meme_v2')
			ORDER BY id LIMIT 2
		`, params.ChainID, params.TokenAddress, *params.TransactionHash, params.EventType)
		if err != nil {
			return MarketEvent{}, fmt.Errorf("find legacy trade parent: %w", err)
		}
		if len(candidates) > 1 {
			return MarketEvent{}, fmt.Errorf("%w: ambiguous legacy trade parents", ErrInvalidMarketEvent)
		}
		if len(candidates) == 1 {
			parentID = candidates[0].ID
		} else {
			params.LogIndex = nil
			params.EventKey = "trade:v2:" + *params.TransactionHash + ":" + params.TokenAddress + ":" + params.EventType
			// A logical total can exceed one uint256/NUMERIC(78,0), although
			// every individual on-chain leg is valid. Preserve it as exact text
			// and let the unbounded NUMERIC projection sum the immutable legs.
			if params.TokenAmountRaw != nil && len(*params.TokenAmountRaw) > 78 {
				var metadata map[string]json.RawMessage
				_ = json.Unmarshal(normalizedJSON(params.Metadata), &metadata)
				if metadata == nil {
					metadata = make(map[string]json.RawMessage)
				}
				metadata["trade_original_token_amount_raw"], _ = json.Marshal(*params.TokenAmountRaw)
				params.Metadata, _ = json.Marshal(metadata)
				params.TokenAmountRaw = nil
			}
			if params.QuoteAmountRaw != nil && len(*params.QuoteAmountRaw) > 78 {
				var metadata map[string]json.RawMessage
				_ = json.Unmarshal(normalizedJSON(params.Metadata), &metadata)
				if metadata == nil {
					metadata = make(map[string]json.RawMessage)
				}
				metadata["trade_original_quote_amount_raw"], _ = json.Marshal(*params.QuoteAmountRaw)
				params.Metadata, _ = json.Marshal(metadata)
				params.QuoteAmountRaw = nil
			}
			event, _, err := createMarketEvent(ctx, tx, params)
			if err != nil {
				return MarketEvent{}, err
			}
			if event.EventType != params.EventType || event.TokenAddress != params.TokenAddress || event.TransactionHash == nil || *event.TransactionHash != *params.TransactionHash {
				return MarketEvent{}, fmt.Errorf("%w: conflicting logical trade parent", ErrInvalidMarketEvent)
			}
			parentID = event.ID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO market_trade_parents(parent_event_id,chain_id,transaction_hash,token_address,direction) VALUES($1,$2,$3,$4,$5)`,
			parentID, params.ChainID, *params.TransactionHash, params.TokenAddress, params.EventType); err != nil {
			return MarketEvent{}, fmt.Errorf("map logical market trade parent: %w", err)
		}
	}
	if err := guardMarketParentPlacement(ctx, tx, parentID, *params.BlockHash, canonicalKnown); err != nil {
		return MarketEvent{}, err
	}
	// The inclusion can change after a reorganization, but immutable legacy
	// amounts and already-emitted notification snapshots must not be rewritten.
	if _, err := tx.Exec(ctx, `
		UPDATE market_events SET block_number=$2,block_hash=$3,transaction_index=$4,
		    occurred_at=$5,reorged=0,confirmed=0
		WHERE id=$1 AND (reorged=1 OR block_hash IS DISTINCT FROM $3)
	`, parentID, params.BlockNumber, params.BlockHash, params.TransactionIndex, params.OccurredAt); err != nil {
		return MarketEvent{}, fmt.Errorf("place logical trade inclusion: %w", err)
	}
	return collectOne[MarketEvent](ctx, tx, `SELECT `+marketEventColumns+` FROM market_events WHERE id=$1`, parentID)
}

func guardMarketParentPlacement(ctx context.Context, db DBTX, parentID int64, newHash string, canonicalKnown bool) error {
	var changed, oldCanonical bool
	if err := db.QueryRow(ctx, `
		SELECT e.block_hash IS NOT NULL AND e.block_hash<>$2,
		       e.reorged=0 AND EXISTS(SELECT 1 FROM market_scanned_blocks b WHERE b.chain_id=e.chain_id
		         AND b.block_number=e.block_number AND b.block_hash=e.block_hash AND b.canonical=1)
		FROM market_events e WHERE e.id=$1
	`, parentID, newHash).Scan(&changed, &oldCanonical); err != nil {
		return fmt.Errorf("check logical parent placement: %w", err)
	}
	if changed && (!canonicalKnown || oldCanonical) {
		return fmt.Errorf("%w: logical parent has a competing canonical inclusion", ErrInvalidMarketEvent)
	}
	return nil
}

func retireOtherMarketInclusions(ctx context.Context, db DBTX, params MarketTransactionParams) error {
	rows, err := db.Query(ctx, `
		UPDATE market_transaction_receipts SET reorged=TRUE,confirmed=FALSE
		WHERE chain_id=$1 AND transaction_hash=$2 AND block_hash<>$3 AND NOT reorged RETURNING id
	`, params.ChainID, params.TransactionHash, params.BlockHash)
	if err != nil {
		return fmt.Errorf("retire superseded transaction inclusion: %w", err)
	}
	ids, err := collectInt64Rows(rows)
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		if _, err := db.Exec(ctx, `UPDATE market_trade_ready_events SET valid=FALSE WHERE receipt_id=ANY($1::bigint[]) AND valid`, ids); err != nil {
			return err
		}
	}
	return nil
}

func restoreMarketReceipt(ctx context.Context, db DBTX, receiptID int64, params MarketTransactionParams) error {
	events, err := marketReceiptEvents(ctx, db, receiptID)
	if err != nil {
		return err
	}
	for _, event := range events {
		if err := guardMarketParentPlacement(ctx, db, event.ID, params.BlockHash, true); err != nil {
			return err
		}
	}
	if err := retireOtherMarketInclusions(ctx, db, params); err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `UPDATE market_transaction_receipts SET reorged=FALSE,confirmed=FALSE,revision=revision+1 WHERE id=$1 AND reorged`, receiptID); err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `
		UPDATE market_events e SET block_number=$2,block_hash=$3,transaction_index=$4,
		    occurred_at=$5,reorged=0,confirmed=0
		WHERE e.id IN (SELECT market_event_id FROM market_transaction_events WHERE receipt_id=$1)
	`, receiptID, params.BlockNumber, params.BlockHash, params.TransactionIndex, params.OccurredAt); err != nil {
		return err
	}
	return reconcileFourMemeEventTokens(ctx, db, events)
}

func reconcileFourMemeEventTokens(ctx context.Context, db DBTX, events []MarketEvent) error {
	tokens := make(map[string]int64)
	for _, event := range events {
		var metadata struct {
			Adapter string `json:"adapter"`
		}
		_ = json.Unmarshal(event.Metadata, &metadata)
		if metadata.Adapter == "four_meme_v1" || metadata.Adapter == "four_meme_v2" {
			tokens[event.TokenAddress] = event.ChainID
		}
	}
	addresses := make([]string, 0, len(tokens))
	for token := range tokens {
		addresses = append(addresses, token)
	}
	sort.Strings(addresses)
	for _, token := range addresses {
		if _, err := reconcileMarketFourMemeState(ctx, db, tokens[token], token); err != nil {
			return err
		}
	}
	return nil
}

func marketReceiptEvents(ctx context.Context, db DBTX, receiptID int64) ([]MarketEvent, error) {
	return collectMany[MarketEvent](ctx, db, `
		SELECT e.*,COALESCE((SELECT r.id FROM market_trade_ready_events r
		JOIN market_transaction_receipts receipt ON receipt.id=r.receipt_id AND receipt.revision=r.receipt_revision
		WHERE r.receipt_id=$1 AND r.parent_event_id=e.id AND r.valid),0) AS trade_ready_id
		FROM (SELECT `+marketEventColumns+` FROM market_events WHERE id IN (
			SELECT market_event_id FROM market_transaction_events WHERE receipt_id=$1
		)) e ORDER BY e.id
	`, receiptID)
}

func confirmMarketReceipt(ctx context.Context, db DBTX, receiptID int64) error {
	_, err := confirmMarketReceipts(ctx, db, []int64{receiptID})
	return err
}

func confirmMarketReceipts(ctx context.Context, db DBTX, receiptIDs []int64) (int64, error) {
	if len(receiptIDs) == 0 {
		return 0, nil
	}
	if _, err := db.Exec(ctx, `
		UPDATE market_transaction_receipts r SET confirmed=TRUE
		WHERE r.id=ANY($1::bigint[]) AND r.complete AND NOT r.reorged AND NOT r.confirmed
		  AND EXISTS(SELECT 1 FROM market_scanned_blocks b WHERE b.chain_id=r.chain_id
		    AND b.block_number=r.block_number AND b.block_hash=r.block_hash AND b.canonical=1)
	`, receiptIDs); err != nil {
		return 0, fmt.Errorf("verify complete market receipt: %w", err)
	}
	tag, err := db.Exec(ctx, `
		UPDATE market_events e SET confirmed=1
		FROM market_transaction_events x,market_transaction_receipts r
		WHERE x.receipt_id=ANY($1::bigint[]) AND r.id=x.receipt_id AND x.market_event_id=e.id
		  AND r.complete AND r.confirmed AND NOT r.reorged AND e.reorged=0 AND e.confirmed=0
		  AND e.chain_id=r.chain_id AND e.transaction_hash=r.transaction_hash
		  AND e.block_number=r.block_number AND e.block_hash=r.block_hash
	`, receiptIDs)
	if err != nil {
		return 0, fmt.Errorf("confirm validated market receipt events: %w", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO market_trade_ready_events(receipt_id,receipt_revision,parent_event_id,chain_id,token_address)
		SELECT r.id,r.revision,p.parent_event_id,p.chain_id,p.token_address
		FROM market_transaction_receipts r
		JOIN market_transaction_events x ON x.receipt_id=r.id
		JOIN market_trade_parents p ON p.parent_event_id=x.market_event_id
		JOIN market_events e ON e.id=p.parent_event_id
		WHERE r.id=ANY($1::bigint[]) AND r.complete AND r.confirmed AND NOT r.reorged
		  AND e.reorged=0 AND e.block_hash=r.block_hash AND e.block_number=r.block_number
		  AND EXISTS(SELECT 1 FROM market_trade_legs l WHERE l.receipt_id=r.id AND l.parent_event_id=p.parent_event_id)
		  AND EXISTS(SELECT 1 FROM market_scanned_blocks b WHERE b.chain_id=r.chain_id
		    AND b.block_number=r.block_number AND b.block_hash=r.block_hash AND b.canonical=1)
		ORDER BY p.parent_event_id ON CONFLICT(receipt_id,receipt_revision,parent_event_id) DO NOTHING
	`, receiptIDs); err != nil {
		return 0, fmt.Errorf("publish complete canonical market trades: %w", err)
	}
	return tag.RowsAffected(), nil
}

func retireMarketTradeReceipts(ctx context.Context, db DBTX, chainID, fromBlock int64, toBlock *int64, canonicalHash string) error {
	rows, err := db.Query(ctx, `
		UPDATE market_transaction_receipts SET reorged=TRUE,confirmed=FALSE
		WHERE chain_id=$1 AND block_number>=$2 AND ($3::bigint IS NULL OR block_number<=$3)
		  AND ($4::text='' OR block_hash<>$4) AND NOT reorged RETURNING id
	`, chainID, fromBlock, toBlock, canonicalHash)
	if err != nil {
		return fmt.Errorf("retire market receipt inclusions: %w", err)
	}
	ids, err := collectInt64Rows(rows)
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		if _, err := db.Exec(ctx, `UPDATE market_trade_ready_events SET valid=FALSE WHERE receipt_id=ANY($1::bigint[]) AND valid`, ids); err != nil {
			return fmt.Errorf("retire market trade publications: %w", err)
		}
	}
	return nil
}

func reconcileReorgedFourMemeTokens(ctx context.Context, db DBTX, chainID, fromBlock int64, toBlock *int64) error {
	type tokenRow struct {
		Address string `db:"token_address"`
	}
	tokens, err := collectMany[tokenRow](ctx, db, `
		SELECT DISTINCT token_address FROM market_events
		WHERE chain_id=$1 AND block_number>=$2 AND ($3::bigint IS NULL OR block_number<=$3)
		  AND reorged=1 AND metadata->>'adapter' IN ('four_meme_v1','four_meme_v2')
		ORDER BY token_address
	`, chainID, fromBlock, toBlock)
	if err != nil {
		return fmt.Errorf("find reorganized Four.meme tokens: %w", err)
	}
	for _, token := range tokens {
		if _, err := reconcileMarketFourMemeState(ctx, db, chainID, token.Address); err != nil {
			return err
		}
	}
	return nil
}

func normalizeMarketTransaction(params *MarketTransactionParams) error {
	params.ChainKey = strings.ToLower(strings.TrimSpace(params.ChainKey))
	params.TransactionHash = strings.ToLower(strings.TrimSpace(params.TransactionHash))
	params.BlockHash = strings.ToLower(strings.TrimSpace(params.BlockHash))
	if params.ChainID <= 0 || params.ChainKey == "" || params.BlockNumber < 0 || params.TransactionIndex < 0 ||
		!marketHashPattern.MatchString(params.TransactionHash) || !marketHashPattern.MatchString(params.BlockHash) ||
		params.OccurredAt.IsZero() || len(params.Events) > 2000 || len(params.ScopePoolIDs) > 10000 || len(params.ScopeTokenAddresses) > 10000 {
		return ErrInvalidMarketEvent
	}
	if params.ParserVersion == 0 {
		params.ParserVersion = 1
	}
	if params.ParserVersion < 1 {
		return ErrInvalidMarketEvent
	}
	params.OccurredAt = params.OccurredAt.UTC()
	for _, poolID := range params.ScopePoolIDs {
		if poolID <= 0 {
			return ErrInvalidMarketEvent
		}
	}
	params.ScopePoolIDs = uniquePositiveIDs(params.ScopePoolIDs)
	sort.Slice(params.ScopePoolIDs, func(i, j int) bool { return params.ScopePoolIDs[i] < params.ScopePoolIDs[j] })
	if params.ScopePoolIDs == nil {
		params.ScopePoolIDs = []int64{}
	}
	tokens := make(map[string]bool)
	for _, token := range params.ScopeTokenAddresses {
		normalized, err := normalizeMarketAddress(token)
		if err != nil {
			return ErrInvalidMarketEvent
		}
		tokens[normalized] = true
	}
	params.ScopeTokenAddresses = make([]string, 0, len(tokens))
	for token := range tokens {
		params.ScopeTokenAddresses = append(params.ScopeTokenAddresses, token)
	}
	sort.Strings(params.ScopeTokenAddresses)
	if err := validateMarketReceiptPayload(*params); err != nil {
		return err
	}
	params.Events = append([]MarketTransactionEvent(nil), params.Events...)
	seen := make(map[string]bool)
	legCount := 0
	var envelope struct {
		Logs []struct {
			LogIndex string `json:"logIndex"`
		} `json:"logs"`
	}
	_ = json.Unmarshal(params.ReceiptPayload, &envelope)
	receiptLogs := make(map[int64]bool, len(envelope.Logs))
	for _, log := range envelope.Logs {
		if !strings.HasPrefix(log.LogIndex, "0x") {
			return fmt.Errorf("%w: missing receipt log index", ErrInvalidMarketEvent)
		}
		index, err := strconv.ParseInt(log.LogIndex[2:], 16, 64)
		if err != nil || index < 0 || receiptLogs[index] {
			return fmt.Errorf("%w: invalid receipt log indices", ErrInvalidMarketEvent)
		}
		receiptLogs[index] = true
	}
	for index := range params.Events {
		item := &params.Events[index]
		event := &item.Event
		if event.ChainID != 0 && event.ChainID != params.ChainID ||
			event.TransactionHash != nil && !strings.EqualFold(*event.TransactionHash, params.TransactionHash) ||
			event.BlockHash != nil && !strings.EqualFold(*event.BlockHash, params.BlockHash) ||
			event.BlockNumber != nil && *event.BlockNumber != params.BlockNumber ||
			event.TransactionIndex != nil && *event.TransactionIndex != params.TransactionIndex {
			return fmt.Errorf("%w: event belongs to a different receipt", ErrInvalidMarketEvent)
		}
		event.ChainKey, event.ChainID = params.ChainKey, params.ChainID
		event.TransactionHash, event.BlockHash = &params.TransactionHash, &params.BlockHash
		event.BlockNumber, event.TransactionIndex = &params.BlockNumber, &params.TransactionIndex
		event.OccurredAt, event.Confirmed = params.OccurredAt, false
		if err := normalizeMarketEventParams(event); err != nil {
			return err
		}
		if !tokens[event.TokenAddress] {
			return fmt.Errorf("%w: event token is outside parser scope", ErrInvalidMarketEvent)
		}
		key := event.EventKey
		if len(item.Legs) > 0 {
			if event.EventType != "buy" && event.EventType != "sell" {
				return fmt.Errorf("%w: non-trade event has swap legs", ErrInvalidMarketEvent)
			}
			key = event.TokenAddress + ":" + event.EventType
		} else if event.EventType == "buy" || event.EventType == "sell" {
			var metadata struct {
				Adapter string `json:"adapter"`
			}
			_ = json.Unmarshal(event.Metadata, &metadata)
			if metadata.Adapter != "four_meme_v1" && metadata.Adapter != "four_meme_v2" {
				return fmt.Errorf("%w: DEX trade has no complete legs", ErrInvalidMarketEvent)
			}
		}
		if seen[key] {
			return fmt.Errorf("%w: duplicate logical event in receipt", ErrInvalidMarketEvent)
		}
		seen[key] = true
		item.Legs = append([]MarketTradeLegParams(nil), item.Legs...)
		logs := make(map[int64]bool)
		tokenTotal := new(big.Int)
		for legIndex := range item.Legs {
			leg := &item.Legs[legIndex]
			legCount++
			if legCount > 20000 || leg.LogIndex < 0 || !receiptLogs[leg.LogIndex] || logs[leg.LogIndex] ||
				!containsTradePool(params.ScopePoolIDs, leg.PoolID) || !validTradeRaw(leg.TokenAmountRaw) ||
				leg.QuoteAmountRaw != "" && !validTradeRaw(leg.QuoteAmountRaw) {
				return fmt.Errorf("%w: invalid or duplicate trade leg", ErrInvalidMarketEvent)
			}
			logs[leg.LogIndex] = true
			amount, _ := new(big.Int).SetString(leg.TokenAmountRaw, 10)
			tokenTotal.Add(tokenTotal, amount)
			if leg.QuoteAddress != "" {
				var err error
				leg.QuoteAddress, err = normalizeMarketAddress(leg.QuoteAddress)
				if err != nil {
					return ErrInvalidMarketEvent
				}
			}
			for _, value := range []*string{leg.TokenAmount, leg.QuoteAmount, leg.USDValue, leg.PriceUSD} {
				if value != nil && !validTradeDecimal(*value) {
					return fmt.Errorf("%w: invalid trade valuation", ErrInvalidMarketEvent)
				}
			}
			if len(leg.Metadata) > 0 {
				if len(leg.Metadata) > 2048 {
					return fmt.Errorf("%w: trade leg metadata exceeds 2048 bytes", ErrInvalidMarketEvent)
				}
				var object map[string]json.RawMessage
				if json.Unmarshal(leg.Metadata, &object) != nil || object == nil {
					return ErrInvalidMarketEvent
				}
			}
		}
		if len(item.Legs) > 0 {
			if event.TokenAmountRaw == nil || len(*event.TokenAmountRaw) > 82 {
				return fmt.Errorf("%w: trade parent has no raw total", ErrInvalidMarketEvent)
			}
			parentTotal, ok := new(big.Int).SetString(*event.TokenAmountRaw, 10)
			if !ok || parentTotal.Sign() < 0 || tokenTotal.Cmp(parentTotal) != 0 {
				return fmt.Errorf("%w: incomplete trade parent legs", ErrInvalidMarketEvent)
			}
		}
	}
	// Four.meme helpers take token locks: every receipt takes them in the same
	// address order, including receipts that contain multiple watched tokens.
	sort.SliceStable(params.Events, func(i, j int) bool {
		return params.Events[i].Event.TokenAddress < params.Events[j].Event.TokenAddress
	})
	return nil
}

func validateMarketTradePools(ctx context.Context, db DBTX, params MarketTransactionParams) error {
	ids := make([]int64, 0)
	for _, event := range params.Events {
		for _, leg := range event.Legs {
			ids = append(ids, leg.PoolID)
		}
	}
	ids = uniquePositiveIDs(ids)
	if len(ids) == 0 {
		return nil
	}
	type poolIdentity struct {
		ID     int64  `db:"id"`
		Token0 string `db:"token0_address"`
		Token1 string `db:"token1_address"`
	}
	pools, err := collectMany[poolIdentity](ctx, db, `SELECT id,token0_address,token1_address FROM market_pools WHERE chain_id=$1 AND id=ANY($2::bigint[])`, params.ChainID, ids)
	if err != nil {
		return fmt.Errorf("validate market trade pool identities: %w", err)
	}
	byID := make(map[int64]poolIdentity, len(pools))
	for _, pool := range pools {
		byID[pool.ID] = pool
	}
	for _, event := range params.Events {
		for _, leg := range event.Legs {
			pool, exists := byID[leg.PoolID]
			if !exists {
				return fmt.Errorf("%w: trade pool belongs to another chain", ErrInvalidMarketEvent)
			}
			quote := ""
			if pool.Token0 == event.Event.TokenAddress {
				quote = pool.Token1
			} else if pool.Token1 == event.Event.TokenAddress {
				quote = pool.Token0
			}
			if quote == "" || leg.QuoteAddress != "" && leg.QuoteAddress != quote {
				return fmt.Errorf("%w: trade pool token mismatch", ErrInvalidMarketEvent)
			}
		}
	}
	return nil
}

func containsTradePool(pools []int64, id int64) bool {
	index := sort.Search(len(pools), func(i int) bool { return pools[i] >= id })
	return index < len(pools) && pools[index] == id
}

func validTradeRaw(value string) bool {
	if value == "" || len(value) > 78 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	integer, ok := new(big.Int).SetString(value, 10)
	return ok && integer.Sign() >= 0 && integer.BitLen() <= 256
}

func validTradeDecimal(value string) bool {
	if len(value) > 512 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789.+-eE", character) {
			return false
		}
	}
	if index := strings.IndexAny(value, "eE"); index >= 0 {
		exponent, err := strconv.Atoi(value[index+1:])
		if err != nil || exponent < -512 || exponent > 512 {
			return false
		}
	}
	number, ok := new(big.Rat).SetString(value)
	return ok && number.Sign() >= 0
}

func validateMarketReceiptPayload(params MarketTransactionParams) error {
	var receipt map[string]json.RawMessage
	if json.Unmarshal(params.ReceiptPayload, &receipt) != nil || receipt == nil {
		return fmt.Errorf("%w: receipt payload is not an object", ErrInvalidMarketEvent)
	}
	for _, field := range []struct{ name, expected string }{
		{"transactionHash", params.TransactionHash}, {"blockHash", params.BlockHash},
	} {
		var value string
		if json.Unmarshal(receipt[field.name], &value) != nil || !strings.EqualFold(value, field.expected) {
			return fmt.Errorf("%w: receipt %s mismatch", ErrInvalidMarketEvent, field.name)
		}
	}
	for _, field := range []struct {
		name     string
		expected int64
	}{{"blockNumber", params.BlockNumber}, {"transactionIndex", int64(params.TransactionIndex)}} {
		var value string
		if json.Unmarshal(receipt[field.name], &value) != nil || !strings.HasPrefix(value, "0x") {
			return fmt.Errorf("%w: missing receipt %s", ErrInvalidMarketEvent, field.name)
		}
		actual, err := strconv.ParseInt(value[2:], 16, 64)
		if err != nil || actual != field.expected {
			return fmt.Errorf("%w: receipt %s mismatch", ErrInvalidMarketEvent, field.name)
		}
	}
	var logs []json.RawMessage
	if len(receipt["logs"]) == 0 || string(receipt["logs"]) == "null" || json.Unmarshal(receipt["logs"], &logs) != nil || len(logs) > 10000 {
		return fmt.Errorf("%w: receipt logs are unavailable or too large", ErrInvalidMarketEvent)
	}
	return nil
}
