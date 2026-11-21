package chain

import (
	"net/http"
	"sync"
	"time"
)

// MaxWalletResponseBytes bounds individual wallet observations before their
// decoded values can enter a per-batch memo. It is a wire limit, not an RSS cap.
const MaxWalletResponseBytes int64 = 16 << 20

// NewWalletReadClient shares the original transport, attempt meter and limiter,
// while bounding wallet responses. The primary/payment client is not mutated.
func NewWalletReadClient(primary *Client) *WalletReadClient {
	if primary == nil {
		return nil
	}
	clone := *primary
	clone.maxResponseBytes = MaxWalletResponseBytes
	if client, ok := primary.httpClient.(*http.Client); ok {
		bounded := *client
		bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		clone.httpClient = &bounded
	}
	return &WalletReadClient{
		Client:           &clone,
		metadata:         make(map[walletTokenMetadataKey]walletTokenMetadata),
		metadataInFlight: make(map[walletTokenMetadataKey]*walletTokenMetadataRead),
		now:              time.Now,
	}
}

// WalletReadClient keeps wallet observations separate from the primary client
// used by payments. Only ERC-20 wallet balance reads use its RPC override.
// The metadata cache contains no wallet addresses or balances.
type WalletReadClient struct {
	*Client
	metadataMu       sync.Mutex
	metadata         map[walletTokenMetadataKey]walletTokenMetadata
	metadataInFlight map[walletTokenMetadataKey]*walletTokenMetadataRead
	metadataBytes    int
	now              func() time.Time
}

// WalletBalancePrefetchWorkers opts the monitor into a small fixed read pool.
// The transport, rate limiter and metadata cache are safe for these concurrent
// reads. Rule evaluation, database commits and delivery remain serial.
func (*WalletReadClient) WalletBalancePrefetchWorkers() int { return 2 }
