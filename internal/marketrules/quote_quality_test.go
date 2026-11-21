package marketrules

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestSnapshotUnusableQuotePreservesStateAndCannotCreateCrossing(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	price := "120"
	state, _ := json.Marshal(ruleState{LastSnapshotID: 40, LastEventID: 30, ConditionActive: true, PendingCrossing: true})
	old, future := now.Add(-marketquality.MaxAge-time.Second), now.Add(time.Second)
	for _, quality := range []marketquality.Quote{
		{Status: marketquality.StatusUnknown},
		{Status: marketquality.StatusStaleFallback, FetchedAt: &now},
		{Status: marketquality.StatusLive, FetchedAt: &old},
		{Status: marketquality.StatusLive, FetchedAt: &now, SourceTimestamp: &old},
		{Status: marketquality.StatusLive, FetchedAt: &future},
	} {
		result, err := Evaluate(EvaluationInput{Rule: store.MarketRule{RuleType: plans.MarketPriceAbove, ThresholdValue: "100", State: state},
			Snapshots: []store.MarketSnapshot{{ID: 41, CapturedAt: now, PriceUSD: &price, QuoteQuality: quality}}, Now: now})
		if err != nil || len(result.Triggers) != 0 || !bytes.Equal(result.State, state) {
			t.Fatalf("unusable quote reset or triggered state: quality=%#v state=%s err=%v", quality, result.State, err)
		}
	}
}

func TestSnapshotNormalCachedQuoteWithinApprovedAgeCanTrigger(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	fetched := now.Add(-marketquality.MaxAge + time.Second)
	price := "120"
	result, err := Evaluate(EvaluationInput{Rule: store.MarketRule{RuleType: plans.MarketPriceAbove, ThresholdValue: "100"},
		Snapshots: []store.MarketSnapshot{{ID: 41, CapturedAt: now, PriceUSD: &price, QuoteQuality: marketquality.Quote{Status: marketquality.StatusCached, FetchedAt: &fetched}}}, Now: now})
	if err != nil || len(result.Triggers) != 1 {
		t.Fatalf("normal cached quote inside approved age was disabled: %#v %v", result, err)
	}
}

func TestQuoteRecoveryPreservesAbsoluteCrossingAndLegitimateRepeatPolicy(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	baselineAt, window := now.Add(-time.Hour), int32(60)
	baseline, price := "100", "120"
	for _, test := range []struct {
		name, kind, threshold string
		repeat                bool
		want                  int
	}{
		{"absolute already active", plans.MarketPriceAbove, "100", false, 0},
		{"relative repeats disabled", plans.MarketPriceIncrease, "10", false, 0},
		{"relative repeat preserved", plans.MarketPriceIncrease, "10", true, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			last := now.Add(-30 * time.Second)
			state, _ := json.Marshal(ruleState{LastSnapshotID: 40, ConditionActive: true})
			rule := store.MarketRule{RuleType: test.kind, ThresholdValue: test.threshold, ThresholdUnit: "percent", WindowMinutes: &window,
				State: state, RepeatWhileActive: test.repeat, CooldownSeconds: 60, LastTriggeredAt: &last}
			snapshots := []store.MarketSnapshot{{ID: 41, CapturedAt: now, PriceUSD: &price, QuoteQuality: marketquality.Quote{Status: marketquality.StatusStaleFallback, FetchedAt: &now}},
				{ID: 1, CapturedAt: baselineAt, PriceUSD: &baseline, QuoteQuality: liveQuoteFixture(baselineAt)}}
			unavailable, err := Evaluate(EvaluationInput{Rule: rule, Snapshots: snapshots, Now: now})
			if err != nil || !bytes.Equal(unavailable.State, state) || len(unavailable.Triggers) != 0 {
				t.Fatalf("outage changed state: %s %v", unavailable.State, err)
			}
			recoveredAt := now.Add(31 * time.Second)
			snapshots[0].ID, snapshots[0].CapturedAt, snapshots[0].QuoteQuality = 42, recoveredAt, liveQuoteFixture(recoveredAt)
			alignedBaselineAt := recoveredAt.Add(-time.Hour)
			snapshots[1].CapturedAt, snapshots[1].QuoteQuality = alignedBaselineAt, liveQuoteFixture(alignedBaselineAt)
			rule.State = unavailable.State
			recovered, err := Evaluate(EvaluationInput{Rule: rule, Snapshots: snapshots, Now: recoveredAt})
			if err != nil || len(recovered.Triggers) != test.want || !rule.LastTriggeredAt.Equal(last) {
				t.Fatalf("recovery changed original policy: triggers=%d err=%v", len(recovered.Triggers), err)
			}
		})
	}
}

