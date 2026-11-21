package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestPostgresHolderJobsRollingBudgetRequiresRoomForCompleteRound(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		holderJobProject(t, database, 56, "bsc", fmt.Sprintf("0x%040x", 900+i), fmt.Sprintf("rolling-round-%d", i))
	}
	// The separate minute window has expired, while 101 of 120 recorded
	// admissions remain inside the rolling window. Nineteen slots are free.
	if _, err := pool.Exec(ctx, `INSERT INTO market_holder_refresh_scheduler(id,window_started_at,pages_used,recent_page_admissions)
 VALUES(1,clock_timestamp()-INTERVAL '2 minutes',60,
 array_fill(clock_timestamp()-INTERVAL '14 minutes',ARRAY[101]) ||
 array_fill(clock_timestamp()-INTERVAL '16 minutes',ARRAY[19]))`); err != nil {
		t.Fatal(err)
	}
	var initialVersion, idleVersion string
	var count int
	if err := pool.QueryRow(ctx, `SELECT xmin::text FROM market_holder_refresh_scheduler`).Scan(&initialVersion); err != nil {
		t.Fatal(err)
	}
	if job, err := database.ClaimMarketHolderRefreshJob(ctx, 15*time.Minute); err != nil || job != nil {
		t.Fatalf("claim with only 19 pages remaining: job=%+v err=%v", job, err)
	}
	if err := pool.QueryRow(ctx, `SELECT xmin::text,cardinality(recent_page_admissions) FROM market_holder_refresh_scheduler`).Scan(&idleVersion, &count); err != nil ||
		idleVersion != initialVersion || count != MarketHolderRefreshRollingPages {
		t.Fatalf("budget polling rewrote the row: versions=%s/%s count=%d err=%v", initialVersion, idleVersion, count, err)
	}

	// Exactly one more expired admission allows one twenty-page round.
	if _, err := pool.Exec(ctx, `UPDATE market_holder_refresh_scheduler
 SET recent_page_admissions[101]=clock_timestamp()-INTERVAL '15 minutes' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	job := claimHolderJob(t, database)
	if next, err := database.ClaimMarketHolderRefreshJob(ctx, 15*time.Minute); err != nil || next != nil {
		t.Fatalf("second claim reused reserved rolling pages: job=%+v err=%v", next, err)
	}
	if err := database.ReserveMarketHolderRefreshPage(ctx, *job); err != nil {
		t.Fatal(err)
	}
	var minutePages int
	if err := pool.QueryRow(ctx, `SELECT pages_used,cardinality(recent_page_admissions) FROM market_holder_refresh_scheduler`).Scan(&minutePages, &count); err != nil ||
		minutePages != 1 || count != 101 {
		t.Fatalf("expected one new admission and expired records pruned: minute=%d rolling=%d err=%v", minutePages, count, err)
	}
}

func TestPostgresHolderJobsRollingBudgetSerializesLastPage(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		holderJobProject(t, database, 56, "bsc", fmt.Sprintf("0x%040x", 910+i), fmt.Sprintf("rolling-page-%d", i))
	}
	first, second := claimHolderJob(t, database), claimHolderJob(t, database)
	// Recheck the ceiling at the page entry point even with outstanding
	// leases. The older admission comes last to exercise retry calculation
	// without assuming timestamp order after a database clock adjustment.
	var oldest time.Time
	if err := pool.QueryRow(ctx, `UPDATE market_holder_refresh_scheduler
 SET recent_page_admissions=array_fill(clock_timestamp()-INTERVAL '3 minutes',ARRAY[118]) ||
 ARRAY[clock_timestamp()-INTERVAL '4 minutes']
 WHERE id=1 RETURNING recent_page_admissions[119]`).Scan(&oldest); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, job := range []*MarketHolderRefreshJob{first, second} {
		go func() {
			<-start
			results <- database.ReserveMarketHolderRefreshPage(ctx, *job)
		}()
	}
	close(start)
	successful := 0
	for range 2 {
		if err := <-results; err == nil {
			successful++
		} else {
			var budget *MarketHolderRefreshBudgetError
			if !errors.As(err, &budget) || !budget.RetryAt.Equal(oldest.Add(holderRefreshPageWindow)) {
				t.Fatalf("unexpected page denial: %+v err=%v", budget, err)
			}
		}
	}
	var minutePages, rollingPages, leasePages int
	if err := pool.QueryRow(ctx, `SELECT pages_used,cardinality(recent_page_admissions),
 (SELECT SUM(refresh_pages_used) FROM market_holder_refresh_states)
 FROM market_holder_refresh_scheduler`).Scan(&minutePages, &rollingPages, &leasePages); err != nil ||
		successful != 1 || minutePages != 1 || rollingPages != MarketHolderRefreshRollingPages || leasePages != 1 {
		t.Fatalf("last page admission was not atomic: success=%d minute=%d rolling=%d lease=%d err=%v",
			successful, minutePages, rollingPages, leasePages, err)
	}
}
