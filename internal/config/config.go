package config

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAppName                = "DeBox Asset Alert"
	defaultEnvironment            = "development"
	defaultHost                   = "0.0.0.0"
	defaultPort                   = 8000
	defaultReceiveMode            = "polling"
	defaultDeBoxAPI               = "https://open.debox.pro"
	defaultChainKey               = "bsc"
	defaultNoditAPI               = "https://web3.nodit.io/v1"
	defaultTokenSymbol            = "USDT"
	defaultTokenDecimals          = 18
	defaultPlanPrice              = "5"
	defaultPlanDays               = 30
	defaultPaymentMode            = "preview"
	defaultDexScreenerAPI         = "https://api.dexscreener.com"
	defaultNoditCUPerSecond int64 = 400
)

type Config struct {
	AppName                      string
	Environment                  string
	Host                         string
	Port                         int
	ReceiveMode                  string
	StaticDir                    string
	DatabaseURL                  string
	PublicAppURL                 string
	DeBoxBotAPIKey               string
	DeBoxBotAPISecret            string
	DeBoxBotUserID               string
	DeBoxWebhookKey              string
	DeBoxOpenAPIBase             string
	DeBoxNotificationChatID      string
	DeBoxNotificationChatType    string
	ChainKey                     string
	NoditAPIKey                  string
	NoditBaseURL                 string
	NoditCUPerSecond             int64
	SubscriptionTokenAddress     string
	SubscriptionTokenSymbol      string
	SubscriptionTokenDecimals    int
	SubscriptionPrice            string
	SubscriptionDays             int
	PaymentRecipientAddress      string
	PaymentMode                  string
	ComplimentaryWalletAddresses string
	MarketCollectorEnabled       bool
	MarketRuleEngineEnabled      bool
	MarketRuleInterval           time.Duration
	MarketHolderRefreshInterval  time.Duration
	DexScreenerBaseURL           string
	NoditWebhookSigningKey       string
	NoditWebhookSigningKeys      map[string]string
	MarketWebhookAutoRepair      bool
	MarketConfirmationDepth      int64
	MarketScanBatchSize          int64
	MarketInitialLookback        int64
	MarketReorgLookback          int
	MarketInboxInterval          time.Duration
	MarketScanInterval           time.Duration
	MarketSnapshotInterval       time.Duration
	MarketDiscoveryInterval      time.Duration
	MarketHealthInterval         time.Duration
	MarketCleanupInterval        time.Duration
	NoditMonthlyCULimit          int64
}

