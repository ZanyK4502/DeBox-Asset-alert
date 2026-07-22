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

func TestLoadRejectsInvalidPort(t *testing.T) {
	t.Setenv("APP_PORT", "not-a-port")
	t.Setenv("STATIC_DIR", testStaticDir(t))

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want invalid port error")
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
