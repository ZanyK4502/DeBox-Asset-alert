package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	"github.com/jackc/pgx/v5/pgxpool"
)

func summaryDispatchFixture(t *testing.T) (*Store, *pgxpool.Pool, Subscription, DailySummaryTarget, time.Time) {
	t.Helper()
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	subscription, err := database.ActivateSubscription(ctx, "dispatch-owner", "professional", 30)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.UpdateDailySummarySettings(ctx, subscription.DeBoxUserID, DailySummarySettings{Enabled: true, PushTime: "12:00", TimezoneName: "UTC", ChatType: "private", ChatID: subscription.DeBoxUserID, Language: "en"}); err != nil {
		t.Fatal(err)
	}
	targets, err := database.ListDailySummaryTargets(ctx, subscription.ID)
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets=%+v %v", targets, err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	previous := now.Add(-3 * 24 * time.Hour)
	if _, err = pool.Exec(ctx, `UPDATE daily_summary_targets SET last_period_end_at=$2,last_sent_date=$3 WHERE id=$1`, targets[0].ID, previous, previous.Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	targets[0].LastPeriodEndAt = &previous
	return database, pool, subscription, targets[0], now
}

func summaryDispatchParamsFixture(t *testing.T, subscription Subscription, target DailySummaryTarget, end time.Time, text string) PrepareDailySummaryDispatchParams {
	t.Helper()
	from := end.Add(-24 * time.Hour)
	if target.LastPeriodEndAt != nil {
		from = *target.LastPeriodEndAt
	}
	details, err := json.Marshal(map[string]any{"schema_version": 2, "period_start": from, "period_end": end, "target": map[string]any{"id": target.ID}})
	if err != nil {
		t.Fatal(err)
	}
	return PrepareDailySummaryDispatchParams{SubscriptionID: subscription.ID, TargetID: target.ID, PeriodStartAt: from, PeriodEndAt: end, LocalDate: end.Format("2006-01-02"), Snapshot: CreateNotificationDetailSnapshotParams{SourceKey: fmt.Sprintf("daily_summary:%d:%d:%s:%s:%d", subscription.ID, target.ID, target.ChatType, target.ChatID, end.UnixNano()), NotificationKind: NotificationKindDailySummary, SourceType: "daily_summary_target", SourceID: &target.ID, DeBoxUserID: subscription.DeBoxUserID, RuleType: "daily_summary", NotificationChatID: target.ChatID, NotificationChatType: target.ChatType, NotificationLanguage: "en", NotificationText: text, Details: details}, BuildPayload: func(snapshot NotificationDetailSnapshot) (notificationdelivery.Payload, error) {
		return notificationdelivery.Payload{ChatID: snapshot.NotificationChatID, ChatType: snapshot.NotificationChatType, Text: snapshot.NotificationText, ActionText: "Full report", ActionURL: "https://first.example/app?notification_id=" + snapshot.PublicID}, nil
	}}
}

func TestPostgresDailySummaryDispatchFreezesClaimsAndFinalizesAtomically(t *testing.T) {
	database, pool, subscription, target, now := summaryDispatchFixture(t)
	ctx := context.Background()
	end := now.Add(-time.Minute)
	params := summaryDispatchParamsFixture(t, subscription, target, end, "original frozen report")
	dispatch, err := database.PrepareDailySummaryDispatch(ctx, params)
	if err != nil || dispatch == nil {
		t.Fatalf("prepare=%+v %v", dispatch, err)
	}
	params.Snapshot.NotificationText = "later data must not change the report"
	params.BuildPayload = func(NotificationDetailSnapshot) (notificationdelivery.Payload, error) {
		t.Fatal("retry rebuilt a payload")
		return notificationdelivery.Payload{}, nil
	}
	duplicate, err := database.PrepareDailySummaryDispatch(ctx, params)
	if err != nil || duplicate.ID != dispatch.ID || duplicate.Payload.Text != "original frozen report" {
		t.Fatalf("freeze=%+v %v", duplicate, err)
	}
	var wait sync.WaitGroup
	claims := make(chan *DailySummaryDispatchClaim, 2)
	failures := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			claim, err := database.ClaimDailySummaryDispatch(ctx, dispatch.ID, now)
			claims <- claim
			failures <- err
		}()
	}
	wait.Wait()
	close(claims)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var selected *DailySummaryDispatchClaim
	count := 0
	for claim := range claims {
		if claim != nil {
			count++
			selected = claim
		}
	}
	if count != 1 || selected.Dispatch.Attempts != 1 || selected.Dispatch.ClaimToken == nil {
		t.Fatalf("claims=%d selected=%+v", count, selected)
	}
	if err := database.CompleteDailySummaryDispatch(ctx, dispatch.ID, "wrong-claim", "message", now); !errors.Is(err, ErrDailySummaryDispatchConflict) {
		t.Fatalf("wrong token=%v", err)
	}
	if err := database.CompleteDailySummaryDispatch(ctx, dispatch.ID, *selected.Dispatch.ClaimToken, "confirmed-message", now); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteDailySummaryDispatch(ctx, dispatch.ID, *selected.Dispatch.ClaimToken, "confirmed-message", now); err != nil {
		t.Fatalf("same-token DB finalization retry was not idempotent: %v", err)
	}
	if err := database.CompleteDailySummaryDispatch(ctx, dispatch.ID, "different-token", "confirmed-message", now); !errors.Is(err, ErrDailySummaryDispatchConflict) {
		t.Fatalf("sent claim lost fencing: %v", err)
	}
	var cutoff time.Time
	var ledger int
	if err := pool.QueryRow(ctx, `SELECT last_period_end_at FROM daily_summary_targets WHERE id=$1`, target.ID).Scan(&cutoff); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM daily_summary_deliveries WHERE subscription_id=$1`, subscription.ID).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if !cutoff.Equal(end) || ledger != 1 {
		t.Fatalf("atomic progress=%s ledger%d", cutoff, ledger)
	}
	var closed time.Time
	if err := pool.QueryRow(ctx, `SELECT closed_at FROM daily_summary_dispatches WHERE id=$1`, dispatch.ID).Scan(&closed); err != nil || !closed.Equal(now) {
		t.Fatalf("sent closure=%s %v", closed, err)
	}
	if open, err := database.GetOpenDailySummaryDispatch(ctx, subscription.ID, target.ChatType, target.ChatID); err != nil || open != nil {
		t.Fatalf("sent still open=%+v %v", open, err)
	}
	if again, err := database.ClaimDailySummaryDispatch(ctx, dispatch.ID, now.Add(time.Minute)); err != nil || again != nil {
		t.Fatalf("sent re-claimed=%+v %v", again, err)
	}
}

func TestPostgresDailySummaryDispatchUnknownBlocksAndRequiresExplicitResolution(t *testing.T) {
	database, pool, subscription, target, now := summaryDispatchFixture(t)
	ctx := context.Background()
	end := now.Add(-time.Minute)
	d, err := database.PrepareDailySummaryDispatch(ctx, summaryDispatchParamsFixture(t, subscription, target, end, "unknown result"))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := database.ClaimDailySummaryDispatch(ctx, d.ID, now)
	if err != nil || claim == nil {
		t.Fatalf("claim=%+v %v", claim, err)
	}
	if err := database.FailDailySummaryDispatch(ctx, d.ID, *claim.Dispatch.ClaimToken, "unknown", "transport_timeout", nil, now); err != nil {
		t.Fatal(err)
	}
	if replaced, err := database.SupersedeDailySummaryDispatch(ctx, d.ID, now.Add(time.Hour), now.Add(time.Hour)); err != nil || replaced {
		t.Fatalf("unknown superseded=%v %v", replaced, err)
	}
	if again, err := database.ClaimDailySummaryDispatch(ctx, d.ID, now.Add(24*time.Hour)); err != nil || again != nil {
		t.Fatalf("unknown retried=%+v %v", again, err)
	}
	later, err := database.PrepareDailySummaryDispatch(ctx, summaryDispatchParamsFixture(t, subscription, target, now.Add(time.Hour), "new content"))
	if err != nil || later.ID != d.ID {
		t.Fatalf("unknown allowed replacement=%+v %v", later, err)
	}
	if err := database.ResolveUnknownDailySummaryDispatch(ctx, d.ID, false, "", "operator verified not delivered", now); err != nil {
		t.Fatal(err)
	}
	open, err := database.GetOpenDailySummaryDispatch(ctx, subscription.ID, target.ChatType, target.ChatID)
	if err != nil || open.Status != "failed" || open.Attempts != 1 || open.Payload.Text != "unknown result" || open.Resolution != "confirmed_not_sent" {
		t.Fatalf("resolution reset evidence=%+v %v", open, err)
	}
	if replaced, err := database.SupersedeDailySummaryDispatch(ctx, d.ID, now, now); err != nil || !replaced {
		t.Fatalf("known-unsent not superseded=%v %v", replaced, err)
	}
	var closed time.Time
	if err := pool.QueryRow(ctx, `SELECT closed_at FROM daily_summary_dispatches WHERE id=$1`, d.ID).Scan(&closed); err != nil || !closed.Equal(now) {
		t.Fatalf("superseded audit closure=%s %v", closed, err)
	}
	var oldText string
	if err := pool.QueryRow(ctx, `SELECT notification_text FROM notification_detail_snapshots WHERE id=$1`, *d.SnapshotID).Scan(&oldText); err != nil || oldText != "unknown result" {
		t.Fatalf("old snapshot overwritten=%q %v", oldText, err)
	}
}

func TestPostgresDailySummaryDispatchConfirmedUnknownDoesNotAdvanceReplacementTarget(t *testing.T) {
	database, pool, subscription, target, now := summaryDispatchFixture(t)
	ctx := context.Background()
	end := now.Add(-time.Minute)
	d, err := database.PrepareDailySummaryDispatch(ctx, summaryDispatchParamsFixture(t, subscription, target, end, "possibly delivered"))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := database.ClaimDailySummaryDispatch(ctx, d.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = database.FailDailySummaryDispatch(ctx, d.ID, *claim.Dispatch.ClaimToken, "unknown", "response_lost", nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM daily_summary_targets WHERE id=$1`, target.ID); err != nil {
		t.Fatal(err)
	}
	var replacement int64
	if err = pool.QueryRow(ctx, `INSERT INTO daily_summary_targets(subscription_id,chat_type,chat_id,enabled,push_time,timezone,language) VALUES($1,$2,$3,1,'12:00','UTC','en') RETURNING id`, subscription.ID, target.ChatType, target.ChatID).Scan(&replacement); err != nil {
		t.Fatal(err)
	}
	if err = database.ResolveUnknownDailySummaryDispatch(ctx, d.ID, true, "message-confirmed", "platform record checked", now); err != nil {
		t.Fatal(err)
	}
	var cutoff *time.Time
	if err = pool.QueryRow(ctx, `SELECT last_period_end_at FROM daily_summary_targets WHERE id=$1`, replacement).Scan(&cutoff); err != nil || cutoff != nil {
		t.Fatalf("replacement target moved=%v %v", cutoff, err)
	}
	var status, notes string
	if err = pool.QueryRow(ctx, `SELECT status,resolution_note FROM daily_summary_dispatches WHERE id=$1`, d.ID).Scan(&status, &notes); err != nil || status != "sent" || notes != "platform record checked" {
		t.Fatalf("resolution=%s %s %v", status, notes, err)
	}
	if err = database.ResolveUnknownDailySummaryDispatch(ctx, d.ID, true, "another", "duplicate resolution", now); !errors.Is(err, ErrDailySummaryDispatchConflict) {
		t.Fatalf("resolved twice=%v", err)
	}
}

