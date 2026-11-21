package marketquality

import (
	"testing"
	"time"
)

func TestQuoteEvidenceRequiresRealSuccessTimeAndPreservesHistoricalUse(t *testing.T) {
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	fetched := now.Add(-30 * time.Second)
	for _, status := range []string{StatusLive, StatusCached} {
		quote := Quote{Status: status, FetchedAt: &fetched}
		if !quote.AvailableWithin(now, time.Minute) || quote.AvailableWithin(now.Add(2*time.Minute), time.Minute) {
			t.Fatalf("incorrect quote age for %s", status)
		}
		if !quote.AvailableWithin(fetched.Add(time.Second), time.Minute) {
			t.Fatal("valid historical observation was rejected because the wall clock moved")
		}
	}
	for _, quote := range []Quote{{}, {Status: StatusLive}, {Status: StatusStaleFallback, FetchedAt: &fetched}} {
		if quote.AvailableWithin(now, time.Minute) {
			t.Fatalf("unknown/fallback evidence accepted: %+v", quote)
		}
	}
	future := now.Add(time.Second)
	if (Quote{Status: StatusLive, FetchedAt: &future}).AvailableWithin(now, time.Minute) {
		t.Fatal("future fetch accepted")
	}
	oldSource := fetched.Add(-time.Hour)
	if (Quote{Status: StatusLive, FetchedAt: &fetched, SourceTimestamp: &oldSource}).AvailableWithin(now, time.Minute) {
		t.Fatal("a recent API request washed an explicitly old upstream quote")
	}
}

func TestQuoteCloneDoesNotMutateSharedEvidence(t *testing.T) {
	now := time.Now().UTC()
	quote := Quote{Status: StatusLive, FetchedAt: &now, SourceTimestamp: &now}
	copy := quote.Clone()
	*copy.FetchedAt = now.Add(time.Hour)
	*copy.SourceTimestamp = now.Add(-time.Hour)
	if !quote.FetchedAt.Equal(now) || !quote.SourceTimestamp.Equal(now) {
		t.Fatal("quote clone shared mutable time pointers")
	}
}

func TestConfirmedTwoMinutePolicyNeverPermitsKnownFaultFallback(t *testing.T) {
	at := time.Date(2026, 11, 14, 11, 0, 0, 0, time.UTC)
	fetched := at.Add(-2 * time.Minute)
	quote := Quote{Status: StatusLive, FetchedAt: &fetched}
	if MaxAge != 2*time.Minute || !quote.AvailableAt(at) || quote.AvailableAt(at.Add(time.Nanosecond)) {
		t.Fatal("the confirmed two-minute maximum was not enforced")
	}
	quote.Status = StatusStaleFallback
	if quote.AvailableAt(fetched) || quote.DisplayStatus(at) != StatusStaleFallback {
		t.Fatal("known fallback was allowed during its former success age")
	}
}
