package marketcollector

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestFourMemeProgressUsesRemainingOffers(t *testing.T) {
	total := "1000"
	progress := fourMemeProgress("250", []store.MarketProject{{
		TotalSupplyRaw: &total,
	}})
	if progress != "75" {
		t.Fatalf("progress = %q, want 75", progress)
	}
}

func TestPersistFourMemeQuoteKeepsPoolKeyWithoutFakeAddress(t *testing.T) {
	repository := &fakeRepository{}
	service := New(Dependencies{
		Repository: repository,
		Chain:      &fakeChain{},
		Market:     fakeMarket{},
	}, Settings{ChainKey: "bsc", ChainID: 56})
	tokenAddress := "0x1111111111111111111111111111111111111111"
	quoteAddress := "0x2222222222222222222222222222222222222222"
	err := service.persistDiscoveredPair(
		context.Background(),
		store.MarketProject{
			DeBoxUserID: "user", ChainKey: "bsc", ChainID: 56,
			TokenAddress: tokenAddress, TokenSymbol: "MEME", TokenDecimals: 18,
		},
		marketdata.Pair{
			ChainID: "bsc", DexID: "fourmeme",
			PairAddress: tokenAddress + ":4meme",
			BaseToken:   marketdata.Token{Address: tokenAddress, Symbol: "MEME"},
			QuoteToken:  marketdata.Token{Address: quoteAddress, Symbol: "WBNB"},
		},
		map[string][2]string{},
	)
	if err != nil {
		t.Fatalf("persist Four.meme quote: %v", err)
	}
	if repository.lastPool.PoolKey != tokenAddress+":4meme" ||
		repository.lastPool.PoolAddress != nil ||
		repository.lastPool.Protocol != "fourmeme" {
		t.Fatalf("persisted Four.meme pool = %#v", repository.lastPool)
	}
}

