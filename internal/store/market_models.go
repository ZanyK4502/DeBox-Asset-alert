package store

import (
	"encoding/json"
	"time"
)

type MarketAsset struct {
	ID                 int64           `db:"id" json:"id"`
	CanonicalName      string          `db:"canonical_name" json:"canonical_name"`
	Symbol             string          `db:"symbol" json:"symbol"`
	LogoURL            string          `db:"logo_url" json:"logo_url"`
	IdentitySource     string          `db:"identity_source" json:"identity_source"`
	CanonicalAssetID   string          `db:"canonical_asset_id" json:"canonical_asset_id"`
	VerificationStatus string          `db:"verification_status" json:"verification_status"`
	Metadata           json.RawMessage `db:"metadata" json:"metadata"`
	CreatedAt          time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt          time.Time       `db:"updated_at" json:"updated_at"`
}

type MarketAssetDeployment struct {
	ID                   int64           `db:"id" json:"id"`
	MarketAssetID        int64           `db:"market_asset_id" json:"market_asset_id"`
	ChainKey             string          `db:"chain_key" json:"chain_key"`
	ChainID              int64           `db:"chain_id" json:"chain_id"`
	TokenAddress         string          `db:"token_address" json:"token_address"`
	TokenName            string          `db:"token_name" json:"token_name"`
	TokenSymbol          string          `db:"token_symbol" json:"token_symbol"`
	TokenDecimals        int32           `db:"token_decimals" json:"token_decimals"`
	TotalSupplyRaw       *string         `db:"total_supply_raw" json:"total_supply_raw"`
	VerificationStatus   string          `db:"verification_status" json:"verification_status"`
	VerificationSource   string          `db:"verification_source" json:"verification_source"`
	VerificationEvidence json.RawMessage `db:"verification_evidence" json:"verification_evidence"`
	DefaultMarketPoolID  *int64          `db:"default_market_pool_id" json:"default_market_pool_id"`
	Metadata             json.RawMessage `db:"metadata" json:"metadata"`
	VerifiedAt           *time.Time      `db:"verified_at" json:"verified_at"`
	CreatedAt            time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt            time.Time       `db:"updated_at" json:"updated_at"`
}

type MarketAssetIdentityEvidence struct {
	ID                      int64           `db:"id" json:"id"`
	MarketAssetID           int64           `db:"market_asset_id" json:"market_asset_id"`
	MarketAssetDeploymentID *int64          `db:"market_asset_deployment_id" json:"market_asset_deployment_id"`
	EvidenceKey             string          `db:"evidence_key" json:"evidence_key"`
	Source                  string          `db:"source" json:"source"`
	EvidenceType            string          `db:"evidence_type" json:"evidence_type"`
	ExternalAssetID         string          `db:"external_asset_id" json:"external_asset_id"`
	Verdict                 string          `db:"verdict" json:"verdict"`
	Confidence              string          `db:"confidence" json:"confidence"`
	Payload                 json.RawMessage `db:"payload" json:"payload"`
	ObservedAt              time.Time       `db:"observed_at" json:"observed_at"`
	CreatedAt               time.Time       `db:"created_at" json:"created_at"`
}

type MarketProjectDeployment struct {
	ID                      int64           `db:"id" json:"id"`
	MarketProjectID         int64           `db:"market_project_id" json:"market_project_id"`
	MarketAssetID           int64           `db:"market_asset_id" json:"market_asset_id"`
	MarketAssetDeploymentID int64           `db:"market_asset_deployment_id" json:"market_asset_deployment_id"`
	Status                  string          `db:"status" json:"status"`
	PauseReason             string          `db:"pause_reason" json:"pause_reason"`
	DefaultMarketPoolID     *int64          `db:"default_market_pool_id" json:"default_market_pool_id"`
	Metadata                json.RawMessage `db:"metadata" json:"metadata"`
	CreatedAt               time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt               time.Time       `db:"updated_at" json:"updated_at"`
}

type MarketProjectDeploymentView struct {
	MarketProjectDeployment
	ChainKey             string          `db:"chain_key" json:"chain_key"`
	ChainID              int64           `db:"chain_id" json:"chain_id"`
	TokenAddress         string          `db:"token_address" json:"token_address"`
	TokenName            string          `db:"token_name" json:"token_name"`
	TokenSymbol          string          `db:"token_symbol" json:"token_symbol"`
	TokenDecimals        int32           `db:"token_decimals" json:"token_decimals"`
	VerificationStatus   string          `db:"verification_status" json:"verification_status"`
	VerificationSource   string          `db:"verification_source" json:"verification_source"`
	VerificationEvidence json.RawMessage `db:"verification_evidence" json:"verification_evidence"`
	VerifiedAt           *time.Time      `db:"verified_at" json:"verified_at,omitempty"`
}

type MarketRuleDeploymentScope struct {
	MarketRuleID              int64     `db:"market_rule_id" json:"market_rule_id"`
	MarketProjectDeploymentID int64     `db:"market_project_deployment_id" json:"market_project_deployment_id"`
	CreatedAt                 time.Time `db:"created_at" json:"created_at"`
}

