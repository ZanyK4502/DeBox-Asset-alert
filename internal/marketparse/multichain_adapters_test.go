package marketparse

import "testing"

func TestAlgebraLegacyAndIntegralParsing(t *testing.T) {
	t.Parallel()
	legacyPool := testPool(
		AdapterAlgebra,
		testPoolA,
		testPoolA,
		testToken,
		testQuote,
	)
	integralPool := testPool(
		AdapterAlgebra,
		testPoolB,
		testPoolB,
		testToken,
		testQuote,
	)
	parser, err := NewParser([]Pool{legacyPool, integralPool}, nil)
	if err != nil {
		t.Fatal(err)
	}

	events, err := parser.Parse(testReceipt(
		v3SwapLog(1, testPoolA, testRouter, testWallet, -25, 7_000),
		Log{
			Address: testPoolB,
			Topics: []string{
				topicAlgebraSwapIntegral,
				topicAddressWord(testRouter),
				topicAddressWord(testWallet),
			},
			Data: dataWords(
				intWord(-100),
				intWord(30_000),
				uintWord(1),
				uintWord(2),
				intWord(3),
				uintWord(4),
				uintWord(5),
			),
			LogIndex: 2,
		},
	), []string{testToken})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 ||
		events[0].Type != EventBuy ||
		events[0].TokenAmountRaw != "125" ||
		events[0].QuoteAmountRaw != "37000" ||
		events[0].Metadata["target_leg_count"] != "2" {
		t.Fatalf("Algebra swap events = %#v", events)
	}

	burnEvents, err := parser.Parse(testReceipt(Log{
		Address: testPoolB,
		Topics: []string{
			topicAlgebraBurnIntegral,
			topicAddressWord(testWallet),
			intWordHex(-10),
			intWordHex(10),
		},
		Data: dataWords(
			uintWord(50),
			uintWord(11),
			uintWord(22),
			uintWord(3),
		),
		LogIndex: 3,
	}), []string{testToken})
	if err != nil {
		t.Fatal(err)
	}
	if len(burnEvents) != 1 ||
		burnEvents[0].Type != EventLiquidityRemoved ||
		burnEvents[0].TokenAmountRaw != "11" ||
		burnEvents[0].QuoteAmountRaw != "22" {
		t.Fatalf("Algebra burn event = %#v", burnEvents)
	}
}

func TestSolidlySwapMintAndBurnParsing(t *testing.T) {
	t.Parallel()
	parser, err := NewParser([]Pool{
		testPool(
			AdapterSolidly,
			testPoolA,
			testPoolA,
			testToken,
			testQuote,
		),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	events, err := parser.Parse(testReceipt(
		Log{
			Address: testPoolA,
			Topics: []string{
				topicSolidlySwap,
				topicAddressWord(testRouter),
				topicAddressWord(testWallet),
			},
			Data: dataWords(
				uintWord(0),
				uintWord(7_000),
				uintWord(25),
				uintWord(0),
			),
			LogIndex: 1,
		},
		Log{
			Address: testPoolA,
			Topics: []string{
				topicSolidlyMint,
				topicAddressWord(testWallet),
			},
			Data:     dataWords(uintWord(11), uintWord(22)),
			LogIndex: 2,
		},
		Log{
			Address: testPoolA,
			Topics: []string{
				topicSolidlyBurn,
				topicAddressWord(testWallet),
				topicAddressWord(testRouter),
			},
			Data:     dataWords(uintWord(33), uintWord(44)),
			LogIndex: 3,
		},
	), []string{testToken})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 ||
		events[0].Type != EventBuy ||
		events[0].TokenAmountRaw != "25" ||
		events[1].Type != EventLiquidityAdded ||
		events[1].TokenAmountRaw != "11" ||
		events[2].Type != EventLiquidityRemoved ||
		events[2].TokenAmountRaw != "33" ||
		events[2].RecipientAddress != testRouter {
		t.Fatalf("Solidly events = %#v", events)
	}
}

func TestAlgebraAndSolidlyFactoryParsing(t *testing.T) {
	t.Parallel()
	algebraFactory := "0x6666666666666666666666666666666666666666"
	solidlyFactory := "0x7777777777777777777777777777777777777777"
	algebraPool := "0x8888888888888888888888888888888888888888"
	solidlyPool := "0x9999999999999999999999999999999999999999"
	parser, err := NewParser(nil, []Emitter{
		{
			Address: algebraFactory, Protocol: "quickswap",
			Version: "algebra", Adapter: AdapterAlgebra,
		},
		{
			Address: solidlyFactory, Protocol: "aerodrome",
			Version: "solidly", Adapter: AdapterSolidly,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := parser.Parse(testReceipt(
		Log{
			Address: algebraFactory,
			Topics: []string{
				topicAlgebraPoolCreated,
				topicAddressWord(testToken),
				topicAddressWord(testQuote),
			},
			Data:     dataWords(addressWord(algebraPool)),
			LogIndex: 1,
		},
		Log{
			Address: solidlyFactory,
			Topics: []string{
				topicSolidlyPoolCreated,
				topicAddressWord(testToken),
				topicAddressWord(testQuote),
				intWordHex(1),
			},
			Data: dataWords(
				addressWord(solidlyPool),
				uintWord(12),
			),
			LogIndex: 2,
		},
	), []string{testToken})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 ||
		events[0].PoolKey != algebraPool ||
		events[0].Adapter != AdapterAlgebra ||
		events[1].PoolKey != solidlyPool ||
		events[1].Adapter != AdapterSolidly ||
		events[1].Metadata["stable"] != "true" ||
		events[1].Metadata["pool_count"] != "12" {
		t.Fatalf("factory events = %#v", events)
	}
}
