package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/monitor"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/subscription"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/testdb"
)

const creationToken = "0x2222222222222222222222222222222222222222"

type walletSnapshotEvidenceStore struct {
	*store.Store
	details json.RawMessage
}

func (s *walletSnapshotEvidenceStore) RecordWalletObservation(ctx context.Context, p store.RecordWalletObservationParams) (store.WalletObservationResult, error) {
	build := p.BuildNotification
	p.BuildNotification = func(result store.WalletObservationResult) (store.CreateNotificationDetailSnapshotParams, store.WalletNotificationPayload, error) {
		snapshot, payload, err := build(result)
		if err == nil {
			s.details = append(json.RawMessage(nil), snapshot.Details...)
		}
		return snapshot, payload, err
	}
	return s.Store.RecordWalletObservation(ctx, p)
}

func creationWalletRPC(t *testing.T) (*chain.WalletReadClient, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	var dataCalls atomic.Int64
	var balance atomic.Int64
	balance.Store(12500000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			dataCalls.Add(1)
			http.Error(w, "Data API balance must not be used", http.StatusBadRequest)
			return
		}
		var request struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Method != "eth_call" || len(request.Params) != 2 {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		var call struct{ To, Data string }
		_ = json.Unmarshal(request.Params[0], &call)
		if call.To != creationToken {
			http.Error(w, "unexpected token", http.StatusBadRequest)
			return
		}
		result := ""
		switch {
		case call.Data == "0x313ce567":
			result = fmt.Sprintf("0x%064x", 6)
		case call.Data == "0x95d89b41":
			result = "0x" + fmt.Sprintf("%-64s", fmt.Sprintf("%x", []byte("USDC")))
			result = strings.ReplaceAll(result, " ", "0")
		case strings.HasPrefix(call.Data, "0x70a08231") && len(call.Data) == 74:
			result = fmt.Sprintf("0x%064x", balance.Load())
		default:
			http.Error(w, "unexpected selector", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	t.Cleanup(server.Close)
	primary, err := chain.NewClient("local-only", server.URL, chain.WithRPCBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	return chain.NewWalletReadClient(primary), &dataCalls, &balance
}

func TestWalletRPCSourceSeedsStandaloneAndCombinationCreation(t *testing.T) {
	reader, dataCalls, _ := creationWalletRPC(t)
	entitlements := &fakeEntitlements{plan: planForTest(t, plans.Professional)}
	service := New(Dependencies{Repository: &fakeRepository{}, Entitlements: entitlements, Chain: reader})
	input := DefaultWatchRuleInput()
	input.WalletAddress = "0x1111111111111111111111111111111111111111"
	input.TokenAddress = creationToken
	input.RuleType = plans.Incoming
	input.Threshold = "1"
	before := time.Now().Add(-time.Second)
	if _, err := service.CreateWatchRule(context.Background(), "wallet-rpc-creation", input); err != nil {
		t.Fatal(err)
	}
	seed := entitlements.createdRuleInput
	if seed.LastValue == nil || *seed.LastValue != "12.5" || seed.BalanceReadSource != chain.WalletBalanceReadSource || seed.BalanceReadDecimals == nil || *seed.BalanceReadDecimals != 6 || seed.BalanceReadObservedAt == nil || seed.BalanceReadObservedAt.Before(before) || seed.BalanceReadObservedAt.After(time.Now()) {
		t.Fatalf("creation did not carry verified seed: %#v", seed)
	}
	combo := DefaultCombinationRuleInput()
	combo.Members = []CombinationMemberInput{{Rule: input, RequiredTriggerCount: 1}, {Rule: input, RequiredTriggerCount: 2}}
	combo.Members[1].Rule.RuleType = plans.Outgoing
	if _, err := service.CreateCombinationRule(context.Background(), "wallet-rpc-creation", combo); err != nil {
		t.Fatal(err)
	}
	for _, member := range entitlements.combinationInput.Members {
		seed := member.Rule
		if seed.LastValue == nil || *seed.LastValue != "12.5" || seed.BalanceReadSource != chain.WalletBalanceReadSource || seed.BalanceReadDecimals == nil || *seed.BalanceReadDecimals != 6 || seed.BalanceReadObservedAt == nil {
			t.Fatalf("combination member lost source: %#v", seed)
		}
	}
	if dataCalls.Load() != 0 {
		t.Fatalf("creation used Data API %d times", dataCalls.Load())
	}
}

func TestWalletRPCThresholdCreationKeepsInitialCheck(t *testing.T) {
	reader, _, _ := creationWalletRPC(t)
	entitlements := &fakeEntitlements{plan: planForTest(t, plans.Standard)}
	checker := &fakeInitialChecker{}
	service := New(Dependencies{Repository: &fakeRepository{}, Entitlements: entitlements, Chain: reader, InitialChecker: checker})
	input := DefaultWatchRuleInput()
	input.WalletAddress = "0x1111111111111111111111111111111111111111"
	input.TokenAddress = creationToken
	input.RuleType = plans.HighBalanceThreshold
	input.Threshold = "10"
	created, err := service.CreateWatchRule(context.Background(), "wallet-rpc-threshold", input)
	if err != nil {
		t.Fatal(err)
	}
	if entitlements.createdRuleInput.LastValue != nil || entitlements.createdRuleInput.BalanceReadSource != chain.WalletBalanceReadSource || created.InitialCheck == nil {
		t.Fatalf("initial threshold behavior changed: %#v / %#v", entitlements.createdRuleInput, created)
	}
}

func TestWalletCreationRejectsUnverifiedSourceWithoutSavingRule(t *testing.T) {
	for _, source := range []string{"", "legacy_data_api", chain.WalletBalanceReadSource} {
		t.Run(source, func(t *testing.T) {
			entitlements := &fakeEntitlements{plan: planForTest(t, plans.Standard)}
			value := chain.BalanceResult{Value: "0", TokenAddress: stringPointer(creationToken), ReadSource: source}
			service := New(Dependencies{Repository: &fakeRepository{}, Entitlements: entitlements, Chain: &fakeChainService{balance: value}})
			input := DefaultWatchRuleInput()
			input.WalletAddress = "0x1111111111111111111111111111111111111111"
			input.TokenAddress = creationToken
			if _, err := service.CreateWatchRule(context.Background(), "unverified-source", input); err == nil || entitlements.createdRuleInput.DeBoxUserID != "" {
				t.Fatalf("unverified source saved a rule: source=%q error=%v", source, err)
			}
		})
	}
}

func TestWalletCreationEvidencePreservesZeroPrecision(t *testing.T) {
	zero := 0
	wallet := "0x1111111111111111111111111111111111111111"
	value := chain.BalanceResult{Value: "7", ChainKey: "bsc", ChainID: 56, WalletAddress: wallet, TokenAddress: stringPointer(creationToken), ReadSource: chain.WalletBalanceReadSource, ReadDecimals: &zero}
	source, decimals, at, err := walletBalanceCreationEvidence(value, wallet, creationToken, "bsc", time.Now())
	if err != nil || source != chain.WalletBalanceReadSource || decimals == nil || *decimals != 0 || at == nil {
		t.Fatalf("zero precision: %q %v %v %v", source, decimals, at, err)
	}
}

// This joins the actual management, quota, RPC, observation transaction and
// outbox paths in a fresh private database; fake repository success is not
// sufficient to prove that a newly seeded rule can run without a cutover.
func TestAcceptanceWalletRPCCreationAndFirstObservationShareSource(t *testing.T) {
	database := testdb.Open(t)
	catalog := acceptanceCatalog(t)
	reader, dataCalls, balance := creationWalletRPC(t)
	notifications := &fakeNotificationService{}
	evidence := &walletSnapshotEvidenceStore{Store: database}
	executor := monitor.New(monitor.Dependencies{Repository: evidence, Chain: reader, Catalog: catalog, Notifications: notifications, DefaultChainKey: "bsc"})
	service := New(Dependencies{Repository: database, Entitlements: subscription.New(database, catalog), Chain: reader, InitialChecker: executor})
	input := DefaultWatchRuleInput()
	input.WalletAddress = "0x1111111111111111111111111111111111111111"
	input.TokenAddress = creationToken
	input.Threshold = "1"
	ctx := context.Background()
	created, err := service.CreateWatchRule(ctx, "rpc-source-acceptance", input)
	if err != nil {
		t.Fatal(err)
	}
	first, err := executor.CheckRule(ctx, created.Rule, plans.Free)
	if err != nil {
		t.Fatal(err)
	}
	observed, ok := first.(monitor.RuleResult)
	if !ok || observed.Status != "no_change" || notifications.text != "" {
		t.Fatalf("new RPC rule incorrectly required a legacy handoff: %#v", first)
	}
	rules, err := database.ListUserWatchRules(ctx, created.Rule.DeBoxUserID)
	if err != nil || len(rules) != 1 || rules[0].LastValue == nil || *rules[0].LastValue != "12.5" {
		t.Fatalf("new source baseline missing: %#v %v", rules, err)
	}
	balance.Store(13500000)
	second, err := executor.CheckRule(ctx, rules[0], plans.Free)
	if err != nil {
		t.Fatal(err)
	}
	observed, ok = second.(monitor.RuleResult)
	if !ok || observed.Status != "alerted" || !strings.Contains(notifications.text, "USDC") || dataCalls.Load() != 0 {
		t.Fatalf("normal net-change rule or display metadata changed: %#v text=%q dataCalls=%d", second, notifications.text, dataCalls.Load())
	}
	used, err := database.CountDailyAlertEvents(ctx, created.Rule.DeBoxUserID, "Asia/Shanghai")
	if err != nil || used != 1 {
		t.Fatalf("notification quota=%d error=%v", used, err)
	}
	var details struct {
		SchemaVersion    int        `json:"schema_version"`
		Source           string     `json:"balance_read_source"`
		Decimals         *int       `json:"balance_read_decimals"`
		ReadObservedAt   *time.Time `json:"read_observed_at"`
		SourceOccurredAt *time.Time `json:"source_occurred_at"`
	}
	if err := json.Unmarshal(evidence.details, &details); err != nil || details.SchemaVersion != 1 || details.Source != chain.WalletBalanceReadSource || details.Decimals == nil || *details.Decimals != 6 || details.ReadObservedAt == nil || details.SourceOccurredAt != nil {
		t.Fatalf("committed observation lost its immutable source evidence: %s error=%v", evidence.details, err)
	}
}
