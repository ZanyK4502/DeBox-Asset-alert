package marketcollector

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
)

func TestOrderedMarketReadsBoundPrefetchIncludingConsumerAndKeepOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := make(chan int, 8)
	first, consumeFirst := make(chan struct{}), make(chan struct{})
	consuming := make(chan struct{})
	var active, peak atomic.Int32
	var order []int
	done := make(chan error, 1)
	go func() {
		done <- consumeOrderedMarketReads(ctx, 6, 2, func(ctx context.Context, index int) (hydratedMarketTransaction, error) {
			current := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); current > old && !peak.CompareAndSwap(old, current); old = peak.Load() {
			}
			started <- index
			if index == 0 {
				select {
				case <-first:
				case <-ctx.Done():
					return hydratedMarketTransaction{}, ctx.Err()
				}
			}
			return hydratedMarketTransaction{}, nil
		}, func(ctx context.Context, index int, _ hydratedMarketTransaction) error {
			order = append(order, index)
			if index == 0 {
				close(consuming)
				select {
				case <-consumeFirst:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		})
	}()
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("two read workers did not start")
		}
	}
	assertNoRead := func() {
		t.Helper()
		select {
		case index := <-started:
			t.Fatalf("read %d exceeded the two owned payload credits", index)
		case <-time.After(20 * time.Millisecond):
		}
	}
	assertNoRead() // The second result cannot keep fetching behind a slow first.
	close(first)
	select {
	case <-consuming:
	case <-ctx.Done():
		t.Fatal("serial consumer did not start")
	}
	assertNoRead() // Processing the first value still owns its payload credit.
	close(consumeFirst)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []int{0, 1, 2, 3, 4, 5}) || peak.Load() > 2 {
		t.Fatalf("order=%v peak reads=%d", order, peak.Load())
	}
}

func TestOrderedMarketReadsCancelAndJoinOnScopeReload(t *testing.T) {
	secondStarted, secondExited := make(chan struct{}), make(chan struct{})
	var writes []int
	err := consumeOrderedMarketReads(context.Background(), 3, 2, func(ctx context.Context, index int) (hydratedMarketTransaction, error) {
		if index == 1 {
			close(secondStarted)
			defer close(secondExited)
			<-ctx.Done()
			return hydratedMarketTransaction{}, ctx.Err()
		}
		return hydratedMarketTransaction{}, nil
	}, func(_ context.Context, index int, _ hydratedMarketTransaction) error {
		writes = append(writes, index)
		<-secondStarted
		return ErrParserScopeChanged
	})
	if !errors.Is(err, ErrParserScopeChanged) || !reflect.DeepEqual(writes, []int{0}) {
		t.Fatalf("scope changed after extra persistence: writes=%v err=%v", writes, err)
	}
	select {
	case <-secondExited:
	default:
		t.Fatal("read worker survived cancellation")
	}
}

func TestOrderedMarketReadFailurePersistsOnlyOriginalPrefix(t *testing.T) {
	failure := errors.New("receipt read failed")
	var writes []int
	err := consumeOrderedMarketReads(context.Background(), 5, 2, func(_ context.Context, index int) (hydratedMarketTransaction, error) {
		if index == 1 {
			return hydratedMarketTransaction{}, failure
		}
		return hydratedMarketTransaction{}, nil
	}, func(_ context.Context, index int, _ hydratedMarketTransaction) error {
		writes = append(writes, index)
		return nil
	})
	if !errors.Is(err, failure) || !reflect.DeepEqual(writes, []int{0}) {
		t.Fatalf("read error changed serial prefix: writes=%v err=%v", writes, err)
	}
}

func TestOrderedMarketReadsRepeatedCancellationReclaimsReaders(t *testing.T) {
	var active atomic.Int32
	for iteration := 0; iteration < 25; iteration++ {
		secondStarted := make(chan struct{})
		var writes int
		err := consumeOrderedMarketReads(context.Background(), 5, 2,
			func(ctx context.Context, index int) (hydratedMarketTransaction, error) {
				active.Add(1)
				defer active.Add(-1)
				if index == 1 {
					close(secondStarted)
					<-ctx.Done()
					return hydratedMarketTransaction{}, ctx.Err()
				}
				return hydratedMarketTransaction{receiptPayload: make([]byte, 8192)}, nil
			}, func(_ context.Context, _ int, _ hydratedMarketTransaction) error {
				writes++
				<-secondStarted
				return ErrParserScopeChanged
			})
		if !errors.Is(err, ErrParserScopeChanged) || active.Load() != 0 || writes != 1 {
			t.Fatalf("iteration %d retained reader work or wrote a future source: active=%d writes=%d err=%v", iteration, active.Load(), writes, err)
		}
	}
}

