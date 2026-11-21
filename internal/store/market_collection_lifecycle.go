package store

import (
	"context"
	"errors"
	"strings"
	"time"
)

var (
	ErrMarketCollectionStateChanged   = errors.New("market collection lifecycle changed; reload proof")
	ErrMarketCollectionProofRequired  = errors.New("market collection idle proof is required")
	ErrMarketCollectionCursorConflict = errors.New("market collection cursor changed; reload proof")
)

type MarketCollectionIdleInterval struct {
	ID                  int64      `db:"id"`
	ChainID             int64      `db:"chain_id"`
	ChainKey            string     `db:"chain_key"`
	FromAt              time.Time  `db:"from_at"`
	ThroughAt           *time.Time `db:"through_at"`
	OpenedRevision      int64      `db:"opened_revision"`
	EarlierActivationAt *time.Time `db:"earlier_activation_at"`
}

type MarketChainCollectionState struct {
	ChainKey                   string                        `db:"chain_key"`
	ChainID                    int64                         `db:"chain_id"`
	Revision                   int64                         `db:"revision"`
	ActiveTargetCount          int64                         `db:"active_target_count"`
	Phase                      string                        `db:"phase"`
	AcknowledgedIdleIntervalID int64                         `db:"acknowledged_idle_interval_id"`
	PendingIdle                *MarketCollectionIdleInterval `db:"-"`
}

const collectionStateColumns = `chain_key,chain_id,revision,active_target_count,phase,acknowledged_idle_interval_id`
const collectionStateTimeout = 2 * time.Second

func readCollectionState(ctx context.Context, db DBTX, key string, id int64, lock bool) (MarketChainCollectionState, error) {
	query := `SELECT ` + collectionStateColumns + ` FROM market_chain_collection_states WHERE chain_id=$1 AND chain_key=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	state, err := collectOne[MarketChainCollectionState](ctx, db, query, id, key)
	if isNoRows(err) {
		return state, ErrMarketCollectionProofRequired
	}
	if err != nil {
		return state, err
	}
	var actual int64
	if err = db.QueryRow(ctx, `SELECT debox_market_collection_target_count($1)`, id).Scan(&actual); err != nil {
		return state, err
	}
	if actual != state.ActiveTargetCount {
		return state, ErrMarketCollectionStateChanged
	}
	return state, nil
}

func pendingCollectionIdle(ctx context.Context, db DBTX, state MarketChainCollectionState) (*MarketCollectionIdleInterval, error) {
	return collectOptional[MarketCollectionIdleInterval](ctx, db, `SELECT i.id,i.chain_id,i.chain_key,i.from_at,i.through_at,i.opened_revision,
 (SELECT earlier.through_at FROM market_collection_idle_intervals earlier
  WHERE earlier.chain_id=i.chain_id AND earlier.id>$2 AND earlier.id<i.id ORDER BY earlier.id LIMIT 1) AS earlier_activation_at
 FROM market_collection_idle_intervals i WHERE i.chain_id=$1 AND i.id>$2
 ORDER BY i.id DESC LIMIT 1`, state.ChainID, state.AcknowledgedIdleIntervalID)
}

// ObserveMarketChainCollection is called under the existing log-scanner session
// lock. It must not reacquire that advisory lock through the ordinary query
// pool. Lifecycle writers and an idle advance serialize only on this state row;
// these transactions never lock project rows. Repeated idle observations write
// nothing. The first committed zero observation establishes proof from now,
// never retroactively from a legacy timestamp or current subscription count.
func (s *Store) ObserveMarketChainCollection(ctx context.Context, key string, id int64) (MarketChainCollectionState, error) {
	ctx, cancel := context.WithTimeout(ctx, collectionStateTimeout)
	defer cancel()
	key = strings.ToLower(strings.TrimSpace(key))
	// A stable idle/active poll needs no row lock or write transaction. Any
	// subsequent start decision independently rechecks the revision and scope.
	state, err := readCollectionState(ctx, s.db, key, id, false)
	if err != nil {
		return state, err
	}
	if state.ActiveTargetCount != 0 || state.Phase == "idle" {
		state.PendingIdle, err = pendingCollectionIdle(ctx, s.db, state)
		return state, err
	}
	return withTxValue(ctx, s.db, func(tx DBTX) (MarketChainCollectionState, error) {
		state, err := readCollectionState(ctx, tx, key, id, true)
		if err != nil {
			return state, err
		}
		if state.ActiveTargetCount == 0 && state.Phase != "idle" {
			state, err = collectOne[MarketChainCollectionState](ctx, tx, `UPDATE market_chain_collection_states
 SET phase='idle',revision=revision+1,updated_at=clock_timestamp() WHERE chain_id=$1 RETURNING `+collectionStateColumns, id)
			if err != nil {
				return state, err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO market_collection_idle_intervals(chain_id,chain_key,from_at,opened_revision)
 VALUES($1,$2,clock_timestamp(),$3)`, id, key, state.Revision); err != nil {
				return state, err
			}
		}
		state.PendingIdle, err = pendingCollectionIdle(ctx, tx, state)
		return state, err
	})
}

