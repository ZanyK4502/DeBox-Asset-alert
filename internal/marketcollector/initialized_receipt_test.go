package marketcollector

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketprotocol"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const (
	initializedToken  = "0x1111111111111111111111111111111111111111"
	initializedQuote  = "0x2222222222222222222222222222222222222222"
	initializedPool   = "0x3333333333333333333333333333333333333333"
	initializedWallet = "0x4444444444444444444444444444444444444444"
)

type initializedReceiptRepository struct {
	*fakeRepository
	ids      map[string]int64
	upserts  int
	selected bool
}

func (r *initializedReceiptRepository) EnsureMarketProjectPool(context.Context, store.EnsureMarketProjectPoolParams) (store.MarketProjectPool, error) {
	return store.MarketProjectPool{Selected: boolInt32(r.selected)}, nil
}

func (r *initializedReceiptRepository) UpsertMarketPool(_ context.Context, params store.UpsertMarketPoolParams) (store.MarketPool, error) {
	r.upserts++
	r.lastPool = params
	if r.ids == nil {
		r.ids = make(map[string]int64)
	}
	if r.ids[params.PoolKey] == 0 {
		r.ids[params.PoolKey] = int64(100 + len(r.ids))
	}
	return store.MarketPool{ID: r.ids[params.PoolKey]}, nil
}

func initializedWord(value int64) string {
	n := big.NewInt(value)
	if value < 0 {
		n.Add(n, new(big.Int).Lsh(big.NewInt(1), 256))
	}
	return fmt.Sprintf("%064x", n)
}
func initializedAddressWord(address string) string {
	return strings.Repeat("0", 24) + strings.TrimPrefix(address, "0x")
}

func initializedFixture(t *testing.T, chainKey, adapter string) (*Service, *initializedReceiptRepository, parserContext, marketparse.Receipt, []marketparse.Event) {
	t.Helper()
	var emitter marketparse.Emitter
	for _, candidate := range marketprotocol.FactoryEmitters(chainKey) {
		if candidate.Adapter == adapter {
			emitter = candidate
			break
		}
	}
	if emitter.Address == "" {
		t.Fatalf("missing fixture factory %s/%s", chainKey, adapter)
	}
	chainID := int64(56)
	if chainKey == "polygon" {
		chainID = 137
	}
	if chainKey == "base" {
		chainID = 8453
	}
	poolKey := initializedPool
	log := marketparse.Log{Address: emitter.Address, LogIndex: 1}
	first, second := "0x"+initializedAddressWord(initializedToken), "0x"+initializedAddressWord(initializedQuote)
	switch adapter {
	case marketparse.AdapterV2:
		log.Topics = []string{marketparse.V2FactoryEventTopic(), first, second}
		log.Data = "0x" + initializedAddressWord(poolKey) + initializedWord(1)
	case marketparse.AdapterV3:
		log.Topics = []string{marketparse.V3FactoryEventTopic(), first, second, "0x" + initializedWord(500)}
		log.Data = "0x" + initializedWord(10) + initializedAddressWord(poolKey)
	case marketparse.AdapterAlgebra:
		log.Topics = []string{marketparse.AlgebraFactoryEventTopic(), first, second}
		log.Data = "0x" + initializedAddressWord(poolKey)
	case marketparse.AdapterSolidly:
		log.Topics = []string{marketparse.SolidlyFactoryEventTopic(), first, second, "0x" + initializedWord(0)}
		log.Data = "0x" + initializedAddressWord(poolKey) + initializedWord(1)
	case marketparse.AdapterInfinityCL, marketparse.AdapterInfinityBin:
		poolKey = testHashC
		topic := marketparse.InfinityCLInitializeTopic()
		if adapter == marketparse.AdapterInfinityBin {
			topic = marketparse.InfinityBinInitializeTopic()
		}
		log.Topics = []string{topic, poolKey, first, second}
		log.Data = "0x" + initializedAddressWord(marketparse.NativeTokenAddress) + initializedWord(500) + initializedWord(0) + initializedWord(123)
		if adapter == marketparse.AdapterInfinityCL {
			log.Data += initializedWord(0)
		}
	}
	parser, err := marketparse.NewParser(nil, marketprotocol.FactoryEmitters(chainKey))
	if err != nil {
		t.Fatal(err)
	}
	context := parserContext{parser: parser, targetTokens: []string{initializedToken}, tokenDecimals: map[string]int{initializedToken: 6, initializedQuote: 18}, poolByID: map[int64]store.MarketCollectionTarget{}, projectsByToken: map[string][]store.MarketProject{initializedToken: {{ID: 1, DeBoxUserID: "fixture", TokenAddress: initializedToken, TokenDecimals: 6}}}}
	receipt := marketparse.Receipt{ChainID: uint64(chainID), TransactionHash: testHashA, BlockHash: testHashB, BlockNumber: 100, From: initializedWallet, Logs: []marketparse.Log{log}}
	events, err := parser.Parse(receipt, context.targetTokens)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("initial factory events=%d", len(events))
	}
	repository := &initializedReceiptRepository{fakeRepository: &fakeRepository{}, selected: true}
	service := New(Dependencies{Repository: repository, Chain: &fakeChain{}}, Settings{ChainKey: chainKey, ChainID: chainID})
	return service, repository, context, receipt, events
}