type countedHeaderChain struct {
	*receiptFactChain
	headers int
}

func (c *countedHeaderChain) BlockByNumber(ctx context.Context, number uint64, transactions bool, key, fallback string) (map[string]any, error) {
	c.headers++
	return c.receiptFactChain.BlockByNumber(ctx, number, transactions, key, fallback)
}

func TestReadMarketTransactionReusesOnlyMatchingRangeHeader(t *testing.T) {
	s, repository, original, _ := receiptFactFixture(t)
	c := &countedHeaderChain{receiptFactChain: original}
	s.chain = c
	header, err := decodeBlockHeader(original.blocks[100])
	if err != nil {
		t.Fatal(err)
	}
	expected, err := expectedScannerReceipts(original.receipt["logs"].([]chain.RPCLog))
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.readMarketTransaction(context.Background(), testHashA, &header, []receiptExpectation{expected[testHashA]})
	if err != nil || c.headers != 0 || read.header.Hash != testHashB || repository.writes != 0 {
		t.Fatalf("read performed a redundant header read or a write: headers=%d err=%v", c.headers, err)
	}
	for _, bad := range []blockHeader{{Number: 101, Hash: header.Hash, Timestamp: header.Timestamp}, {Number: header.Number, Hash: testHashC, Timestamp: header.Timestamp}, {Number: header.Number, Hash: header.Hash}} {
		if _, err := s.readMarketTransaction(context.Background(), testHashA, &bad, nil); err == nil {
			t.Fatal("mismatched or untimed header accepted")
		}
	}
	if _, err := s.readMarketTransaction(context.Background(), testHashA, nil, nil); err != nil || c.headers != 1 {
		t.Fatalf("standalone hydration lost fresh header validation: headers=%d err=%v", c.headers, err)
	}
}

func TestReceiptObjectDecoderPreservesEncodedValidation(t *testing.T) {
	_, _, client, _ := receiptFactFixture(t)
	payload, _ := json.Marshal(client.receipt)
	var object map[string]any
	_ = json.Unmarshal(payload, &object)
	old, oldErr := decodeReceiptPayload(payload, 56)
	current, err := decodeReceiptObject(object, 56)
	if oldErr != nil || err != nil || !reflect.DeepEqual(old, current) {
		t.Fatalf("direct decode changed the receipt: old=%+v current=%+v errors=%v/%v", old, current, oldErr, err)
	}
	for _, edit := range []func(map[string]any){
		func(v map[string]any) { delete(v, "logs") },
		func(v map[string]any) { v["logs"] = nil },
		func(v map[string]any) { v["logs"] = "not an array" },
		func(v map[string]any) { v["blockNumber"] = 100 },
		func(v map[string]any) { v["status"] = "0x0" },
		func(v map[string]any) { v["logs"].([]any)[0].(map[string]any)["removed"] = "false" },
		func(v map[string]any) { v["logs"].([]any)[0].(map[string]any)["topics"] = []any{5} },
	} {
		var candidate map[string]any
		_ = json.Unmarshal(payload, &candidate)
		edit(candidate)
		encoded, _ := json.Marshal(candidate)
		if _, oldErr := decodeReceiptPayload(encoded, 56); oldErr == nil {
			t.Fatal("invalid fixture was accepted by prior decoder")
		}
		if _, err := decodeReceiptObject(candidate, 56); err == nil {
			t.Fatal("direct decoder relaxed an existing validation")
		}
	}
}

func TestHydrationRetainsEncodedReceiptByteLimit(t *testing.T) {
	s, r, c, _ := receiptFactFixture(t)
	c.receipt["extra"] = strings.Repeat("x", 16<<20)
	if _, err := s.readMarketTransaction(context.Background(), testHashA, nil, nil); err == nil || !strings.Contains(err.Error(), "bounded encoding") || r.writes != 0 {
		t.Fatalf("large encoded receipt was not rejected before any write: %v", err)
	}
}

func TestScannerSettingsDefaultsAndDirectUpperBounds(t *testing.T) {
	s := New(Dependencies{}, Settings{Enabled: true})
	if s.settings.ScanBatchSize != 500 || s.settings.HydrationConcurrency != 2 || s.settings.ConfirmationDepth != 15 || s.settings.ScanInterval != time.Minute {
		t.Fatalf("defaults changed unrelated timing/finality: %+v", s.settings)
	}
	for _, settings := range []Settings{{Enabled: true, ScanBatchSize: 1001}, {Enabled: true, HydrationConcurrency: 3}} {
		if err := New(Dependencies{Repository: &fakeRepository{}}, settings).ScanLogs(context.Background()); err == nil {
			t.Fatal("direct settings bypassed the hard bounds")
		}
	}
}
