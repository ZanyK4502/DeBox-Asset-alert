package app

import (
	"context"
	"fmt"
	"sync/atomic"

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
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/payment"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/subscription"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/summary"
)

type dependencies struct {
	httpapi httpapi.Dependencies
	bot     *bot.Runner
	monitor *monitor.Runner
	payment *payment.Runner
	summary *summary.Runner
	market  *marketcollector.Runner
	rules   *marketrules.Runner
}

func buildDependencies(
	ctx context.Context,
	cfg config.Config,
) (dependencies, func(), error) {
	repository, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return dependencies{}, func() {}, fmt.Errorf("open data store: %w", err)
	}
	closeDependencies := repository.Close
	if err := repository.Migrate(ctx); err != nil {
		closeDependencies()
		return dependencies{}, func() {}, fmt.Errorf("migrate data store: %w", err)
	}

	catalog, err := plans.NewCatalog(
		cfg.SubscriptionPrice,
		cfg.SubscriptionDays,
		cfg.SubscriptionTokenSymbol,
	)
	if err != nil {
		closeDependencies()
		return dependencies{}, func() {}, fmt.Errorf("create plan catalog: %w", err)
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
	marketService := marketcollector.New(
		marketcollector.Dependencies{
			Repository:       repository,
			Chain:            chainClient,
			Market:           marketDataClient,
			EstimatedCUMilli: noditCUMeter,
			TryLock: func(
				ctx context.Context,
				task string,
			) (marketcollector.Lock, bool, error) {
				lock, acquired, lockErr := repository.TryMarketTaskLock(ctx, task)
				if lockErr != nil || !acquired {
					return nil, acquired, lockErr
				}
				return lock, true, nil
			},
		},
		marketcollector.Settings{
			Enabled:            cfg.MarketCollectorEnabled,
			ChainKey:           cfg.ChainKey,
			ChainID:            marketcollector.DefaultChainID,
			WebhookSigningKey:  cfg.NoditWebhookSigningKey,
			WebhookSigningKeys: cfg.NoditWebhookSigningKeys,
			WebhookAutoRepair:  cfg.MarketWebhookAutoRepair,
			PublicAppURL:       cfg.PublicAppURL,
			ConfirmationDepth:  cfg.MarketConfirmationDepth,
			ScanBatchSize:      cfg.MarketScanBatchSize,
			InitialLookback:    cfg.MarketInitialLookback,
			ReorgLookback:      cfg.MarketReorgLookback,
			InboxInterval:      cfg.MarketInboxInterval,
			ScanInterval:       cfg.MarketScanInterval,
			SnapshotInterval:   cfg.MarketSnapshotInterval,
			DiscoveryInterval:  cfg.MarketDiscoveryInterval,
			HealthInterval:     cfg.MarketHealthInterval,
			CleanupInterval:    cfg.MarketCleanupInterval,
			MonthlyCULimit:     cfg.NoditMonthlyCULimit,
		},
	)
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
	subscriptions := subscription.New(repository, catalog, cfg.ComplimentaryWalletAddresses)
	marketRuleService := marketrules.New(
		marketrules.Dependencies{
			Repository:    repository,
			Notifications: messenger,
			Holders:       chainClient,
			TryLock: func(
				ctx context.Context,
				task string,
			) (marketrules.Lock, bool, error) {
				lock, acquired, lockErr := repository.TryMarketTaskLock(ctx, task)
				if lockErr != nil || !acquired {
					return nil, acquired, lockErr
				}
				return lock, true, nil
			},
		},
		marketrules.Settings{
			Enabled:               cfg.MarketRuleEngineEnabled,
			Interval:              cfg.MarketRuleInterval,
			HolderRefreshInterval: cfg.MarketHolderRefreshInterval,
		},
	)
	tryMonitorLock := func(ctx context.Context) (monitor.Lock, bool, error) {
		return repository.TryMonitorExecutionLock(ctx)
	}
	monitorExecutor := monitor.New(monitor.Dependencies{
		Repository:       repository,
		Chain:            chainClient,
		Notifications:    messenger,
		Catalog:          catalog,
		TryExecutionLock: tryMonitorLock,
		DefaultChainKey:  cfg.ChainKey,
		PublicAppURL:     cfg.PublicAppURL,
	})
	managementService := management.New(management.Dependencies{
		Repository:      repository,
		Entitlements:    subscriptions,
		Chain:           chainClient,
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
		Chain:         chainClient,
		Catalog:       catalog,
		Settings: bot.Settings{
			PublicAppURL:             cfg.PublicAppURL,
			BotUserID:                cfg.DeBoxBotUserID,
			DefaultChainKey:          cfg.ChainKey,
			SubscriptionTokenAddress: cfg.SubscriptionTokenAddress,
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
		TryLock: func(ctx context.Context, subscriptionID int64) (summary.Lock, bool, error) {
			return repository.TryScheduledSummaryLock(ctx, subscriptionID)
		},
	})
	return dependencies{
		httpapi: httpapi.Dependencies{
			Auth:          auth.New(repository, deboxClient),
			Subscriptions: subscriptions,
			Chain:         chainClient,
			DeBox:         deboxClient,
			Management:    managementService,
			Payments:      paymentService,
			Bot:           botService,
			MarketWebhook: marketService,
			Market:        marketViewService,
			Catalog:       catalog,
			ReadyCheck:    repository.Ping,
		},
		bot:     botRunner,
		monitor: monitorRunner,
		payment: paymentRunner,
		summary: summary.NewRunner(summaryExecutor, summary.DefaultInterval),
		market:  marketcollector.NewRunner(marketService),
		rules:   marketrules.NewRunner(marketRuleService),
	}, closeDependencies, nil
}
