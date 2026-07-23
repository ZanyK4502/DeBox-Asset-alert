package tunnel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractPublicURL(t *testing.T) {
	output := "Connect to HTTPS://SAMPLE-123.LHR.LIFE to access your service"
	if got := ExtractPublicURL(output); got != "https://sample-123.lhr.life" {
		t.Fatalf("ExtractPublicURL() = %q", got)
	}
}

func TestPublishURLWritesBotDiscoveryFile(t *testing.T) {
	dataDir := t.TempDir()
	manager := New(Config{DataDir: dataDir})

	if err := manager.publishURL("https://sample.lhr.life"); err != nil {
		t.Fatalf("publishURL() error = %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dataDir, "public_url.txt"))
	if err != nil {
		t.Fatalf("read public URL: %v", err)
	}
	if string(body) != "https://sample.lhr.life" {
		t.Fatalf("public URL = %q", string(body))
	}
}
