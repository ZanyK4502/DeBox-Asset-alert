package store

import (
	"context"
	"fmt"
	"time"
)

// The persistent per-rule generation is advanced by the same statement that
// actually pauses it. This bounded cleanup is only bookkeeping: unswept rows
// cannot become eligible again when the rule is restored.
func cancelInvalidWalletNotifications(ctx context.Context, db DBTX, owner, reason string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := collectMany[WalletNotificationOutbox](ctx, db, `WITH invalid AS (SELECT o.id FROM wallet_notification_outbox o WHERE ($1='' OR o.debox_user_id=$1) AND o.status IN ('pending','retry','sending') AND NOT (`+walletOutboxRuleCurrentSQL("o")+`) ORDER BY o.id LIMIT 200 FOR UPDATE OF o SKIP LOCKED) UPDATE wallet_notification_outbox o SET status=CASE WHEN o.status='sending' AND o.send_started_at IS NOT NULL THEN 'unknown' ELSE 'skipped' END,closed_at=CASE WHEN o.status='sending' AND o.send_started_at IS NOT NULL THEN o.closed_at ELSE COALESCE(o.closed_at,NOW()) END,last_error=CASE WHEN o.last_error='' THEN $2 ELSE o.last_error||'; '||$2 END,lease_token=NULL,lease_expires_at=NULL FROM invalid WHERE o.id=invalid.id RETURNING o.source_type,o.source_id,o.status,o.attempts,o.attempted_at,o.last_error`, owner, truncate(reason, 500))
	if err != nil {
		return 0, fmt.Errorf("cancel invalid wallet deliveries: %w", err)
	}
	for _, row := range rows {
		if err := mirrorWalletNotification(ctx, db, row, row.Status, nil, row.LastError); err != nil {
			return 0, err
		}
	}
	return int64(len(rows)), nil
}

func closePausedWalletWindows(ctx context.Context, db DBTX, owner string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := db.Exec(ctx, `WITH closed_windows AS (UPDATE aggregation_windows aw SET closed_at=NOW(),updated_at=NOW() WHERE aw.debox_user_id=$1 AND aw.closed_at IS NULL AND ((aw.watch_rule_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM watch_rules wr WHERE wr.id=aw.watch_rule_id AND wr.debox_user_id=$1 AND wr.enabled=1 AND wr.run_status='active')) OR (aw.combination_rule_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM combination_rules cr WHERE cr.id=aw.combination_rule_id AND cr.debox_user_id=$1 AND cr.enabled=1 AND cr.run_status='active'))) RETURNING id) UPDATE aggregate_notifications an SET notification_status='unknown',notification_error=CASE WHEN notification_error='' THEN 'wallet source stopped; legacy delivery result is unconfirmed' ELSE notification_error||'; wallet source stopped; legacy delivery result is unconfirmed' END WHERE an.aggregation_window_id IN (SELECT id FROM closed_windows) AND an.notification_status NOT IN ('sent','unknown','skipped') AND NOT EXISTS(SELECT 1 FROM wallet_notification_outbox o WHERE o.source_type='aggregate_notification' AND o.source_id=an.id)`, owner)
	return err
}