type MarketRulePoolScope struct {
	MarketRuleID        int64     `db:"market_rule_id" json:"market_rule_id"`
	MarketProjectPoolID int64     `db:"market_project_pool_id" json:"market_project_pool_id"`
	CreatedAt           time.Time `db:"created_at" json:"created_at"`
}

type MarketRuleTarget struct {
	TargetKey                 string          `db:"target_key" json:"target_key"`
	MarketProjectDeploymentID *int64          `db:"market_project_deployment_id" json:"market_project_deployment_id,omitempty"`
	MarketProjectPoolID       *int64          `db:"market_project_pool_id" json:"market_project_pool_id,omitempty"`
	MarketPoolID              *int64          `db:"market_pool_id" json:"market_pool_id,omitempty"`
	ChainKey                  string          `db:"chain_key" json:"chain_key"`
	ChainID                   int64           `db:"chain_id" json:"chain_id"`
	TokenAddress              string          `db:"token_address" json:"token_address"`
	TokenName                 string          `db:"token_name" json:"token_name"`
	TokenSymbol               string          `db:"token_symbol" json:"token_symbol"`
	TokenDecimals             int32           `db:"token_decimals" json:"token_decimals"`
	State                     json.RawMessage `db:"state" json:"state"`
	LastEvaluatedAt           *time.Time      `db:"last_evaluated_at" json:"last_evaluated_at,omitempty"`
	LastTriggeredAt           *time.Time      `db:"last_triggered_at" json:"last_triggered_at,omitempty"`
}

type MarketCombinationRuleProject struct {
	MarketCombinationRuleID int64     `db:"market_combination_rule_id" json:"market_combination_rule_id"`
	MarketProjectID         int64     `db:"market_project_id" json:"market_project_id"`
	CreatedAt               time.Time `db:"created_at" json:"created_at"`
}

type MarketProject struct {
	ID                        int64           `db:"id" json:"id"`
	DeBoxUserID               string          `db:"debox_user_id" json:"debox_user_id"`
	MarketAssetID             *int64          `db:"market_asset_id" json:"market_asset_id,omitempty"`
	IdentitySource            string          `db:"identity_source" json:"identity_source,omitempty"`
	CanonicalAssetID          string          `db:"canonical_asset_id" json:"canonical_asset_id,omitempty"`
	MarketAssetDeploymentID   *int64          `db:"market_asset_deployment_id" json:"market_asset_deployment_id,omitempty"`
	MarketProjectDeploymentID *int64          `db:"market_project_deployment_id" json:"market_project_deployment_id,omitempty"`
	ChainKey                  string          `db:"chain_key" json:"chain_key"`
	ChainID                   int64           `db:"chain_id" json:"chain_id"`
	TokenAddress              string          `db:"token_address" json:"token_address"`
	TokenName                 string          `db:"token_name" json:"token_name"`
	TokenSymbol               string          `db:"token_symbol" json:"token_symbol"`
	TokenDecimals             int32           `db:"token_decimals" json:"token_decimals"`
	TotalSupplyRaw            *string         `db:"total_supply_raw" json:"total_supply_raw"`
	Status                    string          `db:"status" json:"status"`
	PauseReason               string          `db:"pause_reason" json:"pause_reason"`
	FourMemeStatus            string          `db:"four_meme_status" json:"four_meme_status"`
	MainPoolID                *int64          `db:"main_pool_id" json:"main_pool_id"`
	Metadata                  json.RawMessage `db:"metadata" json:"metadata"`
	LastDiscoveredAt          *time.Time      `db:"last_discovered_at" json:"last_discovered_at"`
	CreatedAt                 time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt                 time.Time       `db:"updated_at" json:"updated_at"`
}

type MarketPool struct {
	ID                   int64           `db:"id" json:"id"`
	ChainKey             string          `db:"chain_key" json:"chain_key"`
	ChainID              int64           `db:"chain_id" json:"chain_id"`
	Protocol             string          `db:"protocol" json:"protocol"`
	ProtocolVersion      string          `db:"protocol_version" json:"protocol_version"`
	PoolKey              string          `db:"pool_key" json:"pool_key"`
	PoolAddress          *string         `db:"pool_address" json:"pool_address"`
	FactoryAddress       *string         `db:"factory_address" json:"factory_address"`
	FactoryVerified      int32           `db:"factory_verified" json:"factory_verified"`
	Token0Address        string          `db:"token0_address" json:"token0_address"`
	Token0Symbol         string          `db:"token0_symbol" json:"token0_symbol"`
	Token0Decimals       int32           `db:"token0_decimals" json:"token0_decimals"`
	Token1Address        string          `db:"token1_address" json:"token1_address"`
	Token1Symbol         string          `db:"token1_symbol" json:"token1_symbol"`
	Token1Decimals       int32           `db:"token1_decimals" json:"token1_decimals"`
	LiquidityUSD         string          `db:"liquidity_usd" json:"liquidity_usd"`
	SupportsEventParsing int32           `db:"supports_event_parsing" json:"supports_event_parsing"`
	ParserAdapter        string          `db:"parser_adapter" json:"parser_adapter"`
	VerificationStatus   string          `db:"verification_status" json:"verification_status"`
	Metadata             json.RawMessage `db:"metadata" json:"metadata"`
	FirstSeenAt          time.Time       `db:"first_seen_at" json:"first_seen_at"`
	LastSeenAt           time.Time       `db:"last_seen_at" json:"last_seen_at"`
	CreatedAt            time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt            time.Time       `db:"updated_at" json:"updated_at"`
}

