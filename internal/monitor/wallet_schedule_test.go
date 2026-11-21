package monitor

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type dueScheduleRepository struct {
	*fakeRepository
	now       time.Time
	backoff   map[int64]time.Time
	committed []int64
	attempted []int64
	limits    []int
	periods   []time.Duration
	unknown   bool
}

func scheduleKeyLess(a, b *store.WalletObservationCursor) bool {
	if a.LastCheckedAt == nil && b.LastCheckedAt != nil {
		return true
	}
	if a.LastCheckedAt != nil && b.LastCheckedAt == nil {
		return false
	}
	if a.LastCheckedAt != nil && !a.LastCheckedAt.Equal(*b.LastCheckedAt) {
		return a.LastCheckedAt.Before(*b.LastCheckedAt)
	}
	return a.RuleID < b.RuleID
}

func (r *dueScheduleRepository) due(interval time.Duration) []store.WatchRule {
	var result []store.WatchRule
	for _, rule := range r.rules {
		if rule.Enabled != 1 || rule.RunStatus != "active" || r.backoff[rule.ID].After(r.now) || rule.LastCheckedAt != nil && rule.LastCheckedAt.Add(interval).After(r.now) {
			continue
		}
		result = append(result, rule)
	}
	sort.Slice(result, func(i, j int) bool {
		return scheduleKeyLess(walletObservationKey(result[i]), walletObservationKey(result[j]))
	})
	return result
}

func (r *dueScheduleRepository) WalletObservationDueFence(_ context.Context, interval time.Duration) (*store.WalletObservationCursor, error) {
	rows := r.due(interval)
	if len(rows) == 0 {
		return nil, nil
	}
	key := walletObservationKey(rows[len(rows)-1])
	for _, rule := range r.rules {
		if rule.ID > key.MaxRuleID {
			key.MaxRuleID = rule.ID
		}
	}
	return key, nil
}

func (r *dueScheduleRepository) ListDueWatchRules(ctx context.Context, limit int, interval time.Duration) ([]store.WatchRule, error) {
	return r.ListDueWatchRulesPage(ctx, limit, interval, nil, nil)
}

