package marketcollector

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const (
	DefaultChainID            int64 = 56
	DefaultCursorKey                = "market_logs"
	DefaultInboxInterval            = 2 * time.Second
	DefaultScanInterval             = time.Minute
	DefaultSnapshotInterval         = 20 * time.Second
	DefaultDiscoveryInterval        = 5 * time.Minute
	DefaultHealthInterval           = time.Minute
	DefaultCleanupInterval          = 6 * time.Hour
	DefaultConfirmationDepth  int64 = 15
	DefaultScanBatchSize      int64 = 100
	DefaultInitialLookback    int64 = 200
	DefaultReorgLookback      int   = 128
	DefaultInboxBatchSize     int   = 100
	DefaultWebhookMaxAttempts int   = 10
	DefaultWebhookLease             = 2 * time.Minute
	DefaultWebhookMaxAge            = 5 * time.Minute
	DefaultMonthlyCULimit     int64 = 100_000_000
)

var (
	ErrCollectorDisabled   = errors.New("market collector is disabled")
	ErrWebhookUnavailable  = errors.New("market webhook signing key is not configured")
	ErrInvalidWebhook      = errors.New("invalid market webhook")
	ErrInvalidSignature    = errors.New("invalid market webhook signature")
	ErrExpiredWebhook      = errors.New("market webhook timestamp is outside the accepted window")
	ErrWebhookBodyTooLarge = errors.New("market webhook body is too large")
	ErrNoCanonicalAncestor = errors.New("no canonical ancestor found within the reorganization lookback")
	ErrUnsupportedChain    = errors.New("unsupported market collector chain")
)

type Settings struct {
	Enabled            bool
	ChainKey           string
	ChainID            int64
	ChainFallback      string
	CursorKey          string
	WebhookSigningKey  string
	WebhookSigningKeys map[string]string
	WebhookAutoRepair  bool
	PublicAppURL       string
	WebhookMaxAge      time.Duration
	WebhookLease       time.Duration
	WebhookMaxAttempts int
	InboxBatchSize     int
	ConfirmationDepth  int64
	ScanBatchSize      int64
	InitialLookback    int64
	ReorgLookback      int
	InboxInterval      time.Duration
	ScanInterval       time.Duration
	SnapshotInterval   time.Duration
	DiscoveryInterval  time.Duration
	HealthInterval     time.Duration
	CleanupInterval    time.Duration
	MonthlyCULimit     int64
}

func (settings Settings) normalized() Settings {
	if settings.ChainKey == "" {
		settings.ChainKey = "bsc"
	}
	if settings.ChainID == 0 {
		if profile, err := chain.ChainProfile(settings.ChainKey, ""); err == nil {
			settings.ChainID = profile.ChainID
		} else {
			settings.ChainID = DefaultChainID
		}
	}
	if settings.CursorKey == "" {
		settings.CursorKey = DefaultCursorKey
	}
	if settings.WebhookMaxAge <= 0 {
		settings.WebhookMaxAge = DefaultWebhookMaxAge
	}
	if settings.WebhookLease <= 0 {
		settings.WebhookLease = DefaultWebhookLease
	}
	if settings.WebhookMaxAttempts <= 0 {
		settings.WebhookMaxAttempts = DefaultWebhookMaxAttempts
	}
	if settings.InboxBatchSize <= 0 {
		settings.InboxBatchSize = DefaultInboxBatchSize
	}
	if settings.ConfirmationDepth <= 0 {
		settings.ConfirmationDepth = DefaultConfirmationDepth
	}
	if settings.ScanBatchSize <= 0 {
		settings.ScanBatchSize = DefaultScanBatchSize
	}
	if settings.InitialLookback <= 0 {
		settings.InitialLookback = DefaultInitialLookback
	}
	if settings.ReorgLookback <= 0 {
		settings.ReorgLookback = DefaultReorgLookback
	}
	if settings.InboxInterval <= 0 {
		settings.InboxInterval = DefaultInboxInterval
	}
	if settings.ScanInterval <= 0 {
		settings.ScanInterval = DefaultScanInterval
	}
	if settings.SnapshotInterval <= 0 {
		settings.SnapshotInterval = DefaultSnapshotInterval
	}
	if settings.DiscoveryInterval <= 0 {
		settings.DiscoveryInterval = DefaultDiscoveryInterval
	}
	if settings.HealthInterval <= 0 {
		settings.HealthInterval = DefaultHealthInterval
	}
	if settings.CleanupInterval <= 0 {
		settings.CleanupInterval = DefaultCleanupInterval
	}
	if settings.MonthlyCULimit <= 0 {
		settings.MonthlyCULimit = DefaultMonthlyCULimit
	}
	if len(settings.WebhookSigningKeys) > 0 {
		keys := make(map[string]string, len(settings.WebhookSigningKeys))
		for scope, secret := range settings.WebhookSigningKeys {
			scope = normalizeWebhookKeyScope(scope)
			secret = strings.TrimSpace(secret)
			if scope != "" && secret != "" {
				keys[scope] = secret
			}
		}
		settings.WebhookSigningKeys = keys
	}
	return settings
}

