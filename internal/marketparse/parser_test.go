package marketparse

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
)

const (
	testToken  = "0x1111111111111111111111111111111111111111"
	testQuote  = "0x2222222222222222222222222222222222222222"
	testMiddle = "0x3333333333333333333333333333333333333333"
	testPoolA  = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testPoolB  = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testWallet = "0x4444444444444444444444444444444444444444"
	testRouter = "0x5555555555555555555555555555555555555555"
	testHash   = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testBlock  = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testPoolID = "0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func TestCanonicalEventTopics(t *testing.T) {
	t.Parallel()
	if topicV2Swap != "0xd78ad95fa46c994b6551d0da85fc275fe613ce37657fb8d5e3d130840159d822" {
		t.Fatalf("V2 Swap topic = %s", topicV2Swap)
	}
	if topicV3Swap != "0xc42079f94a6350d7e6235f29174924f928cc2ac818eb64fed8004e115fbcca67" {
		t.Fatalf("V3 Swap topic = %s", topicV3Swap)
	}
	if topicPancakeV3Swap != "0x19b47279256b2a23a1665c810c8d55a1758940ee09377d4f8d26497a3577dc83" {
		t.Fatalf("PancakeSwap V3 Swap topic = %s", topicPancakeV3Swap)
	}
	if topicInfinityCLSwap == topicInfinityBinSwap {
		t.Fatal("Infinity CL and Bin topics must differ")
	}
	if topicInfinityCLSwap != "0x04206ad2b7c0f463bff3dd4f33c5735b0f2957a351e4f79763a4fa9e775dd237" {
		t.Fatalf("Infinity CL Swap topic = %s", topicInfinityCLSwap)
	}
	if topicFourPurchaseV1 == topicFourPurchaseV2 {
		t.Fatal("Four.meme V1 and V2 trade topics must differ")
	}
	if topicFourPurchaseV2 != "0x7db52723a3b2cdd6164364b3b766e65e540d7be48ffa89582956d8eaebe62942" {
		t.Fatalf("Four.meme V2 TokenPurchase topic = %s", topicFourPurchaseV2)
	}
	if topicFourSaleV2 != "0x0a5575b3648bae2210cee56bf33254cc1ddfbc7bf637c0af2ac18b14fb1bae19" {
		t.Fatalf("Four.meme V2 TokenSale topic = %s", topicFourSaleV2)
	}
}

func TestV2AndV3ExactSwapParsing(t *testing.T) {
	t.Parallel()
	pools := []Pool{
		testPool(AdapterV2, testPoolA, testPoolA, testToken, testQuote),
		testPool(AdapterV3, testPoolB, testPoolB, testToken, testQuote),
	}
	parser, err := NewParser(pools, nil)
	if err != nil {
		t.Fatal(err)
	}
	v2Events, err := parser.Parse(testReceipt(
		v2SwapLog(2, testPoolA, testRouter, testWallet, 0, 7_000, 25, 0),
	), []string{testToken})
	if err != nil {
		t.Fatal(err)
	}
	v3Events, err := parser.Parse(testReceipt(
		v3SwapLog(5, testPoolB, testRouter, testWallet, -100, 30_000),
	), []string{testToken})
	if err != nil {
		t.Fatal(err)
	}
	if len(v2Events) != 1 || len(v3Events) != 1 {
		t.Fatalf("V2=%#v V3=%#v", v2Events, v3Events)
	}
	if v2Events[0].Type != EventBuy || v2Events[0].TokenAmountRaw != "25" || v2Events[0].QuoteAmountRaw != "7000" {
		t.Fatalf("V2 event = %#v", v2Events[0])
	}
	if v3Events[0].Type != EventBuy || v3Events[0].TokenAmountRaw != "100" || v3Events[0].QuoteAmountRaw != "30000" {
		t.Fatalf("V3 event = %#v", v3Events[0])
	}
	if v3Events[0].Amount0DeltaRaw != "-100" || v3Events[0].Amount1DeltaRaw != "30000" {
		t.Fatalf("V3 deltas = %#v", v3Events[0])
	}
	if v2Events[0].WalletAddress != testWallet || v3Events[0].WalletAddress != testWallet {
		t.Fatalf("wallet must use transaction origin: V2=%#v V3=%#v", v2Events, v3Events)
	}
}

