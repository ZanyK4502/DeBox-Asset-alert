package store

import "time"

type Subscription struct {
	ID                          int64      `db:"id" json:"id"`
	DeBoxUserID                 string     `db:"debox_user_id" json:"debox_user_id"`
	PlanCode                    string     `db:"plan_code" json:"plan_code"`
	Status                      string     `db:"status" json:"status"`
	StartsAt                    time.Time  `db:"starts_at" json:"starts_at"`
	ExpiresAt                   time.Time  `db:"expires_at" json:"expires_at"`
	DailySummaryEnabled         int32      `db:"daily_summary_enabled" json:"daily_summary_enabled"`
	DailySummaryTime            string     `db:"daily_summary_time" json:"daily_summary_time"`
	DailySummaryTimezone        string     `db:"daily_summary_timezone" json:"daily_summary_timezone"`
	DailySummaryChatType        string     `db:"daily_summary_chat_type" json:"daily_summary_chat_type"`
	DailySummaryChatID          string     `db:"daily_summary_chat_id" json:"daily_summary_chat_id"`
	DailySummaryLabel           string     `db:"daily_summary_label" json:"daily_summary_label"`
	DailySummaryLanguage        string     `db:"daily_summary_language" json:"daily_summary_language"`
	DailySummaryLastSentDate    string     `db:"daily_summary_last_sent_date" json:"daily_summary_last_sent_date"`
	ScheduledPushLastSentAt     *time.Time `db:"scheduled_push_last_sent_at" json:"scheduled_push_last_sent_at"`
	DailySummaryLastPeriodEndAt *time.Time `db:"daily_summary_last_period_end_at" json:"daily_summary_last_period_end_at"`
	CreatedAt                   time.Time  `db:"created_at" json:"created_at"`
}

type WatchRule struct {
	ID                   int64      `db:"id" json:"id"`
	DeBoxUserID          string     `db:"debox_user_id" json:"debox_user_id"`
	ChainKey             string     `db:"chain_key" json:"chain_key"`
	ChainID              int32      `db:"chain_id" json:"chain_id"`
	WalletAddress        string     `db:"wallet_address" json:"wallet_address"`
	TokenAddress         *string    `db:"token_address" json:"token_address"`
	TargetAddress        *string    `db:"target_address" json:"target_address"`
	TargetLabel          string     `db:"target_label" json:"target_label"`
	RuleType             string     `db:"rule_type" json:"rule_type"`
	Threshold            string     `db:"threshold" json:"threshold"`
	NotificationChatID   string     `db:"notification_chat_id" json:"notification_chat_id"`
	NotificationChatType string     `db:"notification_chat_type" json:"notification_chat_type"`
	NotificationLabel    string     `db:"notification_label" json:"notification_label"`
	NotificationLanguage string     `db:"notification_language" json:"notification_language"`
	Enabled              int32      `db:"enabled" json:"enabled"`
	RunStatus            string     `db:"run_status" json:"run_status"`
	LastValue            *string    `db:"last_value" json:"last_value"`
	LastCheckedAt        *time.Time `db:"last_checked_at" json:"last_checked_at"`
	CreatedAt            time.Time  `db:"created_at" json:"created_at"`
	EffectivePlanCode    string     `db:"effective_plan_code" json:"effective_plan_code,omitempty"`
}

type Order struct {
	ID                int64      `db:"id" json:"id"`
	DeBoxUserID       string     `db:"debox_user_id" json:"debox_user_id"`
	PayerAddress      string     `db:"payer_address" json:"payer_address"`
	PlanCode          string     `db:"plan_code" json:"plan_code"`
	ChainKey          string     `db:"chain_key" json:"chain_key"`
	ChainID           int32      `db:"chain_id" json:"chain_id"`
	TokenAddress      *string    `db:"token_address" json:"token_address"`
	TokenSymbol       string     `db:"token_symbol" json:"token_symbol"`
	TokenDecimals     int32      `db:"token_decimals" json:"token_decimals"`
	TotalAmount       string     `db:"total_amount" json:"total_amount"`
	RecipientAddress  string     `db:"recipient_address" json:"recipient_address"`
	TxHash            *string    `db:"tx_hash" json:"tx_hash"`
	Status            string     `db:"status" json:"status"`
	CreatedAt         time.Time  `db:"created_at" json:"created_at"`
	ExpiresAt         time.Time  `db:"expires_at" json:"expires_at"`
	CompletedAt       *time.Time `db:"completed_at" json:"completed_at"`
	TxBlockNumber     *int64     `db:"tx_block_number" json:"tx_block_number"`
	TxConfirmations   int32      `db:"tx_confirmations" json:"tx_confirmations"`
	VerifiedAt        *time.Time `db:"verified_at" json:"verified_at"`
	VerificationError string     `db:"verification_error" json:"verification_error"`
}