func TestQuoteRecoveryKeepsPendingCrossingAndChecksHistoricalSnapshotAtCapture(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	price, baseline := "120", "100"
	baselineAt, window := now.Add(-time.Hour), int32(60)
	state, _ := json.Marshal(ruleState{LastSnapshotID: 40, ConditionActive: true, PendingCrossing: true})
	last := now.Add(-2 * time.Minute)
	result, err := Evaluate(EvaluationInput{Rule: store.MarketRule{RuleType: plans.MarketPriceIncrease, ThresholdValue: "10", WindowMinutes: &window, State: state,
		CooldownSeconds: 60, LastTriggeredAt: &last}, Snapshots: []store.MarketSnapshot{
		{ID: 41, CapturedAt: now, PriceUSD: &price, QuoteQuality: liveQuoteFixture(now)},
		{ID: 1, CapturedAt: baselineAt, PriceUSD: &baseline, QuoteQuality: liveQuoteFixture(baselineAt)}}, Now: now})
	if err != nil || len(result.Triggers) != 1 {
		t.Fatalf("valid historical baseline/pending crossing was discarded: %#v %v", result, err)
	}
	var after ruleState
	_ = json.Unmarshal(result.State, &after)
	if !after.ConditionActive || after.PendingCrossing || after.LastSnapshotID != 41 {
		t.Fatalf("pending recovery state=%s", result.State)
	}
}

func TestEventUSDRequiresOriginalValuationProofWhileTokenAndSupplyPercentStayIndependent(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	usd, token, raw, supply := "12000", "12", "12000000", "100000000"
	wallet := "0x2222222222222222222222222222222222222222"
	event := store.MarketEvent{ID: 1, EventType: marketparse.EventBuy, EventKey: "fixture-buy", USDValue: &usd, TokenAmount: &token, TokenAmountRaw: &raw, Confirmed: 1, OccurredAt: now}
	result, err := Evaluate(EvaluationInput{Rule: store.MarketRule{RuleType: plans.MarketLargeBuy, ThresholdValue: "10000", ThresholdUnit: "usd"}, Events: []store.MarketEvent{event}, Now: now})
	if err != nil || len(result.Triggers) != 0 || len(result.Suppressed) != 1 || result.Suppressed[0].Reason != "market_valuation_quote_unavailable" {
		t.Fatalf("unproved USD triggered: %#v %v", result, err)
	}
	result, err = Evaluate(EvaluationInput{Rule: store.MarketRule{RuleType: plans.MarketLargeBuy, ThresholdValue: "10", ThresholdUnit: "token"}, Events: []store.MarketEvent{event}, Now: now})
	if err != nil || len(result.Triggers) != 1 {
		t.Fatalf("raw token rule was blocked by unrelated quote: %#v %v", result, err)
	}
	event.EventType, event.WalletAddress = "holder_increase", &wallet
	result, err = Evaluate(EvaluationInput{Rule: store.MarketRule{RuleType: plans.MarketHolderIncrease, ThresholdValue: "10", ThresholdUnit: "percent"},
		Project: store.MarketProject{TotalSupplyRaw: &supply}, Events: []store.MarketEvent{event}, Holders: []store.MarketHolder{{HolderAddress: wallet}}, Now: now})
	if err != nil || len(result.Triggers) != 1 {
		t.Fatalf("holder supply ratio was blocked by unrelated quote: %#v %v", result, err)
	}
	old := now.Add(-24 * time.Hour)
	historical := event
	historical.OccurredAt = old
	proved := withValuationProofFixture(historical, old)
	if !eventValuationAvailable(proved) {
		t.Fatal("original valid valuation was tested against current wall time")
	}
	proved.OccurredAt = now
	if eventValuationAvailable(proved) {
		t.Fatal("a new valuation timestamp was allowed to certify a different event time")
	}
}