func TestPancakeV3RealSwapParsing(t *testing.T) {
	t.Parallel()

	const (
		poolAddress = "0x7f51c8aaa6b0599abd16674e2b17fec7a9f674a1"
		txHash      = "0xd1473850232afd328e89ab63cd330fece11dddc58b7b3421af7a6d585ac51de6"
		blockHash   = "0xb34238c1a5633abbcc1d68d6692908dcd7413649f5f37e102c4f4738197085e7"
		sender      = "0x000b102d41da3abe03001a541da1cacc77c26214"
		recipient   = "0x982385d229082d390d4fb942d28eb728e8f42c33"
	)
	parser, err := NewParser([]Pool{
		testPool(AdapterV3, poolAddress, poolAddress, testToken, testQuote),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	events, err := parser.Parse(Receipt{
		ChainID:          56,
		TransactionHash:  txHash,
		From:             testWallet,
		To:               testRouter,
		BlockNumber:      112286766,
		BlockHash:        blockHash,
		TransactionIndex: 2,
		Logs: []Log{{
			Address: poolAddress,
			Topics: []string{
				topicPancakeV3Swap,
				topicAddressWord(sender),
				topicAddressWord(recipient),
			},
			Data:             "0x0000000000000000000000000000000000000000000000062030a54ce3240000fffffffffffffffffffffffffffffffffffffffffffffff761f88806b2554b4400000000000000000000000000000000000000013001f0e3d329c945d52ec5e60000000000000000000000000000000000000000000477e9348fa21a00e9c3bd0000000000000000000000000000000000000000000000000000000000000d6d00000000000000000000000000000000000000000000000001412a522fb200000000000000000000000000000000000000000000000000000000000000000000",
			TransactionHash:  txHash,
			TransactionIndex: 2,
			BlockNumber:      112286766,
			BlockHash:        blockHash,
			LogIndex:         12,
		}},
	}, []string{testToken})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	event := events[0]
	if event.Type != EventSell ||
		event.TokenAmountRaw != "113000000000000000000" ||
		event.QuoteAmountRaw != "158961154685139596476" {
		t.Fatalf("PancakeSwap V3 event = %#v", event)
	}
	if event.TransactionHash != txHash || event.LogIndex != 12 {
		t.Fatalf("PancakeSwap V3 provenance = %#v", event)
	}
}

func TestTargetTokenTransferParsing(t *testing.T) {
	t.Parallel()
	parser, err := NewParser(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	recipient := "0x6666666666666666666666666666666666666666"
	events, err := parser.Parse(testReceipt(Log{
		Address: testToken,
		Topics: []string{
			topicERC20Transfer,
			topicAddressWord(testWallet),
			topicAddressWord(recipient),
		},
		Data:     dataWords(uintWord(12345)),
		LogIndex: 9,
	}), []string{testToken})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(events) != 1 || events[0].Type != EventTokenTransfer ||
		events[0].WalletAddress != testWallet ||
		events[0].RecipientAddress != recipient ||
		events[0].TokenAmountRaw != "12345" {
		t.Fatalf("transfer event = %#v", events)
	}
}

func TestV2AndV3SellDirection(t *testing.T) {
	t.Parallel()
	parser, err := NewParser([]Pool{
		testPool(AdapterV2, testPoolA, testPoolA, testToken, testQuote),
		testPool(AdapterV3, testPoolB, testPoolB, testToken, testQuote),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		log  Log
	}{
		{name: "v2", log: v2SwapLog(1, testPoolA, testRouter, testWallet, 10, 0, 0, 100)},
		{name: "v3", log: v3SwapLog(1, testPoolB, testRouter, testWallet, 10, -100)},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			events, err := parser.Parse(testReceipt(testCase.log), []string{testToken})
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 || events[0].Type != EventSell ||
				events[0].TokenAmountRaw != "10" || events[0].QuoteAmountRaw != "100" {
				t.Fatalf("events = %#v", events)
			}
		})
	}
}

func TestUniversalRouterMultiHopAndSplitRouteAggregation(t *testing.T) {
	t.Parallel()
	pools := []Pool{
		testPool(AdapterV2, testPoolA, testPoolA, testQuote, testMiddle),
		testPool(AdapterV3, testPoolB, testPoolB, testMiddle, testToken),
		{
			ID: 3, ChainID: 56, Protocol: "pancakeswap", Version: "v2", Adapter: AdapterV2,
			PoolKey:    "0x6666666666666666666666666666666666666666",
			LogAddress: "0x6666666666666666666666666666666666666666",
			Token0:     Token{Address: testToken, Decimals: 18},
			Token1:     Token{Address: testMiddle, Decimals: 18},
		},
	}
	parser, err := NewParser(pools, nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt := testReceipt(
		v2SwapLog(1, testPoolA, testRouter, testRouter, 1000, 0, 0, 900),
		v3SwapLog(2, testPoolB, testRouter, testWallet, 900, -100),
		v2SwapLog(3, "0x6666666666666666666666666666666666666666", testRouter, testWallet, 0, 450, 50, 0),
	)
	events, err := parser.Parse(receipt, []string{testToken})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	event := events[0]
	if event.Type != EventBuy || event.TokenAmountRaw != "150" || event.QuoteAmountRaw != "1350" {
		t.Fatalf("aggregated event = %#v", event)
	}
	if event.PoolID != 0 || event.PoolKey != "" {
		t.Fatalf("split route must not claim one pool: %#v", event)
	}
	if got := event.LogIndices; len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("log indices = %#v", got)
	}
	if event.Metadata["route_log_count"] != "3" || event.Metadata["target_leg_count"] != "2" {
		t.Fatalf("route metadata = %#v", event.Metadata)
	}
}

func TestInfinityCLAndBinUseManagerPlusPoolID(t *testing.T) {
	t.Parallel()
	clPool := testPool(AdapterInfinityCL, testPoolID, BSCInfinityCLManager, testToken, testQuote)
	binPoolID := "0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	binPool := testPool(AdapterInfinityBin, binPoolID, BSCInfinityBinManager, testToken, testQuote)
	parser, err := NewParser([]Pool{clPool, binPool}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cl := Log{
		Address:  BSCInfinityCLManager,
		Topics:   []string{topicInfinityCLSwap, testPoolID, topicAddressWord(testRouter)},
		Data:     dataWords(intWord(-10), uintWord(20), uintWord(1), uintWord(2), intWord(3), uintWord(25), uintWord(0)),
		LogIndex: 4,
	}
	bin := Log{
		Address:  BSCInfinityBinManager,
		Topics:   []string{topicInfinityBinSwap, binPoolID, topicAddressWord(testRouter)},
		Data:     dataWords(uintWord(30), intWord(-15), uintWord(1), uintWord(25), uintWord(0)),
		LogIndex: 5,
	}
	unregistered := cl
	unregistered.Topics = append([]string(nil), cl.Topics...)
	unregistered.Topics[1] = "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	unregistered.LogIndex = 3
	events, err := parser.Parse(testReceipt(unregistered, cl, bin), []string{testToken})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].TokenAmountRaw != "10" || events[1].TokenAmountRaw != "30" {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Adapter != AdapterInfinityCL || events[1].Adapter != AdapterInfinityBin {
		t.Fatalf("adapters = %#v", events)
	}
}

func TestFourMemeV1AndV2Trades(t *testing.T) {
	t.Parallel()
	parser, err := NewBSCParser(nil)
	if err != nil {
		t.Fatal(err)
	}
	v1 := Log{
		Address:  BSCFourMemeTokenManager,
		Topics:   []string{topicFourPurchaseV1},
		Data:     dataWords(addressWord(testToken), addressWord(testWallet), uintWord(100), uintWord(200), uintWord(3)),
		LogIndex: 1,
	}
	v2 := Log{
		Address: BSCFourMemeTokenManager,
		Topics:  []string{topicFourSaleV2},
		Data: dataWords(
			addressWord(testToken), addressWord(testWallet), uintWord(4), uintWord(50),
			uintWord(90), uintWord(2), uintWord(900), uintWord(800),
		),
		LogIndex: 2,
	}
	events, err := parser.Parse(testReceipt(v1, v2), []string{testToken})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Type != EventBuy || events[0].Adapter != AdapterFourMemeV1 ||
		events[0].QuoteAddress != NativeTokenAddress || events[0].QuoteAmountRaw != "200" {
		t.Fatalf("V1 event = %#v", events[0])
	}
	if events[1].Type != EventSell || events[1].Adapter != AdapterFourMemeV2 ||
		events[1].TokenAmountRaw != "50" || events[1].QuoteAmountRaw != "90" ||
		events[1].QuoteAddress != "" || events[1].Confidence != "0.9500" {
		t.Fatalf("V2 event = %#v", events[1])
	}
}

func TestFourMemeV2RealChainFixtures(t *testing.T) {
	t.Parallel()
	parser, err := NewBSCParser(nil)
	if err != nil {
		t.Fatal(err)
	}
	fixtures := []struct {
		name      string
		topic     string
		txHash    string
		data      string
		eventType string
		token     string
		account   string
		amountHex string
		costHex   string
	}{
		{
			name: "purchase", topic: topicFourPurchaseV2,
			txHash:    "0x323fc0797edb260723efd9fd9ed18429299567e8f7d659ebe0f589bb04cc30b0",
			data:      "0x00000000000000000000000036a0ab0170282be51214f98d2675790451f34444000000000000000000000000c0509ad5c95b84c441750afbb080bdfbffcc6f3300000000000000000000000000000000000000000000000000000007039e4d1f00000000000000000000000000000000000000000001157cd72166cd862d5a00000000000000000000000000000000000000000000000000008bdb797f286f8200000000000000000000000000000000000000000000000000016608e51c9079000000000000000000000000000000000000000000a1254d576c43fb0fb614000000000000000000000000000000000000000000000000006e708a72938de058",
			eventType: EventBuy, token: "0x36a0ab0170282be51214f98d2675790451f34444",
			account:   "0xc0509ad5c95b84c441750afbb080bdfbffcc6f33",
			amountHex: "1157cd72166cd862d5a00", costHex: "8bdb797f286f82",
		},
		{
			name: "sale", topic: topicFourSaleV2,
			txHash:    "0x00781da6ba98a65ccdca3bb6e10858b926a637ec7333842ecf70b4ffa6e54250",
			data:      "0x000000000000000000000000019eea00588d3cb9c00aa539942894d678344444000000000000000000000000d6be6b2d94bdee58e182cbde8a972d7ab510863700000000000000000000000000000000000000000000000000000002dacd3a1b0000000000000000000000000000000000000000000076581e691d117010960000000000000000000000000000000000000000000000000000185cba2502bb2a00000000000000000000000000000000000000000000000000003e5e057d77a10000000000000000000000000000000000000000017d347ffa2b208dc6781a00000000000000000000000000000000000000000000000000277bead7f6de8938",
			eventType: EventSell, token: "0x019eea00588d3cb9c00aa539942894d678344444",
			account:   "0xd6be6b2d94bdee58e182cbde8a972d7ab5108637",
			amountHex: "76581e691d1170109600", costHex: "185cba2502bb2a",
		},
	}
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			log := Log{
				Address:         BSCFourMemeTokenManager,
				Topics:          []string{fixture.topic},
				Data:            fixture.data,
				TransactionHash: fixture.txHash,
				LogIndex:        1,
			}
			receipt := Receipt{
				ChainID: 56, TransactionHash: fixture.txHash, From: fixture.account,
				Logs: []Log{log},
			}
			events, err := parser.Parse(receipt, []string{fixture.token})
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 {
				t.Fatalf("events = %#v", events)
			}
			event := events[0]
			if event.Type != fixture.eventType || event.TokenAddress != fixture.token ||
				event.WalletAddress != fixture.account ||
				event.TokenAmountRaw != hexDecimal(fixture.amountHex) ||
				event.QuoteAmountRaw != hexDecimal(fixture.costHex) {
				t.Fatalf("event = %#v", event)
			}
		})
	}
}

