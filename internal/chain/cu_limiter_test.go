package chain

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestCURateLimiterHonorsContextWhileQueued(t *testing.T) {
	client, err := NewClient("nodit-key", "", WithCURateLimit(400))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if client.cuLimiter == nil || client.cuLimiter.unitsPerSecond != 400 {
		t.Fatalf("unexpected CU limiter: %#v", client.cuLimiter)
	}
	client.cuLimiter.next = time.Now().Add(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.waitForCU(ctx, NoditUsage{CU: 66}); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForCU() error = %v, want context.Canceled", err)
	}
}

func TestCURateLimiterCanBeDisabled(t *testing.T) {
	client, err := NewClient("nodit-key", "", WithCURateLimit(0))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if client.cuLimiter != nil {
		t.Fatalf("CU limiter = %#v, want nil", client.cuLimiter)
	}
	if err := client.waitForCU(context.Background(), NoditUsage{CU: 66}); err != nil {
		t.Fatalf("waitForCU() error = %v", err)
	}
}

func TestHolderPacingDoesNotReserveSharedCapacityWhileWaiting(t *testing.T) {
	client, err := NewClient("nodit-key", "", WithCURateLimit(250), WithHolderCURateLimit(75))
	if err != nil {
		t.Fatal(err)
	}
	holder := NoditUsage{Operation: tokenHoldersOperation, CU: 150}
	if err := client.waitForCU(context.Background(), holder); err != nil {
		t.Fatal(err)
	}
	limiter := client.cuLimiter
	if spacing := limiter.nextHolder.Sub(limiter.next); spacing != 1400*time.Millisecond {
		t.Fatalf("holder waits %s after shared slot, want 1.4s", spacing)
	}
	// A shared slot has opened, while the next holder slot is still pending.
	limiter.next = time.Time{}
	holderStart := limiter.nextHolder
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := client.waitForCU(ctx, holder); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued holder error = %v, want deadline exceeded", err)
	}
	if !limiter.next.IsZero() || !limiter.nextHolder.Equal(holderStart) {
		t.Fatal("cancelled holder advanced an admission clock")
	}
	scanCtx, cancelScan := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelScan()
	if err := client.waitForCU(scanCtx, NoditUsage{Operation: "eth_getLogs", CU: 66}); err != nil {
		t.Fatalf("scanner blocked by holder schedule: %v", err)
	}
	if !limiter.nextHolder.Equal(holderStart) {
		t.Fatal("normal request changed holder admission clock")
	}
}

func TestHolderPacingDelaysPhysicalRetries(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client, err := NewClient("test", server.URL, WithCURateLimit(1000), WithHolderCURateLimit(75))
	if err != nil {
		t.Fatal(err)
	}
	// The ordinary retry would fit in this deadline. The holder retry is two seconds apart.
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	_, err = client.TokenHoldersByContract(ctx, testWallet, "bsc", "", TokenHoldersOptions{WithCount: true})
	if !errors.Is(err, context.DeadlineExceeded) || attempts.Load() != 1 {
		t.Fatalf("holder request error = %v, physical attempts = %d, want deadline after one attempt", err, attempts.Load())
	}
}