type MarketProjectPool struct {
	ID                        int64     `db:"id" json:"id"`
	MarketProjectID           int64     `db:"market_project_id" json:"market_project_id"`
	MarketProjectDeploymentID *int64    `db:"market_project_deployment_id" json:"market_project_deployment_id,omitempty"`
	MarketPoolID              int64     `db:"market_pool_id" json:"market_pool_id"`
	Selected                  int32     `db:"selected" json:"selected"`
	IsPrimary                 int32     `db:"is_primary" json:"is_primary"`
	DiscoverySource           string    `db:"discovery_source" json:"discovery_source"`
	CreatedAt                 time.Time `db:"created_at" json:"created_at"`
	UpdatedAt                 time.Time `db:"updated_at" json:"updated_at"`
}

type MarketPoolView struct {
	MarketPool
	Selected        int32  `db:"selected" json:"selected"`
	IsPrimary       int32  `db:"is_primary" json:"is_primary"`
	DiscoverySource string `db:"discovery_source" json:"discovery_source"`
}

type MarketSnapshot struct {
	ID                      int64           `db:"id" json:"id"`
	MarketAssetDeploymentID *int64          `db:"market_asset_deployment_id" json:"market_asset_deployment_id,omitempty"`
	ChainKey                string          `db:"chain_key" json:"chain_key"`
	ChainID                 int64           `db:"chain_id" json:"chain_id"`
	TokenAddress            string          `db:"token_address" json:"token_address"`
	MarketPoolID            int64           `db:"market_pool_id" json:"market_pool_id"`
	PriceUSD                *string         `db:"price_usd" json:"price_usd"`
	LiquidityUSD            *string         `db:"liquidity_usd" json:"liquidity_usd"`
	FDVUSD                  *string         `db:"fdv_usd" json:"fdv_usd"`
	MarketCapUSD            *string         `db:"market_cap_usd" json:"market_cap_usd"`
	Volume5mUSD             *string         `db:"volume_5m_usd" json:"volume_5m_usd"`
	Volume15mUSD            *string         `db:"volume_15m_usd" json:"volume_15m_usd"`
	Volume1hUSD             *string         `db:"volume_1h_usd" json:"volume_1h_usd"`
	Volume6hUSD             *string         `db:"volume_6h_usd" json:"volume_6h_usd"`
	Volume24hUSD            *string         `db:"volume_24h_usd" json:"volume_24h_usd"`
	Buys5m                  *int64          `db:"buys_5m" json:"buys_5m"`
	Sells5m                 *int64          `db:"sells_5m" json:"sells_5m"`
	Buys1h                  *int64          `db:"buys_1h" json:"buys_1h"`
	Sells1h                 *int64          `db:"sells_1h" json:"sells_1h"`
	Buys24h                 *int64          `db:"buys_24h" json:"buys_24h"`
	Sells24h                *int64          `db:"sells_24h" json:"sells_24h"`
	Source                  string          `db:"source" json:"source"`
	SourceTimestamp         *time.Time      `db:"source_timestamp" json:"source_timestamp"`
	CapturedAt              time.Time       `db:"captured_at" json:"captured_at"`
	RawPayload              json.RawMessage `db:"raw_payload" json:"raw_payload"`
}

type MarketRule struct {
	ID                    int64           `db:"id" json:"id"`
	DeBoxUserID           string          `db:"debox_user_id" json:"debox_user_id"`
	MarketProjectID       int64           `db:"market_project_id" json:"market_project_id"`
	MarketPoolID          *int64          `db:"market_pool_id" json:"market_pool_id"`
	RuleType              string          `db:"rule_type" json:"rule_type"`
	ThresholdValue        string          `db:"threshold_value" json:"threshold_value"`
	ThresholdUnit         string          `db:"threshold_unit" json:"threshold_unit"`
	WindowMinutes         *int32          `db:"window_minutes" json:"window_minutes"`
	Sensitivity           string          `db:"sensitivity" json:"sensitivity"`
	CooldownSeconds       int32           `db:"cooldown_seconds" json:"cooldown_seconds"`
	RepeatWhileActive     bool            `db:"repeat_while_active" json:"repeat_while_active"`
	RuleScope             string          `db:"rule_scope" json:"rule_scope"`
	DeploymentScope       string          `db:"deployment_scope" json:"deployment_scope"`
	PoolScope             string          `db:"pool_scope" json:"pool_scope"`
	CooldownScope         string          `db:"cooldown_scope" json:"cooldown_scope"`
	DeliveryMode          string          `db:"delivery_mode" json:"delivery_mode"`
	CycleType             string          `db:"cycle_type" json:"cycle_type"`
	CycleMinutes          int32           `db:"cycle_minutes" json:"cycle_minutes"`
	TriggerCountThreshold int64           `db:"trigger_count_threshold" json:"trigger_count_threshold"`
	NotificationChatID    string          `db:"notification_chat_id" json:"notification_chat_id"`
	NotificationChatType  string          `db:"notification_chat_type" json:"notification_chat_type"`
	NotificationLabel     string          `db:"notification_label" json:"notification_label"`
	NotificationLanguage  string          `db:"notification_language" json:"notification_language"`
	Enabled               int32           `db:"enabled" json:"enabled"`
	RunStatus             string          `db:"run_status" json:"run_status"`
	PauseReason           string          `db:"pause_reason" json:"pause_reason"`
	AggregationAnchorAt   *time.Time      `db:"aggregation_anchor_at" json:"aggregation_anchor_at"`
	State                 json.RawMessage `db:"state" json:"state"`
	LastEvaluatedAt       *time.Time      `db:"last_evaluated_at" json:"last_evaluated_at"`
	LastTriggeredAt       *time.Time      `db:"last_triggered_at" json:"last_triggered_at"`
	CreatedAt             time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt             time.Time       `db:"updated_at" json:"updated_at"`
}