func TestPostgresDailySummaryDispatchClaimRechecksStopAndExpiry(t *testing.T) {
	for _, mode := range []string{"disabled", "expired", "downgraded", "changed-route"} {
		t.Run(mode, func(t *testing.T) {
			database, pool, subscription, target, now := summaryDispatchFixture(t)
			ctx := context.Background()
			d, err := database.PrepareDailySummaryDispatch(ctx, summaryDispatchParamsFixture(t, subscription, target, now.Add(-time.Minute), "frozen"))
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "disabled":
				_, err = database.DisableDailySummarySettings(ctx, subscription.DeBoxUserID, subscription.ID)
			case "expired":
				_, err = pool.Exec(ctx, `UPDATE subscriptions SET expires_at=NOW()-INTERVAL '1 second',is_permanent=0 WHERE id=$1`, subscription.ID)
			case "downgraded":
				_, err = pool.Exec(ctx, `UPDATE subscriptions SET plan_code='free' WHERE id=$1`, subscription.ID)
			case "changed-route":
				_, err = pool.Exec(ctx, `UPDATE daily_summary_targets SET chat_id='different-owner' WHERE id=$1`, target.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			claim, err := database.ClaimDailySummaryDispatch(ctx, d.ID, now)
			if err != nil || claim != nil {
				t.Fatalf("stale config sent=%+v %v", claim, err)
			}
		})
	}
}

func TestPostgresDailySummaryDispatchPreservesSuccessfulCutoffAcrossScheduleChanges(t *testing.T) {
	database, pool, subscription, target, now := summaryDispatchFixture(t)
	ctx := context.Background()
	if _, err := database.UpdateDailySummarySettings(ctx, subscription.DeBoxUserID, DailySummarySettings{Enabled: true, PushTime: "21:00", TimezoneName: "Asia/Tokyo", ChatType: "private", ChatID: subscription.DeBoxUserID, Language: "zh"}); err != nil {
		t.Fatal(err)
	}
	var cutoff time.Time
	if err := pool.QueryRow(ctx, `SELECT last_period_end_at FROM daily_summary_targets WHERE id=$1`, target.ID).Scan(&cutoff); err != nil || !cutoff.Equal(*target.LastPeriodEndAt) {
		t.Fatalf("settings erased cutoff=%s %v", cutoff, err)
	}
	end := now.Add(-time.Minute)
	if _, err := pool.Exec(ctx, `INSERT INTO daily_summary_deliveries(subscription_id,period_end_at,chat_type,chat_id) VALUES($1,$2,$3,$4)`, subscription.ID, end, target.ChatType, target.ChatID); err != nil {
		t.Fatal(err)
	}
	if d, err := database.PrepareDailySummaryDispatch(ctx, summaryDispatchParamsFixture(t, subscription, target, end, "must not resend legacy sent")); err != nil || d != nil {
		t.Fatalf("legacy ledger allowed resend=%+v %v", d, err)
	}
}

func TestPostgresDailySummaryDispatchPreflightTimeoutRollsBackAndRemainsRetryable(t *testing.T) {
	database, pool, subscription, target, now := summaryDispatchFixture(t)
	ctx := context.Background()
	d, err := database.PrepareDailySummaryDispatch(ctx, summaryDispatchParamsFixture(t, subscription, target, now.Add(-time.Minute), "frozen"))
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(ctx, `LOCK TABLE notification_detail_snapshots IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	claim, err := database.ClaimDailySummaryDispatch(ctx, d.ID, now)
	if err == nil || claim != nil || time.Since(started) > 4*time.Second {
		t.Fatalf("preflight was unbounded or claimed: %+v %v", claim, err)
	}
	if err = blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	open, err := database.GetOpenDailySummaryDispatch(ctx, subscription.ID, target.ChatType, target.ChatID)
	if err != nil || open.Status != "pending" || open.Attempts != 0 {
		t.Fatalf("failed DB preflight burned attempt/quarantined: %+v %v", open, err)
	}
	claim, err = database.ClaimDailySummaryDispatch(ctx, d.ID, time.Now().UTC())
	if err != nil || claim == nil || claim.Dispatch.Attempts != 1 {
		t.Fatalf("recovered DB could not retry: %+v %v", claim, err)
	}
}

func TestPostgresDailySummaryDispatchFinalizationFailureIsAtomicAndStoresAcceptedEvidence(t *testing.T) {
	database, pool, subscription, target, now := summaryDispatchFixture(t)
	ctx := context.Background()
	d, err := database.PrepareDailySummaryDispatch(ctx, summaryDispatchParamsFixture(t, subscription, target, now.Add(-time.Minute), "frozen"))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := database.ClaimDailySummaryDispatch(ctx, d.ID, now)
	if err != nil || claim == nil {
		t.Fatalf("claim=%+v %v", claim, err)
	}
	if _, err = pool.Exec(ctx, `CREATE FUNCTION reject_summary_ledger_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture ledger failure'; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `CREATE TRIGGER reject_summary_ledger_fixture BEFORE INSERT ON daily_summary_deliveries FOR EACH ROW EXECUTE FUNCTION reject_summary_ledger_fixture()`); err != nil {
		t.Fatal(err)
	}
	if err = database.CompleteDailySummaryDispatch(ctx, d.ID, *claim.Dispatch.ClaimToken, "accepted-message", now); err == nil {
		t.Fatal("fixture failure was swallowed")
	}
	var cutoff time.Time
	var status string
	var ledger int
	if err = pool.QueryRow(ctx, `SELECT last_period_end_at FROM daily_summary_targets WHERE id=$1`, target.ID).Scan(&cutoff); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT status FROM daily_summary_dispatches WHERE id=$1`, d.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM daily_summary_deliveries`).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if !cutoff.Equal(*target.LastPeriodEndAt) || status != "sending" || ledger != 0 {
		t.Fatalf("partial success committed: %s %s %d", cutoff, status, ledger)
	}
	if err = database.FailDailySummaryDispatch(ctx, d.ID, *claim.Dispatch.ClaimToken, "unknown", "send_confirmed_record_failed", nil, now, "accepted-message"); err != nil {
		t.Fatal(err)
	}
	open, err := database.GetOpenDailySummaryDispatch(ctx, subscription.ID, target.ChatType, target.ChatID)
	if err != nil || open.Status != "unknown" || open.MessageID == nil || *open.MessageID != "accepted-message" {
		t.Fatalf("accepted evidence lost: %+v %v", open, err)
	}
}

