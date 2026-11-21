package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestPostgresNotificationBoundaryIsExplicitMonotoneAndDoesNotMoveBusinessState(t *testing.T) {
	database, pool, _ := tradeFactsFixture(t)
	ctx := context.Background()
	rule := tradeRuleFixture(t, database)
	before, err := database.GetNotificationRecoveryBoundary(ctx)
	if err != nil || before != nil {
		t.Fatalf("migration configured a boundary: %#v,%v", before, err)
	}
	at := time.Date(2026, 11, 14, 14, 0, 0, 0, time.UTC)
	gap := NotificationRecoveryGap{ChainID: 56, ChainKey: "bsc", FromBlock: 100, ThroughBlock: 200, ThroughAt: at.Add(-time.Second), CutoffAt: at, Reason: "reviewed history intentionally not collected"}
	if _, err := database.RecordNotificationRecoveryGap(ctx, gap); !errors.Is(err, ErrNotificationBoundaryConflict) {
		t.Fatalf("gap recorded without explicit boundary: %v", err)
	}
	first, err := database.ConfigureNotificationRecoveryBoundary(ctx, at, 0, "fixture cutover, not production")
	if err != nil || first.Revision != 1 {
		t.Fatalf("configure=%#v,%v", first, err)
	}
	if _, err := database.ConfigureNotificationRecoveryBoundary(ctx, at.Add(-time.Minute), 1, "backwards"); !errors.Is(err, ErrNotificationBoundaryConflict) {
		t.Fatalf("boundary moved backwards: %v", err)
	}
	if _, err := database.ConfigureNotificationRecoveryBoundary(ctx, at.Add(time.Minute), 0, "stale CAS"); !errors.Is(err, ErrNotificationBoundaryConflict) {
		t.Fatalf("stale configuration accepted: %v", err)
	}
	if same, err := database.ConfigureNotificationRecoveryBoundary(ctx, at, 1, "idempotent retry"); err != nil || same.Revision != 1 || same.Reason != first.Reason {
		t.Fatalf("same boundary changed audit: %#v,%v", same, err)
	}
	one, err := database.RecordNotificationRecoveryGap(ctx, gap)
	if err != nil {
		t.Fatal(err)
	}
	two, err := database.RecordNotificationRecoveryGap(ctx, gap)
	if err != nil || one.ID != two.ID {
		t.Fatalf("gap retry duplicated evidence: %#v,%v", two, err)
	}
	gap.Reason = "silently change original reason"
	if _, err := database.RecordNotificationRecoveryGap(ctx, gap); !errors.Is(err, ErrNotificationBoundaryConflict) {
		t.Fatalf("gap history overwritten: %v", err)
	}
	if _, err := database.ConfigureNotificationRecoveryBoundary(ctx, at.Add(time.Minute), 1, "another explicit episode"); err != nil {
		t.Fatal(err)
	}
	var state string
	var historical int
	if err := pool.QueryRow(ctx, `SELECT state::text FROM market_rules WHERE id=$1`, rule.ID).Scan(&state); err != nil || state != "{}" {
		t.Fatalf("boundary advanced rule state: %s,%v", state, err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM notification_recovery_boundary_history`).Scan(&historical); err != nil || historical != 2 {
		t.Fatalf("configuration audit missing: %d,%v", historical, err)
	}
}

func TestPostgresDailySummaryRecoveryCoverageDoesNotConfuseMissingProofWithZero(t *testing.T) {
	database, _, pools := tradeFactsFixture(t)
	ctx := context.Background()
	_ = tradeRuleFixture(t, database)
	from := time.Date(2026, 11, 14, 1, 0, 0, 0, time.UTC)
	through := from.Add(time.Hour)
	coverage, err := database.LoadDailySummaryRecoveryCoverage(ctx, "trade-owner", from, through)
	if err != nil || coverage.Status != "incomplete" || len(coverage.Chains) != 1 || coverage.Chains[0].Status != "unknown" {
		t.Fatalf("empty records claimed complete zero: %#v,%v", coverage, err)
	}
	for _, b := range []struct {
		n  int64
		at time.Time
	}{{99, from.Add(-time.Second)}, {200, through.Add(time.Second)}} {
		if _, err := database.UpsertMarketScannedBlock(ctx, UpsertMarketScannedBlockParams{ChainKey: "bsc", ChainID: 56, CursorKey: "summary-proof", BlockNumber: b.n, BlockHash: fmt.Sprintf("0x%064x", b.n), ParentHash: fmt.Sprintf("0x%064x", b.n-1), BlockTimestamp: &b.at}); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.RecordMarketScanCoverage(ctx, RecordMarketScanCoverageParams{ChainID: 56, CursorKey: "summary-proof", ScopePoolIDs: pools, ScopeTokenAddresses: []string{tradeFixtureToken}, FromBlock: 100, ThroughBlock: 200, AfterAt: from.Add(-time.Second), ThroughAt: through.Add(time.Second), AfterHash: fmt.Sprintf("0x%064x", 99), ThroughHash: fmt.Sprintf("0x%064x", 200)}); err != nil {
		t.Fatal(err)
	}
	coverage, err = database.LoadDailySummaryRecoveryCoverage(ctx, "trade-owner", from, through)
	if err != nil || coverage.Status != "complete" || coverage.Scope != "configured_collection" {
		t.Fatalf("completed actual scope rejected: %#v,%v", coverage, err)
	}
	cutoff := through.Add(2 * time.Second)
	if _, err := database.ConfigureNotificationRecoveryBoundary(ctx, cutoff, 0, "explicit fixture cutover"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.RecordNotificationRecoveryGap(ctx, NotificationRecoveryGap{ChainID: 56, ChainKey: "bsc", FromBlock: 100, ThroughBlock: 150, FromAt: &from, ThroughAt: from.Add(30 * time.Minute), CutoffAt: cutoff, Reason: "intentional fixture gap"}); err != nil {
		t.Fatal(err)
	}
	coverage, err = database.LoadDailySummaryRecoveryCoverage(ctx, "trade-owner", from, through)
	if err != nil || coverage.Status != "incomplete" || coverage.Chains[0].Status != "recorded_gap" || len(coverage.Chains[0].Gaps) != 1 {
		t.Fatalf("explicit history gap hidden: %#v,%v", coverage, err)
	}
	other, err := database.LoadDailySummaryRecoveryCoverage(ctx, "different-owner", from, through)
	if err != nil || other.Status != "not_applicable" || len(other.Chains) != 0 {
		t.Fatalf("other owner collection scope leaked: %#v,%v", other, err)
	}
}

func TestPostgresTransactionPanicAndCancelledFailureReleaseConnectionAndLocks(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("fixture did not panic")
			}
		}()
		_, _ = withTxValue(ctx, database.db, func(tx DBTX) (bool, error) {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(73100929)`); err != nil {
				t.Fatal(err)
			}
			panic("fixture callback failed after taking transaction lock")
		})
	}()
	var lockFree bool
	if err := pool.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(73100929)`).Scan(&lockFree); err != nil || !lockFree {
		t.Fatalf("panic leaked transaction lock: %v,%v", lockFree, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	failure := errors.New("rollback after cancellation")
	if _, err := withTxValue(cancelled, database.db, func(tx DBTX) (bool, error) {
		if _, err := tx.Exec(cancelled, `SELECT pg_advisory_xact_lock(73100930)`); err != nil {
			t.Fatal(err)
		}
		cancel()
		return false, failure
	}); !errors.Is(err, failure) {
		t.Fatalf("original failure lost: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(73100930)`).Scan(&lockFree); err != nil || !lockFree {
		t.Fatalf("cancelled rollback leaked transaction lock: %v,%v", lockFree, err)
	}
}
