package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	"github.com/jackc/pgx/v5/pgxpool"
)

func walletDeliveryFixture(t *testing.T, plan, mode string) (*Store, *pgxpool.Pool, WatchRule) {
	t.Helper()
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	user := "wallet-delivery-user"
	if _, err := pool.Exec(ctx, `INSERT INTO subscriptions(debox_user_id,plan_code,status,starts_at,expires_at) VALUES($1,$2,'active',NOW()-INTERVAL '1 day',NOW()+INTERVAL '30 days')`, user, plan); err != nil {
		t.Fatal(err)
	}
	previous := "10"
	rule, err := db.CreateWatchRule(ctx, CreateWatchRuleParams{DeBoxUserID: user, ChainKey: "bsc", ChainID: 56, WalletAddress: "0x1111111111111111111111111111111111111111", RuleType: "balance_change", Threshold: "0", NotificationChatID: user, NotificationChatType: "private", NotificationLanguage: "zh", DeliveryMode: mode, CycleType: "fixed", CycleMinutes: 60, TriggerCountThreshold: 1, LastValue: &previous})
	if err != nil {
		t.Fatal(err)
	}
	if plan == "free" {
		if _, err := db.SetFreeWatchRule(ctx, user, rule.ID); err != nil {
			t.Fatal(err)
		}
	}
	rule = walletDeliveryRule(t, db, rule.ID, user, plan)
	return db, pool, rule
}

func walletDeliveryRule(t *testing.T, db *Store, id int64, user, plan string) WatchRule {
	t.Helper()
	r, err := db.GetWatchRule(context.Background(), id, user)
	if err != nil || r == nil {
		t.Fatalf("rule=%+v err=%v", r, err)
	}
	r.EffectivePlanCode = plan
	return *r
}

func walletTestBuilder(r WalletObservationResult) (CreateNotificationDetailSnapshotParams, WalletNotificationPayload, error) {
	p := CreateNotificationDetailSnapshotParams{DeBoxUserID: r.Rule.DeBoxUserID, RuleID: &r.Rule.ID, RuleType: r.Rule.RuleType, NotificationChatID: r.Rule.NotificationChatID, NotificationChatType: r.Rule.NotificationChatType, NotificationLanguage: r.Rule.NotificationLanguage, NotificationText: "frozen original body", Details: json.RawMessage(`{"frozen":"original"}`)}
	switch {
	case r.Event != nil:
		p.SourceType = "alert_event"
		p.SourceID = &r.Event.ID
		p.SourceKey = fmt.Sprintf("address_realtime:%d", r.Event.ID)
		p.NotificationKind = NotificationKindAddressRealtime
	case r.Stage != nil && r.Stage.Notification != nil:
		p.SourceType = "aggregate_notification"
		p.SourceID = &r.Stage.Notification.ID
		p.SourceKey = fmt.Sprintf("address_stage:%d", *p.SourceID)
		p.NotificationKind = NotificationKindAddressStage
	case r.Combination != nil && r.Combination.Notification != nil:
		p.SourceType = "aggregate_notification"
		p.SourceID = &r.Combination.Notification.ID
		p.SourceKey = fmt.Sprintf("address_combination:%d", *p.SourceID)
		p.NotificationKind = NotificationKindAddressCombination
	default:
		return p, WalletNotificationPayload{}, ErrInvalidNotificationSnapshot
	}
	return p, WalletNotificationPayload{Payload: notificationdelivery.Payload{ActionText: "details", ActionURL: "https://original.invalid?notification_id=" + WalletNotificationIDPlaceholder}}, nil
}

func walletTestObservation(rule WatchRule, current string) RecordWalletObservationParams {
	return RecordWalletObservationParams{Rule: rule, CurrentValue: current, Matched: true, TokenSymbol: "BNB", BuildNotification: walletTestBuilder}
}