type MarketEvent struct {
	ID                      int64           `db:"id" json:"id"`
	MarketPoolID            *int64          `db:"market_pool_id" json:"market_pool_id"`
	MarketAssetDeploymentID *int64          `db:"market_asset_deployment_id" json:"market_asset_deployment_id,omitempty"`
	ChainKey                string          `db:"chain_key" json:"chain_key"`
	ChainID                 int64           `db:"chain_id" json:"chain_id"`
	TokenAddress            string          `db:"token_address" json:"token_address"`
	EventType               string          `db:"event_type" json:"event_type"`
	EventKey                string          `db:"event_key" json:"event_key"`
	TransactionHash         *string         `db:"transaction_hash" json:"transaction_hash"`
	TransactionIndex        *int32          `db:"transaction_index" json:"transaction_index"`
	LogIndex                *int32          `db:"log_index" json:"log_index"`
	BlockNumber             *int64          `db:"block_number" json:"block_number"`
	BlockHash               *string         `db:"block_hash" json:"block_hash"`
	WalletAddress           *string         `db:"wallet_address" json:"wallet_address"`
	TokenAmountRaw          *string         `db:"token_amount_raw" json:"token_amount_raw"`
	QuoteAmountRaw          *string         `db:"quote_amount_raw" json:"quote_amount_raw"`
	TokenAmount             *string         `db:"token_amount" json:"token_amount"`
	QuoteAmount             *string         `db:"quote_amount" json:"quote_amount"`
	USDValue                *string         `db:"usd_value" json:"usd_value"`
	PriceUSD                *string         `db:"price_usd" json:"price_usd"`
	Source                  string          `db:"source" json:"source"`
	Confidence              string          `db:"confidence" json:"confidence"`
	Confirmed               int32           `db:"confirmed" json:"confirmed"`
	Reorged                 int32           `db:"reorged" json:"reorged"`
	OccurredAt              time.Time       `db:"occurred_at" json:"occurred_at"`
	ObservedAt              time.Time       `db:"observed_at" json:"observed_at"`
	RawPayload              json.RawMessage `db:"raw_payload" json:"raw_payload"`
	Metadata                json.RawMessage `db:"metadata" json:"metadata"`
}

type MarketEventFilter struct {
	BeforeID      int64
	Limit         int
	ChainKey      string
	EventType     string
	RuleType      string
	MarketPoolID  int64
	WalletAddress string
}

type MarketRuleEventHistory struct {
	ID                     int64      `db:"id" json:"id"`
	MarketRuleID           int64      `db:"market_rule_id" json:"market_rule_id"`
	MarketEventID          int64      `db:"market_event_id" json:"market_event_id"`
	RuleType               string     `db:"rule_type" json:"rule_type"`
	ThresholdValue         string     `db:"threshold_value" json:"threshold_value"`
	ThresholdUnit          string     `db:"threshold_unit" json:"threshold_unit"`
	PreviousValue          *string    `db:"previous_value" json:"previous_value"`
	CurrentValue           *string    `db:"current_value" json:"current_value"`
	Note                   string     `db:"note" json:"note"`
	NotificationStatus     string     `db:"notification_status" json:"notification_status"`
	NotificationError      string     `db:"notification_error" json:"notification_error"`
	NotificationSentAt     *time.Time `db:"notification_sent_at" json:"notification_sent_at"`
	CreatedAt              time.Time  `db:"created_at" json:"created_at"`
	MarketPoolID           *int64     `db:"market_pool_id" json:"market_pool_id"`
	ChainKey               string     `db:"chain_key" json:"chain_key"`
	EventType              string     `db:"event_type" json:"event_type"`
	TransactionHash        *string    `db:"transaction_hash" json:"transaction_hash"`
	WalletAddress          *string    `db:"wallet_address" json:"wallet_address"`
	TokenAmount            *string    `db:"token_amount" json:"token_amount"`
	USDValue               *string    `db:"usd_value" json:"usd_value"`
	Source                 string     `db:"source" json:"source"`
	OccurredAt             time.Time  `db:"occurred_at" json:"occurred_at"`
	NotificationSuccessful bool       `db:"notification_successful" json:"notification_successful"`
}

