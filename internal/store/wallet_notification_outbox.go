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

const walletNotificationOutboxColumns = `id,source_key,source_type,source_id,watch_rule_id,source_generation,combination_rule_id,debox_user_id,snapshot_id,payload,status,attempts,next_attempt_at,lease_token,lease_expires_at,message_id,last_error,created_at,attempted_at,send_started_at,closed_at,sent_at`

type WalletNotificationPayload struct {
	notificationdelivery.Payload
	PublicID            string     `json:"public_id"`
	Language            string     `json:"language"`
	SourceOccurredAt    *time.Time `json:"source_occurred_at,omitempty"`
	SourceTimeBasis     string     `json:"source_time_basis,omitempty"`
	ReadObservedAt      *time.Time `json:"read_observed_at,omitempty"`
	BalanceReadSource   string     `json:"balance_read_source,omitempty"`
	BalanceReadDecimals *int       `json:"balance_read_decimals,omitempty"`
}

type WalletNotificationOutbox struct {
	ID                int64           `db:"id"`
	SourceKey         string          `db:"source_key"`
	SourceType        string          `db:"source_type"`
	SourceID          int64           `db:"source_id"`
	WatchRuleID       int64           `db:"watch_rule_id"`
	SourceGeneration  int64           `db:"source_generation"`
	CombinationRuleID *int64          `db:"combination_rule_id"`
	DeBoxUserID       string          `db:"debox_user_id"`
	SnapshotID        int64           `db:"snapshot_id"`
	Payload           json.RawMessage `db:"payload"`
	Status            string          `db:"status"`
	Attempts          int32           `db:"attempts"`
	NextAttemptAt     time.Time       `db:"next_attempt_at"`
	LeaseToken        *string         `db:"lease_token"`
	LeaseExpiresAt    *time.Time      `db:"lease_expires_at"`
	MessageID         *string         `db:"message_id"`
	LastError         string          `db:"last_error"`
	CreatedAt         time.Time       `db:"created_at"`
	AttemptedAt       *time.Time      `db:"attempted_at"`
	SendStartedAt     *time.Time      `db:"send_started_at"`
	ClosedAt          *time.Time      `db:"closed_at"`
	SentAt            *time.Time      `db:"sent_at"`
}

type FinishWalletNotificationParams struct {
	ID         int64
	LeaseToken string
	Status     string
	MessageID  *string
	Error      string
	RetryAt    *time.Time
}

