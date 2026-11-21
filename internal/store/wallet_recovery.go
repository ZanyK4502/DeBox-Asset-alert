package store

import (
	"context"
	"fmt"
	"time"
)

func walletSourceTime(value *string) *time.Time {
	if value == nil {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return nil
	}
	return &parsed
}

func recordWalletInteractionRecoveryDecision(ctx context.Context, db DBTX, rule WatchRule, current string, sourceAt *time.Time, basis string) (bool, error) {
	cutoff, err := notificationSourceCutoff(ctx, db)
	if err != nil || cutoff == nil {
		return false, err
	}
	var now time.Time
	if err := db.QueryRow(ctx, `SELECT NOW()`).Scan(&now); err != nil {
		return false, err
	}
	if sourceAt == nil || sourceAt.IsZero() || sourceAt.After(now) || basis != "nodit_transaction_created_at" {
		return false, ErrWalletSourceTimeUnavailable
	}
	if !sourceAt.Before(*cutoff) {
		return false, nil
	}
	_, err = db.Exec(ctx, `INSERT INTO wallet_trigger_recovery_decisions(watch_rule_id,debox_user_id,source_key,occurred_at,cutoff_at,reason,previous_value,current_value,observed_from_at,observed_through_at,time_basis) VALUES($1,$2,$3,$4,$5,'interaction transaction created before notification recovery boundary',$6,$7,$8,$9,$10) ON CONFLICT(watch_rule_id,source_key,cutoff_at) DO NOTHING`, rule.ID, rule.DeBoxUserID, "interaction:"+current, *sourceAt, *cutoff, rule.LastValue, current, rule.LastCheckedAt, now, basis)
	return true, err
}

// Wallet balance polling proves an observation interval, not the occurrence
// time of individual transfers. This decision never labels it as a chain event.
func recordWalletBalanceRecoveryObservation(ctx context.Context, db DBTX, rule WatchRule, current string) (bool, error) {
	switch rule.RuleType {
	case "balance_change", "incoming", "outgoing", "balance_threshold", "balance_threshold_high":
	default:
		return false, nil
	}
	if rule.LastValue == nil || rule.LastCheckedAt == nil {
		return false, nil
	}
	cutoff, err := notificationSourceCutoff(ctx, db)
	if err != nil || cutoff == nil || !rule.LastCheckedAt.Before(*cutoff) {
		return false, err
	}
	var observedAt time.Time
	if err := db.QueryRow(ctx, `SELECT NOW()`).Scan(&observedAt); err != nil {
		return false, err
	}
	if observedAt.Before(*cutoff) {
		return false, nil
	}
	sourceKey := "wallet-observation:" + rule.LastCheckedAt.UTC().Format(time.RFC3339Nano)
	_, err = db.Exec(ctx, `INSERT INTO wallet_trigger_recovery_decisions(watch_rule_id,debox_user_id,source_key,occurred_at,cutoff_at,reason,previous_value,current_value,observed_from_at,observed_through_at,time_basis) VALUES($1,$2,$3,$4,$5,'wallet balance observation interval crosses notification recovery boundary',$6,$7,$8,$4,'wallet_observation_interval') ON CONFLICT(watch_rule_id,source_key,cutoff_at) DO NOTHING`, rule.ID, rule.DeBoxUserID, sourceKey, observedAt, *cutoff, rule.LastValue, current, rule.LastCheckedAt)
	return true, err
}

func walletTriggerRecoverySkip(ctx context.Context, db DBTX, ruleID int64, userID, eventKey string, occurredAt time.Time) (string, error) {
	cutoff, err := notificationSourceCutoff(ctx, db)
	if err != nil || cutoff == nil || !occurredAt.Before(*cutoff) {
		return "", err
	}
	if eventKey == "" {
		eventKey = fmt.Sprintf("detected:%s", occurredAt.UTC().Format(time.RFC3339Nano))
	}
	reason := "source occurred before notification recovery boundary"
	_, err = db.Exec(ctx, `INSERT INTO wallet_trigger_recovery_decisions(watch_rule_id,debox_user_id,source_key,occurred_at,cutoff_at,reason) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(watch_rule_id,source_key,cutoff_at) DO NOTHING`, ruleID, userID, eventKey, occurredAt, *cutoff, reason)
	return reason, err
}

func closeWalletWindowBeforeRecovery(ctx context.Context, db DBTX, window AggregationWindow, cutoff *time.Time, now time.Time) (bool, error) {
	if cutoff == nil || !window.CreatedAt.Before(*cutoff) {
		return false, nil
	}
	reason := "window created before notification recovery boundary"
	if _, err := db.Exec(ctx, `UPDATE aggregation_windows SET closed_at=$2,updated_at=$2,recovery_skipped_at=$3,recovery_reason=$4 WHERE id=$1 AND closed_at IS NULL`, window.ID, now, *cutoff, reason); err != nil {
		return false, err
	}
	if _, err := db.Exec(ctx, `UPDATE wallet_notification_outbox o SET status=CASE WHEN o.status='sending' AND o.send_started_at IS NOT NULL THEN 'unknown' ELSE 'skipped' END,closed_at=CASE WHEN o.status='sending' AND o.send_started_at IS NOT NULL THEN o.closed_at ELSE COALESCE(o.closed_at,NOW()) END,last_error=CASE WHEN o.last_error='' THEN $2 ELSE o.last_error||'; '||$2 END,lease_token=NULL,lease_expires_at=NULL FROM aggregate_notifications an WHERE an.aggregation_window_id=$1 AND o.source_type='aggregate_notification' AND o.source_id=an.id AND o.status NOT IN ('sent','unknown','skipped')`, window.ID, reason); err != nil {
		return false, err
	}
	if _, err := db.Exec(ctx, `UPDATE aggregate_notifications an SET notification_status=CASE WHEN notification_status='sending' OR NOT EXISTS(SELECT 1 FROM wallet_notification_outbox o WHERE o.source_type='aggregate_notification' AND o.source_id=an.id) OR EXISTS(SELECT 1 FROM wallet_notification_outbox o WHERE o.source_type='aggregate_notification' AND o.source_id=an.id AND o.status IN ('sending','unknown') AND o.send_started_at IS NOT NULL) THEN 'unknown' ELSE 'skipped' END,notification_error=CASE WHEN notification_error='' THEN $2 ELSE notification_error||'; '||$2 END WHERE aggregation_window_id=$1 AND notification_status NOT IN ('sent','unknown','skipped')`, window.ID, reason); err != nil {
		return false, err
	}
	return true, nil
}
