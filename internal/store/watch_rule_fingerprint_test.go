package store

import "testing"

func TestWatchRuleConfigurationFingerprintIgnoresPresentationFields(t *testing.T) {
	t.Parallel()

	token := "0x2222222222222222222222222222222222222222"
	target := "0x3333333333333333333333333333333333333333"
	first := CreateWatchRuleParams{
		ChainKey:              "bsc",
		WalletAddress:         "0x1111111111111111111111111111111111111111",
		TokenAddress:          &token,
		TargetAddress:         &target,
		TargetLabel:           "Treasury",
		RuleType:              "incoming",
		Threshold:             "1.00",
		NotificationChatID:    "user-1",
		NotificationChatType:  "private",
		NotificationLanguage:  "zh",
		DeliveryMode:          "realtime",
		CycleType:             "fixed",
		CycleMinutes:          60,
		TriggerCountThreshold: 1,
	}
	second := first
	second.ChainKey = " BSC "
	second.WalletAddress = "0X1111111111111111111111111111111111111111"
	second.TargetLabel = "Updated label"
	second.Threshold = "1"
	second.NotificationLanguage = "en"
	second.CycleType = "follow"
	second.CycleMinutes = 120

	if watchRuleConfigurationFingerprint(first, true) !=
		watchRuleConfigurationFingerprint(second, true) {
		t.Fatal("equivalent real-time watch rules have different fingerprints")
	}

	otherTarget := "0x4444444444444444444444444444444444444444"
	second.TargetAddress = &otherTarget
	if watchRuleConfigurationFingerprint(first, true) ==
		watchRuleConfigurationFingerprint(second, true) {
		t.Fatal("different target addresses must not be treated as duplicates")
	}
}

func TestWatchRuleConfigurationFingerprintIncludesStageSettingsAndTarget(t *testing.T) {
	t.Parallel()

	first := CreateWatchRuleParams{
		ChainKey:              "ethereum",
		WalletAddress:         "0x1111111111111111111111111111111111111111",
		RuleType:              "balance_change",
		Threshold:             "0",
		NotificationChatID:    "user-1",
		NotificationChatType:  "private",
		DeliveryMode:          "stage",
		CycleType:             "fixed",
		CycleMinutes:          30,
		TriggerCountThreshold: 2,
	}
	second := first
	second.CycleMinutes = 60
	if watchRuleConfigurationFingerprint(first, true) ==
		watchRuleConfigurationFingerprint(second, true) {
		t.Fatal("different stage cycles must not be treated as duplicates")
	}

	second = first
	second.NotificationChatID = "group-1"
	second.NotificationChatType = "group"
	if watchRuleConfigurationFingerprint(first, true) ==
		watchRuleConfigurationFingerprint(second, true) {
		t.Fatal("different notification targets must not be treated as duplicates")
	}
}

func TestCombinationRuleConfigurationFingerprintIgnoresOrderAndNotes(t *testing.T) {
	t.Parallel()

	memberOne := CreateWatchRuleParams{
		ChainKey:      "bsc",
		WalletAddress: "0x1111111111111111111111111111111111111111",
		RuleType:      "incoming",
		Threshold:     "1",
		DeliveryMode:  "realtime",
		TargetLabel:   "first label",
	}
	memberTwo := CreateWatchRuleParams{
		ChainKey:      "ethereum",
		WalletAddress: "0x2222222222222222222222222222222222222222",
		RuleType:      "outgoing",
		Threshold:     "2",
		DeliveryMode:  "realtime",
		TargetLabel:   "second label",
	}
	first := CreateCombinationRuleParams{
		Note:                 "first note",
		CycleType:            "fixed",
		CycleMinutes:         60,
		NotificationChatID:   "user-1",
		NotificationChatType: "private",
		Members: []CreateCombinationMemberParams{
			{Rule: memberOne, RequiredTriggerCount: 1},
			{Rule: memberTwo, RequiredTriggerCount: 2},
		},
	}
	second := first
	second.Note = "updated note"
	second.Members = []CreateCombinationMemberParams{
		{Rule: memberTwo, RequiredTriggerCount: 2},
		{Rule: memberOne, RequiredTriggerCount: 1},
	}

	if combinationRuleConfigurationFingerprint(first) !=
		combinationRuleConfigurationFingerprint(second) {
		t.Fatal("equivalent combination rules have different fingerprints")
	}

	second.Members[0].RequiredTriggerCount = 3
	if combinationRuleConfigurationFingerprint(first) ==
		combinationRuleConfigurationFingerprint(second) {
		t.Fatal("different member trigger counts must not be treated as duplicates")
	}
}