// ClaimWalletNotifications never reclaims an expired send as safe-to-retry:
// a vanished sender may have delivered it. Such rows become unknown instead.
func (s *Store) ClaimWalletNotifications(ctx context.Context, onlyID int64, limit int) ([]WalletNotificationOutbox, error) {
	limit = clamp(limit, 1, notificationdelivery.MaxBatch)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return withTxValue(ctx, s.db, func(tx DBTX) ([]WalletNotificationOutbox, error) {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '2s'`); err != nil {
			return nil, err
		}
		if _, err := cancelInvalidWalletNotifications(ctx, tx, "", "wallet rule or destination invalidated before delivery"); err != nil {
			return nil, err
		}
		cutoff, err := notificationSourceCutoff(ctx, tx)
		if err != nil {
			return nil, err
		}
		if cutoff != nil {
			old, err := collectMany[WalletNotificationOutbox](ctx, tx, `WITH old AS (SELECT id FROM wallet_notification_outbox WHERE status IN ('pending','retry') AND created_at<$1 ORDER BY id LIMIT $2 FOR UPDATE SKIP LOCKED) UPDATE wallet_notification_outbox o SET status='skipped',closed_at=COALESCE(closed_at,NOW()),last_error=CASE WHEN last_error='' THEN 'notification created before recovery boundary' ELSE last_error||'; notification created before recovery boundary' END FROM old WHERE o.id=old.id RETURNING `+walletOutboxQualifiedColumns("o"), *cutoff, limit)
			if err != nil {
				return nil, err
			}
			for _, row := range old {
				if err := mirrorWalletNotification(ctx, tx, row, "skipped", nil, row.LastError); err != nil {
					return nil, err
				}
			}
		}
		expired, err := collectMany[WalletNotificationOutbox](ctx, tx, `WITH expired AS (SELECT id FROM wallet_notification_outbox WHERE status='sending' AND lease_expires_at<=NOW() ORDER BY lease_expires_at,id LIMIT $1 FOR UPDATE SKIP LOCKED) UPDATE wallet_notification_outbox o SET status=CASE WHEN send_started_at IS NULL THEN 'retry' ELSE 'unknown' END,next_attempt_at=CASE WHEN send_started_at IS NULL THEN NOW() ELSE next_attempt_at END,last_error=CASE WHEN send_started_at IS NULL THEN 'send lease expired before transport started' ELSE 'delivery outcome unknown after send lease expired' END,lease_token=NULL,lease_expires_at=NULL FROM expired WHERE o.id=expired.id RETURNING `+walletOutboxQualifiedColumns("o"), limit)
		if err != nil {
			return nil, fmt.Errorf("recover wallet send leases: %w", err)
		}
		for _, row := range expired {
			status := row.Status
			if status == "retry" {
				status = "failed"
			}
			if err := mirrorWalletNotification(ctx, tx, row, status, nil, row.LastError); err != nil {
				return nil, err
			}
		}
		var tokenBytes [20]byte
		if _, err := rand.Read(tokenBytes[:]); err != nil {
			return nil, err
		}
		token := hex.EncodeToString(tokenBytes[:])
		rows, err := collectMany[WalletNotificationOutbox](ctx, tx, `WITH due AS (SELECT o.id FROM wallet_notification_outbox o WHERE status IN ('pending','retry') AND attempts<5 AND next_attempt_at<=NOW() AND ($1::bigint=0 OR o.id=$1) AND `+walletOutboxRuleCurrentSQL("o")+` ORDER BY next_attempt_at,o.id LIMIT $2 FOR UPDATE OF o SKIP LOCKED) UPDATE wallet_notification_outbox o SET status='sending',send_started_at=NULL,lease_token=$3,lease_expires_at=NOW()+INTERVAL '2 minutes' FROM due WHERE o.id=due.id RETURNING `+walletOutboxQualifiedColumns("o"), onlyID, limit, token)
		if err != nil {
			return nil, fmt.Errorf("claim wallet notifications: %w", err)
		}
		return rows, nil
	})
}

func (s *Store) StartWalletNotificationAttempt(ctx context.Context, id int64, token string) (WalletNotificationOutbox, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	row, err := collectOne[WalletNotificationOutbox](ctx, s.db, `UPDATE wallet_notification_outbox o SET attempts=attempts+1,attempted_at=NOW(),send_started_at=NOW() WHERE id=$1 AND status='sending' AND attempts<5 AND lease_token=$2 AND lease_expires_at>NOW() AND send_started_at IS NULL AND `+walletOutboxRuleCurrentSQL("o")+` RETURNING `+walletNotificationOutboxColumns, id, token)
	if isNoRows(err) {
		return WalletNotificationOutbox{}, ErrWalletObservationStale
	}
	if err != nil {
		return WalletNotificationOutbox{}, fmt.Errorf("start wallet notification attempt: %w", err)
	}
	return row, nil
}

func walletOutboxQualifiedColumns(alias string) string {
	parts := strings.Split(walletNotificationOutboxColumns, ",")
	for i, p := range parts {
		parts[i] = alias + "." + p
	}
	return strings.Join(parts, ",")
}

// WalletNotificationRule validates current delivery eligibility without changing
// the frozen destination or rebuilding the original message.
func (s *Store) WalletNotificationRule(ctx context.Context, row WalletNotificationOutbox) (WatchRule, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cutoff, err := s.GetNotificationSourceCutoff(ctx)
	if err != nil {
		return WatchRule{}, false, err
	}
	if cutoff != nil && row.CreatedAt.Before(*cutoff) {
		return WatchRule{}, false, nil
	}
	rule, err := collectOptional[WatchRule](ctx, s.db, `SELECT `+watchRuleColumns+` FROM watch_rules WHERE id=$1 AND debox_user_id=$2 AND enabled=1 AND run_status='active'`, row.WatchRuleID, row.DeBoxUserID)
	if err != nil {
		return WatchRule{}, false, err
	}
	if rule == nil {
		return WatchRule{}, false, nil
	}
	plan, eligible, err := walletRuleCurrentEligibility(ctx, s.db, *rule)
	if err != nil {
		return WatchRule{}, false, err
	}
	rule.EffectivePlanCode = plan
	var generationMatches bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM watch_rules WHERE id=$1 AND debox_user_id=$2 AND notification_generation=$3)`, row.WatchRuleID, row.DeBoxUserID, row.SourceGeneration).Scan(&generationMatches); err != nil {
		return *rule, false, err
	}
	if !generationMatches {
		return *rule, false, nil
	}
	var payload WalletNotificationPayload
	if json.Unmarshal(row.Payload, &payload) != nil {
		return *rule, false, ErrInvalidNotificationSnapshot
	}
	if !eligible || rule.NotificationChatID != payload.ChatID || rule.NotificationChatType != payload.ChatType {
		return *rule, false, nil
	}
	var sourceExists bool
	if row.SourceType == "alert_event" {
		err = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM alert_events WHERE id=$1 AND watch_rule_id=$2)`, row.SourceID, rule.ID).Scan(&sourceExists)
	} else {
		err = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM aggregate_notifications an JOIN aggregation_windows aw ON aw.id=an.aggregation_window_id WHERE an.id=$1 AND an.debox_user_id=$2 AND (aw.watch_rule_id=$3 OR aw.combination_rule_id=$4))`, row.SourceID, row.DeBoxUserID, rule.ID, row.CombinationRuleID).Scan(&sourceExists)
	}
	if err != nil {
		return *rule, false, err
	}
	if !sourceExists {
		return *rule, false, nil
	}
	if row.CombinationRuleID != nil {
		var active bool
		err = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM combination_rules cr JOIN combination_rule_members crm ON crm.combination_rule_id=cr.id WHERE cr.id=$1 AND cr.debox_user_id=$2 AND cr.enabled=1 AND cr.run_status='active' AND crm.watch_rule_id=$3 AND cr.notification_chat_id=$4 AND cr.notification_chat_type=$5)`, *row.CombinationRuleID, row.DeBoxUserID, rule.ID, payload.ChatID, payload.ChatType).Scan(&active)
		if err != nil {
			return *rule, false, err
		}
		if !active {
			return *rule, false, nil
		}
	}
	if payload.ChatType == "group" {
		var enabled bool
		err = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notification_groups WHERE debox_user_id=$1 AND gid=$2 AND enabled=1)`, row.DeBoxUserID, payload.ChatID).Scan(&enabled)
		if err != nil {
			return *rule, false, err
		}
		if !enabled {
			return *rule, false, nil
		}
	}
	return *rule, true, nil
}

