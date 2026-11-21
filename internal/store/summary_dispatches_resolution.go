package store

import (
	"context"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
)

// All dispatch mutations use subscription -> original target -> dispatch lock
// order, matching settings/preparation, so finalization cannot deadlock a new
// report while trying to record a known external success.
func lockDailySummaryDispatch(ctx context.Context, db DBTX, id int64) (*DailySummaryDispatch, error) {
	identity, err := collectOptional[struct {
		SubscriptionID int64 `db:"subscription_id"`
		TargetID       int64 `db:"target_id"`
	}](ctx, db, `SELECT subscription_id,target_id FROM daily_summary_dispatches WHERE id=$1`, id)
	if err != nil || identity == nil {
		return nil, err
	}
	if _, err = db.Exec(ctx, `SELECT id FROM subscriptions WHERE id=$1 FOR UPDATE`, identity.SubscriptionID); err != nil {
		return nil, err
	}
	if _, err = db.Exec(ctx, `SELECT id FROM daily_summary_targets WHERE id=$1 AND subscription_id=$2 FOR UPDATE`, identity.TargetID, identity.SubscriptionID); err != nil {
		return nil, err
	}
	return collectOptional[DailySummaryDispatch](ctx, db, `SELECT `+dailySummaryDispatchColumns+` FROM daily_summary_dispatches WHERE id=$1 FOR UPDATE`, id)
}

func completeDailySummaryDispatch(ctx context.Context, db DBTX, d DailySummaryDispatch, messageID string, now time.Time) error {
	if _, err := db.Exec(ctx, `INSERT INTO daily_summary_deliveries(subscription_id,period_end_at,chat_type,chat_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, d.SubscriptionID, d.PeriodEndAt, d.ChatType, d.ChatID); err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `UPDATE daily_summary_targets SET last_sent_date=$1,last_period_end_at=$2,last_sent_at=$3 WHERE id=$4 AND subscription_id=$5 AND chat_type=$6 AND chat_id=$7 AND (last_period_end_at IS NULL OR last_period_end_at<$2)`, d.LocalDate, d.PeriodEndAt, now.UTC(), d.TargetID, d.SubscriptionID, d.ChatType, d.ChatID); err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `UPDATE subscriptions SET daily_summary_last_sent_date=CASE WHEN daily_summary_last_period_end_at IS NULL OR daily_summary_last_period_end_at<=$2 THEN $3 ELSE daily_summary_last_sent_date END,
	 daily_summary_last_period_end_at=GREATEST(daily_summary_last_period_end_at,$2),scheduled_push_last_sent_at=$4 WHERE id=$1`, d.SubscriptionID, d.PeriodEndAt, d.LocalDate, now.UTC()); err != nil {
		return err
	}
	_, err := db.Exec(ctx, `UPDATE daily_summary_dispatches SET status='sent',claim_token=NULL,lease_until=NULL,message_id=$2,sent_at=$3,closed_at=$3,last_error='' WHERE id=$1`, d.ID, messageID, now.UTC())
	return err
}

// This is deliberately Store-only: an operator must supply explicit evidence.
// Confirming delivery never sends another message; confirming non-delivery
// preserves the attempt counter and the exact frozen payload.
func (s *Store) ResolveUnknownDailySummaryDispatch(ctx context.Context, id int64, delivered bool, messageID, note string, now time.Time) error {
	note = strings.TrimSpace(note)
	messageID = strings.TrimSpace(messageID)
	if id < 1 || note == "" || len(note) > 2048 || now.IsZero() || delivered && (messageID == "" || len(messageID) > 1024) || !delivered && messageID != "" {
		return ErrDailySummaryDispatchConflict
	}
	_, err := summaryDispatchTx(ctx, s.db, false, func(ctx context.Context, db DBTX) (struct{}, error) {
		d, err := lockDailySummaryDispatch(ctx, db, id)
		if err != nil {
			return struct{}{}, err
		}
		if d == nil || d.Status != DailySummaryDispatchUnknown {
			return struct{}{}, ErrDailySummaryDispatchConflict
		}
		if _, err := db.Exec(ctx, `INSERT INTO daily_summary_unknown_resolutions(dispatch_id,delivered,message_id,note,attempts,last_claim_token,resolved_at) VALUES($1,$2,NULLIF($3,''),$4,$5,$6,$7)`, id, delivered, messageID, note, d.Attempts, d.LastClaimToken, now.UTC()); err != nil {
			return struct{}{}, err
		}
		resolution := "confirmed_not_sent"
		if delivered {
			if err := completeDailySummaryDispatch(ctx, db, *d, messageID, now); err != nil {
				return struct{}{}, err
			}
			resolution = "confirmed_sent"
		} else {
			var retryAt *time.Time
			if d.Attempts < notificationdelivery.MaxAttempts {
				retryAt = &now
			}
			if _, err := db.Exec(ctx, `UPDATE daily_summary_dispatches SET status='failed',retry_at=$2,last_error='operator_confirmed_not_sent' WHERE id=$1`, id, retryAt); err != nil {
				return struct{}{}, err
			}
		}
		_, err = db.Exec(ctx, `UPDATE daily_summary_dispatches SET resolution=$2,resolution_note=$3,resolved_at=$4 WHERE id=$1`, id, resolution, note, now.UTC())
		return struct{}{}, err
	})
	return err
}
