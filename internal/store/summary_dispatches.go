package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
)

const (
	DailySummaryDispatchPending    = "pending"
	DailySummaryDispatchSending    = "sending"
	DailySummaryDispatchSent       = "sent"
	DailySummaryDispatchFailed     = "failed"
	DailySummaryDispatchUnknown    = "unknown"
	DailySummaryDispatchCancelled  = "cancelled"
	DailySummaryDispatchSuperseded = "superseded"
	DailySummaryReadTimeout        = 2 * time.Second
	DailySummaryMaxPayloadBytes    = notificationdelivery.MaxPayloadBytes
	DailySummaryMaxDetailsBytes    = notificationdelivery.MaxSnapshotBytes
	DailySummaryClaimLease         = 90 * time.Second
)

var (
	ErrDailySummaryDispatchConflict    = errors.New("daily summary dispatch claim or successful cutoff changed")
	ErrDailySummaryDispatchUnavailable = errors.New("daily summary dispatch is unavailable")
)

const dailySummaryDispatchColumns = `id,subscription_id,target_id,chat_type,chat_id,period_start_at,period_end_at,
 local_date,snapshot_id,payload,status,claim_token,last_claim_token,lease_until,attempts,retry_at,message_id,last_error,resolution,resolution_note,resolved_at,created_at,attempted_at,sent_at,closed_at`

// Read a cleared legacy watermark from this target's confirmed ledger without
// rewriting stored fields or borrowing another target's subscription mirror.
const dailySummaryEffectiveTargetColumns = `t.id,t.subscription_id,t.chat_type,t.chat_id,t.enabled,t.push_time,t.timezone,t.label,t.language,t.last_sent_date,
 GREATEST(t.last_period_end_at,(SELECT MAX(sent.period_end_at) FROM daily_summary_deliveries sent WHERE sent.subscription_id=t.subscription_id AND sent.chat_type=t.chat_type AND sent.chat_id=t.chat_id)) AS last_period_end_at,
 t.last_sent_at,t.created_at`

type DailySummaryDispatch struct {
	ID             int64                        `db:"id"`
	SubscriptionID int64                        `db:"subscription_id"`
	TargetID       int64                        `db:"target_id"`
	ChatType       string                       `db:"chat_type"`
	ChatID         string                       `db:"chat_id"`
	PeriodStartAt  time.Time                    `db:"period_start_at"`
	PeriodEndAt    time.Time                    `db:"period_end_at"`
	LocalDate      string                       `db:"local_date"`
	SnapshotID     *int64                       `db:"snapshot_id"`
	Payload        notificationdelivery.Payload `db:"payload"`
	Status         string                       `db:"status"`
	ClaimToken     *string                      `db:"claim_token"`
	LastClaimToken *string                      `db:"last_claim_token"`
	LeaseUntil     *time.Time                   `db:"lease_until"`
	Attempts       int32                        `db:"attempts"`
	RetryAt        *time.Time                   `db:"retry_at"`
	MessageID      *string                      `db:"message_id"`
	LastError      string                       `db:"last_error"`
	Resolution     string                       `db:"resolution"`
	ResolutionNote string                       `db:"resolution_note"`
	ResolvedAt     *time.Time                   `db:"resolved_at"`
	CreatedAt      time.Time                    `db:"created_at"`
	AttemptedAt    *time.Time                   `db:"attempted_at"`
	SentAt         *time.Time                   `db:"sent_at"`
	ClosedAt       *time.Time                   `db:"closed_at"`
}

type PrepareDailySummaryDispatchParams struct {
	SubscriptionID int64
	TargetID       int64
	PeriodStartAt  time.Time
	PeriodEndAt    time.Time
	LocalDate      string
	NotBefore      *time.Time
	Snapshot       CreateNotificationDetailSnapshotParams
	// BuildPayload performs only pure URL/label formatting after the immutable
	// snapshot has been obtained. No external work runs inside this transaction.
	BuildPayload func(NotificationDetailSnapshot) (notificationdelivery.Payload, error)
}

type DailySummaryDispatchClaim struct {
	Dispatch DailySummaryDispatch
	Snapshot NotificationDetailSnapshot
}

