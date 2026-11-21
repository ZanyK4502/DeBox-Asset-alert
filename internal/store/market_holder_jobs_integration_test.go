package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// A scheduler fixture is an actual enabled holder request with current
// Professional entitlement. Bare projects no longer create sampling demand.
func holderJobProject(t *testing.T, s *Store, chainID int64, key, token, owner string) {
	t.Helper()
	ctx := context.Background()
	var assetID, deploymentID, projectID int64
	if err := s.db.QueryRow(ctx, `INSERT INTO market_assets(identity_source,canonical_asset_id)
 VALUES('holder-job-test',$1) ON CONFLICT(identity_source,canonical_asset_id) DO UPDATE SET canonical_asset_id=EXCLUDED.canonical_asset_id RETURNING id`, fmt.Sprintf("%d:%s", chainID, token)).Scan(&assetID); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(ctx, `INSERT INTO market_asset_deployments(market_asset_id,chain_id,chain_key,token_address,token_decimals)
 VALUES($1,$2,$3,$4,0) ON CONFLICT(chain_id,token_address) DO UPDATE SET token_decimals=EXCLUDED.token_decimals RETURNING id`, assetID, chainID, key, token).Scan(&deploymentID); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(ctx, `INSERT INTO market_projects(market_asset_id,debox_user_id,chain_id,chain_key,token_address,token_decimals)
 VALUES($1,$2,$3,$4,$5,0) RETURNING id`, assetID, owner, chainID, key, token).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO market_project_deployments(market_project_id,market_asset_id,market_asset_deployment_id) VALUES($1,$2,$3)`, projectID, assetID, deploymentID); err != nil {
		t.Fatal(err)
	}
	holderJobDemand(t, s, projectID, owner)
}

func holderJobDemand(t *testing.T, s *Store, projectID int64, owner string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.db.Exec(ctx, `INSERT INTO subscriptions(debox_user_id,plan_code,status,starts_at,expires_at,is_permanent)
 SELECT $1,'professional','active',NOW(),NOW()+INTERVAL '30 days',1 WHERE NOT EXISTS(
 SELECT 1 FROM subscriptions WHERE debox_user_id=$1 AND plan_code='professional' AND status='active' AND(is_permanent=1 OR expires_at>NOW()))`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO market_rules(debox_user_id,market_project_id,rule_type,threshold_value,threshold_unit,notification_chat_id)
 VALUES($1,$2,'market_holder_increase',1,'percent',$1)`, owner, projectID); err != nil {
		t.Fatal(err)
	}
}

