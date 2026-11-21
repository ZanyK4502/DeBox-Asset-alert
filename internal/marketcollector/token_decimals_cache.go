package marketcollector

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"
)

const (
	tokenDecimalsCacheTTL         = 5 * time.Minute
	tokenDecimalsCacheMaxKeys     = 4096
	tokenDecimalsCacheMaxBytes    = 1 << 20 // Estimated retained payload, not process RSS.
	tokenDecimalsCacheMaxInFlight = 8
)

var (
	errTokenDecimalsReadCapacity = errors.New("token decimals read capacity busy")
	errTokenDecimalsInterrupted  = errors.New("token decimals read interrupted")
)

type tokenDecimalsSource struct {
	reader   any // Retain the actual wrapper, including its immutable source configuration.
	identity any // Optional underlying RPC identity; never substitute it for the wrapper.
}

type tokenDecimalsKey struct {
	chainID  int64
	chainKey string
	token    string
}

type tokenDecimalsEntry struct {
	value     int
	fetchedAt time.Time
	bytes     int
}

type tokenDecimalsFlightKey struct {
	generation uint64
	key        tokenDecimalsKey
}

type tokenDecimalsRead struct {
	done  chan struct{}
	value int
	err   error
}

type tokenDecimalsCache struct {
	mu         sync.Mutex
	source     tokenDecimalsSource
	generation uint64
	observedAt time.Time
	entries    map[tokenDecimalsKey]tokenDecimalsEntry
	bytes      int
	inflight   map[tokenDecimalsFlightKey]*tokenDecimalsRead
}

func tokenDecimalsPointer(value any) bool {
	if value == nil {
		return false
	}
	v := reflect.ValueOf(value)
	return v.Kind() == reflect.Pointer && !v.IsNil()
}

func tokenDecimalsReaderNil(reader ChainClient) bool {
	return reader == nil || reflect.ValueOf(reader).Kind() == reflect.Pointer && reflect.ValueOf(reader).IsNil()
}

func tokenDecimalsCacheSource(reader ChainClient) (tokenDecimalsSource, bool) {
	if !tokenDecimalsPointer(reader) {
		return tokenDecimalsSource{}, false
	}
	identity := any(reader)
	if provider, ok := reader.(interface{ PoolFactsCacheIdentity() any }); ok {
		identity = provider.PoolFactsCacheIdentity()
	}
	if !tokenDecimalsPointer(identity) {
		return tokenDecimalsSource{}, false
	}
	return tokenDecimalsSource{reader: reader, identity: identity}, true
}

func readCheckedTokenDecimals(ctx context.Context, read func(context.Context) (int32, error)) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	value, err := read(ctx)
	if err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if value < 0 || value > 255 {
		return 0, fmt.Errorf("invalid token decimals")
	}
	return int(value), nil
}

// read coalesces only readers with a trustworthy pointer identity. Leaders run
// in the caller's goroutine and context; cancelling a follower never cancels it.
func (cache *tokenDecimalsCache) read(ctx context.Context, source tokenDecimalsSource, key tokenDecimalsKey, now func() time.Time, read func(context.Context) (int32, error)) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	cache.mu.Lock()
	at := now()
	cache.observeTimeLocked(at)
	if cache.source != source {
		cache.source = source
		cache.generation++
		cache.entries = nil
		cache.bytes = 0
		// Old requests still occupy the global in-flight ceiling until they
		// finish. A source change must not manufacture eight more slots.
	}
	if entry, ok := cache.entries[key]; ok {
		age := at.Sub(entry.fetchedAt)
		if age >= 0 && age < tokenDecimalsCacheTTL {
			cache.mu.Unlock()
			return entry.value, nil
		}
		cache.bytes -= entry.bytes
		delete(cache.entries, key)
	}
	flightKey := tokenDecimalsFlightKey{generation: cache.generation, key: key}
	if pending := cache.inflight[flightKey]; pending != nil {
		cache.mu.Unlock()
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-pending.done:
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			return pending.value, pending.err
		}
	}
	if len(cache.inflight) >= tokenDecimalsCacheMaxInFlight {
		cache.mu.Unlock()
		return 0, errTokenDecimalsReadCapacity
	}
	if cache.inflight == nil {
		cache.inflight = make(map[tokenDecimalsFlightKey]*tokenDecimalsRead)
	}
	pending := &tokenDecimalsRead{done: make(chan struct{}), err: errTokenDecimalsInterrupted}
	cache.inflight[flightKey] = pending
	cache.mu.Unlock()
	// A recovered transport panic must not strand followers or a capacity slot.
	defer func() {
		cache.mu.Lock()
		delete(cache.inflight, flightKey)
		close(pending.done)
		cache.mu.Unlock()
	}()
	started := now()
	pending.value, pending.err = readCheckedTokenDecimals(ctx, read)
	if pending.err == nil {
		cache.mu.Lock()
		finished := now()
		cache.observeTimeLocked(finished)
		if cache.generation == flightKey.generation && cache.source == source &&
			!finished.Before(started) && finished.Sub(started) < tokenDecimalsCacheTTL {
			cache.putLocked(key, pending.value, started)
		}
		cache.mu.Unlock()
	}
	return pending.value, pending.err
}

func (cache *tokenDecimalsCache) observeTimeLocked(at time.Time) {
	if !cache.observedAt.IsZero() && at.Before(cache.observedAt) {
		// Even a rollback still later than fetchedAt could otherwise extend
		// reuse. Also fence reads that began before the observed rollback.
		cache.generation++
		cache.entries = nil
		cache.bytes = 0
	}
	cache.observedAt = at
}

func (cache *tokenDecimalsCache) putLocked(key tokenDecimalsKey, value int, started time.Time) {
	bytes := len(key.chainKey) + len(key.token) + 160
	if bytes > tokenDecimalsCacheMaxBytes {
		return
	}
	if previous, ok := cache.entries[key]; ok {
		cache.bytes -= previous.bytes
		delete(cache.entries, key)
	}
	for len(cache.entries) >= tokenDecimalsCacheMaxKeys || cache.bytes+bytes > tokenDecimalsCacheMaxBytes {
		var oldestKey tokenDecimalsKey
		var oldestAt time.Time
		found := false
		for oldKey, entry := range cache.entries {
			if !found || entry.fetchedAt.Before(oldestAt) {
				oldestKey, oldestAt, found = oldKey, entry.fetchedAt, true
			}
		}
		if !found {
			return
		}
		cache.bytes -= cache.entries[oldestKey].bytes
		delete(cache.entries, oldestKey)
	}
	if cache.entries == nil {
		cache.entries = make(map[tokenDecimalsKey]tokenDecimalsEntry)
	}
	cache.entries[key] = tokenDecimalsEntry{value: value, fetchedAt: started, bytes: bytes}
	cache.bytes += bytes
}