type MarketCollectionHistoryPrefix struct {
	ThroughBlock              int64
	ThroughAt                 time.Time
	PreviousHash, ThroughHash string
	FirstIdleAt               *time.Time
}

type AdvanceMarketChainCursorOverIdleParams struct {
	HistoryStartBlock                                     int64
	HistoryPrefix                                         *MarketCollectionHistoryPrefix
	ChainKey, CursorKey                                   string
	ChainID, StateRevision, IntervalID, ExpectedNextBlock int64
	NextBlockNumber, SafeBlockNumber                      int64
	FirstSkippedBlockAt, LastSkippedBlockAt               time.Time
	LastBlockHash, LastBlockParentHash                    string
}

type AcknowledgeMarketChainCollectionIdleParams struct {
	ChainKey, CursorKey                                   string
	ChainID, StateRevision, IntervalID, ExpectedNextBlock int64
	NextBlockAt                                           *time.Time
}

func validateCollectionStart(ctx context.Context, tx DBTX, key string, id, revision, intervalID int64) (MarketChainCollectionState, *MarketCollectionIdleInterval, error) {
	state, err := readCollectionState(ctx, tx, key, id, true)
	if err != nil {
		return state, nil, err
	}
	if state.Revision != revision || state.Phase != "active" || state.ActiveTargetCount == 0 {
		return state, nil, ErrMarketCollectionStateChanged
	}
	interval, err := pendingCollectionIdle(ctx, tx, state)
	if err != nil {
		return state, nil, err
	}
	if interval == nil || interval.ID != intervalID || interval.ThroughAt == nil {
		return state, nil, ErrMarketCollectionProofRequired
	}
	return state, interval, nil
}

func readCollectionCursor(ctx context.Context, tx DBTX, id int64, key string, expected int64) (*MarketChainCursor, error) {
	cursor, err := collectOptional[MarketChainCursor](ctx, tx, `SELECT `+marketChainCursorColumns+`
 FROM market_chain_cursors WHERE chain_id=$1 AND cursor_key=$2 FOR UPDATE`, id, key)
	if err != nil {
		return nil, err
	}
	if cursor == nil && expected != 0 || cursor != nil && cursor.NextBlockNumber != expected {
		return nil, ErrMarketCollectionCursorConflict
	}
	return cursor, nil
}

func acknowledgeCollectionIdle(ctx context.Context, tx DBTX, id, intervalID int64) error {
	_, err := tx.Exec(ctx, `UPDATE market_chain_collection_states SET acknowledged_idle_interval_id=$2,revision=revision+1,updated_at=clock_timestamp() WHERE chain_id=$1`, id, intervalID)
	return err
}

