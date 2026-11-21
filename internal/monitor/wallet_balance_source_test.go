package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

// Unlike fakeChain's legacy fixtures, this source never fills absent evidence.
type rawWalletBalanceSource struct {
	*fakeChain
	result chain.BalanceResult
}

func (s *rawWalletBalanceSource) Balance(context.Context, string, string, string, string) (chain.BalanceResult, error) {
	return s.result, nil
}

func rpcBalanceFixture(value string, decimals int) chain.BalanceResult {
	token := "0x" + strings.Repeat("2", 40)
	return chain.BalanceResult{
		Value: value, Symbol: "TST", Decimals: decimals,
		ChainID: 56, ChainKey: "bsc", WalletAddress: "0x" + strings.Repeat("1", 40), TokenAddress: &token,
		ReadSource: chain.WalletBalanceReadSource, ReadDecimals: &decimals,
	}
}

func TestWalletERC20InvalidSourceOrValueNeverReachesObservation(t *testing.T) {
	negative, tooLarge, mismatch := -1, 256, 8
	overflow := new(big.Int).Lsh(big.NewInt(1), 256).String()
	tests := []struct {
		name string
		edit func(*chain.BalanceResult)
	}{
		{"missing_source", func(v *chain.BalanceResult) { v.ReadSource = "" }},
		{"other_source", func(v *chain.BalanceResult) { v.ReadSource = "indexed_balance:v1" }},
		{"missing_precision", func(v *chain.BalanceResult) { v.ReadDecimals = nil }},
		{"negative_precision", func(v *chain.BalanceResult) { v.ReadDecimals = &negative }},
		{"excessive_precision", func(v *chain.BalanceResult) { v.ReadDecimals = &tooLarge }},
		{"precision_disagrees", func(v *chain.BalanceResult) { v.ReadDecimals = &mismatch }},
		{"wrong_chain_id", func(v *chain.BalanceResult) { v.ChainID = 1 }},
		{"missing_chain_id", func(v *chain.BalanceResult) { v.ChainID = 0 }},
		{"wrong_chain_key", func(v *chain.BalanceResult) { v.ChainKey = "ethereum" }},
		{"missing_chain_key", func(v *chain.BalanceResult) { v.ChainKey = "" }},
		{"wrong_wallet", func(v *chain.BalanceResult) { v.WalletAddress = "0x" + strings.Repeat("3", 40) }},
		{"missing_wallet", func(v *chain.BalanceResult) { v.WalletAddress = "" }},
		{"wrong_token", func(v *chain.BalanceResult) { token := "0x" + strings.Repeat("3", 40); v.TokenAddress = &token }},
		{"missing_token", func(v *chain.BalanceResult) { v.TokenAddress = nil }},
		{"empty", func(v *chain.BalanceResult) { v.Value = "" }},
		{"negative", func(v *chain.BalanceResult) { v.Value = "-1" }},
		{"not_a_number", func(v *chain.BalanceResult) { v.Value = "NaN" }},
		{"exponent", func(v *chain.BalanceResult) { v.Value = "1e2" }},
		{"space", func(v *chain.BalanceResult) { v.Value = " 1" }},
		{"missing_whole", func(v *chain.BalanceResult) { v.Value = ".1" }},
		{"missing_fraction", func(v *chain.BalanceResult) { v.Value = "1." }},
		{"multiple_points", func(v *chain.BalanceResult) { v.Value = "1.2.3" }},
		{"precision_overflow", func(v *chain.BalanceResult) { v.Value = "1.0000001" }},
		{"uint256_overflow", func(v *chain.BalanceResult) { v.Value = overflow }},
		{"oversized", func(v *chain.BalanceResult) { v.Value = strings.Repeat("1", 259) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previous := "10"
			rule := testRule(plans.Outgoing, &previous, plans.Standard)
			at := time.Now().Add(-time.Minute)
			rule.LastCheckedAt = &at
			repository := &cycleCommitRepository{fakeRepository: &fakeRepository{rules: []store.WatchRule{rule}, lastValue: previous}}
			current := rpcBalanceFixture("12", 6)
			test.edit(&current)
			source := &rawWalletBalanceSource{fakeChain: &fakeChain{}, result: current}
			notifier := &fakeNotifier{}
			executor := newTestExecutor(t, repository, source, notifier)
			result, err := executor.CheckAll(context.Background(), 200)
			if err != nil || len(result.Errors) != 1 || len(repository.observations) != 0 ||
				len(repository.deferred) != 1 || repository.lastValue != previous ||
				!rule.LastCheckedAt.Equal(at) || len(repository.walletQueue) != 0 || notifier.message != "" {
				t.Fatalf("invalid evidence reached persistence/delivery: result=%+v observations=%v deferred=%v value=%s", result, repository.observations, repository.deferred, repository.lastValue)
			}
		})
	}
}