func TestFourMemeLifecycleEvents(t *testing.T) {
	t.Parallel()
	parser, err := NewBSCParser(nil)
	if err != nil {
		t.Fatal(err)
	}
	create := Log{
		Address:  BSCFourMemeTokenManager,
		Topics:   []string{topicFourTokenCreateV2},
		Data:     fourTokenCreateData(testWallet, testToken, "测试 Meme", "MEME"),
		LogIndex: 1,
	}
	migrated := Log{
		Address:  BSCFourMemeTokenManager,
		Topics:   []string{topicFourLiquidity},
		Data:     dataWords(addressWord(testToken), uintWord(1000), addressWord(testQuote), uintWord(20)),
		LogIndex: 2,
	}
	stopped := Log{
		Address:  BSCFourMemeTokenManager,
		Topics:   []string{topicFourTradeStop},
		Data:     dataWords(addressWord(testToken)),
		LogIndex: 3,
	}
	events, err := parser.Parse(testReceipt(create, migrated, stopped), []string{testToken})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Type != EventTokenCreated || events[0].Metadata["name"] != "测试 Meme" ||
		events[0].Metadata["symbol"] != "MEME" {
		t.Fatalf("create = %#v", events[0])
	}
	if events[1].Type != EventMigrated || events[1].QuoteAddress != testQuote ||
		events[1].TokenAmountRaw != "1000" || events[1].QuoteAmountRaw != "20" {
		t.Fatalf("migration = %#v", events[1])
	}
	if events[2].Type != EventTradingStopped {
		t.Fatalf("stop = %#v", events[2])
	}
}