func TestPostgresWalletObservationAtomicRollbackCASAndLegitimateCycles(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "standard", "realtime")
	ctx := context.Background()
	p := walletTestObservation(rule, "12")
	p.BuildNotification = func(WalletObservationResult) (CreateNotificationDetailSnapshotParams, WalletNotificationPayload, error) {
		return CreateNotificationDetailSnapshotParams{}, WalletNotificationPayload{}, errors.New("injected snapshot builder failure")
	}
	if _, err := db.RecordWalletObservation(ctx, p); err == nil {
		t.Fatal("expected rollback")
	}
	read := walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "standard")
	if *read.LastValue != "10" || read.LastCheckedAt == nil || !read.LastCheckedAt.Equal(*rule.LastCheckedAt) {
		t.Fatalf("baseline advanced on failure: %+v", read)
	}
	for _, table := range []string{"alert_events", "notification_detail_snapshots", "wallet_notification_outbox"} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s count=%d err=%v", table, n, err)
		}
	}
	p.BuildNotification = walletTestBuilder
	first, err := db.RecordWalletObservation(ctx, p)
	if err != nil || first.Outbox == nil {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if _, err := db.RecordWalletObservation(ctx, p); !errors.Is(err, ErrWalletObservationStale) {
		t.Fatalf("duplicate error=%v", err)
	}
	for _, current := range []string{"10", "12"} {
		next := walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "standard")
		if _, err := db.RecordWalletObservation(ctx, walletTestObservation(next, current)); err != nil {
			t.Fatal(err)
		}
	}
	var events, outboxes int
	if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM alert_events),(SELECT COUNT(*) FROM wallet_notification_outbox)`).Scan(&events, &outboxes); err != nil || events != 3 || outboxes != 3 {
		t.Fatalf("events=%d outbox=%d err=%v", events, outboxes, err)
	}
	next := walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "standard")
	bad := walletTestObservation(next, "15")
	bad.BuildNotification = func(r WalletObservationResult) (CreateNotificationDetailSnapshotParams, WalletNotificationPayload, error) {
		sp, pay, err := walletTestBuilder(r)
		pay.ActionURL = strings.Repeat("a", WalletNotificationPayloadLimit+1)
		return sp, pay, err
	}
	if _, err := db.RecordWalletObservation(ctx, bad); err == nil {
		t.Fatal("oversize payload accepted")
	}
	after := walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "standard")
	if *after.LastValue != "12" || !after.LastCheckedAt.Equal(*next.LastCheckedAt) {
		t.Fatal("oversize advanced baseline")
	}
	// PostgreSQL's JSONB text representation adds whitespace to a compact JSON
	// payload at the exact budget. Its CHECK failure is still a typed rollback.
	exact := walletTestObservation(next, "15")
	exact.BuildNotification = func(r WalletObservationResult) (CreateNotificationDetailSnapshotParams, WalletNotificationPayload, error) {
		sp, pay, err := walletTestBuilder(r)
		pay.ChatID = sp.NotificationChatID
		pay.ChatType = sp.NotificationChatType
		pay.Text = sp.NotificationText
		pay.Language = "zh"
		pay.PublicID = WalletNotificationIDPlaceholder
		encoded, _ := json.Marshal(pay)
		pay.ActionURL += strings.Repeat("x", WalletNotificationPayloadLimit-len(encoded))
		return sp, pay, err
	}
	if _, err := db.RecordWalletObservation(ctx, exact); !errors.Is(err, ErrWalletNotificationCapacity) {
		t.Fatalf("JSONB capacity must be typed rollback: %v", err)
	}
}

func TestPostgresWalletFreeQuotaReservedOnceAndExceededObservationAdvances(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "free", "realtime")
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if _, err := db.CreateAlertEvent(ctx, CreateAlertEventParams{WatchRuleID: rule.ID, EventType: rule.RuleType, NotificationStatus: "failed"}); err != nil {
			t.Fatal(err)
		}
	}
	limit := int64(5)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, value := range []string{"12", "13"} {
		wg.Add(1)
		go func(v string) {
			defer wg.Done()
			p := walletTestObservation(rule, v)
			p.DailyAlertLimit = &limit
			_, err := db.RecordWalletObservation(ctx, p)
			errs <- err
		}(value)
	}
	wg.Wait()
	close(errs)
	success, stale := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, ErrWalletObservationStale) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || stale != 1 {
		t.Fatalf("success=%d stale=%d", success, stale)
	}
	next := walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "free")
	p := walletTestObservation(next, "14")
	p.DailyAlertLimit = &limit
	r, err := db.RecordWalletObservation(ctx, p)
	if err != nil || r.Status != "daily_limit" || r.Used != 5 {
		t.Fatalf("limit result=%+v err=%v", r, err)
	}
	after := walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "free")
	used, err := db.CountDailyAlertEvents(ctx, rule.DeBoxUserID, "Asia/Shanghai")
	if err != nil || used != 5 || *after.LastValue != "14" {
		t.Fatalf("used=%d last=%s err=%v", used, *after.LastValue, err)
	}
	var outboxes int
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM wallet_notification_outbox`).Scan(&outboxes)
	if outboxes != 1 {
		t.Fatalf("outboxes=%d", outboxes)
	}
}

func TestPostgresWalletStageCountsRollbackAndOneQueuePerWindow(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "standard", "stage")
	ctx := context.Background()
	p := walletTestObservation(rule, "12")
	p.BuildNotification = func(WalletObservationResult) (CreateNotificationDetailSnapshotParams, WalletNotificationPayload, error) {
		return CreateNotificationDetailSnapshotParams{}, WalletNotificationPayload{}, errors.New("fail after window counter")
	}
	if _, err := db.RecordWalletObservation(ctx, p); err == nil {
		t.Fatal("expected failure")
	}
	var windows int
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM aggregation_windows`).Scan(&windows)
	if windows != 0 {
		t.Fatal("window escaped rollback")
	}
	p.BuildNotification = walletTestBuilder
	r, err := db.RecordWalletObservation(ctx, p)
	if err != nil || r.Stage == nil || r.Stage.TotalTriggerCount != 1 || r.Outbox == nil {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	next := walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "standard")
	r2, err := db.RecordWalletObservation(ctx, walletTestObservation(next, "14"))
	if err != nil || r2.Stage.TotalTriggerCount != 2 || r2.Outbox != nil {
		t.Fatalf("same window=%+v err=%v", r2, err)
	}
	var n, flag int
	_ = pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM wallet_notification_outbox),notification_sent FROM aggregation_windows`).Scan(&n, &flag)
	if n != 1 || flag != 1 {
		t.Fatalf("outboxes=%d flag=%d", n, flag)
	}
}

