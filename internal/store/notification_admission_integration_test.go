package store

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
)

func TestPostgresNotificationAdmissionSerializesFiveAcrossInstancesAndTenWhenVerified(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	var group sync.WaitGroup
	permits := make(chan notificationdelivery.AdmissionPermit, 24)
	errors := make(chan error, 24)
	for range 24 {
		group.Add(1)
		go func() {
			defer group.Done()
			// Independent Store wrappers model independent processes sharing PG.
			permit, err := newWithDB(pool).ReserveNotificationAdmission(ctx, "same-bot", 5)
			if err == nil {
				permits <- permit
			} else if _, waiting := notificationdelivery.AdmissionRetryAt(err); !waiting {
				errors <- err
			}
		}()
	}
	group.Wait()
	close(permits)
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if len(permits) != 5 {
		t.Fatalf("admitted=%d", len(permits))
	}
	for permit := range permits {
		if err := database.CheckNotificationAdmission(ctx, permit); err != nil {
			t.Fatal(err)
		}
		if err := newWithDB(pool).CheckNotificationAdmission(ctx, permit); err == nil {
			t.Fatal("durable consumed permit was reused")
		}
		if err := database.ReleaseNotificationAdmission(ctx, permit); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := newWithDB(pool).ReserveNotificationAdmission(ctx, "same-bot", 5); err == nil {
		t.Fatal("restart or release bypassed the rolling five")
	}
	for range 10 {
		if _, err := database.ReserveNotificationAdmission(ctx, "verified-ten-bot", 10); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.ReserveNotificationAdmission(ctx, "verified-ten-bot", 10); err == nil {
		t.Fatal("verified bot exceeded ten")
	}
	var maximum int
	if err := pool.QueryRow(ctx, `SELECT MAX(jsonb_array_length(reservations)) FROM notification_send_admission`).Scan(&maximum); err != nil || maximum != 10 {
		t.Fatalf("unbounded state=%d %v", maximum, err)
	}
}

func TestPostgresNotificationAdmissionRefreshesWindowAtHTTPAndSharesLongestServerDelay(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	permit, err := database.ReserveNotificationAdmission(ctx, "same-bot", 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CheckNotificationAdmission(ctx, permit); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT reservations FROM notification_send_admission WHERE bot_id='same-bot'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var live []notificationdelivery.AdmissionPermit
	if err := json.Unmarshal(raw, &live); err != nil || len(live) != 1 || !live[0].Consumed || !live[0].GrantedAt.After(permit.GrantedAt) || !live[0].ExpiresAt.Equal(live[0].GrantedAt.Add(time.Minute)) {
		t.Fatalf("not anchored at transport: %+v %v", live, err)
	}
	unused, err := database.ReserveNotificationAdmission(ctx, "same-bot", 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.BlockNotificationAdmission(ctx, "same-bot", 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := newWithDB(pool).BlockNotificationAdmission(ctx, "same-bot", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := database.CheckNotificationAdmission(ctx, unused); err == nil {
		t.Fatal("new block did not stop unused reservation")
	}
	if _, err := newWithDB(pool).ReserveNotificationAdmission(ctx, "same-bot", 5); err == nil {
		t.Fatal("server block lost across instances")
	} else if at, ok := notificationdelivery.AdmissionRetryAt(err); !ok || time.Until(at) < 110*time.Second {
		t.Fatalf("shorter response shortened server delay: %s %v", at, err)
	}
	if err := database.ReleaseNotificationAdmission(ctx, unused); err != nil {
		t.Fatal(err)
	}
	// Simulate only time expiry in this isolated fixture, not a reset API.
	if _, err := pool.Exec(ctx, `UPDATE notification_send_admission SET blocked_until=NOW()-INTERVAL '1 second',reservations='[]' WHERE bot_id='same-bot'`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ReserveNotificationAdmission(ctx, "same-bot", 5); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresNotificationAdmissionHasHardLockBudgetAndCancelledContextDoesNotWrite(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	if _, err := database.ReserveNotificationAdmission(ctx, "same-bot", 5); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT bot_id FROM notification_send_admission WHERE bot_id='same-bot' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := database.ReserveNotificationAdmission(ctx, "same-bot", 5); err == nil || time.Since(started) > 3*time.Second {
		t.Fatalf("lock budget exceeded: %s %v", time.Since(started), err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := database.ReserveNotificationAdmission(cancelled, "cancelled-bot", 5); err == nil {
		t.Fatal("cancelled admission succeeded")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM notification_send_admission WHERE bot_id='cancelled-bot'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cancelled context wrote state: %d %v", count, err)
	}
}

func TestPostgresWalletAdmissionDeferralRefundsOnlyFencedUnsentStart(t *testing.T) {
	database, _, rule := walletDeliveryFixture(t, "free", "realtime")
	ctx := context.Background()
	observation, err := database.RecordWalletObservation(ctx, walletTestObservation(rule, "12"))
	if err != nil || observation.Outbox == nil {
		t.Fatalf("observation=%+v %v", observation, err)
	}
	frozen := string(observation.Outbox.Payload)
	for _, start := range []bool{false, true} {
		claim, err := database.ClaimWalletNotifications(ctx, observation.Outbox.ID, 1)
		if err != nil || len(claim) != 1 {
			t.Fatalf("claim=%+v %v", claim, err)
		}
		row := claim[0]
		if start {
			row, err = database.StartWalletNotificationAttempt(ctx, row.ID, *row.LeaseToken)
			if err != nil {
				t.Fatal(err)
			}
		}
		token := *row.LeaseToken
		if _, err := database.DeferWalletNotificationAdmission(ctx, row.ID, "wrong-token", time.Now()); err == nil {
			t.Fatal("wrong token refunded an attempt")
		}
		deferred, err := database.DeferWalletNotificationAdmission(ctx, row.ID, token, time.Now().Add(-time.Millisecond))
		if err != nil || deferred.Attempts != 0 || deferred.Status != "retry" || string(deferred.Payload) != frozen || deferred.SendStartedAt != nil {
			t.Fatalf("deferred=%+v %v", deferred, err)
		}
		if _, err := database.DeferWalletNotificationAdmission(ctx, row.ID, token, time.Now()); err == nil {
			t.Fatal("same deferral refunded again")
		}
	}
	used, err := database.CountDailyAlertEvents(ctx, rule.DeBoxUserID, "Asia/Shanghai")
	if err != nil || used != 1 {
		t.Fatalf("admission wait recounted free quota: %d %v", used, err)
	}
	claim, _ := database.ClaimWalletNotifications(ctx, observation.Outbox.ID, 1)
	started, err := database.StartWalletNotificationAttempt(ctx, claim[0].ID, *claim[0].LeaseToken)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.FinishWalletNotification(ctx, FinishWalletNotificationParams{ID: started.ID, LeaseToken: *started.LeaseToken, Status: "unknown", Error: "response lost"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DeferWalletNotificationAdmission(ctx, started.ID, *started.LeaseToken, time.Now()); err == nil {
		t.Fatal("unknown send was refunded")
	}
}

func TestPostgresDailySummaryAdmissionWaitKeepsAttemptsAndFrozenCutoff(t *testing.T) {
	database, _, subscription, target, now := summaryDispatchFixture(t)
	ctx := context.Background()
	params := summaryDispatchParamsFixture(t, subscription, target, now.Add(-time.Minute), "frozen body")
	job, err := database.PrepareDailySummaryDispatch(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	wait := now.Add(time.Minute)
	if err := database.DeferDailySummaryAdmission(ctx, job.ID, "", wait, now); err != nil {
		t.Fatal(err)
	}
	open, _ := database.GetOpenDailySummaryDispatch(ctx, subscription.ID, target.ChatType, target.ChatID)
	if open.Attempts != 0 || open.Status != "pending" || open.Payload != job.Payload || !open.PeriodStartAt.Equal(job.PeriodStartAt) || open.RetryAt == nil || !open.RetryAt.Equal(wait) {
		t.Fatalf("unclaimed wait mutated job: %+v", open)
	}
	claim, err := database.ClaimDailySummaryDispatch(ctx, job.ID, wait)
	if err != nil || claim == nil || claim.Dispatch.Attempts != 1 {
		t.Fatalf("claim=%+v %v", claim, err)
	}
	if err := database.DeferDailySummaryAdmission(ctx, job.ID, "wrong", wait, wait); err == nil {
		t.Fatal("wrong token refunded daily attempt")
	}
	if err := database.DeferDailySummaryAdmission(ctx, job.ID, *claim.Dispatch.ClaimToken, wait.Add(time.Minute), wait); err != nil {
		t.Fatal(err)
	}
	open, _ = database.GetOpenDailySummaryDispatch(ctx, subscription.ID, target.ChatType, target.ChatID)
	if open.Attempts != 0 || open.Status != "failed" || open.Payload != job.Payload || open.ClaimToken != nil {
		t.Fatalf("claimed wait mutated payload/attempts: %+v", open)
	}
	claim, err = database.ClaimDailySummaryDispatch(ctx, job.ID, wait.Add(2*time.Minute))
	if err != nil || claim == nil {
		t.Fatalf("next claim=%+v %v", claim, err)
	}
	if err := database.FailDailySummaryDispatch(ctx, job.ID, *claim.Dispatch.ClaimToken, "unknown", "response lost", nil, wait.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := database.DeferDailySummaryAdmission(ctx, job.ID, *claim.Dispatch.ClaimToken, wait.Add(3*time.Minute), wait.Add(2*time.Minute)); err == nil {
		t.Fatal("unknown daily send was refunded")
	}
}