func TestLiquidityEventsAcrossAdapters(t *testing.T) {
	t.Parallel()
	clPool := testPool(AdapterInfinityCL, testPoolID, BSCInfinityCLManager, testToken, testQuote)
	v2Pool := testPool(AdapterV2, testPoolA, testPoolA, testToken, testQuote)
	v3Pool := testPool(AdapterV3, testPoolB, testPoolB, testToken, testQuote)
	parser, err := NewParser([]Pool{clPool, v2Pool, v3Pool}, nil)
	if err != nil {
		t.Fatal(err)
	}
	v2Mint := Log{
		Address: testPoolA, Topics: []string{topicV2Mint, topicAddressWord(testWallet)},
		Data: dataWords(uintWord(11), uintWord(22)), LogIndex: 1,
	}
	v3Burn := Log{
		Address: testPoolB,
		Topics:  []string{topicV3Burn, topicAddressWord(testWallet), intWordHex(-10), intWordHex(10)},
		Data:    dataWords(uintWord(33), uintWord(44), uintWord(55)), LogIndex: 2,
	}
	clRemove := Log{
		Address: BSCInfinityCLManager,
		Topics:  []string{topicInfinityCLModify, testPoolID, topicAddressWord(testWallet)},
		Data:    dataWords(intWord(-10), intWord(10), intWord(-99), bytes.Repeat([]byte{1}, 32)), LogIndex: 3,
	}
	events, err := parser.Parse(testReceipt(v2Mint, v3Burn, clRemove), []string{testToken})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Type != EventLiquidityAdded ||
		events[1].Type != EventLiquidityRemoved || events[2].Type != EventLiquidityRemoved {
		t.Fatalf("events = %#v", events)
	}
	if events[0].TokenAmountRaw != "11" || events[0].QuoteAmountRaw != "22" {
		t.Fatalf("V2 liquidity = %#v", events[0])
	}
	if events[1].TokenAmountRaw != "44" || events[1].QuoteAmountRaw != "55" {
		t.Fatalf("V3 liquidity = %#v", events[1])
	}
	if events[2].Metadata["liquidity_delta_raw"] != "-99" {
		t.Fatalf("Infinity liquidity = %#v", events[2])
	}
}

