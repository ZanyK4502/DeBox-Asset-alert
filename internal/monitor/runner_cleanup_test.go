package monitor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type failedCleanupRepository struct {
	*fakeRepository
	cleanupCalls int
	fail         bool
}

func (f *failedCleanupRepository) CleanupAggregationHistory(context.Context) (store.AggregationCleanupResult, error) {
	f.cleanupCalls++
	if f.fail {
		return store.AggregationCleanupResult{}, errors.New("bounded cleanup unavailable")
	}
	return store.AggregationCleanupResult{}, nil
}

func TestRunnerCleanupFailureBackoffDoesNotStopMonitoring(t *testing.T) {
	base := &fakeRepository{}
	repository := &failedCleanupRepository{fakeRepository: base, fail: true}
	executor := newTestExecutor(t, base, &fakeChain{}, &fakeNotifier{})
	executor.deps.Repository = repository
	runner := NewRunner(executor, func(context.Context) (Lock, bool, error) {
		return &fakeLock{}, true, nil
	}, DefaultInterval)
	runner.runCycle(context.Background(), discardLogger())
	if repository.cleanupCalls != 1 || !runner.lastCleanupAt.IsZero() || runner.nextCleanupAttemptAt.Before(time.Now().Add(59*time.Second)) {
		t.Fatalf("failure retry gate missing: calls=%d success=%s retry=%s", repository.cleanupCalls, runner.lastCleanupAt, runner.nextCleanupAttemptAt)
	}
	runner.runCycle(context.Background(), discardLogger())
	if repository.cleanupCalls != 1 || len(base.calls) != 3 || base.calls[1] != "list" || base.calls[2] != "list" {
		t.Fatalf("cleanup retried early or detection stopped: cleanup=%d calls=%v", repository.cleanupCalls, base.calls)
	}
	runner.nextCleanupAttemptAt = time.Now().Add(-time.Second)
	repository.fail = false
	runner.runCycle(context.Background(), discardLogger())
	runner.runCycle(context.Background(), discardLogger())
	if repository.cleanupCalls != 2 || runner.lastCleanupAt.IsZero() || !runner.nextCleanupAttemptAt.IsZero() || len(base.calls) != 5 {
		t.Fatalf("success did not restore daily interval: cleanup=%d calls=%v", repository.cleanupCalls, base.calls)
	}
}
