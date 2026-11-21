package marketcollector

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

// Production storage must persist lifecycle proof. Keeping this capability
// separate lets old adapters retain the conservative, ordinary scanner path;
// absence of this interface can never authorize skipping history.
type scanCollectionRepository interface {
	ObserveMarketChainCollection(context.Context, string, int64) (store.MarketChainCollectionState, error)
	AdvanceMarketChainCursorOverIdle(context.Context, store.AdvanceMarketChainCursorOverIdleParams) (store.MarketChainCursor, error)
	AcknowledgeMarketChainCollectionIdle(context.Context, store.AcknowledgeMarketChainCollectionIdleParams) error
}

var _ scanCollectionRepository = (*store.Store)(nil)

func (service *Service) observeScanCollection(ctx context.Context) (*store.MarketChainCollectionState, error) {
	repository, ok := service.repository.(scanCollectionRepository)
	if !ok {
		return nil, nil
	}
	state, err := repository.ObserveMarketChainCollection(ctx, service.settings.ChainKey, service.settings.ChainID)
	if err != nil {
		return nil, err
	}
	if state.ChainID != service.settings.ChainID || state.ChainKey != service.settings.ChainKey {
		return nil, store.ErrMarketCollectionStateChanged
	}
	return &state, nil
}

// A single last-notice value, rather than a growing map or a timer, bounds
// memory and emits a blocked reason only when the interval/state changes.
type scanScopeNotice struct {
	mu         sync.Mutex
	intervalID int64
	revision   int64
	reason     string
}

func (service *Service) reportCollectionBoundary(ctx context.Context, state store.MarketChainCollectionState, next int64, reason string) {
	if service.logger == nil || state.PendingIdle == nil {
		return
	}
	interval := state.PendingIdle
	notice := &service.scanScopeNotice
	notice.mu.Lock()
	changed := notice.intervalID != interval.ID || notice.revision != state.Revision || notice.reason != reason
	if changed {
		notice.intervalID, notice.revision, notice.reason = interval.ID, state.Revision, reason
	}
	notice.mu.Unlock()
	if changed {
		service.logger.WarnContext(ctx, "market collection idle boundary retained",
			"chain_key", service.settings.ChainKey, "chain_id", service.settings.ChainID,
			"cursor_key", service.settings.CursorKey, "idle_interval_id", interval.ID,
			"idle_from", interval.FromAt, "idle_through", interval.ThroughAt,
			"next_block", next, "reason", reason)
	}
}

