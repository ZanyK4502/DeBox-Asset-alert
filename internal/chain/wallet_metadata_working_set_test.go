package chain

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestWalletMetadataRetainsThousandTokenWorkingSetWithoutMoreFlights(t *testing.T) {
	var unexpected atomic.Int64
	primary, err := NewClient("local-only", "http://invalid", WithHTTPClient(walletBudgetDoer(func(*http.Request) (*http.Response, error) {
		unexpected.Add(1)
		return nil, errors.New("cached token metadata must not request RPC")
	})))
	if err != nil {
		t.Fatal(err)
	}
	wallet := NewWalletReadClient(primary)
	now := time.Now()
	wallet.now = func() time.Time { return now }
	for i := 1; i <= 1000; i++ {
		key := walletTokenMetadataKey{chainID: 56, token: fmt.Sprintf("0x%040x", i)}
		wallet.storeMetadata(key, walletTokenMetadata{decimals: 6, symbol: "TST", expiresAt: now.Add(walletTokenMetadataTTL)})
	}
	for pass := 0; pass < 2; pass++ {
		for i := 1; i <= 1000; i++ {
			metadata, err := wallet.tokenMetadata(context.Background(), wallet.Client, profiles["bsc"], fmt.Sprintf("0x%040x", i))
			if err != nil || metadata.decimals != 6 || metadata.symbol != "TST" {
				t.Fatalf("working set evicted atpass%d token%d: %#v %v", pass, i, metadata, err)
			}
		}
		now = now.Add(time.Minute)
	}
	if unexpected.Load() != 0 || len(wallet.metadata) != 1000 || wallet.metadataBytes > walletTokenMetadataMaxBytes || walletTokenMetadataMaxInFlight != 512 || len(wallet.metadataInFlight) != 0 {
		t.Fatalf("cache/concurrency bound changed: reads=%d keys=%d bytes=%d flights=%d", unexpected.Load(), len(wallet.metadata), wallet.metadataBytes, len(wallet.metadataInFlight))
	}
}
