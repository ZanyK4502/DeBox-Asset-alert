package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/tunnel"
)

func main() {
	config := tunnel.DefaultConfig()
	if value := strings.TrimSpace(os.Getenv("SSH_PATH")); value != "" {
		config.SSHPath = value
	}
	if value := strings.TrimSpace(os.Getenv("TUNNEL_LOCAL_ADDRESS")); value != "" {
		config.LocalAddress = value
	}
	if value := strings.TrimSpace(os.Getenv("TUNNEL_DATA_DIR")); value != "" {
		config.DataDir = value
	}
	logger, closeLog := newLogger(config.DataDir)
	defer closeLog()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := tunnel.New(config).Run(ctx, logger); err != nil {
		logger.Error("tunnel manager stopped with an error", "error", err)
		os.Exit(1)
	}
}

func newLogger(dataDir string) (*slog.Logger, func()) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return slog.New(slog.NewJSONHandler(os.Stdout, nil)), func() {}
	}
	file, err := os.OpenFile(
		filepath.Join(dataDir, "tunnel-manager.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return slog.New(slog.NewJSONHandler(os.Stdout, nil)), func() {}
	}
	output := io.MultiWriter(os.Stdout, file)
	return slog.New(slog.NewJSONHandler(output, nil)), func() {
		_ = file.Close()
	}
}