func TestPostgresDailySummaryDispatchUsesExistingFrozenSnapshotAndProviderWait(t *testing.T) {
	database, _, subscription, target, now := summaryDispatchFixture(t)
	ctx := context.Background()
	params := summaryDispatchParamsFixture(t, subscription, target, now.Add(-time.Minute), "original previously frozen")
	snapshot, err := database.CreateNotificationDetailSnapshot(ctx, params.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	params.Snapshot.NotificationText = "newly recomputed text must not be sent"
	wait := now.Add(3 * 24 * time.Hour)
	params.NotBefore = &wait
	d, err := database.PrepareDailySummaryDispatch(ctx, params)
	if err != nil || d != nil {
		t.Fatalf("legacy orphan snapshot was assumed unsent: %+v %v", d, err)
	}
	if err := database.ResolveLegacyDailySummaryReview(ctx, snapshot.ID, false, "", "operator verified no delivery", now); err != nil {
		t.Fatal(err)
	}
	d, err = database.PrepareDailySummaryDispatch(ctx, params)
	if err != nil || d == nil || d.Payload.Text != "original previously frozen" || d.SnapshotID == nil || *d.SnapshotID != snapshot.ID || d.RetryAt == nil || !d.RetryAt.Equal(wait) {
		t.Fatalf("snapshot/wait frozen=%+v %v", d, err)
	}
	if claim, err := database.ClaimDailySummaryDispatch(ctx, d.ID, now); err != nil || claim != nil {
		t.Fatalf("provider wait bypassed=%+v %v", claim, err)
	}
}

func TestPostgresDailySummaryLegacyOrphanBlocksNewPeriodWithoutInventingDates(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprint(malformed), func(t *testing.T) {
			database, pool, subscription, target, now := summaryDispatchFixture(t)
			ctx := context.Background()
			oldParams := summaryDispatchParamsFixture(t, subscription, target, now.Add(-24*time.Hour), "legacy possibly sent")
			if malformed {
				oldParams.Snapshot.Details = json.RawMessage(fmt.Sprintf(`{"period_start":%q,"period_end":"not-a-time","target":{"id":%d}}`, oldParams.PeriodStartAt.Format(time.RFC3339Nano), target.ID))
			}
			snapshot, err := database.CreateNotificationDetailSnapshot(ctx, oldParams.Snapshot)
			if err != nil {
				t.Fatal(err)
			}
			newParams := summaryDispatchParamsFixture(t, subscription, target, now.Add(-time.Minute), "current report")
			if d, err := database.PrepareDailySummaryDispatch(ctx, newParams); err != nil || d != nil {
				t.Fatalf("period change bypassed legacy uncertainty: %+v %v", d, err)
			}
			var proof bool
			var through *time.Time
			if err := pool.QueryRow(ctx, `SELECT proof_valid,period_end_at FROM daily_summary_legacy_reviews WHERE snapshot_id=$1`, snapshot.ID).Scan(&proof, &through); err != nil {
				t.Fatal(err)
			}
			if proof == malformed || malformed && through != nil {
				t.Fatalf("legacy proof/date invented: proof%v through%v", proof, through)
			}
			if malformed {
				if err := database.ResolveLegacyDailySummaryReview(ctx, snapshot.ID, true, "message-id", "verified delivery but unknown period", now); !errors.Is(err, ErrInvalidNotificationSnapshot) {
					t.Fatalf("invalid dates advanced cutoff: %v", err)
				}
			} else {
				if err := database.ResolveLegacyDailySummaryReview(ctx, snapshot.ID, true, "legacy-message", "platform delivery verified", now); err != nil {
					t.Fatal(err)
				}
				target.LastPeriodEndAt = &oldParams.PeriodEndAt
				newParams = summaryDispatchParamsFixture(t, subscription, target, now.Add(-time.Minute), "current report")
				if d, err := database.PrepareDailySummaryDispatch(ctx, newParams); err != nil || d == nil || !d.PeriodStartAt.Equal(oldParams.PeriodEndAt) {
					t.Fatalf("verified original cutoff was not used: %+v %v", d, err)
				}
			}
		})
	}
}

