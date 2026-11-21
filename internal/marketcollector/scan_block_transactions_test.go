package marketcollector

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
)

type fullBlockChain struct {
	*receiptFactChain
	transactions     []any
	fullBlockErr     error
	blockRequests    []bool
	transactionReads int
}

func (c *fullBlockChain) BlockByNumber(ctx context.Context, number uint64, full bool, key, fallback string) (map[string]any, error) {
	c.blockRequests = append(c.blockRequests, full)
	if !full {
		return c.receiptFactChain.BlockByNumber(ctx, number, false, key, fallback)
	}
	if c.fullBlockErr != nil {
		return nil, c.fullBlockErr
	}
	block := maps.Clone(c.blocks[number])
	block["transactions"] = c.transactions
	return block, nil
}

func (c *fullBlockChain) RPCTransactionByHash(ctx context.Context, hash, key, fallback string) (map[string]any, error) {
	c.transactionReads++
	return c.receiptFactChain.RPCTransactionByHash(ctx, hash, key, fallback)
}

func TestDenseScanBlockReusesOnlyCompleteTransactionIdentities(t *testing.T) {
	for _, test := range []struct {
		name             string
		chainKey         string
		configure        func(*fullBlockChain)
		wantBlocks       []bool
		wantTxReads      int
		wantHydrationErr string
	}{
		{name: "polygon reuses full block", chainKey: "polygon", wantBlocks: []bool{true}},
		{name: "ethereum reuses full block", chainKey: "ethereum", wantBlocks: []bool{true}},
		{name: "header only on other chains", chainKey: "bsc", wantBlocks: []bool{false}, wantTxReads: 1},
		{name: "incomplete response", chainKey: "ethereum", configure: func(c *fullBlockChain) { c.transactions = c.transactions[:3] }, wantBlocks: []bool{true}, wantTxReads: 1},
		{name: "incomplete identity", chainKey: "ethereum", configure: func(c *fullBlockChain) { delete(c.transactions[0].(map[string]any), "from") }, wantBlocks: []bool{true}, wantTxReads: 1},
		{name: "oversized full block", chainKey: "ethereum", configure: func(c *fullBlockChain) { c.fullBlockErr = chain.ErrMarketRPCResponseTooLarge }, wantBlocks: []bool{true, false}, wantTxReads: 1},
		{name: "conflicting receipt", chainKey: "ethereum", configure: func(c *fullBlockChain) {
			c.transactions[0].(map[string]any)["from"] = "0x" + strings.Repeat("9", 40)
		}, wantBlocks: []bool{true}, wantHydrationErr: "senders differ"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, original, _ := receiptFactFixture(t)
			client := &fullBlockChain{receiptFactChain: original}
			service.chain = client
			profile, err := chain.ChainProfile(test.chainKey, "")
			if err != nil {
				t.Fatal(err)
			}
			service.settings.ChainKey, service.settings.ChainID = profile.Key, profile.ChainID
			logs := append([]chain.RPCLog(nil), original.receipt["logs"].([]chain.RPCLog)...)
			client.transactions = append(client.transactions, maps.Clone(original.transaction))
			for i := 2; i <= minFullBlockTrackedTransactions; i++ {
				hash := fmt.Sprintf("0x%064x", 1000+i)
				log := logs[0]
				log.TransactionHash = hash
				logs = append(logs, log)
				transaction := maps.Clone(original.transaction)
				transaction["hash"] = hash
				transaction["transactionIndex"] = fmt.Sprintf("0x%x", i)
				client.transactions = append(client.transactions, transaction)
			}
			if test.configure != nil {
				test.configure(client)
			}
			expected, err := expectedScannerReceipts(logs)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			blocks := newScanBlockTransactions(service.settings.ChainKey, expected)
			header, err := service.fetchScanBlockHeader(ctx, 100, blocks)
			if err != nil || header.Hash != testHashB || !reflect.DeepEqual(client.blockRequests, test.wantBlocks) {
				t.Fatalf("header=%+v err=%v full requests=%v", header, err, client.blockRequests)
			}
			_, err = service.readMarketTransactionWithIdentity(ctx, testHashA, &header, []receiptExpectation{expected[testHashA]}, blocks.identity(testHashA))
			if test.wantHydrationErr == "" && err != nil || test.wantHydrationErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantHydrationErr)) {
				t.Fatalf("hydrate error=%v, want %q", err, test.wantHydrationErr)
			}
			if client.transactionReads != test.wantTxReads {
				t.Fatalf("used %d individual tx reads, want %d", client.transactionReads, test.wantTxReads)
			}
		})
	}
}
