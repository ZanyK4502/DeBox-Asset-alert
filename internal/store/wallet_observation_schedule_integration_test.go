package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"
)

func TestPostgresWalletDuePagesKeepPeriodOrderAndFixedTraversalFence(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO subscriptions(debox_user_id,plan_code,status,starts_at,expires_at)
	 SELECT 'wallet-schedule-'||n,'professional','active',NOW()-INTERVAL '1 day',NOW()+INTERVAL '30 days'
	 FROM generate_series(0,2)n`); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `INSERT INTO watch_rules(debox_user_id,chain_key,chain_id,wallet_address,rule_type,threshold,notification_chat_id,last_value,last_checked_at)
	 SELECT 'wallet-schedule-'||((n-1)/100),'bsc',56,'0x'||LPAD(TO_HEX(n),40,'0'),'balance_change','0','fixture-route','10',NULL
	 FROM generate_series(1,201)n RETURNING id`)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := collectInt64Rows(rows)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	fence, err := db.WalletObservationDueFence(ctx, time.Minute)
	if err != nil || fence == nil || fence.LastCheckedAt != nil || fence.RuleID != ids[200] {
		t.Fatalf("fence=%+v err=%v", fence, err)
	}
	first, err := db.ListDueWatchRulesPage(ctx, 200, time.Minute, nil, fence)
	if err != nil || len(first) != 200 || first[0].ID != ids[0] || first[199].ID != ids[199] {
		t.Fatalf("first len=%d err=%v", len(first), err)
	}
	// Model successes moving to a new sort key. A failed/unknown old row is
	// intentionally left unchanged and must not trap this transient traversal.
	if _, err := pool.Exec(ctx, `UPDATE watch_rules SET last_checked_at=NOW(),last_value='12' WHERE id=ANY($1)`, ids[1:200]); err != nil {
		t.Fatal(err)
	}
	after := &WalletObservationCursor{RuleID: first[199].ID, LastCheckedAt: first[199].LastCheckedAt}
	tail, err := db.ListDueWatchRulesPage(ctx, 200, time.Minute, after, fence)
	if err != nil || len(tail) != 1 || tail[0].ID != ids[200] {
		t.Fatalf("tail=%+v err=%v", tail, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE watch_rules SET last_checked_at=NOW() WHERE id=$1`, ids[200]); err != nil {
		t.Fatal(err)
	}
	newPass, err := db.ListDueWatchRules(ctx, 200, time.Minute)
	if err != nil || len(newPass) != 1 || newPass[0].ID != ids[0] {
		t.Fatalf("successes ran early or old unknown lost: %+v/%v", newPass, err)
	}
	if err := db.DeferWalletObservation(ctx, ids[0], time.Minute); err != nil {
		t.Fatal(err)
	}
	empty, err := db.ListDueWatchRules(ctx, 200, time.Minute)
	if err != nil || len(empty) != 0 {
		t.Fatalf("backoff ignored: %+v/%v", empty, err)
	}
	next, err := db.NextWalletObservationAt(ctx, time.Minute)
	if err != nil || next == nil || time.Until(*next) < 55*time.Second || time.Until(*next) > 61*time.Second {
		t.Fatalf("next=%v err=%v", next, err)
	}
	var value string
	var baseline *time.Time
	if err := pool.QueryRow(ctx, `SELECT last_value,last_checked_at FROM watch_rules WHERE id=$1`, ids[0]).Scan(&value, &baseline); err != nil || value != "10" || baseline != nil {
		t.Fatalf("defer changed business baseline: %s/%v/%v", value, baseline, err)
	}
	// Legacy/manual list keeps its own contract: it does not acquire the new
	// minimum observation interval just because the runner now supports it.
	legacy, err := db.ListEnabledWatchRules(ctx, 200)
	if err != nil || len(legacy) != 200 {
		t.Fatalf("legacy list changed: %d/%v", len(legacy), err)
	}
}

func TestPostgresWalletDueNextProbeHandlesBothDominantClocksAndCustomPeriod(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "standard", "realtime")
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE watch_rules SET last_checked_at=NOW()-INTERVAL '2 minutes',next_observation_at=NOW()+INTERVAL '10 seconds' WHERE id=$1`, rule.ID); err != nil {
		t.Fatal(err)
	}
	soon, err := db.NextWalletObservationAt(ctx, time.Minute)
	if err != nil || soon == nil || time.Until(*soon) < 8*time.Second || time.Until(*soon) > 11*time.Second {
		t.Fatalf("backoff dominant next=%v/%v", soon, err)
	}
	later, err := db.NextWalletObservationAt(ctx, 5*time.Minute)
	if err != nil || later == nil || time.Until(*later) < 178*time.Second || time.Until(*later) > 181*time.Second {
		t.Fatalf("period dominant next=%v/%v", later, err)
	}
	due, err := db.ListDueWatchRules(ctx, 200, 5*time.Minute)
	if err != nil || len(due) != 0 {
		t.Fatalf("custom period ignored: %+v/%v", due, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE watch_rules SET next_observation_at=NOW()-INTERVAL '1 second' WHERE id=$1`, rule.ID); err != nil {
		t.Fatal(err)
	}
	due, err = db.ListDueWatchRules(ctx, 200, time.Minute)
	if err != nil || len(due) != 1 {
		t.Fatalf("retry never became due: %+v/%v", due, err)
	}
}