func (settings Settings) webhookSigningKey(category string) string {
	if key := settings.WebhookSigningKeys[settings.ChainKey+":"+category]; key != "" {
		return key
	}
	// Unscoped keys predate multi-chain callbacks and belong to the legacy BNB
	// subscriptions. Reusing them on another chain would allow a valid BNB
	// delivery to authenticate against the wrong route.
	if settings.ChainKey == "bsc" {
		if key := settings.WebhookSigningKeys[category]; key != "" {
			return key
		}
		return settings.WebhookSigningKey
	}
	return ""
}

type Repository interface {
	ListActiveMarketProjectsForCollection(context.Context, int64, int) ([]store.MarketProject, error)
	ListMarketCollectionTargets(context.Context, int64) ([]store.MarketCollectionTarget, error)
	UpsertMarketPool(context.Context, store.UpsertMarketPoolParams) (store.MarketPool, error)
	EnsureMarketProjectPool(context.Context, store.EnsureMarketProjectPoolParams) (store.MarketProjectPool, error)
	CreateMarketSnapshot(context.Context, store.CreateMarketSnapshotParams) (store.MarketSnapshot, error)
	LatestMarketSnapshot(context.Context, int64, string, *int64) (*store.MarketSnapshot, error)

	CreateWebhookInboxMessage(context.Context, store.CreateWebhookInboxParams) (store.WebhookInboxMessage, bool, error)
	ClaimWebhookInboxMessages(context.Context, int64, int) ([]store.WebhookInboxMessage, error)
	MarkWebhookInboxProcessed(context.Context, int64) (store.WebhookInboxMessage, error)
	MarkWebhookInboxFailed(context.Context, int64, string, time.Time, bool) (store.WebhookInboxMessage, error)
	RecoverStaleWebhookInbox(context.Context, int64, time.Time, int) (int64, error)
	GetNoditWebhookSubscriptionByCategory(context.Context, string, int64, string) (*store.NoditWebhookSubscription, error)
	ListNoditWebhookSubscriptions(context.Context, *int64) ([]store.NoditWebhookSubscription, error)
	UpsertNoditWebhookSubscription(context.Context, store.UpsertNoditWebhookSubscriptionParams) (store.NoditWebhookSubscription, error)
	UpdateNoditWebhookSubscriptionCheck(context.Context, int64, string, string, time.Time) (store.NoditWebhookSubscription, error)

	GetMarketChainCursor(context.Context, int64, string) (*store.MarketChainCursor, error)
	AdvanceMarketChainCursor(context.Context, store.AdvanceMarketChainCursorParams) (store.MarketChainCursor, error)
	UpsertMarketScannedBlock(context.Context, store.UpsertMarketScannedBlockParams) (store.MarketScannedBlock, error)
	ListCanonicalMarketScannedBlocks(context.Context, int64, string, int64, int) ([]store.MarketScannedBlock, error)
	ReconcileMarketReorg(context.Context, int64, string, int64, string, string) (store.MarketReorgResult, error)
	MarkMarketBlockConfirmed(context.Context, int64, int64) (int64, error)
	ListUnconfirmedMarketEventBlocks(context.Context, int64, int64, int64) ([]int64, error)
	MarkMarketBlockReorged(context.Context, int64, int64, string) (int64, error)
	CreateMarketEvent(context.Context, store.CreateMarketEventParams) (store.MarketEvent, bool, error)
	UpdateMarketProjectsFourMemeStatus(context.Context, int64, string, string) (int64, error)

	RecordMarketProviderHealth(context.Context, store.RecordMarketProviderHealthParams) (store.MarketProviderHealth, error)
	UpsertMarketProviderUsage(context.Context, store.UpsertMarketProviderUsageParams) (store.MarketProviderUsage, error)
	AddMarketProviderUsage(context.Context, store.AddMarketProviderUsageParams) (store.MarketProviderUsage, error)
	CleanupMarketCollectionData(context.Context, time.Time, time.Time, time.Time) (int64, error)
}

