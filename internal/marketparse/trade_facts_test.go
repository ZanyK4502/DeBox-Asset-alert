package marketparse

import (
	"reflect"
	"testing"
)

func TestTradeFactsPreserveBothMonitoredSidesOfOneSwap(t *testing.T) {
	pool := testPool(AdapterV2, testPoolA, testPoolA, testToken, testQuote)
	pool.ID = 17
	parser, err := NewParser([]Pool{pool}, nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt := testReceipt(v2SwapLog(4, testPoolA, testRouter, testWallet, 100, 0, 0, 75))
	events, err := parser.Parse(receipt, []string{testToken, testQuote})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("both monitored tokens need independent events: %#v", events)
	}
	for _, event := range events {
		wantType, wantTokenAmount, wantQuote, wantQuoteAmount := EventSell, "100", testQuote, "75"
		if event.TokenAddress == testQuote {
			wantType, wantTokenAmount, wantQuote, wantQuoteAmount = EventBuy, "75", testToken, "100"
		} else if event.TokenAddress != testToken {
			t.Fatalf("unexpected target: %#v", event)
		}
		if event.Type != wantType || event.TokenAmountRaw != wantTokenAmount || len(event.TradeLegs) != 1 {
			t.Fatalf("incorrect logical side: %#v", event)
		}
		leg := event.TradeLegs[0]
		if leg.PoolID != 17 || leg.PoolKey != testPoolA || leg.LogIndex != 4 ||
			leg.TokenAmountRaw != wantTokenAmount || leg.QuoteAddress != wantQuote || leg.QuoteAmountRaw != wantQuoteAmount ||
			leg.WalletAddress != testWallet || leg.RecipientAddress != testWallet {
			t.Fatalf("incorrect exact side fact: %#v", leg)
		}
	}
}

func TestTradeFactsKeepSplitPoolsAndMixedQuotesSeparate(t *testing.T) {
	pools := []Pool{
		testPool(AdapterV2, testPoolA, testPoolA, testToken, testQuote),
		testPool(AdapterV3, testPoolB, testPoolB, testToken, testMiddle),
	}
	pools[0].ID, pools[1].ID = 17, 29
	parser, err := NewParser(pools, nil)
	if err != nil {
		t.Fatal(err)
	}
	first := v2SwapLog(2, testPoolA, testRouter, testWallet, 0, 7000, 25, 0)
	second := v3SwapLog(8, testPoolB, testRouter, testWallet, -100, 30000)
	events, err := parser.Parse(testReceipt(second, first, first), []string{testToken})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("one logical transaction, not one per pool: %#v", events)
	}
	event := events[0]
	if event.TokenAmountRaw != "125" || event.PoolID != 0 || event.QuoteAddress != "" || event.QuoteAmountRaw != "" {
		t.Fatalf("mixed quote totals must not be added: %#v", event)
	}
	if len(event.TradeLegs) != 2 || event.TradeLegs[0].PoolID != 17 || event.TradeLegs[1].PoolID != 29 ||
		event.TradeLegs[0].TokenAmountRaw != "25" || event.TradeLegs[1].TokenAmountRaw != "100" ||
		event.TradeLegs[0].QuoteAddress != testQuote || event.TradeLegs[1].QuoteAddress != testMiddle ||
		event.TradeLegs[0].QuoteAmountRaw != "7000" || event.TradeLegs[1].QuoteAmountRaw != "30000" {
		t.Fatalf("pool facts lost identities or exact amounts: %#v", event.TradeLegs)
	}
}

func TestTradeFactsAddingAnotherTargetDoesNotChangeExistingParentFacts(t *testing.T) {
	parser, err := NewParser([]Pool{testPool(AdapterV2, testPoolA, testPoolA, testToken, testQuote)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt := testReceipt(v2SwapLog(4, testPoolA, testRouter, testWallet, 100, 0, 0, 75))
	before, err := parser.Parse(receipt, []string{testToken})
	if err != nil {
		t.Fatal(err)
	}
	after, err := parser.Parse(receipt, []string{testToken, testQuote})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range after {
		if event.TokenAddress == testToken {
			if len(before) != 1 || !reflect.DeepEqual(before[0], event) {
				t.Fatalf("adding the opposite monitored token changed existing facts: before=%#v after=%#v", before, event)
			}
			return
		}
	}
	t.Fatal("existing target disappeared after adding another target")
}