func TestWalletERC20ZeroAndZeroPrecisionKeepOriginalThresholdBehavior(t *testing.T) {
	for _, test := range []struct {
		name      string
		value     string
		decimals  int
		ruleType  string
		previous  *string
		threshold string
		matched   bool
	}{
		{"explicit_zero", "0", 0, plans.BalanceThreshold, nil, "1", true},
		{"zero_precision_integer", "12", 0, plans.HighBalanceThreshold, nil, "12", true},
		{"initial_non_threshold_baseline", "0", 6, plans.BalanceChange, nil, "0", false},
		{"fractional_units", "1.000001", 6, plans.HighBalanceThreshold, nil, "1.000001", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			rule := testRule(test.ruleType, test.previous, plans.Standard)
			rule.Threshold = test.threshold
			repository := &cycleCommitRepository{fakeRepository: &fakeRepository{rules: []store.WatchRule{rule}}}
			source := &rawWalletBalanceSource{fakeChain: &fakeChain{}, result: rpcBalanceFixture(test.value, test.decimals)}
			executor := newTestExecutor(t, repository, source, &fakeNotifier{})
			result, err := executor.CheckAll(context.Background(), 200)
			if err != nil || len(result.Errors) != 0 || len(repository.observations) != 1 {
				t.Fatalf("valid balance rejected: result=%+v observations=%v", result, repository.observations)
			}
			p := repository.observations[0]
			if p.Matched != test.matched || p.CurrentValue != test.value || p.BalanceReadSource != chain.WalletBalanceReadSource ||
				p.BalanceReadDecimals == nil || *p.BalanceReadDecimals != test.decimals || p.ReadObservedAt == nil || p.SourceOccurredAt != nil {
				t.Fatalf("source, precision, or initial threshold changed: %+v", p)
			}
		})
	}
}

func TestWalletERC20LegacyChainIdentityIsCheckedWithoutChangingRule(t *testing.T) {
	for _, test := range []struct {
		name     string
		chainKey string
		chainID  int32
		valid    bool
	}{
		{"contradictory_stored_id", "bsc", 1, false},
		{"contradictory_stored_key", "ethereum", 56, false},
		{"canonical", "bsc", 56, true},
		{"existing_chain_alias", "bnb_chain", 56, true},
		{"existing_default_chain", "", 56, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			previous := "10"
			rule := testRule(plans.Incoming, &previous, plans.Standard)
			rule.ChainKey, rule.ChainID = test.chainKey, test.chainID
			repository := &cycleCommitRepository{fakeRepository: &fakeRepository{rules: []store.WatchRule{rule}}}
			source := &rawWalletBalanceSource{fakeChain: &fakeChain{}, result: rpcBalanceFixture("10", 6)}
			executor := newTestExecutor(t, repository, source, &fakeNotifier{})
			result, err := executor.CheckAll(context.Background(), 200)
			if err != nil || (len(repository.observations) == 1) != test.valid || (len(result.Errors) == 0) != test.valid {
				t.Fatalf("rule identity validation failed: %+v observations=%+v", result, repository.observations)
			}
			if rule.ChainKey != test.chainKey || rule.ChainID != test.chainID {
				t.Fatal("identity check rewrote the stored rule")
			}
		})
	}
}

