package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrActiveSubscriptionConflict = errors.New("an active subscription prevents this plan change")
	ErrCombinationMemberManaged   = errors.New("combination members are managed by their combination rule")
	ErrCombinationRulesDenied     = errors.New("combination rules are not allowed")
	ErrGroupLimitReached          = errors.New("notification group limit reached")
	ErrGroupNotificationDenied    = errors.New("group notification is not allowed")
	ErrInvalidFreeWatchRule       = errors.New("watch rule is not eligible for the free plan")
	ErrInvalidCombinationRule     = errors.New("invalid combination rule")
	ErrInvalidMarketAddress       = errors.New("invalid market address")
	ErrInvalidMarketAddressLabel  = errors.New("invalid market address label")
	ErrInvalidMarketCursor        = errors.New("invalid market chain cursor")
	ErrInvalidMarketEvent         = errors.New("invalid market event")
	ErrInvalidMarketHolder        = errors.New("invalid market holder")
	ErrInvalidMarketPool          = errors.New("invalid market pool")
	ErrInvalidMarketProject       = errors.New("invalid market project")
	ErrInvalidMarketRule          = errors.New("invalid market rule")
	ErrInvalidMarketRuleEvent     = errors.New("invalid market rule event")
	ErrInvalidMarketStatus        = errors.New("invalid market status")
	ErrInvalidNotificationStatus  = errors.New("invalid notification status")
	ErrInvalidWebhookDelivery     = errors.New("invalid webhook delivery")
	ErrInvalidWebhookSubscription = errors.New("invalid webhook subscription")
	ErrMarketMonitoringDenied     = errors.New("market monitoring is not allowed")
	ErrMarketPoolMismatch         = errors.New("market pool does not belong to the token")
	ErrMarketProjectExists        = errors.New("market project already exists")
	ErrMarketProjectLimitReached  = errors.New("market project limit reached")
	ErrMarketProjectNotArchived   = errors.New("only archived market projects can be deleted")
	ErrMarketCombinationNotPaused = errors.New("only paused market combinations can be deleted")
	ErrMarketRuleTypeDenied       = errors.New("market rule type is not allowed")
	ErrNotFound                   = errors.New("record not found")
	ErrOrderConflict              = errors.New("payment order conflict")
	ErrOrderInvalid               = errors.New("payment order is missing, expired, or not owned by the user")
	ErrOrderTransactionUsed       = errors.New("transaction is already assigned to another order")
	ErrPermanentEntitlement       = errors.New("permanent plan does not require payment")
	ErrPoolRequired               = errors.New("a PostgreSQL pool is required for this operation")
	ErrRuleLimitReached           = errors.New("watch rule limit reached")
	ErrRuleTypeDenied             = errors.New("watch rule type is not allowed")
	ErrStageDeliveryRequired      = errors.New("watch rule is not configured for stage delivery")
	ErrStageNotificationsDenied   = errors.New("stage notifications are not allowed")
	ErrSubscriptionChanged        = errors.New("subscription changed during the operation")
	ErrWalletLimitReached         = errors.New("monitored wallet limit reached")
)

type DBTX interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Store struct {
	db   DBTX
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return &Store{db: pool, pool: pool}, nil
}

func newWithDB(db DBTX) *Store {
	return &Store{db: db}
}

func (s *Store) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

func (s *Store) Ping(ctx context.Context) error {
	if s.pool == nil {
		return ErrPoolRequired
	}
	return s.pool.Ping(ctx)
}

func withTxValue[T any](ctx context.Context, db DBTX, fn func(DBTX) (T, error)) (value T, err error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return value, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()

	value, err = fn(tx)
	if err != nil {
		return value, err
	}
	if err = tx.Commit(ctx); err != nil {
		return value, fmt.Errorf("commit transaction: %w", err)
	}
	return value, nil
}

func collectOne[T any](ctx context.Context, db DBTX, sql string, args ...any) (T, error) {
	var zero T
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return zero, err
	}
	value, err := pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[T])
	if err != nil {
		return zero, err
	}
	return value, nil
}

func collectOptional[T any](ctx context.Context, db DBTX, sql string, args ...any) (*T, error) {
	value, err := collectOne[T](ctx, db, sql, args...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func collectMany[T any](ctx context.Context, db DBTX, sql string, args ...any) ([]T, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByNameLax[T])
}

func collectInt64Rows(rows pgx.Rows) ([]int64, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (int64, error) {
		var value int64
		err := row.Scan(&value)
		return value, err
	})
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

func queryCount(ctx context.Context, db DBTX, sql string, args ...any) (int64, error) {
	var count int64
	if err := db.QueryRow(ctx, sql, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func clamp(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}
