package marketcollector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
)

type decimalsReuseClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *decimalsReuseClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}
func (c *decimalsReuseClock) advance(delta time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(delta)
	c.mu.Unlock()
}

type decimalsReuseReader struct {
	*fakeChain
	mu       sync.Mutex
	identity *int
	value    int32
	err      error
	panicFor any
	calls    int
	active   int
	entered  chan string
	release  <-chan struct{}
}

func (r *decimalsReuseReader) PoolFactsCacheIdentity() any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.identity
}
func (r *decimalsReuseReader) TokenDecimals(ctx context.Context, token, _, _ string) (int32, error) {
	r.mu.Lock()
	r.calls++
	r.active++
	value, err, entered, release, panicFor := r.value, r.err, r.entered, r.release, r.panicFor
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.active--
		r.mu.Unlock()
	}()
	if entered != nil {
		select {
		case entered <- token:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	if panicFor != nil {
		panic(panicFor)
	}
	return value, err
}
func (r *decimalsReuseReader) set(value int32, err error) {
	r.mu.Lock()
	r.value, r.err = value, err
	r.mu.Unlock()
}
func (r *decimalsReuseReader) stats() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, r.active
}

func decimalsReuseFixture() (*Service, *decimalsReuseReader, *decimalsReuseClock) {
	clock := &decimalsReuseClock{at: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
	reader := &decimalsReuseReader{fakeChain: &fakeChain{}, identity: new(int), value: 6}
	service := New(Dependencies{Chain: reader, Now: clock.now}, Settings{ChainKey: "bsc", ChainID: 56})
	return service, reader, clock
}

func decimalsReuseToken(index int) string { return fmt.Sprintf("0x%040x", index+10000) }

func requireDecimalsReuse(t *testing.T, service *Service, token string, want int) {
	t.Helper()
	if got, err := service.resolveTokenDecimals(context.Background(), token, nil); err != nil || got != want {
		t.Fatalf("resolve %s: value=%d want=%d error=%v", token, got, want, err)
	}
}

func TestTokenDecimalsReuseExactExpiryIsNotSliding(t *testing.T) {
	service, reader, clock := decimalsReuseFixture()
	token := decimalsReuseToken(1)
	requireDecimalsReuse(t, service, token, 6)
	clock.advance(5*time.Minute - time.Nanosecond)
	reader.set(8, nil)
	requireDecimalsReuse(t, service, token, 6)
	clock.advance(time.Nanosecond)
	requireDecimalsReuse(t, service, token, 8)
	if calls, _ := reader.stats(); calls != 2 {
		t.Fatalf("exact original five-minute expiry was extended by a hit: calls=%d", calls)
	}
}

func TestTokenDecimalsReuseKnownNativeAndRealZero(t *testing.T) {
	service, reader, _ := decimalsReuseFixture()
	token := decimalsReuseToken(2)
	reader.set(0, nil)
	requireDecimalsReuse(t, service, token, 0)
	requireDecimalsReuse(t, service, token, 0)
	known := map[string]int{token: 18}
	if got, err := service.resolveTokenDecimals(context.Background(), token, known); err != nil || got != 18 {
		t.Fatalf("cached zero overrode verified project precision: %d %v", got, err)
	}
	for _, invalid := range []int{-1, 256} {
		if _, err := service.resolveTokenDecimals(context.Background(), token, map[string]int{token: invalid}); err == nil {
			t.Fatal("invalid known precision fell back to cache", invalid)
		}
	}
	requireDecimalsReuse(t, service, marketparse.NativeTokenAddress, 18)
	if calls, _ := reader.stats(); calls != 1 {
		t.Fatalf("zero/native/known path made extra reads: %d", calls)
	}
}

func TestTokenDecimalsReuseFailureCannotReturnExpiredValueOrPoisonKnown(t *testing.T) {
	service, reader, clock := decimalsReuseFixture()
	token := decimalsReuseToken(3)
	requireDecimalsReuse(t, service, token, 6)
	clock.advance(5 * time.Minute)
	reader.set(0, errors.New("bounded source unavailable"))
	for range 2 {
		known := map[string]int{}
		if _, err := service.resolveTokenDecimals(context.Background(), token, known); err == nil {
			t.Fatal("source failure returned expired precision")
		}
		if _, exists := known[token]; exists {
			t.Fatal("source failure entered task-known precision")
		}
	}
	reader.set(8, nil)
	requireDecimalsReuse(t, service, token, 8)
	if calls, _ := reader.stats(); calls != 4 {
		t.Fatalf("errors were cached or old value rescued them: %d", calls)
	}
	for _, invalid := range []int32{-1, 256} {
		other := decimalsReuseToken(int(invalid) + 1000)
		reader.set(invalid, nil)
		before, _ := reader.stats()
		for range 2 {
			if _, err := service.resolveTokenDecimals(context.Background(), other, nil); err == nil {
				t.Fatal("invalid RPC precision was cached", invalid)
			}
		}
		if calls, _ := reader.stats(); calls != before+2 {
			t.Fatal("invalid RPC precision was retained across tasks", invalid)
		}
	}
}

func TestTokenDecimalsReuseClockRollbackForcesFreshRead(t *testing.T) {
	service, reader, clock := decimalsReuseFixture()
	token := decimalsReuseToken(4)
	requireDecimalsReuse(t, service, token, 6)
	clock.advance(2 * time.Minute)
	requireDecimalsReuse(t, service, token, 6)
	clock.advance(-time.Minute) // Still after fetchedAt, but before the last observed hit.
	reader.set(8, nil)
	requireDecimalsReuse(t, service, token, 8)
	clock.advance(-2 * time.Minute)
	reader.set(18, nil)
	requireDecimalsReuse(t, service, token, 18)
	if calls, _ := reader.stats(); calls != 3 {
		t.Fatalf("future-dated cache survived rollback: %d", calls)
	}
}

type decimalsReuseResult struct {
	value int
	err   error
}

func awaitDecimalsReuse[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	timer := time.NewTimer(3 * time.Second) // Failure deadline only; no timing-based synchronization.
	defer timer.Stop()
	select {
	case result := <-channel:
		return result
	case <-timer.C:
		t.Fatal("bounded decimals fixture did not finish")
		var zero T
		return zero
	}
}

// Observe entry into the caller's wait select without sleeps or relying on a
// goroutine having been scheduled before the leader is released.
type decimalsReuseWaitContext struct {
	context.Context
	once    sync.Once
	entered chan struct{}
}

func (c *decimalsReuseWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestTokenDecimalsReuseMergesSameKeyAndCancelsOnlyWaiter(t *testing.T) {
	service, reader, _ := decimalsReuseFixture()
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()
	reader.release, reader.entered = release, make(chan string, 1)
	token := decimalsReuseToken(5)
	leader := make(chan decimalsReuseResult, 1)
	go func() {
		value, err := service.resolveTokenDecimals(context.Background(), token, nil)
		leader <- decimalsReuseResult{value, err}
	}()
	awaitDecimalsReuse(t, reader.entered)
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	waitCtx := &decimalsReuseWaitContext{Context: base, entered: make(chan struct{})}
	waiter := make(chan decimalsReuseResult, 1)
	go func() {
		value, err := service.resolveTokenDecimals(waitCtx, token, nil)
		waiter <- decimalsReuseResult{value, err}
	}()
	awaitDecimalsReuse(t, waitCtx.entered)
	cancel()
	if result := awaitDecimalsReuse(t, waiter); !errors.Is(result.err, context.Canceled) {
		t.Fatalf("waiter cancellation was ignored: %+v", result)
	}
	if calls, active := reader.stats(); calls != 1 || active != 1 {
		t.Fatalf("waiter duplicated/cancelled leader: calls=%d active=%d", calls, active)
	}
	secondCtx := &decimalsReuseWaitContext{Context: context.Background(), entered: make(chan struct{})}
	second := make(chan decimalsReuseResult, 1)
	go func() {
		value, err := service.resolveTokenDecimals(secondCtx, token, nil)
		second <- decimalsReuseResult{value, err}
	}()
	awaitDecimalsReuse(t, secondCtx.entered)
	releaseAll()
	for _, done := range []chan decimalsReuseResult{leader, second} {
		if result := awaitDecimalsReuse(t, done); result.err != nil || result.value != 6 {
			t.Fatalf("merged read failed: %+v", result)
		}
	}
	if calls, active := reader.stats(); calls != 1 || active != 0 {
		t.Fatalf("completed callers leaked RPC work: calls=%d active=%d", calls, active)
	}
}

func TestTokenDecimalsReuseDoesNotCacheRPCPastOriginalTTL(t *testing.T) {
	service, reader, clock := decimalsReuseFixture()
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	defer finish()
	reader.release, reader.entered = release, make(chan string, 1)
	token := decimalsReuseToken(6)
	result := make(chan decimalsReuseResult, 1)
	go func() {
		value, err := service.resolveTokenDecimals(context.Background(), token, nil)
		result <- decimalsReuseResult{value, err}
	}()
	awaitDecimalsReuse(t, reader.entered)
	clock.advance(5 * time.Minute)
	finish()
	if got := awaitDecimalsReuse(t, result); got.err != nil || got.value != 6 {
		t.Fatalf("original read result changed: %+v", got)
	}
	reader.set(8, nil)
	requireDecimalsReuse(t, service, token, 8)
	if calls, _ := reader.stats(); calls != 2 {
		t.Fatalf("five-minute old in-flight result entered cache: %d", calls)
	}
}

func TestTokenDecimalsReuseSeparatesCanonicalChains(t *testing.T) {
	service, reader, _ := decimalsReuseFixture()
	token := decimalsReuseToken(7)
	requireDecimalsReuse(t, service, token, 6)
	reader.set(8, nil)
	// These synchronous test-only switches have no in-flight service access.
	service.settings.ChainKey = "bnb"
	requireDecimalsReuse(t, service, token, 6)
	service.settings.ChainKey, service.settings.ChainID = "ethereum", 1
	requireDecimalsReuse(t, service, token, 8)
	if calls, _ := reader.stats(); calls != 2 {
		t.Fatalf("chain alias split or another chain borrowed decimals: %d", calls)
	}
}

func TestTokenDecimalsReuseOldFlightCannotPoisonReplacementSource(t *testing.T) {
	service, reader, _ := decimalsReuseFixture()
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	defer finish()
	reader.release, reader.entered = release, make(chan string, 1)
	token := decimalsReuseToken(8)
	old := make(chan decimalsReuseResult, 1)
	go func() {
		value, err := service.resolveTokenDecimals(context.Background(), token, nil)
		old <- decimalsReuseResult{value, err}
	}()
	awaitDecimalsReuse(t, reader.entered)
	// The wrapper stays immutable; its synchronized source getter models a
	// replacement actual RPC client. No Service.chain field is raced.
	reader.mu.Lock()
	reader.identity, reader.value, reader.release, reader.entered = new(int), 8, nil, nil
	reader.mu.Unlock()
	requireDecimalsReuse(t, service, token, 8)
	finish()
	if result := awaitDecimalsReuse(t, old); result.err == nil && result.value != 6 {
		t.Fatalf("old caller borrowed the new source value: %+v", result)
	}
	requireDecimalsReuse(t, service, token, 8)
	if calls, active := reader.stats(); calls != 2 || active != 0 {
		t.Fatalf("old flight poisoned new source or leaked: calls=%d active=%d", calls, active)
	}
}

func TestTokenDecimalsReuseNilReaderFailsWithoutCaching(t *testing.T) {
	var reader *decimalsReuseReader
	service := New(Dependencies{Chain: reader}, Settings{ChainKey: "bsc", ChainID: 56})
	token := decimalsReuseToken(9)
	if got, err := service.resolveTokenDecimals(context.Background(), token, map[string]int{token: 0}); err != nil || got != 0 {
		t.Fatalf("known project precision incorrectly required an RPC reader: %d %v", got, err)
	}
	requireDecimalsReuse(t, service, marketparse.NativeTokenAddress, 18)
	if _, err := service.resolveTokenDecimals(context.Background(), token, nil); err == nil {
		t.Fatal("typed-nil reader returned an invented precision")
	}
}

func TestTokenDecimalsReuseUnverifiableIdentityCannotBorrowOrRetainCache(t *testing.T) {
	service, reader, _ := decimalsReuseFixture()
	token := decimalsReuseToken(11)
	requireDecimalsReuse(t, service, token, 6)
	reader.mu.Lock()
	reader.identity, reader.value = nil, 8
	reader.mu.Unlock()
	before, _ := reader.stats()
	for range 2 {
		value, err := service.resolveTokenDecimals(context.Background(), token, nil)
		if err == nil && value != 8 {
			t.Fatalf("unverifiable replacement borrowed old precision: %d", value)
		}
	}
	if calls, _ := reader.stats(); calls == before+1 {
		t.Fatal("unverifiable identity retained a successful result across tasks")
	}
}

func TestTokenDecimalsReuseInflightLimitSurvivesSourceChangeAndCancellation(t *testing.T) {
	service, reader, _ := decimalsReuseFixture()
	reader.entered = make(chan string, tokenDecimalsCacheMaxInFlight)
	release := make(chan struct{})
	reader.release = release
	var once sync.Once
	defer once.Do(func() { close(release) })
	results := make(chan decimalsReuseResult, tokenDecimalsCacheMaxInFlight)
	var cancels []context.CancelFunc
	for index := range tokenDecimalsCacheMaxInFlight {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		go func(index int) {
			value, err := service.resolveTokenDecimals(ctx, decimalsReuseToken(100+index), nil)
			results <- decimalsReuseResult{value, err}
		}(index)
	}
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()
	for range tokenDecimalsCacheMaxInFlight {
		awaitDecimalsReuse(t, reader.entered)
	}
	reader.mu.Lock()
	reader.identity, reader.value, reader.release, reader.entered = new(int), 8, nil, nil
	reader.mu.Unlock()
	if _, err := service.resolveTokenDecimals(context.Background(), decimalsReuseToken(200), nil); err == nil {
		t.Fatal("source switch discarded live-flight capacity accounting")
	}
	if calls, active := reader.stats(); calls != tokenDecimalsCacheMaxInFlight || active != tokenDecimalsCacheMaxInFlight {
		t.Fatalf("over-capacity read reached source: calls=%d active=%d", calls, active)
	}
	for _, cancel := range cancels {
		cancel()
	}
	for range tokenDecimalsCacheMaxInFlight {
		if result := awaitDecimalsReuse(t, results); !errors.Is(result.err, context.Canceled) {
			t.Fatalf("cancelled leader returned precision: %+v", result)
		}
	}
	service.tokenDecimals.mu.Lock()
	flights := len(service.tokenDecimals.inflight)
	service.tokenDecimals.mu.Unlock()
	if _, active := reader.stats(); active != 0 || flights != 0 {
		t.Fatalf("cancelled source work leaked: active=%d flights=%d", active, flights)
	}
	requireDecimalsReuse(t, service, decimalsReuseToken(200), 8)
}

func TestTokenDecimalsReuseKeyAndByteCapacityStayBounded(t *testing.T) {
	service, reader, clock := decimalsReuseFixture()
	for index := range tokenDecimalsCacheMaxKeys + 1 {
		requireDecimalsReuse(t, service, decimalsReuseToken(1000+index), 6)
		clock.advance(time.Nanosecond)
	}
	service.tokenDecimals.mu.Lock()
	keys, bytes, flights := len(service.tokenDecimals.entries), service.tokenDecimals.bytes, len(service.tokenDecimals.inflight)
	service.tokenDecimals.mu.Unlock()
	if keys > tokenDecimalsCacheMaxKeys || bytes < 0 || bytes > tokenDecimalsCacheMaxBytes || flights != 0 {
		t.Fatalf("cache grew beyond approved bounds: keys=%d bytes=%d flights=%d", keys, bytes, flights)
	}
	before, _ := reader.stats()
	requireDecimalsReuse(t, service, decimalsReuseToken(1000+tokenDecimalsCacheMaxKeys), 6)
	if calls, _ := reader.stats(); calls != before {
		t.Fatal("newest verified key was lost during oldest-key eviction")
	}
	requireDecimalsReuse(t, service, decimalsReuseToken(1000), 6)
	if calls, _ := reader.stats(); calls != before+1 {
		t.Fatal("oldest key was retained beyond the key limit")
	}
	// Canonical addresses are fixed size, so the 4096-key limit normally binds
	// first. Exercise the private byte guard directly without widening the real
	// resolve input contract or adding test-only configuration to production.
	oversized := tokenDecimalsKey{chainID: 56, chainKey: "bsc", token: strings.Repeat("x", tokenDecimalsCacheMaxBytes)}
	source := tokenDecimalsSource{reader: reader, identity: reader.identity}
	readCalls := 0
	for range 2 {
		_, _ = service.tokenDecimals.read(context.Background(), source, oversized, clock.now, func(context.Context) (int32, error) {
			readCalls++
			return 6, nil
		})
	}
	service.tokenDecimals.mu.Lock()
	_, retained := service.tokenDecimals.entries[oversized]
	bytes, flights = service.tokenDecimals.bytes, len(service.tokenDecimals.inflight)
	service.tokenDecimals.mu.Unlock()
	if retained || bytes < 0 || bytes > tokenDecimalsCacheMaxBytes || flights != 0 || readCalls == 1 {
		t.Fatalf("oversized key was retained or leaked flight capacity: retained=%t bytes=%d flights=%d reads=%d", retained, bytes, flights, readCalls)
	}
}

func TestTokenDecimalsReusePanicWakesWaiterAndReleasesFlight(t *testing.T) {
	service, reader, _ := decimalsReuseFixture()
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	defer finish()
	reader.release, reader.entered, reader.panicFor = release, make(chan string, 1), "bounded fixture panic"
	token := decimalsReuseToken(10)
	type outcome struct {
		decimalsReuseResult
		panicFor any
	}
	leader := make(chan outcome, 1)
	go func() {
		result := outcome{}
		defer func() {
			result.panicFor = recover()
			leader <- result
		}()
		result.value, result.err = service.resolveTokenDecimals(context.Background(), token, nil)
	}()
	awaitDecimalsReuse(t, reader.entered)
	waitCtx := &decimalsReuseWaitContext{Context: context.Background(), entered: make(chan struct{})}
	waiter := make(chan decimalsReuseResult, 1)
	go func() {
		value, err := service.resolveTokenDecimals(waitCtx, token, nil)
		waiter <- decimalsReuseResult{value, err}
	}()
	awaitDecimalsReuse(t, waitCtx.entered)
	finish()
	if result := awaitDecimalsReuse(t, leader); result.err == nil && result.panicFor == nil {
		t.Fatal("source panic turned into successful precision")
	}
	if result := awaitDecimalsReuse(t, waiter); result.err == nil {
		t.Fatal("source panic was cached or left waiter with an invented value")
	}
	service.tokenDecimals.mu.Lock()
	flights := len(service.tokenDecimals.inflight)
	service.tokenDecimals.mu.Unlock()
	if _, active := reader.stats(); flights != 0 || active != 0 {
		t.Fatalf("source panic leaked work: active=%d flights=%d", active, flights)
	}
	reader.mu.Lock()
	reader.panicFor, reader.release, reader.entered = nil, nil, nil
	reader.mu.Unlock()
	requireDecimalsReuse(t, service, token, 6)
}
