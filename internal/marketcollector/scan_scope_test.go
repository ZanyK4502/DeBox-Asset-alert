package marketcollector

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

var activationEpoch = time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)

func activationHash(number uint64) string { return fmt.Sprintf("0x%064x", number+1) }

func activationHeader(number uint64) blockHeader {
	parent := activationHash(0)
	if number > 0 {
		parent = activationHash(number - 1)
	}
	return blockHeader{Number: number, Hash: activationHash(number), ParentHash: parent, Timestamp: activationEpoch.Add(time.Duration(number) * time.Second)}
}

type activationChain struct {
	*fakeChain
	latest     uint64
	headCalls  int
	blockCalls int
	readCounts map[uint64]int
	mutate     func(blockHeader, int) (blockHeader, error)
	logFilters []chain.LogFilter
}

func (c *activationChain) LatestBlockNumber(context.Context, string, string) (uint64, error) {
	c.headCalls++
	return c.latest, nil
}

func (c *activationChain) BlockByNumber(_ context.Context, number uint64, _ bool, _, _ string) (map[string]any, error) {
	c.blockCalls++
	if c.readCounts == nil {
		c.readCounts = make(map[uint64]int)
	}
	c.readCounts[number]++
	header := activationHeader(number)
	if c.mutate != nil {
		var err error
		header, err = c.mutate(header, c.readCounts[number])
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{"number": fmt.Sprintf("0x%x", header.Number), "hash": header.Hash, "parentHash": header.ParentHash,
		"timestamp": fmt.Sprintf("0x%x", header.Timestamp.Unix())}, nil
}

func (c *activationChain) Logs(_ context.Context, _, _ string, filter chain.LogFilter) ([]chain.RPCLog, error) {
	c.logFilters = append(c.logFilters, filter)
	return nil, nil
}

type activationRepository struct {
	*fakeRepository
	state           store.MarketChainCollectionState
	observeErr      error
	advanceErr      error
	observeCalls    int
	cursorReads     int
	healthCalls     int
	confirmCalls    int
	coverageCalls   int
	ordinaryWrites  int
	advances        []store.AdvanceMarketChainCursorOverIdleParams
	acknowledgments []store.AcknowledgeMarketChainCollectionIdleParams
	onObserve       func()
}

func (r *activationRepository) ObserveMarketChainCollection(context.Context, string, int64) (store.MarketChainCollectionState, error) {
	r.observeCalls++
	if r.onObserve != nil {
		r.onObserve()
	}
	return r.state, r.observeErr
}

func (r *activationRepository) AdvanceMarketChainCursorOverIdle(_ context.Context, p store.AdvanceMarketChainCursorOverIdleParams) (store.MarketChainCursor, error) {
	r.advances = append(r.advances, p)
	if r.advanceErr != nil {
		return store.MarketChainCursor{}, r.advanceErr
	}
	if r.cursor == nil || r.cursor.NextBlockNumber != p.NextBlockNumber {
		hash := p.LastBlockHash
		r.cursor = &store.MarketChainCursor{ChainKey: p.ChainKey, ChainID: p.ChainID, CursorKey: p.CursorKey,
			NextBlockNumber: p.NextBlockNumber, SafeBlockNumber: p.SafeBlockNumber, LastBlockHash: &hash}
	}
	r.state.PendingIdle = nil
	r.state.Revision++
	return *r.cursor, nil
}

func (r *activationRepository) AcknowledgeMarketChainCollectionIdle(_ context.Context, p store.AcknowledgeMarketChainCollectionIdleParams) error {
	r.acknowledgments = append(r.acknowledgments, p)
	if r.advanceErr != nil {
		return r.advanceErr
	}
	r.state.PendingIdle = nil
	r.state.Revision++
	return nil
}

func (r *activationRepository) GetMarketChainCursor(ctx context.Context, id int64, key string) (*store.MarketChainCursor, error) {
	r.cursorReads++
	return r.fakeRepository.GetMarketChainCursor(ctx, id, key)
}

func (r *activationRepository) AdvanceMarketChainCursor(ctx context.Context, p store.AdvanceMarketChainCursorParams) (store.MarketChainCursor, error) {
	r.ordinaryWrites++
	return r.fakeRepository.AdvanceMarketChainCursor(ctx, p)
}

func (r *activationRepository) MarkMarketBlockConfirmed(context.Context, int64, int64) (int64, error) {
	r.confirmCalls++
	return 0, nil
}

func (r *activationRepository) RecordMarketScanCoverage(context.Context, store.RecordMarketScanCoverageParams) error {
	r.coverageCalls++
	return nil
}

func (r *activationRepository) RecordMarketProviderHealth(context.Context, store.RecordMarketProviderHealthParams) (store.MarketProviderHealth, error) {
	r.healthCalls++
	return store.MarketProviderHealth{}, nil
}

func activationFixture(next int64) (*Service, *activationRepository, *activationChain) {
	through := activationEpoch.Add(500 * time.Second)
	r := &activationRepository{fakeRepository: &fakeRepository{
		activeProjects: []store.MarketProject{{TokenAddress: "0x1111111111111111111111111111111111111111", TokenDecimals: 0}},
	}, state: store.MarketChainCollectionState{ChainKey: "bsc", ChainID: 56, Revision: 7, ActiveTargetCount: 1, Phase: "active",
		PendingIdle: &store.MarketCollectionIdleInterval{ID: 4, ChainKey: "bsc", ChainID: 56, FromAt: activationEpoch.Add(50 * time.Second), ThroughAt: &through}}}
	if next != 0 {
		hash := activationHash(uint64(next - 1))
		r.cursor = &store.MarketChainCursor{ChainKey: "bsc", ChainID: 56, CursorKey: DefaultCursorKey, NextBlockNumber: next, LastBlockHash: &hash}
	}
	c := &activationChain{fakeChain: &fakeChain{}, latest: 1015}
	s := New(Dependencies{Repository: r, Chain: c}, Settings{Enabled: true, ChainKey: "bsc", ChainID: 56, InitialLookback: 20, ScanBatchSize: 1})
	return s, r, c
}

func activationPrevious(r *activationRepository) *blockHeader {
	if r.cursor == nil {
		return nil
	}
	header := activationHeader(uint64(r.cursor.NextBlockNumber - 1))
	return &header
}

func TestCollectionActivationKeepsActivationLookbackInsteadOfLateStartup(t *testing.T) {
	for _, next := range []int64{0, 100, 490} {
		t.Run(fmt.Sprintf("old_next_%d", next), func(t *testing.T) {
			s, r, c := activationFixture(next)
			cursor, previous, deferred, err := s.prepareCollectionActivation(context.Background(), r.state, r.cursor, activationPrevious(r), 1000)
			want := int64(481) // first activation block 500, with the original 20-block lookback
			if next > want {
				want = next
			}
			if err != nil || deferred || cursor == nil || cursor.NextBlockNumber != want || previous.Number != uint64(want-1) {
				t.Fatalf("activation = cursor %+v, previous %+v, deferred %v, error %v", cursor, previous, deferred, err)
			}
			if len(r.advances) != 1 || len(r.acknowledgments) != 0 || r.ordinaryWrites != 0 || r.confirmCalls != 0 || r.coverageCalls != 0 {
				t.Fatalf("activation used ordinary advancement/confirmation: %+v", r)
			}
			p := r.advances[0]
			if p.ExpectedNextBlock != next || p.NextBlockNumber != want || p.StateRevision != 7 || p.IntervalID != 4 || p.LastBlockHash != activationHash(uint64(want-1)) || p.LastBlockParentHash != activationHash(uint64(want-2)) {
				t.Fatalf("lost lifecycle/cursor/anchor evidence: %+v", p)
			}
			if next > 0 && next < want && (p.FirstSkippedBlockAt.Before(activationEpoch.Add(50*time.Second)) || !p.LastSkippedBlockAt.Before(activationEpoch.Add(500*time.Second))) {
				t.Fatalf("skip is outside proven idle interval: %+v", p)
			}
			if c.blockCalls > 20 {
				t.Fatalf("activation search was not bounded: %d header calls", c.blockCalls)
			}
		})
	}
}

func TestCollectionActivationSearchHasLogarithmicBound(t *testing.T) {
	const high int64 = 1 << 40
	const cutoff int64 = high/2 + 7
	header := func(number int64) blockHeader {
		return blockHeader{Number: uint64(number), Hash: activationHash(uint64(number)),
			ParentHash: activationHash(uint64(number - 1)), Timestamp: time.Unix(number, 0).UTC()}
	}
	reads := 0
	activation, err := findCollectionActivation(1, high, time.Unix(cutoff, 0).UTC(), header(1), header(high), func(number int64) (blockHeader, error) {
		reads++
		return header(number), nil
	})
	if err != nil || activation != cutoff || reads > 40 {
		t.Fatalf("large idle range was not bounded: activation=%d reads=%d error=%v", activation, reads, err)
	}
	_, err = findCollectionActivation(cutoff-1, cutoff, time.Unix(cutoff, 0).UTC(), header(cutoff-1), blockHeader{
		Number: uint64(cutoff), Hash: activationHash(uint64(cutoff)), ParentHash: testHashA, Timestamp: time.Unix(cutoff, 0).UTC(),
	}, func(int64) (blockHeader, error) {
		t.Fatal("adjacent boundary should not require another read")
		return blockHeader{}, nil
	})
	if err == nil {
		t.Fatal("unlinked activation boundary accepted")
	}
}

func TestCollectionActivationInitialFailureDoesNotLoseExplicitStart(t *testing.T) {
	s, r, _ := activationFixture(0)
	failure := errors.New("atomic activation write failed")
	r.advanceErr = failure
	if _, _, _, err := s.prepareCollectionActivation(context.Background(), r.state, nil, nil, 1000); !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
	if r.cursor != nil || r.state.PendingIdle == nil || len(r.acknowledgments) != 0 {
		t.Fatal("failed initialization consumed idle proof or persisted a partial start")
	}
	r.advanceErr = nil
	cursor, _, deferred, err := s.prepareCollectionActivation(context.Background(), r.state, nil, nil, 2000)
	if err != nil || deferred || cursor.NextBlockNumber != 481 || len(r.advances) != 2 {
		t.Fatalf("retry used late safe height instead of activation: %+v, %v, %v", cursor, deferred, err)
	}
}

func TestCollectionActivationUnsafeHeadPreservesCursor(t *testing.T) {
	for _, tt := range []struct {
		name       string
		next       int64
		safe       int64
		deferScan  bool
		maxHeaders int
	}{
		{"activation not safe", 100, 400, true, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, r, c := activationFixture(tt.next)
			original := r.cursor
			cursor, _, deferred, err := s.prepareCollectionActivation(context.Background(), r.state, r.cursor, activationPrevious(r), tt.safe)
			if err != nil || deferred != tt.deferScan || cursor != original || len(r.advances) != 0 || len(r.acknowledgments) != 0 || c.blockCalls > tt.maxHeaders {
				t.Fatalf("unsafe range moved or over-read: cursor=%+v deferred=%v error=%v calls=%d", cursor, deferred, err, c.blockCalls)
			}
		})
	}
}

