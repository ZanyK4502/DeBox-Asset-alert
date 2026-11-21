package chain

import (
	"encoding/json"
	"math"
	"strconv"
	"time"
)

// Nodit documents timestamp as Unix seconds of transaction creation, not the
// API fetch/ingestion time or an independently verified block timestamp.
func interactionSourceTime(transaction map[string]any) (*time.Time, string) {
	var seconds int64
	switch value := transaction["timestamp"].(type) {
	case int:
		seconds = int64(value)
	case int64:
		seconds = value
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return nil, ""
		}
		seconds = parsed
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value > 253402300799 || value <= 0 {
			return nil, ""
		}
		seconds = int64(value)
	case string:
		if len(value) == 0 || len(value) > 12 {
			return nil, ""
		}
		for _, digit := range value {
			if digit < '0' || digit > '9' {
				return nil, ""
			}
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, ""
		}
		seconds = parsed
	default:
		return nil, ""
	}
	if seconds <= 0 || seconds > 253402300799 {
		return nil, ""
	}
	at := time.Unix(seconds, 0).UTC()
	return &at, "nodit_transaction_created_at"
}