// All summary database work has a request deadline and a server-side statement
// budget. Cleanup can outlive cancellation for at most one second.
func summaryDispatchTx[T any](ctx context.Context, db DBTX, readOnly bool, fn func(context.Context, DBTX) (T, error)) (result T, err error) {
	readCtx, cancel := context.WithTimeout(ctx, DailySummaryReadTimeout)
	defer cancel()
	if err = readCtx.Err(); err != nil {
		return result, err
	}
	tx, err := db.Begin(readCtx)
	if err != nil {
		return result, err
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer done()
		_ = tx.Rollback(cleanup)
	}()
	if readOnly {
		if _, err = tx.Exec(readCtx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY"); err != nil {
			return result, err
		}
	}
	if _, err = tx.Exec(readCtx, "SET LOCAL statement_timeout = '2s'"); err != nil {
		return result, err
	}
	result, err = fn(readCtx, tx)
	if err != nil {
		return result, err
	}
	if err = tx.Commit(readCtx); err != nil {
		var zero T
		return zero, err
	}
	return result, nil
}

func (s *Store) GetOpenDailySummaryDispatch(ctx context.Context, subscriptionID int64, chatType, chatID string) (*DailySummaryDispatch, error) {
	return summaryDispatchTx(ctx, s.db, true, func(ctx context.Context, db DBTX) (*DailySummaryDispatch, error) {
		return collectOptional[DailySummaryDispatch](ctx, db, `SELECT `+dailySummaryDispatchColumns+` FROM daily_summary_dispatches
		 WHERE subscription_id=$1 AND chat_type=$2 AND chat_id=$3 AND status IN ('pending','sending','failed','unknown') ORDER BY id LIMIT 1`, subscriptionID, chatType, chatID)
	})
}

func (s *Store) ListDailySummaryDispatchTargets(ctx context.Context, subscriptionID, afterID int64, limit int) ([]DailySummaryTarget, error) {
	return summaryDispatchTx(ctx, s.db, true, func(ctx context.Context, db DBTX) ([]DailySummaryTarget, error) {
		return collectMany[DailySummaryTarget](ctx, db, `SELECT `+dailySummaryEffectiveTargetColumns+` FROM daily_summary_targets t WHERE t.subscription_id=$1 AND t.id>$2 AND t.enabled=1 ORDER BY t.id LIMIT $3`, subscriptionID, max(afterID, 0), clamp(limit, 1, 1000))
	})
}