type MarketRuleEvent struct {
	ID                      int64           `db:"id" json:"id"`
	MarketRuleID            int64           `db:"market_rule_id" json:"market_rule_id"`
	MarketEventID           int64           `db:"market_event_id" json:"market_event_id"`
	TriggerKey              string          `db:"trigger_key" json:"trigger_key"`
	PreviousValue           *string         `db:"previous_value" json:"previous_value"`
	CurrentValue            *string         `db:"current_value" json:"current_value"`
	Note                    string          `db:"note" json:"note"`
	Details                 json.RawMessage `db:"details" json:"details"`
	NotificationMessageID   *string         `db:"notification_message_id" json:"notification_message_id"`
	NotificationStatus      string          `db:"notification_status" json:"notification_status"`
	NotificationError       string          `db:"notification_error" json:"notification_error"`
	NotificationAttempts    int32           `db:"notification_attempts" json:"notification_attempts"`
	NotificationAttemptedAt *time.Time      `db:"notification_attempted_at" json:"notification_attempted_at"`
	NotificationSentAt      *time.Time      `db:"notification_sent_at" json:"notification_sent_at"`
	NextAttemptAt           time.Time       `db:"next_attempt_at" json:"next_attempt_at"`
	CreatedAt               time.Time       `db:"created_at" json:"created_at"`
}

type MarketStageWindow struct {
	ID                      int64      `db:"id" json:"id"`
	DeBoxUserID             string     `db:"debox_user_id" json:"debox_user_id"`
	MarketRuleID            int64      `db:"market_rule_id" json:"market_rule_id"`
	StartsAt                time.Time  `db:"starts_at" json:"starts_at"`
	EndsAt                  time.Time  `db:"ends_at" json:"ends_at"`
	TriggerCount            int64      `db:"trigger_count" json:"trigger_count"`
	NotificationStatus      string     `db:"notification_status" json:"notification_status"`
	NotificationMessageID   *string    `db:"notification_message_id" json:"notification_message_id"`
	NotificationError       string     `db:"notification_error" json:"notification_error"`
	NotificationAttempts    int32      `db:"notification_attempts" json:"notification_attempts"`
	NotificationAttemptedAt *time.Time `db:"notification_attempted_at" json:"notification_attempted_at"`
	NotificationSentAt      *time.Time `db:"notification_sent_at" json:"notification_sent_at"`
	NextAttemptAt           time.Time  `db:"next_attempt_at" json:"next_attempt_at"`
	ClosedAt                *time.Time `db:"closed_at" json:"closed_at"`
	CreatedAt               time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt               time.Time  `db:"updated_at" json:"updated_at"`
}

type MarketCombinationRule struct {
	ID                   int64                     `db:"id" json:"id"`
	DeBoxUserID          string                    `db:"debox_user_id" json:"debox_user_id"`
	Note                 string                    `db:"note" json:"note"`
	CycleType            string                    `db:"cycle_type" json:"cycle_type"`
	CycleMinutes         int32                     `db:"cycle_minutes" json:"cycle_minutes"`
	NotificationChatID   string                    `db:"notification_chat_id" json:"notification_chat_id"`
	NotificationChatType string                    `db:"notification_chat_type" json:"notification_chat_type"`
	NotificationLabel    string                    `db:"notification_label" json:"notification_label"`
	NotificationLanguage string                    `db:"notification_language" json:"notification_language"`
	Enabled              int32                     `db:"enabled" json:"enabled"`
	RunStatus            string                    `db:"run_status" json:"run_status"`
	PauseReason          string                    `db:"pause_reason" json:"pause_reason"`
	AggregationAnchorAt  *time.Time                `db:"aggregation_anchor_at" json:"aggregation_anchor_at"`
	CreatedAt            time.Time                 `db:"created_at" json:"created_at"`
	UpdatedAt            time.Time                 `db:"updated_at" json:"updated_at"`
	Members              []MarketCombinationMember `db:"-" json:"members"`
}

type MarketCombinationMember struct {
	ID                      int64     `db:"id" json:"id"`
	MarketCombinationRuleID int64     `db:"market_combination_rule_id" json:"market_combination_rule_id"`
	SourceType              string    `db:"source_type" json:"source_type"`
	WatchRuleID             *int64    `db:"watch_rule_id" json:"watch_rule_id"`
	MarketRuleID            *int64    `db:"market_rule_id" json:"market_rule_id"`
	RequiredTriggerCount    int64     `db:"required_trigger_count" json:"required_trigger_count"`
	CreatedAt               time.Time `db:"created_at" json:"created_at"`
}

type MarketCombinationWindow struct {
	ID                      int64      `db:"id" json:"id"`
	DeBoxUserID             string     `db:"debox_user_id" json:"debox_user_id"`
	MarketCombinationRuleID int64      `db:"market_combination_rule_id" json:"market_combination_rule_id"`
	StartsAt                time.Time  `db:"starts_at" json:"starts_at"`
	EndsAt                  time.Time  `db:"ends_at" json:"ends_at"`
	TotalTriggerCount       int64      `db:"total_trigger_count" json:"total_trigger_count"`
	NotificationStatus      string     `db:"notification_status" json:"notification_status"`
	NotificationMessageID   *string    `db:"notification_message_id" json:"notification_message_id"`
	NotificationError       string     `db:"notification_error" json:"notification_error"`
	NotificationAttempts    int32      `db:"notification_attempts" json:"notification_attempts"`
	NotificationAttemptedAt *time.Time `db:"notification_attempted_at" json:"notification_attempted_at"`
	NotificationSentAt      *time.Time `db:"notification_sent_at" json:"notification_sent_at"`
	NextAttemptAt           time.Time  `db:"next_attempt_at" json:"next_attempt_at"`
	ClosedAt                *time.Time `db:"closed_at" json:"closed_at"`
	CreatedAt               time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt               time.Time  `db:"updated_at" json:"updated_at"`
}