// AdvanceMarketChainCursorOverIdle atomically validates the lifecycle revision,
// actual committed demand and expected cursor; records only a proven idle gap;
// then advances and acknowledges. The caller already owns the chain scanner
// lock and supplies verified canonical block timestamps/hashes, not wall times.
// With no prior cursor (ExpectedNextBlock=0), it instead persists the explicit
// activation/lookback start atomically, without claiming any unknown history as
// a proven idle gap. No rule, notification boundary or summary state is changed.
func (s *Store) AdvanceMarketChainCursorOverIdle(ctx context.Context, p AdvanceMarketChainCursorOverIdleParams) (MarketChainCursor, error) {
	ctx, cancel := context.WithTimeout(ctx, collectionStateTimeout)
	defer cancel()
	if p.CursorKey == "" || p.NextBlockNumber < 1 || p.NextBlockNumber > p.SafeBlockNumber+1 || p.LastSkippedBlockAt.IsZero() {
		return MarketChainCursor{}, ErrMarketCollectionProofRequired
	}
	return withTxValue(ctx, s.db, func(tx DBTX) (MarketChainCursor, error) {
		// Canonical block writers already order trade-chain before project
		// rows. Take that existing lock before lifecycle state, never after.
		if p.ExpectedNextBlock == 0 || p.NextBlockNumber != p.ExpectedNextBlock {
			if err := lockMarketTradeChain(ctx, tx, p.ChainID); err != nil {
				return MarketChainCursor{}, err
			}
		}
		_, interval, err := validateCollectionStart(ctx, tx, p.ChainKey, p.ChainID, p.StateRevision, p.IntervalID)
		if err != nil {
			return MarketChainCursor{}, err
		}
		cursor, err := readCollectionCursor(ctx, tx, p.ChainID, p.CursorKey, p.ExpectedNextBlock)
		if err != nil {
			return MarketChainCursor{}, err
		}
		historyStart := p.ExpectedNextBlock
		if cursor == nil && interval.EarlierActivationAt != nil {
			historyStart = p.HistoryStartBlock
			if historyStart < 1 || historyStart > p.NextBlockNumber {
				return MarketChainCursor{}, ErrMarketCollectionProofRequired
			}
		}
		if cursor != nil && p.NextBlockNumber == historyStart {
			// The retained lookback already covers every block in question.
			if p.HistoryPrefix != nil || p.FirstSkippedBlockAt.IsZero() {
				return MarketChainCursor{}, ErrMarketCollectionProofRequired
			}
			if err = acknowledgeCollectionIdle(ctx, tx, p.ChainID, p.IntervalID); err != nil {
				return *cursor, err
			}
			return *cursor, nil
		}
		if p.NextBlockNumber < historyStart || !p.LastSkippedBlockAt.Before(*interval.ThroughAt) {
			return MarketChainCursor{}, ErrMarketCollectionProofRequired
		}
		if err = recordCollectionStartRanges(ctx, tx, p, historyStart, cursor, *interval); err != nil {
			return MarketChainCursor{}, err
		}
		anchorAt := p.LastSkippedBlockAt.UTC()
		if _, err = newWithDB(tx).UpsertMarketScannedBlock(ctx, UpsertMarketScannedBlockParams{
			ChainKey: p.ChainKey, ChainID: p.ChainID, CursorKey: p.CursorKey,
			BlockNumber: p.NextBlockNumber - 1, BlockHash: p.LastBlockHash,
			ParentHash: p.LastBlockParentHash, BlockTimestamp: &anchorAt, ScannedAt: time.Now().UTC(),
		}); err != nil {
			return MarketChainCursor{}, err
		}
		advanced, err := newWithDB(tx).AdvanceMarketChainCursor(ctx, AdvanceMarketChainCursorParams{ChainKey: p.ChainKey, ChainID: p.ChainID, CursorKey: p.CursorKey,
			NextBlockNumber: p.NextBlockNumber, SafeBlockNumber: p.SafeBlockNumber, LastBlockHash: &p.LastBlockHash, Status: "active", ScannedAt: time.Now().UTC()})
		if err != nil {
			return advanced, err
		}
		if err = acknowledgeCollectionIdle(ctx, tx, p.ChainID, p.IntervalID); err != nil {
			return advanced, err
		}
		return advanced, nil
	})
}