func TestPostgresWalletOutboxLeaseUnknownResolutionAndSentGuard(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "standard", "realtime")
	ctx := context.Background()
	r, err := db.RecordWalletObservation(ctx, walletTestObservation(rule, "12"))
	if err != nil {
		t.Fatal(err)
	}
	original := string(r.Outbox.Payload)
	claims, err := db.ClaimWalletNotifications(ctx, 0, 200)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims=%+v err=%v", claims, err)
	}
	row := claims[0]
	row, err = db.StartWalletNotificationAttempt(ctx, row.ID, *row.LeaseToken)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := db.ClaimWalletNotifications(ctx, 0, 200); err != nil || len(other) != 0 {
		t.Fatalf("duplicate claim=%+v err=%v", other, err)
	}
	if _, err := db.FinishWalletNotification(ctx, FinishWalletNotificationParams{ID: row.ID, LeaseToken: "stale", Status: "failed"}); !errors.Is(err, ErrWalletObservationStale) {
		t.Fatalf("stale token=%v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE wallet_notification_outbox SET lease_expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, row.ID); err != nil {
		t.Fatal(err)
	}
	if more, err := db.ClaimWalletNotifications(ctx, 0, 200); err != nil || len(more) != 0 {
		t.Fatalf("expired re-sent=%+v err=%v", more, err)
	}
	if _, err := db.ResolveWalletDeliveryUnknown(ctx, row.ID, false, "", ""); err == nil {
		t.Fatal("resolution without evidence accepted")
	}
	resolved, err := db.ResolveWalletDeliveryUnknown(ctx, row.ID, false, "", "operator verified recipient history: not delivered")
	if err != nil || resolved.Attempts != 1 || resolved.Status != "retry" {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	claims, err = db.ClaimWalletNotifications(ctx, 0, 1)
	if err != nil || len(claims) != 1 || claims[0].Attempts != 1 || string(claims[0].Payload) != original {
		t.Fatalf("retry=%+v err=%v", claims, err)
	}
	row = claims[0]
	row, err = db.StartWalletNotificationAttempt(ctx, row.ID, *row.LeaseToken)
	if err != nil || row.Attempts != 2 {
		t.Fatalf("started=%+v err=%v", row, err)
	}
	id := "confirmed-message"
	sent, err := db.FinishWalletNotification(ctx, FinishWalletNotificationParams{ID: row.ID, LeaseToken: *row.LeaseToken, Status: "sent", MessageID: &id})
	if err != nil || sent.Status != "sent" {
		t.Fatal(err)
	}
	if _, err := db.FinishWalletNotification(ctx, FinishWalletNotificationParams{ID: row.ID, LeaseToken: *row.LeaseToken, Status: "failed", Error: "late error"}); !errors.Is(err, ErrWalletObservationStale) {
		t.Fatalf("sent regressed=%v", err)
	}
	var status, body string
	_ = pool.QueryRow(ctx, `SELECT ae.notification_status,ns.notification_text FROM alert_events ae JOIN notification_detail_snapshots ns ON ns.source_id=ae.id AND ns.source_type='alert_event' WHERE ae.id=$1`, row.SourceID).Scan(&status, &body)
	if status != "sent" || body != "frozen original body" {
		t.Fatalf("source=%s body=%s", status, body)
	}
}

func TestPostgresWalletRecoveryClosesOnlyOldCreatedWindowsAndAuditsOldSources(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "standard", "stage")
	ctx := context.Background()
	old, err := db.RecordStageTrigger(ctx, RecordStageTriggerParams{WatchRuleID: rule.ID, DeBoxUserID: rule.DeBoxUserID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE aggregate_notifications SET notification_status='failed',notification_error='legacy response timeout evidence' WHERE aggregation_window_id=$1`, old.WindowID); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC().Add(-30 * time.Second).Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `UPDATE aggregation_windows SET created_at=$2::timestamptz-INTERVAL '1 second' WHERE id=$1`, old.WindowID, cutoff); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ConfigureNotificationRecoveryBoundary(ctx, cutoff, 0, "isolated test explicit activation"); err != nil {
		t.Fatal(err)
	}
	before := cutoff.Add(-time.Second)
	skip, err := db.RecordStageTrigger(ctx, RecordStageTriggerParams{WatchRuleID: rule.ID, DeBoxUserID: rule.DeBoxUserID, OccurredAt: &before, EventKey: "old-source"})
	if err != nil || skip.SkippedReason == "" || skip.TriggerEventID != 0 {
		t.Fatalf("skip=%+v err=%v", skip, err)
	}
	fresh, err := db.RecordStageTrigger(ctx, RecordStageTriggerParams{WatchRuleID: rule.ID, DeBoxUserID: rule.DeBoxUserID})
	if err != nil || fresh.WindowID == old.WindowID || fresh.WindowStartsAt.Before(cutoff) {
		t.Fatalf("fresh=%+v err=%v", fresh, err)
	}
	again, err := db.RecordStageTrigger(ctx, RecordStageTriggerParams{WatchRuleID: rule.ID, DeBoxUserID: rule.DeBoxUserID})
	if err != nil || again.WindowID != fresh.WindowID || again.TotalTriggerCount != 2 {
		t.Fatalf("new window reclosed=%+v err=%v", again, err)
	}
	var oldStatus string
	var oldError string
	var auditAt *time.Time
	_ = pool.QueryRow(ctx, `SELECT an.notification_status,an.notification_error,aw.recovery_skipped_at FROM aggregation_windows aw JOIN aggregate_notifications an ON an.aggregation_window_id=aw.id WHERE aw.id=$1`, old.WindowID).Scan(&oldStatus, &oldError, &auditAt)
	if oldStatus != "unknown" || auditAt == nil || !strings.Contains(oldError, "legacy response timeout evidence") || !strings.Contains(oldError, "recovery boundary") {
		t.Fatalf("old status=%s audit=%v", oldStatus, auditAt)
	}
	var auditCount int
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM wallet_trigger_recovery_decisions`).Scan(&auditCount)
	if auditCount != 1 {
		t.Fatalf("skip audit=%d", auditCount)
	}
}

func TestPostgresWalletBalanceCrossBoundaryAuditsAndAdvancesWithoutQuotaOrTrigger(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "free", "realtime")
	ctx := context.Background()
	cutoff := time.Now().UTC().Add(-30 * time.Second).Truncate(time.Microsecond)
	before := cutoff.Add(-time.Hour)
	if _, err := pool.Exec(ctx, `UPDATE watch_rules SET last_value='4',last_checked_at=$2 WHERE id=$1`, rule.ID, before); err != nil {
		t.Fatal(err)
	}
	rule = walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "free")
	if _, err := db.ConfigureNotificationRecoveryBoundary(ctx, cutoff, 0, "explicit local cutover"); err != nil {
		t.Fatal(err)
	}
	limit := int64(5)
	p := walletTestObservation(rule, "6")
	p.DailyAlertLimit = &limit
	first, err := db.RecordWalletObservation(ctx, p)
	if err != nil || first.Status != "recovery_skipped" || first.Event != nil || first.Outbox != nil {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if _, err := db.RecordWalletObservation(ctx, p); !errors.Is(err, ErrWalletObservationStale) {
		t.Fatalf("repeated observation=%v", err)
	}
	used, err := db.CountDailyAlertEvents(ctx, rule.DeBoxUserID, "Asia/Shanghai")
	if err != nil || used != 0 {
		t.Fatalf("cross period used quota=%d err=%v", used, err)
	}
	var oldValue, newValue, basis string
	var from, through time.Time
	var decisions int
	if err := pool.QueryRow(ctx, `SELECT previous_value,current_value,time_basis,observed_from_at,observed_through_at,(SELECT COUNT(*) FROM wallet_trigger_recovery_decisions) FROM wallet_trigger_recovery_decisions`).Scan(&oldValue, &newValue, &basis, &from, &through, &decisions); err != nil || oldValue != "4" || newValue != "6" || basis != "wallet_observation_interval" || !from.Equal(before) || through.Before(cutoff) || decisions != 1 {
		t.Fatalf("audit %s/%s %s %s..%s n=%d err=%v", oldValue, newValue, basis, from, through, decisions, err)
	}
	next := walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "free")
	p = walletTestObservation(next, "8")
	p.DailyAlertLimit = &limit
	second, err := db.RecordWalletObservation(ctx, p)
	if err != nil || second.Event == nil || second.Outbox == nil || *second.Event.PreviousValue != "6" || *second.Event.CurrentValue != "8" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	used, err = db.CountDailyAlertEvents(ctx, rule.DeBoxUserID, "Asia/Shanghai")
	if err != nil || used != 1 {
		t.Fatalf("normal quota=%d err=%v", used, err)
	}
}