type MarketNotificationDelivery struct {
	Kind                    string                      `db:"kind" json:"kind"`
	ID                      int64                       `db:"id" json:"id"`
	DeBoxUserID             string                      `db:"debox_user_id" json:"debox_user_id"`
	MarketRuleID            *int64                      `db:"market_rule_id" json:"market_rule_id"`
	MarketCombinationRuleID *int64                      `db:"market_combination_rule_id" json:"market_combination_rule_id"`
	MarketRuleEventID       *int64                      `db:"market_rule_event_id" json:"market_rule_event_id"`
	NotificationChatID      string                      `db:"notification_chat_id" json:"notification_chat_id"`
	NotificationChatType    string                      `db:"notification_chat_type" json:"notification_chat_type"`
	NotificationLanguage    string                      `db:"notification_language" json:"notification_language"`
	NotificationLabel       string                      `db:"notification_label" json:"notification_label"`
	Timezone                string                      `db:"timezone" json:"timezone"`
	Project                 MarketProject               `db:"-" json:"project"`
	Rule                    *MarketRule                 `db:"-" json:"rule,omitempty"`
	Event                   *MarketEvent                `db:"-" json:"event,omitempty"`
	Pool                    *MarketPool                 `db:"-" json:"pool,omitempty"`
	Snapshot                *MarketSnapshot             `db:"-" json:"snapshot,omitempty"`
	TriggerCount            int64                       `db:"trigger_count" json:"trigger_count"`
	StartsAt                time.Time                   `db:"starts_at" json:"starts_at"`
	EndsAt                  time.Time                   `db:"ends_at" json:"ends_at"`
	Note                    string                      `db:"note" json:"note"`
	RecentNotes             []string                    `db:"-" json:"recent_notes"`
	RecentEvents            []MarketNotificationEvent   `db:"-" json:"recent_events"`
	CombinationMembers      []MarketCombinationProgress `db:"-" json:"combination_members"`
}

type MarketNotificationEvent struct {
	Project MarketProject `db:"-" json:"project"`
	Event   MarketEvent   `db:"-" json:"event"`
	Pool    *MarketPool   `db:"-" json:"pool,omitempty"`
	Note    string        `db:"note" json:"note"`
}

type MarketCombinationProgress struct {
	MemberID             int64                     `db:"member_id" json:"member_id"`
	SourceType           string                    `db:"source_type" json:"source_type"`
	RuleType             string                    `db:"rule_type" json:"rule_type"`
	RequiredTriggerCount int64                     `db:"required_trigger_count" json:"required_trigger_count"`
	TriggerCount         int64                     `db:"trigger_count" json:"trigger_count"`
	RecentNotes          []string                  `db:"-" json:"recent_notes"`
	RecentEvents         []MarketNotificationEvent `db:"-" json:"recent_events"`
}

type MarketHolder struct {
	ID                      int64     `db:"id" json:"id"`
	MarketAssetDeploymentID *int64    `db:"market_asset_deployment_id" json:"market_asset_deployment_id,omitempty"`
	ChainKey                string    `db:"chain_key" json:"chain_key"`
	ChainID                 int64     `db:"chain_id" json:"chain_id"`
	TokenAddress            string    `db:"token_address" json:"token_address"`
	HolderAddress           string    `db:"holder_address" json:"holder_address"`
	BalanceRaw              string    `db:"balance_raw" json:"balance_raw"`
	Balance                 string    `db:"balance" json:"balance"`
	SupplyPercent           *string   `db:"supply_percent" json:"supply_percent"`
	Rank                    *int32    `db:"rank" json:"rank"`
	AddressKind             string    `db:"address_kind" json:"address_kind"`
	Excluded                int32     `db:"excluded" json:"excluded"`
	ExclusionReason         string    `db:"exclusion_reason" json:"exclusion_reason"`
	Source                  string    `db:"source" json:"source"`
	FirstSeenAt             time.Time `db:"first_seen_at" json:"first_seen_at"`
	LastSeenAt              time.Time `db:"last_seen_at" json:"last_seen_at"`
	UpdatedAt               time.Time `db:"updated_at" json:"updated_at"`
}

type MarketHolderView struct {
	MarketHolder
	PreviousBalance *string `db:"previous_balance" json:"previous_balance,omitempty"`
	PreviousRank    *int32  `db:"previous_rank" json:"previous_rank,omitempty"`
	ChangeType      string  `db:"change_type" json:"change_type"`
}