func (s *Store) PrepareDailySummaryDispatch(ctx context.Context, params PrepareDailySummaryDispatchParams) (*DailySummaryDispatch, error) {
	if params.SubscriptionID < 1 || params.TargetID < 1 || params.PeriodStartAt.IsZero() || !params.PeriodStartAt.Before(params.PeriodEndAt) || params.LocalDate == "" || params.BuildPayload == nil || len(params.Snapshot.NotificationText) > DailySummaryMaxPayloadBytes || len(params.Snapshot.Details) > DailySummaryMaxDetailsBytes {
		return nil, ErrInvalidNotificationSnapshot
	}
	return summaryDispatchTx(ctx, s.db, false, func(ctx context.Context, db DBTX) (*DailySummaryDispatch, error) {
		var subscriptionOwner string
		if err := db.QueryRow(ctx, `SELECT debox_user_id FROM subscriptions WHERE id=$1 FOR UPDATE`, params.SubscriptionID).Scan(&subscriptionOwner); err != nil {
			return nil, err
		}
		target, err := collectOptional[DailySummaryTarget](ctx, db, `SELECT `+dailySummaryEffectiveTargetColumns+` FROM daily_summary_targets t WHERE t.id=$1 AND t.subscription_id=$2 FOR UPDATE OF t`, params.TargetID, params.SubscriptionID)
		if err != nil {
			return nil, err
		}
		if target == nil || target.Enabled != 1 || params.Snapshot.DeBoxUserID != subscriptionOwner || params.Snapshot.NotificationChatType != target.ChatType || params.Snapshot.NotificationChatID != target.ChatID {
			return nil, ErrDailySummaryDispatchUnavailable
		}
		if target.LastPeriodEndAt != nil {
			if !target.LastPeriodEndAt.Before(params.PeriodEndAt) {
				return nil, nil
			}
			if !target.LastPeriodEndAt.Equal(params.PeriodStartAt) {
				return nil, ErrDailySummaryDispatchConflict
			}
		}
		var sent bool
		if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM daily_summary_deliveries WHERE subscription_id=$1 AND chat_type=$2 AND chat_id=$3 AND period_end_at=$4)`, params.SubscriptionID, target.ChatType, target.ChatID, params.PeriodEndAt.UTC()).Scan(&sent); err != nil {
			return nil, err
		}
		if sent {
			return nil, nil
		}
		if blocked, err := checkDailySummaryLegacyReviews(ctx, db, params.SubscriptionID, subscriptionOwner, *target, time.Now().UTC()); err != nil || blocked {
			return nil, err
		}
		open, err := collectOptional[DailySummaryDispatch](ctx, db, `SELECT `+dailySummaryDispatchColumns+` FROM daily_summary_dispatches WHERE subscription_id=$1 AND chat_type=$2 AND chat_id=$3 AND status IN ('pending','sending','failed','unknown') ORDER BY id LIMIT 1 FOR UPDATE`, params.SubscriptionID, target.ChatType, target.ChatID)
		if err != nil || open != nil {
			return open, err
		}
		existing, err := collectOptional[DailySummaryDispatch](ctx, db, `SELECT `+dailySummaryDispatchColumns+` FROM daily_summary_dispatches WHERE subscription_id=$1 AND chat_type=$2 AND chat_id=$3 AND period_end_at=$4`, params.SubscriptionID, target.ChatType, target.ChatID, params.PeriodEndAt.UTC())
		if err != nil || existing != nil {
			return existing, err
		}
		snapshot, err := newWithDB(db).CreateNotificationDetailSnapshot(ctx, params.Snapshot)
		if err != nil {
			return nil, err
		}
		if snapshot.NotificationChatType != target.ChatType || snapshot.NotificationChatID != target.ChatID || snapshot.DeBoxUserID != subscriptionOwner || len(snapshot.NotificationText) > DailySummaryMaxPayloadBytes || len(snapshot.Details) > DailySummaryMaxDetailsBytes {
			return nil, ErrInvalidNotificationSnapshot
		}
		var periodEvidence struct {
			From    time.Time `json:"period_start"`
			Through time.Time `json:"period_end"`
		}
		if json.Unmarshal(snapshot.Details, &periodEvidence) != nil || !periodEvidence.From.Equal(params.PeriodStartAt) || !periodEvidence.Through.Equal(params.PeriodEndAt) {
			return nil, ErrInvalidNotificationSnapshot
		}
		payload, err := params.BuildPayload(snapshot)
		if err != nil {
			return nil, err
		}
		if err = payload.Validate(); err != nil {
			return nil, err
		}
		if payload.ChatID != snapshot.NotificationChatID || payload.ChatType != snapshot.NotificationChatType || payload.Text != snapshot.NotificationText {
			return nil, ErrInvalidNotificationSnapshot
		}
		return collectOptional[DailySummaryDispatch](ctx, db, `INSERT INTO daily_summary_dispatches(subscription_id,target_id,chat_type,chat_id,period_start_at,period_end_at,local_date,snapshot_id,payload,status,retry_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending',$10) RETURNING `+dailySummaryDispatchColumns, params.SubscriptionID, target.ID, target.ChatType, target.ChatID, params.PeriodStartAt.UTC(), params.PeriodEndAt.UTC(), params.LocalDate, snapshot.ID, payload, params.NotBefore)
	})
}

