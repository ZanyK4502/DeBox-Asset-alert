// Package marketquality carries quote provenance across providers, persistence
// and consumers. It has no network, database or mutable cache dependencies.
package marketquality

import "time"

const (
	StatusLive          = "live"
	StatusCached        = "cached"
	StatusStaleFallback = "stale_fallback"
	StatusUnknown       = "unknown"
	StatusStale         = "stale"
)

// MaxAge is the confirmed business limit. Known fault fallback is unavailable
// immediately, even while the original response is younger than this limit.
// No consumer may invent a timestamp for data with unknown provenance.
const MaxAge time.Duration = 2 * time.Minute

type Quote struct {
	Status          string     `json:"status"`
	FetchedAt       *time.Time `json:"fetched_at,omitempty"`
	SourceTimestamp *time.Time `json:"source_timestamp,omitempty"`
}

// ObservationTime reports an upstream quote time only when it really exists;
// otherwise the successful API response time is an explicit approximation.
// FetchedAt and SourceTimestamp stay separate in the serialized evidence.
func (quote Quote) ObservationTime() *time.Time {
	if quote.SourceTimestamp != nil && !quote.SourceTimestamp.IsZero() {
		return quote.SourceTimestamp
	}
	if quote.FetchedAt == nil || quote.FetchedAt.IsZero() {
		return nil
	}
	return quote.FetchedAt
}

func (quote Quote) AvailableAt(at time.Time) bool {
	return quote.AvailableWithin(at, MaxAge)
}

// AvailableWithin is pure and also supports tests without a process clock.
// Historical observations must be checked at their use/valuation time, rather
// than treating all retained market history as stale simply because it is old.
func (quote Quote) AvailableWithin(at time.Time, maxAge time.Duration) bool {
	if maxAge <= 0 || at.IsZero() || quote.Status != StatusLive && quote.Status != StatusCached ||
		quote.FetchedAt == nil || quote.FetchedAt.IsZero() || at.Before(*quote.FetchedAt) {
		return false
	}
	observed := quote.ObservationTime()
	return observed != nil && !at.Before(*observed) && at.Sub(*quote.FetchedAt) <= maxAge && at.Sub(*observed) <= maxAge
}

func (quote Quote) DisplayStatus(at time.Time) string {
	if quote.Status == StatusStaleFallback {
		return StatusStaleFallback
	}
	if quote.Status != StatusLive && quote.Status != StatusCached || quote.ObservationTime() == nil {
		return StatusUnknown
	}
	if !quote.AvailableAt(at) {
		return StatusStale
	}
	return quote.Status
}

func (quote Quote) Clone() Quote {
	result := quote
	if quote.FetchedAt != nil {
		value := *quote.FetchedAt
		result.FetchedAt = &value
	}
	if quote.SourceTimestamp != nil {
		value := *quote.SourceTimestamp
		result.SourceTimestamp = &value
	}
	return result
}
