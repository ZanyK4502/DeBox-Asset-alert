package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	"github.com/jackc/pgx/v5/pgxpool"
)

func marketDeliveryFixture(t *testing.T, stage bool) (*Store, *pgxpool.Pool, MarketRule) {
	t.Helper()
	db, pool := openIntegrationStore(t)
	rule := tradeRuleFixture(t, db)
	if _, err := db.ActivateSubscription(context.Background(), rule.DeBoxUserID, "professional", 30); err != nil {
		t.Fatal(err)
	}
	if stage {
		if _, err := pool.Exec(context.Background(), `UPDATE market_rules SET delivery_mode='stage',cycle_type='follow',cycle_minutes=60,trigger_count_threshold=1 WHERE id=$1`, rule.ID); err != nil {
			t.Fatal(err)
		}
	}
	return db, pool, rule
}
func marketDeliveryFact(t *testing.T, db *Store, key string, at time.Time) MarketEvent {
	t.Helper()
	event, _, err := db.CreateMarketEvent(context.Background(), CreateMarketEventParams{ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken,
		EventType: "buy", EventKey: key, Source: "local_delivery_test", Confirmed: true, OccurredAt: at, TokenAmount: pointerOf("12")})
	if err != nil {
		t.Fatal(err)
	}
	return event
}
func marketDeliveryTrigger(t *testing.T, db *Store, rule MarketRule, event MarketEvent, mode string) MarketRuleEvent {
	t.Helper()
	mre, _, err := db.CreateMarketRuleEventAndEnqueue(context.Background(), CreateMarketRuleEventParams{MarketRuleID: rule.ID, MarketEventID: event.ID, TriggerKey: event.EventKey, NotificationStatus: mode})
	if err != nil {
		t.Fatal(err)
	}
	return mre
}
func claimMarketDelivery(t *testing.T, db *Store, kind string) MarketDeliveryClaim {
	t.Helper()
	claims, err := db.ClaimMarketDeliveries(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, claim := range claims {
		if claim.Kind == kind {
			return claim
		}
	}
	t.Fatalf("missing %s claim: %#v", kind, claims)
	return MarketDeliveryClaim{}
}
func prepareMarketDelivery(t *testing.T, db *Store, claim MarketDeliveryClaim, owner string) MarketDeliveryPayload {
	t.Helper()
	ctx := context.Background()
	delivery, err := db.LoadMarketDelivery(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.CreateNotificationDetailSnapshot(ctx, CreateNotificationDetailSnapshotParams{
		SourceKey: fmt.Sprintf("market_%s:%d", claim.Kind, claim.ID), NotificationKind: "market_" + claim.Kind, SourceType: "market_notification_delivery", SourceID: &claim.ID,
		DeBoxUserID: owner, NotificationChatID: delivery.NotificationChatID, NotificationChatType: delivery.NotificationChatType, NotificationLanguage: "en", NotificationText: "first immutable text", Details: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := db.PrepareMarketDelivery(ctx, claim, MarketDeliveryPayload{SnapshotID: snapshot.ID, Payload: notificationdelivery.Payload{
		ChatID: "caller-must-not-win", ChatType: "group", Text: "caller-must-not-win", ActionText: "View details", ActionURL: "https://frozen.example/detail"}})
	if err != nil {
		t.Fatal(err)
	}
	if payload.Payload.ChatID != delivery.NotificationChatID || payload.Payload.Text != "first immutable text" {
		t.Fatalf("snapshot not authoritative: %#v", payload)
	}
	return payload
}

func TestPostgresMarketEnqueueAtomicRollbackReplayAndOrphanRepair(t *testing.T) {
	db, pool, rule := marketDeliveryFixture(t, true)
	ctx := context.Background()
	event := marketDeliveryFact(t, db, "atomic-stage", time.Now().UTC())
	_, err := pool.Exec(ctx, `CREATE FUNCTION fail_stage_link() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected stage association failure'; END $$;
 CREATE TRIGGER fail_stage_link BEFORE INSERT ON market_stage_window_events FOR EACH ROW EXECUTE FUNCTION fail_stage_link()`)
	if err != nil {
		t.Fatal(err)
	}
	params := CreateMarketRuleEventParams{MarketRuleID: rule.ID, MarketEventID: event.ID, TriggerKey: event.EventKey, NotificationStatus: "staged"}
	if _, _, err = db.CreateMarketRuleEventAndEnqueue(ctx, params); err == nil {
		t.Fatal("association failure did not abort enqueue")
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_rule_events`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial MRE committed: %d/%v", count, err)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER fail_stage_link ON market_stage_window_events`); err != nil {
		t.Fatal(err)
	}
	mre, created, err := db.CreateMarketRuleEventAndEnqueue(ctx, params)
	if err != nil || !created {
		t.Fatal(mre, created, err)
	}
	if duplicate, created, err := db.CreateMarketRuleEventAndEnqueue(ctx, params); err != nil || created || duplicate.ID != mre.ID {
		t.Fatal(duplicate, created, err)
	}
	var triggers, links int64
	if err = pool.QueryRow(ctx, `SELECT trigger_count,(SELECT COUNT(*) FROM market_stage_window_events) FROM market_stage_windows`).Scan(&triggers, &links); err != nil || triggers != 1 || links != 1 {
		t.Fatalf("duplicate counted: %d/%d/%v", triggers, links, err)
	}
	orphan := marketDeliveryFact(t, db, "orphan-stage", time.Now().UTC())
	if _, _, err = db.CreateMarketRuleEvent(ctx, CreateMarketRuleEventParams{MarketRuleID: rule.ID, MarketEventID: orphan.ID, TriggerKey: orphan.EventKey, NotificationStatus: "staged"}); err != nil {
		t.Fatal(err)
	}
	if err = db.ReconcileMarketNotificationSources(ctx); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT trigger_count,(SELECT COUNT(*) FROM market_stage_window_events) FROM market_stage_windows`).Scan(&triggers, &links); err != nil || triggers != 2 || links != 2 {
		t.Fatalf("orphan not repaired: %d/%d/%v", triggers, links, err)
	}
}

func TestPostgresMarketLeaseRetryFrozenPayloadUnknownAndExplicitResolution(t *testing.T) {
	db, pool, rule := marketDeliveryFixture(t, false)
	ctx := context.Background()
	event := marketDeliveryFact(t, db, "retry-frozen", time.Now().UTC())
	mre := marketDeliveryTrigger(t, db, rule, event, "pending")
	first := claimMarketDelivery(t, db, "realtime")
	frozen := prepareMarketDelivery(t, db, first, rule.DeBoxUserID)
	if err := db.BeginMarketDeliverySend(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteMarketDeliveryResult(ctx, first, nil, &notificationdelivery.Failure{Kind: notificationdelivery.Retryable, Code: "rate_limited", RetryAfter: time.Minute}); err != nil {
		t.Fatal(err)
	}
	var attempts int
	var wait bool
	if err := pool.QueryRow(ctx, `SELECT notification_attempts,next_attempt_at>=NOW()+INTERVAL '55 seconds' FROM market_rule_events WHERE id=$1`, mre.ID).Scan(&attempts, &wait); err != nil || attempts != 1 || !wait {
		t.Fatal(attempts, wait, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE market_rule_events SET next_attempt_at=NOW() WHERE id=$1`, mre.ID); err != nil {
		t.Fatal(err)
	}
	second := claimMarketDelivery(t, db, "realtime")
	if second.LeaseToken <= first.LeaseToken {
		t.Fatal("lease did not advance")
	}
	if err := db.CompleteMarketDeliveryResult(ctx, first, pointerOf("late-worker"), nil); !errors.Is(err, ErrMarketDeliveryLeaseLost) {
		t.Fatalf("late finalizer accepted: %v", err)
	}
	if err := db.BeginMarketDeliverySend(ctx, first); !errors.Is(err, ErrMarketDeliveryLeaseLost) {
		t.Fatalf("late sender accepted: %v", err)
	}
	got, err := db.GetMarketDeliveryPayload(ctx, second)
	if err != nil || !reflect.DeepEqual(got, &frozen) {
		t.Fatalf("frozen payload changed: %#v,%v", got, err)
	}
	if err = db.BeginMarketDeliverySend(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err = db.CompleteMarketDeliveryResult(ctx, second, nil, &notificationdelivery.Failure{Kind: notificationdelivery.Unknown, Code: "request_timed_out"}); err != nil {
		t.Fatal(err)
	}
	claims, err := db.ClaimMarketDeliveries(ctx, 200)
	if err != nil || len(claims) != 0 {
		t.Fatalf("unknown was automatically retried: %#v,%v", claims, err)
	}
	if err = db.ResolveMarketDeliveryUnknown(ctx, "realtime", mre.ID, false, "", ""); err == nil {
		t.Fatal("resolution accepted without evidence")
	}
	if err = db.ResolveMarketDeliveryUnknown(ctx, "realtime", mre.ID, false, "", "receiver confirmed message absent"); err != nil {
		t.Fatal(err)
	}
	third := claimMarketDelivery(t, db, "realtime")
	if err = db.BeginMarketDeliverySend(ctx, third); err != nil {
		t.Fatal(err)
	}
	if err = db.CompleteMarketDeliveryResult(ctx, third, pointerOf("confirmed-message"), nil); err != nil {
		t.Fatal(err)
	}
	var status, outcome string
	var rows int
	if err = pool.QueryRow(ctx, `SELECT notification_status,delivery_outcome,notification_attempts,(SELECT COUNT(*) FROM market_rule_events) FROM market_rule_events WHERE id=$1`, mre.ID).Scan(&status, &outcome, &attempts, &rows); err != nil || status != "sent" || outcome != "sent" || attempts != 3 || rows != 1 {
		t.Fatal(status, outcome, attempts, rows, err)
	}
}

func TestPostgresMarketInterruptedSendUnknownVersusKnownUnsent(t *testing.T) {
	db, pool, rule := marketDeliveryFixture(t, false)
	ctx := context.Background()
	event := marketDeliveryFact(t, db, "lease-expiry", time.Now().UTC())
	mre := marketDeliveryTrigger(t, db, rule, event, "pending")
	first := claimMarketDelivery(t, db, "realtime")
	if _, err := pool.Exec(ctx, `UPDATE market_rule_events SET notification_lease_expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, mre.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := db.RecoverStaleMarketDeliveries(ctx, time.Minute); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	second := claimMarketDelivery(t, db, "realtime")
	prepareMarketDelivery(t, db, second, rule.DeBoxUserID)
	if err := db.BeginMarketDeliverySend(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE market_rule_events SET notification_lease_expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, mre.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := db.RecoverStaleMarketDeliveries(ctx, time.Minute); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if err := db.CompleteMarketDeliveryResult(ctx, second, pointerOf("too-late"), nil); !errors.Is(err, ErrMarketDeliveryLeaseLost) {
		t.Fatal(err)
	}
	if err := db.CompleteMarketDeliveryResult(ctx, first, pointerOf("older-worker"), nil); !errors.Is(err, ErrMarketDeliveryLeaseLost) {
		t.Fatal(err)
	}
	var status, outcome string
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT notification_status,delivery_outcome,notification_attempts FROM market_rule_events WHERE id=$1`, mre.ID).Scan(&status, &outcome, &attempts); err != nil || status != "skipped" || outcome != "unknown" || attempts != 1 {
		t.Fatal(status, outcome, attempts, err)
	}
	if err := db.ResolveMarketDeliveryUnknown(ctx, "realtime", mre.ID, true, "verified-message", "receiver supplied exact message id"); err != nil {
		t.Fatal(err)
	}
	legacy := marketDeliveryFact(t, db, "legacy-sending", time.Now().UTC())
	old := marketDeliveryTrigger(t, db, rule, legacy, "pending")
	if _, err := pool.Exec(ctx, `UPDATE market_rule_events SET notification_status='sending',notification_attempted_at=NOW()-INTERVAL '10 minutes' WHERE id=$1`, old.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := db.RecoverStaleMarketDeliveries(ctx, time.Minute); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if err := pool.QueryRow(ctx, `SELECT delivery_outcome FROM market_rule_events WHERE id=$1`, old.ID).Scan(&outcome); err != nil || outcome != "unknown" {
		t.Fatal(outcome, err)
	}
}

func TestPostgresMarketStageFinalizationAndChildrenAreOneTransaction(t *testing.T) {
	db, pool, rule := marketDeliveryFixture(t, true)
	ctx := context.Background()
	event := marketDeliveryFact(t, db, "finalize-atomic", time.Now().UTC())
	mre := marketDeliveryTrigger(t, db, rule, event, "staged")
	claim := claimMarketDelivery(t, db, "stage")
	prepareMarketDelivery(t, db, claim, rule.DeBoxUserID)
	if err := db.BeginMarketDeliverySend(ctx, claim); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `CREATE FUNCTION fail_mre_finalization() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.notification_status='sent' THEN RAISE EXCEPTION 'injected child finalization failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER fail_mre_finalization BEFORE UPDATE ON market_rule_events FOR EACH ROW EXECUTE FUNCTION fail_mre_finalization()`)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CompleteMarketDeliveryResult(ctx, claim, pointerOf("accepted-message"), nil); err == nil {
		t.Fatal("child failure was ignored")
	}
	var parentStatus, childStatus string
	if err = pool.QueryRow(ctx, `SELECT w.notification_status,mre.notification_status FROM market_stage_windows w JOIN market_stage_window_events l ON l.market_stage_window_id=w.id JOIN market_rule_events mre ON mre.id=l.market_rule_event_id WHERE mre.id=$1`, mre.ID).Scan(&parentStatus, &childStatus); err != nil || parentStatus != "sending" || childStatus != "staged" {
		t.Fatal(parentStatus, childStatus, err)
	}
	// After an actual acknowledgement persistence failure, recovery is unknown;
	// it never performs another network attempt to "repair" the database.
	if _, err = pool.Exec(ctx, `UPDATE market_stage_windows SET notification_lease_expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, claim.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.RecoverStaleMarketDeliveries(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER fail_mre_finalization ON market_rule_events`); err != nil {
		t.Fatal(err)
	}
	if err = db.ResolveMarketDeliveryUnknown(ctx, "stage", claim.ID, true, "accepted-message", "receiver confirms original acceptance"); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT notification_status FROM market_rule_events WHERE id=$1`, mre.ID).Scan(&childStatus); err != nil || childStatus != "sent" {
		t.Fatal(childStatus, err)
	}
}

func TestPostgresMarketDeliveryPayloadRejectsOversizeAndUnconfirmedSource(t *testing.T) {
	db, pool, rule := marketDeliveryFixture(t, false)
	ctx := context.Background()
	event := marketDeliveryFact(t, db, "bounded-payload", time.Now().UTC())
	marketDeliveryTrigger(t, db, rule, event, "pending")
	claim := claimMarketDelivery(t, db, "realtime")
	payload := prepareMarketDelivery(t, db, claim, rule.DeBoxUserID)
	if _, err := pool.Exec(ctx, `UPDATE market_events SET confirmed=0 WHERE id=$1`, event.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.BeginMarketDeliverySend(ctx, claim); !errors.Is(err, ErrMarketDeliveryLeaseLost) {
		t.Fatalf("unconfirmed source crossed send boundary: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE market_events SET confirmed=1 WHERE id=$1`, event.ID); err != nil {
		t.Fatal(err)
	}
	// A separately stored oversize detail cannot be copied into the bounded
	// transport payload even though it fits the larger detail budget.
	oversizeEvent := marketDeliveryFact(t, db, "oversize-new-payload", time.Now().UTC())
	oversizeMRE := marketDeliveryTrigger(t, db, rule, oversizeEvent, "pending")
	oversizeClaim := claimMarketDelivery(t, db, "realtime")
	snapshot, err := db.CreateNotificationDetailSnapshot(ctx, CreateNotificationDetailSnapshotParams{SourceKey: fmt.Sprintf("market_realtime:%d", oversizeClaim.ID), NotificationKind: NotificationKindMarketRealtime, SourceType: "market_notification_delivery", SourceID: &oversizeMRE.ID, DeBoxUserID: rule.DeBoxUserID, NotificationChatID: "owner", NotificationChatType: "private", NotificationText: strings.Repeat("x", notificationdelivery.MaxPayloadBytes), Details: []byte(`{}`)})
	if err == nil {
		payload.SnapshotID = snapshot.ID
		if _, err = db.PrepareMarketDelivery(ctx, oversizeClaim, payload); !errors.Is(err, ErrMarketDeliveryCapacity) {
			t.Fatalf("oversize accepted: %v", err)
		}
	} else if !errors.Is(err, notificationdelivery.ErrCapacity) {
		t.Fatal(err)
	}
}

func TestPostgresMarketPreparationFailuresDoNotConsumeSendBudget(t *testing.T) {
	db, pool, rule := marketDeliveryFixture(t, false)
	ctx := context.Background()
	event := marketDeliveryFact(t, db, "preflight-budget", time.Now().UTC())
	mre := marketDeliveryTrigger(t, db, rule, event, "pending")
	for i := 0; i < 6; i++ {
		claim := claimMarketDelivery(t, db, "realtime")
		if err := db.CompleteMarketDeliveryResult(ctx, claim, nil, &notificationdelivery.Failure{Kind: notificationdelivery.Retryable, Code: "market_delivery_preparation_failed", Cause: errors.New("database preflight temporarily unavailable")}); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE market_rule_events SET next_attempt_at=NOW() WHERE id=$1`, mre.ID); err != nil {
			t.Fatal(err)
		}
	}
	var attempts int
	var status string
	if err := pool.QueryRow(ctx, `SELECT notification_attempts,notification_status FROM market_rule_events WHERE id=$1`, mre.ID).Scan(&attempts, &status); err != nil || attempts != 0 || status != "failed" {
		t.Fatalf("unsent work exhausted transport budget: %d/%s/%v", attempts, status, err)
	}
	claim := claimMarketDelivery(t, db, "realtime")
	prepareMarketDelivery(t, db, claim, rule.DeBoxUserID)
	if err := db.BeginMarketDeliverySend(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteMarketDeliveryResult(ctx, claim, nil, &notificationdelivery.Failure{Kind: notificationdelivery.Retryable, Code: "explicit_remote_rate_limit"}); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT notification_attempts FROM market_rule_events WHERE id=$1`, mre.ID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatal(attempts, err)
	}
}

func TestPostgresMarketEvaluationWriteRollsBackLinksStateAndCooldown(t *testing.T) {
	db, pool, rule := marketDeliveryFixture(t, true)
	ctx := context.Background()
	event := marketDeliveryFact(t, db, "evaluation-write-atomic", time.Now().UTC())
	_, err := pool.Exec(ctx, `CREATE FUNCTION fail_evaluation_state() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state ? 'fail' THEN RAISE EXCEPTION 'injected final state write failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER fail_evaluation_state BEFORE UPDATE ON market_rules FOR EACH ROW EXECUTE FUNCTION fail_evaluation_state()`)
	if err != nil {
		t.Fatal(err)
	}
	persist := func(state []byte) error {
		return db.WithMarketRuleEvaluationWrite(ctx, rule.ID, func(writer *Store) error {
			if _, _, err := writer.CreateMarketRuleEventAndEnqueue(ctx, CreateMarketRuleEventParams{MarketRuleID: rule.ID, MarketEventID: event.ID, TriggerKey: event.EventKey, NotificationStatus: "staged"}); err != nil {
				return err
			}
			if err := writer.UpdateMarketRuleTargetState(ctx, rule.ID, MarketRuleTarget{TargetKey: "legacy:1"}, state, true); err != nil {
				return err
			}
			_, err := writer.UpdateMarketRuleState(ctx, rule.ID, state, true)
			return err
		})
	}
	if err = persist([]byte(`{"fail":true}`)); err == nil {
		t.Fatal("final state failure accepted")
	}
	var count int
	var clean bool
	if err = pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM market_rule_events)+(SELECT COUNT(*) FROM market_stage_windows)+(SELECT COUNT(*) FROM market_rule_target_states),state='{}'::jsonb AND last_triggered_at IS NULL FROM market_rules WHERE id=$1`, rule.ID).Scan(&count, &clean); err != nil || count != 0 || !clean {
		t.Fatalf("partial evaluation committed: %d/%t/%v", count, clean, err)
	}
	if err = persist([]byte(`{"cursor":1}`)); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT last_triggered_at IS NOT NULL AND state=$2::jsonb AND EXISTS(SELECT 1 FROM market_rule_target_states WHERE market_rule_id=$1 AND state=$2::jsonb AND last_triggered_at IS NOT NULL) FROM market_rules WHERE id=$1`, rule.ID, `{"cursor":1}`).Scan(&clean); err != nil || !clean {
		t.Fatal(clean, err)
	}
}

func TestPostgresMarketCutoffPreservesSentHistoryAndClipsNewFixedWindow(t *testing.T) {
	db, pool, rule := marketDeliveryFixture(t, true)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE market_rules SET cycle_type='fixed',aggregation_anchor_at=NOW()-INTERVAL '30 minutes' WHERE id=$1`, rule.ID); err != nil {
		t.Fatal(err)
	}
	before, err := db.GetNotificationSourceCutoff(ctx)
	if err != nil || before != nil {
		t.Fatal(before, err)
	}
	old := marketDeliveryFact(t, db, "cutoff-old", time.Now().UTC())
	mre := marketDeliveryTrigger(t, db, rule, old, "staged")
	claim := claimMarketDelivery(t, db, "stage")
	frozen := prepareMarketDelivery(t, db, claim, rule.DeBoxUserID)
	if err = db.BeginMarketDeliverySend(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if err = db.CompleteMarketDeliveryResult(ctx, claim, pointerOf("original-message"), nil); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC().Truncate(time.Microsecond)
	if _, err = db.ConfigureNotificationRecoveryBoundary(ctx, cutoff, 0, "local explicit cutover fixture"); err != nil {
		t.Fatal(err)
	}
	if err = db.ReconcileMarketNotificationSources(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	var count int64
	var preserved bool
	raw, _ := json.Marshal(frozen)
	if err = pool.QueryRow(ctx, `SELECT notification_status,trigger_count,closed_at=$2 AND notification_payload=$3::jsonb FROM market_stage_windows WHERE id=$1`, claim.ID, cutoff, string(raw)).Scan(&status, &count, &preserved); err != nil || status != "sent" || count != 1 || !preserved {
		t.Fatalf("sent history rewritten: %s/%d/%t/%v", status, count, preserved, err)
	}
	late := marketDeliveryFact(t, db, "cutoff-late-old", cutoff.Add(-time.Microsecond))
	skipped := marketDeliveryTrigger(t, db, rule, late, "staged")
	if skipped.NotificationStatus != "skipped" {
		t.Fatal("late old source was admitted")
	}
	fresh := marketDeliveryFact(t, db, "cutoff-exact-is-new", cutoff)
	newMRE := marketDeliveryTrigger(t, db, rule, fresh, "staged")
	var windowID int64
	var starts, ends time.Time
	if err = pool.QueryRow(ctx, `SELECT w.id,w.starts_at,w.ends_at,w.trigger_count FROM market_stage_windows w JOIN market_stage_window_events l ON l.market_stage_window_id=w.id WHERE l.market_rule_event_id=$1`, newMRE.ID).Scan(&windowID, &starts, &ends, &count); err != nil || windowID == claim.ID || !starts.Equal(cutoff) || !ends.After(cutoff) || count != 1 {
		t.Fatalf("new fixed window not clipped: %d/%s/%s/%d/%v", windowID, starts, ends, count, err)
	}
	if err = db.ReconcileMarketNotificationSources(ctx); err != nil {
		t.Fatal(err)
	}
	var stillOpen bool
	if err = pool.QueryRow(ctx, `SELECT closed_at IS NULL FROM market_stage_windows WHERE id=$1`, windowID).Scan(&stillOpen); err != nil || !stillOpen {
		t.Fatalf("new fixed window repeatedly reclosed: %t/%v", stillOpen, err)
	}
	var unchanged bool
	if err = pool.QueryRow(ctx, `SELECT notification_status='sent' AND notification_message_id='original-message' FROM market_rule_events WHERE id=$1`, mre.ID).Scan(&unchanged); err != nil || !unchanged {
		t.Fatal(unchanged, err)
	}
}

func TestPostgresMarketCombinationSkipDecisionsDoNotStarveNewWatchSources(t *testing.T) {
	db, pool, rule := marketDeliveryFixture(t, false)
	ctx := context.Background()
	watch, err := db.CreateWatchRule(ctx, CreateWatchRuleParams{DeBoxUserID: rule.DeBoxUserID, ChainKey: "bsc", ChainID: 56, WalletAddress: tradeFixtureToken, RuleType: "incoming", Threshold: "1", NotificationChatID: "owner", NotificationChatType: "private", NotificationLanguage: "en", RuleScope: "combination"})
	if err != nil {
		t.Fatal(err)
	}
	var combination, member, window int64
	if err = pool.QueryRow(ctx, `INSERT INTO market_combination_rules(debox_user_id,cycle_type,cycle_minutes,notification_chat_id,created_at) VALUES($1,'follow',1,$1,NOW()-INTERVAL '1 day') RETURNING id`, rule.DeBoxUserID).Scan(&combination); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO market_combination_members(market_combination_rule_id,source_type,watch_rule_id)VALUES($1,'watch',$2)RETURNING id`, combination, watch.ID).Scan(&member); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO market_combination_members(market_combination_rule_id,source_type,market_rule_id)VALUES($1,'market',$2)`, combination, rule.ID); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO aggregation_windows(debox_user_id,source_type,watch_rule_id,starts_at,ends_at)VALUES($1,'rule',$2,NOW()-INTERVAL '1 day',NOW()+INTERVAL '1 day')RETURNING id`, rule.DeBoxUserID, watch.ID).Scan(&window); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO rule_trigger_events(debox_user_id,watch_rule_id,aggregation_window_id,event_type,occurred_at)
  SELECT $1,$2,$3,'incoming',NOW()-INTERVAL '2 hours' FROM generate_series(1,2)`, rule.DeBoxUserID, watch.ID, window); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO rule_trigger_events(debox_user_id,watch_rule_id,aggregation_window_id,event_type,occurred_at) VALUES($1,$2,$3,'incoming',NOW())`, rule.DeBoxUserID, watch.ID, window); err != nil {
		t.Fatal(err)
	}
	if n, err := db.ProcessPendingWatchCombinationTriggers(ctx, 2); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	var decisions int
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_combination_source_decisions WHERE market_combination_member_id=$1`, member).Scan(&decisions); err != nil || decisions != 2 {
		t.Fatal(decisions, err)
	}
	if n, err := db.ProcessPendingWatchCombinationTriggers(ctx, 2); err != nil || n != 1 {
		t.Fatalf("old sources starved fresh source: %d/%v", n, err)
	}
	if n, err := db.ProcessPendingWatchCombinationTriggers(ctx, 2); err != nil || n != 0 {
		t.Fatalf("duplicate watch trigger recounted: %d/%v", n, err)
	}
}

func TestPostgresMarketReorgReconciliationRetainsLinksAndFrozenSentCounters(t *testing.T) {
	for _, sent := range []bool{false, true} {
		t.Run(fmt.Sprintf("sent_%t", sent), func(t *testing.T) {
			db, pool, rule := marketDeliveryFixture(t, true)
			ctx := context.Background()
			event := marketDeliveryFact(t, db, "reorg-reconcile", time.Now().UTC())
			mre := marketDeliveryTrigger(t, db, rule, event, "staged")
			var window int64
			if err := pool.QueryRow(ctx, `SELECT market_stage_window_id FROM market_stage_window_events WHERE market_rule_event_id=$1`, mre.ID).Scan(&window); err != nil {
				t.Fatal(err)
			}
			if sent {
				claim := claimMarketDelivery(t, db, "stage")
				prepareMarketDelivery(t, db, claim, rule.DeBoxUserID)
				if err := db.BeginMarketDeliverySend(ctx, claim); err != nil {
					t.Fatal(err)
				}
				if err := db.CompleteMarketDeliveryResult(ctx, claim, pointerOf("original"), nil); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := pool.Exec(ctx, `UPDATE market_events SET reorged=1,confirmed=0 WHERE id=$1`, event.ID); err != nil {
				t.Fatal(err)
			}
			if err := db.ReconcileMarketNotificationSources(ctx); err != nil {
				t.Fatal(err)
			}
			var count, links int64
			var status, xmin string
			if err := pool.QueryRow(ctx, `SELECT trigger_count,notification_status,xmin::text,(SELECT COUNT(*)FROM market_stage_window_events) FROM market_stage_windows WHERE id=$1`, window).Scan(&count, &status, &xmin, &links); err != nil {
				t.Fatal(err)
			}
			wantCount, wantStatus := int64(0), "collecting"
			if sent {
				wantCount, wantStatus = 1, "sent"
			}
			if count != wantCount || status != wantStatus || links != 1 {
				t.Fatalf("reorg result %d/%s links=%d", count, status, links)
			}
			if err := db.ReconcileMarketNotificationSources(ctx); err != nil {
				t.Fatal(err)
			}
			var after string
			if err := pool.QueryRow(ctx, `SELECT xmin::text FROM market_stage_windows WHERE id=$1`, window).Scan(&after); err != nil || after != xmin {
				t.Fatalf("unchanged window rewritten: %s/%s/%v", xmin, after, err)
			}
		})
	}
}

func TestPostgresMarketStageCapacityIsExplicitAndSentWindowDoesNotChange(t *testing.T) {
	db, pool, rule := marketDeliveryFixture(t, true)
	ctx := context.Background()
	event := marketDeliveryFact(t, db, "bounded-stage", time.Now().UTC())
	marketDeliveryTrigger(t, db, rule, event, "staged")
	claim := claimMarketDelivery(t, db, "stage")
	if _, err := pool.Exec(ctx, `UPDATE market_stage_windows SET trigger_count=2001 WHERE id=$1`, claim.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LoadMarketDelivery(ctx, claim); !errors.Is(err, ErrMarketDeliveryCapacity) {
		t.Fatalf("oversize detail silently truncated: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE market_stage_windows SET trigger_count=1 WHERE id=$1`, claim.ID); err != nil {
		t.Fatal(err)
	}
	prepareMarketDelivery(t, db, claim, rule.DeBoxUserID)
	if err := db.BeginMarketDeliverySend(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteMarketDeliveryResult(ctx, claim, pointerOf("sent-once"), nil); err != nil {
		t.Fatal(err)
	}
	late := marketDeliveryFact(t, db, "same-window-after-send", time.Now().UTC())
	skipped := marketDeliveryTrigger(t, db, rule, late, "staged")
	if skipped.NotificationStatus != "skipped" {
		t.Fatal("same emitted window accepted another delivery")
	}
	var count, links int
	if err := pool.QueryRow(ctx, `SELECT trigger_count,(SELECT COUNT(*) FROM market_stage_window_events WHERE market_stage_window_id=$1) FROM market_stage_windows WHERE id=$1`, claim.ID).Scan(&count, &links); err != nil || count != 1 || links != 1 {
		t.Fatalf("sent counter/link history changed: %d/%d/%v", count, links, err)
	}
}