func TestPostgresWalletStageDetailCapacityRollsBackWholeObservation(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "standard", "stage")
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE watch_rules SET trigger_count_threshold=2001 WHERE id=$1`, rule.ID); err != nil {
		t.Fatal(err)
	}
	initial, err := db.RecordStageTrigger(ctx, RecordStageTriggerParams{WatchRuleID: rule.ID, DeBoxUserID: rule.DeBoxUserID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rule_trigger_events(debox_user_id,watch_rule_id,aggregation_window_id,event_type) SELECT $1,$2,$3,'balance_change' FROM generate_series(1,1999)`, rule.DeBoxUserID, rule.ID, initial.WindowID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE aggregation_windows SET total_trigger_count=2000 WHERE id=$1`, initial.WindowID); err != nil {
		t.Fatal(err)
	}
	rule = walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "standard")
	if _, err := db.RecordWalletObservation(ctx, walletTestObservation(rule, "12")); !errors.Is(err, ErrWalletNotificationCapacity) {
		t.Fatalf("capacity error=%v", err)
	}
	after := walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "standard")
	var count, events, outbox int
	if err := pool.QueryRow(ctx, `SELECT total_trigger_count,(SELECT COUNT(*) FROM rule_trigger_events),(SELECT COUNT(*) FROM wallet_notification_outbox) FROM aggregation_windows WHERE id=$1`, initial.WindowID).Scan(&count, &events, &outbox); err != nil || count != 2000 || events != 2000 || outbox != 0 || *after.LastValue != "10" {
		t.Fatalf("count=%d events=%d outbox=%d baseline=%s err=%v", count, events, outbox, *after.LastValue, err)
	}
}

func TestPostgresWalletCombinationRollbackDoesNotDoubleCountMemberOrNotification(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "professional", "realtime")
	ctx := context.Background()
	previous := "10"
	combo, err := db.CreateCombinationRuleWithinQuota(ctx, CreateCombinationRuleParams{DeBoxUserID: rule.DeBoxUserID, CycleType: "follow", CycleMinutes: 15, NotificationChatID: rule.DeBoxUserID, NotificationChatType: "private", NotificationLanguage: "zh", Members: []CreateCombinationMemberParams{{Rule: CreateWatchRuleParams{ChainKey: "bsc", ChainID: 56, WalletAddress: "0x2222222222222222222222222222222222222222", RuleType: "balance_change", Threshold: "0", LastValue: &previous}, RequiredTriggerCount: 1}, {Rule: CreateWatchRuleParams{ChainKey: "bsc", ChainID: 56, WalletAddress: "0x3333333333333333333333333333333333333333", RuleType: "balance_change", Threshold: "0", LastValue: &previous}, RequiredTriggerCount: 1}}}, QuotaPolicy{PlanCode: "professional", WalletLimit: 20, RuleLimit: 20, CombinationRules: true, AllowedRuleTypes: []string{"balance_change"}})
	if err != nil {
		t.Fatal(err)
	}
	firstRule := walletDeliveryRule(t, db, combo.Members[0].WatchRuleID, rule.DeBoxUserID, "professional")
	secondRule := walletDeliveryRule(t, db, combo.Members[1].WatchRuleID, rule.DeBoxUserID, "professional")
	first, err := db.RecordWalletObservation(ctx, walletTestObservation(firstRule, "12"))
	if err != nil || first.Combination == nil || first.Outbox != nil {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	bad := walletTestObservation(secondRule, "14")
	bad.BuildNotification = func(WalletObservationResult) (CreateNotificationDetailSnapshotParams, WalletNotificationPayload, error) {
		return CreateNotificationDetailSnapshotParams{}, WalletNotificationPayload{}, errors.New("injected combo snapshot failure")
	}
	if _, err := db.RecordWalletObservation(ctx, bad); err == nil {
		t.Fatal("expected combination rollback")
	}
	var count, events int
	_ = pool.QueryRow(ctx, `SELECT total_trigger_count,(SELECT COUNT(*) FROM rule_trigger_events) FROM aggregation_windows WHERE id=$1`, first.Combination.WindowID).Scan(&count, &events)
	if count != 1 || events != 1 {
		t.Fatalf("counter escaped rollback=%d/%d", count, events)
	}
	second, err := db.RecordWalletObservation(ctx, walletTestObservation(secondRule, "14"))
	if err != nil || second.Outbox == nil || second.Combination == nil || !second.Combination.NotificationDue {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if _, err := db.RecordWalletObservation(ctx, walletTestObservation(secondRule, "14")); !errors.Is(err, ErrWalletObservationStale) {
		t.Fatalf("same observation recounted=%v", err)
	}
	var outbox int
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM wallet_notification_outbox`).Scan(&outbox)
	if outbox != 1 {
		t.Fatalf("outboxes=%d", outbox)
	}
}

