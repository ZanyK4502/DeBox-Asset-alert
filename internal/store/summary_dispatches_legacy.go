package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
)

const DailySummaryMaxLegacyCandidates = 64

type dailySummaryLegacyCandidate struct {
	ID          int64   `db:"id"`
	SourceKey   string  `db:"source_key"`
	KeyBounded  bool    `db:"key_bounded"`
	SourceID    *int64  `db:"source_id"`
	OriginID    *int64  `db:"origin_id"`
	FromText    *string `db:"from_text"`
	ThroughText *string `db:"through_text"`
	TargetText  *string `db:"target_text"`
}

type DailySummaryLegacyReview struct {
	SnapshotID           int64      `db:"snapshot_id"`
	SubscriptionID       int64      `db:"subscription_id"`
	DeBoxUserID          string     `db:"debox_user_id"`
	ChatType             string     `db:"chat_type"`
	ChatID               string     `db:"chat_id"`
	SourceSubscriptionID *int64     `db:"source_subscription_id"`
	SourceTargetID       *int64     `db:"source_target_id"`
	PeriodStartAt        *time.Time `db:"period_start_at"`
	PeriodEndAt          *time.Time `db:"period_end_at"`
	ProofValid           bool       `db:"proof_valid"`
	Status               string     `db:"status"`
	MessageID            *string    `db:"message_id"`
	Note                 string     `db:"note"`
	CreatedAt            time.Time  `db:"created_at"`
	ClosedAt             *time.Time `db:"closed_at"`
}

func dailySummaryLegacyTimestampSQL(alias, field string) string {
	value := alias + ".details->>'" + field + "'"
	return "(CASE WHEN octet_length(" + value + ")<=128 THEN " + quoteTimestampSQL(value) + " END)"
}

func dailySummaryLegacySnapshotProofSQL(alias, origin, at string) string {
	from := dailySummaryLegacyTimestampSQL(alias, "period_start")
	through := dailySummaryLegacyTimestampSQL(alias, "period_end")
	return `(` + alias + `.source_id>0 AND ` + origin + `.id IS NOT NULL AND octet_length(` + alias + `.source_key)<=512
	 AND octet_length(` + alias + `.details->'target'->>'id')<=128 AND ` + alias + `.details->'target'->>'id'=` + alias + `.source_id::text
	 AND ` + from + ` IS NOT NULL AND ` + through + ` IS NOT NULL AND ` + from + `<` + through + `
	 AND ` + from + `<>'0001-01-01T00:00:00Z'::timestamptz AND ` + through + `<=` + at + `
	 AND ` + alias + `.source_key=('daily_summary:'||` + origin + `.id::text||':'||` + alias + `.source_id::text||':'||` + alias + `.notification_chat_type||':'||` + alias + `.notification_chat_id||':'||(EXTRACT(EPOCH FROM ` + through + `)*1000000000)::numeric(30,0)::text))`
}

// Keep unresolved pre-outbox evidence even if cleanup runs before the first
// daily cycle. Only positive, same-origin route success can cover a legacy
// period; malformed metadata and snapshot expiry never prove delivery.
// alias is an internal SQL table alias, never request input.
func dailySummaryLegacySnapshotNeedsReviewSQL(alias string) string {
	through := dailySummaryLegacyTimestampSQL(alias, "period_end")
	return `(` + alias + `.notification_kind='daily_summary' AND ` + alias + `.source_type='daily_summary_target'
	 AND NOT EXISTS(SELECT 1 FROM daily_summary_dispatches legacy_dispatch WHERE legacy_dispatch.snapshot_id=` + alias + `.id)
	 AND NOT EXISTS(SELECT 1 FROM daily_summary_legacy_reviews legacy_review WHERE legacy_review.snapshot_id=` + alias + `.id)
	 AND NOT EXISTS(SELECT 1 FROM subscriptions legacy_origin WHERE legacy_origin.debox_user_id=` + alias + `.debox_user_id
	 AND legacy_origin.id::text=split_part(LEFT(` + alias + `.source_key,512),':',2)
	 AND ` + dailySummaryLegacySnapshotProofSQL(alias, "legacy_origin", "NOW()") + ` AND(
	 EXISTS(SELECT 1 FROM daily_summary_deliveries legacy_sent WHERE legacy_sent.subscription_id=legacy_origin.id
	 AND legacy_sent.chat_type=` + alias + `.notification_chat_type AND legacy_sent.chat_id=` + alias + `.notification_chat_id AND legacy_sent.period_end_at>=` + through + `)
	 OR EXISTS(SELECT 1 FROM daily_summary_targets legacy_target WHERE legacy_target.subscription_id=legacy_origin.id
	 AND legacy_target.chat_type=` + alias + `.notification_chat_type AND legacy_target.chat_id=` + alias + `.notification_chat_id AND legacy_target.last_period_end_at>=` + through + `))))`
}