func (s *Store) FinishWalletNotification(ctx context.Context, p FinishWalletNotificationParams) (WalletNotificationOutbox, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	switch p.Status {
	case "sent", "retry", "failed", "unknown", "skipped":
	default:
		return WalletNotificationOutbox{}, ErrInvalidNotificationStatus
	}
	if p.Status == "sent" && (p.MessageID == nil || strings.TrimSpace(*p.MessageID) == "") {
		return WalletNotificationOutbox{}, ErrInvalidNotificationStatus
	}
	if p.Status == "retry" && p.RetryAt == nil {
		return WalletNotificationOutbox{}, ErrInvalidNotificationStatus
	}
	return withTxValue(ctx, s.db, func(tx DBTX) (WalletNotificationOutbox, error) {
		row, err := collectOne[WalletNotificationOutbox](ctx, tx, `UPDATE wallet_notification_outbox SET status=$3,message_id=$4,last_error=$5,next_attempt_at=COALESCE($6::timestamptz,next_attempt_at),sent_at=CASE WHEN $3='sent' THEN NOW() ELSE sent_at END,closed_at=CASE WHEN $3 IN ('sent','failed','skipped') THEN COALESCE(closed_at,NOW()) ELSE closed_at END,lease_token=NULL,lease_expires_at=NULL WHERE id=$1 AND status='sending' AND lease_token=$2 RETURNING `+walletNotificationOutboxColumns, p.ID, p.LeaseToken, p.Status, p.MessageID, truncate(p.Error, 500), p.RetryAt)
		if isNoRows(err) {
			return WalletNotificationOutbox{}, ErrWalletObservationStale
		}
		if err != nil {
			return WalletNotificationOutbox{}, fmt.Errorf("finish wallet notification: %w", err)
		}
		status := p.Status
		if status == "retry" {
			status = "failed"
		}
		if err := mirrorWalletNotification(ctx, tx, row, status, p.MessageID, row.LastError); err != nil {
			return WalletNotificationOutbox{}, err
		}
		return row, nil
	})
}

