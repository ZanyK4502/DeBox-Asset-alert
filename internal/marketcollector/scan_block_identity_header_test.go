package marketcollector

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
)

type scanIdentityHeaderReader struct {
	block             map[string]any
	err               error
	fullBlockCalls    int
	blockReceiptCalls int
}

func (reader *scanIdentityHeaderReader) MarketFullBlock(context.Context, uint64, string, string) (map[string]any, error) {
	reader.fullBlockCalls++
	return reader.block, reader.err
}

func (reader *scanIdentityHeaderReader) MarketBlockReceipts(context.Context, string, string, string) ([]any, error) {
	reader.blockReceiptCalls++
	return nil, errors.New("unexpected block receipt read")
}

func TestScanBlockIdentityHeaderKeepsOnlyUsableCompactIdentities(t *testing.T) {
	blockHash := "0x" + strings.Repeat("1", 64)
	parentHash := "0x" + strings.Repeat("2", 64)
	firstHash := "0x" + strings.Repeat("3", 64)
	secondHash := "0x" + strings.Repeat("4", 64)
	unselectedHash := "0x" + strings.Repeat("5", 64)
	for _, test := range []struct {
		name            string
		modify          func(map[string]any, map[string]any)
		wantsIdentities bool
	}{
		{name: "both selected identities", wantsIdentities: true},
		{name: "wrong block number", modify: func(_, second map[string]any) { second["blockNumber"] = "0x2b" }},
		{name: "wrong block hash", modify: func(_, second map[string]any) { second["blockHash"] = parentHash }},
		{name: "duplicate position", modify: func(first, second map[string]any) { second["transactionIndex"] = first["transactionIndex"] }},
		{name: "missing sender", modify: func(_, second map[string]any) { delete(second, "from") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			transaction := func(hash, index string) map[string]any {
				return map[string]any{"hash": hash, "blockHash": blockHash, "blockNumber": "0x2a", "transactionIndex": index,
					"from": "0x" + strings.Repeat("a", 40), "input": strings.Repeat("f", 1024)}
			}
			first, second := transaction(firstHash, "0x1"), transaction(secondHash, "0x2")
			if test.modify != nil {
				test.modify(first, second)
			}
			reader := &scanIdentityHeaderReader{block: map[string]any{
				"number": "0x2a", "hash": blockHash, "parentHash": parentHash, "timestamp": "0x65",
				"transactions": []any{transaction(unselectedHash, "0x0"), first, second},
			}}
			expected := map[string]receiptExpectation{
				firstHash:  {blockNumber: 42, blockHash: blockHash},
				secondHash: {blockNumber: 42, blockHash: blockHash},
			}
			service := &Service{settings: Settings{ChainKey: "base"}}
			header, identities, err := service.fetchScanBlockIdentityHeader(context.Background(), reader, 42, 2, expected)
			if err != nil || header.Hash != blockHash || header.Number != 42 || reader.fullBlockCalls != 1 || reader.blockReceiptCalls != 0 {
				t.Fatalf("block read: header=%+v calls=%d/%d err=%v", header, reader.fullBlockCalls, reader.blockReceiptCalls, err)
			}
			if !test.wantsIdentities {
				if identities != nil {
					t.Fatalf("kept identities from incomplete batch: %+v", identities)
				}
				return
			}
			if len(identities) != 2 || len(identities[firstHash]) != 5 || len(identities[secondHash]) != 5 || identities[unselectedHash] != nil {
				t.Fatalf("unexpected compact identity set: %+v", identities)
			}
		})
	}
}

func TestScanBlockIdentityHeaderPropagatesCancellationAndBounds(t *testing.T) {
	service := &Service{settings: Settings{ChainKey: "base"}}
	reader := &scanIdentityHeaderReader{err: chain.ErrMarketRPCResponseTooLarge}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := service.fetchScanBlockIdentityHeader(ctx, reader, 42, 1, nil); !errors.Is(err, context.Canceled) || reader.fullBlockCalls != 0 {
		t.Fatalf("canceled call read provider: calls=%d err=%v", reader.fullBlockCalls, err)
	}
	if _, _, err := service.fetchScanBlockIdentityHeader(context.Background(), reader, 42, 1, nil); !errors.Is(err, chain.ErrMarketRPCResponseTooLarge) || reader.fullBlockCalls != 1 {
		t.Fatalf("oversized response did not reach fallback caller: calls=%d err=%v", reader.fullBlockCalls, err)
	}
}