func TestPostgresDailySummaryDispatcherRecoversOnlyItsOwnLedgerCutoffReadOnly(t *testing.T) {
	database, pool, subscription, target, now := summaryDispatchFixture(t)
	ctx := context.Background()
	confirmed := now.Add(-48 * time.Hour)
	if _, err := pool.Exec(ctx, `UPDATE daily_summary_targets SET last_period_end_at=NULL WHERE id=$1`, target.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO daily_summary_deliveries(subscription_id,period_end_at,chat_type,chat_id) VALUES($1,$2,$3,$4),($1,$5,'group','other-route')`, subscription.ID, confirmed, target.ChatType, target.ChatID, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	rows, err := database.ListDailySummaryDispatchTargets(ctx, subscription.ID, 0, 10)
	if err != nil || len(rows) != 1 || rows[0].LastPeriodEndAt == nil || !rows[0].LastPeriodEndAt.Equal(confirmed) {
		t.Fatalf("ledger target scope=%+v %v", rows, err)
	}
	var stored *time.Time
	if err := pool.QueryRow(ctx, `SELECT last_period_end_at FROM daily_summary_targets WHERE id=$1`, target.ID).Scan(&stored); err != nil || stored != nil {
		t.Fatalf("read path backfilled original field: %v %v", stored, err)
	}
	d, err := database.PrepareDailySummaryDispatch(ctx, summaryDispatchParamsFixture(t, subscription, rows[0], now.Add(-time.Minute), "merged from confirmed ledger"))
	if err != nil || d == nil || !d.PeriodStartAt.Equal(confirmed) {
		t.Fatalf("merged range lost legacy success: %+v %v", d, err)
	}
}

func TestPostgresDailySummaryUnknownResolutionHistoryAppendsAcrossAttempts(t *testing.T) {
	database, pool, subscription, target, now := summaryDispatchFixture(t)
	ctx := context.Background()
	d, err := database.PrepareDailySummaryDispatch(ctx, summaryDispatchParamsFixture(t, subscription, target, now.Add(-time.Minute), "frozen"))
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		claim, err := database.ClaimDailySummaryDispatch(ctx, d.ID, now)
		if err != nil || claim == nil {
			t.Fatalf("claim=%+v %v", claim, err)
		}
		if err = database.FailDailySummaryDispatch(ctx, d.ID, *claim.Dispatch.ClaimToken, "unknown", "fixture uncertainty", nil, now); err != nil {
			t.Fatal(err)
		}
		if err = database.ResolveUnknownDailySummaryDispatch(ctx, d.ID, attempt == 2, map[bool]string{false: "", true: "verified-message"}[attempt == 2], fmt.Sprintf("review-%d", attempt), now); err != nil {
			t.Fatal(err)
		}
	}
	var count, minAttempt, maxAttempt int
	var notes []string
	if err := pool.QueryRow(ctx, `SELECT COUNT(*),MIN(attempts),MAX(attempts),array_agg(note ORDER BY id) FROM daily_summary_unknown_resolutions WHERE dispatch_id=$1`, d.ID).Scan(&count, &minAttempt, &maxAttempt, &notes); err != nil || count != 2 || minAttempt != 1 || maxAttempt != 2 || len(notes) != 2 || notes[0] != "review-1" || notes[1] != "review-2" {
		t.Fatalf("resolution history overwritten: %d/%d/%d %v %v", count, minAttempt, maxAttempt, notes, err)
	}
}

func TestPostgresDailySummaryLegacyConfirmedHistoryDoesNotBlockNewReportRegardlessOfTTL(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint(expired), func(t *testing.T) {
			database, pool, subscription, target, now := summaryDispatchFixture(t)
			ctx := context.Background()
			end := now.Add(-24 * time.Hour)
			params := summaryDispatchParamsFixture(t, subscription, target, end, "old historical report")
			snapshot, err := database.CreateNotificationDetailSnapshot(ctx, params.Snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if expired {
				if _, err = pool.Exec(ctx, `UPDATE notification_detail_snapshots SET created_at=NOW()-INTERVAL '40 days',expires_at=NOW()-INTERVAL '10 days' WHERE id=$1`, snapshot.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = pool.Exec(ctx, `INSERT INTO daily_summary_deliveries(subscription_id,period_end_at,chat_type,chat_id) VALUES($1,$2,$3,$4)`, subscription.ID, end, target.ChatType, target.ChatID); err != nil {
				t.Fatal(err)
			}
			target.LastPeriodEndAt = &end
			if _, err := database.CleanupAggregationHistory(ctx); err != nil {
				t.Fatal(err)
			}
			var exists bool
			if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notification_detail_snapshots WHERE id=$1)`, snapshot.ID).Scan(&exists); err != nil || exists == expired {
				t.Fatalf("confirmed expired history was unnecessarily retained: exists=%v expired=%v err=%v", exists, expired, err)
			}
			if blocked, err := database.CheckDailySummaryLegacyReviews(ctx, subscription.ID, target.ID, now); err != nil || blocked {
				t.Fatalf("irrelevant old snapshot blocked target=%v %v", blocked, err)
			}
			if d, err := database.PrepareDailySummaryDispatch(ctx, summaryDispatchParamsFixture(t, subscription, target, now.Add(-time.Minute), "new report")); err != nil || d == nil {
				t.Fatalf("new report blocked=%+v %v", d, err)
			}
		})
	}
}