// prepareCollectionActivation runs under the existing chain log-scanner lock.
// It never confirms receipts. The store rechecks lifecycle revision, actual
// committed collection scope and the old cursor in the same transaction that
// records a proven idle gap (if any) and acknowledges its activation decision.
// A deferred result means the activation block is not safe yet. Any old
// prefix outside the proven idle period is retained for separate scope review.
func (service *Service) prepareCollectionActivation(
	ctx context.Context,
	state store.MarketChainCollectionState,
	cursor *store.MarketChainCursor,
	previous *blockHeader,
	safeBlock int64,
) (*store.MarketChainCursor, *blockHeader, bool, error) {
	repository, ok := service.repository.(scanCollectionRepository)
	if !ok || state.PendingIdle == nil {
		return cursor, previous, false, nil
	}
	interval := state.PendingIdle
	if state.Phase != "active" || state.ActiveTargetCount <= 0 || interval.ThroughAt == nil {
		return cursor, previous, true, nil
	}
	if interval.ChainID != state.ChainID || interval.ChainKey != state.ChainKey || interval.ThroughAt.Before(interval.FromAt) {
		return cursor, previous, false, store.ErrMarketCollectionProofRequired
	}
	oldNext := int64(0)
	if cursor != nil {
		oldNext = cursor.NextBlockNumber
		if oldNext < 1 || previous == nil || previous.Number != uint64(oldNext-1) {
			return cursor, previous, false, store.ErrMarketCollectionProofRequired
		}
	}

	// Locating the earliest unscanned activation, current activation and idle boundary may each cost
	// up to 63 bisections, plus a fixed number of endpoint reads.
	// It creates no background work and is never retained between scan rounds.
	headers := make(map[int64]blockHeader)
	if previous != nil {
		headers[int64(previous.Number)] = *previous
	}
	read := func(number int64) (blockHeader, error) {
		if header, exists := headers[number]; exists {
			return header, nil
		}
		if number < 0 || len(headers) >= 202 {
			return blockHeader{}, fmt.Errorf("market activation header search exceeded its bounded range")
		}
		header, err := service.fetchBlockHeader(ctx, uint64(number))
		if err == nil {
			headers[number] = header
		}
		return header, err
	}
	low := int64(1)
	if cursor != nil {
		low = oldNext
	}
	first, err := read(low)
	if err != nil {
		return cursor, previous, false, err
	}
	if previous != nil && (first.Timestamp.Before(previous.Timestamp) || first.ParentHash != previous.Hash) {
		return cursor, previous, false, fmt.Errorf("market activation start contradicts its verified predecessor")
	}
	if cursor != nil && !first.Timestamp.Before(*interval.ThroughAt) {
		if err := repository.AcknowledgeMarketChainCollectionIdle(ctx, store.AcknowledgeMarketChainCollectionIdleParams{
			ChainKey: state.ChainKey, ChainID: state.ChainID, CursorKey: service.settings.CursorKey,
			StateRevision: state.Revision, IntervalID: interval.ID, ExpectedNextBlock: oldNext, NextBlockAt: &first.Timestamp,
		}); err != nil {
			return cursor, previous, false, err
		}
		return cursor, previous, false, nil
	}

	safe, err := read(safeBlock)
	if err != nil {
		return cursor, previous, false, err
	}
	if safe.Timestamp.Before(first.Timestamp) {
		return cursor, previous, false, fmt.Errorf("market activation safe header precedes its lower bound")
	}
	if safe.Timestamp.Before(*interval.ThroughAt) {
		service.reportCollectionBoundary(ctx, state, oldNext, "activation block has not reached the confirmed safe height")
		return cursor, previous, true, nil
	}
	// Several idle/active transitions may have committed before the very
	// first scanner run. Reserve the first activation's lookback as historical
	// work and prioritize the latest activation on the live cursor.
	historyStart := oldNext
	var earliestBoundary *blockHeader
	if cursor == nil && interval.EarlierActivationAt != nil {
		earliest, err := findCollectionActivation(low, safeBlock, *interval.EarlierActivationAt, first, safe, read)
		if err != nil {
			return cursor, previous, false, err
		}
		earliestHeader := headers[earliest]
		earliestBoundary = &earliestHeader
		historyStart = max(1, earliest-service.settings.InitialLookback+1)
		low = historyStart
		first, err = read(low)
		if err != nil {
			return cursor, previous, false, err
		}
	}
	activation, err := findCollectionActivation(low, safeBlock, *interval.ThroughAt, first, safe, read)
	if err != nil {
		return cursor, previous, false, err
	}
	next := activation - service.settings.InitialLookback + 1
	if next < 1 {
		next = 1
	}
	if next < oldNext {
		next = oldNext
	}
	anchor, err := read(next - 1)
	if err != nil {
		return cursor, previous, false, err
	}
	if !anchor.Timestamp.Before(*interval.ThroughAt) || cursor != nil && next > oldNext && anchor.Timestamp.Before(first.Timestamp) {
		return cursor, previous, false, fmt.Errorf("market activation anchor is outside the proven idle boundary")
	}
	advance := store.AdvanceMarketChainCursorOverIdleParams{
		ChainKey: state.ChainKey, ChainID: state.ChainID, CursorKey: service.settings.CursorKey,
		StateRevision: state.Revision, IntervalID: interval.ID, ExpectedNextBlock: oldNext,
		NextBlockNumber: next, SafeBlockNumber: safeBlock,
		FirstSkippedBlockAt: first.Timestamp, LastSkippedBlockAt: anchor.Timestamp, HistoryStartBlock: historyStart,
		LastBlockHash: anchor.Hash, LastBlockParentHash: anchor.ParentHash,
	}
	// Re-read the split boundaries just before the compare-and-swap. Do not
	// infer coverage or the historical filter from today's project selection.
	endpoints := []blockHeader{first, anchor, headers[activation]}
	if earliestBoundary != nil {
		endpoints = append(endpoints, *earliestBoundary)
	}
	if historyStart > 0 && next > historyStart && first.Timestamp.Before(interval.FromAt) {
		lastUnknown := anchor
		var firstIdleAt *time.Time
		if !anchor.Timestamp.Before(interval.FromAt) {
			firstIdleNumber, err := findCollectionActivation(historyStart, next-1, interval.FromAt, first, anchor, read)
			if err != nil {
				return cursor, previous, false, err
			}
			firstIdle, err := read(firstIdleNumber)
			if err != nil {
				return cursor, previous, false, err
			}
			lastUnknown, err = read(firstIdleNumber - 1)
			if err != nil {
				return cursor, previous, false, err
			}
			if firstIdle.ParentHash != lastUnknown.Hash || !lastUnknown.Timestamp.Before(interval.FromAt) {
				return cursor, previous, false, fmt.Errorf("unlinked collection history/idle boundary")
			}
			firstIdleAt = &firstIdle.Timestamp
			endpoints = append(endpoints, lastUnknown, firstIdle)
		}
		advance.HistoryPrefix = &store.MarketCollectionHistoryPrefix{
			ThroughBlock: int64(lastUnknown.Number), ThroughAt: lastUnknown.Timestamp,
			PreviousHash: first.ParentHash, ThroughHash: lastUnknown.Hash, FirstIdleAt: firstIdleAt,
		}
	}
	for _, cached := range endpoints {
		fresh, fetchErr := service.fetchBlockHeader(ctx, cached.Number)
		if fetchErr != nil {
			return cursor, previous, false, fetchErr
		}
		if fresh.Hash != cached.Hash || fresh.ParentHash != cached.ParentHash || !fresh.Timestamp.Equal(cached.Timestamp) {
			return cursor, previous, false, fmt.Errorf("market activation header changed before its cursor decision")
		}
	}
	advanced, err := repository.AdvanceMarketChainCursorOverIdle(ctx, advance)
	if err != nil {
		return cursor, previous, false, err
	}
	if service.logger != nil {
		service.logger.InfoContext(ctx, "market collection activation start recorded",
			"chain_key", state.ChainKey, "chain_id", state.ChainID, "cursor_key", service.settings.CursorKey,
			"idle_interval_id", interval.ID, "previous_next_block", oldNext, "next_block", advanced.NextBlockNumber,
			"activation_block", activation, "initial_lookback", service.settings.InitialLookback)
	}
	return &advanced, &anchor, false, nil
}