func TestPostgresWalletOutboxPausedExpiredOrChangedDestinationCannotDeliver(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "standard", "realtime")
	ctx := context.Background()
	r, err := db.RecordWalletObservation(ctx, walletTestObservation(rule, "12"))
	if err != nil {
		t.Fatal(err)
	}
	check := func(want bool) {
		t.Helper()
		_, eligible, err := db.WalletNotificationRule(ctx, *r.Outbox)
		if err != nil || eligible != want {
			t.Fatalf("eligible=%t want=%t err=%v", eligible, want, err)
		}
	}
	check(true)
	if _, err := pool.Exec(ctx, `UPDATE subscriptions SET expires_at=NOW()-INTERVAL '1 second'`); err != nil {
		t.Fatal(err)
	}
	check(false)
	if _, err := pool.Exec(ctx, `UPDATE subscriptions SET expires_at=NOW()+INTERVAL '1 day'`); err != nil {
		t.Fatal(err)
	}
	check(true)
	if _, err := pool.Exec(ctx, `UPDATE watch_rules SET notification_chat_id='another-destination' WHERE id=$1`, rule.ID); err != nil {
		t.Fatal(err)
	}
	check(false)
	if _, err := pool.Exec(ctx, `UPDATE watch_rules SET notification_chat_id=$2,run_status='paused' WHERE id=$1`, rule.ID, rule.NotificationChatID); err != nil {
		t.Fatal(err)
	}
	check(false)
	claims, err := db.ClaimWalletNotifications(ctx, r.Outbox.ID, 1)
	if err != nil || len(claims) != 0 {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE watch_rules SET run_status='active' WHERE id=$1`, rule.ID); err != nil {
		t.Fatal(err)
	}
	if more, err := db.ClaimWalletNotifications(ctx, 0, 10); err != nil || len(more) != 0 {
		t.Fatalf("old queue resurrected=%+v err=%v", more, err)
	}
}

func TestPostgresWalletObservationBackoffDoesNotStarveRule201OrMoveBaseline(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "standard", "realtime")
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO watch_rules(debox_user_id,chain_key,chain_id,wallet_address,rule_type,threshold,notification_chat_id,last_value,last_checked_at) SELECT $1,'bsc',56,'wallet-'||n,'balance_change',0,$1,'10',NOW() FROM generate_series(2,201) n`, rule.DeBoxUserID); err != nil {
		t.Fatal(err)
	}
	first, err := db.ListEnabledWatchRules(ctx, 200)
	if err != nil || len(first) != 200 {
		t.Fatalf("first=%d err=%v", len(first), err)
	}
	for _, r := range first {
		if err := db.DeferWalletObservation(ctx, r.ID, 5*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	next, err := db.ListEnabledWatchRules(ctx, 200)
	if err != nil || len(next) != 1 || next[0].ID != 201 {
		t.Fatalf("next=%+v err=%v", next, err)
	}
	after := walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "standard")
	if *after.LastValue != *rule.LastValue || !after.LastCheckedAt.Equal(*rule.LastCheckedAt) {
		t.Fatal("backoff changed detection baseline")
	}
	var seconds int
	if err := pool.QueryRow(ctx, `SELECT ROUND(EXTRACT(EPOCH FROM(next_observation_at-NOW())))::int FROM watch_rules WHERE id=$1`, rule.ID).Scan(&seconds); err != nil || seconds < 59 || seconds > 60 {
		t.Fatalf("delay=%d err=%v", seconds, err)
	}
	p := walletTestObservation(after, "12")
	if _, err := db.RecordWalletObservation(ctx, p); err != nil {
		t.Fatal(err)
	}
	var due *time.Time
	if err := pool.QueryRow(ctx, `SELECT next_observation_at FROM watch_rules WHERE id=$1`, rule.ID).Scan(&due); err != nil || due != nil {
		t.Fatalf("successful observation did not clear scheduling: %v err=%v", due, err)
	}
}

func TestPostgresWalletClaimWithoutTransportCanRecoverWithoutUsingSendBudget(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "standard", "realtime")
	ctx := context.Background()
	r, err := db.RecordWalletObservation(ctx, walletTestObservation(rule, "12"))
	if err != nil {
		t.Fatal(err)
	}
	claims, err := db.ClaimWalletNotifications(ctx, r.Outbox.ID, 1)
	if err != nil || len(claims) != 1 || claims[0].Attempts != 0 {
		t.Fatalf("claims=%+v err=%v", claims, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE wallet_notification_outbox SET lease_expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, r.Outbox.ID); err != nil {
		t.Fatal(err)
	}
	claims, err = db.ClaimWalletNotifications(ctx, r.Outbox.ID, 1)
	if err != nil || len(claims) != 1 || claims[0].Attempts != 0 || claims[0].Status != "sending" {
		t.Fatalf("unstarted recovery=%+v err=%v", claims, err)
	}
}

func TestPostgresWalletRecoveryPreservesInitialThresholdAndApprovalSemantics(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "standard", "realtime")
	ctx := context.Background()
	cutoff := time.Now().UTC().Add(-30 * time.Second).Truncate(time.Microsecond)
	if _, err := db.ConfigureNotificationRecoveryBoundary(ctx, cutoff, 0, "explicit local activation"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE watch_rules SET rule_type='balance_threshold',threshold=10,last_value=NULL,last_checked_at=$2 WHERE id=$1`, rule.ID, cutoff.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	rule = walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "standard")
	initial, err := db.RecordWalletObservation(ctx, walletTestObservation(rule, "4"))
	if err != nil || initial.Event == nil || initial.Outbox == nil || initial.Status == "recovery_skipped" {
		t.Fatalf("initial threshold changed=%+v err=%v", initial, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE watch_rules SET rule_type='approval_change',last_value='0',last_checked_at=$2 WHERE id=$1`, rule.ID, cutoff.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	rule = walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "standard")
	approval, err := db.RecordWalletObservation(ctx, walletTestObservation(rule, "100"))
	if err != nil || approval.Event == nil || approval.Outbox == nil || approval.Status == "recovery_skipped" {
		t.Fatalf("approval implicitly reset=%+v err=%v", approval, err)
	}
}

