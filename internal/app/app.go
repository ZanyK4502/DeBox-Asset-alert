package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/config"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/httpapi"
)

const (
	Name            = "DeBox Asset Alert"
	shutdownTimeout = 10 * time.Second
)

func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	server := &http.Server{
		Addr:              cfg.Address(),
		Handler:           httpapi.New(cfg),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("HTTP server starting", "address", server.Addr, "environment", cfg.Environment)
		errCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
	}

	logger.Info("HTTP server shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("listen during shutdown: %w", err)
	}
	logger.Info("HTTP server stopped")
	return nil
}
