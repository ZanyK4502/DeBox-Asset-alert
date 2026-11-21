package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestPostgresNotificationCleanupKeepsUnresolvedEvidenceAndNewlyClosedAudit(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	makeSnapshot := func(name string) int64 {
		t.Helper()
		snapshot, err := database.CreateNotificationDetailSnapshot(ctx, CreateNotificationDetailSnapshotParams{SourceKey: "retention:" + name, NotificationKind: NotificationKindAddressRealtime, SourceType: "alert_event", DeBoxUserID: "retention-owner", NotificationChatID: "retention-owner", NotificationChatType: "private", NotificationText: "Original " + name, Details: []byte(`{"preserved":true}`)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE notification_detail_snapshots SET created_at=NOW()-INTERVAL '70 days',expires_at=NOW()-INTERVAL '40 days' WHERE id=$1`, snapshot.ID); err != nil {
			t.Fatal(err)
		}
		return snapshot.ID
	}
	unknown, pending, sent, recentlySkipped, unreferenced := makeSnapshot("unknown"), makeSnapshot("pending"), makeSnapshot("sent"), makeSnapshot("newly-skipped"), makeSnapshot("unreferenced")
	for i, item := range []struct {
		id     int64
		status string
		recent bool
	}{{unknown, "unknown", false}, {pending, "pending", false}, {sent, "sent", false}, {recentlySkipped, "skipped", true}} {
		if _, err := pool.Exec(ctx, `INSERT INTO wallet_notification_outbox(source_key,source_type,source_id,watch_rule_id,debox_user_id,snapshot_id,payload,status,created_at,sent_at,closed_at)
		 VALUES($1,'alert_event',$2,1,'retention-owner',$3,'{}'::jsonb,$4,NOW()-INTERVAL '45 days',
		 CASE WHEN $4='sent' THEN NOW()-INTERVAL '40 days' END,
		 CASE WHEN $5 THEN NOW() WHEN $4='sent' THEN NOW()-INTERVAL '40 days' END)`, fmt.Sprintf("retention-source-%d", i), i+1, item.id, item.status, item.recent); err != nil {
			t.Fatal(err)
		}
	}
	// A new closure is an auditable action even when the original pending row
	// is old. Keep its frozen snapshot until that new 35-day audit period ends.
	if _, err := database.CleanupAggregationHistory(ctx); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id   int64
		want bool
	}{{unknown, true}, {pending, true}, {sent, false}, {recentlySkipped, true}, {unreferenced, false}} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notification_detail_snapshots WHERE id=$1)`, item.id).Scan(&exists); err != nil || exists != item.want {
			t.Fatalf("snapshot %d retained=%t want=%t err=%v", item.id, exists, item.want, err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM wallet_notification_outbox`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("active/newlyclosed rows were purged: %d,%v", count, err)
	}
}

func TestPostgresNotificationCleanupRetainsLastConfirmedDailyCutoff(t *testing.T) {
	database, pool, subscription, target, now := summaryDispatchFixture(t)
	ctx := context.Background()
	latest := now.Add(-40 * 24 * time.Hour)
	if _, err := pool.Exec(ctx, `UPDATE daily_summary_targets SET last_period_end_at=NULL WHERE id=$1`, target.ID); err != nil {
		t.Fatal(err)
	}
	for _, end := range []time.Time{latest.Add(-24 * time.Hour), latest} {
		if _, err := pool.Exec(ctx, `INSERT INTO daily_summary_deliveries(subscription_id,chat_type,chat_id,period_end_at,sent_at) VALUES($1,$2,$3,$4,$4)`, subscription.ID, target.ChatType, target.ChatID, end); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.CleanupAggregationHistory(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM daily_summary_deliveries WHERE subscription_id=$1`, subscription.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("last confirmed ledger was removed: %d,%v", count, err)
	}
	targets, err := database.ListDailySummaryDispatchTargets(ctx, subscription.ID, 0, 100)
	if err != nil || len(targets) != 1 || targets[0].LastPeriodEndAt == nil || !targets[0].LastPeriodEndAt.Equal(latest) {
		t.Fatalf("last confirmed cutoff lost after retention: %+v,%v", targets, err)
	}
}