func mirrorWalletNotification(ctx context.Context, db DBTX, row WalletNotificationOutbox, status string, messageID *string, detail string) error {
	table := "alert_events"
	if row.SourceType == "aggregate_notification" {
		table = "aggregate_notifications"
	} else if row.SourceType != "alert_event" {
		return errors.New("invalid wallet notification source")
	}
	_, err := db.Exec(ctx, `UPDATE `+table+` SET notification_status=$2,notification_message_id=$3,notification_error=$4,notification_attempts=$5,notification_attempted_at=$6,notification_sent_at=CASE WHEN $2='sent' THEN NOW() ELSE notification_sent_at END WHERE id=$1 AND notification_status<>'sent'`, row.SourceID, status, messageID, truncate(detail, 2000), row.Attempts, row.AttemptedAt)
	return err
}

// ResolveWalletDeliveryUnknown is an explicit, evidence-backed control-plane
// operation. There is deliberately no HTTP route or background caller.
func (s *Store) ResolveWalletDeliveryUnknown(ctx context.Context, id int64, delivered bool, messageID, explanation string) (WalletNotificationOutbox, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	messageID = strings.TrimSpace(messageID)
	explanation = strings.TrimSpace(explanation)
	if id < 1 || explanation == "" || len(explanation) > 1000 || delivered && messageID == "" || !delivered && messageID != "" {
		return WalletNotificationOutbox{}, ErrInvalidNotificationStatus
	}
	return withTxValue(ctx, s.db, func(tx DBTX) (WalletNotificationOutbox, error) {
		var previousError string
		if err := tx.QueryRow(ctx, `SELECT last_error FROM wallet_notification_outbox WHERE id=$1 AND status='unknown' FOR UPDATE`, id).Scan(&previousError); isNoRows(err) {
			return WalletNotificationOutbox{}, ErrWalletObservationStale
		} else if err != nil {
			return WalletNotificationOutbox{}, err
		}
		status := "retry"
		var msg *string
		if delivered {
			status = "sent"
			msg = &messageID
		}
		row, err := collectOne[WalletNotificationOutbox](ctx, tx, `UPDATE wallet_notification_outbox SET status=CASE WHEN $2='retry' AND attempts>=5 THEN 'failed' ELSE $2 END,message_id=$3,last_error=$4,next_attempt_at=CASE WHEN $2='retry' AND attempts<5 THEN NOW() ELSE next_attempt_at END,sent_at=CASE WHEN $2='sent' THEN NOW() ELSE sent_at END,closed_at=CASE WHEN $2='sent' OR($2='retry' AND attempts>=5) THEN COALESCE(closed_at,NOW()) ELSE closed_at END WHERE id=$1 AND status='unknown' RETURNING `+walletNotificationOutboxColumns, id, status, msg, "explicit delivery resolution: "+explanation)
		if isNoRows(err) {
			return WalletNotificationOutbox{}, ErrWalletObservationStale
		}
		if err != nil {
			return WalletNotificationOutbox{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO wallet_notification_unknown_resolutions(outbox_id,delivered,message_id,explanation,attempts,previous_error) VALUES($1,$2,$3,$4,$5,$6)`, id, delivered, msg, explanation, row.Attempts, truncate(previousError, 2000)); err != nil {
			return WalletNotificationOutbox{}, err
		}
		mirrorStatus := status
		if mirrorStatus == "retry" {
			mirrorStatus = "failed"
		}
		if err := mirrorWalletNotification(ctx, tx, row, mirrorStatus, msg, row.LastError); err != nil {
			return WalletNotificationOutbox{}, err
		}
		return row, nil
	})
}
