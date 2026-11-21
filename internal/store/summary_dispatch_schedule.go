package store

import (
	"context"
	"time"
)

type DailySummaryDispatchRef struct {
	ID             int64 `db:"id"`
	SubscriptionID int64 `db:"subscription_id"`
	TargetID       int64 `db:"target_id"`
}

// Scheduling queries only select references. The existing fenced claim still
// checks eligibility, frozen snapshots, delivery history and legacy reviews.
const dailySummaryRetryScheduleSQL = `d.status IN('pending','failed') AND d.attempts<5
 AND(d.status='pending' OR d.retry_at IS NOT NULL)
 AND (` + dailySummaryDispatchEligibleSourceSQL + `)
 AND NOT EXISTS(SELECT 1 FROM daily_summary_legacy_reviews review JOIN subscriptions owner ON owner.debox_user_id=review.debox_user_id
 WHERE owner.id=d.subscription_id AND review.chat_type=d.chat_type AND review.chat_id=d.chat_id AND review.status='unknown')`

func (s *Store) ListDueDailySummaryDispatches(ctx context.Context, now time.Time, limit int) ([]DailySummaryDispatchRef, error) {
	return summaryDispatchTx(ctx, s.db, true, func(ctx context.Context, db DBTX) ([]DailySummaryDispatchRef, error) {
		return collectMany[DailySummaryDispatchRef](ctx, db, `SELECT d.id,d.subscription_id,d.target_id FROM daily_summary_dispatches d
 WHERE `+dailySummaryRetryScheduleSQL+` AND COALESCE(d.retry_at,d.created_at)<=$1
 ORDER BY COALESCE(d.retry_at,d.created_at),d.id LIMIT $2`, now.UTC(), clamp(limit, 1, 100))
	})
}

func (s *Store) NextDailySummaryDispatchAt(ctx context.Context) (*time.Time, error) {
	return summaryDispatchTx(ctx, s.db, true, func(ctx context.Context, db DBTX) (*time.Time, error) {
		var at *time.Time
		err := db.QueryRow(ctx, `SELECT (SELECT COALESCE(d.retry_at,d.created_at) FROM daily_summary_dispatches d
 WHERE `+dailySummaryRetryScheduleSQL+` ORDER BY COALESCE(d.retry_at,d.created_at),d.id LIMIT 1)`).Scan(&at)
		return at, err
	})
}

func (s *Store) GetDailySummaryRetryTarget(ctx context.Context, subscriptionID, targetID int64) (*DailySummaryTarget, error) {
	return summaryDispatchTx(ctx, s.db, true, func(ctx context.Context, db DBTX) (*DailySummaryTarget, error) {
		return collectOptional[DailySummaryTarget](ctx, db, `SELECT `+dailySummaryEffectiveTargetColumns+`
 FROM daily_summary_targets t WHERE t.subscription_id=$1 AND t.id=$2 AND t.enabled=1`, subscriptionID, targetID)
	})
}
