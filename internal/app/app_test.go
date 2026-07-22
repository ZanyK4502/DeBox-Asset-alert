package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/config"
)

func TestName(t *testing.T) {
	if Name != "DeBox Asset Alert" {
		t.Fatalf("unexpected application name: %q", Name)
	}
}

func TestRunShutsDownWhenContextIsCanceled(t *testing.T) {
	staticDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("test"), 0o600); err != nil {
		t.Fatalf("write test index: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}

	cfg := config.Config{
		AppName:     Name,
		Environment: "test",
		Host:        "127.0.0.1",
		Port:        port,
		ReceiveMode: "polling",
		StaticDir:   staticDir,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() {
		done <- Run(ctx, cfg, logger)
	}()

	readyURL := "http://" + cfg.Address() + "/api/ready"
	deadline := time.Now().Add(2 * time.Second)
	for {
		response, requestErr := http.Get(readyURL)
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("server did not become ready: %v", requestErr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down after cancellation")
	}
}
