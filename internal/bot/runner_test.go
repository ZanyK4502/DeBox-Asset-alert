package bot

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	boxbotapi "github.com/debox-pro/debox-chat-go-sdk/boxbotapi"
)

type fakeLock struct {
	mu       sync.Mutex
	unlocked bool
}

func (f *fakeLock) Unlock(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unlocked = true
	return nil
}

func (f *fakeLock) wasUnlocked() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unlocked
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRunnerDoesNotPollOutsidePollingMode(t *testing.T) {
	service, client, _, _ := newTestService(t)
	runner := NewRunner(service, client, "webhook", func(context.Context) (Lock, bool, error) {
		t.Fatal("lock must not be requested outside polling mode")
		return nil, false, nil
	})
	runner.Run(context.Background(), testLogger())
	if len(client.updateCalls) != 0 {
		t.Fatalf("GetUpdates calls = %d, want 0", len(client.updateCalls))
	}
}

func TestRunnerAdvancesOffsetAndReleasesSingletonLock(t *testing.T) {
	service, client, _, _ := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	client.updates = [][]boxbotapi.Update{{
		{Id: 7, Message: testMessage("/start", "private", "user-id")},
	}}
	client.onUpdate = func(call int) {
		if call == 1 {
			cancel()
		}
	}
	lock := &fakeLock{}
	runner := NewRunner(service, client, "polling", func(context.Context) (Lock, bool, error) {
		return lock, true, nil
	})
	runner.retryDelay = time.Millisecond
	runner.healthLogRate = time.Hour
	runner.Run(ctx, testLogger())

	client.mu.Lock()
	updateCalls := append([]boxbotapi.UpdateConfig(nil), client.updateCalls...)
	client.mu.Unlock()
	if len(updateCalls) < 2 {
		t.Fatalf("GetUpdates calls = %d, want at least 2", len(updateCalls))
	}
	if updateCalls[0].Offset != 0 || updateCalls[1].Offset != 8 {
		t.Fatalf("polling offsets = %d, %d; want 0, 8", updateCalls[0].Offset, updateCalls[1].Offset)
	}
	if len(client.sentConfigs()) != 1 {
		t.Fatalf("handled messages = %d, want 1", len(client.sentConfigs()))
	}
	if !lock.wasUnlocked() {
		t.Fatal("polling lock was not released")
	}
}

func TestRunnerWaitsForSingletonLockUntilCancelled(t *testing.T) {
	service, client, _, _ := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	runner := NewRunner(service, client, "polling", func(context.Context) (Lock, bool, error) {
		attempts++
		if attempts == 2 {
			cancel()
		}
		return nil, false, nil
	})
	runner.retryDelay = time.Millisecond
	runner.Run(ctx, testLogger())
	if attempts != 2 {
		t.Fatalf("lock attempts = %d, want 2", attempts)
	}
	if len(client.updateCalls) != 0 {
		t.Fatal("runner polled without the singleton lock")
	}
}
