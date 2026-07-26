package chain

import (
	"context"
	"errors"
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
	if err := client.waitForCU(ctx, 66); !errors.Is(err, context.Canceled) {
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
	if err := client.waitForCU(context.Background(), 66); err != nil {
		t.Fatalf("waitForCU() error = %v", err)
	}
}
