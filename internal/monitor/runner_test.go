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
	if len(repository.calls) != 1 || repository.calls[0] != "list" {
		t.Fatalf("repository calls = %v, want [list]", repository.calls)
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
