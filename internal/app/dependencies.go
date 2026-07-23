package app

import (
	"context"
	"fmt"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/auth"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/config"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/debox"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/httpapi"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/subscription"
)

func buildDependencies(
	ctx context.Context,
	cfg config.Config,
) (httpapi.Dependencies, func(), error) {
	repository, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return httpapi.Dependencies{}, func() {}, fmt.Errorf("open data store: %w", err)
	}
	closeDependencies := repository.Close
	if err := repository.Migrate(ctx); err != nil {
		closeDependencies()
		return httpapi.Dependencies{}, func() {}, fmt.Errorf("migrate data store: %w", err)
	}

	catalog, err := plans.NewCatalog(
		cfg.SubscriptionPrice,
		cfg.SubscriptionDays,
		cfg.SubscriptionTokenSymbol,
	)
	if err != nil {
		closeDependencies()
		return httpapi.Dependencies{}, func() {}, fmt.Errorf("create plan catalog: %w", err)
	}
	deboxClient, err := debox.NewOpenAPIClient(
		cfg.DeBoxBotAPIKey,
		cfg.DeBoxOpenAPIBase,
		nil,
	)
	if err != nil {
		closeDependencies()
		return httpapi.Dependencies{}, func() {}, fmt.Errorf("create DeBox client: %w", err)
	}
	chainClient, err := chain.NewClient(cfg.NoditAPIKey, cfg.NoditBaseURL)
	if err != nil {
		closeDependencies()
		return httpapi.Dependencies{}, func() {}, fmt.Errorf("create Nodit client: %w", err)
	}

	return httpapi.Dependencies{
		Auth:          auth.New(repository, deboxClient),
		Subscriptions: subscription.New(repository, catalog, cfg.ComplimentaryWalletAddresses),
		Chain:         chainClient,
		DeBox:         deboxClient,
		Catalog:       catalog,
	}, closeDependencies, nil
}
