package config

import (
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultAppName       = "DeBox Asset Alert"
	defaultEnvironment   = "development"
	defaultHost          = "0.0.0.0"
	defaultPort          = 8000
	defaultReceiveMode   = "polling"
	defaultDeBoxAPI      = "https://open.debox.pro"
	defaultChainKey      = "bsc"
	defaultNoditAPI      = "https://web3.nodit.io/v1"
	defaultTokenSymbol   = "USDT"
	defaultTokenDecimals = 18
	defaultPlanPrice     = "10"
	defaultPlanDays      = 30
	defaultPaymentMode   = "preview"
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
	DeBoxOpenAPIBase             string
	DeBoxNotificationChatID      string
	DeBoxNotificationChatType    string
	ChainKey                     string
	NoditAPIKey                  string
	NoditBaseURL                 string
	SubscriptionTokenAddress     string
	SubscriptionTokenSymbol      string
	SubscriptionTokenDecimals    int
	SubscriptionPrice            string
	SubscriptionDays             int
	PaymentRecipientAddress      string
	PaymentMode                  string
	ComplimentaryWalletAddresses string
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
		DeBoxOpenAPIBase:             firstNonEmpty(os.Getenv("DEBOX_OPENAPI_BASE"), defaultDeBoxAPI),
		DeBoxNotificationChatID:      strings.TrimSpace(os.Getenv("DEBOX_NOTIFICATION_CHAT_ID")),
		DeBoxNotificationChatType:    strings.ToLower(firstNonEmpty(os.Getenv("DEBOX_NOTIFICATION_CHAT_TYPE"), "private")),
		ChainKey:                     strings.ToLower(firstNonEmpty(os.Getenv("CHAIN_KEY"), defaultChainKey)),
		NoditAPIKey:                  strings.TrimSpace(os.Getenv("NODIT_API_KEY")),
		NoditBaseURL:                 firstNonEmpty(os.Getenv("NODIT_BASE_URL"), defaultNoditAPI),
		SubscriptionTokenAddress:     strings.TrimSpace(os.Getenv("SUBSCRIPTION_TOKEN_ADDRESS")),
		SubscriptionTokenSymbol:      firstNonEmpty(os.Getenv("SUBSCRIPTION_TOKEN_SYMBOL"), defaultTokenSymbol),
		SubscriptionTokenDecimals:    decimals,
		SubscriptionPrice:            firstNonEmpty(os.Getenv("SUBSCRIPTION_PRICE"), defaultPlanPrice),
		SubscriptionDays:             days,
		PaymentRecipientAddress:      strings.TrimSpace(os.Getenv("PAYMENT_RECIPIENT_ADDRESS")),
		PaymentMode:                  strings.ToLower(firstNonEmpty(os.Getenv("PAYMENT_MODE"), defaultPaymentMode)),
		ComplimentaryWalletAddresses: strings.TrimSpace(os.Getenv("COMPLIMENTARY_WALLET_ADDRESSES")),
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
