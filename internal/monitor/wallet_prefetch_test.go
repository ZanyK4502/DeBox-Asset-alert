package monitor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type concurrentWalletSource struct {
	*fakeChain
	mu                       sync.Mutex
	reads                    map[string]int
	delay                    map[string]time.Duration
	started                  chan string
	active, maxActive, total atomic.Int64
}

func (*concurrentWalletSource) WalletBalancePrefetchWorkers() int { return 2 }
func (s *concurrentWalletSource) Balance(ctx context.Context, wallet, token, key, fallback string) (chain.BalanceResult, error) {
	active := s.active.Add(1)
	defer s.active.Add(-1)
	for old := s.maxActive.Load(); active > old; old = s.maxActive.Load() {
		if s.maxActive.CompareAndSwap(old, active) {
			break
		}
	}
	s.total.Add(1)
	if s.started != nil {
		s.started <- token
	}
	s.mu.Lock()
	if s.reads == nil {
		s.reads = map[string]int{}
	}
	s.reads[token]++
	number := s.reads[token]
	delay := s.delay[token]
	s.mu.Unlock()
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return chain.BalanceResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return chain.BalanceResult{}, err
	}
	profile, err := chain.ChainProfile(key, fallback)
	if err != nil {
		return chain.BalanceResult{}, err
	}
	value := rpcBalanceFixture(fmt.Sprint(10+number), 6)
	value.ChainKey, value.ChainID, value.WalletAddress = profile.Key, profile.ChainID, wallet
	if token == "" {
		value.ReadSource = ""
		value.ReadDecimals = nil
		value.TokenAddress = nil
	} else {
		value.TokenAddress = &token
	}
	return value, nil
}

type streamingWalletRepository struct {
	*cycleCommitRepository
	firstCommitReads int64
	source           *concurrentWalletSource
}

func (r *streamingWalletRepository) RecordWalletObservation(ctx context.Context, p store.RecordWalletObservationParams) (store.WalletObservationResult, error) {
	if len(r.observations) == 0 {
		r.firstCommitReads = r.source.total.Load()
	}
	return r.cycleCommitRepository.RecordWalletObservation(ctx, p)
}

func prefetchRules(n int) []store.WatchRule {
	previous := "10"
	result := make([]store.WatchRule, 0, n)
	for i := 0; i < n; i++ {
		rule := testRule(plans.BalanceChange, &previous, plans.Professional)
		token := fmt.Sprintf("0x%040x", i+100)
		rule.ID = int64(i + 1)
		rule.TokenAddress = &token
		rule.Threshold = "100"
		result = append(result, rule)
	}
	return result
}

func TestWalletPrefetchStreamsOriginalOrderWithTwoReaders(t *testing.T) {
	rules := prefetchRules(20)
	source := &concurrentWalletSource{fakeChain: &fakeChain{}, delay: map[string]time.Duration{*rules[0].TokenAddress: 20 * time.Millisecond}}
	repo := &streamingWalletRepository{cycleCommitRepository: &cycleCommitRepository{fakeRepository: &fakeRepository{rules: rules}}, source: source}
	executor := newTestExecutor(t, repo, source, &fakeNotifier{})
	result, err := executor.CheckAll(context.Background(), 200)
	if err != nil || len(result.Errors) != 0 || len(repo.observations) != len(rules) || source.maxActive.Load() != 2 || source.active.Load() != 0 || repo.firstCommitReads >= int64(len(rules)) {
		t.Fatalf("prefetch did not stream/join: result=%+v peak=%d active=%d atfirstcommit=%d", result, source.maxActive.Load(), source.active.Load(), repo.firstCommitReads)
	}
	for i, p := range repo.observations {
		if p.Rule.ID != rules[i].ID || p.ReadObservedAt == nil || p.ReadObservedAt.After(time.Now()) {
			t.Fatalf("commit order/sample evidence changed: %+v", p)
		}
	}
	if result.Reads.Requests != len(rules) {
		t.Fatalf("unused/actual prefetch reads not included: %+v", result.Reads)
	}
}

