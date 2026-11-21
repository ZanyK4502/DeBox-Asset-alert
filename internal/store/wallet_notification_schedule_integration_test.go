package store

import (
	"context"
	"testing"
	"time"
)

func assertNextWalletNotificationAt(t *testing.T, db *Store, want *time.Time, notBefore ...time.Time) {
	t.Helper()
	gate := time.Time{}
	if len(notBefore) > 0 {
		gate = notBefore[0]
	}
	at, err := db.NextWalletNotificationAt(context.Background(), gate)
	if err != nil || (at == nil) != (want == nil) || (at != nil && !at.Equal(*want)) {
		t.Fatalf("next wallet notification=%v want=%v error=%v", at, want, err)
	}
}

func TestPostgresWalletNotificationScheduleEmptyTerminalAndRetry(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "standard", "realtime")
	ctx := context.Background()
	assertNextWalletNotificationAt(t, db, nil)
	result, err := db.RecordWalletObservation(ctx, walletTestObservation(rule, "12"))
	if err != nil || result.Outbox == nil {
		t.Fatal(result, err)
	}
	row := result.Outbox
	assertNextWalletNotificationAt(t, db, &row.NextAttemptAt)
	for _, status := range []string{"unknown", "sent", "failed", "skipped"} {
		if _, err := pool.Exec(ctx, `UPDATE wallet_notification_outbox SET status=$2 WHERE id=$1`, row.ID, status); err != nil {
			t.Fatal(err)
		}
		assertNextWalletNotificationAt(t, db, nil)
	}
	retryAt := time.Now().UTC().Add(47 * time.Second).Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `UPDATE wallet_notification_outbox SET status='retry',attempts=2,next_attempt_at=$2 WHERE id=$1`, row.ID, retryAt); err != nil {
		t.Fatal(err)
	}
	assertNextWalletNotificationAt(t, db, &retryAt)
	var status string
	var attempts int32
	if err := pool.QueryRow(ctx, `SELECT status,attempts FROM wallet_notification_outbox WHERE id=$1`, row.ID).Scan(&status, &attempts); err != nil || status != "retry" || attempts != 2 {
		t.Fatalf("schedule lookup mutated retry=%s/%d/%v", status, attempts, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE wallet_notification_outbox SET attempts=5 WHERE id=$1`, row.ID); err != nil {
		t.Fatal(err)
	}
	assertNextWalletNotificationAt(t, db, nil)
}

func TestPostgresWalletNotificationScheduleLeaseWakeupPreservesUnknownPolicy(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "standard", "realtime")
	ctx := context.Background()
	result, err := db.RecordWalletObservation(ctx, walletTestObservation(rule, "12"))
	if err != nil || result.Outbox == nil {
		t.Fatal(result, err)
	}
	claims, err := db.ClaimWalletNotifications(ctx, result.Outbox.ID, 1)
	if err != nil || len(claims) != 1 {
		t.Fatal(claims, err)
	}
	claim := claims[0]
	assertNextWalletNotificationAt(t, db, claim.LeaseExpiresAt)
	expiredAt := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `UPDATE wallet_notification_outbox SET lease_expires_at=$2 WHERE id=$1`, claim.ID, expiredAt); err != nil {
		t.Fatal(err)
	}
	assertNextWalletNotificationAt(t, db, &expiredAt)
	// The hint must leave the lease untouched. Existing Claim safely recovers
	// an unstarted send, while an expired started send stays delivery-unknown.
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM wallet_notification_outbox WHERE id=$1`, claim.ID).Scan(&status); err != nil || status != "sending" {
		t.Fatalf("lookup recovered a lease itself=%s/%v", status, err)
	}
	claims, err = db.ClaimWalletNotifications(ctx, claim.ID, 1)
	if err != nil || len(claims) != 1 || claims[0].Attempts != 0 {
		t.Fatalf("existing unstarted lease recovery=%+v/%v", claims, err)
	}
	claim = claims[0]
	if _, err = db.StartWalletNotificationAttempt(ctx, claim.ID, *claim.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE wallet_notification_outbox SET lease_expires_at=$2 WHERE id=$1`, claim.ID, expiredAt); err != nil {
		t.Fatal(err)
	}
	assertNextWalletNotificationAt(t, db, &expiredAt)
	claims, err = db.ClaimWalletNotifications(ctx, claim.ID, 1)
	if err != nil || len(claims) != 0 {
		t.Fatalf("expired started send retried=%+v/%v", claims, err)
	}
	assertNextWalletNotificationAt(t, db, nil)
	var attempts int32
	if err := pool.QueryRow(ctx, `SELECT status,attempts FROM wallet_notification_outbox WHERE id=$1`, claim.ID).Scan(&status, &attempts); err != nil || status != "unknown" || attempts != 1 {
		t.Fatalf("unknown-send evidence altered=%s/%d/%v", status, attempts, err)
	}
}

func TestPostgresWalletNotificationScheduleIgnoresIneligiblePendingHead(t *testing.T) {
	for _, invalid := range []string{"paused", "disabled", "generation", "destination", "entitlement", "recovery_boundary", "group_disabled", "combination"} {
		t.Run(invalid, func(t *testing.T) {
			plan := "standard"
			if invalid == "group_disabled" {
				plan = "professional"
			}
			db, pool, rule := walletDeliveryFixture(t, plan, "realtime")
			ctx := context.Background()
			first, err := db.RecordWalletObservation(ctx, walletTestObservation(rule, "12"))
			if err != nil || first.Outbox == nil {
				t.Fatal(first, err)
			}
			const other = "wallet-schedule-other-user"
			if _, err := db.ActivateSubscription(ctx, other, "standard", 30); err != nil {
				t.Fatal(err)
			}
			previous := "20"
			secondRule, err := db.CreateWatchRule(ctx, CreateWatchRuleParams{DeBoxUserID: other, ChainKey: "bsc", ChainID: 56, WalletAddress: "0x2222222222222222222222222222222222222222", RuleType: "balance_change", Threshold: "0", NotificationChatID: other, NotificationChatType: "private", DeliveryMode: "realtime", LastValue: &previous})
			if err != nil {
				t.Fatal(err)
			}
			secondRule = walletDeliveryRule(t, db, secondRule.ID, other, "standard")
			second, err := db.RecordWalletObservation(ctx, walletTestObservation(secondRule, "22"))
			if err != nil || second.Outbox == nil {
				t.Fatal(second, err)
			}
			earlier := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
			later := time.Now().UTC().Add(20 * time.Minute).Truncate(time.Microsecond)
			if _, err := pool.Exec(ctx, `UPDATE wallet_notification_outbox SET next_attempt_at=CASE WHEN id=$1 THEN $3::timestamptz ELSE $4::timestamptz END WHERE id IN($1,$2)`, first.Outbox.ID, second.Outbox.ID, earlier, later); err != nil {
				t.Fatal(err)
			}
			switch invalid {
			case "paused":
				_, err = pool.Exec(ctx, `UPDATE watch_rules SET run_status='paused' WHERE id=$1`, rule.ID)
			case "disabled":
				_, err = pool.Exec(ctx, `UPDATE watch_rules SET enabled=0 WHERE id=$1`, rule.ID)
			case "generation":
				_, err = pool.Exec(ctx, `UPDATE watch_rules SET notification_generation=notification_generation+1 WHERE id=$1`, rule.ID)
			case "destination":
				_, err = pool.Exec(ctx, `UPDATE watch_rules SET notification_chat_id='new-destination' WHERE id=$1`, rule.ID)
			case "entitlement":
				_, err = pool.Exec(ctx, `UPDATE subscriptions SET expires_at=NOW()-INTERVAL '1 second' WHERE debox_user_id=$1`, rule.DeBoxUserID)
			case "recovery_boundary":
				cutoff := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
				if _, err = pool.Exec(ctx, `UPDATE wallet_notification_outbox SET created_at=$2 WHERE id=$1`, first.Outbox.ID, cutoff.Add(-time.Second)); err == nil {
					_, err = db.ConfigureNotificationRecoveryBoundary(ctx, cutoff, 0, "isolated wallet scheduler eligibility fixture")
				}
			case "group_disabled":
				if _, err = pool.Exec(ctx, `UPDATE watch_rules SET notification_chat_id='disabled-group',notification_chat_type='group' WHERE id=$1`, rule.ID); err == nil {
					_, err = pool.Exec(ctx, `UPDATE wallet_notification_outbox SET payload=payload||'{"chat_id":"disabled-group","chat_type":"group"}'::jsonb WHERE id=$1`, first.Outbox.ID)
				}
			case "combination":
				_, err = pool.Exec(ctx, `UPDATE wallet_notification_outbox SET combination_rule_id=987654321 WHERE id=$1`, first.Outbox.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			assertNextWalletNotificationAt(t, db, &later)
			var status string
			if err := pool.QueryRow(ctx, `SELECT status FROM wallet_notification_outbox WHERE id=$1`, first.Outbox.ID).Scan(&status); err != nil || status != "pending" {
				t.Fatalf("hint changed stale pending source instead of filtering it=%s/%v", status, err)
			}
			if _, err := pool.Exec(ctx, `UPDATE wallet_notification_outbox SET status='unknown' WHERE id=$1`, second.Outbox.ID); err != nil {
				t.Fatal(err)
			}
			assertNextWalletNotificationAt(t, db, nil)
		})
	}
}

func TestPostgresWalletNotificationScheduleGateDoesNotDelaySendLease(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "standard", "realtime")
	ctx := context.Background()
	gate := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Microsecond)
	assertNextWalletNotificationAt(t, db, nil, gate)
	first, err := db.RecordWalletObservation(ctx, walletTestObservation(rule, "12"))
	if err != nil || first.Outbox == nil {
		t.Fatal(first, err)
	}
	assertNextWalletNotificationAt(t, db, &gate, gate)
	claims, err := db.ClaimWalletNotifications(ctx, first.Outbox.ID, 1)
	if err != nil || len(claims) != 1 {
		t.Fatal(claims, err)
	}
	rule = walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "standard")
	second, err := db.RecordWalletObservation(ctx, walletTestObservation(rule, "14"))
	if err != nil || second.Outbox == nil {
		t.Fatal(second, err)
	}
	lease := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `UPDATE wallet_notification_outbox SET lease_expires_at=$2 WHERE id=$1`, first.Outbox.ID, lease); err != nil {
		t.Fatal(err)
	}
	assertNextWalletNotificationAt(t, db, &lease, gate)
	expired := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `UPDATE wallet_notification_outbox SET lease_expires_at=$2,attempts=5 WHERE id=$1`, first.Outbox.ID, expired); err != nil {
		t.Fatal(err)
	}
	assertNextWalletNotificationAt(t, db, &expired, gate)
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM wallet_notification_outbox WHERE id=$1`, first.Outbox.ID).Scan(&status); err != nil || status != "sending" {
		t.Fatalf("gate hint changed original lease state=%s/%v", status, err)
	}
}