type MarketHolderSnapshot struct {
	ID                      int64     `db:"id" json:"id"`
	MarketAssetDeploymentID *int64    `db:"market_asset_deployment_id" json:"market_asset_deployment_id,omitempty"`
	ChainKey                string    `db:"chain_key" json:"chain_key"`
	ChainID                 int64     `db:"chain_id" json:"chain_id"`
	TokenAddress            string    `db:"token_address" json:"token_address"`
	HolderAddress           string    `db:"holder_address" json:"holder_address"`
	BalanceRaw              string    `db:"balance_raw" json:"balance_raw"`
	Balance                 string    `db:"balance" json:"balance"`
	SupplyPercent           *string   `db:"supply_percent" json:"supply_percent"`
	Rank                    *int32    `db:"rank" json:"rank"`
	Source                  string    `db:"source" json:"source"`
	CapturedAt              time.Time `db:"captured_at" json:"captured_at"`
}

type MarketAddressLabel struct {
	ID                        int64     `db:"id" json:"id"`
	DeBoxUserID               string    `db:"debox_user_id" json:"debox_user_id"`
	MarketProjectID           int64     `db:"market_project_id" json:"market_project_id"`
	MarketProjectDeploymentID *int64    `db:"market_project_deployment_id" json:"market_project_deployment_id,omitempty"`
	ChainKey                  string    `db:"chain_key" json:"chain_key"`
	ChainID                   int64     `db:"chain_id" json:"chain_id"`
	Address                   string    `db:"address" json:"address"`
	LabelType                 string    `db:"label_type" json:"label_type"`
	Label                     string    `db:"label" json:"label"`
	Excluded                  int32     `db:"excluded" json:"excluded"`
	CreatedAt                 time.Time `db:"created_at" json:"created_at"`
	UpdatedAt                 time.Time `db:"updated_at" json:"updated_at"`
}

type MarketChainCursor struct {
	ID                      int64      `db:"id" json:"id"`
	MarketAssetDeploymentID *int64     `db:"market_asset_deployment_id" json:"market_asset_deployment_id,omitempty"`
	ChainKey                string     `db:"chain_key" json:"chain_key"`
	ChainID                 int64      `db:"chain_id" json:"chain_id"`
	CursorKey               string     `db:"cursor_key" json:"cursor_key"`
	NextBlockNumber         int64      `db:"next_block_number" json:"next_block_number"`
	SafeBlockNumber         int64      `db:"safe_block_number" json:"safe_block_number"`
	LastBlockHash           *string    `db:"last_block_hash" json:"last_block_hash"`
	Status                  string     `db:"status" json:"status"`
	LastError               string     `db:"last_error" json:"last_error"`
	LastScannedAt           *time.Time `db:"last_scanned_at" json:"last_scanned_at"`
	CreatedAt               time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt               time.Time  `db:"updated_at" json:"updated_at"`
}

type NoditWebhookSubscription struct {
	ID              int64           `db:"id" json:"id"`
	Provider        string          `db:"provider" json:"provider"`
	ExternalID      *string         `db:"external_id" json:"external_id"`
	ChainKey        string          `db:"chain_key" json:"chain_key"`
	ChainID         int64           `db:"chain_id" json:"chain_id"`
	EventCategory   string          `db:"event_category" json:"event_category"`
	CallbackURLHash string          `db:"callback_url_hash" json:"callback_url_hash"`
	SecretReference string          `db:"secret_reference" json:"secret_reference"`
	Status          string          `db:"status" json:"status"`
	Configuration   json.RawMessage `db:"configuration" json:"configuration"`
	LastSyncedAt    *time.Time      `db:"last_synced_at" json:"last_synced_at"`
	LastCheckedAt   *time.Time      `db:"last_checked_at" json:"last_checked_at"`
	LastError       string          `db:"last_error" json:"last_error"`
	CreatedAt       time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time       `db:"updated_at" json:"updated_at"`
}

type WebhookInboxMessage struct {
	ID                    int64           `db:"id" json:"id"`
	WebhookSubscriptionID *int64          `db:"webhook_subscription_id" json:"webhook_subscription_id"`
	Provider              string          `db:"provider" json:"provider"`
	ChainKey              string          `db:"chain_key" json:"chain_key"`
	ChainID               int64           `db:"chain_id" json:"chain_id"`
	DeliveryID            string          `db:"delivery_id" json:"delivery_id"`
	DedupeKey             string          `db:"dedupe_key" json:"dedupe_key"`
	SignatureValid        int32           `db:"signature_valid" json:"signature_valid"`
	Headers               json.RawMessage `db:"headers" json:"headers"`
	RawBody               []byte          `db:"raw_body" json:"raw_body"`
	Payload               json.RawMessage `db:"payload" json:"payload"`
	ProcessingStatus      string          `db:"processing_status" json:"processing_status"`
	Attempts              int32           `db:"attempts" json:"attempts"`
	NextAttemptAt         time.Time       `db:"next_attempt_at" json:"next_attempt_at"`
	LockedAt              *time.Time      `db:"locked_at" json:"locked_at"`
	ProcessedAt           *time.Time      `db:"processed_at" json:"processed_at"`
	LastError             string          `db:"last_error" json:"last_error"`
	ReceivedAt            time.Time       `db:"received_at" json:"received_at"`
	CreatedAt             time.Time       `db:"created_at" json:"created_at"`
}

