package store

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var marketHashPattern = regexp.MustCompile(`^0x[0-9a-f]{64}$`)

const marketScannedBlockColumns = `
	id, chain_key, chain_id, cursor_key, block_number, block_hash,
	parent_hash, block_timestamp, canonical, scanned_at,
	created_at, updated_at
`

const marketProviderHealthColumns = `
	id, provider, component, chain_key, chain_id, status,
	consecutive_failures, latency_ms, last_success_at, last_failure_at,
	last_checked_at, last_error, metadata, created_at, updated_at
`

const marketProviderUsageColumns = `
	id, provider, metric, period_start, period_end,
	used_units::text AS used_units,
	limit_units::text AS limit_units,
	usage_percent::text AS usage_percent,
	alert_level, last_alert_at, metadata, checked_at, created_at, updated_at
`

type UpsertMarketScannedBlockParams struct {
	ChainKey       string
	ChainID        int64
	CursorKey      string
	BlockNumber    int64
	BlockHash      string
	ParentHash     string
	BlockTimestamp *time.Time
	ScannedAt      time.Time
}

type RecordMarketProviderHealthParams struct {
	Provider  string
	Component string
	ChainKey  string
	ChainID   int64
	Success   bool
	Latency   time.Duration
	Error     string
	Metadata  json.RawMessage
	CheckedAt time.Time
}

type UpsertMarketProviderUsageParams struct {
	Provider     string
	Metric       string
	PeriodStart  time.Time
	PeriodEnd    time.Time
	UsedUnits    string
	LimitUnits   *string
	UsagePercent *string
	AlertLevel   int32
	LastAlertAt  *time.Time
	Metadata     json.RawMessage
	CheckedAt    time.Time
}

type AddMarketProviderUsageParams struct {
	Provider    string
	Metric      string
	PeriodStart time.Time
	PeriodEnd   time.Time
	DeltaUnits  string
	LimitUnits  string
	Metadata    json.RawMessage
	CheckedAt   time.Time
}

type EnsureMarketProjectPoolParams struct {
	DeBoxUserID     string
	MarketProjectID int64
	MarketPoolID    int64
	SelectIfNone    bool
	DiscoverySource string
}

// MarketTaskLock is connection scoped so multiple Railway instances cannot run
// the same collector task concurrently.
type MarketTaskLock struct {
	conn *pgxpool.Conn
	key  int64
	once sync.Once
}

func (s *Store) TryMarketTaskLock(
	ctx context.Context,
	task string,
) (*MarketTaskLock, bool, error) {
	if s.pool == nil {
		return nil, false, ErrPoolRequired
	}
	task = strings.TrimSpace(task)
	if task == "" {
		return nil, false, fmt.Errorf("market task name is required")
	}
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte("debox-market-collector:" + task))
	key := int64(hasher.Sum64())
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquire market task connection: %w", err)
	}
	var acquired bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&acquired); err != nil {
		conn.Release()
		return nil, false, fmt.Errorf("try market task lock: %w", err)
	}
	if !acquired {
		conn.Release()
		return nil, false, nil
	}
	return &MarketTaskLock{conn: conn, key: key}, true, nil
}

func (lock *MarketTaskLock) Unlock(ctx context.Context) (err error) {
	lock.once.Do(func() {
		base := context.Background()
		if ctx != nil {
			base = context.WithoutCancel(ctx)
		}
		unlockCtx, cancel := context.WithTimeout(base, 5*time.Second)
		defer cancel()
		var unlocked bool
		if scanErr := lock.conn.QueryRow(
			unlockCtx,
			"SELECT pg_advisory_unlock($1)",
			lock.key,
		).Scan(&unlocked); scanErr != nil {
			raw := lock.conn.Hijack()
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer closeCancel()
			_ = raw.Close(closeCtx)
			err = fmt.Errorf("unlock market task: %w", scanErr)
			return
		}
		lock.conn.Release()
		if !unlocked {
			err = fmt.Errorf("unlock market task: lock was not held")
		}
	})
	return err
}

