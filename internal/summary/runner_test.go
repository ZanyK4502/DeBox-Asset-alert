package summary

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

func TestRunnerRunsOneSummaryCycle(t *testing.T) {
	repository := &fakeRepository{}
	executor := newTestExecutor(repository, &fakeNotifier{}, acquiredLock(nil))
	runner := NewRunner(executor, DefaultInterval)

	runner.runCycle(
		context.Background(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	if len(repository.listAfterIDs) != 1 || repository.listAfterIDs[0] != 0 {
		t.Fatalf("list calls = %v", repository.listAfterIDs)
	}
}
