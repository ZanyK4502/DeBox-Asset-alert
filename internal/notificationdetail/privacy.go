package notificationdetail

import "strings"

var alwaysHiddenDetailKeys = map[string]struct{}{
	"debox_user_id":              {},
	"subscription_id":            {},
	"notification_chat_id":       {},
	"notification_message_id":    {},
	"notification_error":         {},
	"notification_attempts":      {},
	"notification_attempted_at":  {},
	"notification_sent_at":       {},
	"effective_plan_code":        {},
	"entitlement_source":         {},
	"entitlement_wallet_address": {},
	"state":                      {},
	"raw_payload":                {},
	"verification_evidence":      {},
}

func sanitizeDetailValue(value any, group bool) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized := strings.ToLower(strings.TrimSpace(key))
			if _, hidden := alwaysHiddenDetailKeys[normalized]; hidden {
				continue
			}
			if group && groupPrivateDetailKey(normalized) {
				continue
			}
			result[key] = sanitizeDetailValue(item, group)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = sanitizeDetailValue(item, group)
		}
		return result
	default:
		return value
	}
}

func groupPrivateDetailKey(key string) bool {
	switch key {
	case "chat_id", "wallet", "wallet_address", "target_address",
		"holder_address", "address_label", "target_label", "notification_label",
		"last_value", "note", "recent_notes", "id":
		return true
	}
	return strings.Contains(key, "wallet_address") ||
		strings.Contains(key, "holder_address") ||
		strings.HasSuffix(key, "_chat_id") ||
		(strings.HasSuffix(key, "_id") && key != "chain_id")
}