func TestConsecutiveTradeMissingValuationPausesWithoutAdvancingOrUndercounting(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	old, current := tradeFactsTestEvent(2, 99, now.Add(-time.Minute)), tradeFactsTestEvent(3, 101, now)
	var metadata map[string]any
	_ = json.Unmarshal(old.Metadata, &metadata)
	metadata["trade_projection"].(map[string]any)["valuation_metadata"] = []any{}
	old.Metadata, _ = json.Marshal(metadata)
	rule := tradeFactsTestRule(100, 100)
	rule.RuleType, rule.TriggerCountThreshold = plans.MarketConsecutiveLargeBuy, 2
	before := append([]byte(nil), rule.State...)
	result, err := evaluateTradeFacts(rule, tradeFactsTestProject(), []store.MarketEvent{old, current}, now)
	if !errors.Is(err, ErrTradeWindowContextIncomplete) || len(result.State) != 0 || len(result.Triggers) != 0 || !bytes.Equal(rule.State, before) {
		t.Fatalf("unknown window was counted or advanced: %#v %v", result, err)
	}
	rule.ThresholdUnit, rule.ThresholdValue = "token", "10"
	result, err = evaluateTradeFacts(rule, tradeFactsTestProject(), []store.MarketEvent{old, current}, now)
	if err != nil || len(result.Triggers) != 1 || *result.Triggers[0].CurrentValue != "2" {
		t.Fatalf("token window unexpectedly required USD quotes: %#v %v", result, err)
	}
}

func TestTradePercentRequiresFreshQualityForEverySelectedPool(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	event := tradeFactsTestEvent(3, 101, now)
	var metadata map[string]any
	_ = json.Unmarshal(event.Metadata, &metadata)
	projection := metadata["trade_projection"].(map[string]any)
	snapshots := projection["liquidity_snapshots"].([]any)
	snapshots[1].(map[string]any)["quote_quality"] = marketquality.Quote{Status: marketquality.StatusStaleFallback, FetchedAt: &now}
	event.Metadata, _ = json.Marshal(metadata)
	rule := tradeFactsTestRule(100, 100)
	rule.ThresholdUnit, rule.ThresholdValue = "percent", "0.5"
	result, err := evaluateTradeFacts(rule, tradeFactsTestProject(), []store.MarketEvent{event}, now)
	if err != nil || len(result.Triggers) != 0 || len(result.Suppressed) != 1 || result.Suppressed[0].Reason != "trade_liquidity_quote_unavailable" {
		t.Fatalf("one stale pool denominator was used: %#v %v", result, err)
	}
	rule.ThresholdUnit, rule.ThresholdValue = "usd", "10000"
	result, err = evaluateTradeFacts(rule, tradeFactsTestProject(), []store.MarketEvent{event}, now)
	if err != nil || len(result.Triggers) != 1 {
		t.Fatalf("USD trade depended on unrelated current liquidity: %#v %v", result, err)
	}
}