func TestPancakeFactoryPoolCreation(t *testing.T) {
	t.Parallel()
	parser, err := NewBSCParser(nil)
	if err != nil {
		t.Fatal(err)
	}
	v2Pair := "0x7777777777777777777777777777777777777777"
	v3Pool := "0x8888888888888888888888888888888888888888"
	v2 := Log{
		Address:  BSCPancakeV2Factory,
		Topics:   []string{topicV2PairCreated, topicAddressWord(testToken), topicAddressWord(testQuote)},
		Data:     dataWords(addressWord(v2Pair), uintWord(123)),
		LogIndex: 1,
	}
	v3 := Log{
		Address: BSCPancakeV3Factory,
		Topics: []string{
			topicV3PoolCreated, topicAddressWord(testToken), topicAddressWord(testQuote), intWordHex(500),
		},
		Data:     dataWords(intWord(10), addressWord(v3Pool)),
		LogIndex: 2,
	}
	events, err := parser.Parse(testReceipt(v2, v3), []string{testToken})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Type != EventPoolInitialized || events[0].PoolKey != v2Pair ||
		events[0].Metadata["pair_count"] != "123" {
		t.Fatalf("V2 factory event = %#v", events[0])
	}
	if events[1].Type != EventPoolInitialized || events[1].PoolKey != v3Pool ||
		events[1].Metadata["fee"] != "500" || events[1].Metadata["tick_spacing"] != "10" {
		t.Fatalf("V3 factory event = %#v", events[1])
	}
}

