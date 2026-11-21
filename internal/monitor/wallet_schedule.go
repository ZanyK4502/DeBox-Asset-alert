package monitor

import "github.com/ZanyK4502/DeBox-Asset-alert/internal/store"

func walletObservationKey(rule store.WatchRule) *store.WalletObservationCursor {
	key := &store.WalletObservationCursor{RuleID: rule.ID}
	if rule.LastCheckedAt != nil {
		at := *rule.LastCheckedAt
		key.LastCheckedAt = &at
	}
	return key
}

func sameWalletObservationKey(a, b *store.WalletObservationCursor) bool {
	if a == nil || b == nil || a.RuleID != b.RuleID || (a.LastCheckedAt == nil) != (b.LastCheckedAt == nil) {
		return false
	}
	return a.LastCheckedAt == nil || a.LastCheckedAt.Equal(*b.LastCheckedAt)
}

func walletObservationRuleID(key *store.WalletObservationCursor) int64 {
	if key == nil {
		return 0
	}
	return key.RuleID
}