// Acknowledge a cursor that already reached or passed activation without
// moving it. A nil cursor must use atomic initialization.
func (s *Store) AcknowledgeMarketChainCollectionIdle(ctx context.Context, p AcknowledgeMarketChainCollectionIdleParams) error {
	ctx, cancel := context.WithTimeout(ctx, collectionStateTimeout)
	defer cancel()
	_, err := withTxValue(ctx, s.db, func(tx DBTX) (struct{}, error) {
		_, interval, err := validateCollectionStart(ctx, tx, p.ChainKey, p.ChainID, p.StateRevision, p.IntervalID)
		if err != nil {
			return struct{}{}, err
		}
		cursor, err := readCollectionCursor(ctx, tx, p.ChainID, p.CursorKey, p.ExpectedNextBlock)
		if err != nil {
			return struct{}{}, err
		}
		if cursor == nil || p.NextBlockAt == nil || p.NextBlockAt.Before(*interval.ThroughAt) {
			return struct{}{}, ErrMarketCollectionProofRequired
		}
		return struct{}{}, acknowledgeCollectionIdle(ctx, tx, p.ChainID, p.IntervalID)
	})
	return err
}

// recordCollectionStartRanges partitions blocks leaving the live cursor.
// Only the range inside a confirmed zero-demand interval receives an idle gap.
func recordCollectionStartRanges(ctx context.Context, tx DBTX, p AdvanceMarketChainCursorOverIdleParams, start int64, cursor *MarketChainCursor, interval MarketCollectionIdleInterval) error {
	prefix := p.HistoryPrefix
	if start == 0 || start == p.NextBlockNumber {
		if prefix != nil {
			return ErrMarketCollectionProofRequired
		}
		return nil
	}
	fromAt := p.FirstSkippedBlockAt
	if fromAt.IsZero() || p.LastSkippedBlockAt.Before(fromAt) {
		return ErrMarketCollectionProofRequired
	}
	if fromAt.Before(interval.FromAt) {
		if prefix == nil || prefix.ThroughBlock < start || prefix.ThroughBlock >= p.NextBlockNumber ||
			prefix.ThroughAt.Before(fromAt) || !prefix.ThroughAt.Before(interval.FromAt) ||
			!marketHashPattern.MatchString(prefix.PreviousHash) || !marketHashPattern.MatchString(prefix.ThroughHash) {
			return ErrMarketCollectionProofRequired
		}
		if cursor != nil && cursor.LastBlockHash != nil && *cursor.LastBlockHash != prefix.PreviousHash {
			return ErrMarketCollectionCursorConflict
		}
		if _, err := tx.Exec(ctx, `INSERT INTO market_collection_history_ranges
 (chain_id,cursor_key,idle_interval_id,from_block,through_block,from_at,through_at,predecessor_hash,through_hash,next_review_block)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$4)`, p.ChainID, p.CursorKey, p.IntervalID, start, prefix.ThroughBlock, fromAt, prefix.ThroughAt, prefix.PreviousHash, prefix.ThroughHash); err != nil {
			return err
		}
		start = prefix.ThroughBlock + 1
		if start == p.NextBlockNumber {
			if prefix.FirstIdleAt != nil || prefix.ThroughHash != p.LastBlockHash || !prefix.ThroughAt.Equal(p.LastSkippedBlockAt) {
				return ErrMarketCollectionProofRequired
			}
			return nil
		}
		if prefix.FirstIdleAt == nil {
			return ErrMarketCollectionProofRequired
		}
		fromAt = *prefix.FirstIdleAt
	} else if prefix != nil {
		return ErrMarketCollectionProofRequired
	}
	if fromAt.Before(interval.FromAt) || p.LastSkippedBlockAt.Before(fromAt) {
		return ErrMarketCollectionProofRequired
	}
	_, err := tx.Exec(ctx, `INSERT INTO market_collection_gaps(chain_id,chain_key,cursor_key,idle_interval_id,from_block,through_block,from_at,through_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, p.ChainID, p.ChainKey, p.CursorKey, p.IntervalID, start, p.NextBlockNumber-1, fromAt, p.LastSkippedBlockAt)
	return err
}