func (r *dueScheduleRepository) ListDueWatchRulesPage(_ context.Context, limit int, interval time.Duration, after, through *store.WalletObservationCursor) ([]store.WatchRule, error) {
	r.limits = append(r.limits, limit)
	r.periods = append(r.periods, interval)
	var result []store.WatchRule
	for _, rule := range r.due(interval) {
		if through != nil && through.MaxRuleID > 0 && rule.ID > through.MaxRuleID {
			continue
		}
		key := walletObservationKey(rule)
		if after != nil && !scheduleKeyLess(after, key) || through != nil && scheduleKeyLess(through, key) {
			continue
		}
		result = append(result, rule)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (r *dueScheduleRepository) NextWalletObservationAt(_ context.Context, interval time.Duration) (*time.Time, error) {
	var earliest *time.Time
	for _, rule := range r.rules {
		if rule.Enabled != 1 || rule.RunStatus != "active" {
			continue
		}
		at := r.now
		if rule.LastCheckedAt != nil {
			at = rule.LastCheckedAt.Add(interval)
		}
		if r.backoff[rule.ID].After(at) {
			at = r.backoff[rule.ID]
		}
		if earliest == nil || at.Before(*earliest) {
			copy := at
			earliest = &copy
		}
	}
	return earliest, nil
}

func (r *dueScheduleRepository) DeferWalletObservation(_ context.Context, id int64, delay time.Duration) error {
	if r.backoff == nil {
		r.backoff = map[int64]time.Time{}
	}
	r.backoff[id] = r.now.Add(delay)
	return nil
}

func (r *dueScheduleRepository) RecordWalletObservation(_ context.Context, p store.RecordWalletObservationParams) (store.WalletObservationResult, error) {
	r.attempted = append(r.attempted, p.Rule.ID)
	if r.unknown {
		return store.WalletObservationResult{}, errors.New("commit result unknown")
	}
	for i, rule := range r.rules {
		if rule.ID == p.Rule.ID {
			at, value := r.now, p.CurrentValue
			r.rules[i].LastCheckedAt = &at
			r.rules[i].LastValue = &value
			delete(r.backoff, rule.ID)
			r.committed = append(r.committed, rule.ID)
			return store.WalletObservationResult{Rule: p.Rule, Status: p.NoTriggerStatus, Value: p.CurrentValue, ReadObservedAt: p.ReadObservedAt}, nil
		}
	}
	return store.WalletObservationResult{}, store.ErrWalletObservationStale
}

func newDueScheduleRepository(count int) *dueScheduleRepository {
	r := &dueScheduleRepository{fakeRepository: &fakeRepository{}, now: time.Now(), backoff: map[int64]time.Time{}}
	for i := 1; i <= count; i++ {
		rule := testRule(plans.BalanceChange, nil, plans.Standard)
		rule.ID = int64(i)
		r.rules = append(r.rules, rule)
	}
	return r
}

func TestWalletDuePagesContinueAfterTwoHundredWithoutRecheckingSuccessfulRules(t *testing.T) {
	r := newDueScheduleRepository(201)
	source := &fakeChain{balance: chain.BalanceResult{Value: "10", Symbol: "T"}}
	e := newTestExecutor(t, r, source, &fakeNotifier{})
	runner := NewRunner(e, func(context.Context) (Lock, bool, error) { return &fakeLock{}, true, nil }, time.Minute)
	first := runner.runCycle(context.Background(), discardLogger())
	if len(r.committed) != 200 || first != 250*time.Millisecond || runner.walletAfter == nil || runner.walletAfter.RuleID != 200 {
		t.Fatalf("first page did not yield continuation: commits=%d delay=%s after=%+v", len(r.committed), first, runner.walletAfter)
	}
	second := runner.runCycle(context.Background(), discardLogger())
	if len(r.committed) != 201 || r.committed[200] != 201 || second < 59*time.Second || runner.walletAfter != nil || runner.walletThrough != nil {
		t.Fatalf("tail slept a period or rechecked head: commits=%v delay=%s", r.committed, second)
	}
	if fmt.Sprint(r.limits) != "[200 200]" || len(source.calls) != 2 {
		t.Fatalf("unbounded page or cross-batch memo: limits=%v reads=%v", r.limits, source.calls)
	}
	_, err := e.CheckDue(context.Background(), 200, time.Minute)
	if err != nil || len(r.committed) != 201 {
		t.Fatal("successful rule ran before its original interval", err)
	}
	r.now = r.now.Add(time.Minute)
	result, err := e.CheckDue(context.Background(), 200, time.Minute)
	if err != nil || result.Checked != 200 {
		t.Fatalf("next normal interval did not resume: %+v/%v", result, err)
	}
}

func TestWalletDueInvalidHeadBackoffDoesNotStarveHealthyTail(t *testing.T) {
	for _, kind := range []string{"invalid", "unsupported", "plan_limited", "bad_plan", "panic"} {
		t.Run(kind, func(t *testing.T) {
			r := newDueScheduleRepository(201)
			for i := 0; i < 200; i++ {
				switch kind {
				case "invalid":
					r.rules[i].RuleType = plans.ApprovalChange
					r.rules[i].TargetAddress = nil
				case "unsupported":
					r.rules[i].RuleType = "not_a_rule"
				case "plan_limited":
					r.rules[i].RuleType = plans.AddressInteraction
				case "bad_plan":
					r.rules[i].EffectivePlanCode = "not_a_plan"
				case "panic":
					r.rules[i].WalletAddress = "0x0000000000000000000000000000000000000001"
				}
			}
			source := &scheduleSource{fakeChain: &fakeChain{balance: chain.BalanceResult{Value: "10"}}, panicWallet: "0x0000000000000000000000000000000000000001"}
			e := newTestExecutor(t, r, source, &fakeNotifier{})
			runner := NewRunner(e, func(context.Context) (Lock, bool, error) { return &fakeLock{}, true, nil }, time.Minute)
			runner.runCycle(context.Background(), discardLogger())
			runner.runCycle(context.Background(), discardLogger())
			if len(r.committed) != 1 || r.committed[0] != 201 || len(r.backoff) != 200 {
				t.Fatalf("bad head starved tail: committed=%v deferred=%d", r.committed, len(r.backoff))
			}
			for i := 0; i < 200; i++ {
				if r.rules[i].LastCheckedAt != nil || r.rules[i].LastValue != nil {
					t.Fatal("backoff advanced success baseline")
				}
			}
		})
	}
}

type scheduleSource struct {
	*fakeChain
	panicWallet string
	slowWallet  string
	reads       []string
}

func (s *scheduleSource) Balance(ctx context.Context, address, token, network, fallback string) (chain.BalanceResult, error) {
	s.reads = append(s.reads, address)
	if address == s.panicWallet {
		panic("broken read implementation")
	}
	if address == s.slowWallet {
		<-ctx.Done()
		return chain.BalanceResult{}, ctx.Err()
	}
	return s.fakeChain.Balance(ctx, address, token, network, fallback)
}

func TestWalletDueSlowHeadBudgetUsesConstantTraversalAndRechecksAfterRestart(t *testing.T) {
	r := newDueScheduleRepository(3)
	r.rules[0].WalletAddress = "0x0000000000000000000000000000000000000001"
	source := &scheduleSource{fakeChain: &fakeChain{balance: chain.BalanceResult{Value: "10"}}, slowWallet: r.rules[0].WalletAddress}
	e := newTestExecutor(t, r, source, &fakeNotifier{})
	period := 20 * time.Millisecond
	runner := NewRunner(e, func(context.Context) (Lock, bool, error) { return &fakeLock{}, true, nil }, period)
	started := time.Now()
	runner.runCycle(context.Background(), discardLogger())
	if time.Since(started) > time.Second || runner.walletAfter == nil || runner.walletAfter.RuleID != 1 || len(r.committed) != 0 {
		t.Fatalf("slow read not bounded: after=%+v commits=%v", runner.walletAfter, r.committed)
	}
	runner.runCycle(context.Background(), discardLogger())
	if fmt.Sprint(r.committed) != "[2 3]" || r.rules[0].LastCheckedAt != nil || len(r.backoff) != 0 {
		t.Fatalf("slow head repeatedly blocked tail or falsely advanced: %v", r.committed)
	}
	// A restart discards only traversal state. Persisted successful rows remain
	// ineligible until their period; the uncommitted head is independently read.
	source.slowWallet = ""
	restarted := NewRunner(e, func(context.Context) (Lock, bool, error) { return &fakeLock{}, true, nil }, period)
	restarted.runCycle(context.Background(), discardLogger())
	if fmt.Sprint(r.committed) != "[2 3 1]" {
		t.Fatalf("restart lost/duplicated a successful observation: %v", r.committed)
	}
}

func TestWalletDueCommitUnknownContinuesTraversalWithoutDeferOrBaselineClaim(t *testing.T) {
	r := newDueScheduleRepository(3)
	r.unknown = true
	e := newTestExecutor(t, r, &fakeChain{balance: chain.BalanceResult{Value: "10"}}, &fakeNotifier{})
	first, err := e.checkDuePage(context.Background(), 2, time.Minute, nil, nil)
	if err != nil || !first.HasMoreDue || len(r.backoff) != 0 || len(r.committed) != 0 {
		t.Fatalf("commit ambiguity guessed: %+v/%v", first, err)
	}
	r.unknown = false
	second, err := e.checkDuePage(context.Background(), 2, time.Minute, first.lastAttempted, first.through)
	if err != nil || second.Checked != 1 || fmt.Sprint(r.committed) != "[3]" {
		t.Fatalf("unknown head blocked current traversal: %+v/%v commits=%v", second, err, r.committed)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := len(r.attempted)
	_, _ = e.CheckDue(ctx, 200, time.Minute)
	if len(r.attempted) != before {
		t.Fatal("outer cancellation continued rule checks")
	}
}

func TestWalletDueFixedMaxIDKeepsNewNullRowsFromStarvingOldNonNullTail(t *testing.T) {
	r := newDueScheduleRepository(3)
	old := r.now.Add(-time.Hour)
	r.rules[0].LastCheckedAt = &old
	r.rules[1].LastCheckedAt = &old
	r.rules[2].WalletAddress = "0x0000000000000000000000000000000000000001"
	source := &scheduleSource{fakeChain: &fakeChain{balance: chain.BalanceResult{Value: "10"}}, slowWallet: r.rules[2].WalletAddress}
	e := newTestExecutor(t, r, source, &fakeNotifier{})
	runner := NewRunner(e, func(context.Context) (Lock, bool, error) { return &fakeLock{}, true, nil }, 20*time.Millisecond)
	runner.runCycle(context.Background(), discardLogger())
	if runner.walletThrough == nil || runner.walletThrough.RuleID != 2 || runner.walletThrough.MaxRuleID != 3 || runner.walletAfter == nil || runner.walletAfter.RuleID != 3 {
		t.Fatalf("mixed traversal fence=%+v after=%+v", runner.walletThrough, runner.walletAfter)
	}
	for id := 4; id <= 204; id++ {
		rule := testRule(plans.BalanceChange, nil, plans.Standard)
		rule.ID = int64(id)
		r.rules = append(r.rules, rule)
	}
	runner.runCycle(context.Background(), discardLogger())
	if fmt.Sprint(r.committed) != "[1 2]" || runner.walletThrough != nil {
		t.Fatalf("new NULL rows entered the old pass: commits=%v fence=%+v", r.committed, runner.walletThrough)
	}
	source.slowWallet = ""
	runner.runCycle(context.Background(), discardLogger())
	if len(r.committed) <= 2 || r.committed[2] != 3 {
		t.Fatalf("next pass failed to include deferred/new work: %v", r.committed)
	}
}

func TestWalletDueUnknownCommitRetriesAreLocallyBoundedWithoutPersistedFailure(t *testing.T) {
	r := newDueScheduleRepository(2)
	r.unknown = true
	e := newTestExecutor(t, r, &fakeChain{balance: chain.BalanceResult{Value: "10"}}, &fakeNotifier{})
	runner := NewRunner(e, func(context.Context) (Lock, bool, error) { return &fakeLock{}, true, nil }, time.Minute)
	delay := runner.runCycle(context.Background(), discardLogger())
	if delay != 5*time.Second || len(r.backoff) != 0 || len(r.committed) != 0 || runner.walletAfter != nil || runner.walletThrough != nil {
		t.Fatalf("unknown commit became a successful/failed persistent observation or hot loop: delay=%s backoff=%v", delay, r.backoff)
	}
	r.unknown = false
	runner.runCycle(context.Background(), discardLogger())
	if fmt.Sprint(r.committed) != "[1 2]" || runner.walletPassHadError {
		t.Fatalf("temporary error pause stuck after recovery: committed=%v errors=%v", r.committed, runner.walletPassHadError)
	}
}
