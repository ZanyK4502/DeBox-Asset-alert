package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	NotificationDetailRetentionDays    = 30
	NotificationDetailCleanupGraceDays = 5

	NotificationKindAddressRealtime    = "address_realtime"
	NotificationKindAddressStage       = "address_stage"
	NotificationKindAddressCombination = "address_combination"
	NotificationKindMarketRealtime     = "market_realtime"
	NotificationKindMarketStage        = "market_stage"
	NotificationKindMarketCombination  = "market_combination"
	NotificationKindDailySummary       = "daily_summary"
)

const notificationDetailSnapshotColumns = `
	id, public_id, source_key, notification_kind, source_type, source_id,
	debox_user_id, rule_id, rule_type, rule_name, rule_threshold,
	actual_value, notification_chat_id, notification_chat_type,
	notification_language, notification_label, notification_text, details,
	created_at, expires_at
`

var notificationDetailPublicIDPattern = regexp.MustCompile(`^nd_[a-f0-9]{40}$`)

type NotificationDetailSnapshot struct {
	ID                   int64           `db:"id" json:"id"`
	PublicID             string          `db:"public_id" json:"public_id"`
	SourceKey            string          `db:"source_key" json:"source_key"`
	NotificationKind     string          `db:"notification_kind" json:"notification_kind"`
	SourceType           string          `db:"source_type" json:"source_type"`
	SourceID             *int64          `db:"source_id" json:"source_id,omitempty"`
	DeBoxUserID          string          `db:"debox_user_id" json:"debox_user_id"`
	RuleID               *int64          `db:"rule_id" json:"rule_id,omitempty"`
	RuleType             string          `db:"rule_type" json:"rule_type"`
	RuleName             string          `db:"rule_name" json:"rule_name"`
	RuleThreshold        string          `db:"rule_threshold" json:"rule_threshold"`
	ActualValue          string          `db:"actual_value" json:"actual_value"`
	NotificationChatID   string          `db:"notification_chat_id" json:"notification_chat_id"`
	NotificationChatType string          `db:"notification_chat_type" json:"notification_chat_type"`
	NotificationLanguage string          `db:"notification_language" json:"notification_language"`
	NotificationLabel    string          `db:"notification_label" json:"notification_label"`
	NotificationText     string          `db:"notification_text" json:"notification_text"`
	Details              json.RawMessage `db:"details" json:"details"`
	CreatedAt            time.Time       `db:"created_at" json:"created_at"`
	ExpiresAt            time.Time       `db:"expires_at" json:"expires_at"`
}

type CreateNotificationDetailSnapshotParams struct {
	SourceKey            string
	NotificationKind     string
	SourceType           string
	SourceID             *int64
	DeBoxUserID          string
	RuleID               *int64
	RuleType             string
	RuleName             string
	RuleThreshold        string
	ActualValue          string
	NotificationChatID   string
	NotificationChatType string
	NotificationLanguage string
	NotificationLabel    string
	NotificationText     string
	Details              json.RawMessage
}

