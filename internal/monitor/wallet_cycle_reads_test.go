package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type cycleReadSource struct {
	*fakeChain
	err          error
	reads        int
	afterRead    func()
	transactions []map[string]any
	rawCalls     int
	rpp          []int
}

func (s *cycleReadSource) Balance(ctx context.Context, address, token, chainKey, fallback string) (chain.BalanceResult, error) {
	s.reads++
	if s.afterRead != nil {
		s.afterRead()
	}
	if s.err != nil {
		return chain.BalanceResult{}, s.err
	}
	return s.fakeChain.Balance(ctx, address, token, chainKey, fallback)
}

func (s *cycleReadSource) TransactionsByAccount(_ context.Context, _, _, _ string, rpp int) ([]map[string]any, error) {
	s.rawCalls++
	s.rpp = append(s.rpp, rpp)
	return s.transactions, s.err
}

func TestWalletCycleReadsCanonicalKeysAndOwnedResults(t *testing.T) {
	ctx := context.Background()
	wallet, token, spender := "0x"+strings.Repeat("a", 40), "0x"+strings.Repeat("b", 40), "0x"+strings.Repeat("c", 40)
	source := &fakeChain{balance: chain.BalanceResult{Value: "12", TokenAddress: &token}, allowance: chain.AllowanceResult{Value: "5"}}
	reads := newWalletCycleReads(ctx, source, time.Minute)
	a, err := reads.Balance(ctx, wallet, token, "", "bnb")
	if err != nil {
		t.Fatal(err)
	}
	*a.TokenAddress = "changed by consumer"
	b, err := reads.Balance(ctx, strings.ToUpper(wallet[:2])+strings.ToUpper(wallet[2:]), token, "bsc", "ethereum")
	// EVM validation requires the original 0x prefix, but address hex case is
	// canonicalized. Check it separately from an invalid prefix below.
	if err == nil {
		t.Fatal("invalid 0X prefix unexpectedly accepted")
	}
	b, err = reads.Balance(ctx, "0x"+strings.ToUpper(wallet[2:]), "0x"+strings.ToUpper(token[2:]), "bnb_chain", "ethereum")
	if err != nil || b.TokenAddress == nil || *b.TokenAddress != token || len(source.calls) != 1 {
		t.Fatalf("canonical reuse/ownership failed: value=%+v calls=%v err=%v", b, source.calls, err)
	}
	queries := []struct{ address, token, network string }{
		{wallet, "", "bsc"}, {wallet, "0x" + strings.Repeat("0", 40), "bsc"},
		{wallet, "0x" + strings.Repeat("d", 40), "bsc"}, {wallet, token, "base"},
		{"0x" + strings.Repeat("e", 40), token, "bsc"},
	}
	for _, query := range queries {
		if _, err := reads.Balance(ctx, query.address, query.token, query.network, "bsc"); err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range []string{spender, "0x" + strings.Repeat("d", 40), spender} {
		if _, err := reads.TokenAllowance(ctx, wallet, token, target, "bsc", "bsc"); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(source.calls, ","); got != "balance,balance,balance,balance,balance,balance,allowance,allowance" {
		t.Fatalf("distinct read kinds/scopes conflated: %s", got)
	}
}

func TestWalletCycleReadsFailuresAgeCancellationAndBudgets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wallet, token := "0x"+strings.Repeat("1", 40), "0x"+strings.Repeat("2", 40)
	sentinel := errors.New("provider unavailable")
	source := &cycleReadSource{fakeChain: &fakeChain{balance: chain.BalanceResult{Value: "17"}}, err: sentinel}
	reads := newWalletCycleReads(ctx, source, time.Minute)
	now := time.Now()
	reads.now = func() time.Time { return now }
	for i := 0; i < 2; i++ {
		_, err := reads.Balance(ctx, wallet, token, "bsc", "bsc")
		if !errors.Is(err, sentinel) || !walletReadCanDefer(err) {
			t.Fatalf("source failure was replaced or swallowed: %v", err)
		}
	}
	if source.reads != 1 || reads.metrics.Hits != 1 {
		t.Fatalf("failure memo not bounded to the batch: %+v", reads.metrics)
	}
	source.err = nil
	now = now.Add(time.Minute)
	value, err := reads.Balance(ctx, wallet, token, "bsc", "bsc")
	if err != nil || value.Value != "17" || source.reads != 2 || reads.metrics.Expired != 1 {
		t.Fatalf("expired failure did not refresh: value=%+v err=%v stats=%+v", value, err, reads.metrics)
	}
	cancel()
	if _, err := reads.Balance(context.Background(), wallet, token, "bsc", "bsc"); !errors.Is(err, context.Canceled) || source.reads != 2 {
		t.Fatalf("cancelled scope consumed cached value: %v", err)
	}
	limited := newWalletCycleReads(context.Background(), source, time.Minute)
	limited.maxKeys, limited.maxBytes = 1, 1000
	for _, address := range []string{wallet, "0x" + strings.Repeat("3", 40), "0x" + strings.Repeat("3", 40)} {
		if _, err := limited.Balance(context.Background(), address, token, "bsc", "bsc"); err != nil {
			t.Fatal(err)
		}
	}
	if limited.metrics.Entries != 1 || limited.metrics.NotCached != 2 || limited.metrics.Bytes > 1000 {
		t.Fatalf("entry cap not enforced: %+v", limited.metrics)
	}
	limited.maxBytes = 1
	if _, err := limited.Balance(context.Background(), "0x"+strings.Repeat("4", 40), token, "bsc", "bsc"); err != nil {
		t.Fatal("cache capacity must not replace a valid source result", err)
	}
	slow := newWalletCycleReads(context.Background(), source, time.Minute)
	slow.now = func() time.Time { return now }
	source.afterRead = func() { now = now.Add(time.Minute) }
	if _, err := slow.Balance(context.Background(), wallet, token, "bsc", "bsc"); err == nil || slow.metrics.Entries != 0 {
		t.Fatal("over-age response became a current observation")
	}
}

func TestWalletInteractionSharesOriginalThirtyTransactionsButMatchesEachTarget(t *testing.T) {
	ctx := context.Background()
	wallet, targetA, targetB := "0x"+strings.Repeat("1", 40), "0x"+strings.Repeat("2", 40), "0x"+strings.Repeat("3", 40)
	source := &cycleReadSource{fakeChain: &fakeChain{}, transactions: []map[string]any{
		{"hash": "first", "to": targetA, "timestamp": json.Number("1700000000"), "raw": json.Number("115792089237316195423570985008687907853269984665640564039457584007913129639935")},
		{"hash": "second", "to": targetB, "timestamp": json.Number("1700000001")},
	}}
	reads := newWalletCycleReads(ctx, source, time.Minute)
	a, err := reads.LatestInteraction(ctx, wallet, targetA, "bsc", "bsc")
	if err != nil || a.Cursor != "first" || a.SourceOccurredAt == nil {
		t.Fatalf("first match lost: %+v/%v", a, err)
	}
	a.Transaction["hash"] = "consumer mutation"
	b, err := reads.LatestInteraction(ctx, wallet, targetB, "bsc", "bsc")
	if err != nil || b.Cursor != "second" || b.SourceOccurredAt == nil || b.SourceOccurredAt.Unix() != 1700000001 {
		t.Fatalf("second target reused first target's result: %+v/%v", b, err)
	}
	again, err := reads.LatestInteraction(ctx, wallet, targetA, "bsc", "bsc")
	if err != nil || again.Cursor != "first" || again.Transaction["raw"].(json.Number).String() != source.transactions[0]["raw"].(json.Number).String() {
		t.Fatalf("mutable alias or number precision lost: %+v/%v", again, err)
	}
	if source.rawCalls != 1 || fmt.Sprint(source.rpp) != "[30]" || len(source.calls) != 0 {
		t.Fatalf("raw read contract changed: calls=%d rpp=%v", source.rawCalls, source.rpp)
	}
	source.transactions = make([]map[string]any, 31)
	source.transactions[30] = map[string]any{"to": targetA, "hash": "outside-window"}
	other := newWalletCycleReads(ctx, source, time.Minute)
	if _, err := other.LatestInteraction(ctx, wallet, targetA, "bsc", "bsc"); err == nil {
		t.Fatal("provider excess rows expanded the original 30-record window")
	}
}

type cycleCommitRepository struct {
	*fakeRepository
	observations []store.RecordWalletObservationParams
	commitErr    error
	deferred     []int64
}

func (r *cycleCommitRepository) RecordWalletObservation(ctx context.Context, params store.RecordWalletObservationParams) (store.WalletObservationResult, error) {
	r.observations = append(r.observations, params)
	if r.commitErr != nil {
		return store.WalletObservationResult{}, r.commitErr
	}
	return r.fakeRepository.RecordWalletObservation(ctx, params)
}

func (r *cycleCommitRepository) DeferWalletObservation(_ context.Context, id int64, _ time.Duration) error {
	r.deferred = append(r.deferred, id)
	return nil
}

func TestWalletCycleReadsKeepPerRuleBaselinesThresholdsAndCommitOrder(t *testing.T) {
	previous := "10"
	first := testRule(plans.Incoming, &previous, plans.Standard)
	first.ID, first.Threshold = 1, "1"
	second := first
	second.ID, second.Threshold, second.DeBoxUserID, second.NotificationChatID = 2, "5", "other-owner", "other-route"
	repository := &cycleCommitRepository{fakeRepository: &fakeRepository{rules: []store.WatchRule{first, second}}}
	source := &fakeChain{balance: chain.BalanceResult{Value: "12", Symbol: "T"}}
	executor := newTestExecutor(t, repository, source, &fakeNotifier{})
	result, err := executor.CheckAll(context.Background(), 200)
	if err != nil || result.Checked != 2 || len(repository.observations) != 2 || len(source.calls) != 1 || result.Reads.Hits != 1 {
		t.Fatalf("shared read or independent commits missing: %+v err=%v observations=%d reads=%v", result, err, len(repository.observations), source.calls)
	}
	if !repository.observations[0].Matched || repository.observations[1].Matched || repository.observations[0].Rule.ID != 1 || repository.observations[1].Rule.ID != 2 || repository.observations[1].Rule.NotificationChatID != "other-route" {
		t.Fatalf("rule semantics/order/owner route changed: %+v", repository.observations)
	}
	firstAt, secondAt := repository.observations[0].ReadObservedAt, repository.observations[1].ReadObservedAt
	if firstAt == nil || secondAt == nil || !firstAt.Equal(*secondAt) || repository.observations[0].SourceOccurredAt != nil || repository.observations[1].SourceOccurredAt != nil {
		t.Fatal("memo hit invented a new sample time or a blockchain occurrence time")
	}
	var frozen addressNotificationSnapshotPayload
	if len(repository.snapshots) != 1 || json.Unmarshal(repository.snapshots[0].Details, &frozen) != nil || frozen.ReadObservedAt == nil || !frozen.ReadObservedAt.Equal(*firstAt) {
		t.Fatal("actual sample time missing from immutable notification details")
	}
	var payload store.WalletNotificationPayload
	if len(repository.walletQueue) != 1 || json.Unmarshal(repository.walletQueue[0].Payload, &payload) != nil || payload.ReadObservedAt == nil || !payload.ReadObservedAt.Equal(*firstAt) {
		t.Fatal("outbox did not freeze actual sample time")
	}
	_, _ = executor.CheckRule(context.Background(), second, plans.Standard)
	if len(source.calls) != 2 {
		t.Fatal("immediate check reused a previous batch's observation")
	}
}

func TestWalletCycleReadFailureDefersOnlyProvenPreCommitFailure(t *testing.T) {
	previous := "10"
	rule := testRule(plans.BalanceChange, &previous, plans.Standard)
	repository := &cycleCommitRepository{fakeRepository: &fakeRepository{rules: []store.WatchRule{rule}}}
	source := &cycleReadSource{fakeChain: &fakeChain{balance: chain.BalanceResult{Value: "10"}}, err: errors.New("source down")}
	executor := newTestExecutor(t, repository, source, &fakeNotifier{})
	result, _ := executor.CheckAll(context.Background(), 200)
	if len(result.Errors) != 1 || len(repository.deferred) != 1 || len(repository.observations) != 0 || rule.LastValue == nil || *rule.LastValue != "10" {
		t.Fatal("failed source advanced baseline or did not defer", result)
	}
	source.err = nil
	repository.commitErr = errors.New("commit result unknown")
	_, _ = executor.CheckAll(context.Background(), 200)
	if len(repository.deferred) != 1 || len(repository.observations) != 1 {
		t.Fatal("ambiguous commit was misclassified as proven rollback")
	}
}

func TestWalletCycleReadsDoNotReuseObservationOlderThanAnotherRulesBaseline(t *testing.T) {
	now := time.Now().UTC()
	source := &cycleReadSource{fakeChain: &fakeChain{balance: chain.BalanceResult{Value: "12"}}}
	reads := newWalletCycleReads(context.Background(), source, time.Minute)
	reads.now = func() time.Time { return now }
	wallet, token := "0x"+strings.Repeat("1", 40), "0x"+strings.Repeat("2", 40)
	if _, err := reads.Balance(context.Background(), wallet, token, "bsc", "bsc"); err != nil {
		t.Fatal(err)
	}
	baseline := now.Add(time.Second)
	now = now.Add(2 * time.Second)
	ctx := context.WithValue(context.Background(), walletReadBaselineTimeKey{}, &baseline)
	if _, err := reads.Balance(ctx, wallet, token, "bsc", "bsc"); err != nil || source.reads != 2 || reads.metrics.Expired != 1 {
		t.Fatalf("cache moved a later baseline backwards: reads=%d stats=%+v err=%v", source.reads, reads.metrics, err)
	}
}
