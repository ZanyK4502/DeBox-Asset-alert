package store

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestWalletBalanceSourceOptionalPayloadPreservesLegacyDelivery(t *testing.T) {
	const old = `{"chat_id":"user","chat_type":"private","text":"old frozen text","language":"zh","public_id":"nd_0000000000000000000000000000000000000000","action_text":"details","action_url":"https://example.invalid/detail"}`
	var payload WalletNotificationPayload
	if err := json.Unmarshal([]byte(old), &payload); err != nil || payload.Validate() != nil {
		t.Fatalf("legacy frozen payload rejected: decode=%v validate=%v", err, payload.Validate())
	}
	if payload.BalanceReadSource != "" || payload.BalanceReadDecimals != nil {
		t.Fatal("legacy payload invented source evidence")
	}
	encoded, err := json.Marshal(payload)
	if err != nil || strings.Contains(string(encoded), "balance_read_") {
		t.Fatalf("legacy payload gained optional evidence: %s error=%v", encoded, err)
	}
}

func TestWalletBalanceSourceValuesRequireARepresentableUint256(t *testing.T) {
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1)).String()
	overflow := new(big.Int).Lsh(big.NewInt(1), 256).String()
	for _, tc := range []struct {
		value    string
		decimals int
		valid    bool
	}{
		{"0", 0, true}, {"42", 0, true}, {"0.000001", 6, true}, {"1.23456789", 8, true},
		{"0." + strings.Repeat("0", 254) + "1", 255, true}, {max, 0, true},
		{overflow, 0, false}, {"1.1", 0, false}, {"0.0000001", 6, false},
		{"1e18", 18, false}, {"1/2", 18, false}, {"-1", 18, false}, {"+1", 18, false},
		{".1", 18, false}, {"1.", 18, false}, {" 0", 18, false}, {"", 18, false},
		{"0", -1, false}, {"0", 256, false}, {strings.Repeat("0", 336), 18, false},
	} {
		if got := walletBalanceValueValid(tc.value, tc.decimals); got != tc.valid {
			t.Fatalf("value=%q decimals=%d accepted=%v want=%v", tc.value, tc.decimals, got, tc.valid)
		}
	}
}

func TestWalletBalanceSourceEvidenceDoesNotApplyToOtherRuleKinds(t *testing.T) {
	token := "0x2222222222222222222222222222222222222222"
	decimals, observed := 0, time.Now().UTC()
	rule := WatchRule{ChainKey: "bsc", ChainID: 56, WalletAddress: "0x1111111111111111111111111111111111111111", TokenAddress: &token, RuleType: "balance_change"}
	if !walletBalanceEvidenceValid(rule, WalletBalanceReadSource, &decimals, &observed, "42") {
		t.Fatal("valid zero-decimal evidence rejected")
	}
	for _, kind := range []string{"approval_change", "address_interaction", "unsupported"} {
		rule.RuleType = kind
		if walletBalanceEvidenceValid(rule, WalletBalanceReadSource, &decimals, &observed, "42") {
			t.Fatalf("source evidence accepted for %s", kind)
		}
	}
	rule.RuleType, rule.TokenAddress = "balance_change", nil
	if walletBalanceEvidenceValid(rule, WalletBalanceReadSource, &decimals, &observed, "42") {
		t.Fatal("ERC20 evidence accepted for native balance")
	}
	rule.TokenAddress = &token
	for _, source := range []string{"", "unverified", strings.Repeat("x", 100)} {
		if walletBalanceEvidenceValid(rule, source, &decimals, &observed, "42") {
			t.Fatal("unknown source accepted")
		}
	}
	if walletBalanceEvidenceValid(rule, WalletBalanceReadSource, nil, &observed, "42") || walletBalanceEvidenceValid(rule, WalletBalanceReadSource, &decimals, nil, "42") {
		t.Fatal("missing precision or read time accepted")
	}
}
