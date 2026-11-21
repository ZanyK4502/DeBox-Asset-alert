package store

import (
	"context"
	"testing"
	"time"
)

func TestPostgresMarketDeliveryScheduleMatchesClaimableEvidence(t *testing.T) {
	db, pool, rule := marketDeliveryFixture(t, false)
	ctx := context.Background()
	event := marketDeliveryFact(t, db, "schedule-only", time.Now().UTC())
	queued := marketDeliveryTrigger(t, db, rule, event, "pending")
	future := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `UPDATE market_rule_events SET next_attempt_at=$2 WHERE id=$1`, queued.ID, future); err != nil {
		t.Fatal(err)
	}
	if next, err := db.NextMarketDeliveryAt(ctx); err != nil || next == nil || !next.Equal(future) {
		t.Fatalf("next=%v %v", next, err)
	}
	if claims, err := db.ClaimMarketDeliveries(ctx, 1); err != nil || len(claims) != 0 {
		t.Fatalf("future claimed=%v %v", claims, err)
	}
	for _, change := range []string{
		`notification_status='failed',delivery_outcome='unknown'`,
		`notification_status='failed',delivery_outcome='retryable',notification_attempts=5`,
	} {
		if _, err := pool.Exec(ctx, `UPDATE market_rule_events SET `+change+` WHERE id=$1`, queued.ID); err != nil {
			t.Fatal(err)
		}
		if next, err := db.NextMarketDeliveryAt(ctx); err != nil || next != nil {
			t.Fatalf("nonretry evidence scheduled=%v %v for %s", next, err, change)
		}
	}
}

func TestPostgresSummaryDeliveryScheduleExcludesUnknownTerminalAndFutureClaims(t *testing.T) {
	db, pool, subscription, target, now := summaryDispatchFixture(t)
	ctx := context.Background()
	dispatch, err := db.PrepareDailySummaryDispatch(ctx, summaryDispatchParamsFixture(t, subscription, target, now.Add(-time.Minute), "frozen schedule"))
	if err != nil || dispatch == nil {
		t.Fatal(dispatch, err)
	}
	future := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	if _, err = pool.Exec(ctx, `UPDATE daily_summary_dispatches SET status='failed',retry_at=$2 WHERE id=$1`, dispatch.ID, future); err != nil {
		t.Fatal(err)
	}
	if next, err := db.NextDailySummaryDispatchAt(ctx); err != nil || next == nil || !next.Equal(future) {
		t.Fatalf("next=%v %v", next, err)
	}
	if refs, err := db.ListDueDailySummaryDispatches(ctx, time.Now().UTC(), 100); err != nil || len(refs) != 0 {
		t.Fatalf("future scheduled now=%v %v", refs, err)
	}
	if refs, err := db.ListDueDailySummaryDispatches(ctx, future, 100); err != nil || len(refs) != 1 || refs[0].ID != dispatch.ID {
		t.Fatalf("due refs=%v %v", refs, err)
	}
	if actual, err := db.GetDailySummaryRetryTarget(ctx, subscription.ID, target.ID); err != nil || actual == nil || actual.ID != target.ID {
		t.Fatalf("target=%v %v", actual, err)
	}
	for _, change := range []string{`status='unknown',retry_at=NULL`, `status='failed',retry_at=NULL`, `status='failed',retry_at=NOW(),attempts=5`, `status='sent',retry_at=NULL`} {
		if _, err := pool.Exec(ctx, `UPDATE daily_summary_dispatches SET `+change+` WHERE id=$1`, dispatch.ID); err != nil {
			t.Fatal(err)
		}
		if next, err := db.NextDailySummaryDispatchAt(ctx); err != nil || next != nil {
			t.Fatalf("nonretry evidence scheduled=%v %v for %s", next, err, change)
		}
	}
}