func TestInfinityManagerPoolDiscovery(t *testing.T) {
	t.Parallel()
	parser, err := NewBSCParser(nil)
	if err != nil {
		t.Fatal(err)
	}
	cl := Log{
		Address: BSCInfinityCLManager,
		Topics: []string{
			topicInfinityCLInitialize, testPoolID, topicAddressWord(testToken), topicAddressWord(testQuote),
		},
		Data: dataWords(
			addressWord(NativeTokenAddress), uintWord(500), bytes.Repeat([]byte{1}, 32),
			uintWord(123456), intWord(-20),
		),
		LogIndex: 1,
	}
	binPoolID := "0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	bin := Log{
		Address: BSCInfinityBinManager,
		Topics: []string{
			topicInfinityBinInitialize, binPoolID, topicAddressWord(testToken), topicAddressWord(testQuote),
		},
		Data: dataWords(
			addressWord(NativeTokenAddress), uintWord(100), bytes.Repeat([]byte{2}, 32), uintWord(8388608),
		),
		LogIndex: 2,
	}
	events, err := parser.Parse(testReceipt(cl, bin), []string{testToken})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Adapter != AdapterInfinityCL || events[0].PoolKey != testPoolID ||
		events[0].Metadata["fee"] != "500" || events[0].Metadata["tick"] != "-20" {
		t.Fatalf("CL discovery = %#v", events[0])
	}
	if events[1].Adapter != AdapterInfinityBin || events[1].PoolKey != binPoolID ||
		events[1].Metadata["active_id"] != "8388608" {
		t.Fatalf("Bin discovery = %#v", events[1])
	}
}

