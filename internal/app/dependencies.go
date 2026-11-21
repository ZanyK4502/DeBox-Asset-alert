package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/assetcatalog"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/auth"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/bot"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/config"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/debox"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/httpapi"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/management"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketcollector"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketrules"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketview"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/monitor"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdetail"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/payment"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/subscription"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/summary"
)

// Enable only when maintenance requires suspending purchases and user configuration writes.
const userWriteMaintenance = false

// Menu availability is independent from the write gate. Opening the existing
// menu after acceptance must not reopen purchases or monitoring expansion.
const privateStartMaintenance = false

type dependencies struct {
	httpapi httpapi.Dependencies
	bot     *bot.Runner
	monitor *monitor.Runner
	payment *payment.Runner
	summary *summary.Runner
	market  *marketcollector.Runner
	rules   *marketrules.Runner
	holders *marketrules.HolderRefreshRunner
}

func buildDependencies(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
) (dependencies, func(), error) {
	quotaLimits, err := cfg.EffectiveQuotaLimits()
	if err != nil {
		return dependencies{}, func() {}, fmt.Errorf("configure plan quotas: %w", err)
	}
	monitoringChains, err := cfg.EffectiveMarketMonitoringChainKeys()
	if err != nil {
		return dependencies{}, func() {}, fmt.Errorf("configure market monitoring scope: %w", err)
	}
	collectorChains, err := marketCollectorChainKeys(cfg, monitoringChains)
	if err != nil {
		return dependencies{}, func() {}, err
	}
	catalog, err := plans.NewCatalogWithMarketScope(cfg.SubscriptionPrice, cfg.SubscriptionDays, cfg.SubscriptionTokenSymbol, quotaLimits, monitoringChains)
	if err != nil {
		return dependencies{}, func() {}, fmt.Errorf("create plan catalog: %w", err)
	}
	capacity, err := resourceCapacityPolicy(cfg, catalog)
	if err != nil {
		return dependencies{}, func() {}, fmt.Errorf("configure resource capacity: %w", err)
	}
	repository, err := store.Open(ctx, cfg.DatabaseURL, store.WithMarketTaskLockConnections(cfg.TaskLockConnections()), store.WithResourceCapacityPolicy(capacity), store.WithMarketScopePolicies(marketScopePolicies(catalog)))
	if err != nil {
		return dependencies{}, func() {}, fmt.Errorf("open data store: %w", err)
	}
	closeDependencies := repository.Close
	if err := repository.Migrate(ctx); err != nil {
		closeDependencies()
		return dependencies{}, func() {}, fmt.Errorf("migrate data store: %w", err)
	}

	deboxClient, err := debox.NewOpenAPIClient(
		cfg.DeBoxBotAPIKey,
		cfg.DeBoxOpenAPIBase,
		nil,
	)
	if err != nil {
		closeDependencies()
		return dependencies{}, func() {}, fmt.Errorf("create DeBox client: %w", err)
	}
	noditCUMeter := &atomic.Int64{}
	chainClient, err := chain.NewClient(
		cfg.NoditAPIKey,
		cfg.NoditBaseURL,
		chain.WithCURateLimit(cfg.NoditCUPerSecond),
		chain.WithHolderCURateLimit(min(75, max(1, cfg.NoditCUPerSecond/3))),
		chain.WithUsageObserver(func(usage chain.NoditUsage) {
			noditCUMeter.Add(usage.CU * 1000)
		}),
	)
	if err != nil {
		closeDependencies()
		return dependencies{}, func() {}, fmt.Errorf("create Nodit client: %w", err)
	}
	marketDataClient, err := marketdata.NewDexScreenerClient(cfg.DexScreenerBaseURL)
	if err != nil {
		closeDependencies()
		return dependencies{}, func() {}, fmt.Errorf("create DexScreener client: %w", err)
	}
	coinGeckoClient, err := assetcatalog.NewCoinGeckoClient(
		assetcatalog.CoinGeckoSettings{
			Tier:    cfg.CoinGeckoAPITier,
			APIKey:  cfg.CoinGeckoAPIKey,
			BaseURL: cfg.CoinGeckoBaseURL,
		},
	)
	if err != nil {
		closeDependencies()
		return dependencies{}, func() {}, fmt.Errorf("create CoinGecko client: %w", err)
	}
	assetCatalog, err := assetcatalog.NewCatalog(
		coinGeckoClient,
		marketDataClient,
		nil,
		assetcatalog.WithManualProviders(chainClient, marketDataClient),
	)
	if err != nil {
		closeDependencies()
		return dependencies{}, func() {}, fmt.Errorf("create asset catalog: %w", err)
	}
	marketReadClient, err := chain.NewMarketReadClient(chainClient, cfg.MarketRPCFallbackURLs)
	if err != nil {
		closeDependencies()
		return dependencies{}, func() {}, fmt.Errorf("create market read client: %w", err)
	}
	marketDependencies := marketcollector.Dependencies{
		Repository:       repository,
		Chain:            marketReadClient,
		Market:           marketDataClient,
		EstimatedCUMilli: noditCUMeter,
		Logger:           logger,
		TryLock: func(
			ctx context.Context,
			task string,
		) (marketcollector.Lock, bool, error) {
			lock, acquired, lockErr := repository.AcquireMarketTaskLock(ctx, task)
			if lockErr != nil || !acquired {
				return nil, acquired, lockErr
			}
			return lock, true, nil
		},
	}
	marketServices := make([]*marketcollector.Service, 0, len(collectorChains))
	for _, marketChainKey := range collectorChains {
		marketChain, profileErr := chain.ChainProfile(marketChainKey, "")
		if profileErr != nil {
			closeDependencies()
			return dependencies{}, func() {}, fmt.Errorf(
				"create market chain profile: %w",
				profileErr,
			)
		}
		marketServices = append(marketServices, marketcollector.New(
			marketDependencies,
			marketcollector.Settings{
				Enabled:              cfg.MarketCollectorEnabled,
				ChainKey:             marketChain.Key,
				ChainID:              marketChain.ChainID,
				WebhookSigningKey:    cfg.NoditWebhookSigningKey,
				WebhookSigningKeys:   cfg.NoditWebhookSigningKeys,
				WebhookWrappingKey:   cfg.NoditWebhookWrappingKey,
				WebhookAutoRepair:    cfg.MarketWebhookAutoRepair,
				PublicAppURL:         cfg.PublicAppURL,
				ConfirmationDepth:    cfg.MarketConfirmationDepthFor(marketChain.Key),
				ScanBatchSize:        cfg.MarketScanBatchSize,
				HydrationConcurrency: cfg.MarketHydrationConcurrency,
				InitialLookback:      cfg.MarketInitialLookback,
				ReorgLookback:        cfg.MarketReorgLookback,
				InboxInterval:        cfg.MarketInboxInterval,
				ScanInterval:         cfg.MarketScanInterval,
				SnapshotInterval:     cfg.MarketSnapshotInterval,
				DiscoveryInterval:    cfg.MarketDiscoveryInterval,
				HealthInterval:       cfg.MarketHealthInterval,
				CleanupInterval:      cfg.MarketCleanupInterval,
				MonthlyCULimit:       cfg.NoditMonthlyCULimit,
			},
		))
	}
	// The legacy callback path was provisioned for BNB Chain before callbacks
	// became chain-scoped. Keep it pinned to BNB regardless of wallet defaults.
	marketCluster, err := marketcollector.NewCluster(marketServices, "bsc")
	if err != nil {
		closeDependencies()
		return dependencies{}, func() {}, fmt.Errorf("create market collector cluster: %w", err)
	}
	messenger, err := debox.NewMessenger(
		cfg.DeBoxBotAPIKey,
		cfg.DeBoxBotAPISecret,
		cfg.DeBoxOpenAPIBase,
		nil,
	)
	if err != nil {
		closeDependencies()
		return dependencies{}, func() {}, fmt.Errorf("create DeBox messenger: %w", err)
	}
	if cfg.DeBoxBotUserID != "" && cfg.DeBoxBotUserID != messenger.Self().UserId {
		closeDependencies()
		return dependencies{}, func() {}, fmt.Errorf("configured DeBox bot identity differs from authenticated bot")
	}
	admission, err := notificationdelivery.NewAdmission(repository, messenger.Self().UserId, cfg.NotificationSendLimit())
	if err == nil {
		err = messenger.SetNotificationAdmission(admission)
	}
	if err != nil {
		closeDependencies()
		return dependencies{}, func() {}, fmt.Errorf("create notification admission: %w", err)
	}
	subscriptions := subscription.New(repository, catalog)
	marketRuleService := newMarketRuleService(
		cfg,
		monitoringChains,
		marketrules.Dependencies{
			Repository:    repository,
			Notifications: messenger,
			Holders:       chainClient,
			PublicAppURL:  cfg.PublicAppURL,
			TryLock: func(
				ctx context.Context,
				task string,
			) (marketrules.Lock, bool, error) {
				lock, acquired, lockErr := repository.AcquireMarketTaskLock(ctx, task)
				if lockErr != nil || !acquired {
					return nil, acquired, lockErr
				}
				return lock, true, nil
			},
		},
	)
	// A full holder sample can have 20 pages. One worker leaves room to finish
	// its sample within the existing deadline at the holder request rate.
	holderRunner, err := marketrules.NewHolderRefreshRunner(marketRuleService, marketrules.HolderRefreshSettings{Workers: 1})
	if err != nil {
		closeDependencies()
		return dependencies{}, func() {}, fmt.Errorf("create holder refresh runner: %w", err)
	}
	tryMonitorLock := func(ctx context.Context) (monitor.Lock, bool, error) {
		return repository.TryMonitorExecutionLock(ctx)
	}
	walletReadClient := chain.NewWalletReadClient(chainClient)
	monitorExecutor := monitor.New(monitor.Dependencies{
		Repository:       repository,
		Chain:            walletReadClient,
		Notifications:    messenger,
		Catalog:          catalog,
		TryExecutionLock: tryMonitorLock,
		DefaultChainKey:  cfg.ChainKey,
		PublicAppURL:     cfg.PublicAppURL,
	})
	managementService := management.New(management.Dependencies{
		Repository:      repository,
		Entitlements:    subscriptions,
		Chain:           walletReadClient,
		Groups:          deboxClient,
		Notifications:   messenger,
		InitialChecker:  monitorExecutor,
		DefaultChainKey: cfg.ChainKey,
	})
	marketViewService := marketview.New(marketview.Dependencies{
		Repository:   repository,
		Entitlements: subscriptions,
		Chain:        chainClient,
		Market:       marketDataClient,
		Assets:       assetCatalog,
		Catalog:      catalog,
	})
	paymentService := payment.New(
		repository,
		chainClient,
		catalog,
		payment.Settings{
			Mode:             cfg.PaymentMode,
			RecipientAddress: cfg.PaymentRecipientAddress,
			TokenAddress:     cfg.SubscriptionTokenAddress,
			TokenSymbol:      cfg.SubscriptionTokenSymbol,
			TokenDecimals:    cfg.SubscriptionTokenDecimals,
		},
		subscriptions,
	)
	paymentRunner := payment.NewRunner(
		paymentService,
		func(ctx context.Context) (payment.Lock, bool, error) {
			return repository.TryPaymentReconciliationLock(ctx)
		},
		payment.DefaultInterval,
	)
	botService := bot.New(bot.Dependencies{
		Client:        messenger,
		Repository:    repository,
		Subscriptions: subscriptions,
		DeBox:         deboxClient,
		Chain:         walletReadClient,
		Catalog:       catalog,
		Settings: bot.Settings{
			PublicAppURL:             cfg.PublicAppURL,
			BotUserID:                cfg.DeBoxBotUserID,
			DefaultChainKey:          cfg.ChainKey,
			SubscriptionTokenAddress: cfg.SubscriptionTokenAddress,
			PrivateStartMaintenance:  privateStartMaintenance,
		},
	})
	tryBotLock := func(ctx context.Context) (bot.Lock, bool, error) {
		return repository.TryBotPollingLock(ctx)
	}
	botRunner := bot.NewRunner(
		botService,
		messenger,
		cfg.ReceiveMode,
		tryBotLock,
	)

	monitorRunner := monitor.NewRunner(
		monitorExecutor,
		tryMonitorLock,
		monitor.DefaultInterval,
	)
	summaryExecutor := summary.New(summary.Dependencies{
		Repository:    repository,
		Notifications: messenger,
		PublicAppURL:  cfg.PublicAppURL,
		TryLock: func(ctx context.Context, subscriptionID int64) (summary.Lock, bool, error) {
			return repository.TryScheduledSummaryLock(ctx, subscriptionID)
		},
	})
	notificationDetails := notificationdetail.New(repository, notificationdetail.Settings{
		PublicAppURL: cfg.PublicAppURL,
	})
	return dependencies{
		httpapi: httpapi.Dependencies{
			Auth:                 auth.New(repository, deboxClient),
			Subscriptions:        subscriptions,
			Chain:                walletReadClient,
			DeBox:                deboxClient,
			Management:           managementService,
			Payments:             paymentService,
			Bot:                  botService,
			MarketWebhook:        marketCluster,
			Market:               marketViewService,
			Assets:               assetCatalog,
			NotificationDetails:  notificationDetails,
			Catalog:              catalog,
			ReadyCheck:           repository.Ping,
			UserWriteMaintenance: userWriteMaintenance,
		},
		bot:     botRunner,
		monitor: monitorRunner,
		payment: paymentRunner,
		summary: summary.NewRunner(summaryExecutor, summary.DefaultInterval),
		market:  marketcollector.NewRunner(marketCluster.Services()...),
		rules:   marketrules.NewRunner(marketRuleService),
		holders: holderRunner,
	}, closeDependencies, nil
}
