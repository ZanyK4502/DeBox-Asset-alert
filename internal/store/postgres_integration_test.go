package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresMigrationContract(t *testing.T) {
	store, pool := openIntegrationStore(t)
	_ = store

	expected := []string{
		"alert_events",
		"auth_challenges",
		"auth_sessions",
		"complimentary_grants",
		"notification_groups",
		"orders",
		"schema_migrations",
		"subscriptions",
		"user_preferences",
		"watch_rules",
	}
	rows, err := pool.Query(context.Background(), `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = current_schema()
		ORDER BY table_name
	`)
	if err != nil {
		t.Fatalf("list migrated tables: %v", err)
	}
	got, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (string, error) {
		var name string
		err := row.Scan(&name)
		return name, err
	})
	if err != nil {
		t.Fatalf("collect migrated tables: %v", err)
	}
	if fmt.Sprint(got) != fmt.Sprint(expected) {
		t.Fatalf("migrated tables = %v, want %v", got, expected)
	}
}

func TestPostgresConcurrentFreeRuleQuota(t *testing.T) {
	store, _ := openIntegrationStore(t)
	const attempts = 8
	policy := QuotaPolicy{
		PlanCode:         "free",
		WalletLimit:      1,
		RuleLimit:        1,
		AllowedRuleTypes: []string{"balance_change"},
	}
	params := CreateWatchRuleParams{
		DeBoxUserID:          "integration-free-user",
		ChainKey:             "bsc",
		ChainID:              56,
		WalletAddress:        "0x1111111111111111111111111111111111111111",
		RuleType:             "balance_change",
		Threshold:            "0",
		NotificationChatID:   "integration-free-user",
		NotificationChatType: "private",
		NotificationLanguage: "zh",
	}

	var successes atomic.Int32
	errorsSeen := make(chan error, attempts)
	var workers sync.WaitGroup
	for range attempts {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, err := store.CreateWatchRuleWithinQuota(context.Background(), params, policy)
			if err == nil {
				successes.Add(1)
				return
			}
			errorsSeen <- err
		}()
	}
	workers.Wait()
	close(errorsSeen)

	if got := successes.Load(); got != 1 {
		t.Fatalf("successful concurrent creates = %d, want 1", got)
	}
	for err := range errorsSeen {
		if !errors.Is(err, ErrRuleLimitReached) {
			t.Fatalf("concurrent create error = %v, want ErrRuleLimitReached", err)
		}
	}
}

func TestPostgresTransactionHashIsGloballyUnique(t *testing.T) {
	store, _ := openIntegrationStore(t)
	token := "0x55d398326f99059ff775485246999027b3197955"
	first := createIntegrationOrder(t, store, "integration-user-1", token)
	second := createIntegrationOrder(t, store, "integration-user-2", token)
	txHash := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	if _, err := store.ClaimOrderTransaction(
		context.Background(), first.ID, first.DeBoxUserID, first.PayerAddress, txHash,
	); err != nil {
		t.Fatalf("claim first transaction: %v", err)
	}
	if _, err := store.ClaimOrderTransaction(
		context.Background(), second.ID, second.DeBoxUserID, second.PayerAddress, txHash,
	); !errors.Is(err, ErrOrderTransactionUsed) {
		t.Fatalf("claim duplicate transaction error = %v, want ErrOrderTransactionUsed", err)
	}
}

func openIntegrationStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	if os.Getenv("RUN_POSTGRES_INTEGRATION") != "1" {
		t.Skip("set RUN_POSTGRES_INTEGRATION=1 to run PostgreSQL integration tests")
	}
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("TEST_DATABASE_URL is required; production DATABASE_URL is intentionally ignored")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	adminConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	adminPool, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("open integration admin pool: %v", err)
	}
	t.Cleanup(adminPool.Close)
	if err := adminPool.Ping(ctx); err != nil {
		t.Fatalf("ping integration database: %v", err)
	}

	schema := fmt.Sprintf("go_contract_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create integration schema: %v", err)
	}

	schemaConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse schema pool config: %v", err)
	}
	if schemaConfig.ConnConfig.RuntimeParams == nil {
		schemaConfig.ConnConfig.RuntimeParams = map[string]string{}
	}
	schemaConfig.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, schemaConfig)
	if err != nil {
		t.Fatalf("open integration schema pool: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		_, _ = adminPool.Exec(dropCtx, "DROP SCHEMA "+quotedSchema+" CASCADE")
	})

	store := &Store{db: pool, pool: pool}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate integration schema: %v", err)
	}
	return store, pool
}

func createIntegrationOrder(t *testing.T, store *Store, userID, token string) Order {
	t.Helper()
	order, err := store.CreateOrder(context.Background(), CreateOrderParams{
		DeBoxUserID:      userID,
		PayerAddress:     "0x2222222222222222222222222222222222222222",
		PlanCode:         "standard",
		ChainKey:         "bsc",
		ChainID:          56,
		TokenAddress:     &token,
		TokenSymbol:      "USDT",
		TokenDecimals:    18,
		TotalAmount:      "10",
		RecipientAddress: "0x3333333333333333333333333333333333333333",
	})
	if err != nil {
		t.Fatalf("create order for %s: %v", userID, err)
	}
	return order
}
