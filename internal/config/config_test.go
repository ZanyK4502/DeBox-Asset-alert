package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	staticDir := testStaticDir(t)
	for _, name := range []string{
		"APP_NAME",
		"APP_ENV",
		"APP_HOST",
		"APP_PORT",
		"PORT",
		"DEBOX_BOT_RECEIVE_MODE",
		"PUBLIC_APP_URL",
		"DEBOX_BOT_API_KEY",
		"DEBOX_BOT_API_SECRET",
		"DEBOX_BOT_USER_ID",
		"DEBOX_WEBHOOK_KEY",
		"DEBOX_OPENAPI_BASE",
		"DEBOX_NOTIFICATION_CHAT_ID",
		"DEBOX_NOTIFICATION_CHAT_TYPE",
		"CHAIN_KEY",
		"NODIT_API_KEY",
		"NODIT_BASE_URL",
		"NODIT_CU_PER_SECOND",
		"SUBSCRIPTION_TOKEN_ADDRESS",
		"SUBSCRIPTION_TOKEN_SYMBOL",
		"SUBSCRIPTION_TOKEN_DECIMALS",
		"SUBSCRIPTION_PRICE",
		"SUBSCRIPTION_DAYS",
		"PAYMENT_RECIPIENT_ADDRESS",
		"PAYMENT_MODE",
		"COMPLIMENTARY_WALLET_ADDRESSES",
		"MARKET_COLLECTOR_ENABLED",
		"MARKET_RULE_ENGINE_ENABLED",
		"MARKET_RULE_INTERVAL",
		"MARKET_HOLDER_REFRESH_INTERVAL",
		"DEXSCREENER_BASE_URL",
		"NODIT_WEBHOOK_SIGNING_KEY",
		"NODIT_WEBHOOK_SIGNING_KEYS_JSON",
		"MARKET_WEBHOOK_AUTO_REPAIR",
		"MARKET_CONFIRMATION_DEPTH",
		"MARKET_SCAN_BATCH_SIZE",
		"MARKET_INITIAL_LOOKBACK",
		"MARKET_REORG_LOOKBACK",
		"MARKET_INBOX_INTERVAL",
		"MARKET_SCAN_INTERVAL",
		"MARKET_SNAPSHOT_INTERVAL",
		"MARKET_DISCOVERY_INTERVAL",
		"MARKET_HEALTH_INTERVAL",
		"MARKET_CLEANUP_INTERVAL",
		"NODIT_MONTHLY_CU_LIMIT",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("STATIC_DIR", staticDir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AppName != defaultAppName {
		t.Fatalf("AppName = %q, want %q", cfg.AppName, defaultAppName)
	}
	if cfg.Environment != defaultEnvironment {
		t.Fatalf("Environment = %q, want %q", cfg.Environment, defaultEnvironment)
	}
	if cfg.Address() != "0.0.0.0:8000" {
		t.Fatalf("Address() = %q, want %q", cfg.Address(), "0.0.0.0:8000")
	}
	if cfg.ReceiveMode != defaultReceiveMode {
		t.Fatalf("ReceiveMode = %q, want %q", cfg.ReceiveMode, defaultReceiveMode)
	}
	if cfg.MarketCollectorEnabled || cfg.MarketRuleEngineEnabled ||
		cfg.MarketRuleInterval.String() != "5s" ||
		cfg.MarketHolderRefreshInterval.String() != "15m0s" {
		t.Fatalf(
			"market worker defaults = collector:%v rules:%v interval:%s holders:%s",
			cfg.MarketCollectorEnabled,
			cfg.MarketRuleEngineEnabled,
			cfg.MarketRuleInterval,
			cfg.MarketHolderRefreshInterval,
		)
	}
	if cfg.DeBoxOpenAPIBase != defaultDeBoxAPI || cfg.ChainKey != defaultChainKey || cfg.NoditBaseURL != defaultNoditAPI {
		t.Fatalf("external API defaults = %q/%q/%q", cfg.DeBoxOpenAPIBase, cfg.ChainKey, cfg.NoditBaseURL)
	}
	if cfg.NoditCUPerSecond != defaultNoditCUPerSecond {
		t.Fatalf("NoditCUPerSecond = %d, want %d", cfg.NoditCUPerSecond, defaultNoditCUPerSecond)
	}
	if cfg.SubscriptionTokenSymbol != defaultTokenSymbol {
		t.Fatalf("SubscriptionTokenSymbol = %q, want %q", cfg.SubscriptionTokenSymbol, defaultTokenSymbol)
	}
	if cfg.SubscriptionTokenDecimals != defaultTokenDecimals || cfg.PaymentMode != defaultPaymentMode {
		t.Fatalf(
			"payment defaults = decimals %d/mode %q",
			cfg.SubscriptionTokenDecimals,
			cfg.PaymentMode,
		)
	}
	if cfg.SubscriptionPrice != defaultPlanPrice || cfg.SubscriptionDays != defaultPlanDays {
		t.Fatalf("subscription price/days = %s/%d", cfg.SubscriptionPrice, cfg.SubscriptionDays)
	}
}

func TestLoadReadsPerWebhookSigningKeys(t *testing.T) {
	t.Setenv(
		"NODIT_WEBHOOK_SIGNING_KEYS_JSON",
		`{"v2-v3":" first ","infinity":"second"}`,
	)
	t.Setenv("STATIC_DIR", testStaticDir(t))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.NoditWebhookSigningKeys["v2-v3"] != "first" ||
		cfg.NoditWebhookSigningKeys["infinity"] != "second" {
		t.Fatalf("unexpected webhook signing keys")
	}
}

func TestLoadReadsExternalAPISettings(t *testing.T) {
	t.Setenv("PUBLIC_APP_URL", " https://app.example/ ")
	t.Setenv("DEBOX_BOT_API_KEY", " api-key ")
	t.Setenv("DEBOX_BOT_API_SECRET", " api-secret ")
	t.Setenv("DEBOX_BOT_USER_ID", " bot-user ")
	t.Setenv("DEBOX_WEBHOOK_KEY", " webhook-key ")
	t.Setenv("DEBOX_OPENAPI_BASE", " https://debox.example ")
	t.Setenv("DEBOX_NOTIFICATION_CHAT_ID", " user-1 ")
	t.Setenv("DEBOX_NOTIFICATION_CHAT_TYPE", "GROUP")
	t.Setenv("CHAIN_KEY", "ETHEREUM")
	t.Setenv("NODIT_API_KEY", " nodit-key ")
	t.Setenv("NODIT_BASE_URL", " https://nodit.example/v1 ")
	t.Setenv("NODIT_CU_PER_SECOND", "321")
	t.Setenv("STATIC_DIR", testStaticDir(t))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DeBoxBotAPIKey != "api-key" || cfg.DeBoxBotAPISecret != "api-secret" || cfg.DeBoxBotUserID != "bot-user" {
		t.Fatalf("unexpected DeBox bot settings")
	}
	if cfg.DeBoxWebhookKey != "webhook-key" {
		t.Fatalf("DeBoxWebhookKey = %q", cfg.DeBoxWebhookKey)
	}
	if cfg.PublicAppURL != "https://app.example" {
		t.Fatalf("PublicAppURL = %q", cfg.PublicAppURL)
	}
	if cfg.DeBoxOpenAPIBase != "https://debox.example" ||
		cfg.DeBoxNotificationChatID != "user-1" ||
		cfg.DeBoxNotificationChatType != "group" {
		t.Fatalf("unexpected DeBox API settings")
	}
	if cfg.ChainKey != "ethereum" || cfg.NoditAPIKey != "nodit-key" || cfg.NoditBaseURL != "https://nodit.example/v1" {
		t.Fatalf("unexpected Nodit settings")
	}
	if cfg.NoditCUPerSecond != 321 {
		t.Fatalf("NoditCUPerSecond = %d", cfg.NoditCUPerSecond)
	}
}

func TestLoadPrefersAPPPort(t *testing.T) {
	t.Setenv("APP_PORT", "9001")
	t.Setenv("PORT", "9002")
	t.Setenv("STATIC_DIR", testStaticDir(t))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Port != 9001 {
		t.Fatalf("Port = %d, want 9001", cfg.Port)
	}
}

func TestLoadReadsDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "  postgres://example.invalid/app  ")
	t.Setenv("STATIC_DIR", testStaticDir(t))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DatabaseURL != "postgres://example.invalid/app" {
		t.Fatalf("DatabaseURL = %q", cfg.DatabaseURL)
	}
}

