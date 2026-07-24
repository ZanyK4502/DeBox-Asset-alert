package store

import (
	"context"
	"errors"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestCleanupAggregationHistoryDeletesExpiredRecordsInTransaction(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM aggregate_notifications an.*NOT EXISTS").
		WithArgs(AggregationHistoryRetentionDays).
		WillReturnResult(pgxmock.NewResult("DELETE", 2))
	mock.ExpectExec("DELETE FROM rule_trigger_events").
		WithArgs(AggregationHistoryRetentionDays).
		WillReturnResult(pgxmock.NewResult("DELETE", 5))
	mock.ExpectExec("DELETE FROM aggregation_windows aw").
		WithArgs(AggregationHistoryRetentionDays).
		WillReturnResult(pgxmock.NewResult("DELETE", 3))
	mock.ExpectCommit()

	result, err := newWithDB(mock).CleanupAggregationHistory(context.Background())
	if err != nil {
		t.Fatalf("CleanupAggregationHistory(): %v", err)
	}
	if result.NotificationsDeleted != 2 ||
		result.EventsDeleted != 5 ||
		result.WindowsDeleted != 3 {
		t.Fatalf("cleanup result = %+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestCleanupAggregationHistoryRollsBackOnFailure(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	defer mock.Close()

	queryErr := errors.New("database unavailable")
	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM aggregate_notifications an.*NOT EXISTS").
		WithArgs(AggregationHistoryRetentionDays).
		WillReturnError(queryErr)
	mock.ExpectRollback()

	_, err = newWithDB(mock).CleanupAggregationHistory(context.Background())
	if !errors.Is(err, queryErr) {
		t.Fatalf("error = %v, want wrapped %v", err, queryErr)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