func TestCollectionActivationAlreadyPassedOnlyAcknowledges(t *testing.T) {
	s, r, c := activationFixture(600)
	original := r.cursor
	cursor, previous, deferred, err := s.prepareCollectionActivation(context.Background(), r.state, r.cursor, activationPrevious(r), 1000)
	if err != nil || deferred || cursor != original || previous.Number != 599 || len(r.advances) != 0 || len(r.acknowledgments) != 1 || c.blockCalls != 1 {
		t.Fatalf("already-passed interval moved cursor: cursor=%+v deferred=%v error=%v reads=%d", cursor, deferred, err, c.blockCalls)
	}
	if p := r.acknowledgments[0]; p.ExpectedNextBlock != 600 || p.NextBlockAt == nil || !p.NextBlockAt.Equal(activationEpoch.Add(600*time.Second)) {
		t.Fatalf("incorrect acknowledgement evidence: %+v", p)
	}
}

func TestCollectionZeroLengthIdleCannotBlockActivation(t *testing.T) {
	for _, next := range []int64{0, 100} {
		s, r, _ := activationFixture(next)
		through := r.state.PendingIdle.FromAt
		r.state.PendingIdle.ThroughAt = &through
		cursor, _, deferred, err := s.prepareCollectionActivation(context.Background(), r.state, r.cursor, activationPrevious(r), 1000)
		want := int64(31) // activation 50, preserving the original 20-block lookback
		if next > 0 {
			want = next
		}
		if err != nil || deferred || cursor.NextBlockNumber != want || r.state.PendingIdle != nil {
			t.Fatalf("zero-length idle blocked activation: next=%d cursor=%+v deferred=%v error=%v", next, cursor, deferred, err)
		}
		if next > 0 && (len(r.advances) != 0 || len(r.acknowledgments) != 1) {
			t.Fatal("empty idle interval moved an existing cursor")
		}
	}
}