func initializedSwapLog() marketparse.Log {
	return marketparse.Log{Address: initializedPool, LogIndex: 2,
		Topics: []string{marketparse.Topic("Swap(address,uint256,uint256,uint256,uint256,address)"), "0x" + initializedAddressWord(initializedWallet), "0x" + initializedAddressWord(initializedWallet)},
		Data:   "0x" + initializedWord(0) + initializedWord(2000) + initializedWord(100) + initializedWord(0),
	}
}

func TestPrepareInitializedReceiptReparsesNewPoolSwapWithoutLeakingScope(t *testing.T) {
	service, repo, original, receipt, initial := initializedFixture(t, "bsc", marketparse.AdapterV2)
	delete(original.tokenDecimals, initializedQuote)
	receipt.Logs = append(receipt.Logs, initializedSwapLog())
	updated, events, ids, changed, err := service.prepareInitializedReceipt(context.Background(), receipt, original, initial)
	if err != nil {
		t.Fatal(err)
	}
	id := ids[marketparse.AdapterV2+":"+initializedPool]
	if !changed || id <= 0 || repo.upserts != 1 || len(updated.pools) != 1 || len(updated.poolByID) != 1 {
		t.Fatalf("scope changed=%v id=%d writes=%d", changed, id, repo.upserts)
	}
	var buy, creation int
	for _, event := range events {
		if event.Type == marketparse.EventBuy {
			buy++
			if event.PoolID != id || event.TokenAmountRaw != "100" || len(event.TradeLegs) != 1 || event.TradeLegs[0].PoolID != id {
				t.Fatalf("new pool trade=%+v", event)
			}
		}
		if event.Type == marketparse.EventPoolInitialized {
			creation++
			if event.PoolID != id {
				t.Fatal("factory event did not receive real pool ID")
			}
		}
	}
	if buy != 1 || creation != 1 {
		t.Fatalf("buy=%d initialization=%d", buy, creation)
	}
	if len(original.poolByID) != 0 || len(original.pools) != 0 {
		t.Fatal("original parser coverage mutated")
	}
	if _, exists := original.tokenDecimals[initializedQuote]; exists {
		t.Fatal("metadata cache leaked into original context")
	}
	oldEvents, err := original.parser.Parse(receipt, original.targetTokens)
	if err != nil || len(oldEvents) != 1 {
		t.Fatalf("old parser changed: %d %v", len(oldEvents), err)
	}
	updated.projectsByToken[initializedToken][0].TokenDecimals = 0
	updated.targetTokens[0] = initializedQuote
	if original.projectsByToken[initializedToken][0].TokenDecimals != 6 || original.targetTokens[0] != initializedToken {
		t.Fatal("context slices share writable storage")
	}
}

func TestPrepareInitializedReceiptRegistersSupportedAdapters(t *testing.T) {
	for _, test := range []struct{ chain, adapter string }{
		{"bsc", marketparse.AdapterV2}, {"bsc", marketparse.AdapterV3}, {"polygon", marketparse.AdapterAlgebra},
		{"base", marketparse.AdapterSolidly}, {"bsc", marketparse.AdapterInfinityCL}, {"bsc", marketparse.AdapterInfinityBin},
	} {
		t.Run(test.chain+test.adapter, func(t *testing.T) {
			service, _, original, receipt, events := initializedFixture(t, test.chain, test.adapter)
			updated, reparsed, ids, changed, err := service.prepareInitializedReceipt(context.Background(), receipt, original, events)
			if err != nil {
				t.Fatal(err)
			}
			if !changed || len(ids) != 1 || len(reparsed) != 1 || reparsed[0].PoolID <= 0 {
				t.Fatalf("expanded events=%+v ids=%v", reparsed, ids)
			}
			pool := updated.pools[0]
			if pool.Token0.Decimals != 6 || pool.Token1.Decimals != 18 {
				t.Fatal("real token decimals were not registered")
			}
			if test.adapter == marketparse.AdapterInfinityCL && pool.LogAddress != marketparse.BSCInfinityCLManager {
				t.Fatal("wrong CL log address")
			}
			if test.adapter == marketparse.AdapterInfinityBin && pool.LogAddress != marketparse.BSCInfinityBinManager {
				t.Fatal("wrong Bin log address")
			}
			if test.adapter != marketparse.AdapterInfinityCL && test.adapter != marketparse.AdapterInfinityBin && pool.LogAddress != initializedPool {
				t.Fatal("wrong pool log address")
			}
		})
	}
}