func TestPostgresDailySummaryLegacyExpiredUnconfirmedSnapshotStillBlocksAndIsRetained(t *testing.T) {
	database, pool, subscription, target, now := summaryDispatchFixture(t)
	ctx := context.Background()
	end := now.Add(-24 * time.Hour)
	params := summaryDispatchParamsFixture(t, subscription, target, end, "expired detail with unknown delivery")
	snapshot, err := database.CreateNotificationDetailSnapshot(ctx, params.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE notification_detail_snapshots SET created_at=NOW()-INTERVAL '40 days',expires_at=NOW()-INTERVAL '10 days' WHERE id=$1`, snapshot.ID); err != nil {
		t.Fatal(err)
	}
	// Startup cleanup may run before the daily executor has registered review.
	if _, err := database.CleanupAggregationHistory(ctx); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notification_detail_snapshots WHERE id=$1)`, snapshot.ID).Scan(&exists); err != nil || !exists {
		t.Fatalf("startup cleanup lost unregistered legacy uncertainty: exists=%v err=%v", exists, err)
	}
	if blocked, err := database.CheckDailySummaryLegacyReviews(ctx, subscription.ID, target.ID, now); err != nil || !blocked {
		t.Fatalf("expired unconfirmed report was treated as delivered or unsent: blocked=%v err=%v", blocked, err)
	}
	var review DailySummaryLegacyReview
	review, err = collectOne[DailySummaryLegacyReview](ctx, database.db, `SELECT * FROM daily_summary_legacy_reviews WHERE snapshot_id=$1`, snapshot.ID)
	if err != nil || review.Status != "unknown" || !review.ProofValid || review.PeriodEndAt == nil || !review.PeriodEndAt.Equal(end) {
		t.Fatalf("expired report delivery evidence changed: %+v err=%v", review, err)
	}
	if dispatch, err := database.PrepareDailySummaryDispatch(ctx, summaryDispatchParamsFixture(t, subscription, target, now.Add(-time.Minute), "must not replace uncertain old report")); err != nil || dispatch != nil {
		t.Fatalf("expired unknown was automatically replaced: %+v err=%v", dispatch, err)
	}
	if _, err := database.CleanupAggregationHistory(ctx); err != nil {
		t.Fatal(err)
	}
	var text string
	var dispatches int
	if err := pool.QueryRow(ctx, `SELECT notification_text,(SELECT COUNT(*) FROM daily_summary_dispatches) FROM notification_detail_snapshots WHERE id=$1`, snapshot.ID).Scan(&text, &dispatches); err != nil || text != params.Snapshot.NotificationText || dispatches != 0 {
		t.Fatalf("expired unknown evidence was deleted or rewritten: text=%q dispatches=%d err=%v", text, dispatches, err)
	}
}

