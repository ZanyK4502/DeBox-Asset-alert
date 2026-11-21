package marketcollector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type observationRepository struct {
	*confirmationFailureRepository
	advanceFailure error
	returnedNext   int64
}

func (r *observationRepository) AdvanceMarketChainCursor(ctx context.Context, p store.AdvanceMarketChainCursorParams) (store.MarketChainCursor, error) {
	if r.advanceFailure != nil {
		return store.MarketChainCursor{}, r.advanceFailure
	}
	cursor, err := r.fakeRepository.AdvanceMarketChainCursor(ctx, p)
	if r.returnedNext != 0 {
		cursor.NextBlockNumber = r.returnedNext
	}
	return cursor, err
}

func observationFixture(log *bytes.Buffer) (*Service, *observationRepository) {
	r := &observationRepository{confirmationFailureRepository: &confirmationFailureRepository{fakeRepository: &fakeRepository{
		activeProjects: []store.MarketProject{{TokenAddress: "0x1111111111111111111111111111111111111111", TokenDecimals: 0}},
		cursor:         &store.MarketChainCursor{ChainKey: "bsc", ChainID: 56, CursorKey: DefaultCursorKey, NextBlockNumber: 100},
	}}}
	c := aheadCursorChain{&fakeChain{blocks: map[uint64]map[string]any{
		99:  {"number": "0x63", "hash": testHashC, "parentHash": testHashA, "timestamp": "0x6553f0ff"},
		100: {"number": "0x64", "hash": testHashB, "parentHash": testHashC, "timestamp": "0x6553f100"},
	}}}
	s := New(Dependencies{Repository: r, Chain: c, Logger: slog.New(slog.NewJSONHandler(log, nil))},
		Settings{Enabled: true, ChainKey: "bsc", ChainID: 56, ScanBatchSize: 1})
	return s, r
}

func TestScannerObservationRecordsOnlyCompletedRange(t *testing.T) {
	var output bytes.Buffer
	s, r := observationFixture(&output)
	before := time.Now().UTC()
	if err := s.ScanLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := time.Now().UTC()
	if r.cursor.NextBlockNumber != 101 || r.confirmCalls != 1 || r.coverageCalls != 1 {
		t.Fatalf("scan completion changed: cursor=%d confirm=%d coverage=%d", r.cursor.NextBlockNumber, r.confirmCalls, r.coverageCalls)
	}
	if strings.Count(output.String(), "\n") != 1 {
		t.Fatalf("expected one completed-range record: %s", output.String())
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]string{"msg": "market scan completed", "chain_key": "bsc", "cursor_key": DefaultCursorKey} {
		var got string
		if err := json.Unmarshal(record[field], &got); err != nil || got != want {
			t.Fatalf("%s = %q, error=%v", field, got, err)
		}
	}
	for field, want := range map[string]int64{"chain_id": 56, "from_block": 100, "through_block": 100, "next_block": 101, "safe_block": 185, "backlog_blocks": 85} {
		var got int64
		if err := json.Unmarshal(record[field], &got); err != nil || got != want {
			t.Fatalf("%s = %d, want %d, error=%v", field, got, want, err)
		}
	}
	var observed, completed time.Time
	if err := json.Unmarshal(record["safe_observed_at"], &observed); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(record["completed_at"], &completed); err != nil {
		t.Fatal(err)
	}
	if observed.Before(before) || completed.Before(observed) || completed.After(after) {
		t.Fatalf("invalid observation times: %s .. %s", observed, completed)
	}
	var duration int64
	if err := json.Unmarshal(record["work_duration_ms"], &duration); err != nil || duration < 0 {
		t.Fatalf("invalid work duration: %d, %v", duration, err)
	}
	if len(record) != 14 || strings.Contains(output.String(), "0x") {
		t.Fatalf("unexpected fields or address/hash in observation: %s", output.String())
	}
}

func TestScannerObservationDoesNotClaimDeferredOrFailedWork(t *testing.T) {
	failure := errors.New("observation test write failure")
	tests := []struct {
		name  string
		setup func(*Service, *observationRepository)
		want  error
	}{
		{"no targets", func(_ *Service, r *observationRepository) { r.activeProjects = nil }, nil},
		{"caught up", func(_ *Service, r *observationRepository) { r.cursor.NextBlockNumber = 186 }, nil},
		{"lock owned elsewhere", func(s *Service, _ *observationRepository) {
			s.tryLock = func(context.Context, string) (Lock, bool, error) { return nil, false, nil }
		}, nil},
		{"confirmation pending", func(_ *Service, r *observationRepository) { r.failure = store.ErrMarketTradeConfirmationPending }, nil},
		{"confirmation failed", func(_ *Service, r *observationRepository) { r.failure = failure }, failure},
		{"coverage pending", func(_ *Service, r *observationRepository) {
			r.coverageFailure = store.ErrMarketTradeConfirmationPending
		}, nil},
		{"coverage failed", func(_ *Service, r *observationRepository) { r.coverageFailure = failure }, failure},
		{"cursor write failed", func(_ *Service, r *observationRepository) { r.advanceFailure = failure }, failure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			s, r := observationFixture(&output)
			tt.setup(s, r)
			if err := s.ScanLogs(context.Background()); !errors.Is(err, tt.want) {
				t.Fatalf("error=%v, want %v", err, tt.want)
			}
			if output.Len() != 0 {
				t.Fatalf("unfinished scan emitted success: %s", output.String())
			}
		})
	}
}

func TestScannerObservationUsesReturnedCursor(t *testing.T) {
	var output bytes.Buffer
	s, r := observationFixture(&output)
	r.returnedNext = 777
	if err := s.ScanLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	var record struct {
		Next int64 `json:"next_block"`
	}
	if err := json.Unmarshal(output.Bytes(), &record); err != nil || record.Next != 777 {
		t.Fatalf("returned cursor was replaced by requested cursor: %d, %v", record.Next, err)
	}
}

func TestScannerObservationReportsActualRangeAtSafeHead(t *testing.T) {
	var output bytes.Buffer
	r := &fakeRepository{
		activeProjects: []store.MarketProject{{TokenAddress: "0x1111111111111111111111111111111111111111", TokenDecimals: 0}},
		cursor:         &store.MarketChainCursor{ChainKey: "bsc", ChainID: 56, CursorKey: DefaultCursorKey, NextBlockNumber: 100},
	}
	c := aheadCursorChain{&fakeChain{blocks: map[uint64]map[string]any{
		99:  {"number": "0x63", "hash": testHashC, "parentHash": testHashA, "timestamp": "0x6553f0ff"},
		185: {"number": "0xb9", "hash": testHashB, "parentHash": testHashC, "timestamp": "0x6553f100"},
	}}}
	s := New(Dependencies{Repository: r, Chain: c, Logger: slog.New(slog.NewJSONHandler(&output, nil))},
		Settings{Enabled: true, ChainKey: "bsc", ChainID: 56, ScanBatchSize: 500})
	if err := s.ScanLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	var record struct {
		From    int64 `json:"from_block"`
		Through int64 `json:"through_block"`
		Next    int64 `json:"next_block"`
		Safe    int64 `json:"safe_block"`
		Backlog int64 `json:"backlog_blocks"`
	}
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record.From != 100 || record.Through != 185 || record.Next != 186 || record.Safe != 185 || record.Backlog != 0 {
		t.Fatalf("configured batch replaced actual completed range: %+v", record)
	}
}
