package store

import (
	"context"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
)

// d is an internal dispatch alias, never request input. The same predicate is
// used by claim and lifecycle invalidation so an expired queue cannot revive
// after the owner later renews or re-enables an unrelated target.
const dailySummaryDispatchEligibleSourceSQL = `EXISTS(SELECT 1 FROM daily_summary_targets t JOIN subscriptions s ON s.id=t.subscription_id
 WHERE t.id=d.target_id AND s.id=d.subscription_id AND t.enabled=1 AND s.daily_summary_enabled=1 AND t.chat_type=d.chat_type AND t.chat_id=d.chat_id
 AND s.status='active' AND (s.is_permanent=1 OR s.expires_at>NOW()) AND s.plan_code IN ('standard','professional')
 AND s.id=(SELECT candidate.id FROM subscriptions candidate WHERE candidate.debox_user_id=s.debox_user_id AND candidate.status='active'
   AND (candidate.is_permanent=1 OR candidate.expires_at>NOW()) ORDER BY CASE candidate.plan_code WHEN 'professional' THEN 2 WHEN 'standard' THEN 1 ELSE 0 END DESC,candidate.is_permanent DESC,candidate.expires_at DESC,candidate.id DESC LIMIT 1)
 AND ((t.chat_type='private' AND t.chat_id=s.debox_user_id) OR (t.chat_type='group' AND s.plan_code='professional'
   AND EXISTS(SELECT 1 FROM notification_groups g WHERE g.debox_user_id=s.debox_user_id AND g.gid=t.chat_id AND g.enabled=1)))
 AND COALESCE(GREATEST(t.last_period_end_at,(SELECT MAX(sent.period_end_at) FROM daily_summary_deliveries sent WHERE sent.subscription_id=t.subscription_id AND sent.chat_type=t.chat_type AND sent.chat_id=t.chat_id))<d.period_end_at,TRUE))`

// Called inside the lifecycle/settings transaction after an actual source
// change, before a later reactivation can make the old queue appear eligible.
// A bounded owner scope is required; oversized legacy data fails atomically
// instead of leaving an uncancelled tail that could revive after renewal.
func cancelInvalidDailySummaryDispatches(ctx context.Context, db DBTX, owner string, now time.Time) (int64, error) {
	if owner == "" || now.IsZero() {
		return 0, ErrDailySummaryDispatchConflict
	}
	rows, err := collectMany[struct {
		ID             int64 `db:"id"`
		SubscriptionID int64 `db:"subscription_id"`
		TargetID       int64 `db:"target_id"`
	}](ctx, db, `SELECT d.id,d.subscription_id,d.target_id FROM daily_summary_dispatches d JOIN subscriptions owner_sub ON owner_sub.id=d.subscription_id
	 WHERE owner_sub.debox_user_id=$1 AND d.status IN ('pending','failed','sending') AND NOT (`+dailySummaryDispatchEligibleSourceSQL+`) ORDER BY d.subscription_id,d.target_id,d.id LIMIT 1001`, owner)
	if err != nil {
		return 0, err
	}
	if len(rows) > 1000 {
		return 0, notificationdelivery.ErrCapacity
	}
	if len(rows) == 0 {
		return 0, nil
	}
	ids, subscriptions, targets := make([]int64, 0, len(rows)), make([]int64, 0, len(rows)), make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
		subscriptions = append(subscriptions, row.SubscriptionID)
		targets = append(targets, row.TargetID)
	}
	// Batch locks preserve subscription -> target -> dispatch order without a
	// separate round trip or payload read for every notification.
	if _, err = db.Exec(ctx, `SELECT id FROM subscriptions WHERE id=ANY($1::bigint[]) ORDER BY id FOR UPDATE`, subscriptions); err != nil {
		return 0, err
	}
	if _, err = db.Exec(ctx, `SELECT id FROM daily_summary_targets WHERE id=ANY($1::bigint[]) ORDER BY subscription_id,id FOR UPDATE`, targets); err != nil {
		return 0, err
	}
	tag, err := db.Exec(ctx, `UPDATE daily_summary_dispatches d SET status=CASE WHEN d.status='sending' THEN 'unknown' ELSE 'cancelled' END,
	 closed_at=CASE WHEN d.status='sending' THEN NULL ELSE $2::timestamptz END,claim_token=NULL,lease_until=NULL,retry_at=NULL,last_error='source_or_entitlement_inactive'
	 WHERE d.id=ANY($1::bigint[]) AND d.status IN ('pending','failed','sending') AND NOT (`+dailySummaryDispatchEligibleSourceSQL+`)`, ids, now.UTC())
	return tag.RowsAffected(), err
}