type ChainClient interface {
	LatestBlockNumber(context.Context, string, string) (uint64, error)
	Logs(context.Context, string, string, chain.LogFilter) ([]chain.RPCLog, error)
	BlockByNumber(context.Context, uint64, bool, string, string) (map[string]any, error)
	RPCTransactionByHash(context.Context, string, string, string) (map[string]any, error)
	TransactionReceipt(context.Context, string, string, string) (map[string]any, error)
	PoolTokens(context.Context, string, string, string) (string, string, error)
	PoolFactory(context.Context, string, string, string) (string, error)
	ListWebhooks(context.Context, string, string, chain.WebhookListOptions) (chain.WebhookList, error)
	UpdateWebhook(context.Context, string, string, string, chain.WebhookUpdateRequest) error
}

type Lock interface {
	Unlock(context.Context) error
}

type TryLockFunc func(context.Context, string) (Lock, bool, error)

type Dependencies struct {
	Repository       Repository
	Chain            ChainClient
	Market           marketdata.Provider
	TryLock          TryLockFunc
	Now              func() time.Time
	EstimatedCUMilli *atomic.Int64
}

type Service struct {
	repository Repository
	chain      ChainClient
	market     marketdata.Provider
	tryLock    TryLockFunc
	settings   Settings
	now        func() time.Time

	estimatedCU       *atomic.Int64
	lastDiscoveryUnix atomic.Int64
}

func New(dependencies Dependencies, settings Settings) *Service {
	now := dependencies.Now
	if now == nil {
		now = time.Now
	}
	estimatedCU := dependencies.EstimatedCUMilli
	if estimatedCU == nil {
		estimatedCU = &atomic.Int64{}
	}
	return &Service{
		repository:  dependencies.Repository,
		chain:       dependencies.Chain,
		market:      dependencies.Market,
		tryLock:     dependencies.TryLock,
		settings:    settings.normalized(),
		now:         now,
		estimatedCU: estimatedCU,
	}
}

func (service *Service) Enabled() bool {
	return service != nil && service.settings.Enabled
}

func (service *Service) withTaskLock(
	ctx context.Context,
	task string,
	fn func(context.Context) error,
) error {
	if service == nil || !service.settings.Enabled {
		return ErrCollectorDisabled
	}
	if service.tryLock == nil {
		return fn(ctx)
	}
	lock, acquired, err := service.tryLock(
		ctx,
		"market:"+service.settings.ChainKey+":"+task,
	)
	if err != nil || !acquired {
		return err
	}
	defer func() { _ = lock.Unlock(ctx) }()
	return fn(ctx)
}

func recordTaskError(
	ctx context.Context,
	logger *slog.Logger,
	task string,
	err error,
) {
	if err != nil && !errors.Is(err, ErrCollectorDisabled) && ctx.Err() == nil {
		logger.Error("market collector task failed", "task", task, "error", err)
	}
}