func TestPostgresDailySummaryLegacyCandidateBudgetFailsClosed(t *testing.T) {
	database, pool, subscription, target, now := summaryDispatchFixture(t)
	ctx := context.Background()
	for i := 1; i <= 65; i++ {
		params := summaryDispatchParamsFixture(t, subscription, target, now.Add(-time.Duration(i)*time.Minute), "legacy possible send")
		if _, err := database.CreateNotificationDetailSnapshot(ctx, params.Snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.CheckDailySummaryLegacyReviews(ctx, subscription.ID, target.ID, now); !errors.Is(err, notificationdelivery.ErrCapacity) {
		t.Fatalf("unbounded or silently truncated audit=%v", err)
	}
	var dispatches, reviews int
	if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM daily_summary_dispatches),(SELECT COUNT(*) FROM daily_summary_legacy_reviews)`).Scan(&dispatches, &reviews); err != nil || dispatches != 0 || reviews != 0 {
		t.Fatalf("budget failure partially admitted an orphan=%d/%d %v", dispatches, reviews, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE daily_summary_targets SET last_period_end_at=$2 WHERE id=$1`, target.ID, now.Add(-30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if blocked, err := database.CheckDailySummaryLegacyReviews(ctx, subscription.ID, target.ID, now); err != nil || blocked {
		t.Fatalf("confirmed old windows were limited before filtering: %v %v", blocked, err)
	}
}

func TestPostgresDailySummaryDispatchInvalidationPreservesContinuousRenewalAndStopsResume(t *testing.T) {
	for _, inFlight := range []bool{false, true} {
		t.Run(fmt.Sprint(inFlight), func(t *testing.T) {
			database, _, subscription, target, now := summaryDispatchFixture(t)
			ctx := context.Background()
			d, err := database.PrepareDailySummaryDispatch(ctx, summaryDispatchParamsFixture(t, subscription, target, now.Add(-time.Minute), "frozen"))
			if err != nil {
				t.Fatal(err)
			}
			if inFlight {
				if claim, err := database.ClaimDailySummaryDispatch(ctx, d.ID, now); err != nil || claim == nil {
					t.Fatalf("claim=%+v %v", claim, err)
				}
			}
			if _, err = database.ActivateSubscription(ctx, subscription.DeBoxUserID, "professional", 30); err != nil {
				t.Fatal(err)
			}
			if _, err = withTxValue(ctx, database.db, func(db DBTX) (int64, error) {
				return cancelInvalidDailySummaryDispatches(ctx, db, subscription.DeBoxUserID, now)
			}); err != nil {
				t.Fatal(err)
			}
			open, err := database.GetOpenDailySummaryDispatch(ctx, subscription.ID, target.ChatType, target.ChatID)
			if err != nil || open == nil || open.Status != map[bool]string{false: "pending", true: "sending"}[inFlight] {
				t.Fatalf("continuous renewal cancelled valid job=%+v %v", open, err)
			}
			if _, err = database.DisableDailySummarySettings(ctx, subscription.DeBoxUserID, subscription.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = database.UpdateDailySummarySettings(ctx, subscription.DeBoxUserID, DailySummarySettings{Enabled: true, PushTime: "12:00", TimezoneName: "UTC", ChatType: "private", ChatID: subscription.DeBoxUserID, Language: "en"}); err != nil {
				t.Fatal(err)
			}
			open, err = database.GetOpenDailySummaryDispatch(ctx, subscription.ID, target.ChatType, target.ChatID)
			if err != nil || inFlight && (open == nil || open.Status != "unknown") || !inFlight && open != nil {
				t.Fatalf("stop->resume revived old queue=%+v %v", open, err)
			}
			if claim, err := database.ClaimDailySummaryDispatch(ctx, d.ID, now.Add(time.Minute)); err != nil || claim != nil {
				t.Fatalf("stopped dispatch reclaimed=%+v %v", claim, err)
			}
		})
	}
}

func TestPostgresDailySummaryDispatchCapacityFailureDoesNotPretendStopSucceeded(t *testing.T) {
	database, pool, subscription, target, now := summaryDispatchFixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO daily_summary_dispatches(subscription_id,target_id,chat_type,chat_id,period_start_at,period_end_at,local_date,payload,status)
	 SELECT $1,$2,'private','oversized-'||n,$3::timestamptz-INTERVAL '1 day',$3::timestamptz,'2026-11-14',jsonb_build_object('chat_id','oversized-'||n,'chat_type','private','text','fixture'),'pending' FROM generate_series(1,1001) n`, subscription.ID, target.ID, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DisableDailySummarySettings(ctx, subscription.DeBoxUserID, subscription.ID); !errors.Is(err, notificationdelivery.ErrCapacity) {
		t.Fatalf("oversized stop was accepted: %v", err)
	}
	var enabled, targetEnabled, pending int
	if err := pool.QueryRow(ctx, `SELECT daily_summary_enabled FROM subscriptions WHERE id=$1`, subscription.ID).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT enabled FROM daily_summary_targets WHERE id=$1`, target.ID).Scan(&targetEnabled); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM daily_summary_dispatches WHERE status='pending'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 || targetEnabled != 1 || pending != 1001 {
		t.Fatalf("capacity error partially committed: %d %d %d", enabled, targetEnabled, pending)
	}
}