func TestPostgresWalletRecoverySkippedOldOutboxGetsFreshClosureAudit(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "standard", "realtime")
	ctx := context.Background()
	r, err := db.RecordWalletObservation(ctx, walletTestObservation(rule, "12"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE wallet_notification_outbox SET created_at=NOW()-INTERVAL '90 days' WHERE id=$1`, r.Outbox.ID); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().Add(-time.Hour)
	if _, err := db.ConfigureNotificationRecoveryBoundary(ctx, cutoff, 0, "explicit local activation"); err != nil {
		t.Fatal(err)
	}
	claims, err := db.ClaimWalletNotifications(ctx, 0, 10)
	if err != nil || len(claims) != 0 {
		t.Fatalf("old sent=%+v err=%v", claims, err)
	}
	var status string
	var closed *time.Time
	var oldForCleanup bool
	if err := pool.QueryRow(ctx, `SELECT status,closed_at,COALESCE(sent_at,closed_at,created_at)<NOW()-INTERVAL '35 days' FROM wallet_notification_outbox WHERE id=$1`, r.Outbox.ID).Scan(&status, &closed, &oldForCleanup); err != nil || status != "skipped" || closed == nil || oldForCleanup {
		t.Fatalf("audit prematurely old status=%s closed=%v old=%t err=%v", status, closed, oldForCleanup, err)
	}
}

func TestPostgresWalletLifecycleGenerationPreventsUnsweptBacklogRevivingAfterRenewal(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "standard", "realtime")
	ctx := context.Background()
	r, err := db.RecordWalletObservation(ctx, walletTestObservation(rule, "12"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `WITH added AS (INSERT INTO alert_events(watch_rule_id,event_type,notification_status) SELECT $1,'balance_change','pending' FROM generate_series(1,402) RETURNING id) INSERT INTO wallet_notification_outbox(source_key,source_type,source_id,watch_rule_id,debox_user_id,snapshot_id,payload,source_generation) SELECT 'address_realtime:'||a.id,'alert_event',a.id,o.watch_rule_id,o.debox_user_id,o.snapshot_id,o.payload,o.source_generation FROM added a CROSS JOIN wallet_notification_outbox o WHERE o.id=$2`, rule.ID, r.Outbox.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE wallet_notification_outbox SET status='unknown',attempts=1 WHERE id=$1`, r.Outbox.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE subscriptions SET expires_at=NOW()-INTERVAL '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ApplyExpiredEntitlementFallbacks(ctx); err != nil {
		t.Fatal(err)
	}
	var generation int64
	if err := pool.QueryRow(ctx, `SELECT notification_generation FROM watch_rules WHERE id=$1`, rule.ID).Scan(&generation); err != nil || generation != 1 {
		t.Fatalf("generation=%d err=%v", generation, err)
	}
	if _, err := db.ApplyExpiredEntitlementFallbacks(ctx); err != nil {
		t.Fatal(err)
	}
	var again int64
	_ = pool.QueryRow(ctx, `SELECT notification_generation FROM watch_rules WHERE id=$1`, rule.ID).Scan(&again)
	if again != generation {
		t.Fatalf("repeated fallback incremented generation %d->%d", generation, again)
	}
	if _, err := db.ActivateSubscription(ctx, rule.DeBoxUserID, "standard", 30); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReconcileUserEntitlements(ctx, rule.DeBoxUserID, QuotaPolicy{PlanCode: "standard", WalletLimit: 20, RuleLimit: 20, AllowedRuleTypes: []string{"balance_change"}}); err != nil {
		t.Fatal(err)
	}
	left, err := collectMany[WalletNotificationOutbox](ctx, pool, `SELECT `+walletNotificationOutboxColumns+` FROM wallet_notification_outbox WHERE status='pending' ORDER BY id`)
	if err != nil || len(left) == 0 {
		t.Fatalf("fixture did not leave bounded backlog=%d err=%v", len(left), err)
	}
	for _, row := range left {
		_, eligible, err := db.WalletNotificationRule(ctx, row)
		if err != nil || eligible {
			t.Fatalf("old generation revived id=%d eligible=%t err=%v", row.ID, eligible, err)
		}
	}
	claims, err := db.ClaimWalletNotifications(ctx, 0, 200)
	if err != nil || len(claims) != 0 {
		t.Fatalf("old backlog claimed=%d err=%v", len(claims), err)
	}
	var unknown string
	_ = pool.QueryRow(ctx, `SELECT status FROM wallet_notification_outbox WHERE id=$1`, r.Outbox.ID).Scan(&unknown)
	if unknown != "unknown" {
		t.Fatalf("unknown evidence changed=%s", unknown)
	}
	next := walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "standard")
	fresh, err := db.RecordWalletObservation(ctx, walletTestObservation(next, "14"))
	if err != nil || fresh.Outbox == nil || fresh.Outbox.SourceGeneration != generation {
		t.Fatalf("fresh=%+v err=%v", fresh, err)
	}
	claims, err = db.ClaimWalletNotifications(ctx, fresh.Outbox.ID, 1)
	if err != nil || len(claims) != 1 {
		t.Fatalf("fresh generation blocked=%+v err=%v", claims, err)
	}
}