func TestTradeWindowValuationPreflightsEveryDeploymentBeforeWriting(t *testing.T) {
	service, repository, rule := newTradeLaneTestService(true)
	rule.RuleType, rule.TriggerCountThreshold = plans.MarketConsecutiveLargeBuy, 1
	deployment, pool := int64(8), int64(30)
	second := repository.targets[0]
	second.TargetKey, second.MarketProjectDeploymentID, second.MarketPoolID = "deployment:8/pool:3", &deployment, &pool
	second.ChainKey, second.ChainID = "eth", 1
	repository.targets = append(repository.targets, second)
	before := make(map[string][]byte)
	for _, target := range MergeTradeRuleTargets(repository.targets) {
		if _, err := service.InitializeTradeRuleState(context.Background(), rule.ID, target, 100); err != nil {
			t.Fatal(err)
		}
		before[target.TargetKey] = append([]byte(nil), repository.states[target.TargetKey].State...)
	}
	firstEvent := tradeFactsTestEvent(3, 101, service.now())
	old, current := tradeFactsTestEvent(4, 99, service.now().Add(-time.Minute)), tradeFactsTestEvent(5, 101, service.now())
	old.ChainID, current.ChainID = 1, 1
	var metadata map[string]any
	_ = json.Unmarshal(old.Metadata, &metadata)
	metadata["trade_projection"].(map[string]any)["valuation_metadata"] = []any{}
	old.Metadata, _ = json.Marshal(metadata)
	repository.tradeEventsByTarget = map[string][]store.MarketEvent{
		"deployment:7/trades:v2": {firstEvent}, "deployment:8/trades:v2": {old, current},
	}
	if _, err := service.evaluateRule(context.Background(), rule); !errors.Is(err, ErrTradeWindowContextIncomplete) {
		t.Fatalf("incomplete second deployment error=%v", err)
	}
	if len(repository.created) != 0 || len(repository.changedTargets) != 0 {
		t.Fatal("a deployment persisted before all quote contexts were checked")
	}
	for key, state := range before {
		if !bytes.Equal(repository.states[key].State, state) {
			t.Fatalf("deployment cursor advanced on unavailable valuation: %s", key)
		}
	}
}

func TestRecommendationsRejectStaleSnapshotAndUnprovedEventSamples(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	liq, usd, price := "1000000", "999999", "1"
	snapshot := store.MarketSnapshot{PriceUSD: &price, LiquidityUSD: &liq, CapturedAt: now, QuoteQuality: marketquality.Quote{Status: marketquality.StatusStaleFallback, FetchedAt: &now}}
	if result := RecommendThresholdsAt(snapshot, nil, now); len(result) != 0 {
		t.Fatal("stale fallback created new recommendations")
	}
	snapshot.QuoteQuality = liveQuoteFixture(now)
	without := RecommendThresholdsAt(snapshot, nil, now)
	unproved := store.MarketEvent{USDValue: &usd, Confirmed: 1, OccurredAt: now}
	with := RecommendThresholdsAt(snapshot, []store.MarketEvent{unproved}, now)
	a, _ := json.Marshal(without)
	b, _ := json.Marshal(with)
	if !bytes.Equal(a, b) {
		t.Fatal("unproved old USD sample changed new recommended thresholds")
	}
}

func TestRecommendationsRequirePriceAndLiquidityWithoutRejectingRealZeroLiquidity(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	price, zero, negative := "1", "0", "-1"
	for _, test := range []struct {
		name             string
		price, liquidity *string
		want             bool
	}{
		{"missing price", nil, &zero, false}, {"zero price", &zero, &zero, false}, {"negative price", &negative, &zero, false},
		{"missing liquidity", &price, nil, false}, {"negative liquidity", &price, &negative, false}, {"real zero liquidity", &price, &zero, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := RecommendThresholdsAt(store.MarketSnapshot{PriceUSD: test.price, LiquidityUSD: test.liquidity, CapturedAt: now, QuoteQuality: liveQuoteFixture(now)}, nil, now)
			if (len(values) > 0) != test.want {
				t.Fatalf("recommendations available=%v want=%v", len(values) > 0, test.want)
			}
		})
	}
}

func TestLifecycleAndHolderRankRulesDoNotRequireUSDQuotes(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	wallet := "0x2222222222222222222222222222222222222222"
	for _, test := range []struct {
		kind, eventType, unit, threshold string
		metadata                         json.RawMessage
	}{
		{plans.MarketNewPool, marketparse.EventPoolInitialized, "count", "1", nil},
		{plans.MarketFourMemeMigration, marketparse.EventMigrated, "count", "1", nil},
		{plans.MarketFourMemeProgress, marketparse.EventBuy, "progress", "80", json.RawMessage(`{"protocol":"four_meme","progress_percent":"90"}`)},
		{plans.MarketHolderRankEntered, "holder_rank_entered", "count", "10", json.RawMessage(`{"new_rank":"9"}`)},
	} {
		t.Run(test.kind, func(t *testing.T) {
			result, err := Evaluate(EvaluationInput{Rule: store.MarketRule{RuleType: test.kind, ThresholdUnit: test.unit, ThresholdValue: test.threshold},
				Events:  []store.MarketEvent{{ID: 1, EventType: test.eventType, EventKey: "fixture-event", WalletAddress: &wallet, Metadata: test.metadata, Confirmed: 1, OccurredAt: now}},
				Holders: []store.MarketHolder{{HolderAddress: wallet}}, Now: now})
			if err != nil || len(result.Triggers) != 1 {
				t.Fatalf("independent rule required an unrelated USD quote: %#v %v", result, err)
			}
		})
	}
}