func TestInfinityBinLiquidityDynamicArrays(t *testing.T) {
	t.Parallel()
	poolID := "0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	parser, err := NewParser([]Pool{
		testPool(AdapterInfinityBin, poolID, BSCInfinityBinManager, testToken, testQuote),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	data := dataWords(
		uintWord(5*32),
		bytes.Repeat([]byte{1}, 32),
		uintWord(7*32),
		bytes.Repeat([]byte{2}, 32),
		bytes.Repeat([]byte{3}, 32),
		uintWord(1),
		uintWord(42),
		uintWord(1),
		bytes.Repeat([]byte{4}, 32),
	)
	log := Log{
		Address:  BSCInfinityBinManager,
		Topics:   []string{topicInfinityBinMint, poolID, topicAddressWord(testWallet)},
		Data:     data,
		LogIndex: 1,
	}
	events, err := parser.Parse(testReceipt(log), []string{testToken})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != EventLiquidityAdded {
		t.Fatalf("events = %#v", events)
	}
	malformed := log
	malformed.Data = dataWords(
		uintWord(99*32),
		bytes.Repeat([]byte{1}, 32),
		uintWord(7*32),
		bytes.Repeat([]byte{2}, 32),
		bytes.Repeat([]byte{3}, 32),
	)
	if _, err := parser.Parse(testReceipt(malformed), []string{testToken}); err == nil {
		t.Fatal("expected invalid dynamic array offset error")
	}
}