func walletOutboxEntitlementCurrentSQL(alias string) string {
	return `EXISTS(SELECT 1 FROM watch_rules quota_rule CROSS JOIN LATERAL(SELECT COALESCE((SELECT sub.plan_code FROM subscriptions sub WHERE sub.debox_user_id=quota_rule.debox_user_id AND sub.status='active' AND(sub.is_permanent=1 OR sub.expires_at>NOW()) ORDER BY CASE sub.plan_code WHEN 'professional' THEN 2 WHEN 'standard' THEN 1 ELSE 0 END DESC,sub.is_permanent DESC,sub.expires_at DESC LIMIT 1),'free') code) current_plan WHERE quota_rule.id=` + alias + `.watch_rule_id AND quota_rule.debox_user_id=` + alias + `.debox_user_id AND(
 (current_plan.code='professional' AND quota_rule.rule_type IN('balance_change','incoming','outgoing','balance_threshold','balance_threshold_high','approval_change','address_interaction')) OR
 (current_plan.code='standard' AND quota_rule.rule_type IN('balance_change','incoming','outgoing','balance_threshold','balance_threshold_high','approval_change') AND quota_rule.rule_scope<>'combination' AND quota_rule.notification_chat_type='private') OR
 (current_plan.code='free' AND quota_rule.rule_type IN('balance_change','incoming','outgoing','balance_threshold','balance_threshold_high') AND quota_rule.rule_scope='standalone' AND quota_rule.delivery_mode='realtime' AND quota_rule.notification_chat_type='private' AND EXISTS(SELECT 1 FROM user_preferences p WHERE p.debox_user_id=quota_rule.debox_user_id AND p.free_watch_rule_id=quota_rule.id) AND(EXISTS(SELECT 1 FROM subscriptions s WHERE s.debox_user_id=quota_rule.debox_user_id AND s.plan_code='free' AND s.status='active' AND(s.is_permanent=1 OR s.expires_at>NOW())) OR NOT EXISTS(SELECT 1 FROM subscriptions history WHERE history.debox_user_id=quota_rule.debox_user_id AND history.plan_code<>'free' AND history.is_permanent=0)))))`
}

func walletOutboxRecoveryCurrentSQL(alias string) string {
	return `NOT EXISTS(SELECT 1 FROM notification_recovery_boundaries boundary WHERE boundary.singleton=TRUE AND(` + alias + `.created_at<boundary.cutoff_at OR EXISTS(SELECT 1 FROM watch_rules source_rule WHERE source_rule.id=` + alias + `.watch_rule_id AND source_rule.rule_type='address_interaction' AND(COALESCE(` + alias + `.payload->>'source_time_basis','')<>'nodit_transaction_created_at' OR CASE WHEN pg_input_is_valid(` + alias + `.payload->>'source_occurred_at','timestamptz') THEN (` + alias + `.payload->>'source_occurred_at')::timestamptz<boundary.cutoff_at OR (` + alias + `.payload->>'source_occurred_at')::timestamptz>NOW() ELSE TRUE END))))`
}

func walletOutboxRuleCurrentSQL(alias string) string {
	base := `EXISTS(SELECT 1 FROM watch_rules delivery_rule WHERE delivery_rule.id=` + alias + `.watch_rule_id AND delivery_rule.debox_user_id=` + alias + `.debox_user_id AND delivery_rule.enabled=1 AND delivery_rule.run_status='active' AND delivery_rule.notification_generation=` + alias + `.source_generation AND delivery_rule.notification_chat_id=` + alias + `.payload->>'chat_id' AND delivery_rule.notification_chat_type=` + alias + `.payload->>'chat_type' AND (` + alias + `.combination_rule_id IS NULL OR EXISTS(SELECT 1 FROM combination_rules delivery_combo JOIN combination_rule_members delivery_member ON delivery_member.combination_rule_id=delivery_combo.id WHERE delivery_combo.id=` + alias + `.combination_rule_id AND delivery_combo.debox_user_id=` + alias + `.debox_user_id AND delivery_combo.enabled=1 AND delivery_combo.run_status='active' AND delivery_member.watch_rule_id=delivery_rule.id AND delivery_combo.notification_chat_id=` + alias + `.payload->>'chat_id' AND delivery_combo.notification_chat_type=` + alias + `.payload->>'chat_type')) AND (delivery_rule.notification_chat_type<>'group' OR EXISTS(SELECT 1 FROM notification_groups delivery_group WHERE delivery_group.debox_user_id=delivery_rule.debox_user_id AND delivery_group.gid=delivery_rule.notification_chat_id AND delivery_group.enabled=1)))`
	return "(" + base + ") AND (" + walletOutboxEntitlementCurrentSQL(alias) + ") AND (" + walletOutboxRecoveryCurrentSQL(alias) + ")"
}