func (s *Store) ListActiveMarketProjectsForCollection(
	ctx context.Context,
	chainID int64,
	limit int,
) ([]MarketProject, error) {
	limit = clamp(limit, 1, 5000)
	values, err := collectMany[MarketProject](ctx, s.db, `
		SELECT `+marketProjectColumns+`
		FROM market_projects
		WHERE status = 'active' AND chain_id = $1
		ORDER BY COALESCE(last_discovered_at, created_at), id
		LIMIT $2
	`, chainID, limit)
	if err != nil {
		return nil, fmt.Errorf("list active market projects for collection: %w", err)
	}
	return values, nil
}

func (s *Store) ListMarketProjectsDueHolderRefresh(
	ctx context.Context,
	chainID int64,
	notAfter time.Time,
	limit int,
) ([]MarketProject, error) {
	projects, err := collectMany[MarketProject](ctx, s.db, `
		SELECT DISTINCT ON (mp.chain_id, mp.token_address)
		       `+marketProjectColumns+`
		FROM market_projects mp
		WHERE mp.chain_id = $1
		  AND mp.status = 'active'
		  AND NOT EXISTS (
			SELECT 1
			FROM market_holders mh
			WHERE mh.chain_id = mp.chain_id
			  AND mh.token_address = mp.token_address
			  AND mh.updated_at > $2
		  )
		ORDER BY mp.chain_id, mp.token_address, mp.created_at, mp.id
		LIMIT $3
	`, chainID, notAfter.UTC(), clamp(limit, 1, 500))
	if err != nil {
		return nil, fmt.Errorf("list projects due holder refresh: %w", err)
	}
	return projects, nil
}

func (s *Store) ListMarketCollectionTargets(
	ctx context.Context,
	chainID int64,
) ([]MarketCollectionTarget, error) {
	values, err := collectMany[MarketCollectionTarget](ctx, s.db, `
		SELECT
			mp.id AS market_project_id, mp.debox_user_id, mp.chain_key,
			mp.chain_id, mp.token_address, mp.token_name, mp.token_symbol,
			mp.token_decimals, p.id AS market_pool_id, p.protocol,
			p.protocol_version, p.pool_key, p.pool_address,
			p.token0_address, p.token0_symbol, p.token0_decimals,
			p.token1_address, p.token1_symbol, p.token1_decimals,
			p.parser_adapter, mpp.selected, mpp.is_primary
		FROM market_projects mp
		JOIN market_project_pools mpp ON mpp.market_project_id = mp.id
		JOIN market_pools p ON p.id = mpp.market_pool_id
		WHERE mp.status = 'active'
		  AND mp.chain_id = $1
		  AND mpp.selected = 1
		ORDER BY p.id, mp.id
	`, chainID)
	if err != nil {
		return nil, fmt.Errorf("list market collection targets: %w", err)
	}
	return values, nil
}

// EnsureMarketProjectPool records discovery without changing an existing
// user's selection. SelectIfNone only selects the pool if the project currently
// has no selected pool.
func (s *Store) EnsureMarketProjectPool(
	ctx context.Context,
	params EnsureMarketProjectPoolParams,
) (MarketProjectPool, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (MarketProjectPool, error) {
		var projectChainID, poolChainID int64
		var token, token0, token1 string
		var hasSelected bool
		if err := tx.QueryRow(ctx, `
			SELECT mp.chain_id, mp.token_address, p.chain_id,
			       p.token0_address, p.token1_address,
			       EXISTS (
			           SELECT 1 FROM market_project_pools current
			           WHERE current.market_project_id = mp.id AND current.selected = 1
			       )
			FROM market_projects mp
			CROSS JOIN market_pools p
			WHERE mp.id = $1 AND mp.debox_user_id = $2 AND p.id = $3
			FOR UPDATE OF mp
		`, params.MarketProjectID, params.DeBoxUserID, params.MarketPoolID).Scan(
			&projectChainID, &token, &poolChainID, &token0, &token1, &hasSelected,
		); err != nil {
			if isNoRows(err) {
				return MarketProjectPool{}, ErrNotFound
			}
			return MarketProjectPool{}, fmt.Errorf("validate discovered market pool: %w", err)
		}
		if projectChainID != poolChainID || (token != token0 && token != token1) {
			return MarketProjectPool{}, ErrMarketPoolMismatch
		}
		selectPool := params.SelectIfNone && !hasSelected
		value, err := collectOne[MarketProjectPool](ctx, tx, `
			INSERT INTO market_project_pools (
				market_project_id, market_pool_id, selected, is_primary, discovery_source
			)
			VALUES ($1, $2, $3, $3, $4)
			ON CONFLICT (market_project_id, market_pool_id) DO UPDATE
			SET discovery_source = EXCLUDED.discovery_source,
			    updated_at = NOW()
			RETURNING `+marketProjectPoolColumns,
			params.MarketProjectID,
			params.MarketPoolID,
			boolInt(selectPool),
			strings.TrimSpace(params.DiscoverySource),
		)
		if err != nil {
			return MarketProjectPool{}, fmt.Errorf("ensure discovered market pool: %w", err)
		}
		if selectPool {
			if _, err := tx.Exec(ctx, `
				UPDATE market_projects
				SET main_pool_id = $1, updated_at = NOW()
				WHERE id = $2 AND debox_user_id = $3 AND main_pool_id IS NULL
			`, params.MarketPoolID, params.MarketProjectID, params.DeBoxUserID); err != nil {
				return MarketProjectPool{}, fmt.Errorf("set discovered primary pool: %w", err)
			}
		}
		return value, nil
	})
}