func TestPostgresWalletSampleEvidenceDoesNotReplaceDetectionWindowTime(t *testing.T) {
	for _, mode := range []string{"realtime", "stage"} {
		t.Run(mode, func(t *testing.T) {
			db, pool, rule := walletDeliveryFixture(t, "standard", mode)
			ctx := context.Background()
			if _, err := pool.Exec(ctx, `UPDATE watch_rules SET last_checked_at=NOW()-INTERVAL '1 minute' WHERE id=$1`, rule.ID); err != nil {
				t.Fatal(err)
			}
			rule = walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "standard")
			at := time.Now().UTC().Truncate(time.Microsecond).Add(-30 * time.Second)
			params := walletTestObservation(rule, "12")
			params.ReadObservedAt = &at
			params.BuildNotification = func(result WalletObservationResult) (CreateNotificationDetailSnapshotParams, WalletNotificationPayload, error) {
				if result.ReadObservedAt == nil || !result.ReadObservedAt.Equal(at) || result.SourceOccurredAt != nil {
					t.Fatal("sample evidence became chain occurrence")
				}
				sp, payload, err := walletTestBuilder(result)
				sp.Details, _ = json.Marshal(map[string]any{"read_observed_at": result.ReadObservedAt})
				return sp, payload, err
			}
			result, err := db.RecordWalletObservation(ctx, params)
			if err != nil || result.Outbox == nil {
				t.Fatalf("observation=%+v/%v", result, err)
			}
			var payload WalletNotificationPayload
			if err := json.Unmarshal(result.Outbox.Payload, &payload); err != nil || payload.ReadObservedAt == nil || !payload.ReadObservedAt.Equal(at) || payload.SourceOccurredAt != nil {
				t.Fatalf("frozen payload=%+v/%v", payload, err)
			}
			if mode == "stage" {
				var occurred *time.Time
				var created, sampled time.Time
				var originalWindow bool
				if err := pool.QueryRow(ctx, `SELECT evt.occurred_at,evt.created_at,(evt.details->>'read_observed_at')::timestamptz,
				 evt.created_at>=aw.starts_at AND evt.created_at<aw.ends_at
				 FROM rule_trigger_events evt JOIN aggregation_windows aw ON aw.id=evt.aggregation_window_id
				 WHERE evt.watch_rule_id=$1`, rule.ID).Scan(&occurred, &created, &sampled, &originalWindow); err != nil || !sampled.Equal(at) || occurred != nil || !created.After(at.Add(20*time.Second)) || !originalWindow {
					t.Fatalf("sample changed stage detection/occurrence time: occurred=%v created=%s sampled=%s window=%v err=%v", occurred, created, sampled, originalWindow, err)
				}
			}
			var raw string
			if err := pool.QueryRow(ctx, `SELECT details->>'read_observed_at' FROM notification_detail_snapshots WHERE id=$1`, result.Outbox.SnapshotID).Scan(&raw); err != nil || raw == "" {
				t.Fatal("sample missing from details", err)
			}
			if _, err := db.RecordWalletObservation(ctx, params); !errors.Is(err, ErrWalletObservationStale) {
				t.Fatalf("old CAS was reused: %v", err)
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM wallet_notification_outbox`).Scan(&count); err != nil || count != 1 {
				t.Fatal(fmt.Sprint("replay duplicated frozen evidence: ", count, err))
			}
		})
	}
}

func TestPostgresWalletTraversalMaxIDExcludesConcurrentNullInsert(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "standard", "realtime")
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE watch_rules SET last_checked_at=NOW()-INTERVAL '1 hour' WHERE id=$1`, rule.ID); err != nil {
		t.Fatal(err)
	}
	fence, err := db.WalletObservationDueFence(ctx, time.Minute)
	if err != nil || fence == nil || fence.MaxRuleID != rule.ID || fence.LastCheckedAt == nil {
		t.Fatalf("fence=%+v err=%v", fence, err)
	}
	var newID int64
	if err := pool.QueryRow(ctx, `INSERT INTO watch_rules(debox_user_id,chain_key,chain_id,wallet_address,rule_type,threshold,notification_chat_id,last_checked_at)
	 VALUES($1,'bsc',56,'0x2222222222222222222222222222222222222222','balance_change','0','fixture-route',NULL) RETURNING id`, rule.DeBoxUserID).Scan(&newID); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListDueWatchRulesPage(ctx, 200, time.Minute, nil, fence)
	if err != nil || len(rows) != 1 || rows[0].ID != rule.ID {
		t.Fatalf("new null ID entered fixed pass: %+v/%v", rows, err)
	}
	nextFence, err := db.WalletObservationDueFence(ctx, time.Minute)
	if err != nil || nextFence == nil || nextFence.MaxRuleID != newID {
		t.Fatalf("new pass did not refresh max ID: %+v/%v", nextFence, err)
	}
	rows, err = db.ListDueWatchRulesPage(ctx, 200, time.Minute, nil, nextFence)
	if err != nil || len(rows) != 2 || rows[0].ID != newID {
		t.Fatalf("new row lost from next pass: %+v/%v", rows, err)
	}
}
