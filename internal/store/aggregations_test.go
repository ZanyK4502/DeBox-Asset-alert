package store

import (
	"testing"
	"time"
)

func TestStageWindowBounds(t *testing.T) {
	t.Parallel()

	anchor := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 24, 10, 37, 30, 0, time.UTC)

	fixedStart, fixedEnd := stageWindowBounds(stageRuleConfiguration{
		CycleType:           "fixed",
		CycleMinutes:        15,
		AggregationAnchorAt: anchor,
	}, now)
	if !fixedStart.Equal(time.Date(2026, 7, 24, 10, 30, 0, 0, time.UTC)) ||
		!fixedEnd.Equal(time.Date(2026, 7, 24, 10, 45, 0, 0, time.UTC)) {
		t.Fatalf("fixed window = %s - %s", fixedStart, fixedEnd)
	}

	followStart, followEnd := stageWindowBounds(stageRuleConfiguration{
		CycleType:    "follow",
		CycleMinutes: 15,
	}, now)
	if !followStart.Equal(now) ||
		!followEnd.Equal(time.Date(2026, 7, 24, 10, 52, 30, 0, time.UTC)) {
		t.Fatalf("follow window = %s - %s", followStart, followEnd)
	}
}