func TestPrepareInitializedReceiptDeduplicatesBothTokenFactoryEvents(t *testing.T) {
	service, repo, original, receipt, _ := initializedFixture(t, "bsc", marketparse.AdapterV2)
	original.targetTokens = append(original.targetTokens, initializedQuote)
	original.projectsByToken[initializedQuote] = []store.MarketProject{{ID: 2, DeBoxUserID: "second", TokenAddress: initializedQuote, TokenDecimals: 18}}
	receipt.Logs = append(receipt.Logs, initializedSwapLog())
	initial, err := original.parser.Parse(receipt, original.targetTokens)
	if err != nil {
		t.Fatal(err)
	}
	_, events, ids, _, err := service.prepareInitializedReceipt(context.Background(), receipt, original, initial)
	if err != nil {
		t.Fatal(err)
	}
	if repo.upserts != 1 || len(ids) != 1 || len(events) != 4 {
		t.Fatalf("writes=%d pools=%d events=%d", repo.upserts, len(ids), len(events))
	}
}

func TestPrepareInitializedReceiptFailsClosedForUnknownPrecisionAndBadSwap(t *testing.T) {
	for _, badPrecision := range []bool{true, false} {
		t.Run(fmt.Sprint(badPrecision), func(t *testing.T) {
			service, repo, original, receipt, initial := initializedFixture(t, "bsc", marketparse.AdapterV2)
			if badPrecision {
				delete(original.tokenDecimals, initializedQuote)
				service.chain = &precisionChain{fakeChain: &fakeChain{}, err: errors.New("metadata unavailable")}
			} else {
				bad := initializedSwapLog()
				bad.Data = "0x"
				receipt.Logs = append(receipt.Logs, bad)
			}
			_, events, ids, changed, err := service.prepareInitializedReceipt(context.Background(), receipt, original, initial)
			if err == nil || events != nil || ids != nil || changed {
				t.Fatal("partial receipt was accepted")
			}
			if badPrecision && repo.upserts != 0 {
				t.Fatal("unknown precision wrote a pool")
			}
			if len(original.poolByID) != 0 || len(original.pools) != 0 {
				t.Fatal("failed registration polluted old scope")
			}
		})
	}
}

func TestPrepareInitializedReceiptBoundsAndAlreadyRegisteredPool(t *testing.T) {
	service, repo, original, receipt, initial := initializedFixture(t, "bsc", marketparse.AdapterV2)
	tooMany := make([]marketparse.Event, maxInitializedReceiptPools+1)
	for index := range tooMany {
		event := initial[0]
		event.PoolKey = fmt.Sprintf("0x%040x", index+10)
		tooMany[index] = event
	}
	if _, _, _, _, err := service.prepareInitializedReceipt(context.Background(), receipt, original, tooMany); err == nil || repo.upserts != 0 {
		t.Fatal("oversized initialization was not rejected before writes")
	}
	expanded, events, _, _, err := service.prepareInitializedReceipt(context.Background(), receipt, original, initial)
	if err != nil {
		t.Fatal(err)
	}
	again, _, _, changed, err := service.prepareInitializedReceipt(context.Background(), receipt, expanded, initial)
	if err != nil || changed || len(again.pools) != 1 {
		t.Fatalf("existing registration duplicated: %v %v", changed, err)
	}
	before := repo.upserts
	_, unchanged, _, changed, err := service.prepareInitializedReceipt(context.Background(), receipt, expanded, events)
	if err != nil || changed || repo.upserts != before || len(unchanged) != 1 {
		t.Fatal("registered pool initialization was treated as a new factory")
	}
}

func TestPrepareInitializedReceiptUnselectedPoolDoesNotRequireRangeRetry(t *testing.T) {
	service, repo, original, receipt, initial := initializedFixture(t, "bsc", marketparse.AdapterV2)
	repo.selected = false
	receipt.Logs = append(receipt.Logs, initializedSwapLog())
	updated, events, _, reload, err := service.prepareInitializedReceipt(context.Background(), receipt, original, initial)
	if err != nil {
		t.Fatal(err)
	}
	if reload || len(updated.pools) != 1 || len(updated.poolByID) != 1 {
		t.Fatalf("unselected pool reload=%v coverage=%d", reload, len(updated.poolByID))
	}
	var trades int
	for _, event := range events {
		if event.Type == marketparse.EventBuy {
			trades++
		}
	}
	if trades != 1 {
		t.Fatal("unselected discovery lost the current receipt's swap")
	}
}
