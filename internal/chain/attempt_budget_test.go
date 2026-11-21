package chain

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNoditBudgetIncludesEachRetriedHTTPAttempt(t *testing.T) {
	for _, node := range []bool{false, true} {
		t.Run(map[bool]string{false: "data", true: "node"}[node], func(t *testing.T) {
			var attempts, units atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if attempts.Add(1) < 3 {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				if node {
					_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":"0x2a"}`)
				} else {
					_, _ = io.WriteString(w, `{"balance":"0"}`)
				}
			}))
			defer server.Close()
			client, _ := NewClient("test", server.URL, WithRPCBaseURL(server.URL), WithCURateLimit(1000000),
				WithUsageObserver(func(u NoditUsage) { units.Add(u.CU) }))
			var err error
			want := int64(90)
			if node {
				_, err = client.LatestBlockNumber(context.Background(), "bsc", "")
				want = 30
			} else {
				_, err = client.NativeBalance(context.Background(), testWallet, "bsc", "")
			}
			if err != nil || attempts.Load() != 3 || units.Load() != want {
				t.Fatalf("error=%v attempts=%d estimatedCU=%d want=%d", err, attempts.Load(), units.Load(), want)
			}
		})
	}
}

func TestCancelledOrUnencodableRequestDoesNotConsumeAttemptBudget(t *testing.T) {
	var calls atomic.Int64
	client, _ := NewClient("test", "http://127.0.0.1", WithUsageObserver(func(NoditUsage) { calls.Add(1) }))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.NativeBalance(ctx, testWallet, "bsc", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	if _, err := client.post(context.Background(), profiles["bsc"], "native/getNativeBalanceByAccount", make(chan int)); err == nil {
		t.Fatal("accepted unencodable payload")
	}
	if calls.Load() != 0 {
		t.Fatalf("unsent requests recorded %d attempts", calls.Load())
	}
}

func TestRateLimitBackoffIsSharedWithWalletAndCollectorClones(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	client, _ := NewClient("test", server.URL, WithCURateLimit(1000000))
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := client.NativeBalance(ctx, testWallet, "bsc", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first request error=%v", err)
	}
	wallet := NewWalletReadClient(client)
	collector := client.boundedMarketClient()
	if wallet.cuLimiter != client.cuLimiter || collector.cuLimiter != client.cuLimiter {
		t.Fatal("clones lost shared limiter")
	}
	for _, clone := range []*Client{wallet.Client, collector} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, err := clone.NativeBalance(ctx, testWallet, "bsc", "")
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("queued clone error=%v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("shared backoff allowed %d physical attempts", calls.Load())
	}
}

func TestCURateLimiterCancelledWaitDoesNotExtendQueue(t *testing.T) {
	limiter := newCURateLimiter(100)
	limiter.next = time.Now().Add(time.Second)
	original := limiter.next
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := limiter.wait(ctx, 1000); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait error=%v", err)
	}
	if !limiter.next.Equal(original) {
		t.Fatal("cancelled waiter left a future reservation")
	}
}

func TestRetryDelaySupportsHTTPDateAndAvoidsDurationOverflow(t *testing.T) {
	now := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	if got := retryDelay(now.Add(3*time.Second).Format(http.TimeFormat), 0, now); got != 3*time.Second {
		t.Fatalf("HTTP date delay=%s", got)
	}
	if got := retryDelay("9223372036854775807", 0, now); got <= 0 {
		t.Fatalf("overflowed Retry-After into %s", got)
	}
}

func TestWalletReadLimitDoesNotMutatePrimaryClient(t *testing.T) {
	client, _ := NewClient("test", "http://127.0.0.1")
	wallet := NewWalletReadClient(client)
	if client.maxResponseBytes != 0 || wallet.maxResponseBytes != MaxWalletResponseBytes {
		t.Fatal("wallet limit mutated payment client")
	}
	// Exercise the same decoder used by the clone, with a small explicit cap,
	// rather than allocating a large fixture just to test the byte boundary.
	wallet.maxResponseBytes = 64
	wallet.httpClient = walletBudgetDoer(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"balance":"0"}` + strings.Repeat(" ", 100)))}, nil
	})
	if _, err := wallet.NativeBalance(context.Background(), testWallet, "bsc", ""); !errors.Is(err, ErrMarketRPCResponseTooLarge) {
		t.Fatalf("oversize wallet response error=%v", err)
	}
}

func TestWalletCloneDoesNotHideRedirectAttemptsOrMutatePaymentTransport(t *testing.T) {
	var redirected, units atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		_, _ = io.WriteString(w, `{"balance":"0"}`)
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
		// Even a JSON-shaped redirect body must not become a valid observation.
		_, _ = io.WriteString(w, `{"balance":"0"}`)
	}))
	defer server.Close()
	primary, _ := NewClient("test", server.URL, WithUsageObserver(func(u NoditUsage) { units.Add(u.CU) }))
	wallet := NewWalletReadClient(primary)
	if primary.httpClient.(*http.Client).CheckRedirect != nil {
		t.Fatal("wallet clone mutated payment redirect policy")
	}
	if _, err := wallet.NativeBalance(context.Background(), testWallet, "bsc", ""); err == nil || !strings.Contains(err.Error(), "error 307") {
		t.Fatalf("redirect observation error=%v", err)
	}
	if redirected.Load() != 0 || units.Load() != 30 {
		t.Fatalf("redirected=%d estimatedCU=%d", redirected.Load(), units.Load())
	}
}

type walletBudgetDoer func(*http.Request) (*http.Response, error)

func (do walletBudgetDoer) Do(r *http.Request) (*http.Response, error) { return do(r) }