func TestLoadRejectsInvalidPort(t *testing.T) {
	t.Setenv("APP_PORT", "not-a-port")
	t.Setenv("STATIC_DIR", testStaticDir(t))

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want invalid port error")
	}
}

func TestLoadReadsSubscriptionSettings(t *testing.T) {
	t.Setenv("SUBSCRIPTION_TOKEN_SYMBOL", "TEST")
	t.Setenv("SUBSCRIPTION_TOKEN_ADDRESS", " 0x1111111111111111111111111111111111111111 ")
	t.Setenv("SUBSCRIPTION_TOKEN_DECIMALS", "6")
	t.Setenv("SUBSCRIPTION_PRICE", "12.5")
	t.Setenv("SUBSCRIPTION_DAYS", "45")
	t.Setenv("PAYMENT_RECIPIENT_ADDRESS", " 0x2222222222222222222222222222222222222222 ")
	t.Setenv("PAYMENT_MODE", "LIVE")
	t.Setenv("COMPLIMENTARY_WALLET_ADDRESSES", " 0xabc,0xdef ")
	t.Setenv("STATIC_DIR", testStaticDir(t))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SubscriptionTokenSymbol != "TEST" || cfg.SubscriptionPrice != "12.5" || cfg.SubscriptionDays != 45 {
		t.Fatalf("subscription settings = %s/%s/%d", cfg.SubscriptionTokenSymbol, cfg.SubscriptionPrice, cfg.SubscriptionDays)
	}
	if cfg.SubscriptionTokenAddress != "0x1111111111111111111111111111111111111111" ||
		cfg.SubscriptionTokenDecimals != 6 ||
		cfg.PaymentRecipientAddress != "0x2222222222222222222222222222222222222222" ||
		cfg.PaymentMode != "live" {
		t.Fatalf("unexpected payment settings: %#v", cfg)
	}
	if cfg.ComplimentaryWalletAddresses != "0xabc,0xdef" {
		t.Fatalf("ComplimentaryWalletAddresses = %q", cfg.ComplimentaryWalletAddresses)
	}
}

func TestLoadRejectsInvalidSubscriptionSettings(t *testing.T) {
	staticDir := testStaticDir(t)
	tests := []struct {
		name  string
		price string
		days  string
	}{
		{name: "invalid price", price: "abc", days: "30"},
		{name: "negative price", price: "-1", days: "30"},
		{name: "zero days", price: "10", days: "0"},
		{name: "invalid days", price: "10", days: "later"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("SUBSCRIPTION_PRICE", test.price)
			t.Setenv("SUBSCRIPTION_DAYS", test.days)
			t.Setenv("STATIC_DIR", staticDir)
			if _, err := Load(); err == nil {
				t.Fatal("Load() error = nil")
			}
		})
	}
}

func TestValidateRequiresStaticIndex(t *testing.T) {
	cfg := Config{
		Host:      "127.0.0.1",
		Port:      8000,
		StaticDir: t.TempDir(),
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want missing index error")
	}
}

func testStaticDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("test"), 0o600); err != nil {
		t.Fatalf("write test index: %v", err)
	}
	return dir
}
