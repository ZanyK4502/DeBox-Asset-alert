package marketrules

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestMarketNotificationSnapshotParamsCoverEveryDeliveryKind(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	current := "2500"
	rawPayload := json.RawMessage(`{"provider":"raw"}`)
	eventMetadata := json.RawMessage(`{"balance_before":"10","balance_after":"12"}`)
	ruleState := json.RawMessage(`{"cursor":"internal"}`)
	projectMetadata := json.RawMessage(`{"provider_cache":"large"}`)
	combinationRuleID := int64(303)
	watchRuleID := int64(404)
	marketRuleID := int64(405)

	tests := []struct {
		kind          string
		deliveryID    int64
		wantKind      string
		wantRuleType  string
		wantThreshold string
		wantActual    string
		delivery      store.MarketNotificationDelivery
	}{
		{
			kind:          "realtime",
			deliveryID:    101,
			wantKind:      store.NotificationKindMarketRealtime,
			wantRuleType:  "market_large_buy",
			wantThreshold: "2000 usd",
			wantActual:    current,
		},
		{
			kind:          "stage",
			deliveryID:    202,
			wantKind:      store.NotificationKindMarketStage,
			wantRuleType:  "market_large_buy",
			wantThreshold: "2000 usd;send_after=3",
			wantActual:    current,
		},
		{
			kind:          "combination",
			deliveryID:    303,
			wantKind:      store.NotificationKindMarketCombination,
			wantRuleType:  "market_combination",
			wantThreshold: "market_large_buy>=2@2000;outgoing>=1@10",
			wantActual:    "market_large_buy=2;outgoing=1",
			delivery: store.MarketNotificationDelivery{
				MarketCombinationRuleID: &combinationRuleID,
				CombinationMembers: []store.MarketCombinationProgress{
					{
						RuleType:             "market_large_buy",
						RequiredTriggerCount: 2,
						TriggerCount:         2,
						MarketRuleID:         &marketRuleID,
						MarketRule: &store.MarketRule{
							ID: marketRuleID, RuleType: "market_large_buy",
							ThresholdValue: "2000", State: ruleState,
						},
					},
					{
						RuleType:             "outgoing",
						RequiredTriggerCount: 1,
						TriggerCount:         1,
						WatchRuleID:          &watchRuleID,
						WatchRule: &store.WatchRule{
							ID: watchRuleID, RuleType: "outgoing", Threshold: "10",
						},
					},
				},
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.kind, func(t *testing.T) {
			t.Parallel()
			delivery := test.delivery
			delivery.Kind = test.kind
			delivery.ID = test.deliveryID
			delivery.DeBoxUserID = "user-1"
			delivery.NotificationChatID = "user-1"
			delivery.NotificationChatType = "private"
			delivery.NotificationLanguage = "zh"
			delivery.NotificationLabel = "主钱包"
			delivery.Project = store.MarketProject{
				ID: 9, TokenName: "Test", TokenSymbol: "TEST", Metadata: projectMetadata,
			}
			if test.kind != "combination" {
				delivery.Rule = &store.MarketRule{
					ID: 7, RuleType: "market_large_buy", ThresholdValue: "2000",
					ThresholdUnit: "usd", TriggerCountThreshold: 3, State: ruleState,
				}
				delivery.CurrentValue = &current
				delivery.Event = &store.MarketEvent{
					ID: 8, EventType: "buy", RawPayload: rawPayload, Metadata: eventMetadata,
					OccurredAt: now,
				}
				delivery.Snapshot = &store.MarketSnapshot{
					ID: 10, CapturedAt: now, RawPayload: rawPayload,
				}
			}

			params, err := marketNotificationSnapshotParams(delivery, "notification text")
			if err != nil {
				t.Fatalf("marketNotificationSnapshotParams(): %v", err)
			}
			if params.NotificationKind != test.wantKind ||
				params.SourceKey != fmt.Sprintf("%s:%d", test.wantKind, test.deliveryID) ||
				params.SourceType != "market_notification_delivery" ||
				params.SourceID == nil || *params.SourceID != test.deliveryID ||
				params.RuleType != test.wantRuleType ||
				params.RuleThreshold != test.wantThreshold ||
				params.ActualValue != test.wantActual ||
				params.NotificationText != "notification text" {
				t.Fatalf("snapshot params = %#v", params)
			}

			var payload marketNotificationSnapshotPayload
			if err := json.Unmarshal(params.Details, &payload); err != nil {
				t.Fatalf("decode snapshot details: %v", err)
			}
			if payload.SchemaVersion != 1 || payload.Delivery.ID != test.deliveryID {
				t.Fatalf("snapshot payload = %#v", payload)
			}
			if rawJSONPresent(payload.Delivery.Project.Metadata) {
				t.Fatal("provider project metadata must not be copied into the snapshot")
			}
			if payload.Delivery.Rule != nil && rawJSONPresent(payload.Delivery.Rule.State) {
				t.Fatal("mutable internal market rule state must not be copied")
			}
			if payload.Delivery.Event != nil {
				if rawJSONPresent(payload.Delivery.Event.RawPayload) {
					t.Fatal("raw provider event payload must not be copied")
				}
				if string(payload.Delivery.Event.Metadata) != string(eventMetadata) {
					t.Fatal("display metadata must remain available in the snapshot")
				}
			}
			if payload.Delivery.Snapshot != nil &&
				rawJSONPresent(payload.Delivery.Snapshot.RawPayload) {
				t.Fatal("raw provider market snapshot must not be copied")
			}
		})
	}
}

func rawJSONPresent(value json.RawMessage) bool {
	return len(value) > 0 && string(value) != "null"
}

func TestMarketNotificationSnapshotParamsRejectUnsupportedKind(t *testing.T) {
	t.Parallel()
	_, err := marketNotificationSnapshotParams(
		store.MarketNotificationDelivery{Kind: "unknown"},
		"notification text",
	)
	if err == nil {
		t.Fatal("unsupported delivery kind must fail")
	}
}
