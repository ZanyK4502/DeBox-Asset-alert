package marketrules

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestTradeLaneCommitsBoundedReadyPrefixesAndResumesWithoutSkippingParents(t *testing.T) {
	service, repository, rule := newTradeLaneTestService(true)
	rule.ThresholdUnit, rule.ThresholdValue, rule.CooldownSeconds = "token", "1", 0
	target := MergeTradeRuleTargets(repository.targets)[0]
	if _, err := service.InitializeTradeRuleState(context.Background(), rule.ID, target, 100); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 45; i++ {
		// Parent identity is deliberately opposite to publication-ready order.
		repository.tradeEvents = append(repository.tradeEvents, tradeFactsTestEvent(int64(1000-i), int64(101+i), service.now()))
	}
	for index, size := range []int64{16, 16, 13, 0} {
		created, err := service.evaluateRule(context.Background(), rule)
		if err != nil || created != size {
			t.Fatalf("page %d: created=%d want=%d err=%v", index, created, size, err)
		}
		var state tradeLaneState
		if err := json.Unmarshal(repository.states[target.TargetKey].State, &state); err != nil {
			t.Fatal(err)
		}
		wantReady := int64(100 + (index+1)*store.MarketRuleCurrentEventLimit)
		if wantReady > 145 {
			wantReady = 145
		}
		if state.LastTradeReadyID != wantReady || state.ActivationFence == nil || *state.ActivationFence != 100 {
			t.Fatalf("page %d advanced outside its committed prefix: %#v", index, state)
		}
	}
	if len(repository.created) != 45 {
		t.Fatalf("logical parent lost or duplicated across pages: %d", len(repository.created))
	}
	for i, event := range repository.created {
		if event.MarketEventID != int64(1000-i) {
			t.Fatalf("page order followed parent ID instead of publication order at %d: %+v", i, event)
		}
	}
}