func TestPostgresMarketCombinationAtomicFinalizePreservesBothSources(t *testing.T) {
	db, pool, rule := marketDeliveryFixture(t, false)
	ctx := context.Background()
	params := CreateMarketRuleParams{DeBoxUserID: rule.DeBoxUserID, MarketProjectID: rule.MarketProjectID, RuleType: "market_large_sell", ThresholdValue: "1", ThresholdUnit: "token", RuleScope: "combination"}
	normalizeMarketRuleParams(&params)
	other, err := createMarketRule(ctx, pool, params)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE market_rules SET rule_scope='combination' WHERE id=$1`, rule.ID); err != nil {
		t.Fatal(err)
	}
	var combination int64
	if err = pool.QueryRow(ctx, `INSERT INTO market_combination_rules(debox_user_id,cycle_type,cycle_minutes,notification_chat_id) VALUES($1,'follow',60,$1) RETURNING id`, rule.DeBoxUserID).Scan(&combination); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO market_combination_members(market_combination_rule_id,source_type,market_rule_id) VALUES($1,'market',$2),($1,'market',$3)`, combination, rule.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	first := marketDeliveryFact(t, db, "combo-first", time.Now().UTC())
	mre1 := marketDeliveryTrigger(t, db, rule, first, "combined")
	second := marketDeliveryFact(t, db, "combo-second", time.Now().UTC())
	mre2 := marketDeliveryTrigger(t, db, other, second, "combined")
	claim := claimMarketDelivery(t, db, "combination")
	prepareMarketDelivery(t, db, claim, rule.DeBoxUserID)
	if err = db.BeginMarketDeliverySend(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if err = db.CompleteMarketDeliveryResult(ctx, claim, pointerOf("combo-message"), nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_rule_events WHERE id=ANY($1) AND notification_status='sent' AND notification_message_id='combo-message'`, []int64{mre1.ID, mre2.ID}).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	third := marketDeliveryFact(t, db, "combo-after-send", time.Now().UTC())
	marketDeliveryTrigger(t, db, rule, third, "combined")
	var triggers, links, decisions int
	if err = pool.QueryRow(ctx, `SELECT total_trigger_count,(SELECT COUNT(*)FROM market_combination_trigger_events WHERE market_combination_window_id=$1),(SELECT COUNT(*)FROM market_combination_source_decisions) FROM market_combination_windows WHERE id=$1`, claim.ID).Scan(&triggers, &links, &decisions); err != nil || triggers != 2 || links != 2 || decisions != 1 {
		t.Fatalf("emitted combination changed: %d/%d/%d/%v", triggers, links, decisions, err)
	}
}

func TestPostgresMarketLegacyPendingWithoutAttemptEvidenceIsNeverAutomaticallySent(t *testing.T) {
	db, pool, rule := marketDeliveryFixture(t, false)
	ctx := context.Background()
	event := marketDeliveryFact(t, db, "legacy-pending-unknown", time.Now().UTC())
	mre := marketDeliveryTrigger(t, db, rule, event, "pending")
	if _, err := pool.Exec(ctx, `UPDATE market_rule_events SET delivery_outcome='',notification_attempts=0 WHERE id=$1`, mre.ID); err != nil {
		t.Fatal(err)
	}
	if claims, err := db.ClaimMarketDeliveries(ctx, 200); err != nil || len(claims) != 0 {
		t.Fatalf("legacy zero-attempt pending was sent: %#v/%v", claims, err)
	}
	if n, err := db.RecoverStaleMarketDeliveries(ctx, time.Minute); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	var outcome, status string
	if err := pool.QueryRow(ctx, `SELECT notification_status,delivery_outcome FROM market_rule_events WHERE id=$1`, mre.ID).Scan(&status, &outcome); err != nil || status != "skipped" || outcome != "unknown" {
		t.Fatal(status, outcome, err)
	}
	if err := db.ResolveMarketDeliveryUnknown(ctx, "realtime", mre.ID, false, "", "receiver audit confirmed no previous message"); err != nil {
		t.Fatal(err)
	}
	claim := claimMarketDelivery(t, db, "realtime")
	prepareMarketDelivery(t, db, claim, rule.DeBoxUserID)
	if err := db.BeginMarketDeliverySend(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteMarketDeliveryResult(ctx, claim, pointerOf("eventually-confirmed"), nil); err != nil {
		t.Fatal(err)
	}
	var audits int
	var evidence bool
	if err := pool.QueryRow(ctx, `SELECT COUNT(*),BOOL_AND(NOT delivered AND explanation='receiver audit confirmed no previous message' AND attempts=0 AND message_id IS NULL) FROM market_delivery_unknown_resolutions WHERE kind='realtime' AND source_id=$1`, mre.ID).Scan(&audits, &evidence); err != nil || audits != 1 || !evidence {
		t.Fatalf("successful retry erased resolution evidence: %d/%t/%v", audits, evidence, err)
	}
}

func TestPostgresMarketLifecycleFenceProtectsQueueRemainderAfterImmediateRestore(t *testing.T) {
	db, pool, rule := marketDeliveryFixture(t, false)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `WITH events AS(INSERT INTO market_events(chain_key,chain_id,token_address,event_type,event_key,source,confirmed,occurred_at)
  SELECT 'bsc',56,$1,'buy','lifecycle-'||i,'local_test',1,NOW() FROM generate_series(1,2002)i RETURNING id,event_key)
  INSERT INTO market_rule_events(market_rule_id,market_event_id,trigger_key,notification_status,delivery_outcome,notification_route)
  SELECT $2,id,event_key,'pending','not_attempted',jsonb_build_object('chat_id','trade-owner','chat_type','private') FROM events`, tradeFixtureToken, rule.ID); err != nil {
		t.Fatal(err)
	}
	var validRoutes int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_rule_events d WHERE `+marketDeliveryRouteValidSQL("realtime", "d")).Scan(&validRoutes); err != nil || validRoutes != 2002 {
		t.Fatalf("new unsent fixture route is not valid before lifecycle fence: %d/%v", validRoutes, err)
	}
	_, err := withTxValue(ctx, db.db, func(tx DBTX) (struct{}, error) {
		if err := advanceMarketRuleNotificationFence(ctx, tx, rule.ID); err != nil {
			return struct{}{}, err
		}
		_, err := tx.Exec(ctx, `UPDATE market_rules SET run_status='paused' WHERE id=$1`, rule.ID)
		return struct{}{}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = withTxValue(ctx, db.db, func(tx DBTX) (struct{}, error) {
		if err := advanceMarketRuleNotificationFence(ctx, tx, rule.ID); err != nil {
			return struct{}{}, err
		}
		_, err := tx.Exec(ctx, `UPDATE market_rules SET run_status='active' WHERE id=$1`, rule.ID)
		return struct{}{}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_rule_events WHERE notification_status='pending'`).Scan(&pending); err != nil || pending != 2 {
		t.Fatalf("fixture did not leave bounded remainder: %d/%v", pending, err)
	}
	if claims, err := db.ClaimMarketDeliveries(ctx, 200); err != nil || len(claims) != 0 {
		t.Fatalf("stopped queue resurrected: %#v/%v", claims, err)
	}
	fresh := marketDeliveryFact(t, db, "after-real-restore", time.Now().UTC())
	newMRE := marketDeliveryTrigger(t, db, rule, fresh, "pending")
	claims, err := db.ClaimMarketDeliveries(ctx, 200)
	if err != nil || len(claims) != 1 || claims[0].ID != newMRE.ID {
		t.Fatalf("fresh observation was blocked or old work revived: %#v/%v", claims, err)
	}
	if err := db.ReconcileMarketNotificationSources(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_rule_events WHERE notification_status='pending'`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("bounded cancellation never drained: %d/%v", pending, err)
	}
}

func TestPostgresMarketEvaluationWriteCannotAdvanceStoppedRuleAndBoundsNestedContext(t *testing.T) {
	db, pool, rule := marketDeliveryFixture(t, true)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE market_rules SET run_status='paused' WHERE id=$1`, rule.ID); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := db.WithMarketRuleEvaluationWrite(ctx, rule.ID, func(writer *Store) error {
		called = true
		_, err := writer.UpdateMarketRuleState(ctx, rule.ID, []byte(`{"last_snapshot_id":999}`), true)
		return err
	}); !errors.Is(err, ErrMarketRuleEvaluationInactive) || called {
		t.Fatalf("stopped rule advanced: called=%t/%v", called, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE market_rules SET run_status='active' WHERE id=$1`, rule.ID); err != nil {
		t.Fatal(err)
	}
	event := marketDeliveryFact(t, db, "bounded-evaluation", time.Now().UTC())
	started := time.Now()
	err := db.WithMarketRuleEvaluationWrite(ctx, rule.ID, func(writer *Store) error {
		if _, _, err := writer.CreateMarketRuleEventAndEnqueue(context.Background(), CreateMarketRuleEventParams{MarketRuleID: rule.ID, MarketEventID: event.ID, TriggerKey: event.EventKey, NotificationStatus: "staged"}); err != nil {
			return err
		}
		// Deliberately pass Background: the transaction adapter must still enforce
		// the original total budget, rather than granting a new 2s per sub-call.
		var ignored any
		return writer.db.QueryRow(context.Background(), `SELECT pg_sleep(3)`).Scan(&ignored)
	})
	if !errors.Is(err, ErrMarketDeliveryBudget) || time.Since(started) > 3*time.Second {
		t.Fatalf("nested context escaped bounded transaction: elapsed=%s err=%v", time.Since(started), err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_rule_events`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("budget timeout committed partial work: %d/%v", count, err)
	}
}

func TestPostgresMarketReconfirmationOnlyRestoresNeverPreparedSourceAndOriginalLink(t *testing.T) {
	for _, mode := range []string{"unprepared", "prepared", "sent", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			db, pool, pools := tradeFactsFixture(t)
			ctx := context.Background()
			rule := tradeRuleFixture(t, db)
			if _, err := db.ActivateSubscription(ctx, rule.DeBoxUserID, "professional", 30); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE market_rules SET delivery_mode='stage',cycle_type='follow',cycle_minutes=60,trigger_count_threshold=1,last_triggered_at=NOW()-INTERVAL '10 minutes' WHERE id=$1`, rule.ID); err != nil {
				t.Fatal(err)
			}
			receipt := tradeReceiptFixture(t, pools[:2])
			tradeCanonicalFixture(t, db, receipt)
			if _, err := db.PersistMarketTransaction(ctx, receipt); err != nil {
				t.Fatal(err)
			}
			target := MarketRuleTarget{ChainID: 56, TokenAddress: tradeFixtureToken, TradePoolIDs: pools[:2]}
			projected, err := db.ListMarketTradeEventsForTarget(ctx, target, 0, true, 10)
			if err != nil || len(projected) != 1 {
				t.Fatal(projected, err)
			}
			params := CreateMarketRuleEventParams{MarketRuleID: rule.ID, MarketEventID: projected[0].ID, TriggerKey: projected[0].EventKey, NotificationStatus: "staged", Details: projected[0].Metadata}
			mre, _, err := db.CreateMarketRuleEventAndEnqueue(ctx, params)
			if err != nil {
				t.Fatal(err)
			}
			var window int64
			if err := pool.QueryRow(ctx, `SELECT market_stage_window_id FROM market_stage_window_events WHERE market_rule_event_id=$1`, mre.ID).Scan(&window); err != nil {
				t.Fatal(err)
			}
			if mode != "unprepared" {
				claim := claimMarketDelivery(t, db, "stage")
				prepareMarketDelivery(t, db, claim, rule.DeBoxUserID)
				if mode == "sent" || mode == "unknown" {
					if err := db.BeginMarketDeliverySend(ctx, claim); err != nil {
						t.Fatal(err)
					}
					var failure *notificationdelivery.Failure
					if mode == "unknown" {
						failure = &notificationdelivery.Failure{Kind: notificationdelivery.Unknown, Code: "transport_result_unknown"}
					}
					if err := db.CompleteMarketDeliveryResult(ctx, claim, pointerOf("original-message"), failure); err != nil {
						t.Fatal(err)
					}
				}
			}
			newHash := "0x" + strings.Repeat("c", 64)
			if _, err := db.MarkMarketBlockReorged(ctx, 56, receipt.BlockNumber, newHash); err != nil {
				t.Fatal(err)
			}
			if err := db.ReconcileMarketNotificationSources(ctx); err != nil {
				t.Fatal(err)
			}
			receipt.BlockHash = newHash
			refreshTradeReceiptPayload(t, &receipt)
			tradeCanonicalFixture(t, db, receipt)
			if _, err := db.PersistMarketTransaction(ctx, receipt); err != nil {
				t.Fatal(err)
			}
			after, err := db.ListMarketTradeEventsForTarget(ctx, target, projected[0].TradeReadyID, true, 10)
			if err != nil || len(after) != 1 {
				t.Fatal(after, err)
			}
			params.Details = after[0].Metadata
			restored, created, err := db.CreateMarketRuleEventAndEnqueue(ctx, params)
			if err != nil || created || restored.ID != mre.ID {
				t.Fatal(restored, created, err)
			}
			var count, links, audits int
			var sameCooldown bool
			if err := pool.QueryRow(ctx, `SELECT w.trigger_count,(SELECT COUNT(*) FROM market_stage_window_events WHERE market_rule_event_id=$2),jsonb_array_length(mre.notification_source_audit),mr.last_triggered_at<NOW()-INTERVAL '9 minutes'
   FROM market_stage_windows w JOIN market_rule_events mre ON mre.id=$2 JOIN market_rules mr ON mr.id=mre.market_rule_id WHERE w.id=$1`, window, mre.ID).Scan(&count, &links, &audits, &sameCooldown); err != nil {
				t.Fatal(err)
			}
			if mode == "unprepared" {
				if restored.NotificationStatus != "staged" || count != 1 || links != 1 || audits != 1 || !sameCooldown {
					t.Fatalf("narrow reconfirmation failed: %#v count=%d links=%d audits=%d cooldown=%t", restored, count, links, audits, sameCooldown)
				}
			} else {
				if mode == "sent" && restored.NotificationStatus != "sent" {
					t.Fatal("sent source revived")
				}
				if mode != "sent" && restored.NotificationStatus != "skipped" {
					t.Fatal("prepared/unknown source revived")
				}
				if count != 1 || links != 1 || audits != 0 {
					t.Fatalf("frozen window changed: %d/%d/%d", count, links, audits)
				}
			}
		})
	}
}

func TestPostgresMarketFrozenRouteAndGroupPermissionAreRecheckedBeforeSending(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(fmt.Sprintf("started_%t", started), func(t *testing.T) {
			db, pool, rule := marketDeliveryFixture(t, false)
			ctx := context.Background()
			if _, err := db.CreateNotificationGroup(ctx, rule.DeBoxUserID, "owned-group", "local group"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.CreateNotificationGroup(ctx, "different-owner", "owned-group", "same group other owner"); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE market_rules SET notification_chat_id='owned-group',notification_chat_type='group' WHERE id=$1`, rule.ID); err != nil {
				t.Fatal(err)
			}
			event := marketDeliveryFact(t, db, "group-recheck", time.Now().UTC())
			mre := marketDeliveryTrigger(t, db, rule, event, "pending")
			claim := claimMarketDelivery(t, db, "realtime")
			frozen := prepareMarketDelivery(t, db, claim, rule.DeBoxUserID)
			if started {
				if err := db.BeginMarketDeliverySend(ctx, claim); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := pool.Exec(ctx, `UPDATE notification_groups SET enabled=0 WHERE debox_user_id=$1 AND gid='owned-group'`, rule.DeBoxUserID); err != nil {
				t.Fatal(err)
			}
			if !started {
				if err := db.BeginMarketDeliverySend(ctx, claim); !errors.Is(err, ErrMarketDeliveryLeaseLost) {
					t.Fatalf("disabled owner group accepted: %v", err)
				}
			}
			if err := db.ReconcileMarketNotificationSources(ctx); err != nil {
				t.Fatal(err)
			}
			want := "cancelled"
			if started {
				want = "unknown"
			}
			var outcome string
			var immutable bool
			raw, _ := json.Marshal(frozen)
			if err := pool.QueryRow(ctx, `SELECT delivery_outcome,notification_payload=$2::jsonb FROM market_rule_events WHERE id=$1`, mre.ID, string(raw)).Scan(&outcome, &immutable); err != nil || outcome != want || !immutable {
				t.Fatalf("group cancellation changed evidence: %s/%t/%v", outcome, immutable, err)
			}
			if claims, err := db.ClaimMarketDeliveries(ctx, 200); err != nil || len(claims) != 0 {
				t.Fatalf("revoked group was retried: %#v/%v", claims, err)
			}
		})
	}
}

func TestPostgresMarketRouteChangeBeforePreparationAndPlanDowngradeCancelOldWork(t *testing.T) {
	for _, changed := range []string{"route", "plan"} {
		t.Run(changed, func(t *testing.T) {
			db, pool, rule := marketDeliveryFixture(t, false)
			ctx := context.Background()
			event := marketDeliveryFact(t, db, "route-or-plan", time.Now().UTC())
			mre := marketDeliveryTrigger(t, db, rule, event, "pending")
			if changed == "route" {
				if _, err := db.CreateNotificationGroup(ctx, rule.DeBoxUserID, "new-group", "new route"); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, `UPDATE market_rules SET notification_chat_id='new-group',notification_chat_type='group' WHERE id=$1`, rule.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := pool.Exec(ctx, `UPDATE subscriptions SET plan_code='standard' WHERE debox_user_id=$1`, rule.DeBoxUserID); err != nil {
					t.Fatal(err)
				}
			}
			if claims, err := db.ClaimMarketDeliveries(ctx, 200); err != nil || len(claims) != 0 {
				t.Fatalf("old queue used changed destination/plan: %#v/%v", claims, err)
			}
			if err := db.ReconcileMarketNotificationSources(ctx); err != nil {
				t.Fatal(err)
			}
			var cancelled bool
			if err := pool.QueryRow(ctx, `SELECT notification_status='skipped' AND delivery_outcome='cancelled' AND notification_payload='{}'::jsonb FROM market_rule_events WHERE id=$1`, mre.ID).Scan(&cancelled); err != nil || !cancelled {
				t.Fatal(cancelled, err)
			}
		})
	}
}

func TestPostgresMarketMemberSourceFenceRejectsOldSentInputBeforeWindowCounts(t *testing.T) {
	db, pool, rule := marketDeliveryFixture(t, false)
	ctx := context.Background()
	params := CreateMarketRuleParams{DeBoxUserID: rule.DeBoxUserID, MarketProjectID: rule.MarketProjectID, RuleType: "market_large_sell", ThresholdValue: "2", ThresholdUnit: "token", RuleScope: "combination"}
	normalizeMarketRuleParams(&params)
	other, err := createMarketRule(ctx, pool, params)
	if err != nil {
		t.Fatal(err)
	}
	var combination int64
	if err := pool.QueryRow(ctx, `INSERT INTO market_combination_rules(debox_user_id,cycle_type,cycle_minutes,notification_chat_id) VALUES($1,'follow',15,$1)RETURNING id`, rule.DeBoxUserID).Scan(&combination); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO market_combination_members(market_combination_rule_id,source_type,market_rule_id) VALUES($1,'market',$2),($1,'market',$3)`, combination, rule.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	old := marketDeliveryFact(t, db, "old-sent-before-member-restore", time.Now().UTC())
	oldMRE, _, err := db.CreateMarketRuleEvent(ctx, CreateMarketRuleEventParams{MarketRuleID: rule.ID, MarketEventID: old.ID, TriggerKey: old.EventKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpdateMarketRuleEventNotification(ctx, oldMRE.ID, "sent", pointerOf("historical-confirmed"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := withMarketDeliveryTx(ctx, db.db, func(tx DBTX) (struct{}, error) {
		return struct{}{}, advanceMarketRuleNotificationFence(ctx, tx, rule.ID)
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := db.RecordMarketCombinationTrigger(ctx, RecordMarketCombinationTriggerParams{SourceType: "market", MarketRuleEventID: &oldMRE.ID}); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	var windows, decisions int
	if err := pool.QueryRow(ctx, `SELECT(SELECT COUNT(*)FROM market_combination_windows),(SELECT COUNT(*)FROM market_combination_source_decisions)`).Scan(&windows, &decisions); err != nil || windows != 0 || decisions != 1 {
		t.Fatalf("old member source polluted a window: %d/%d/%v", windows, decisions, err)
	}
	fresh := marketDeliveryFact(t, db, "fresh-restored-member", time.Now().UTC())
	marketDeliveryTrigger(t, db, rule, fresh, "pending")
	second := marketDeliveryFact(t, db, "other-member-current", time.Now().UTC())
	marketDeliveryTrigger(t, db, other, second, "combined")
	var triggers, links int
	if err := pool.QueryRow(ctx, `SELECT total_trigger_count,(SELECT COUNT(*)FROM market_combination_trigger_events) FROM market_combination_windows`).Scan(&triggers, &links); err != nil || triggers != 2 || links != 2 {
		t.Fatalf("valid member window blocked after old source: %d/%d/%v", triggers, links, err)
	}
}
