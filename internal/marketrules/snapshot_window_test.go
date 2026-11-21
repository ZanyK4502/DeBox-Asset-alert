package marketrules

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketwindow"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestSnapshotWindowBaselineRequiresConfirmedThirtySecondAlignment(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	window := int32(1440)
	target := now.Add(-24 * time.Hour)
	state, _ := json.Marshal(ruleState{LastSnapshotID: 5, ConditionActive: true, PendingCrossing: true})
	for _, test := range []struct {
		name   string
		lag    time.Duration
		status string
		want   bool
	}{
		{"exact", 0, marketquality.StatusLive, true},
		{"inclusive 30 seconds", 30 * time.Second, marketquality.StatusCached, true},
		{"beyond 30 seconds", 30*time.Second + time.Microsecond, marketquality.StatusLive, false},
		{"future is forbidden", -time.Microsecond, marketquality.StatusLive, false},
		{"fault marker not skipped", 0, marketquality.StatusStaleFallback, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			baselineAt := target.Add(-test.lag)
			olderAt := target.Add(-time.Second)
			proof := liveQuoteFixture(baselineAt)
			proof.Status = test.status
			latestPrice, baselinePrice := "120", "100"
			input := EvaluationInput{Rule: store.MarketRule{RuleType: plans.MarketPriceIncrease, WindowMinutes: &window, ThresholdValue: "10", State: state, RepeatWhileActive: true},
				Snapshots: []store.MarketSnapshot{
					{ID: 9, CapturedAt: now, QuoteQuality: liveQuoteFixture(now), PriceUSD: &latestPrice},
					{ID: 8, CapturedAt: baselineAt, QuoteQuality: proof, PriceUSD: &baselinePrice},
				}, Now: now}
			if test.status == marketquality.StatusStaleFallback {
				input.Snapshots = append(input.Snapshots, store.MarketSnapshot{ID: 7, CapturedAt: olderAt, QuoteQuality: liveQuoteFixture(olderAt), PriceUSD: &baselinePrice})
			}
			result, err := Evaluate(input)
			if err != nil || (len(result.Triggers) == 1) != test.want {
				t.Fatalf("aligned evaluation=%d,%v want=%t", len(result.Triggers), err, test.want)
			}
			if !test.want && !bytes.Equal(result.State, state) {
				t.Fatalf("missing baseline advanced condition/pending/cursor: %s", result.State)
			}
		})
	}
}

type indexedSnapshotTestRepository struct {
	Repository
	baseline store.MarketSnapshot
	at       time.Time
	target   store.MarketRuleTarget
}

func (r *indexedSnapshotTestRepository) MarketSnapshotBaselineForTarget(_ context.Context, target store.MarketRuleTarget, at time.Time) (*store.MarketSnapshot, error) {
	r.target, r.at = target, at
	return &r.baseline, nil
}

func TestSnapshotWindowUsesOneIndexedBaselineAndOriginalQuoteClock(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	fetched := now.Add(-time.Minute)
	minutes := int32(1440)
	baselineAt := fetched.Add(-24 * time.Hour)
	r := &indexedSnapshotTestRepository{baseline: store.MarketSnapshot{ID: 1, CapturedAt: baselineAt, QuoteQuality: liveQuoteFixture(baselineAt)}}
	s := &Service{repository: r}
	loads := 0
	target := store.MarketRuleTarget{ChainID: 56, TokenAddress: "token"}
	got, err := s.loadRuleSnapshots(context.Background(), store.MarketRule{RuleType: plans.MarketPriceIncrease, WindowMinutes: &minutes}, target,
		func(limit int) ([]store.MarketSnapshot, error) {
			loads++
			if limit != 1 {
				t.Fatalf("loaded %d snapshots instead of one", limit)
			}
			return []store.MarketSnapshot{{ID: 10001, CapturedAt: now, QuoteQuality: liveQuoteFixture(fetched)}}, nil
		})
	if err != nil || loads != 1 || len(got) != 2 || !r.at.Equal(baselineAt) || r.target.ChainID != target.ChainID {
		t.Fatalf("indexed 24h lookup=%#v,loads=%d,at=%s,err=%v", got, loads, r.at, err)
	}
}

func TestUnsupportedRuleWindowIsNotDefaultedOrRewritten(t *testing.T) {
	window := int32(15)
	state := json.RawMessage(`{"last_snapshot_id":9,"condition_active":true}`)
	result, err := Evaluate(EvaluationInput{Rule: store.MarketRule{RuleType: plans.MarketVolumeAbove, WindowMinutes: &window, ThresholdValue: "1", State: state}})
	if !errors.Is(err, marketwindow.ErrUnsupported) || len(result.Triggers) != 0 || window != 15 || string(state) != `{"last_snapshot_id":9,"condition_active":true}` {
		t.Fatalf("unsupported period changed original configuration: %#v,%v", result, err)
	}
}

func TestSnapshotWindowFiltersLegacyPoolsBeforeChoosingClock(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	poolID, window := int64(7), int32(60)
	selectedAt := now.Add(-time.Minute)
	r := &indexedSnapshotTestRepository{baseline: store.MarketSnapshot{ID: 1, MarketPoolID: poolID}}
	s := &Service{repository: r}
	got, err := s.loadRuleSnapshots(context.Background(), store.MarketRule{RuleType: plans.MarketPriceIncrease, WindowMinutes: &window}, store.MarketRuleTarget{MarketPoolID: &poolID}, func(int) ([]store.MarketSnapshot, error) {
		return []store.MarketSnapshot{
			{ID: 10, MarketPoolID: 8, CapturedAt: now, QuoteQuality: liveQuoteFixture(now)},
			{ID: 9, MarketPoolID: poolID, CapturedAt: selectedAt, QuoteQuality: liveQuoteFixture(selectedAt)},
		}, nil
	})
	if err != nil || len(got) != 2 || got[0].ID != 9 || !r.at.Equal(selectedAt.Add(-time.Hour)) {
		t.Fatalf("other pool selected window clock: %#v,%s,%v", got, r.at, err)
	}
}