// Find the first safe block at or after activation using a bounded lower-bound
// search. In particular, this uses activation time rather than startup time, so
// a late scanner start cannot discard the intervening active collection range.
func findCollectionActivation(low, high int64, at time.Time, first, last blockHeader, read func(int64) (blockHeader, error)) (int64, error) {
	if low < 1 || high < low || first.Number != uint64(low) || last.Number != uint64(high) || last.Timestamp.Before(at) || last.Timestamp.Before(first.Timestamp) {
		return 0, fmt.Errorf("invalid market activation search endpoints")
	}
	if !first.Timestamp.Before(at) {
		return low, nil
	}
	for probes := 0; low+1 < high; probes++ {
		if probes >= 63 {
			return 0, fmt.Errorf("market activation search did not converge within its header bound")
		}
		mid := low + (high-low)/2
		header, err := read(mid)
		if err != nil {
			return 0, err
		}
		if header.Number != uint64(mid) || header.Timestamp.Before(first.Timestamp) || header.Timestamp.After(last.Timestamp) {
			return 0, fmt.Errorf("market activation header timestamps contradict their search bounds")
		}
		if header.Timestamp.Before(at) {
			low, first = mid, header
		} else {
			high, last = mid, header
		}
	}
	if last.ParentHash != first.Hash {
		return 0, fmt.Errorf("market activation boundary headers are not contiguous")
	}
	return high, nil
}