func TestCollectionActivationStopsOnStateOrCanonicalEvidenceChange(t *testing.T) {
	for _, name := range []string{"state changed", "cursor changed", "read failed", "anchor changed", "activation changed", "timestamp contradiction"} {
		t.Run(name, func(t *testing.T) {
			s, r, c := activationFixture(100)
			var want error
			switch name {
			case "state changed":
				want = store.ErrMarketCollectionStateChanged
				r.advanceErr = want
			case "cursor changed":
				want = store.ErrMarketCollectionCursorConflict
				r.advanceErr = want
			case "read failed":
				want = errors.New("unavailable header")
				c.mutate = func(h blockHeader, _ int) (blockHeader, error) {
					if h.Number == 1000 {
						return h, want
					}
					return h, nil
				}
			case "anchor changed", "activation changed":
				height := uint64(480)
				if name == "activation changed" {
					height = 500
				}
				c.mutate = func(h blockHeader, count int) (blockHeader, error) {
					if h.Number == height && count == 2 {
						h.Hash = testHashC
					}
					return h, nil
				}
			case "timestamp contradiction":
				c.mutate = func(h blockHeader, _ int) (blockHeader, error) {
					if h.Number == 550 {
						h.Timestamp = activationEpoch
					}
					return h, nil
				}
			}
			original := r.cursor
			_, _, _, err := s.prepareCollectionActivation(context.Background(), r.state, r.cursor, activationPrevious(r), 1000)
			if err == nil || want != nil && !errors.Is(err, want) || r.cursor != original || r.state.PendingIdle == nil || r.confirmCalls != 0 || r.ordinaryWrites != 0 {
				t.Fatalf("changed evidence consumed progress: error=%v, cursor=%+v", err, r.cursor)
			}
			if name != "state changed" && name != "cursor changed" && len(r.advances) != 0 {
				t.Fatal("contradictory evidence reached atomic cursor advance")
			}
		})
	}
}

