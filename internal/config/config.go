package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultAppName     = "DeBox Asset Alert"
	defaultEnvironment = "development"
	defaultHost        = "0.0.0.0"
	defaultPort        = 8000
	defaultReceiveMode = "polling"
)

type Config struct {
	AppName     string
	Environment string
	Host        string
	Port        int
	ReceiveMode string
	StaticDir   string
}

func Load() (Config, error) {
	portValue := firstNonEmpty(os.Getenv("APP_PORT"), os.Getenv("PORT"), strconv.Itoa(defaultPort))
	port, err := strconv.Atoi(portValue)
	if err != nil {
		return Config{}, fmt.Errorf("APP_PORT/PORT must be an integer: %q", portValue)
	}

	cfg := Config{
		AppName:     firstNonEmpty(os.Getenv("APP_NAME"), defaultAppName),
		Environment: firstNonEmpty(os.Getenv("APP_ENV"), defaultEnvironment),
		Host:        firstNonEmpty(os.Getenv("APP_HOST"), defaultHost),
		Port:        port,
		ReceiveMode: strings.ToLower(firstNonEmpty(os.Getenv("DEBOX_BOT_RECEIVE_MODE"), defaultReceiveMode)),
		StaticDir:   firstNonEmpty(os.Getenv("STATIC_DIR"), "static"),
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
