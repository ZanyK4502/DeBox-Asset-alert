package notificationdetail

import (
	"encoding/json"
	"time"
)

type Detail struct {
	SchemaVersion    int             `json:"schema_version"`
	NotificationID   string          `json:"notification_id"`
	NotificationKind string          `json:"notification_kind"`
	Domain           string          `json:"domain"`
	DeliveryMode     string          `json:"delivery_mode"`
	AccessScope      string          `json:"access_scope"`
	Language         string          `json:"language"`
	Label            string          `json:"label,omitempty"`
	Rule             Rule            `json:"rule"`
	Target           Target          `json:"target"`
	ActualValue      string          `json:"actual_value,omitempty"`
	NotificationText string          `json:"notification_text"`
	Data             json.RawMessage `json:"data"`
	CopyValues       []CopyValue     `json:"copy_values"`
	Links            []Link          `json:"links"`
	CreatedAt        time.Time       `json:"created_at"`
	ExpiresAt        time.Time       `json:"expires_at"`
}

type Rule struct {
	ID        *int64 `json:"id,omitempty"`
	Type      string `json:"type,omitempty"`
	Name      string `json:"name,omitempty"`
	Threshold string `json:"threshold,omitempty"`
}

type Target struct {
	ChatType string `json:"chat_type"`
	Label    string `json:"label,omitempty"`
}

type CopyValue struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Value string `json:"value"`
	Path  string `json:"path"`
}

type Link struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	URL      string `json:"url"`
	Value    string `json:"value,omitempty"`
	ChainKey string `json:"chain_key,omitempty"`
}