func TestMalformedAndDuplicateLogs(t *testing.T) {
	t.Parallel()
	parser, err := NewParser([]Pool{
		testPool(AdapterV2, testPoolA, testPoolA, testToken, testQuote),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	valid := v2SwapLog(1, testPoolA, testRouter, testWallet, 10, 0, 0, 5)
	events, err := parser.Parse(testReceipt(valid, valid), []string{testToken})
	if err != nil || len(events) != 1 {
		t.Fatalf("exact duplicate: events=%#v err=%v", events, err)
	}
	conflict := valid
	conflict.Data = dataWords(uintWord(11), uintWord(0), uintWord(0), uintWord(5))
	if _, err := parser.Parse(testReceipt(valid, conflict), []string{testToken}); err == nil {
		t.Fatal("expected conflicting duplicate error")
	}
	malformed := valid
	malformed.Data = "0x01"
	if _, err := parser.Parse(testReceipt(malformed), []string{testToken}); err == nil {
		t.Fatal("expected malformed ABI error")
	}
	sameDirection := valid
	sameDirection.Data = dataWords(uintWord(10), uintWord(5), uintWord(0), uintWord(0))
	if _, err := parser.Parse(testReceipt(sameDirection), []string{testToken}); err == nil {
		t.Fatal("expected invalid swap delta error")
	}
	wrongTransaction := valid
	wrongTransaction.TransactionHash = "0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	receipt := testReceipt(valid)
	receipt.Logs = []Log{wrongTransaction}
	if _, err := parser.Parse(receipt, []string{testToken}); err == nil {
		t.Fatal("expected transaction mismatch error")
	}
	removed := valid
	removed.Removed = true
	events, err = parser.Parse(testReceipt(removed), []string{testToken})
	if err != nil || len(events) != 0 {
		t.Fatalf("removed log must be ignored: events=%#v err=%v", events, err)
	}
}

func TestLogFromRPC(t *testing.T) {
	t.Parallel()
	value, err := LogFromRPC(structRPCLog())
	if err != nil {
		t.Fatal(err)
	}
	if value.BlockNumber != 16 || value.TransactionIndex != 2 || value.LogIndex != 3 {
		t.Fatalf("converted log = %#v", value)
	}
}

func testPool(adapter, key, logAddress, token0, token1 string) Pool {
	return Pool{
		ID: 1, ChainID: 56, Protocol: "pancakeswap", Version: "test", Adapter: adapter,
		PoolKey: key, LogAddress: logAddress,
		Token0: Token{Address: token0, Decimals: 18},
		Token1: Token{Address: token1, Decimals: 18},
	}
}

func testReceipt(logs ...Log) Receipt {
	for index := range logs {
		logs[index].TransactionHash = testHash
		logs[index].BlockHash = testBlock
		logs[index].BlockNumber = 100
		logs[index].TransactionIndex = 7
	}
	return Receipt{
		ChainID: 56, TransactionHash: testHash, From: testWallet, To: testRouter,
		BlockNumber: 100, BlockHash: testBlock, TransactionIndex: 7, Logs: logs,
	}
}

func v2SwapLog(index uint64, address, sender, recipient string, amount0In, amount1In, amount0Out, amount1Out int64) Log {
	return Log{
		Address:  address,
		Topics:   []string{topicV2Swap, topicAddressWord(sender), topicAddressWord(recipient)},
		Data:     dataWords(uintWord(amount0In), uintWord(amount1In), uintWord(amount0Out), uintWord(amount1Out)),
		LogIndex: index,
	}
}

func v3SwapLog(index uint64, address, sender, recipient string, amount0, amount1 int64) Log {
	return Log{
		Address:  address,
		Topics:   []string{topicV3Swap, topicAddressWord(sender), topicAddressWord(recipient)},
		Data:     dataWords(intWord(amount0), intWord(amount1), uintWord(1), uintWord(2), intWord(3)),
		LogIndex: index,
	}
}

func dataWords(values ...[]byte) string {
	var result []byte
	for _, value := range values {
		if len(value) != 32 {
			panic("test ABI word must be 32 bytes")
		}
		result = append(result, value...)
	}
	return "0x" + hex.EncodeToString(result)
}

func uintWord(value int64) []byte {
	if value < 0 {
		panic("negative uint")
	}
	result := make([]byte, 32)
	big.NewInt(value).FillBytes(result)
	return result
}

func intWord(value int64) []byte {
	result := make([]byte, 32)
	number := big.NewInt(value)
	if value < 0 {
		number.Add(number, new(big.Int).Lsh(big.NewInt(1), 256))
	}
	number.FillBytes(result)
	return result
}

func intWordHex(value int64) string {
	return "0x" + hex.EncodeToString(intWord(value))
}

func addressWord(address string) []byte {
	raw, err := hex.DecodeString(strings.TrimPrefix(address, "0x"))
	if err != nil || len(raw) != 20 {
		panic("invalid test address")
	}
	result := make([]byte, 32)
	copy(result[12:], raw)
	return result
}

func topicAddressWord(address string) string {
	return "0x" + hex.EncodeToString(addressWord(address))
}

func fourTokenCreateData(creator, token, name, symbol string) string {
	head := [][]byte{
		addressWord(creator),
		addressWord(token),
		uintWord(9),
		uintWord(8 * 32),
		nil,
		uintWord(1_000_000),
		uintWord(123),
		uintWord(7),
	}
	nameData := dynamicBytes([]byte(name))
	head[4] = uintWord(int64(8*32 + len(nameData)))
	var raw []byte
	for _, word := range head {
		raw = append(raw, word...)
	}
	raw = append(raw, nameData...)
	raw = append(raw, dynamicBytes([]byte(symbol))...)
	return "0x" + hex.EncodeToString(raw)
}

func dynamicBytes(value []byte) []byte {
	result := append(uintWord(int64(len(value))), value...)
	padding := (32 - len(value)%32) % 32
	return append(result, make([]byte, padding)...)
}

func hexDecimal(value string) string {
	result, ok := new(big.Int).SetString(value, 16)
	if !ok {
		panic("invalid test hex")
	}
	return result.String()
}
