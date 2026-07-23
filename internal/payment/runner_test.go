package payment

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

type fakePaymentLock struct {
	unlocked bool
}

func (f *fakePaymentLock) Unlock(context.Context) error {
	f.unlocked = true
	return nil
}

func TestRunnerSkipsPaymentCycleWhenAnotherInstanceHoldsLock(t *testing.T) {
	repository := &fakeRepository{}
	service := testService(t, repository, &fakeBlockchain{}, Settings{})
	runner := NewRunner(
		service,
		func(context.Context) (Lock, bool, error) {
			return nil, false, nil
		},
		DefaultInterval,
	)

	runner.runCycle(context.Background(), paymentTestLogger())

	if repository.expireCalls != 0 {
		t.Fatalf("expire calls = %d, want 0", repository.expireCalls)
	}
}

func TestRunnerReconcilesAndUnlocks(t *testing.T) {
	repository := &fakeRepository{}
	service := testService(t, repository, &fakeBlockchain{}, Settings{})
	lock := &fakePaymentLock{}
	runner := NewRunner(
		service,
		func(context.Context) (Lock, bool, error) {
			return lock, true, nil
		},
		DefaultInterval,
	)

	runner.runCycle(context.Background(), paymentTestLogger())

	if repository.expireCalls != 1 {
		t.Fatalf("expire calls = %d, want 1", repository.expireCalls)
	}
	if !lock.unlocked {
		t.Fatal("payment reconciliation lock was not released")
	}
}

func paymentTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