func (s *Store) CreateNotificationDetailSnapshot(
	ctx context.Context,
	params CreateNotificationDetailSnapshotParams,
) (NotificationDetailSnapshot, error) {
	params.SourceKey = strings.TrimSpace(params.SourceKey)
	params.NotificationKind = strings.ToLower(strings.TrimSpace(params.NotificationKind))
	params.SourceType = strings.ToLower(strings.TrimSpace(params.SourceType))
	params.DeBoxUserID = strings.TrimSpace(params.DeBoxUserID)
	params.NotificationChatID = strings.TrimSpace(params.NotificationChatID)
	params.NotificationChatType = strings.ToLower(strings.TrimSpace(
		params.NotificationChatType,
	))
	params.NotificationLanguage = normalizeLanguage(params.NotificationLanguage)
	if !validNotificationDetailParams(params) {
		return NotificationDetailSnapshot{}, ErrInvalidNotificationSnapshot
	}
	details, err := notificationDetailObject(params.Details)
	if err != nil {
		return NotificationDetailSnapshot{}, err
	}
	publicID, err := notificationDetailPublicID()
	if err != nil {
		return NotificationDetailSnapshot{}, err
	}
	snapshot, err := collectOne[NotificationDetailSnapshot](ctx, s.db, `
		INSERT INTO notification_detail_snapshots (
			public_id, source_key, notification_kind, source_type, source_id,
			debox_user_id, rule_id, rule_type, rule_name, rule_threshold,
			actual_value, notification_chat_id, notification_chat_type,
			notification_language, notification_label, notification_text, details,
			expires_at
		)
		VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15, $16, $17::jsonb,
			NOW() + ($18 * INTERVAL '1 day')
		)
		ON CONFLICT (source_key) DO UPDATE
		SET source_key = notification_detail_snapshots.source_key
		RETURNING `+notificationDetailSnapshotColumns,
		publicID,
		params.SourceKey,
		params.NotificationKind,
		params.SourceType,
		params.SourceID,
		params.DeBoxUserID,
		params.RuleID,
		strings.TrimSpace(params.RuleType),
		strings.TrimSpace(params.RuleName),
		strings.TrimSpace(params.RuleThreshold),
		strings.TrimSpace(params.ActualValue),
		params.NotificationChatID,
		params.NotificationChatType,
		params.NotificationLanguage,
		strings.TrimSpace(params.NotificationLabel),
		params.NotificationText,
		string(details),
		NotificationDetailRetentionDays,
	)
	if err != nil {
		return NotificationDetailSnapshot{}, fmt.Errorf(
			"create notification detail snapshot: %w",
			err,
		)
	}
	return snapshot, nil
}

func (s *Store) DeleteNotificationDetailSnapshotsForUser(
	ctx context.Context,
	deboxUserID string,
) (int64, error) {
	deboxUserID = strings.TrimSpace(deboxUserID)
	if deboxUserID == "" {
		return 0, ErrInvalidNotificationSnapshot
	}
	tag, err := s.db.Exec(ctx, `
		DELETE FROM notification_detail_snapshots
		WHERE debox_user_id = $1
	`, deboxUserID)
	if err != nil {
		return 0, fmt.Errorf("delete user notification detail snapshots: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *Store) GetNotificationDetailSnapshot(
	ctx context.Context,
	publicID string,
) (*NotificationDetailSnapshot, error) {
	publicID = strings.TrimSpace(publicID)
	if !notificationDetailPublicIDPattern.MatchString(publicID) {
		return nil, ErrInvalidNotificationSnapshot
	}
	snapshot, err := collectOptional[NotificationDetailSnapshot](ctx, s.db, `
		SELECT `+notificationDetailSnapshotColumns+`
		FROM notification_detail_snapshots
		WHERE public_id = $1
	`, publicID)
	if err != nil {
		return nil, fmt.Errorf("get notification detail snapshot: %w", err)
	}
	return snapshot, nil
}

func validNotificationDetailParams(params CreateNotificationDetailSnapshotParams) bool {
	if params.SourceKey == "" || params.SourceType == "" ||
		params.DeBoxUserID == "" || params.NotificationChatID == "" ||
		strings.TrimSpace(params.NotificationText) == "" {
		return false
	}
	if params.NotificationChatType != "private" &&
		params.NotificationChatType != "group" {
		return false
	}
	switch params.NotificationKind {
	case NotificationKindAddressRealtime,
		NotificationKindAddressStage,
		NotificationKindAddressCombination,
		NotificationKindMarketRealtime,
		NotificationKindMarketStage,
		NotificationKindMarketCombination,
		NotificationKindDailySummary:
		return true
	default:
		return false
	}
}

func notificationDetailObject(value json.RawMessage) (json.RawMessage, error) {
	if len(value) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil || object == nil {
		return nil, ErrInvalidNotificationSnapshot
	}
	return value, nil
}

func notificationDetailPublicID() (string, error) {
	value := make([]byte, 20)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate notification detail public id: %w", err)
	}
	return "nd_" + hex.EncodeToString(value), nil
}