func TestWalletPrefetchSmallWindowCancellationAndPerKeyTime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := &concurrentWalletSource{fakeChain: &fakeChain{}, started: make(chan string, 20)}
	reads := newWalletCycleReads(ctx, source, time.Minute)
	rules := prefetchRules(20)
	executor := newTestExecutor(t, &fakeRepository{}, source, &fakeNotifier{})
	requests := executor.walletPrefetchRequests(rules)
	stop := reads.startBalancePrefetch(ctx, requests)
	defer stop()
	for i := 0; i < walletPrefetchWindow; i++ {
		select {
		case <-source.started:
		case <-time.After(time.Second):
			t.Fatal("prefetch did not fill bounded window")
		}
	}
	select {
	case <-source.started:
		t.Fatal("prefetch read ahead beyond four keys without consumer")
	case <-time.After(10 * time.Millisecond):
	}
	first := reads.prefetch[requests[0].key]
	select {
	case <-first.done:
	case <-time.After(time.Second):
		t.Fatal("first prefetch did not finish")
	}
	start := first.entry.fetchedAt
	if _, err := reads.Balance(ctx, requests[0].wallet, requests[0].token, requests[0].chainKey, requests[0].fallback); err != nil || reads.lastObservedAt == nil || !reads.lastObservedAt.Equal(start.UTC().Truncate(time.Microsecond)) {
		t.Fatal("another worker replaced this key's sample time", err)
	}
	if first.entry.data != nil {
		t.Fatal("consumed prefetch retained duplicate payload")
	}
	cancel()
	stop()
	if source.active.Load() != 0 || reads.prefetch != nil || source.total.Load() > walletPrefetchWindow+1 || reads.metrics.Requests != int(source.total.Load()) {
		t.Fatalf("cancel left readers/results behind: active=%d reads=%d stats=%+v", source.active.Load(), source.total.Load(), reads.metrics)
	}
}

func TestWalletPrefetchDiscardsSampleOlderThanLaterBaseline(t *testing.T) {
	ctx := context.Background()
	source := &concurrentWalletSource{fakeChain: &fakeChain{}}
	reads := newWalletCycleReads(ctx, source, time.Minute)
	rule := prefetchRules(1)[0]
	executor := newTestExecutor(t, &fakeRepository{}, source, &fakeNotifier{})
	request := executor.walletPrefetchRequests([]store.WatchRule{rule})[0]
	stop := reads.startBalancePrefetch(ctx, []walletPrefetchRequest{request})
	defer stop()
	pending := reads.prefetch[request.key]
	<-pending.done
	baseline := pending.entry.fetchedAt.Add(time.Microsecond)
	time.Sleep(time.Millisecond)
	currentCtx := context.WithValue(ctx, walletReadBaselineTimeKey{}, &baseline)
	value, err := reads.Balance(currentCtx, request.wallet, request.token, request.chainKey, request.fallback)
	if err != nil || value.Value != "12" || reads.metrics.Expired != 1 || reads.lastObservedAt.Before(baseline) {
		t.Fatalf("old prefetch was compared with later baseline: %+v %v %+v", value, err, reads.metrics)
	}
	newer := reads.lastObservedAt.Add(time.Microsecond)
	time.Sleep(time.Millisecond)
	value, err = reads.Balance(context.WithValue(ctx, walletReadBaselineTimeKey{}, &newer), request.wallet, request.token, request.chainKey, request.fallback)
	stop()
	if err != nil || value.Value != "13" || reads.metrics.Requests != 3 || reads.metrics.Expired != 2 {
		t.Fatalf("duplicate-key rule reused a sample before its baseline: %+v %v %+v", value, err, reads.metrics)
	}
}

func TestWalletPrefetchInterleavedRulesAndEligibility(t *testing.T) {
	rules := prefetchRules(4)
	previous := "10"
	allowance := testRule(plans.ApprovalChange, &previous, plans.Standard)
	allowance.ID = 21
	interaction := testRule(plans.AddressInteraction, &previous, plans.Professional)
	interaction.ID = 22
	limited := testRule(plans.ApprovalChange, &previous, plans.Free)
	limited.ID = 23
	invalid := rules[3]
	invalid.ID = 24
	invalid.WalletAddress = "invalid-wallet"
	ordered := []store.WatchRule{allowance, rules[0], interaction, limited, rules[1], invalid, rules[2]}
	source := &concurrentWalletSource{fakeChain: &fakeChain{allowance: chain.AllowanceResult{Value: "10"}, interaction: chain.InteractionResult{Cursor: "10"}}}
	repo := &cycleCommitRepository{fakeRepository: &fakeRepository{rules: ordered}}
	executor := newTestExecutor(t, repo, source, &fakeNotifier{})
	result, err := executor.CheckAll(context.Background(), 200)
	if err != nil || result.Checked != len(ordered) || len(result.Errors) != 1 || source.total.Load() != 3 || source.active.Load() != 0 || len(repo.observations) != 5 {
		t.Fatalf("mixed rules stalled/skipped/overread: %+v reads=%d params=%v", result, source.total.Load(), repo.observations)
	}
	var ids []string
	for _, p := range repo.observations {
		ids = append(ids, fmt.Sprint(p.Rule.ID))
	}
	if strings.Join(ids, ",") != "21,1,22,2,3" {
		t.Fatal("interleaving changed database order", ids)
	}
}
