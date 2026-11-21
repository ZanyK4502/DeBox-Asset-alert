package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestMarketRecommendationSamplesRejectCanceledContextBeforeDatabaseRead(t *testing.T) {
	now := time.Now().UTC()
	for _, deadline := range []bool{false, true} {
		var ctx context.Context
		var cancel context.CancelFunc
		want := error(context.Canceled)
		if deadline {
			ctx, cancel = context.WithDeadline(context.Background(), now.Add(-time.Second))
			want = context.DeadlineExceeded
		} else {
			ctx, cancel = context.WithCancel(context.Background())
			cancel()
		}
		// A nil database makes any attempted I/O fail the test, rather than
		// depending on a PostgreSQL connection to notice an already ended request.
		samples, err := (&Store{}).ListMarketRecommendationSamples(ctx, 1, "fixture", MarketSnapshot{ChainID: 56, TokenAddress: tradeFixtureToken}, now.Add(-24*time.Hour), now, 100)
		cancel()
		if !errors.Is(err, ErrMarketRecommendationUnavailable) || !errors.Is(err, want) || len(samples) != 0 {
			t.Fatalf("canceled request=%+v %v", samples, err)
		}
	}
}

func TestMarketRecommendationSamplesMapServerTimeoutWithoutSwallowingOtherErrors(t *testing.T) {
	timeout := &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}
	err := marketRecommendationReadError(context.Background(), timeout)
	if !errors.Is(err, ErrMarketRecommendationUnavailable) || !errors.Is(err, timeout) {
		t.Fatalf("server timeout mapping=%v", err)
	}
	failure := errors.New("fixture read failure")
	err = marketRecommendationReadError(context.Background(), failure)
	if errors.Is(err, ErrMarketRecommendationUnavailable) || !errors.Is(err, failure) {
		t.Fatalf("normal failure mapping=%v", err)
	}
}
