package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestCreateNotificationDetailSnapshotPersistsImmutableSourceSnapshot(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	defer mock.Close()

	createdAt := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	expiresAt := createdAt.Add(NotificationDetailRetentionDays * 24 * time.Hour)
	mock.ExpectQuery(`INSERT INTO notification_detail_snapshots.*ON CONFLICT \(source_key\)`).
		WithArgs(
			pgxmock.AnyArg(),
			"address_realtime:42",
			NotificationKindAddressRealtime,
			"alert_event",
			int64Pointer(42),
			"user-1",
			int64Pointer(7),
			"outgoing",
			"转出提醒",
			"100",
			"80",
			"user-1",
			"private",
			"zh",
			"主钱包",
			"notification body",
			`{"event":{"id":42}}`,
			NotificationDetailRetentionDays,
		).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "public_id", "source_key", "notification_kind", "source_type",
			"source_id", "debox_user_id", "rule_id", "rule_type", "rule_name",
			"rule_threshold", "actual_value", "notification_chat_id",
			"notification_chat_type", "notification_language", "notification_label",
			"notification_text", "details", "created_at", "expires_at",
		}).AddRow(
			1, "nd_public", "address_realtime:42", NotificationKindAddressRealtime,
			"alert_event", int64Pointer(42), "user-1", int64Pointer(7), "outgoing", "转出提醒",
			"100", "80", "user-1", "private", "zh", "主钱包",
			"notification body", []byte(`{"event":{"id":42}}`), createdAt, expiresAt,
		))

	snapshot, err := newWithDB(mock).CreateNotificationDetailSnapshot(
		context.Background(),
		CreateNotificationDetailSnapshotParams{
			SourceKey:            "address_realtime:42",
			NotificationKind:     NotificationKindAddressRealtime,
			SourceType:           "alert_event",
			SourceID:             int64Pointer(42),
			DeBoxUserID:          "user-1",
			RuleID:               int64Pointer(7),
			RuleType:             "outgoing",
			RuleName:             "转出提醒",
			RuleThreshold:        "100",
			ActualValue:          "80",
			NotificationChatID:   "user-1",
			NotificationChatType: "private",
			NotificationLanguage: "zh",
			NotificationLabel:    "主钱包",
			NotificationText:     "notification body",
			Details:              json.RawMessage(`{"event":{"id":42}}`),
		},
	)
	if err != nil {
		t.Fatalf("CreateNotificationDetailSnapshot(): %v", err)
	}
	if snapshot.PublicID != "nd_public" || snapshot.SourceID == nil ||
		*snapshot.SourceID != 42 || !snapshot.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestCreateNotificationDetailSnapshotRejectsInvalidData(t *testing.T) {
	t.Parallel()
	store := newWithDB(nil)
	valid := CreateNotificationDetailSnapshotParams{
		SourceKey:            "daily:1",
		NotificationKind:     NotificationKindDailySummary,
		SourceType:           "daily_summary_target",
		DeBoxUserID:          "user-1",
		NotificationChatID:   "user-1",
		NotificationChatType: "private",
		NotificationText:     "summary",
		Details:              json.RawMessage(`{"period":"today"}`),
	}

	invalidKind := valid
	invalidKind.NotificationKind = "unknown"
	if _, err := store.CreateNotificationDetailSnapshot(context.Background(), invalidKind); !errors.Is(err, ErrInvalidNotificationSnapshot) {
		t.Fatalf("invalid kind error = %v", err)
	}
	invalidJSON := valid
	invalidJSON.Details = json.RawMessage(`[]`)
	if _, err := store.CreateNotificationDetailSnapshot(context.Background(), invalidJSON); !errors.Is(err, ErrInvalidNotificationSnapshot) {
		t.Fatalf("invalid details error = %v", err)
	}
}

func TestDeleteNotificationDetailSnapshotsForUser(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	defer mock.Close()
	mock.ExpectExec("DELETE FROM notification_detail_snapshots").
		WithArgs("user-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 3))

	deleted, err := newWithDB(mock).DeleteNotificationDetailSnapshotsForUser(
		context.Background(),
		"user-1",
	)
	if err != nil || deleted != 3 {
		t.Fatalf("DeleteNotificationDetailSnapshotsForUser() = %d, %v", deleted, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetNotificationDetailSnapshotByPublicID(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	defer mock.Close()
	publicID := "nd_0123456789abcdef0123456789abcdef01234567"
	createdAt := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	expiresAt := createdAt.Add(30 * 24 * time.Hour)
	mock.ExpectQuery("SELECT .* FROM notification_detail_snapshots").
		WithArgs(publicID).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "public_id", "source_key", "notification_kind", "source_type",
			"source_id", "debox_user_id", "rule_id", "rule_type", "rule_name",
			"rule_threshold", "actual_value", "notification_chat_id",
			"notification_chat_type", "notification_language", "notification_label",
			"notification_text", "details", "created_at", "expires_at",
		}).AddRow(
			1, publicID, "address_realtime:42", NotificationKindAddressRealtime,
			"alert_event", int64Pointer(42), "user-1", int64Pointer(7),
			"outgoing", "转出提醒", "100", "125", "user-1", "private",
			"zh", "主钱包", "notification body",
			[]byte(`{"event":{"id":42}}`), createdAt, expiresAt,
		))

	snapshot, err := newWithDB(mock).GetNotificationDetailSnapshot(
		context.Background(),
		publicID,
	)
	if err != nil || snapshot == nil || snapshot.PublicID != publicID ||
		snapshot.RuleID == nil || *snapshot.RuleID != 7 {
		t.Fatalf("GetNotificationDetailSnapshot() = %#v, %v", snapshot, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetNotificationDetailSnapshotRejectsInvalidPublicID(t *testing.T) {
	t.Parallel()
	_, err := newWithDB(nil).GetNotificationDetailSnapshot(
		context.Background(),
		"nd_invalid",
	)
	if !errors.Is(err, ErrInvalidNotificationSnapshot) {
		t.Fatalf("error = %v", err)
	}
}

func TestGetNotificationDetailSnapshotReturnsNilWhenMissing(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	defer mock.Close()
	publicID := "nd_0123456789abcdef0123456789abcdef01234567"
	mock.ExpectQuery("SELECT .* FROM notification_detail_snapshots").
		WithArgs(publicID).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "public_id", "source_key", "notification_kind", "source_type",
			"source_id", "debox_user_id", "rule_id", "rule_type", "rule_name",
			"rule_threshold", "actual_value", "notification_chat_id",
			"notification_chat_type", "notification_language", "notification_label",
			"notification_text", "details", "created_at", "expires_at",
		}))
	snapshot, err := newWithDB(mock).GetNotificationDetailSnapshot(
		context.Background(),
		publicID,
	)
	if err != nil || snapshot != nil {
		t.Fatalf("GetNotificationDetailSnapshot() = %#v, %v", snapshot, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func int64Pointer(value int64) *int64 {
	return &value
}