func TestCollectionIdleObservationStaysInsideScannerLockWithoutProviderReads(t *testing.T) {
	s, r, c := activationFixture(100)
	r.activeProjects = nil
	r.state.ActiveTargetCount, r.state.Phase = 0, "idle"
	r.state.PendingIdle.ThroughAt = nil
	held := false
	r.onObserve = func() {
		if !held {
			t.Fatal("idle proof observed outside log-scanner lock")
		}
	}
	s.tryLock = func(_ context.Context, task string) (Lock, bool, error) {
		if task != "market:bsc:log-scanner" {
			t.Fatalf("changed scanner lock key: %s", task)
		}
		held = true
		return activationTestLock{release: func() { held = false }}, true, nil
	}
	for range 3 {
		if err := s.ScanLogs(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if r.observeCalls != 3 || r.cursorReads != 0 || r.ordinaryWrites != 0 || len(r.advances) != 0 || c.headCalls != 0 || c.blockCalls != 0 || len(c.logFilters) != 0 || r.healthCalls != 3 {
		t.Fatalf("empty scanner performed collection work or changed existing health behavior: repository=%+v chain=%+v", r, c)
	}
	if r.cursor.NextBlockNumber != 100 {
		t.Fatal("empty target observation moved a legacy cursor")
	}
}

type activationTestLock struct{ release func() }

func (l activationTestLock) Unlock(context.Context) error { l.release(); return nil }

func TestCollectionObservationMismatchStopsBeforeProviderWork(t *testing.T) {
	s, r, c := activationFixture(100)
	r.state.ActiveTargetCount = 2
	if err := s.ScanLogs(context.Background()); !errors.Is(err, store.ErrMarketCollectionStateChanged) {
		t.Fatalf("scope mismatch error=%v", err)
	}
	if c.headCalls != 0 || c.blockCalls != 0 || r.cursorReads != 0 || len(r.advances) != 0 {
		t.Fatal("stale parser scope reached provider work or changed progress")
	}
	s.tryLock = func(context.Context, string) (Lock, bool, error) { return nil, false, nil }
	if err := s.ScanLogs(context.Background()); err != nil || r.observeCalls != 1 {
		t.Fatalf("unacquired lock observed lifecycle: calls=%d error=%v", r.observeCalls, err)
	}
}

func TestCollectionUnknownPrefixIsRetainedAndNewRulesScanRecentBlocks(t *testing.T) {
	for _, test := range []struct {
		name                   string
		start                  int64
		idleFrom               int64
		earlierActivation      int64
		wantStart, wantThrough int64
		wantIdle               bool
	}{
		{"old cursor before observed idle", 20, 50, 0, 20, 49, true},
		{"no idle proof before activation", 20, 500, 0, 20, 480, false},
		{"lookback overlaps old period", 20, 490, 0, 20, 480, false},
		{"multiple activations before first scan", 0, 360, 80, 61, 359, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, r, c := activationFixture(test.start)
			r.state.PendingIdle.FromAt = activationEpoch.Add(time.Duration(test.idleFrom) * time.Second)
			if test.earlierActivation > 0 {
				earlier := activationEpoch.Add(time.Duration(test.earlierActivation) * time.Second)
				r.state.PendingIdle.EarlierActivationAt = &earlier
			}
			for range 2 {
				if err := s.ScanLogs(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if len(r.advances) != 1 || r.advances[0].NextBlockNumber != 481 || r.cursor.NextBlockNumber != 483 || r.confirmCalls != 2 || r.coverageCalls != 2 {
				t.Fatalf("live cursor failed to scan the activation range: %+v", r)
			}
			p := r.advances[0]
			h := p.HistoryPrefix
			if h == nil || p.HistoryStartBlock != test.wantStart || h.ThroughBlock != test.wantThrough || h.PreviousHash != activationHash(uint64(test.wantStart-1)) ||
				h.ThroughHash != activationHash(uint64(test.wantThrough)) || (h.FirstIdleAt != nil) != test.wantIdle {
				t.Fatalf("history responsibility %+v, params %+v", h, p)
			}
			for _, filter := range c.logFilters {
				if *filter.FromBlock < 481 || *filter.ToBlock > 482 {
					t.Fatalf("live filter blocked by history %+v", filter)
				}
			}
		})
	}
}

func TestCollectionActivationNormalScanStartsAtRecordedLookback(t *testing.T) {
	for _, profile := range chain.SupportedChains() {
		t.Run(profile.Key, func(t *testing.T) {
			s, r, c := activationFixture(100)
			s.settings.ChainKey, s.settings.ChainID = profile.Key, profile.ChainID
			r.state.ChainKey, r.state.ChainID = profile.Key, profile.ChainID
			r.state.PendingIdle.ChainKey, r.state.PendingIdle.ChainID = profile.Key, profile.ChainID
			r.cursor.ChainKey, r.cursor.ChainID = profile.Key, profile.ChainID
			if err := s.ScanLogs(context.Background()); err != nil {
				t.Fatal(err)
			}
			if r.cursor.NextBlockNumber != 482 || len(r.advances) != 1 || r.advances[0].NextBlockNumber != 481 || r.confirmCalls != 1 || r.coverageCalls != 1 || r.ordinaryWrites != 1 {
				t.Fatalf("activation did not rejoin bounded canonical scan: %+v", r)
			}
			for _, filter := range c.logFilters {
				if *filter.FromBlock != 481 || *filter.ToBlock != 481 {
					t.Fatalf("post-activation scan range=%+v", filter)
				}
			}
		})
	}
}