func (s *Store) CheckDailySummaryLegacyReviews(ctx context.Context, subscriptionID, targetID int64, now time.Time) (bool, error) {
	return summaryDispatchTx(ctx, s.db, false, func(ctx context.Context, db DBTX) (bool, error) {
		var owner string
		if err := db.QueryRow(ctx, `SELECT debox_user_id FROM subscriptions WHERE id=$1 FOR UPDATE`, subscriptionID).Scan(&owner); err != nil {
			return false, err
		}
		target, err := collectOptional[DailySummaryTarget](ctx, db, `SELECT `+dailySummaryEffectiveTargetColumns+` FROM daily_summary_targets t WHERE t.id=$1 AND t.subscription_id=$2 FOR UPDATE OF t`, targetID, subscriptionID)
		if err != nil {
			return false, err
		}
		if target == nil {
			return true, nil
		}
		return checkDailySummaryLegacyReviews(ctx, db, subscriptionID, owner, *target, now)
	})
}

func checkDailySummaryLegacyReviews(ctx context.Context, db DBTX, subscriptionID int64, owner string, target DailySummaryTarget, now time.Time) (bool, error) {
	var blocked bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM daily_summary_legacy_reviews WHERE debox_user_id=$1 AND chat_type=$2 AND chat_id=$3 AND status='unknown')`, owner, target.ChatType, target.ChatID).Scan(&blocked); err != nil {
		return false, err
	}
	if blocked {
		return true, nil
	}
	endSQL := dailySummaryLegacyTimestampSQL("n", "period_end")
	proofSQL := dailySummaryLegacySnapshotProofSQL("n", "origin", "$4")
	validBeforeCutoff := `COALESCE(` + proofSQL + ` AND ` + endSQL + `<=$5::timestamptz,FALSE)`
	rows, err := collectMany[dailySummaryLegacyCandidate](ctx, db, `SELECT n.id,LEFT(n.source_key,512) AS source_key,octet_length(n.source_key)<=512 AS key_bounded,n.source_id,origin.id AS origin_id,
	 LEFT(n.details->>'period_start',128) AS from_text,LEFT(n.details->>'period_end',128) AS through_text,LEFT(n.details->'target'->>'id',128) AS target_text
	 FROM notification_detail_snapshots n LEFT JOIN subscriptions origin ON origin.debox_user_id=$1 AND origin.id::text=split_part(LEFT(n.source_key,512),':',2)
	 WHERE n.debox_user_id=$1 AND n.notification_kind='daily_summary' AND n.source_type='daily_summary_target'
	 AND n.notification_chat_type=$2 AND n.notification_chat_id=$3
	 AND NOT EXISTS(SELECT 1 FROM daily_summary_dispatches d WHERE d.snapshot_id=n.id)
	 AND NOT EXISTS(SELECT 1 FROM daily_summary_legacy_reviews reviewed WHERE reviewed.snapshot_id=n.id)
	 AND NOT EXISTS(SELECT 1 FROM daily_summary_deliveries sent WHERE sent.subscription_id=origin.id AND sent.chat_type=$2 AND sent.chat_id=$3 AND sent.period_end_at=`+endSQL+` AND `+proofSQL+`)
	 AND NOT (`+validBeforeCutoff+`)
	 ORDER BY n.created_at DESC,n.id DESC LIMIT 65`, owner, target.ChatType, target.ChatID, now.UTC(), target.LastPeriodEndAt)
	if err != nil {
		return false, err
	}
	// Snapshot TTL only limits detail access; it is not delivery evidence.
	// Every candidate is small metadata; original bodies/details are never read
	// into a 64-row batch or rewritten while auditing a legacy uncertainty.
	if len(rows) > DailySummaryMaxLegacyCandidates {
		return false, fmt.Errorf("%w: daily legacy snapshot candidates exceed %d", notificationdelivery.ErrCapacity, DailySummaryMaxLegacyCandidates)
	}
	for _, row := range rows {
		var from, through *time.Time
		if row.FromText != nil && row.ThroughText != nil {
			f, ferr := time.Parse(time.RFC3339Nano, *row.FromText)
			e, eerr := time.Parse(time.RFC3339Nano, *row.ThroughText)
			if ferr == nil && eerr == nil && !f.IsZero() && !e.IsZero() && f.Before(e) && !e.After(now) {
				from, through = &f, &e
			}
		}
		proof := row.KeyBounded && row.OriginID != nil && row.SourceID != nil && *row.SourceID > 0 && row.TargetText != nil && from != nil && through != nil
		if proof {
			sourceTarget, err := strconv.ParseInt(*row.TargetText, 10, 64)
			proof = err == nil && sourceTarget == *row.SourceID && row.SourceKey == fmt.Sprintf("daily_summary:%d:%d:%s:%s:%d", *row.OriginID, *row.SourceID, target.ChatType, target.ChatID, through.UTC().UnixNano())
		}
		if proof && target.LastPeriodEndAt != nil && !through.After(*target.LastPeriodEndAt) {
			continue
		}
		if _, err := db.Exec(ctx, `INSERT INTO daily_summary_legacy_reviews(snapshot_id,subscription_id,debox_user_id,chat_type,chat_id,source_subscription_id,source_target_id,period_start_at,period_end_at,proof_valid,status)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'unknown') ON CONFLICT(snapshot_id) DO NOTHING`, row.ID, subscriptionID, owner, target.ChatType, target.ChatID, row.OriginID, row.SourceID, from, through, proof); err != nil {
			return false, err
		}
		blocked = true
	}
	return blocked, nil
}

// No HTTP path calls this. Uncertain dates or source identity cannot be turned
// into a successful watermark by guessing; the immutable snapshot is retained.
func (s *Store) ResolveLegacyDailySummaryReview(ctx context.Context, snapshotID int64, delivered bool, messageID, note string, now time.Time) error {
	messageID, note = strings.TrimSpace(messageID), strings.TrimSpace(note)
	if snapshotID < 1 || note == "" || len(note) > 2048 || now.IsZero() || delivered && (messageID == "" || len(messageID) > 1024) || !delivered && messageID != "" {
		return ErrDailySummaryDispatchConflict
	}
	_, err := summaryDispatchTx(ctx, s.db, false, func(ctx context.Context, db DBTX) (struct{}, error) {
		review, err := collectOptional[DailySummaryLegacyReview](ctx, db, `SELECT * FROM daily_summary_legacy_reviews WHERE snapshot_id=$1 FOR UPDATE`, snapshotID)
		if err != nil {
			return struct{}{}, err
		}
		if review == nil || review.Status != "unknown" {
			return struct{}{}, ErrDailySummaryDispatchConflict
		}
		status := "confirmed_not_sent"
		if delivered {
			if !review.ProofValid || review.SourceSubscriptionID == nil || review.SourceTargetID == nil || review.PeriodStartAt == nil || review.PeriodEndAt == nil {
				return struct{}{}, ErrInvalidNotificationSnapshot
			}
			if _, err := db.Exec(ctx, `INSERT INTO daily_summary_deliveries(subscription_id,period_end_at,chat_type,chat_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, *review.SourceSubscriptionID, *review.PeriodEndAt, review.ChatType, review.ChatID); err != nil {
				return struct{}{}, err
			}
			// Keep an unknown historical local-date field unchanged. The exact
			// original cutoff is sufficient for monotonic due checks.
			if _, err := db.Exec(ctx, `UPDATE daily_summary_targets SET last_period_end_at=$2,last_sent_at=$3 WHERE id=$1 AND subscription_id=$4 AND chat_type=$5 AND chat_id=$6 AND (last_period_end_at IS NULL OR last_period_end_at<$2)`, *review.SourceTargetID, *review.PeriodEndAt, now.UTC(), *review.SourceSubscriptionID, review.ChatType, review.ChatID); err != nil {
				return struct{}{}, err
			}
			status = "confirmed_sent"
		}
		_, err = db.Exec(ctx, `UPDATE daily_summary_legacy_reviews SET status=$2,message_id=NULLIF($3,''),note=$4,closed_at=$5 WHERE snapshot_id=$1`, snapshotID, status, messageID, note, now.UTC())
		return struct{}{}, err
	})
	return err
}