const (
	testHashA = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testHashB = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testHashC = "0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

type fakeRepository struct {
	inboxByKey        map[string]store.WebhookInboxMessage
	lastInbox         store.CreateWebhookInboxParams
	claims            []store.WebhookInboxMessage
	failedDead        bool
	cursor            *store.MarketChainCursor
	candidates        []store.MarketScannedBlock
	reorgAt           int64
	lastPool          store.UpsertMarketPoolParams
	activeProjects    []store.MarketProject
	subscriptions     []store.NoditWebhookSubscription
	lastWebhookUpsert *store.UpsertNoditWebhookSubscriptionParams
	lastClaimChain    int64
	lastRecoveryChain int64
}

func (repository *fakeRepository) ListActiveMarketProjectsForCollection(context.Context, int64, int) ([]store.MarketProject, error) {
	return repository.activeProjects, nil
}
func (repository *fakeRepository) ListMarketCollectionTargets(context.Context, int64) ([]store.MarketCollectionTarget, error) {
	return nil, nil
}
func (repository *fakeRepository) UpsertMarketPool(_ context.Context, params store.UpsertMarketPoolParams) (store.MarketPool, error) {
	repository.lastPool = params
	return store.MarketPool{ID: 7}, nil
}
func (repository *fakeRepository) EnsureMarketProjectPool(context.Context, store.EnsureMarketProjectPoolParams) (store.MarketProjectPool, error) {
	return store.MarketProjectPool{}, nil
}
func (repository *fakeRepository) CreateMarketSnapshot(context.Context, store.CreateMarketSnapshotParams) (store.MarketSnapshot, error) {
	return store.MarketSnapshot{}, nil
}
func (repository *fakeRepository) LatestMarketSnapshot(context.Context, int64, string, *int64) (*store.MarketSnapshot, error) {
	return nil, nil
}
func (repository *fakeRepository) CreateWebhookInboxMessage(_ context.Context, params store.CreateWebhookInboxParams) (store.WebhookInboxMessage, bool, error) {
	repository.lastInbox = params
	if repository.inboxByKey == nil {
		repository.inboxByKey = make(map[string]store.WebhookInboxMessage)
	}
	if existing, exists := repository.inboxByKey[params.DedupeKey]; exists {
		return existing, false, nil
	}
	message := store.WebhookInboxMessage{
		ID:             int64(len(repository.inboxByKey) + 1),
		ChainKey:       params.ChainKey,
		ChainID:        params.ChainID,
		DedupeKey:      params.DedupeKey,
		SignatureValid: boolInt32(params.SignatureValid),
		Payload:        params.Payload,
	}
	repository.inboxByKey[params.DedupeKey] = message
	return message, true, nil
}
func (repository *fakeRepository) ClaimWebhookInboxMessages(_ context.Context, chainID int64, _ int) ([]store.WebhookInboxMessage, error) {
	repository.lastClaimChain = chainID
	return repository.claims, nil
}
func (repository *fakeRepository) MarkWebhookInboxProcessed(context.Context, int64) (store.WebhookInboxMessage, error) {
	return store.WebhookInboxMessage{}, nil
}
func (repository *fakeRepository) MarkWebhookInboxFailed(_ context.Context, _ int64, _ string, _ time.Time, dead bool) (store.WebhookInboxMessage, error) {
	repository.failedDead = dead
	return store.WebhookInboxMessage{}, nil
}
func (repository *fakeRepository) RecoverStaleWebhookInbox(_ context.Context, chainID int64, _ time.Time, _ int) (int64, error) {
	repository.lastRecoveryChain = chainID
	return 0, nil
}
func (repository *fakeRepository) GetNoditWebhookSubscriptionByCategory(_ context.Context, provider string, chainID int64, category string) (*store.NoditWebhookSubscription, error) {
	for index := range repository.subscriptions {
		value := &repository.subscriptions[index]
		if value.Provider == provider && value.ChainID == chainID &&
			value.EventCategory == category {
			return value, nil
		}
	}
	return nil, nil
}
func (repository *fakeRepository) ListNoditWebhookSubscriptions(context.Context, *int64) ([]store.NoditWebhookSubscription, error) {
	return repository.subscriptions, nil
}
func (repository *fakeRepository) UpsertNoditWebhookSubscription(_ context.Context, params store.UpsertNoditWebhookSubscriptionParams) (store.NoditWebhookSubscription, error) {
	copy := params
	repository.lastWebhookUpsert = &copy
	return store.NoditWebhookSubscription{
		ID:            1,
		ExternalID:    params.ExternalID,
		ChainKey:      params.ChainKey,
		ChainID:       params.ChainID,
		EventCategory: params.EventCategory,
		Configuration: params.Configuration,
	}, nil
}
func (repository *fakeRepository) UpdateNoditWebhookSubscriptionCheck(context.Context, int64, string, string, time.Time) (store.NoditWebhookSubscription, error) {
	return store.NoditWebhookSubscription{}, nil
}
func (repository *fakeRepository) GetMarketChainCursor(context.Context, int64, string) (*store.MarketChainCursor, error) {
	return repository.cursor, nil
}
func (repository *fakeRepository) AdvanceMarketChainCursor(_ context.Context, params store.AdvanceMarketChainCursorParams) (store.MarketChainCursor, error) {
	value := store.MarketChainCursor{
		ChainKey:        params.ChainKey,
		ChainID:         params.ChainID,
		CursorKey:       params.CursorKey,
		NextBlockNumber: params.NextBlockNumber,
		SafeBlockNumber: params.SafeBlockNumber,
		LastBlockHash:   params.LastBlockHash,
	}
	repository.cursor = &value
	return value, nil
}
func (repository *fakeRepository) UpsertMarketScannedBlock(context.Context, store.UpsertMarketScannedBlockParams) (store.MarketScannedBlock, error) {
	return store.MarketScannedBlock{}, nil
}
func (repository *fakeRepository) ListCanonicalMarketScannedBlocks(context.Context, int64, string, int64, int) ([]store.MarketScannedBlock, error) {
	return repository.candidates, nil
}
func (repository *fakeRepository) ReconcileMarketReorg(_ context.Context, _ int64, _ string, ancestor int64, hash string, _ string) (store.MarketReorgResult, error) {
	repository.reorgAt = ancestor
	return store.MarketReorgResult{Cursor: store.MarketChainCursor{
		ChainKey:        "bsc",
		ChainID:         56,
		CursorKey:       DefaultCursorKey,
		NextBlockNumber: ancestor + 1,
		LastBlockHash:   &hash,
	}}, nil
}
func (repository *fakeRepository) MarkMarketBlockConfirmed(context.Context, int64, int64) (int64, error) {
	return 0, nil
}
func (repository *fakeRepository) ListUnconfirmedMarketEventBlocks(context.Context, int64, int64, int64) ([]int64, error) {
	return nil, nil
}
func (repository *fakeRepository) MarkMarketBlockReorged(context.Context, int64, int64, string) (int64, error) {
	return 0, nil
}
func (repository *fakeRepository) CreateMarketEvent(context.Context, store.CreateMarketEventParams) (store.MarketEvent, bool, error) {
	return store.MarketEvent{}, true, nil
}

func (repository *fakeRepository) UpdateMarketProjectsFourMemeStatus(
	context.Context,
	int64,
	string,
	string,
) (int64, error) {
	return 1, nil
}
func (repository *fakeRepository) RecordMarketProviderHealth(context.Context, store.RecordMarketProviderHealthParams) (store.MarketProviderHealth, error) {
	return store.MarketProviderHealth{}, nil
}
func (repository *fakeRepository) UpsertMarketProviderUsage(context.Context, store.UpsertMarketProviderUsageParams) (store.MarketProviderUsage, error) {
	return store.MarketProviderUsage{}, nil
}
func (repository *fakeRepository) AddMarketProviderUsage(context.Context, store.AddMarketProviderUsageParams) (store.MarketProviderUsage, error) {
	return store.MarketProviderUsage{}, nil
}
func (repository *fakeRepository) CleanupMarketCollectionData(context.Context, time.Time, time.Time, time.Time) (int64, error) {
	return 0, nil
}

type fakeChain struct {
	blocks            map[uint64]map[string]any
	lastWebhookID     string
	lastWebhookUpdate chain.WebhookUpdateRequest
}

func (client *fakeChain) LatestBlockNumber(context.Context, string, string) (uint64, error) {
	return 0, nil
}
func (client *fakeChain) Logs(context.Context, string, string, chain.LogFilter) ([]chain.RPCLog, error) {
	return nil, nil
}
func (client *fakeChain) BlockByNumber(_ context.Context, number uint64, _ bool, _, _ string) (map[string]any, error) {
	block, exists := client.blocks[number]
	if !exists {
		return nil, fmt.Errorf("missing block %d", number)
	}
	return block, nil
}
func (client *fakeChain) RPCTransactionByHash(context.Context, string, string, string) (map[string]any, error) {
	return nil, nil
}
func (client *fakeChain) TransactionReceipt(context.Context, string, string, string) (map[string]any, error) {
	return nil, nil
}
func (client *fakeChain) PoolTokens(context.Context, string, string, string) (string, string, error) {
	return "", "", nil
}
func (client *fakeChain) PoolFactory(context.Context, string, string, string) (string, error) {
	return marketparse.BSCPancakeV3Factory, nil
}
func (client *fakeChain) ListWebhooks(context.Context, string, string, chain.WebhookListOptions) (chain.WebhookList, error) {
	return chain.WebhookList{}, nil
}
func (client *fakeChain) UpdateWebhook(_ context.Context, _, _, id string, update chain.WebhookUpdateRequest) error {
	client.lastWebhookID = id
	client.lastWebhookUpdate = update
	return nil
}

type fakeMarket struct{}

func (fakeMarket) DiscoverPools(context.Context, string, string) ([]marketdata.Pair, error) {
	return nil, nil
}
func (fakeMarket) PairsByTokens(context.Context, string, []string) ([]marketdata.Pair, error) {
	return nil, nil
}
func (fakeMarket) PairsByAddresses(context.Context, string, []string) ([]marketdata.Pair, error) {
	return nil, nil
}

func TestAcceptWebhookPersistsBeforeDeduplication(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	repository := &fakeRepository{}
	service := New(Dependencies{
		Repository: repository,
		Chain:      &fakeChain{},
		Market:     fakeMarket{},
		Now:        func() time.Time { return now },
	}, Settings{
		Enabled:           true,
		WebhookSigningKey: "secret",
	})
	body := []byte(`{"event":{"transactionHash":"` + testHashA + `"}}`)
	signature := signWebhook(body, "secret")
	headers := map[string][]string{
		"X-Signature":       {signature},
		"X-Nodit-Timestamp": {fmt.Sprint(now.Unix())},
	}

	first, err := service.AcceptWebhook(context.Background(), "v2-v3", headers, body)
	if err != nil {
		t.Fatalf("AcceptWebhook() error = %v", err)
	}
	second, err := service.AcceptWebhook(context.Background(), "v2-v3", headers, body)
	if err != nil {
		t.Fatalf("duplicate AcceptWebhook() error = %v", err)
	}
	if !first.Created || !second.Duplicate || first.InboxID != second.InboxID {
		t.Fatalf("unexpected dedupe results: first=%+v second=%+v", first, second)
	}
	if !repository.lastInbox.SignatureValid || string(repository.lastInbox.Payload) != string(body) {
		t.Fatal("verified raw webhook was not persisted")
	}
}

func TestSyncWebhookSubscriptionsMergesActiveProjectTokens(t *testing.T) {
	externalID := "8658"
	now := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	repository := &fakeRepository{
		activeProjects: []store.MarketProject{
			{TokenAddress: "0x2222222222222222222222222222222222222222"},
			{TokenAddress: "0x1111111111111111111111111111111111111111"},
			{TokenAddress: "0x2222222222222222222222222222222222222222"},
		},
		subscriptions: []store.NoditWebhookSubscription{{
			ID:              1,
			Provider:        "nodit",
			ExternalID:      &externalID,
			ChainKey:        "bsc",
			ChainID:         56,
			EventCategory:   transferWebhookCategory,
			CallbackURLHash: "hash",
			SecretReference: "NODIT_WEBHOOK_SIGNING_KEYS_JSON:transfer",
			Status:          "active",
			Configuration:   json.RawMessage(`{"eventType":"TOKEN_TRANSFER","condition":{"tokens":[{"contractAddress":"0x3333333333333333333333333333333333333333"}]}}`),
		}},
	}
	client := &fakeChain{}
	service := New(Dependencies{
		Repository: repository,
		Chain:      client,
		Market:     fakeMarket{},
		Now:        func() time.Time { return now },
	}, Settings{
		Enabled:           true,
		WebhookAutoRepair: true,
		ChainKey:          "bsc",
		ChainID:           56,
		WebhookSigningKeys: map[string]string{
			"transfer": "legacy-bsc-key",
		},
	})

	if err := service.SyncWebhookSubscriptions(context.Background()); err != nil {
		t.Fatalf("SyncWebhookSubscriptions() error = %v", err)
	}
	if client.lastWebhookID != externalID {
		t.Fatalf("updated webhook = %q, want %q", client.lastWebhookID, externalID)
	}
	tokens, ok := client.lastWebhookUpdate.Condition["tokens"].([]map[string]string)
	if !ok || len(tokens) != 2 ||
		tokens[0]["contractAddress"] != "0x1111111111111111111111111111111111111111" ||
		tokens[1]["contractAddress"] != "0x2222222222222222222222222222222222222222" {
		t.Fatalf("synced token condition = %#v", client.lastWebhookUpdate.Condition)
	}
	if repository.lastWebhookUpsert == nil ||
		repository.lastWebhookUpsert.LastSyncedAt == nil ||
		!repository.lastWebhookUpsert.LastSyncedAt.Equal(now) {
		t.Fatalf("webhook registration was not synchronized: %#v", repository.lastWebhookUpsert)
	}
}

func TestAcceptWebhookRejectsExpiredTimestampButAuditsDelivery(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	repository := &fakeRepository{}
	service := New(Dependencies{
		Repository: repository,
		Chain:      &fakeChain{},
		Market:     fakeMarket{},
		Now:        func() time.Time { return now },
	}, Settings{Enabled: true, WebhookSigningKey: "secret"})
	body := []byte(`{"transactionHash":"` + testHashB + `"}`)
	_, err := service.AcceptWebhook(context.Background(), "transfer", map[string][]string{
		"X-Signature":       {signWebhook(body, "secret")},
		"X-Nodit-Timestamp": {fmt.Sprint(now.Add(-10 * time.Minute).Unix())},
	}, body)
	if !errors.Is(err, ErrExpiredWebhook) {
		t.Fatalf("AcceptWebhook() error = %v, want expired", err)
	}
	if repository.lastInbox.SignatureValid {
		t.Fatal("expired delivery was queued as processable")
	}
}

func TestProcessInboxPermanentlyRejectsInvalidSignature(t *testing.T) {
	repository := &fakeRepository{claims: []store.WebhookInboxMessage{{
		ID:             9,
		ChainID:        DefaultChainID,
		SignatureValid: 0,
		Attempts:       1,
	}}}
	service := New(Dependencies{
		Repository: repository,
		Chain:      &fakeChain{},
		Market:     fakeMarket{},
	}, Settings{Enabled: true})
	if err := service.ProcessInbox(context.Background()); err != nil {
		t.Fatalf("ProcessInbox() error = %v", err)
	}
	if !repository.failedDead {
		t.Fatal("invalid signature was scheduled for retry")
	}
	if repository.lastClaimChain != DefaultChainID ||
		repository.lastRecoveryChain != DefaultChainID {
		t.Fatalf(
			"inbox recovery crossed chains: claim=%d recovery=%d",
			repository.lastClaimChain,
			repository.lastRecoveryChain,
		)
	}
}

func TestVerifyCursorContinuityRewindsToCanonicalAncestor(t *testing.T) {
	repository := &fakeRepository{candidates: []store.MarketScannedBlock{{
		BlockNumber: 99,
		BlockHash:   testHashB,
	}}}
	client := &fakeChain{blocks: map[uint64]map[string]any{
		100: rpcBlock(100, testHashC, testHashB),
		99:  rpcBlock(99, testHashB, testHashA),
	}}
	service := New(Dependencies{
		Repository: repository,
		Chain:      client,
		Market:     fakeMarket{},
	}, Settings{Enabled: true})
	oldHash := testHashA
	cursor, err := service.verifyCursorContinuity(context.Background(), store.MarketChainCursor{
		ChainKey:        "bsc",
		ChainID:         56,
		CursorKey:       DefaultCursorKey,
		NextBlockNumber: 101,
		LastBlockHash:   &oldHash,
	})
	if err != nil {
		t.Fatalf("verifyCursorContinuity() error = %v", err)
	}
	if repository.reorgAt != 99 || cursor.NextBlockNumber != 100 {
		t.Fatalf("reorg result = ancestor %d cursor %+v", repository.reorgAt, cursor)
	}
}

func TestPairPriceForQuoteTokenUsesExactRatio(t *testing.T) {
	pair := marketdata.Pair{
		BaseToken:   marketdata.Token{Address: "base"},
		QuoteToken:  marketdata.Token{Address: "quote"},
		PriceUSD:    marketdata.Decimal("6"),
		PriceNative: marketdata.Decimal("2"),
	}
	price := pairPriceForToken(pair, "quote")
	if price == nil || *price != "3" {
		t.Fatalf("quote token price = %v, want 3", price)
	}
}

func TestExtractTransactionHashesHandlesClassicAndFlexibleShapes(t *testing.T) {
	payload := json.RawMessage(`{
		"event":{"transaction_hash":"` + testHashB + `"},
		"messages":[{"txHash":"` + testHashA + `"},{"transactionHash":"` + testHashB + `"}]
	}`)
	hashes, err := extractTransactionHashes(payload)
	if err != nil {
		t.Fatalf("extractTransactionHashes() error = %v", err)
	}
	if len(hashes) != 2 || hashes[0] != testHashA || hashes[1] != testHashB {
		t.Fatalf("hashes = %#v", hashes)
	}
}

func TestInitializedPoolKeepsOnChainTokenOrderForToken1Project(t *testing.T) {
	repository := &fakeRepository{}
	service := New(Dependencies{
		Repository: repository,
		Chain:      &fakeChain{},
		Market:     fakeMarket{},
	}, Settings{Enabled: true})
	token0 := "0x1111111111111111111111111111111111111111"
	token1 := "0x2222222222222222222222222222222222222222"
	_, err := service.ensureInitializedPool(
		context.Background(),
		marketparse.Event{
			Type:         marketparse.EventPoolInitialized,
			Protocol:     "pancakeswap",
			Version:      "v3",
			Adapter:      marketparse.AdapterV3,
			PoolKey:      "0x3333333333333333333333333333333333333333",
			TokenAddress: token1,
			QuoteAddress: token0,
			Metadata: map[string]string{
				"factory_address": marketparse.BSCPancakeV3Factory,
				"token0_address":  token0,
				"token1_address":  token1,
			},
		},
		parserContext{
			projectsByToken: map[string][]store.MarketProject{
				token1: {{ID: 1, DeBoxUserID: "user", TokenAddress: token1, TokenDecimals: 18}},
			},
		},
	)
	if err != nil {
		t.Fatalf("ensureInitializedPool() error = %v", err)
	}
	if repository.lastPool.Token0Address != token0 ||
		repository.lastPool.Token1Address != token1 {
		t.Fatalf("pool token order was reversed: %#v", repository.lastPool)
	}
}

func rpcBlock(number uint64, hash, parent string) map[string]any {
	return map[string]any{
		"number":     fmt.Sprintf("0x%x", number),
		"hash":       hash,
		"parentHash": parent,
		"timestamp":  "0x64",
	}
}

func signWebhook(body []byte, key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func boolInt32(value bool) int32 {
	if value {
		return 1
	}
	return 0
}
