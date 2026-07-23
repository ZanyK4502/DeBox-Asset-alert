package app

import (
	"context"
	"fmt"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/auth"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/bot"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/config"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/debox"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/httpapi"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/management"
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
	chainClient, err := chain.NewClient(cfg.NoditAPIKey, cfg.NoditBaseURL)
	if err != nil {
		closeDependencies()
		return dependencies{}, func() {}, fmt.Errorf("create Nodit client: %w", err)
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
	subscriptions := subscription.New(repository, catalog, cfg.ComplimentaryWalletAddresses)
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
			Catalog:       catalog,
		},
		bot:     botRunner,
		monitor: monitorRunner,
		payment: paymentRunner,
		summary: summary.NewRunner(summaryExecutor, summary.DefaultInterval),
	}, closeDependencies, nil
}