func (s *Store) UpsertMarketScannedBlock(
	ctx context.Context,
	params UpsertMarketScannedBlockParams,
) (MarketScannedBlock, error) {
	params.ChainKey = strings.ToLower(strings.TrimSpace(params.ChainKey))
	params.CursorKey = strings.TrimSpace(params.CursorKey)
	params.BlockHash = strings.ToLower(strings.TrimSpace(params.BlockHash))
	params.ParentHash = strings.ToLower(strings.TrimSpace(params.ParentHash))
	if params.ChainKey == "" || params.ChainID <= 0 || params.CursorKey == "" ||
		params.BlockNumber < 0 || !marketHashPattern.MatchString(params.BlockHash) ||
		!marketHashPattern.MatchString(params.ParentHash) {
		return MarketScannedBlock{}, ErrInvalidMarketCursor
	}
	scannedAt := params.ScannedAt.UTC()
	if scannedAt.IsZero() {
		scannedAt = time.Now().UTC()
	}
	return withTxValue(ctx, s.db, func(tx DBTX) (MarketScannedBlock, error) {
		if _, err := tx.Exec(ctx, `
			UPDATE market_scanned_blocks
			SET canonical = 0, updated_at = NOW()
			WHERE chain_id = $1 AND cursor_key = $2 AND block_number = $3
			  AND block_hash <> $4 AND canonical = 1
		`, params.ChainID, params.CursorKey, params.BlockNumber, params.BlockHash); err != nil {
			return MarketScannedBlock{}, fmt.Errorf("retire replaced market block: %w", err)
		}
		value, err := collectOne[MarketScannedBlock](ctx, tx, `
			INSERT INTO market_scanned_blocks (
				chain_key, chain_id, cursor_key, block_number, block_hash,
				parent_hash, block_timestamp, canonical, scanned_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 1, $8)
			ON CONFLICT (chain_id, cursor_key, block_number, block_hash) DO UPDATE
			SET parent_hash = EXCLUDED.parent_hash,
			    block_timestamp = COALESCE(EXCLUDED.block_timestamp, market_scanned_blocks.block_timestamp),
			    canonical = 1,
			    scanned_at = EXCLUDED.scanned_at,
			    updated_at = NOW()
			RETURNING `+marketScannedBlockColumns,
			params.ChainKey, params.ChainID, params.CursorKey, params.BlockNumber,
			params.BlockHash, params.ParentHash, params.BlockTimestamp, scannedAt,
		)
		if err != nil {
			return MarketScannedBlock{}, fmt.Errorf("upsert market scanned block: %w", err)
		}
		return value, nil
	})
}

func (s *Store) ListCanonicalMarketScannedBlocks(
	ctx context.Context,
	chainID int64,
	cursorKey string,
	throughBlock int64,
	limit int,
) ([]MarketScannedBlock, error) {
	limit = clamp(limit, 1, 1000)
	values, err := collectMany[MarketScannedBlock](ctx, s.db, `
		SELECT `+marketScannedBlockColumns+`
		FROM market_scanned_blocks
		WHERE chain_id = $1 AND cursor_key = $2 AND canonical = 1
		  AND block_number <= $3
		ORDER BY block_number DESC
		LIMIT $4
	`, chainID, strings.TrimSpace(cursorKey), throughBlock, limit)
	if err != nil {
		return nil, fmt.Errorf("list canonical market scanned blocks: %w", err)
	}
	return values, nil
}