func TestPostgresWalletFreeSelectionInvalidatesOnlyPreviouslyActiveRule(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "free", "realtime")
	ctx := context.Background()
	r, err := db.RecordWalletObservation(ctx, walletTestObservation(rule, "12"))
	if err != nil {
		t.Fatal(err)
	}
	previous := "20"
	other, err := db.CreateWatchRule(ctx, CreateWatchRuleParams{DeBoxUserID: rule.DeBoxUserID, ChainKey: "bsc", ChainID: 56, WalletAddress: "0x2222222222222222222222222222222222222222", RuleType: "balance_change", Threshold: "0", NotificationChatID: rule.DeBoxUserID, NotificationChatType: "private", LastValue: &previous})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetFreeWatchRule(ctx, rule.DeBoxUserID, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetFreeWatchRule(ctx, rule.DeBoxUserID, other.ID); err != nil {
		t.Fatal(err)
	}
	var generation int64
	_ = pool.QueryRow(ctx, `SELECT notification_generation FROM watch_rules WHERE id=$1`, rule.ID).Scan(&generation)
	if generation != 1 {
		t.Fatalf("old rule generation=%d", generation)
	}
	var selectedGeneration int64
	_ = pool.QueryRow(ctx, `SELECT notification_generation FROM watch_rules WHERE id=$1`, other.ID).Scan(&selectedGeneration)
	if selectedGeneration != 0 {
		t.Fatalf("unchanged current free rule invalidated=%d", selectedGeneration)
	}
	if _, err := db.SetFreeWatchRule(ctx, rule.DeBoxUserID, rule.ID); err != nil {
		t.Fatal(err)
	}
	if rows, err := db.ClaimWalletNotifications(ctx, r.Outbox.ID, 1); err != nil || len(rows) != 0 {
		t.Fatalf("old free queue revived=%+v err=%v", rows, err)
	}
}

func TestPostgresWalletInteractionUsesRealCreationTimeOnlyForBoundaryAndMetadata(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "professional", "realtime")
	ctx := context.Background()
	cutoff := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `UPDATE watch_rules SET rule_type='address_interaction',last_value='previous-transaction' WHERE id=$1`, rule.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ConfigureNotificationRecoveryBoundary(ctx, cutoff, 0, "explicit local activation"); err != nil {
		t.Fatal(err)
	}
	rule = walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "professional")
	p := walletTestObservation(rule, "old-matched-transaction")
	old := cutoff.Add(-time.Second)
	p.SourceOccurredAt = &old
	p.SourceTimeBasis = "nodit_transaction_created_at"
	skipped, err := db.RecordWalletObservation(ctx, p)
	if err != nil || skipped.Status != "recovery_skipped" || skipped.Event != nil || skipped.Outbox != nil {
		t.Fatalf("old interaction=%+v err=%v", skipped, err)
	}
	next := walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "professional")
	if *next.LastValue != "old-matched-transaction" {
		t.Fatal("old cursor not consumed")
	}
	missing := walletTestObservation(next, "new-matched-transaction")
	if _, err := db.RecordWalletObservation(ctx, missing); !errors.Is(err, ErrWalletSourceTimeUnavailable) {
		t.Fatalf("missing time=%v", err)
	}
	future := time.Now().Add(time.Hour)
	missing.SourceOccurredAt = &future
	missing.SourceTimeBasis = "nodit_transaction_created_at"
	if _, err := db.RecordWalletObservation(ctx, missing); !errors.Is(err, ErrWalletSourceTimeUnavailable) {
		t.Fatalf("future time=%v", err)
	}
	after := walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "professional")
	if *after.LastValue != *next.LastValue || !after.LastCheckedAt.Equal(*next.LastCheckedAt) {
		t.Fatal("unknown source time advanced baseline")
	}
	missing.SourceOccurredAt = &cutoff
	accepted, err := db.RecordWalletObservation(ctx, missing)
	if err != nil || accepted.Event == nil || accepted.Outbox == nil || accepted.SourceOccurredAt == nil || !accepted.SourceOccurredAt.Equal(cutoff) {
		t.Fatalf("cutoff equality rejected=%+v err=%v", accepted, err)
	}
	var sourceAt time.Time
	var basis string
	if err := pool.QueryRow(ctx, `SELECT occurred_at,time_basis FROM wallet_trigger_recovery_decisions WHERE source_key='interaction:old-matched-transaction'`).Scan(&sourceAt, &basis); err != nil || !sourceAt.Equal(old) || basis != "nodit_transaction_created_at" {
		t.Fatalf("source audit=%s %s err=%v", sourceAt, basis, err)
	}
}