type AlertEvent struct {
	ID                      int64      `db:"id" json:"id"`
	WatchRuleID             int64      `db:"watch_rule_id" json:"watch_rule_id"`
	EventType               string     `db:"event_type" json:"event_type"`
	PreviousValue           *string    `db:"previous_value" json:"previous_value"`
	CurrentValue            *string    `db:"current_value" json:"current_value"`
	NotificationMessageID   *string    `db:"notification_message_id" json:"notification_message_id"`
	NotificationStatus      string     `db:"notification_status" json:"notification_status"`
	NotificationError       string     `db:"notification_error" json:"notification_error"`
	NotificationAttempts    int32      `db:"notification_attempts" json:"notification_attempts"`
	NotificationAttemptedAt *time.Time `db:"notification_attempted_at" json:"notification_attempted_at"`
	NotificationSentAt      *time.Time `db:"notification_sent_at" json:"notification_sent_at"`
	CreatedAt               time.Time  `db:"created_at" json:"created_at"`
}

type NotificationGroup struct {
	ID          int64     `db:"id" json:"id"`
	DeBoxUserID string    `db:"debox_user_id" json:"debox_user_id"`
	GID         string    `db:"gid" json:"gid"`
	Name        string    `db:"name" json:"name"`
	Enabled     int32     `db:"enabled" json:"enabled"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
}

type UserPreference struct {
	DeBoxUserID     string     `db:"debox_user_id" json:"debox_user_id"`
	FreeWatchRuleID *int64     `db:"free_watch_rule_id" json:"free_watch_rule_id"`
	BotLanguage     string     `db:"bot_language" json:"bot_language"`
	UpdatedAt       *time.Time `db:"updated_at" json:"updated_at,omitempty"`
}

type AuthChallenge struct {
	ChallengeID   string     `db:"challenge_id" json:"challenge_id"`
	WalletAddress string     `db:"wallet_address" json:"wallet_address"`
	NonceHash     string     `db:"nonce_hash" json:"nonce_hash"`
	Message       string     `db:"message" json:"message"`
	ExpiresAt     time.Time  `db:"expires_at" json:"expires_at"`
	UsedAt        *time.Time `db:"used_at" json:"used_at"`
	CreatedAt     time.Time  `db:"created_at" json:"created_at"`
}

type AuthSession struct {
	TokenHash     string     `db:"token_hash" json:"token_hash"`
	DeBoxUserID   string     `db:"debox_user_id" json:"debox_user_id"`
	WalletAddress string     `db:"wallet_address" json:"wallet_address"`
	ExpiresAt     time.Time  `db:"expires_at" json:"expires_at"`
	RevokedAt     *time.Time `db:"revoked_at" json:"revoked_at"`
	LastSeenAt    time.Time  `db:"last_seen_at" json:"last_seen_at"`
	CreatedAt     time.Time  `db:"created_at" json:"created_at"`
}

type ComplimentaryGrant struct {
	WalletAddress string    `db:"wallet_address" json:"wallet_address"`
	DeBoxUserID   string    `db:"debox_user_id" json:"debox_user_id"`
	PlanCode      string    `db:"plan_code" json:"plan_code"`
	StartsAt      time.Time `db:"starts_at" json:"starts_at"`
	ExpiresAt     time.Time `db:"expires_at" json:"expires_at"`
	CreatedAt     time.Time `db:"created_at" json:"created_at"`
}

type SummaryStatistics struct {
	RuleCount               int64 `db:"rule_count" json:"rule_count"`
	WalletCount             int64 `db:"wallet_count" json:"wallet_count"`
	AssetRuleCount          int64 `db:"asset_rule_count" json:"asset_rule_count"`
	ApprovalRuleCount       int64 `db:"approval_rule_count" json:"approval_rule_count"`
	InteractionRuleCount    int64 `db:"interaction_rule_count" json:"interaction_rule_count"`
	EventCount              int64 `db:"event_count" json:"event_count"`
	AssetEventCount         int64 `db:"asset_event_count" json:"asset_event_count"`
	ApprovalEventCount      int64 `db:"approval_event_count" json:"approval_event_count"`
	InteractionEventCount   int64 `db:"interaction_event_count" json:"interaction_event_count"`
	FailedNotificationCount int64 `db:"failed_notification_count" json:"failed_notification_count"`
}

type SummaryEvent struct {
	ID                      int64      `db:"id" json:"id"`
	WatchRuleID             int64      `db:"watch_rule_id" json:"watch_rule_id"`
	EventType               string     `db:"event_type" json:"event_type"`
	PreviousValue           *string    `db:"previous_value" json:"previous_value"`
	CurrentValue            *string    `db:"current_value" json:"current_value"`
	NotificationMessageID   *string    `db:"notification_message_id" json:"notification_message_id"`
	NotificationStatus      string     `db:"notification_status" json:"notification_status"`
	NotificationError       string     `db:"notification_error" json:"notification_error"`
	NotificationAttempts    int32      `db:"notification_attempts" json:"notification_attempts"`
	NotificationAttemptedAt *time.Time `db:"notification_attempted_at" json:"notification_attempted_at"`
	NotificationSentAt      *time.Time `db:"notification_sent_at" json:"notification_sent_at"`
	CreatedAt               time.Time  `db:"created_at" json:"created_at"`
	ChainKey                string     `db:"chain_key" json:"chain_key"`
	WalletAddress           string     `db:"wallet_address" json:"wallet_address"`
	TokenAddress            *string    `db:"token_address" json:"token_address"`
	RuleType                string     `db:"rule_type" json:"rule_type"`
	TargetAddress           *string    `db:"target_address" json:"target_address"`
}
