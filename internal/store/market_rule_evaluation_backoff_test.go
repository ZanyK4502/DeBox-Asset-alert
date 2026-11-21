package store

import (
	"context"
	"errors"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestDeferMarketRuleEvaluationBoundsRetryAndSurfacesFailures(t *testing.T) {
	for _, test := range []struct {
		delay   time.Duration
		seconds int64
	}{{0, 60}, {-time.Second, 60}, {2 * time.Minute, 60}, {time.Nanosecond, 1}, {2 * time.Second, 2}} {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		mock.ExpectExec("UPDATE market_rules SET last_evaluated_at").WithArgs(int64(7), test.seconds).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		if err := newWithDB(mock).DeferMarketRuleEvaluation(context.Background(), 7, test.delay); err != nil {
			t.Fatal(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		mock.Close()
	}
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	failure := errors.New("fixture database failure")
	mock.ExpectExec("UPDATE market_rules SET last_evaluated_at").WithArgs(int64(7), int64(60)).WillReturnError(failure)
	if err := newWithDB(mock).DeferMarketRuleEvaluation(context.Background(), 7, time.Minute); !errors.Is(err, failure) {
		t.Fatalf("defer swallowed database error: %v", err)
	}
	if err := newWithDB(nil).DeferMarketRuleEvaluation(context.Background(), 0, time.Minute); !errors.Is(err, ErrInvalidMarketRule) {
		t.Fatalf("invalid rule=%v", err)
	}
}