func (s *Store) ReconcileMarketReorg(
	ctx context.Context,
	chainID int64,
	cursorKey string,
	ancestorBlock int64,
	ancestorHash string,
	reason string,
) (MarketReorgResult, error) {
	ancestorHash = strings.ToLower(strings.TrimSpace(ancestorHash))
	if chainID <= 0 || ancestorBlock < 0 || strings.TrimSpace(cursorKey) == "" ||
		!marketHashPattern.MatchString(ancestorHash) {
		return MarketReorgResult{}, ErrInvalidMarketCursor
	}
	return withTxValue(ctx, s.db, func(tx DBTX) (MarketReorgResult, error) {
		blockTag, err := tx.Exec(ctx, `
			UPDATE market_scanned_blocks
			SET canonical = 0, updated_at = NOW()
			WHERE chain_id = $1 AND cursor_key = $2
			  AND block_number > $3 AND canonical = 1
		`, chainID, strings.TrimSpace(cursorKey), ancestorBlock)
		if err != nil {
			return MarketReorgResult{}, fmt.Errorf("retire reorged market blocks: %w", err)
		}
		eventTag, err := tx.Exec(ctx, `
			UPDATE market_events
			SET reorged = 1, confirmed = 0
			WHERE chain_id = $1 AND block_number > $2 AND reorged = 0
		`, chainID, ancestorBlock)
		if err != nil {
			return MarketReorgResult{}, fmt.Errorf("retire reorged market events: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE market_rule_events mre
			SET notification_status = 'skipped',
			    notification_error = 'source event was removed by a chain reorganization'
			FROM market_events me
			WHERE me.id = mre.market_event_id
			  AND me.chain_id = $1 AND me.block_number > $2 AND me.reorged = 1
			  AND mre.notification_status IN ('pending', 'sending', 'failed')
		`, chainID, ancestorBlock); err != nil {
			return MarketReorgResult{}, fmt.Errorf("skip reorged market notifications: %w", err)
		}
		cursor, err := collectOne[MarketChainCursor](ctx, tx, `
			UPDATE market_chain_cursors
			SET next_block_number = $1,
			    safe_block_number = LEAST(safe_block_number, $2),
			    last_block_hash = $3,
			    status = 'active',
			    last_error = $4,
			    updated_at = NOW()
			WHERE chain_id = $5 AND cursor_key = $6
			RETURNING `+marketChainCursorColumns,
			ancestorBlock+1, ancestorBlock, ancestorHash, truncate(reason, 2000),
			chainID, strings.TrimSpace(cursorKey),
		)
		if err != nil {
			return MarketReorgResult{}, fmt.Errorf("rewind reorged market cursor: %w", err)
		}
		return MarketReorgResult{
			ReorgedEvents: eventTag.RowsAffected(),
			ReorgedBlocks: blockTag.RowsAffected(),
			Cursor:        cursor,
		}, nil
	})
}

func (s *Store) RecoverStaleWebhookInbox(
	ctx context.Context,
	staleBefore time.Time,
	maxAttempts int,
) (int64, error) {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE webhook_inbox
		SET processing_status = CASE WHEN attempts >= $2 THEN 'dead' ELSE 'failed' END,
		    next_attempt_at = NOW(),
		    locked_at = NULL,
		    last_error = CASE
		        WHEN attempts >= $2 THEN 'processing lease expired; retry limit reached'
		        ELSE 'processing lease expired; recovered after worker interruption'
		    END
		WHERE processing_status = 'processing' AND locked_at < $1
	`, staleBefore.UTC(), maxAttempts)
	if err != nil {
		return 0, fmt.Errorf("recover stale webhook inbox: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *Store) ReplayWebhookInboxMessage(
	ctx context.Context,
	inboxID int64,
) (WebhookInboxMessage, error) {
	value, err := collectOne[WebhookInboxMessage](ctx, s.db, `
		UPDATE webhook_inbox
		SET processing_status = 'pending',
		    attempts = 0,
		    next_attempt_at = NOW(),
		    locked_at = NULL,
		    processed_at = NULL,
		    last_error = ''
		WHERE id = $1 AND signature_valid = 1
		  AND processing_status IN ('failed', 'dead')
		RETURNING `+webhookInboxColumns,
		inboxID,
	)
	if isNoRows(err) {
		return WebhookInboxMessage{}, ErrNotFound
	}
	if err != nil {
		return WebhookInboxMessage{}, fmt.Errorf("replay webhook inbox message: %w", err)
	}
	return value, nil
}

func (s *Store) GetNoditWebhookSubscriptionByCategory(
	ctx context.Context,
	provider string,
	chainID int64,
	category string,
) (*NoditWebhookSubscription, error) {
	value, err := collectOptional[NoditWebhookSubscription](ctx, s.db, `
		SELECT `+noditWebhookSubscriptionColumns+`
		FROM nodit_webhook_subscriptions
		WHERE provider = $1 AND chain_id = $2 AND event_category = $3
	`, strings.ToLower(strings.TrimSpace(provider)), chainID, strings.ToLower(strings.TrimSpace(category)))
	if err != nil {
		return nil, fmt.Errorf("get Nodit webhook subscription by category: %w", err)
	}
	return value, nil
}

func (s *Store) UpdateNoditWebhookSubscriptionCheck(
	ctx context.Context,
	subscriptionID int64,
	status string,
	checkError string,
	checkedAt time.Time,
) (NoditWebhookSubscription, error) {
	status = normalizeWebhookStatus(status)
	checkedAt = checkedAt.UTC()
	if checkedAt.IsZero() {
		checkedAt = time.Now().UTC()
	}
	value, err := collectOne[NoditWebhookSubscription](ctx, s.db, `
		UPDATE nodit_webhook_subscriptions
		SET status = $1,
		    last_checked_at = $2,
		    last_error = $3,
		    updated_at = NOW()
		WHERE id = $4
		RETURNING `+noditWebhookSubscriptionColumns,
		status, checkedAt, truncate(checkError, 2000), subscriptionID,
	)
	if isNoRows(err) {
		return NoditWebhookSubscription{}, ErrNotFound
	}
	if err != nil {
		return NoditWebhookSubscription{}, fmt.Errorf("update Nodit webhook check: %w", err)
	}
	return value, nil
}

func (s *Store) RecordMarketProviderHealth(
	ctx context.Context,
	params RecordMarketProviderHealthParams,
) (MarketProviderHealth, error) {
	params.Provider = strings.ToLower(strings.TrimSpace(params.Provider))
	params.Component = strings.ToLower(strings.TrimSpace(params.Component))
	params.ChainKey = strings.ToLower(strings.TrimSpace(params.ChainKey))
	if params.Provider == "" || params.Component == "" || params.ChainID < 0 {
		return MarketProviderHealth{}, fmt.Errorf("invalid market provider health")
	}
	checkedAt := params.CheckedAt.UTC()
	if checkedAt.IsZero() {
		checkedAt = time.Now().UTC()
	}
	var latencyMS *int64
	if params.Latency >= 0 {
		value := params.Latency.Milliseconds()
		latencyMS = &value
	}
	status := "healthy"
	if !params.Success {
		status = "degraded"
	}
	value, err := collectOne[MarketProviderHealth](ctx, s.db, `
		INSERT INTO market_provider_health (
			provider, component, chain_key, chain_id, status,
			consecutive_failures, latency_ms, last_success_at,
			last_failure_at, last_checked_at, last_error, metadata
		)
		VALUES (
			$1, $2, $3, $4, $5, CASE WHEN $6 THEN 0 ELSE 1 END, $7,
			CASE WHEN $6 THEN $8::timestamptz ELSE NULL END,
			CASE WHEN $6 THEN NULL ELSE $8::timestamptz END,
			$8::timestamptz, $9, $10
		)
		ON CONFLICT (provider, component, chain_id) DO UPDATE
		SET chain_key = EXCLUDED.chain_key,
		    consecutive_failures = CASE
		        WHEN $6 THEN 0
		        ELSE market_provider_health.consecutive_failures + 1
		    END,
		    status = CASE
		        WHEN $6 THEN 'healthy'
		        WHEN market_provider_health.consecutive_failures + 1 >= 3 THEN 'unavailable'
		        ELSE 'degraded'
		    END,
		    latency_ms = EXCLUDED.latency_ms,
		    last_success_at = CASE
		        WHEN $6 THEN $8::timestamptz ELSE market_provider_health.last_success_at
		    END,
		    last_failure_at = CASE
		        WHEN $6 THEN market_provider_health.last_failure_at ELSE $8::timestamptz
		    END,
		    last_checked_at = $8::timestamptz,
		    last_error = CASE WHEN $6 THEN '' ELSE EXCLUDED.last_error END,
		    metadata = EXCLUDED.metadata,
		    updated_at = NOW()
		RETURNING `+marketProviderHealthColumns,
		params.Provider, params.Component, params.ChainKey, params.ChainID,
		status, params.Success, latencyMS, checkedAt, truncate(params.Error, 2000),
		normalizedJSON(params.Metadata),
	)
	if err != nil {
		return MarketProviderHealth{}, fmt.Errorf("record market provider health: %w", err)
	}
	return value, nil
}

func (s *Store) ListMarketProviderHealth(
	ctx context.Context,
) ([]MarketProviderHealth, error) {
	values, err := collectMany[MarketProviderHealth](ctx, s.db, `
		SELECT `+marketProviderHealthColumns+`
		FROM market_provider_health
		ORDER BY status DESC, provider, component, chain_id
	`)
	if err != nil {
		return nil, fmt.Errorf("list market provider health: %w", err)
	}
	return values, nil
}

func (s *Store) UpsertMarketProviderUsage(
	ctx context.Context,
	params UpsertMarketProviderUsageParams,
) (MarketProviderUsage, error) {
	params.Provider = strings.ToLower(strings.TrimSpace(params.Provider))
	params.Metric = strings.ToLower(strings.TrimSpace(params.Metric))
	params.PeriodStart = params.PeriodStart.UTC()
	params.PeriodEnd = params.PeriodEnd.UTC()
	if params.Provider == "" || params.Metric == "" ||
		!params.PeriodEnd.After(params.PeriodStart) ||
		(params.AlertLevel != 0 && params.AlertLevel != 70 &&
			params.AlertLevel != 85 && params.AlertLevel != 95) {
		return MarketProviderUsage{}, fmt.Errorf("invalid market provider usage")
	}
	checkedAt := params.CheckedAt.UTC()
	if checkedAt.IsZero() {
		checkedAt = time.Now().UTC()
	}
	value, err := collectOne[MarketProviderUsage](ctx, s.db, `
		INSERT INTO market_provider_usage (
			provider, metric, period_start, period_end, used_units,
			limit_units, usage_percent, alert_level, last_alert_at,
			metadata, checked_at
		)
		VALUES (
			$1, $2, $3::timestamptz, $4::timestamptz, $5, $6, $7, $8,
			$9::timestamptz, $10, $11::timestamptz
		)
		ON CONFLICT (provider, metric, period_start, period_end) DO UPDATE
		SET used_units = EXCLUDED.used_units,
		    limit_units = EXCLUDED.limit_units,
		    usage_percent = EXCLUDED.usage_percent,
		    alert_level = EXCLUDED.alert_level,
		    last_alert_at = EXCLUDED.last_alert_at,
		    metadata = EXCLUDED.metadata,
		    checked_at = EXCLUDED.checked_at,
		    updated_at = NOW()
		RETURNING `+marketProviderUsageColumns,
		params.Provider, params.Metric, params.PeriodStart, params.PeriodEnd,
		params.UsedUnits, params.LimitUnits, params.UsagePercent,
		params.AlertLevel, params.LastAlertAt, normalizedJSON(params.Metadata), checkedAt,
	)
	if err != nil {
		return MarketProviderUsage{}, fmt.Errorf("upsert market provider usage: %w", err)
	}
	return value, nil
}

func (s *Store) AddMarketProviderUsage(
	ctx context.Context,
	params AddMarketProviderUsageParams,
) (MarketProviderUsage, error) {
	params.Provider = strings.ToLower(strings.TrimSpace(params.Provider))
	params.Metric = strings.ToLower(strings.TrimSpace(params.Metric))
	params.PeriodStart = params.PeriodStart.UTC()
	params.PeriodEnd = params.PeriodEnd.UTC()
	checkedAt := params.CheckedAt.UTC()
	if checkedAt.IsZero() {
		checkedAt = time.Now().UTC()
	}
	if params.Provider == "" || params.Metric == "" ||
		!params.PeriodEnd.After(params.PeriodStart) {
		return MarketProviderUsage{}, fmt.Errorf("invalid market provider usage delta")
	}
	value, err := collectOne[MarketProviderUsage](ctx, s.db, `
		INSERT INTO market_provider_usage (
			provider, metric, period_start, period_end, used_units,
			limit_units, usage_percent, alert_level, last_alert_at,
			metadata, checked_at
		)
		VALUES (
			$1, $2, $3::timestamptz, $4::timestamptz, $5, $6,
			LEAST(100, ($5::numeric / $6::numeric) * 100),
			CASE
				WHEN ($5::numeric / $6::numeric) * 100 >= 95 THEN 95
				WHEN ($5::numeric / $6::numeric) * 100 >= 85 THEN 85
				WHEN ($5::numeric / $6::numeric) * 100 >= 70 THEN 70
				ELSE 0
			END,
			CASE
				WHEN ($5::numeric / $6::numeric) * 100 >= 70
				THEN $8::timestamptz
				ELSE NULL
			END,
			$7, $8::timestamptz
		)
		ON CONFLICT (provider, metric, period_start, period_end) DO UPDATE
		SET used_units = market_provider_usage.used_units + EXCLUDED.used_units,
		    limit_units = EXCLUDED.limit_units,
		    usage_percent = LEAST(
		        100,
		        (
		            (market_provider_usage.used_units + EXCLUDED.used_units) /
		            EXCLUDED.limit_units
		        ) * 100
		    ),
		    alert_level = CASE
				WHEN (
					(market_provider_usage.used_units + EXCLUDED.used_units) /
					EXCLUDED.limit_units
				) * 100 >= 95 THEN 95
				WHEN (
					(market_provider_usage.used_units + EXCLUDED.used_units) /
					EXCLUDED.limit_units
				) * 100 >= 85 THEN 85
				WHEN (
					(market_provider_usage.used_units + EXCLUDED.used_units) /
					EXCLUDED.limit_units
				) * 100 >= 70 THEN 70
				ELSE 0
			END,
		    last_alert_at = CASE
		        WHEN (
		            (market_provider_usage.used_units + EXCLUDED.used_units) /
		            EXCLUDED.limit_units
		        ) * 100 >= 70
		         AND (
		            CASE
						WHEN (
							(market_provider_usage.used_units + EXCLUDED.used_units) /
							EXCLUDED.limit_units
						) * 100 >= 95 THEN 95
						WHEN (
							(market_provider_usage.used_units + EXCLUDED.used_units) /
							EXCLUDED.limit_units
						) * 100 >= 85 THEN 85
						ELSE 70
		            END
		         ) > market_provider_usage.alert_level
		        THEN EXCLUDED.checked_at
		        ELSE market_provider_usage.last_alert_at
		    END,
		    metadata = EXCLUDED.metadata,
		    checked_at = EXCLUDED.checked_at,
		    updated_at = NOW()
		RETURNING `+marketProviderUsageColumns,
		params.Provider,
		params.Metric,
		params.PeriodStart,
		params.PeriodEnd,
		params.DeltaUnits,
		params.LimitUnits,
		normalizedJSON(params.Metadata),
		checkedAt,
	)
	if err != nil {
		return MarketProviderUsage{}, fmt.Errorf("add market provider usage: %w", err)
	}
	return value, nil
}

func (s *Store) CleanupMarketCollectionData(
	ctx context.Context,
	webhookBefore time.Time,
	eventBefore time.Time,
	blockBefore time.Time,
) (int64, error) {
	var removed int64
	for _, item := range []struct {
		query string
		arg   time.Time
	}{
		{`DELETE FROM webhook_inbox
		  WHERE received_at < $1 AND processing_status IN ('processed', 'dead')`, webhookBefore.UTC()},
		{`DELETE FROM market_events
		  WHERE occurred_at < $1
		    AND NOT EXISTS (
		        SELECT 1 FROM market_rule_events mre WHERE mre.market_event_id = market_events.id
		    )`, eventBefore.UTC()},
		{`DELETE FROM market_scanned_blocks
		  WHERE scanned_at < $1 AND canonical = 0`, blockBefore.UTC()},
	} {
		tag, err := s.db.Exec(ctx, item.query, item.arg)
		if err != nil {
			return removed, fmt.Errorf("cleanup market collection data: %w", err)
		}
		removed += tag.RowsAffected()
	}
	return removed, nil
}