func TestWalletERC20RPCSourceHTTPFailureRecoveryAndIndependentRules(t *testing.T) {
	var invalid, zero atomic.Bool
	var balanceCalls, dataCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Method != "eth_call" || len(request.Params) < 1 {
			dataCalls.Add(1)
			http.Error(w, "unexpected Data API fallback", http.StatusBadRequest)
			return
		}
		var call struct {
			Data string `json:"data"`
		}
		if json.Unmarshal(request.Params[0], &call) != nil {
			http.Error(w, "invalid fixture call", http.StatusBadRequest)
			return
		}
		var response string
		switch {
		case call.Data == "0x313ce567":
			response = fmt.Sprintf("0x%064x", 6)
		case call.Data == "0x95d89b41":
			response = "0x" + fmt.Sprintf("%-64s", fmt.Sprintf("%x", []byte("TST")))
			response = strings.ReplaceAll(response, " ", "0")
		case strings.HasPrefix(call.Data, "0x70a08231"):
			balanceCalls.Add(1)
			response = fmt.Sprintf("0x%064x", 12_000_000)
			if zero.Load() {
				response = fmt.Sprintf("0x%064x", 0)
			}
			if invalid.Load() {
				response = "0x"
			}
		default:
			http.Error(w, "unsupported fixture call", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": response})
	}))
	defer server.Close()
	primary, err := chain.NewClient("fixture-only", server.URL, chain.WithRPCBaseURL(server.URL), chain.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	previous := "10"
	first := testRule(plans.Incoming, &previous, plans.Standard)
	first.ID, first.Threshold = 1, "1"
	second := first
	second.ID, second.Threshold, second.DeBoxUserID, second.NotificationChatID = 2, "5", "other-owner", "other-route"
	repository := &cycleCommitRepository{fakeRepository: &fakeRepository{rules: []store.WatchRule{first, second}, lastValue: previous}}
	executor := newTestExecutor(t, repository, chain.NewWalletReadClient(primary), &fakeNotifier{})
	invalid.Store(true)
	failed, err := executor.CheckAll(context.Background(), 200)
	if err != nil || len(failed.Errors) != 2 || len(repository.observations) != 0 || repository.lastValue != previous || len(repository.walletQueue) != 0 || balanceCalls.Load() != 1 {
		t.Fatalf("RPC failure changed a baseline or was not batch bounded: result=%+v calls=%d observations=%v", failed, balanceCalls.Load(), repository.observations)
	}
	invalid.Store(false)
	recovered, err := executor.CheckAll(context.Background(), 200)
	if err != nil || len(recovered.Errors) != 0 || len(repository.observations) != 2 || recovered.Reads.Hits != 1 || balanceCalls.Load() != 2 || dataCalls.Load() != 0 {
		t.Fatalf("recovery/reuse failed or used Data API: result=%+v calls=%d data=%d", recovered, balanceCalls.Load(), dataCalls.Load())
	}
	for i, p := range repository.observations {
		if p.BalanceReadSource != chain.WalletBalanceReadSource || p.BalanceReadDecimals == nil || *p.BalanceReadDecimals != 6 || p.CurrentValue != "12" || p.ReadObservedAt == nil || p.SourceOccurredAt != nil || p.Rule.ID != int64(i+1) {
			t.Fatalf("lost source, precision, observation time or independent rule order: %+v", p)
		}
	}
	if !repository.observations[0].Matched || repository.observations[1].Matched ||
		!repository.observations[0].ReadObservedAt.Equal(*repository.observations[1].ReadObservedAt) || len(repository.walletQueue) != 1 {
		t.Fatal("memo sharing changed per-rule thresholds, sample evidence, or notification count")
	}
	if _, err := executor.CheckRule(context.Background(), second, plans.Standard); err != nil || balanceCalls.Load() != 3 {
		t.Fatal("direct CheckRule reused a previous batch's sample", err)
	}
	zero.Store(true)
	zeroRule := first
	zeroRule.ID, zeroRule.RuleType = 3, plans.Outgoing
	repository.rules = []store.WatchRule{zeroRule}
	zeroResult, err := executor.CheckAll(context.Background(), 200)
	last := repository.observations[len(repository.observations)-1]
	if err != nil || len(zeroResult.Errors) != 0 || last.CurrentValue != "0" || !last.Matched ||
		last.BalanceReadSource != chain.WalletBalanceReadSource || balanceCalls.Load() != 4 {
		t.Fatalf("explicit RPC zero did not follow the original net-change rule: result=%+v params=%+v", zeroResult, last)
	}
}

func TestWalletERC20SourceTransitionGuardDoesNotSendOrAdvanceBaseline(t *testing.T) {
	for _, guard := range []error{store.ErrWalletBalanceSourceTransitionRequired, store.ErrWalletBalanceSourceUnavailable} {
		t.Run(guard.Error(), func(t *testing.T) {
			previous := "10"
			rule := testRule(plans.Incoming, &previous, plans.Standard)
			rule.Threshold = "1"
			at := time.Now().Add(-time.Hour)
			rule.LastCheckedAt = &at
			repository := &cycleCommitRepository{
				fakeRepository: &fakeRepository{rules: []store.WatchRule{rule}, lastValue: previous},
				commitErr:      guard,
			}
			notifier := &fakeNotifier{}
			source := &rawWalletBalanceSource{fakeChain: &fakeChain{}, result: rpcBalanceFixture("12", 6)}
			executor := newTestExecutor(t, repository, source, notifier)
			result, err := executor.CheckAll(context.Background(), 200)
			if err != nil || len(result.Errors) != 1 || !strings.Contains(result.Errors[0].Error, guard.Error()) ||
				len(repository.observations) != 1 || len(repository.deferred) != 1 || repository.lastValue != previous || !rule.LastCheckedAt.Equal(at) ||
				len(repository.walletQueue) != 0 || notifier.message != "" {
				t.Fatalf("guard did not preserve baseline and suppress delivery: result=%+v observations=%v deferred=%v", result, repository.observations, repository.deferred)
			}
		})
	}
}