func TestUSDValuationRequiresCompleteSchemaProof(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	usd := "1000"
	event := withValuationProofFixture(store.MarketEvent{USDValue: &usd}, now)
	var metadata map[string]any
	_ = json.Unmarshal(event.Metadata, &metadata)
	for _, test := range []struct {
		key   string
		value any
	}{{"valuation_complete", false}, {"valuation_schema_version", 2}, {"valuation_at", nil}} {
		original := metadata[test.key]
		metadata[test.key] = test.value
		broken := event
		broken.Metadata, _ = json.Marshal(metadata)
		if eventValuationAvailable(broken) {
			t.Fatalf("incomplete valuation proof was trusted: %s", broken.Metadata)
		}
		metadata[test.key] = original
	}
}

func TestSnapshotFallbackBaselineDoesNotSkipToOlderFreshObservation(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	latestPrice, badBaseline, olderPrice := "120", "110", "100"
	target, older, window := now.Add(-time.Hour), now.Add(-2*time.Hour), int32(60)
	state, _ := json.Marshal(ruleState{LastSnapshotID: 40, ConditionActive: true, PendingCrossing: true})
	result, err := Evaluate(EvaluationInput{Rule: store.MarketRule{RuleType: plans.MarketPriceIncrease, ThresholdValue: "10", WindowMinutes: &window, State: state},
		Snapshots: []store.MarketSnapshot{
			{ID: 41, CapturedAt: now, PriceUSD: &latestPrice, QuoteQuality: liveQuoteFixture(now)},
			{ID: 20, CapturedAt: target, PriceUSD: &badBaseline, QuoteQuality: marketquality.Quote{Status: marketquality.StatusStaleFallback, FetchedAt: &target}},
			{ID: 10, CapturedAt: older, PriceUSD: &olderPrice, QuoteQuality: liveQuoteFixture(older)},
		}, Now: now})
	if err != nil || len(result.Triggers) != 0 || !bytes.Equal(result.State, state) {
		t.Fatalf("fallback baseline was replaced by an older fresh point: state=%s err=%v", result.State, err)
	}
}

func TestVolumeAverageDoesNotReplaceBadContributingSampleWithOlderFreshValue(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	volume, badVolume, oldVolume := "3000", "2000", "1000"
	badAt, oldAt := now.Add(-time.Minute), now.Add(-2*time.Minute)
	state, _ := json.Marshal(ruleState{LastSnapshotID: 40, ConditionActive: true, PendingCrossing: true})
	window := int32(60)
	result, err := Evaluate(EvaluationInput{Rule: store.MarketRule{RuleType: plans.MarketVolumeSpike, ThresholdValue: "2", WindowMinutes: &window, State: state}, Snapshots: []store.MarketSnapshot{
		{ID: 41, CapturedAt: now, Volume1hUSD: &volume, QuoteQuality: liveQuoteFixture(now)},
		{ID: 20, CapturedAt: badAt, Volume1hUSD: &badVolume, QuoteQuality: marketquality.Quote{Status: marketquality.StatusStaleFallback, FetchedAt: &badAt}},
		{ID: 10, CapturedAt: oldAt, Volume1hUSD: &oldVolume, QuoteQuality: liveQuoteFixture(oldAt)},
	}, Now: now})
	if err != nil || len(result.Triggers) != 0 || !bytes.Equal(result.State, state) {
		t.Fatalf("bad contributing volume sample was skipped in favor of older history: state=%s err=%v", result.State, err)
	}
}
