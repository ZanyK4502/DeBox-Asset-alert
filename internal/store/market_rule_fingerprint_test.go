package store

import "testing"

func TestMarketRuleConfigurationFingerprintIgnoresOrderingAndFormatting(t *testing.T) {
	window := int32(15)
	first := CreateMarketRuleParams{
		DeBoxUserID:           "user-1",
		MarketProjectID:       7,
		RuleType:              "market_volume_spike",
		ThresholdValue:        "1.00",
		ThresholdUnit:         "ratio",
		WindowMinutes:         &window,
		CooldownSeconds:       300,
		RepeatWhileActive:     true,
		RuleScope:             "standalone",
		DeliveryMode:          "realtime",
		DeploymentScope:       "selected",
		PoolScope:             "selected",
		CooldownScope:         "chain",
		NotificationChatID:    "user-1",
		NotificationChatType:  "private",
		NotificationLanguage:  "zh",
		Sensitivity:           "balanced",
		CycleMinutes:          30,
		TriggerCountThreshold: 9,
		NotificationLabel:     "first",
	}
	second := first
	second.ThresholdValue = "1"
	second.NotificationLanguage = "en"
	second.NotificationLabel = "second"
	second.Sensitivity = "custom"
	second.CycleMinutes = 90
	second.TriggerCountThreshold = 99

	left := marketRuleConfigurationFingerprint(
		first,
		[]int64{4, 2, 4},
		[]int64{8, 6, 8},
	)
	right := marketRuleConfigurationFingerprint(
		second,
		[]int64{2, 4},
		[]int64{6, 8},
	)
	if left != right {
		t.Fatalf("equivalent market rules have different fingerprints")
	}
}

func TestMarketRuleConfigurationFingerprintIncludesEffectiveSettings(t *testing.T) {
	first := CreateMarketRuleParams{
		DeBoxUserID:          "user-1",
		MarketProjectID:      7,
		RuleType:             "market_price_above",
		ThresholdValue:       "1",
		ThresholdUnit:        "usd",
		CooldownSeconds:      300,
		RuleScope:            "standalone",
		DeliveryMode:         "realtime",
		DeploymentScope:      "all",
		PoolScope:            "primary",
		CooldownScope:        "chain",
		NotificationChatID:   "user-1",
		NotificationChatType: "private",
	}
	second := first
	second.ThresholdValue = "2"
	if marketRuleConfigurationFingerprint(first, nil, nil) ==
		marketRuleConfigurationFingerprint(second, nil, nil) {
		t.Fatalf("different thresholds must not be treated as duplicates")
	}
}

func TestMarketCombinationConfigurationFingerprintIgnoresMemberOrderAndNote(t *testing.T) {
	firstRuleID := int64(11)
	secondRuleID := int64(22)
	first := CreateMarketCombinationParams{
		DeBoxUserID:          "user-1",
		Note:                 "first note",
		CycleType:            "fixed",
		CycleMinutes:         15,
		NotificationChatID:   "user-1",
		NotificationChatType: "private",
		NotificationLanguage: "zh",
		Members: []CreateMarketCombinationMemberParams{
			{
				SourceType:           "market",
				MarketRuleID:         &firstRuleID,
				RequiredTriggerCount: 1,
			},
			{
				SourceType:           "market",
				MarketRuleID:         &secondRuleID,
				RequiredTriggerCount: 2,
			},
		},
	}
	second := first
	second.Note = "second note"
	second.NotificationLanguage = "en"
	second.Members = []CreateMarketCombinationMemberParams{
		first.Members[1],
		first.Members[0],
	}
	if marketCombinationConfigurationFingerprint(first) !=
		marketCombinationConfigurationFingerprint(second) {
		t.Fatalf("equivalent market combinations have different fingerprints")
	}
}

func TestMarketCombinationConfigurationFingerprintIncludesMemberCountAndTarget(t *testing.T) {
	firstRuleID := int64(11)
	secondRuleID := int64(22)
	first := CreateMarketCombinationParams{
		DeBoxUserID:          "user-1",
		CycleType:            "fixed",
		CycleMinutes:         15,
		NotificationChatID:   "user-1",
		NotificationChatType: "private",
		Members: []CreateMarketCombinationMemberParams{
			{SourceType: "market", MarketRuleID: &firstRuleID, RequiredTriggerCount: 1},
			{SourceType: "market", MarketRuleID: &secondRuleID, RequiredTriggerCount: 1},
		},
	}
	second := first
	second.Members = append(
		[]CreateMarketCombinationMemberParams(nil),
		first.Members...,
	)
	second.Members[1].RequiredTriggerCount = 2
	if marketCombinationConfigurationFingerprint(first) ==
		marketCombinationConfigurationFingerprint(second) {
		t.Fatalf("different member counts must not be treated as duplicates")
	}
	second = first
	second.NotificationChatType = "group"
	second.NotificationChatID = "group-1"
	if marketCombinationConfigurationFingerprint(first) ==
		marketCombinationConfigurationFingerprint(second) {
		t.Fatalf("different notification targets must not be treated as duplicates")
	}
}