func Load() (Config, error) {
	portValue := firstNonEmpty(os.Getenv("APP_PORT"), os.Getenv("PORT"), strconv.Itoa(defaultPort))
	port, err := strconv.Atoi(portValue)
	if err != nil {
		return Config{}, fmt.Errorf("APP_PORT/PORT must be an integer: %q", portValue)
	}
	daysValue := firstNonEmpty(os.Getenv("SUBSCRIPTION_DAYS"), strconv.Itoa(defaultPlanDays))
	days, err := strconv.Atoi(daysValue)
	if err != nil {
		return Config{}, fmt.Errorf("SUBSCRIPTION_DAYS must be an integer: %q", daysValue)
	}
	decimalsValue := firstNonEmpty(
		os.Getenv("SUBSCRIPTION_TOKEN_DECIMALS"),
		strconv.Itoa(defaultTokenDecimals),
	)
	decimals, err := strconv.Atoi(decimalsValue)
	if err != nil {
		return Config{}, fmt.Errorf(
			"SUBSCRIPTION_TOKEN_DECIMALS must be an integer: %q",
			decimalsValue,
		)
	}
	collectorEnabled, err := parseBoolEnv("MARKET_COLLECTOR_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	ruleEngineEnabled, err := parseBoolEnv("MARKET_RULE_ENGINE_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	ruleInterval, err := parseDurationEnv("MARKET_RULE_INTERVAL", 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	holderRefreshInterval, err := parseDurationEnv(
		"MARKET_HOLDER_REFRESH_INTERVAL",
		15*time.Minute,
	)
	if err != nil {
		return Config{}, err
	}
	confirmationDepth, err := parseInt64Env("MARKET_CONFIRMATION_DEPTH", 15)
	if err != nil {
		return Config{}, err
	}
	scanBatchSize, err := parseInt64Env("MARKET_SCAN_BATCH_SIZE", 100)
	if err != nil {
		return Config{}, err
	}
	initialLookback, err := parseInt64Env("MARKET_INITIAL_LOOKBACK", 200)
	if err != nil {
		return Config{}, err
	}
	reorgLookback, err := parseIntEnv("MARKET_REORG_LOOKBACK", 128)
	if err != nil {
		return Config{}, err
	}
	monthlyCULimit, err := parseInt64Env("NODIT_MONTHLY_CU_LIMIT", 100_000_000)
	if err != nil {
		return Config{}, err
	}
	cuPerSecond, err := parseInt64Env("NODIT_CU_PER_SECOND", defaultNoditCUPerSecond)
	if err != nil {
		return Config{}, err
	}
	inboxInterval, err := parseDurationEnv("MARKET_INBOX_INTERVAL", 2*time.Second)
	if err != nil {
		return Config{}, err
	}
	scanInterval, err := parseDurationEnv("MARKET_SCAN_INTERVAL", time.Minute)
	if err != nil {
		return Config{}, err
	}
	snapshotInterval, err := parseDurationEnv("MARKET_SNAPSHOT_INTERVAL", 20*time.Second)
	if err != nil {
		return Config{}, err
	}
	discoveryInterval, err := parseDurationEnv("MARKET_DISCOVERY_INTERVAL", 5*time.Minute)
	if err != nil {
		return Config{}, err
	}
	healthInterval, err := parseDurationEnv("MARKET_HEALTH_INTERVAL", time.Minute)
	if err != nil {
		return Config{}, err
	}
	cleanupInterval, err := parseDurationEnv("MARKET_CLEANUP_INTERVAL", 6*time.Hour)
	if err != nil {
		return Config{}, err
	}
	webhookSigningKeys, err := parseSecretMapEnv("NODIT_WEBHOOK_SIGNING_KEYS_JSON")
	if err != nil {
		return Config{}, err
	}
	webhookAutoRepair, err := parseBoolEnv("MARKET_WEBHOOK_AUTO_REPAIR", false)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		AppName:                      firstNonEmpty(os.Getenv("APP_NAME"), defaultAppName),
		Environment:                  firstNonEmpty(os.Getenv("APP_ENV"), defaultEnvironment),
		Host:                         firstNonEmpty(os.Getenv("APP_HOST"), defaultHost),
		Port:                         port,
		ReceiveMode:                  strings.ToLower(firstNonEmpty(os.Getenv("DEBOX_BOT_RECEIVE_MODE"), defaultReceiveMode)),
		StaticDir:                    firstNonEmpty(os.Getenv("STATIC_DIR"), "static"),
		DatabaseURL:                  strings.TrimSpace(os.Getenv("DATABASE_URL")),
		PublicAppURL:                 strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_APP_URL")), "/"),
		DeBoxBotAPIKey:               strings.TrimSpace(os.Getenv("DEBOX_BOT_API_KEY")),
		DeBoxBotAPISecret:            strings.TrimSpace(os.Getenv("DEBOX_BOT_API_SECRET")),
		DeBoxBotUserID:               strings.TrimSpace(os.Getenv("DEBOX_BOT_USER_ID")),
		DeBoxWebhookKey:              strings.TrimSpace(os.Getenv("DEBOX_WEBHOOK_KEY")),
		DeBoxOpenAPIBase:             firstNonEmpty(os.Getenv("DEBOX_OPENAPI_BASE"), defaultDeBoxAPI),
		DeBoxNotificationChatID:      strings.TrimSpace(os.Getenv("DEBOX_NOTIFICATION_CHAT_ID")),
		DeBoxNotificationChatType:    strings.ToLower(firstNonEmpty(os.Getenv("DEBOX_NOTIFICATION_CHAT_TYPE"), "private")),
		ChainKey:                     strings.ToLower(firstNonEmpty(os.Getenv("CHAIN_KEY"), defaultChainKey)),
		NoditAPIKey:                  strings.TrimSpace(os.Getenv("NODIT_API_KEY")),
		NoditBaseURL:                 firstNonEmpty(os.Getenv("NODIT_BASE_URL"), defaultNoditAPI),
		NoditCUPerSecond:             cuPerSecond,
		SubscriptionTokenAddress:     strings.TrimSpace(os.Getenv("SUBSCRIPTION_TOKEN_ADDRESS")),
		SubscriptionTokenSymbol:      firstNonEmpty(os.Getenv("SUBSCRIPTION_TOKEN_SYMBOL"), defaultTokenSymbol),
		SubscriptionTokenDecimals:    decimals,
		SubscriptionPrice:            firstNonEmpty(os.Getenv("SUBSCRIPTION_PRICE"), defaultPlanPrice),
		SubscriptionDays:             days,
		PaymentRecipientAddress:      strings.TrimSpace(os.Getenv("PAYMENT_RECIPIENT_ADDRESS")),
		PaymentMode:                  strings.ToLower(firstNonEmpty(os.Getenv("PAYMENT_MODE"), defaultPaymentMode)),
		ComplimentaryWalletAddresses: strings.TrimSpace(os.Getenv("COMPLIMENTARY_WALLET_ADDRESSES")),
		MarketCollectorEnabled:       collectorEnabled,
		MarketRuleEngineEnabled:      ruleEngineEnabled,
		MarketRuleInterval:           ruleInterval,
		MarketHolderRefreshInterval:  holderRefreshInterval,
		DexScreenerBaseURL:           firstNonEmpty(os.Getenv("DEXSCREENER_BASE_URL"), defaultDexScreenerAPI),
		NoditWebhookSigningKey:       strings.TrimSpace(os.Getenv("NODIT_WEBHOOK_SIGNING_KEY")),
		NoditWebhookSigningKeys:      webhookSigningKeys,
		MarketWebhookAutoRepair:      webhookAutoRepair,
		MarketConfirmationDepth:      confirmationDepth,
		MarketScanBatchSize:          scanBatchSize,
		MarketInitialLookback:        initialLookback,
		MarketReorgLookback:          reorgLookback,
		MarketInboxInterval:          inboxInterval,
		MarketScanInterval:           scanInterval,
		MarketSnapshotInterval:       snapshotInterval,
		MarketDiscoveryInterval:      discoveryInterval,
		MarketHealthInterval:         healthInterval,
		MarketCleanupInterval:        cleanupInterval,
		NoditMonthlyCULimit:          monthlyCULimit,
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("APP_PORT/PORT must be between 1 and 65535: %d", c.Port)
	}
	if c.NoditCUPerSecond <= 0 {
		return fmt.Errorf("NODIT_CU_PER_SECOND must be greater than zero")
	}
	if strings.TrimSpace(c.Host) == "" {
		return fmt.Errorf("APP_HOST must not be empty")
	}
	if strings.TrimSpace(c.StaticDir) == "" {
		return fmt.Errorf("STATIC_DIR must not be empty")
	}
	if c.SubscriptionDays < 1 {
		return fmt.Errorf("SUBSCRIPTION_DAYS must be greater than zero: %d", c.SubscriptionDays)
	}
	price, ok := new(big.Rat).SetString(strings.TrimSpace(c.SubscriptionPrice))
	if !ok || price.Sign() < 0 {
		return fmt.Errorf("SUBSCRIPTION_PRICE must be a non-negative decimal: %q", c.SubscriptionPrice)
	}
	if strings.TrimSpace(c.SubscriptionTokenSymbol) == "" {
		return fmt.Errorf("SUBSCRIPTION_TOKEN_SYMBOL must not be empty")
	}
	if c.SubscriptionTokenDecimals < 0 {
		return fmt.Errorf(
			"SUBSCRIPTION_TOKEN_DECIMALS must not be negative: %d",
			c.SubscriptionTokenDecimals,
		)
	}
	if c.MarketCollectorEnabled {
		if c.ChainKey != "bsc" {
			return fmt.Errorf("market collector currently requires CHAIN_KEY=bsc")
		}
		if c.MarketConfirmationDepth < 1 || c.MarketScanBatchSize < 1 ||
			c.MarketInitialLookback < 1 || c.MarketReorgLookback < 1 {
			return fmt.Errorf("market collector block settings must be greater than zero")
		}
		if c.MarketInboxInterval <= 0 || c.MarketScanInterval <= 0 ||
			c.MarketSnapshotInterval <= 0 || c.MarketDiscoveryInterval <= 0 ||
			c.MarketHealthInterval <= 0 || c.MarketCleanupInterval <= 0 {
			return fmt.Errorf("market collector intervals must be greater than zero")
		}
		if c.NoditMonthlyCULimit < 1 {
			return fmt.Errorf("NODIT_MONTHLY_CU_LIMIT must be greater than zero")
		}
		if c.MarketWebhookAutoRepair && strings.TrimSpace(c.PublicAppURL) == "" {
			return fmt.Errorf("PUBLIC_APP_URL is required when MARKET_WEBHOOK_AUTO_REPAIR is enabled")
		}
	}
	if c.MarketRuleEngineEnabled {
		if !c.MarketCollectorEnabled {
			return fmt.Errorf("MARKET_COLLECTOR_ENABLED must be true when MARKET_RULE_ENGINE_ENABLED is enabled")
		}
		if c.MarketRuleInterval <= 0 || c.MarketHolderRefreshInterval <= 0 {
			return fmt.Errorf("market rule and holder intervals must be greater than zero")
		}
	}
	indexPath := filepath.Join(c.StaticDir, "index.html")
	if info, err := os.Stat(indexPath); err != nil {
		return fmt.Errorf("static index %q is unavailable: %w", indexPath, err)
	} else if info.IsDir() {
		return fmt.Errorf("static index %q must be a file", indexPath)
	}
	return nil
}

func (c Config) Address() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func parseBoolEnv(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	result, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %q", name, value)
	}
	return result, nil
}

func parseIntEnv(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	result, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %q", name, value)
	}
	return result, nil
}

func parseInt64Env(name string, fallback int64) (int64, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	result, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %q", name, value)
	}
	return result, nil
}

func parseDurationEnv(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	result, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration: %q", name, value)
	}
	return result, nil
}

func parseSecretMapEnv(name string) (map[string]string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return nil, nil
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object of string values", name)
	}
	result := make(map[string]string, len(decoded))
	for key, secret := range decoded {
		key = strings.ToLower(strings.TrimSpace(key))
		secret = strings.TrimSpace(secret)
		if key == "" || secret == "" {
			return nil, fmt.Errorf("%s must not contain empty categories or secrets", name)
		}
		for _, character := range key {
			if (character < 'a' || character > 'z') &&
				(character < '0' || character > '9') &&
				character != '_' && character != '-' {
				return nil, fmt.Errorf("%s contains an invalid category", name)
			}
		}
		result[key] = secret
	}
	return result, nil
}
