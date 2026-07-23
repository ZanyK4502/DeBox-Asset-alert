package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestOpenRequiresDatabaseURL(t *testing.T) {
	t.Parallel()

	store, err := Open(context.Background(), "   ")
	if err == nil {
		store.Close()
		t.Fatal("Open() expected an error for an empty DATABASE_URL")
	}
}

func TestCleanupAuthRecordsCommits(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM auth_challenges").
		WillReturnResult(pgxmock.NewResult("DELETE", 2))
	mock.ExpectExec("DELETE FROM auth_sessions").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectCommit()

	if err := newWithDB(mock).CleanupAuthRecords(context.Background()); err != nil {
		t.Fatalf("CleanupAuthRecords(): %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestCleanupAuthRecordsRollsBack(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	defer mock.Close()

	queryErr := errors.New("database unavailable")
	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM auth_challenges").
		WillReturnResult(pgxmock.NewResult("DELETE", 2))
	mock.ExpectExec("DELETE FROM auth_sessions").
		WillReturnError(queryErr)
	mock.ExpectRollback()

	err = newWithDB(mock).CleanupAuthRecords(context.Background())
	if !errors.Is(err, queryErr) {
		t.Fatalf("CleanupAuthRecords() error = %v, want wrapped %v", err, queryErr)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestCreateOrderUniqueConflictRollsBack(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs("user-1").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT plan_code").
		WithArgs("user-1").
		WillReturnRows(pgxmock.NewRows([]string{"plan_code"}))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("user-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec("UPDATE orders").
		WithArgs("user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO orders").
		WithArgs(
			"user-1",
			"0xabc",
			"standard",
			"bsc",
			int32(56),
			pgxmock.AnyArg(),
			"USDT",
			int32(18),
			"10",
			"0xrecipient",
			pgxmock.AnyArg(),
		).
		WillReturnError(&pgconn.PgError{Code: "23505"})
	mock.ExpectRollback()

	_, err = newWithDB(mock).CreateOrder(context.Background(), CreateOrderParams{
		DeBoxUserID:      "user-1",
		PayerAddress:     "0xabc",
		PlanCode:         "standard",
		ChainKey:         "bsc",
		ChainID:          56,
		TokenSymbol:      "USDT",
		TokenDecimals:    18,
		TotalAmount:      "10",
		RecipientAddress: "0xrecipient",
	})
	if !errors.Is(err, ErrOrderConflict) {
		t.Fatalf("CreateOrder() error = %v, want %v", err, ErrOrderConflict)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestSetFreeWatchRuleRejectsIneligibleRule(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool(): %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs("user-1").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("UPDATE subscriptions").
		WithArgs("user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectQuery("SELECT COALESCE").
		WithArgs("user-1").
		WillReturnRows(pgxmock.NewRows([]string{"plan_code"}).AddRow("free"))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(int64(7), "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectRollback()

	_, err = newWithDB(mock).SetFreeWatchRule(context.Background(), "user-1", 7)
	if !errors.Is(err, ErrInvalidFreeWatchRule) {
		t.Fatalf("SetFreeWatchRule() error = %v, want %v", err, ErrInvalidFreeWatchRule)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPureHelpers(t *testing.T) {
	t.Parallel()

	if got := normalizeLanguage("en"); got != "en" {
		t.Fatalf("normalizeLanguage(en) = %q", got)
	}
	if got := normalizeLanguage(" EN "); got != "en" {
		t.Fatalf("normalizeLanguage(EN) = %q, want en", got)
	}
	if got := clamp(0, 1, 20); got != 1 {
		t.Fatalf("clamp lower bound = %d", got)
	}
	if got := clamp(50, 1, 20); got != 20 {
		t.Fatalf("clamp upper bound = %d", got)
	}
	if got := truncate("中文abc", 3); got != "中文a" {
		t.Fatalf("truncate() = %q, want %q", got, "中文a")
	}
}
