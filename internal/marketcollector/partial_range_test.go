package marketcollector

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type filteredCanonicalRepository struct {
	*fakeRepository
	limit int
}

func (r *filteredCanonicalRepository) ListCanonicalMarketScannedBlocks(_ context.Context, _ int64, _ string, through int64, limit int) ([]store.MarketScannedBlock, error) {
	r.limit = limit
	var rows []store.MarketScannedBlock
	for _, value := range r.candidates {
		if value.BlockNumber <= through && len(rows) < limit {
			rows = append(rows, value)
		}
	}
	return rows, nil
}

func TestPartialScanRangeReorgUsesFreshVerifiedPredecessor(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	ancestor := blockHeader{Number: 99, Hash: testHashC, ParentHash: testHashA, Timestamp: at.Add(-time.Second)}
	cursor := store.MarketChainCursor{NextBlockNumber: 100, LastBlockHash: &ancestor.Hash}
	r := &filteredCanonicalRepository{fakeRepository: &fakeRepository{candidates: []store.MarketScannedBlock{{BlockNumber: 100, BlockHash: testHashA}}}}
	c := &fakeChain{blocks: map[uint64]map[string]any{99: {"number": "0x63", "hash": ancestor.Hash, "parentHash": ancestor.ParentHash, "timestamp": "0x6553f0ff"}}}
	s := New(Dependencies{Repository: r, Chain: c}, Settings{})
	changed, err := s.reconcilePartialScanRange(context.Background(), cursor, ancestor, map[uint64]blockHeader{100: {Number: 100, Hash: testHashB, Timestamp: at}}, 100, 100)
	if err != nil || !changed || r.reorgAt != 99 || r.limit != 2 {
		t.Fatalf("partial reorg did not use bounded verified ancestor: changed=%v at=%d limit=%d err=%v", changed, r.reorgAt, r.limit, err)
	}
	// A failed block read alone is not evidence to discard the range.
	r.reorgAt = 0
	if _, err := s.reconcilePartialScanRange(context.Background(), cursor, ancestor, map[uint64]blockHeader{}, 100, 100); err == nil || r.reorgAt != 0 {
		t.Fatalf("request failure inferred a reorg: at=%d err=%v", r.reorgAt, err)
	}
}

func TestPartialScanRangeDoesNotPromoteChangedPredecessor(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	ancestor := blockHeader{Number: 99, Hash: testHashC, ParentHash: testHashA, Timestamp: at.Add(-time.Second)}
	cursor := store.MarketChainCursor{NextBlockNumber: 100, LastBlockHash: &ancestor.Hash}
	r := &filteredCanonicalRepository{fakeRepository: &fakeRepository{candidates: []store.MarketScannedBlock{{BlockNumber: 100, BlockHash: testHashA}}}}
	c := &fakeChain{blocks: map[uint64]map[string]any{99: {"number": "0x63", "hash": testHashB, "parentHash": testHashA, "timestamp": "0x6553f0ff"}}}
	s := New(Dependencies{Repository: r, Chain: c}, Settings{})
	changed, err := s.reconcilePartialScanRange(context.Background(), cursor, ancestor, map[uint64]blockHeader{100: {Number: 100, Hash: testHashB, Timestamp: at}}, 100, 100)
	if !changed || !errors.Is(err, ErrNoCanonicalAncestor) || r.reorgAt != 0 {
		t.Fatalf("changed predecessor became a fake ancestor: changed=%v at=%d err=%v", changed, r.reorgAt, err)
	}
}

func TestPartialScanRangeIgnoresCanonicalRowsOutsideCurrentRange(t *testing.T) {
	r := &filteredCanonicalRepository{fakeRepository: &fakeRepository{candidates: []store.MarketScannedBlock{{BlockNumber: 99, BlockHash: testHashA}}}}
	s := New(Dependencies{Repository: r}, Settings{})
	changed, err := s.reconcilePartialScanRange(context.Background(), store.MarketChainCursor{}, blockHeader{}, nil, 100, 100)
	if changed || err != nil || r.reorgAt != 0 {
		t.Fatalf("unrelated canonical row changed the range: changed=%v err=%v", changed, err)
	}
}
