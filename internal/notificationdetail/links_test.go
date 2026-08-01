package notificationdetail

import (
	"encoding/json"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestTransactionExplorerURLSupportsEveryConfiguredChain(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"bsc":      "https://bscscan.com/tx/",
		"ethereum": "https://etherscan.io/tx/",
		"base":     "https://basescan.org/tx/",
		"polygon":  "https://polygonscan.com/tx/",
		"arbitrum": "https://arbiscan.io/tx/",
		"optimism": "https://optimistic.etherscan.io/tx/",
	}
	for chainKey, prefix := range tests {
		chainKey, prefix := chainKey, prefix
		t.Run(chainKey, func(t *testing.T) {
			t.Parallel()
			if got := transactionExplorerURL(chainKey, testTransaction); got != prefix+testTransaction {
				t.Fatalf("transactionExplorerURL() = %q", got)
			}
		})
	}
	if got := transactionExplorerURL("unknown", testTransaction); got != "" {
		t.Fatalf("unknown chain URL = %q", got)
	}
	if got := transactionExplorerURL("", testTransaction); got != "" {
		t.Fatalf("missing chain URL = %q", got)
	}
	if got := transactionExplorerURL("bsc", "invalid"); got != "" {
		t.Fatalf("invalid hash URL = %q", got)
	}
}

func TestDetailValuesDoNotTreatBlockHashAsTransactionHash(t *testing.T) {
	t.Parallel()
	values, links := detailValuesAndLinks(map[string]any{
		"chain_key":        "bsc",
		"block_hash":       testTransaction,
		"transaction_hash": "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}, "zh", false)
	if len(values) != 1 || values[0].Kind != "transaction_hash" || len(links) != 1 ||
		links[0].Value != "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("values/links = %#v/%#v", values, links)
	}
}

func TestManagementLinkSupportsEveryNotificationKind(t *testing.T) {
	t.Parallel()
	ruleID := int64(7)
	marketData := map[string]any{
		"delivery": map[string]any{
			"project": map[string]any{"id": json.Number("23")},
		},
	}
	tests := []struct {
		name string
		kind string
		id   *int64
		data map[string]any
		want string
	}{
		{"address realtime", store.NotificationKindAddressRealtime, &ruleID, nil, "https://alerts.example/app?rule_id=7&rule_type=address#activeRulesSection"},
		{"address stage", store.NotificationKindAddressStage, &ruleID, nil, "https://alerts.example/app?rule_id=7&rule_type=address#activeRulesSection"},
		{"address combination", store.NotificationKindAddressCombination, &ruleID, nil, "https://alerts.example/app?combination_id=7&rule_type=address_combination#activeRulesSection"},
		{"market realtime", store.NotificationKindMarketRealtime, &ruleID, marketData, "https://alerts.example/app?project_id=23&rule_id=7&rule_type=market#marketProjectsSection"},
		{"market stage", store.NotificationKindMarketStage, &ruleID, marketData, "https://alerts.example/app?project_id=23&rule_id=7&rule_type=market#marketProjectsSection"},
		{"market combination", store.NotificationKindMarketCombination, &ruleID, nil, "https://alerts.example/app?combination_id=7&rule_type=market_combination#marketProjectsSection"},
		{"daily summary", store.NotificationKindDailySummary, nil, nil, "https://alerts.example/app#summary"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			link := managementLink(
				"https://alerts.example/app",
				test.kind,
				test.id,
				test.data,
				"zh",
			)
			if link == nil || link.URL != test.want {
				t.Fatalf("managementLink() = %#v, want %q", link, test.want)
			}
		})
	}
}