func TestWalletNonERC20ChecksDoNotReceiveBalanceSourceMetadata(t *testing.T) {
	previous := "10"
	for _, ruleType := range []string{plans.BalanceChange, plans.ApprovalChange, plans.AddressInteraction} {
		t.Run(ruleType, func(t *testing.T) {
			rule := testRule(ruleType, &previous, plans.Professional)
			if ruleType == plans.BalanceChange {
				rule.TokenAddress = nil
			}
			repository := &cycleCommitRepository{fakeRepository: &fakeRepository{rules: []store.WatchRule{rule}}}
			source := &fakeChain{
				balance:     chain.BalanceResult{Value: "10", Symbol: "BNB"},
				allowance:   chain.AllowanceResult{Value: "10", Symbol: "TST"},
				interaction: chain.InteractionResult{Cursor: "10"},
			}
			executor := newTestExecutor(t, repository, source, &fakeNotifier{})
			result, err := executor.CheckAll(context.Background(), 200)
			if err != nil || len(result.Errors) != 0 || len(repository.observations) != 1 || repository.observations[0].BalanceReadSource != "" || repository.observations[0].BalanceReadDecimals != nil {
				t.Fatalf("unrelated source contract changed: %+v params=%+v", result, repository.observations)
			}
		})
	}
}

func TestWalletRPCMemoOwnsPrecisionAndUsesVersionedERC20Key(t *testing.T) {
	ctx := context.Background()
	source := &rawWalletBalanceSource{fakeChain: &fakeChain{}, result: rpcBalanceFixture("12", 6)}
	reads := newWalletCycleReads(ctx, source, time.Minute)
	wallet, token := "0x"+strings.Repeat("1", 40), "0x"+strings.Repeat("2", 40)
	first, err := reads.Balance(ctx, wallet, token, "bsc", "")
	if err != nil {
		t.Fatal(err)
	}
	*first.ReadDecimals = 18
	second, err := reads.Balance(ctx, wallet, token, "bsc", "")
	if err != nil || second.ReadDecimals == nil || *second.ReadDecimals != 6 || second.ReadSource != chain.WalletBalanceReadSource || reads.metrics.Hits != 1 {
		t.Fatal("consumer mutation changed shared source provenance", second, err)
	}
	if _, err := reads.Balance(ctx, wallet, "", "bsc", ""); err != nil {
		t.Fatal(err)
	}
	for key := range reads.entries {
		if (key.kind == "token_balance" && key.version != chain.WalletBalanceReadSource) || (key.kind == "native_balance" && key.version != "balance:v1") {
			t.Fatalf("wrong source version in memo key: %+v", key)
		}
	}
}

func TestWalletNotificationFreezesBalanceEvidenceAndOmitsItForNative(t *testing.T) {
	previous, current := "10", "0"
	observed := time.Now().UTC().Truncate(time.Microsecond)
	for _, rpc := range []bool{true, false} {
		t.Run(map[bool]string{true: "rpc_zero_precision", false: "native_legacy_shape"}[rpc], func(t *testing.T) {
			rule := testRule(plans.Outgoing, &previous, plans.Standard)
			result := store.WalletObservationResult{
				Rule: rule, Value: current, ReadObservedAt: &observed,
				Event: &store.AlertEvent{ID: 31, PreviousValue: &previous, CurrentValue: &current, CreatedAt: observed},
			}
			zero := 0
			if rpc {
				result.BalanceReadSource = chain.WalletBalanceReadSource
				result.BalanceReadDecimals = &zero
			} else {
				result.Rule.TokenAddress = nil
			}
			executor := newTestExecutor(t, &fakeRepository{}, &fakeChain{}, &fakeNotifier{})
			snapshot, payload, err := executor.buildWalletNotification(result, "TST", "unchanged rule result")
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			var details, frozen map[string]json.RawMessage
			if json.Unmarshal(snapshot.Details, &details) != nil || json.Unmarshal(encoded, &frozen) != nil {
				t.Fatal("notification evidence is not valid JSON")
			}
			for _, fields := range []map[string]json.RawMessage{details, frozen} {
				source, hasSource := fields["balance_read_source"]
				precision, hasPrecision := fields["balance_read_decimals"]
				if rpc {
					if !hasSource || !hasPrecision || string(source) != `"`+chain.WalletBalanceReadSource+`"` || string(precision) != "0" {
						t.Fatalf("RPC source or legitimate zero precision was omitted: %s %s", source, precision)
					}
				} else if hasSource || hasPrecision {
					t.Fatalf("native notification gained ERC-20 evidence: %v", fields)
				}
			}
			if string(details["schema_version"]) != "1" || payload.Text != snapshot.NotificationText {
				t.Fatal("existing schema or notification text contract changed")
			}
			// Source objects may later be reused; already encoded snapshots and
			// outboxes must preserve the original evidence and legitimate zero.
			zero = 18
			if rpc && (string(details["balance_read_decimals"]) != "0" || string(frozen["balance_read_decimals"]) != "0") {
				t.Fatal("frozen evidence followed a later metadata mutation")
			}
		})
	}
}