// MarketCollectionTarget is the global, de-duplicated collection view. It joins
// an active project to one selected pool without exposing collection details to
// the user-facing project APIs.
type MarketCollectionTarget struct {
	MarketProjectID int64   `db:"market_project_id" json:"market_project_id"`
	DeBoxUserID     string  `db:"debox_user_id" json:"debox_user_id"`
	ChainKey        string  `db:"chain_key" json:"chain_key"`
	ChainID         int64   `db:"chain_id" json:"chain_id"`
	TokenAddress    string  `db:"token_address" json:"token_address"`
	TokenName       string  `db:"token_name" json:"token_name"`
	TokenSymbol     string  `db:"token_symbol" json:"token_symbol"`
	TokenDecimals   int32   `db:"token_decimals" json:"token_decimals"`
	MarketPoolID    int64   `db:"market_pool_id" json:"market_pool_id"`
	Protocol        string  `db:"protocol" json:"protocol"`
	ProtocolVersion string  `db:"protocol_version" json:"protocol_version"`
	PoolKey         string  `db:"pool_key" json:"pool_key"`
	PoolAddress     *string `db:"pool_address" json:"pool_address"`
	Token0Address   string  `db:"token0_address" json:"token0_address"`
	Token0Symbol    string  `db:"token0_symbol" json:"token0_symbol"`
	Token0Decimals  int32   `db:"token0_decimals" json:"token0_decimals"`
	Token1Address   string  `db:"token1_address" json:"token1_address"`
	Token1Symbol    string  `db:"token1_symbol" json:"token1_symbol"`
	Token1Decimals  int32   `db:"token1_decimals" json:"token1_decimals"`
	ParserAdapter   string  `db:"parser_adapter" json:"parser_adapter"`
	Selected        int32   `db:"selected" json:"selected"`
	IsPrimary       int32   `db:"is_primary" json:"is_primary"`
}

type MarketScannedBlock struct {
	ID                      int64      `db:"id" json:"id"`
	MarketAssetDeploymentID *int64     `db:"market_asset_deployment_id" json:"market_asset_deployment_id,omitempty"`
	ChainKey                string     `db:"chain_key" json:"chain_key"`
	ChainID                 int64      `db:"chain_id" json:"chain_id"`
	CursorKey               string     `db:"cursor_key" json:"cursor_key"`
	BlockNumber             int64      `db:"block_number" json:"block_number"`
	BlockHash               string     `db:"block_hash" json:"block_hash"`
	ParentHash              string     `db:"parent_hash" json:"parent_hash"`
	BlockTimestamp          *time.Time `db:"block_timestamp" json:"block_timestamp"`
	Canonical               int32      `db:"canonical" json:"canonical"`
	ScannedAt               time.Time  `db:"scanned_at" json:"scanned_at"`
	CreatedAt               time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt               time.Time  `db:"updated_at" json:"updated_at"`
}

type MarketProviderHealth struct {
	ID                      int64           `db:"id" json:"id"`
	MarketAssetDeploymentID *int64          `db:"market_asset_deployment_id" json:"market_asset_deployment_id,omitempty"`
	Provider                string          `db:"provider" json:"provider"`
	Component               string          `db:"component" json:"component"`
	ChainKey                string          `db:"chain_key" json:"chain_key"`
	ChainID                 int64           `db:"chain_id" json:"chain_id"`
	Status                  string          `db:"status" json:"status"`
	ConsecutiveFailures     int32           `db:"consecutive_failures" json:"consecutive_failures"`
	LatencyMS               *int64          `db:"latency_ms" json:"latency_ms"`
	LastSuccessAt           *time.Time      `db:"last_success_at" json:"last_success_at"`
	LastFailureAt           *time.Time      `db:"last_failure_at" json:"last_failure_at"`
	LastCheckedAt           time.Time       `db:"last_checked_at" json:"last_checked_at"`
	LastError               string          `db:"last_error" json:"last_error"`
	Metadata                json.RawMessage `db:"metadata" json:"metadata"`
	CreatedAt               time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt               time.Time       `db:"updated_at" json:"updated_at"`
}

type MarketProviderUsage struct {
	ID           int64           `db:"id" json:"id"`
	Provider     string          `db:"provider" json:"provider"`
	Metric       string          `db:"metric" json:"metric"`
	PeriodStart  time.Time       `db:"period_start" json:"period_start"`
	PeriodEnd    time.Time       `db:"period_end" json:"period_end"`
	UsedUnits    string          `db:"used_units" json:"used_units"`
	LimitUnits   *string         `db:"limit_units" json:"limit_units"`
	UsagePercent *string         `db:"usage_percent" json:"usage_percent"`
	AlertLevel   int32           `db:"alert_level" json:"alert_level"`
	LastAlertAt  *time.Time      `db:"last_alert_at" json:"last_alert_at"`
	Metadata     json.RawMessage `db:"metadata" json:"metadata"`
	CheckedAt    time.Time       `db:"checked_at" json:"checked_at"`
	CreatedAt    time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time       `db:"updated_at" json:"updated_at"`
}

type MarketReorgResult struct {
	ReorgedEvents int64             `json:"reorged_events"`
	ReorgedBlocks int64             `json:"reorged_blocks"`
	Cursor        MarketChainCursor `json:"cursor"`
}