// Route and entitlement checks happen at claim time. A frozen message never
// follows a replaced target, a newly supplied chat ID, or a downgraded plan.
func (s *Store) ClaimDailySummaryDispatch(ctx context.Context, id int64, now time.Time) (*DailySummaryDispatchClaim, error) {
	if id < 1 || now.IsZero() {
		return nil, ErrDailySummaryDispatchConflict
	}
	return summaryDispatchTx(ctx, s.db, false, func(ctx context.Context, db DBTX) (*DailySummaryDispatchClaim, error) {
		dispatch, err := lockDailySummaryDispatch(ctx, db, id)
		if err != nil || dispatch == nil {
			return nil, err
		}
		if dispatch.Status == DailySummaryDispatchSending {
			if dispatch.LeaseUntil != nil && !dispatch.LeaseUntil.After(now) {
				_, err = db.Exec(ctx, `UPDATE daily_summary_dispatches SET status='unknown',claim_token=NULL,lease_until=NULL,last_error='expired_sending_claim' WHERE id=$1 AND status='sending'`, id)
			}
			return nil, err
		}
		if dispatch.Status != DailySummaryDispatchPending && dispatch.Status != DailySummaryDispatchFailed || dispatch.Status == DailySummaryDispatchFailed && dispatch.RetryAt == nil || dispatch.RetryAt != nil && now.Before(*dispatch.RetryAt) || dispatch.Attempts >= notificationdelivery.MaxAttempts {
			return nil, nil
		}
		var eligible bool
		err = db.QueryRow(ctx, `SELECT `+dailySummaryDispatchEligibleSourceSQL+` FROM daily_summary_dispatches d WHERE d.id=$1`, dispatch.ID).Scan(&eligible)
		if err != nil {
			return nil, err
		}
		var alreadySent bool
		if err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM daily_summary_deliveries WHERE subscription_id=$1 AND chat_type=$2 AND chat_id=$3 AND period_end_at=$4)`, dispatch.SubscriptionID, dispatch.ChatType, dispatch.ChatID, dispatch.PeriodEndAt).Scan(&alreadySent); err != nil {
			return nil, err
		}
		if !eligible || alreadySent {
			_, err = db.Exec(ctx, `UPDATE daily_summary_dispatches SET status='cancelled',retry_at=NULL,last_error='target_inactive_or_already_confirmed',closed_at=$2 WHERE id=$1`, id, now.UTC())
			return nil, err
		}
		var owner string
		if err = db.QueryRow(ctx, `SELECT debox_user_id FROM subscriptions WHERE id=$1`, dispatch.SubscriptionID).Scan(&owner); err != nil {
			return nil, err
		}
		target, err := collectOne[DailySummaryTarget](ctx, db, `SELECT `+dailySummaryEffectiveTargetColumns+` FROM daily_summary_targets t WHERE t.id=$1 AND t.subscription_id=$2`, dispatch.TargetID, dispatch.SubscriptionID)
		if err != nil {
			return nil, err
		}
		if blocked, err := checkDailySummaryLegacyReviews(ctx, db, dispatch.SubscriptionID, owner, target, now); err != nil || blocked {
			return nil, err
		}
		if dispatch.SnapshotID == nil {
			_, err = db.Exec(ctx, `UPDATE daily_summary_dispatches SET status='failed',retry_at=NULL,last_error='frozen_snapshot_missing' WHERE id=$1`, id)
			return nil, err
		}
		snapshot, err := collectOptional[NotificationDetailSnapshot](ctx, db, `SELECT `+notificationDetailSnapshotColumns+` FROM notification_detail_snapshots WHERE id=$1`, *dispatch.SnapshotID)
		if err != nil {
			return nil, err
		}
		if snapshot == nil || snapshot.NotificationChatID != dispatch.ChatID || snapshot.NotificationChatType != dispatch.ChatType || len(snapshot.NotificationText) == 0 || len(snapshot.NotificationText) > DailySummaryMaxPayloadBytes || len(snapshot.Details) > DailySummaryMaxDetailsBytes || !now.Before(snapshot.ExpiresAt) || dispatch.Payload.Validate() != nil || dispatch.Payload.ChatID != dispatch.ChatID || dispatch.Payload.ChatType != dispatch.ChatType || dispatch.Payload.Text != snapshot.NotificationText {
			_, err = db.Exec(ctx, `UPDATE daily_summary_dispatches SET status='failed',retry_at=NULL,last_error='frozen_snapshot_unavailable' WHERE id=$1`, id)
			return nil, err
		}
		if snapshot.DeBoxUserID != owner {
			_, err = db.Exec(ctx, `UPDATE daily_summary_dispatches SET status='failed',retry_at=NULL,last_error='frozen_snapshot_owner_mismatch' WHERE id=$1`, id)
			return nil, err
		}
		token, err := summaryDispatchToken()
		if err != nil {
			return nil, err
		}
		claimed, err := collectOne[DailySummaryDispatch](ctx, db, `UPDATE daily_summary_dispatches SET status='sending',claim_token=$2,last_claim_token=$2,lease_until=$3,attempts=attempts+1,attempted_at=$4,retry_at=NULL,last_error='' WHERE id=$1 RETURNING `+dailySummaryDispatchColumns, id, token, now.Add(DailySummaryClaimLease).UTC(), now.UTC())
		if err != nil {
			return nil, err
		}
		return &DailySummaryDispatchClaim{Dispatch: claimed, Snapshot: *snapshot}, nil
	})
}

func (s *Store) CompleteDailySummaryDispatch(ctx context.Context, id int64, token, messageID string, now time.Time) error {
	if id < 1 || strings.TrimSpace(token) == "" || strings.TrimSpace(messageID) == "" || len(messageID) > 1024 || now.IsZero() {
		return ErrDailySummaryDispatchConflict
	}
	_, err := summaryDispatchTx(ctx, s.db, false, func(ctx context.Context, db DBTX) (struct{}, error) {
		d, err := lockDailySummaryDispatch(ctx, db, id)
		if err != nil {
			return struct{}{}, err
		}
		if d != nil && d.Status == DailySummaryDispatchSent && d.MessageID != nil && *d.MessageID == messageID && d.LastClaimToken != nil && *d.LastClaimToken == token {
			return struct{}{}, nil
		}
		if d == nil || d.Status != DailySummaryDispatchSending || d.ClaimToken == nil || *d.ClaimToken != token || d.LeaseUntil == nil || !d.LeaseUntil.After(now) {
			return struct{}{}, ErrDailySummaryDispatchConflict
		}
		return struct{}{}, completeDailySummaryDispatch(ctx, db, *d, messageID, now)
	})
	return err
}

func (s *Store) FailDailySummaryDispatch(ctx context.Context, id int64, token, status, reason string, retryAt *time.Time, now time.Time, acceptedMessageID ...string) error {
	if status != DailySummaryDispatchFailed && status != DailySummaryDispatchUnknown || token == "" || id < 1 || now.IsZero() {
		return ErrDailySummaryDispatchConflict
	}
	if status == DailySummaryDispatchUnknown {
		retryAt = nil
	}
	accepted := ""
	if len(acceptedMessageID) > 1 {
		return ErrDailySummaryDispatchConflict
	}
	if len(acceptedMessageID) == 1 {
		accepted = strings.TrimSpace(acceptedMessageID[0])
		if status != DailySummaryDispatchUnknown || len(accepted) > 1024 {
			return ErrDailySummaryDispatchConflict
		}
	}
	if len(reason) > 1024 {
		reason = reason[:1024]
	}
	_, err := summaryDispatchTx(ctx, s.db, false, func(ctx context.Context, db DBTX) (struct{}, error) {
		d, err := lockDailySummaryDispatch(ctx, db, id)
		if err != nil {
			return struct{}{}, err
		}
		if d == nil || d.Status != DailySummaryDispatchSending || d.ClaimToken == nil || *d.ClaimToken != token {
			return struct{}{}, ErrDailySummaryDispatchConflict
		}
		tag, err := db.Exec(ctx, `UPDATE daily_summary_dispatches SET status=$3,claim_token=NULL,lease_until=NULL,last_error=$4,retry_at=$5,message_id=COALESCE(NULLIF($6,''),message_id) WHERE id=$1 AND status='sending' AND claim_token=$2`, id, token, status, reason, retryAt, accepted)
		if err != nil {
			return struct{}{}, err
		}
		if tag.RowsAffected() != 1 {
			return struct{}{}, ErrDailySummaryDispatchConflict
		}
		return struct{}{}, nil
	})
	return err
}

func (s *Store) SupersedeDailySummaryDispatch(ctx context.Context, id int64, newEnd, now time.Time) (bool, error) {
	if id < 1 || newEnd.IsZero() || now.IsZero() || newEnd.After(now) {
		return false, ErrDailySummaryDispatchConflict
	}
	return summaryDispatchTx(ctx, s.db, false, func(ctx context.Context, db DBTX) (bool, error) {
		d, err := lockDailySummaryDispatch(ctx, db, id)
		if err != nil {
			return false, err
		}
		if d == nil {
			return false, nil
		}
		tag, err := db.Exec(ctx, `UPDATE daily_summary_dispatches SET status='superseded',last_error='merged_into_later_cutoff',closed_at=$3 WHERE id=$1 AND status IN ('pending','failed') AND period_end_at<$2`, id, newEnd.UTC(), now.UTC())
		return tag.RowsAffected() == 1, err
	})
}

func summaryDispatchToken() (string, error) {
	var value [24]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create summary claim token: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