func TestPostgresWalletInteractionSourceMetadataDoesNotMoveStageDetectionWindow(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "professional", "stage")
	ctx := context.Background()
	cutoff := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Microsecond)
	sourceAt := cutoff.Add(time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE watch_rules SET rule_type='address_interaction',last_value='previous-transaction' WHERE id=$1`, rule.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ConfigureNotificationRecoveryBoundary(ctx, cutoff, 0, "explicit local activation"); err != nil {
		t.Fatal(err)
	}
	rule = walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "professional")
	p := walletTestObservation(rule, "new-matched-transaction")
	p.SourceOccurredAt = &sourceAt
	p.SourceTimeBasis = "nodit_transaction_created_at"
	r, err := db.RecordWalletObservation(ctx, p)
	if err != nil || r.Stage == nil || len(r.Stage.Events) != 1 {
		t.Fatalf("stage=%+v err=%v", r, err)
	}
	event := r.Stage.Events[0]
	if event.SourceOccurredAt == nil || !event.SourceOccurredAt.Equal(sourceAt) || event.SourceTimeBasis != "nodit_transaction_created_at" || !event.OccurredAt.After(sourceAt.Add(8*time.Minute)) || r.Stage.WindowStartsAt.Before(sourceAt) {
		t.Fatalf("source time replaced detection semantics: event=%+v window=%s", event, r.Stage.WindowStartsAt)
	}
}

func TestPostgresWalletObservationCapturedBeforePauseCannotEnterRestoredGeneration(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "standard", "realtime")
	ctx := context.Background()
	oldParams := walletTestObservation(rule, "12")
	if _, err := pool.Exec(ctx, `UPDATE subscriptions SET expires_at=NOW()-INTERVAL '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ApplyExpiredEntitlementFallbacks(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ActivateSubscription(ctx, rule.DeBoxUserID, "standard", 30); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReconcileUserEntitlements(ctx, rule.DeBoxUserID, QuotaPolicy{PlanCode: "standard", WalletLimit: 20, RuleLimit: 20, AllowedRuleTypes: []string{"balance_change"}}); err != nil {
		t.Fatal(err)
	}
	restored := walletDeliveryRule(t, db, rule.ID, rule.DeBoxUserID, "standard")
	if restored.NotificationGeneration != rule.NotificationGeneration+1 || *restored.LastValue != *rule.LastValue || !restored.LastCheckedAt.Equal(*rule.LastCheckedAt) {
		t.Fatalf("fixture baseline or generation=%+v old=%+v", restored, rule)
	}
	if _, err := db.RecordWalletObservation(ctx, oldParams); !errors.Is(err, ErrWalletObservationStale) {
		t.Fatalf("pre-pause RPC entered restored generation=%v", err)
	}
	var count int
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM alert_events`).Scan(&count)
	if count != 0 {
		t.Fatalf("stale RPC created events=%d", count)
	}
	fresh, err := db.RecordWalletObservation(ctx, walletTestObservation(restored, "12"))
	if err != nil || fresh.Outbox == nil || fresh.Outbox.SourceGeneration != restored.NotificationGeneration {
		t.Fatalf("fresh generation blocked=%+v err=%v", fresh, err)
	}
	encoded, _ := json.Marshal(restored)
	if strings.Contains(string(encoded), "notification_generation") {
		t.Fatal("internal generation exposed in user JSON")
	}
}

func TestPostgresWalletUnknownResolutionCannotResetOrExceedFiveAttempts(t *testing.T) {
	db, pool, rule := walletDeliveryFixture(t, "standard", "realtime")
	ctx := context.Background()
	r, err := db.RecordWalletObservation(ctx, walletTestObservation(rule, "12"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE wallet_notification_outbox SET status='unknown',attempts=5,last_error='SDK unknown timeout; original-request-id' WHERE id=$1`, r.Outbox.ID); err != nil {
		t.Fatal(err)
	}
	resolved, err := db.ResolveWalletDeliveryUnknown(ctx, r.Outbox.ID, false, "", "recipient and request trace confirm non-delivery")
	if err != nil || resolved.Status != "failed" || resolved.Attempts != 5 || resolved.ClosedAt == nil {
		t.Fatalf("exhausted resolution=%+v err=%v", resolved, err)
	}
	var previousError string
	if err := pool.QueryRow(ctx, `SELECT previous_error FROM wallet_notification_unknown_resolutions WHERE outbox_id=$1`, r.Outbox.ID).Scan(&previousError); err != nil || previousError != "SDK unknown timeout; original-request-id" {
		t.Fatalf("unknown proof overwritten: %q err=%v", previousError, err)
	}
	if rows, err := db.ClaimWalletNotifications(ctx, 0, 10); err != nil || len(rows) != 0 {
		t.Fatalf("attempt six claimed=%+v err=%v", rows, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE wallet_notification_outbox SET status='retry',next_attempt_at=NOW() WHERE id=$1`, r.Outbox.ID); err != nil {
		t.Fatal(err)
	}
	if rows, err := db.ClaimWalletNotifications(ctx, 0, 10); err != nil || len(rows) != 0 {
		t.Fatalf("hard claim cap bypassed=%+v err=%v", rows, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE wallet_notification_outbox SET status='sending',lease_token='test-cap',lease_expires_at=NOW()+INTERVAL '1 minute',send_started_at=NULL WHERE id=$1`, r.Outbox.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.StartWalletNotificationAttempt(ctx, r.Outbox.ID, "test-cap"); !errors.Is(err, ErrWalletObservationStale) {
		t.Fatalf("hard send cap bypassed=%v", err)
	}
}

func TestPostgresWalletStartRechecksNewCutoffAndCurrentPlanAfterPreflight(t *testing.T) {
	for _, change := range []string{"cutoff", "expiry"} {
		t.Run(change, func(t *testing.T) {
			db, pool, rule := walletDeliveryFixture(t, "standard", "realtime")
			ctx := context.Background()
			r, err := db.RecordWalletObservation(ctx, walletTestObservation(rule, "12"))
			if err != nil {
				t.Fatal(err)
			}
			rows, err := db.ClaimWalletNotifications(ctx, r.Outbox.ID, 1)
			if err != nil || len(rows) != 1 {
				t.Fatalf("claim=%+v err=%v", rows, err)
			}
			row := rows[0]
			if _, eligible, err := db.WalletNotificationRule(ctx, row); err != nil || !eligible {
				t.Fatalf("preflight eligible=%t err=%v", eligible, err)
			}
			if change == "cutoff" {
				if _, err := db.ConfigureNotificationRecoveryBoundary(ctx, time.Now().UTC(), 0, "explicit local cutover after preflight"); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := pool.Exec(ctx, `UPDATE subscriptions SET expires_at=NOW()-INTERVAL '1 second'`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.StartWalletNotificationAttempt(ctx, row.ID, *row.LeaseToken); !errors.Is(err, ErrWalletObservationStale) {
				t.Fatalf("changed %s passed final send gate=%v", change, err)
			}
			if more, err := db.ClaimWalletNotifications(ctx, 0, 10); err != nil || len(more) != 0 {
				t.Fatalf("changed row reclaimed=%+v err=%v", more, err)
			}
			var status string
			var attempts int
			if err := pool.QueryRow(ctx, `SELECT status,attempts FROM wallet_notification_outbox WHERE id=$1`, row.ID).Scan(&status, &attempts); err != nil || status != "skipped" || attempts != 0 {
				t.Fatalf("unattempted result=%s/%d err=%v", status, attempts, err)
			}
		})
	}
}
