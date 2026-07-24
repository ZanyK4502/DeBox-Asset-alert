package monitor

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

type fakeLock struct {
	unlocked bool
}

func (f *fakeLock) Unlock(context.Context) error {
	f.unlocked = true
	return nil
}

func TestRunnerSkipsCycleWhenAnotherInstanceHoldsLock(t *testing.T) {
	repository := &fakeRepository{}
	executor := newTestExecutor(t, repository, &fakeChain{}, &fakeNotifier{})
	runner := NewRunner(
		executor,
		func(context.Context) (Lock, bool, error) {
			return nil, false, nil
		},
		DefaultInterval,
	)

	runner.runCycle(context.Background(), discardLogger())

	if len(repository.calls) != 0 {
		t.Fatalf("repository calls = %v, want none", repository.calls)
	}
}

func TestRunnerUnlocksAfterCompletedCycle(t *testing.T) {
	repository := &fakeRepository{}
	executor := newTestExecutor(t, repository, &fakeChain{}, &fakeNotifier{})
	lock := &fakeLock{}
	runner := NewRunner(
		executor,
		func(context.Context) (Lock, bool, error) {
			return lock, true, nil
		},
		DefaultInterval,
	)

	runner.runCycle(context.Background(), discardLogger())

	if !lock.unlocked {
		t.Fatal("monitor lock was not released")
	}
	if len(repository.calls) != 2 ||
		repository.calls[0] != "list" ||
		repository.calls[1] != "cleanup" {
		t.Fatalf("repository calls = %v, want [list cleanup]", repository.calls)
	}
}

func TestRunnerCleansAggregationHistoryOnlyOncePerInterval(t *testing.T) {
	repository := &fakeRepository{}
	executor := newTestExecutor(t, repository, &fakeChain{}, &fakeNotifier{})
	runner := NewRunner(
		executor,
		func(context.Context) (Lock, bool, error) {
			return &fakeLock{}, true, nil
		},
		DefaultInterval,
	)

	runner.runCycle(context.Background(), discardLogger())
	runner.runCycle(context.Background(), discardLogger())

	want := []string{"list", "cleanup", "list"}
	if len(repository.calls) != len(want) {
		t.Fatalf("repository calls = %v, want %v", repository.calls, want)
	}
	for index := range want {
		if repository.calls[index] != want[index] {
			t.Fatalf("repository calls = %v, want %v", repository.calls, want)
		}
	}
}

func TestCheckRuleSkipsImmediateCheckWhenMonitorLockIsHeld(t *testing.T) {
	repository := &fakeRepository{}
	chainService := &fakeChain{}
	executor := newTestExecutor(t, repository, chainService, &fakeNotifier{})
	executor.deps.TryExecutionLock = func(context.Context) (Lock, bool, error) {
		return nil, false, nil
	}

	value, err := executor.CheckRule(
		context.Background(),
		testRule("balance_threshold", nil, "standard"),
		"standard",
	)

	if err != nil {
		t.Fatalf("CheckRule() error = %v", err)
	}
	result := value.(RuleResult)
	if result.Status != "locked" {
		t.Fatalf("status = %q, want locked", result.Status)
	}
	if len(chainService.calls) != 0 {
		t.Fatalf("chain calls = %v, want none", chainService.calls)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