func claimHolderJob(t *testing.T, s *Store) *MarketHolderRefreshJob {
	t.Helper()
	job, err := s.ClaimMarketHolderRefreshJob(context.Background(), 15*time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	return job
}

func TestPostgresHolderJobsFairAcrossChainsSharedAcrossOwnersAndMaxTwoLeases(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	chains := []struct {
		id  int64
		key string
	}{{1, "ethereum"}, {10, "optimism"}, {56, "bsc"}, {137, "polygon"}, {8453, "base"}, {42161, "arbitrum"}}
	for _, c := range chains {
		holderJobProject(t, database, c.id, c.key, tradeFixtureToken, "shared-owner-a")
	}
	holderJobProject(t, database, 1, "ethereum", tradeFixtureToken, "shared-owner-b")
	other := newWithDB(pool)
	first, second := claimHolderJob(t, database), claimHolderJob(t, other)
	if first.Project.ChainID != 1 || second.Project.ChainID != 10 {
		t.Fatalf("chain rotation=%d/%d", first.Project.ChainID, second.Project.ChainID)
	}
	if third, err := database.ClaimMarketHolderRefreshJob(ctx, 15*time.Minute); err != nil || third != nil {
		t.Fatalf("exceeded global two workers: %+v %v", third, err)
	}
	for _, job := range []*MarketHolderRefreshJob{first, second} {
		if err := database.FailMarketHolderRefreshJob(ctx, *job, "source_error", time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []int64{56, 137, 8453, 42161} {
		job := claimHolderJob(t, database)
		if job.Project.ChainID != want {
			t.Fatalf("chain starved: got %d want%d", job.Project.ChainID, want)
		}
		if err := database.FailMarketHolderRefreshJob(ctx, *job, "source_capacity", time.Now().Add(15*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	var states int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_holder_refresh_states`).Scan(&states); err != nil || states != 6 {
		t.Fatalf("duplicate per owner jobs: %d %v", states, err)
	}
}

func TestPostgresHolderJobsCompleteAndFailurePreserveSuccessfulBaselineAndOverdue(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	holderJobProject(t, database, 56, "bsc", tradeFixtureToken, "no-pro-filter")
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-30 * time.Minute)
	baseline := holderRefreshFixture(at)
	if _, err := database.ApplyMarketHolderRefresh(ctx, baseline); err != nil {
		t.Fatal(err)
	}
	job := claimHolderJob(t, database)
	if job.ExpectedPreviousAt == nil || !job.ExpectedPreviousAt.Equal(at) {
		t.Fatal("pre-HTTP CAS missing")
	}
	var originalDue time.Time
	if err := pool.QueryRow(ctx, `SELECT next_due_at FROM market_holder_refresh_states`).Scan(&originalDue); err != nil {
		t.Fatal(err)
	}
	if err := database.ReserveMarketHolderRefreshPage(ctx, *job); err != nil {
		t.Fatal(err)
	}
	if err := database.FailMarketHolderRefreshJob(ctx, *job, "source_incomplete", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	state, err := database.GetMarketHolderRefreshState(ctx, 56, tradeFixtureToken)
	if err != nil || state.LastSuccessAt == nil || !state.LastSuccessAt.Equal(at) || len(state.RankBaseline) != 100 {
		t.Fatalf("failure changed baseline %+v %v", state, err)
	}
	var due, nextAttempt time.Time
	var failed, tracked, history int
	if err := pool.QueryRow(ctx, `SELECT next_due_at,next_attempt_at,consecutive_failures,(SELECT COUNT(*) FROM market_holders),(SELECT COUNT(*) FROM market_holder_snapshots) FROM market_holder_refresh_states`).Scan(&due, &nextAttempt, &failed, &tracked, &history); err != nil || !due.Equal(originalDue) || !nextAttempt.After(time.Now()) || failed != 1 || tracked != 20 || history != 20 {
		t.Fatalf("failure hid overdue/changed tracker: due=%s failures=%d tracked=%d history=%d err=%v", due, failed, tracked, history, err)
	}
	if next, err := database.ClaimMarketHolderRefreshJob(ctx, 15*time.Minute); err != nil || next != nil {
		t.Fatalf("ignored per-token backoff: %+v %v", next, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE market_holder_refresh_states SET next_attempt_at=NOW()-INTERVAL '1 second'`); err != nil {
		t.Fatal(err)
	}
	job = claimHolderJob(t, database)
	if err := database.ReserveMarketHolderRefreshPage(ctx, *job); err != nil {
		t.Fatal(err)
	}
	p := baseline
	p.ObservedAt = time.Now().UTC().Truncate(time.Microsecond)
	p.ExpectedPreviousAt = job.ExpectedPreviousAt
	result, err := database.CompleteMarketHolderRefreshJob(ctx, *job, p, 15*time.Minute)
	if err != nil || result.SnapshotsCreated != 0 || result.RankEventsCreated != 0 {
		t.Fatalf("unchanged complete=%+v %v", result, err)
	}
	var lease *time.Time
	var code string
	if err := pool.QueryRow(ctx, `SELECT next_due_at,next_attempt_at,refresh_lease_until,consecutive_failures,last_failure_code FROM market_holder_refresh_states`).Scan(&due, &nextAttempt, &lease, &failed, &code); err != nil || !due.Equal(job.StartedAt.Add(15*time.Minute)) || !nextAttempt.Equal(due) || lease != nil || failed != 0 || code != "" {
		t.Fatalf("success schedule/lease=%s %+v %d %s %v", due, lease, failed, code, err)
	}
}

func TestPostgresHolderJobExpiredLeaseAndStaleCASCannotCommitLateHTTPResult(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	holderJobProject(t, database, 56, "bsc", tradeFixtureToken, "lease-test")
	old := claimHolderJob(t, database)
	if err := database.ReserveMarketHolderRefreshPage(ctx, *old); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE market_holder_refresh_states SET refresh_lease_until=NOW()-INTERVAL '1 second'`); err != nil {
		t.Fatal(err)
	}
	p := holderRefreshFixture(time.Now().UTC().Truncate(time.Microsecond))
	if _, err := database.CompleteMarketHolderRefreshJob(ctx, *old, p, 15*time.Minute); !errors.Is(err, ErrMarketHolderRefreshLeaseLost) {
		t.Fatalf("expired HTTP committed: %v", err)
	}
	job := claimHolderJob(t, database)
	if job.LeaseToken <= old.LeaseToken {
		t.Fatal("claim token reused")
	}
	if err := database.ReserveMarketHolderRefreshPage(ctx, *job); err != nil {
		t.Fatal(err)
	}
	if err := database.FailMarketHolderRefreshJob(ctx, *old, "source_error", time.Now().Add(time.Minute)); !errors.Is(err, ErrMarketHolderRefreshLeaseLost) {
		t.Fatalf("old worker changed new lease: %v", err)
	}
	// A manual/newer successful sample during HTTP is independently protected
	// by the old Apply CAS, inside the same lease transaction.
	if _, err := database.ApplyMarketHolderRefresh(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.ObservedAt = p.ObservedAt.Add(time.Second)
	if _, err := database.CompleteMarketHolderRefreshJob(ctx, *job, p, 15*time.Minute); !errors.Is(err, ErrStaleMarketHolderRefresh) {
		t.Fatalf("stale baseline accepted: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_holder_snapshots`).Scan(&count); err != nil || count != 20 {
		t.Fatalf("late batch partial writes: %d %v", count, err)
	}
}

func TestPostgresHolderJobScheduleFailureRollsBackOldApplyInSameTransaction(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	holderJobProject(t, database, 56, "bsc", tradeFixtureToken, "atomic-test")
	job := claimHolderJob(t, database)
	if err := database.ReserveMarketHolderRefreshPage(ctx, *job); err != nil {
		t.Fatal(err)
	}
	// Apply writes last_success while the lease is still held. Only the final
	// scheduling update violates this fixture constraint, after all old writes.
	if _, err := pool.Exec(ctx, `ALTER TABLE market_holder_refresh_states ADD CONSTRAINT reject_lease_release_fixture CHECK(refresh_lease_until IS NOT NULL OR last_success_at IS NULL)`); err != nil {
		t.Fatal(err)
	}
	p := holderRefreshFixture(time.Now().UTC().Truncate(time.Microsecond))
	if _, err := database.CompleteMarketHolderRefreshJob(ctx, *job, p, 15*time.Minute); err == nil {
		t.Fatal("scheduling failure ignored")
	}
	var success *time.Time
	var holders, history, events int
	if err := pool.QueryRow(ctx, `SELECT last_success_at,(SELECT COUNT(*) FROM market_holders),(SELECT COUNT(*) FROM market_holder_snapshots),(SELECT COUNT(*) FROM market_events) FROM market_holder_refresh_states`).Scan(&success, &holders, &history, &events); err != nil || success != nil || holders != 0 || history != 0 || events != 0 {
		t.Fatalf("Apply committed before lease finalizer: %v %d/%d/%d %v", success, holders, history, events, err)
	}
}

func TestPostgresHolderJobsMinutePageBudgetAndIdleClaimsDoNotWrite(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	if job, err := database.ClaimMarketHolderRefreshJob(ctx, 15*time.Minute); err != nil || job != nil {
		t.Fatalf("empty claim=%+v %v", job, err)
	}
	var controls, states int
	if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM market_holder_refresh_scheduler),(SELECT COUNT(*) FROM market_holder_refresh_states)`).Scan(&controls, &states); err != nil || controls != 0 || states != 0 {
		t.Fatalf("idle created WAL rows: %d/%d %v", controls, states, err)
	}
	for i := 0; i < 7; i++ {
		holderJobProject(t, database, 56, "bsc", fmt.Sprintf("0x%040x", i+700), fmt.Sprintf("budget-%d", i))
	}
	// Keep this deterministic at an arbitrary wall-clock minute boundary by
	// setting the test's existing control window one minute ahead.
	first := claimHolderJob(t, database)
	if _, err := pool.Exec(ctx, `UPDATE market_holder_refresh_scheduler SET window_started_at=date_trunc('minute',NOW())+INTERVAL '1 minute'`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MarketHolderRefreshMinutePages/MarketHolderRefreshMaxPages; i++ {
		job := first
		if i > 0 {
			job = claimHolderJob(t, database)
		}
		for page := 0; page < MarketHolderRefreshMaxPages; page++ {
			if err := database.ReserveMarketHolderRefreshPage(ctx, *job); err != nil {
				t.Fatalf("round%d page%d %v", i, page, err)
			}
		}
		if err := database.FailMarketHolderRefreshJob(ctx, *job, "source_error", time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	var xminBefore, xminAfter string
	if err := pool.QueryRow(ctx, `SELECT xmin::text FROM market_holder_refresh_scheduler`).Scan(&xminBefore); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if job, err := database.ClaimMarketHolderRefreshJob(ctx, 15*time.Minute); err != nil || job != nil {
			t.Fatalf("budget exceeded: %+v %v", job, err)
		}
	}
	var used int
	if err := pool.QueryRow(ctx, `SELECT xmin::text,pages_used FROM market_holder_refresh_scheduler`).Scan(&xminAfter, &used); err != nil || xminBefore != xminAfter || used != 60 {
		t.Fatalf("idle budget poll wrote: %s/%s used%d %v", xminBefore, xminAfter, used, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE market_holder_refresh_scheduler SET window_started_at=NOW()-INTERVAL '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	if job := claimHolderJob(t, database); job.Project.TokenAddress != fmt.Sprintf("0x%040x", 700+MarketHolderRefreshMinutePages/MarketHolderRefreshMaxPages) {
		t.Fatalf("budget reset skipped pending token: %+v", job)
	}
}
