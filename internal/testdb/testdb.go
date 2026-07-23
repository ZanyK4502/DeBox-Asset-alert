package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const acceptanceFlag = "RUN_ACCEPTANCE"

// Open creates a migrated, per-test schema in an explicitly configured local database.
func Open(t testing.TB) *store.Store {
	t.Helper()
	if os.Getenv(acceptanceFlag) != "1" {
		t.Skip("set RUN_ACCEPTANCE=1 to run isolated acceptance tests")
	}

	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Fatal("TEST_DATABASE_URL is required for isolated acceptance tests")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	host := parsed.Hostname()
	if host != "localhost" && !net.ParseIP(host).IsLoopback() {
		t.Fatalf("TEST_DATABASE_URL must point to localhost, got %q", host)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open acceptance database: %v", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Fatalf("ping acceptance database: %v", err)
	}

	schema := "acceptance_" + randomHex(t, 12)
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close()
		t.Fatalf("create acceptance schema: %v", err)
	}

	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	database, err := store.Open(ctx, parsed.String())
	if err != nil {
		dropSchema(t, admin, identifier)
		admin.Close()
		t.Fatalf("open isolated store: %v", err)
	}
	if err := database.Migrate(ctx); err != nil {
		database.Close()
		dropSchema(t, admin, identifier)
		admin.Close()
		t.Fatalf("migrate isolated store: %v", err)
	}

	t.Cleanup(func() {
		database.Close()
		dropSchema(t, admin, identifier)
		admin.Close()
	})
	return database
}

func randomHex(t testing.TB, byteCount int) string {
	t.Helper()
	value := make([]byte, byteCount)
	if _, err := rand.Read(value); err != nil {
		t.Fatalf("create acceptance schema suffix: %v", err)
	}
	return hex.EncodeToString(value)
}

func dropSchema(t testing.TB, admin *pgxpool.Pool, identifier string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := admin.Exec(ctx, fmt.Sprintf("DROP SCHEMA %s CASCADE", identifier)); err != nil {
		t.Errorf("drop acceptance schema: %v", err)
	}
}
