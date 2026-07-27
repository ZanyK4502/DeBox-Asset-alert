package marketcollector

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestClusterCoversAllSupportedChains(t *testing.T) {
	var services []*Service
	for index, profile := range chain.SupportedChains() {
		services = append(services, New(Dependencies{}, Settings{
			Enabled:           true,
			ChainKey:          profile.Key,
			ChainID:           profile.ChainID,
			ConfirmationDepth: int64(index + 10),
		}))
	}
	cluster, err := NewCluster(services, "bsc")
	if err != nil {
		t.Fatalf("NewCluster() error = %v", err)
	}
	if len(cluster.Services()) != 6 {
		t.Fatalf("cluster service count = %d", len(cluster.Services()))
	}
	seenDepths := make(map[int64]struct{}, 6)
	for _, service := range cluster.Services() {
		seenDepths[service.settings.ConfirmationDepth] = struct{}{}
	}
	if len(seenDepths) != 6 {
		t.Fatalf("confirmation depths were not independently retained: %#v", seenDepths)
	}
}

func TestClusterRoutesAndDeduplicatesWebhookPerChain(t *testing.T) {
	now := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
	repository := &fakeRepository{}
	keys := map[string]string{
		"bsc:transfer":  "bsc-secret",
		"base:transfer": "base-secret",
	}
	bsc := New(Dependencies{
		Repository: repository,
		Chain:      &fakeChain{},
		Market:     fakeMarket{},
		Now:        func() time.Time { return now },
	}, Settings{
		Enabled:            true,
		ChainKey:           "bsc",
		ChainID:            56,
		WebhookSigningKeys: keys,
	})
	base := New(Dependencies{
		Repository: repository,
		Chain:      &fakeChain{},
		Market:     fakeMarket{},
		Now:        func() time.Time { return now },
	}, Settings{
		Enabled:            true,
		ChainKey:           "base",
		ChainID:            8453,
		WebhookSigningKeys: keys,
	})
	cluster, err := NewCluster([]*Service{bsc, base}, "bsc")
	if err != nil {
		t.Fatalf("NewCluster() error = %v", err)
	}
	body := []byte(`{"transactionHash":"` + testHashA + `"}`)

	bscResult, err := cluster.AcceptWebhookForChain(
		context.Background(),
		"",
		"transfer",
		map[string][]string{
			"X-Signature":         {signWebhook(body, "bsc-secret")},
			"X-Nodit-Delivery-ID": {"same-provider-id"},
		},
		body,
	)
	if err != nil {
		t.Fatalf("BNB webhook error = %v", err)
	}
	baseResult, err := cluster.AcceptWebhookForChain(
		context.Background(),
		"base",
		"transfer",
		map[string][]string{
			"X-Signature":         {signWebhook(body, "base-secret")},
			"X-Nodit-Delivery-ID": {"same-provider-id"},
		},
		body,
	)
	if err != nil {
		t.Fatalf("Base webhook error = %v", err)
	}
	if bscResult.InboxID == baseResult.InboxID ||
		len(repository.inboxByKey) != 2 {
		t.Fatalf(
			"cross-chain deliveries collided: bsc=%+v base=%+v inbox=%d",
			bscResult,
			baseResult,
			len(repository.inboxByKey),
		)
	}
	if repository.lastInbox.ChainKey != "base" ||
		repository.lastInbox.ChainID != 8453 {
		t.Fatalf("last routed inbox = %#v", repository.lastInbox)
	}
}

func TestClusterRejectsUnsupportedOrDisabledChain(t *testing.T) {
	cluster, err := NewCluster(nil, "bsc")
	if err != nil {
		t.Fatalf("NewCluster() error = %v", err)
	}
	_, err = cluster.AcceptWebhookForChain(
		context.Background(),
		"polygon",
		"transfer",
		nil,
		[]byte(`{}`),
	)
	if !errors.Is(err, ErrCollectorDisabled) {
		t.Fatalf("empty cluster error = %v", err)
	}
}

func TestNonBNBWebhookRequiresChainScopedSigningKey(t *testing.T) {
	service := New(Dependencies{
		Repository: &fakeRepository{},
		Chain:      &fakeChain{},
		Market:     fakeMarket{},
	}, Settings{
		Enabled:  true,
		ChainKey: "base",
		ChainID:  8453,
		WebhookSigningKeys: map[string]string{
			"transfer": "legacy-bsc-key",
		},
	})
	body := []byte(`{"transactionHash":"` + testHashA + `"}`)
	_, err := service.AcceptWebhook(
		context.Background(),
		"transfer",
		map[string][]string{
			"X-Signature": {signWebhook(body, "legacy-bsc-key")},
		},
		body,
	)
	if !errors.Is(err, ErrWebhookUnavailable) {
		t.Fatalf("Base accepted an unscoped BNB signing key: %v", err)
	}
}

type testMarketLock struct{}

func (testMarketLock) Unlock(context.Context) error { return nil }

func TestTaskLocksAreScopedByChain(t *testing.T) {
	lockName := ""
	service := New(Dependencies{
		Repository: &fakeRepository{},
		Chain:      &fakeChain{},
		Market:     fakeMarket{},
		TryLock: func(_ context.Context, task string) (Lock, bool, error) {
			lockName = task
			return testMarketLock{}, true, nil
		},
	}, Settings{Enabled: true, ChainKey: "base", ChainID: 8453})

	if err := service.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if lockName != "market:base:cleanup" {
		t.Fatalf("task lock = %q", lockName)
	}
}

func TestWebhookRepairPersistsChainScopedCallback(t *testing.T) {
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	repository := &fakeRepository{}
	client := &fakeChain{}
	service := New(Dependencies{
		Repository: repository,
		Chain:      client,
		Market:     fakeMarket{},
		Now:        func() time.Time { return now },
	}, Settings{
		Enabled:      true,
		ChainKey:     "base",
		ChainID:      8453,
		PublicAppURL: "https://alerts.example",
	})
	externalID := "base-transfer"
	subscription := store.NoditWebhookSubscription{
		ID:              4,
		Provider:        "nodit",
		ExternalID:      &externalID,
		ChainKey:        "base",
		ChainID:         8453,
		EventCategory:   "transfer",
		CallbackURLHash: "old",
		Status:          "active",
	}

	if err := service.repairWebhook(
		context.Background(),
		subscription,
		false,
		true,
		nil,
	); err != nil {
		t.Fatalf("repairWebhook() error = %v", err)
	}
	wantURL := "https://alerts.example/api/market/webhook/base/transfer"
	if client.lastWebhookUpdate.Notification == nil ||
		client.lastWebhookUpdate.Notification.WebhookURL != wantURL {
		t.Fatalf("repaired callback = %#v", client.lastWebhookUpdate.Notification)
	}
	if repository.lastWebhookUpsert == nil ||
		repository.lastWebhookUpsert.CallbackURLHash != callbackURLHash(wantURL) ||
		repository.lastWebhookUpsert.LastSyncedAt == nil ||
		!repository.lastWebhookUpsert.LastSyncedAt.Equal(now) {
		t.Fatalf("persisted callback repair = %#v", repository.lastWebhookUpsert)
	}
}
