package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
)

const (
	walletReadCacheKeys  = 200
	walletReadCacheBytes = 16 << 20
	walletReadMaxAge     = time.Minute
)

type WalletReadMetrics struct {
	Requests  int   `json:"source_reads"`
	Hits      int   `json:"hits"`
	Failures  int   `json:"failures"`
	Expired   int   `json:"expired"`
	NotCached int   `json:"not_cached"`
	Entries   int   `json:"entries"`
	Bytes     int64 `json:"bytes"`
}

type walletCycleReadsKey struct{}
type walletReadBaselineTimeKey struct{}
type walletReadKey struct {
	chainID                     int64
	kind, wallet, token, target string
	version                     string
}
type walletReadEntry struct {
	data      []byte
	err       error
	bytes     int
	fetchedAt time.Time
}

// Sequential and batch-local. Encoded immutable values bound retained payload
// and avoid returning shared mutable pointers/maps to individual rule checks.
type walletCycleReads struct {
	next           ChainService
	ctx            context.Context
	now            func() time.Time
	maxAge         time.Duration
	entries        map[walletReadKey]walletReadEntry
	metrics        WalletReadMetrics
	maxKeys        int
	maxBytes       int
	lastObservedAt *time.Time
	prefetch       map[walletReadKey]*walletPrefetchResult
}

type walletReadFailure struct{ cause error }

func (e *walletReadFailure) Error() string { return e.cause.Error() }
func (e *walletReadFailure) Unwrap() error { return e.cause }

func newWalletCycleReads(ctx context.Context, next ChainService, maxAge time.Duration) *walletCycleReads {
	if maxAge <= 0 || maxAge > walletReadMaxAge {
		maxAge = walletReadMaxAge
	}
	return &walletCycleReads{next: next, ctx: ctx, now: time.Now, maxAge: maxAge,
		entries: make(map[walletReadKey]walletReadEntry), maxKeys: walletReadCacheKeys, maxBytes: walletReadCacheBytes}
}

func (e *Executor) walletChain(ctx context.Context) ChainService {
	if reads, ok := ctx.Value(walletCycleReadsKey{}).(*walletCycleReads); ok {
		return reads
	}
	return e.deps.Chain
}

func walletMemoRead[T any](ctx context.Context, reads *walletCycleReads, key walletReadKey, fetch func() (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if err := reads.ctx.Err(); err != nil {
		return zero, err
	}
	if item, ok := reads.entries[key]; ok {
		age := reads.now().Sub(item.fetchedAt)
		baseline, _ := ctx.Value(walletReadBaselineTimeKey{}).(*time.Time)
		predatesBaseline := baseline != nil && item.fetchedAt.UTC().Truncate(time.Microsecond).Before(baseline.UTC().Truncate(time.Microsecond))
		if age >= 0 && age < reads.maxAge && !predatesBaseline {
			at := item.fetchedAt.UTC().Truncate(time.Microsecond)
			reads.lastObservedAt = &at
			reads.metrics.Hits++
			if item.err != nil {
				return zero, item.err
			}
			var value T
			if err := decodeWalletRead(item.data, &value); err != nil {
				return zero, fmt.Errorf("decode cached wallet read: %w", err)
			}
			return value, nil
		}
		delete(reads.entries, key)
		reads.metrics.Entries = len(reads.entries)
		reads.metrics.Bytes -= int64(item.bytes)
		reads.metrics.Expired++
	}
	// Start time is conservative: a slow request is not made young by its
	// response arrival or by a later consumer's observation commit time.
	fetchedAt := reads.now()
	var value T
	var err error
	var prepared *walletReadEntry
	if pending := reads.prefetch[key]; pending != nil && !pending.consumed {
		pending.consumed = true // only the serial consumer touches this flag
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-reads.ctx.Done():
			return zero, reads.ctx.Err()
		case <-pending.done:
		}
		pending.release()
		entry := pending.entry            // published before close(done), immutable afterward
		pending.entry = walletReadEntry{} // consumed payload is now owned by this read/memo
		baseline, _ := ctx.Value(walletReadBaselineTimeKey{}).(*time.Time)
		predatesBaseline := baseline != nil && entry.fetchedAt.UTC().Truncate(time.Microsecond).Before(baseline.UTC().Truncate(time.Microsecond))
		age := reads.now().Sub(entry.fetchedAt)
		if age >= 0 && age < reads.maxAge && !predatesBaseline {
			prepared = &entry
			fetchedAt = entry.fetchedAt
			err = entry.err
			if err == nil {
				err = decodeWalletRead(entry.data, &value)
				if err != nil {
					reads.metrics.Failures++
				}
			}
		} else {
			// A pre-read cannot move a later baseline backwards. Discard it and
			// perform a fresh read under the current rule's own context.
			reads.metrics.Expired++
		}
	}
	if prepared == nil {
		reads.metrics.Requests++
		fetchedAt = reads.now()
		value, err = fetch()
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if reads.ctx.Err() != nil {
		return zero, reads.ctx.Err()
	}
	if age := reads.now().Sub(fetchedAt); age < 0 || age >= reads.maxAge {
		if prepared == nil {
			reads.metrics.Failures++
		}
		return zero, &walletReadFailure{cause: errors.New("wallet source read exceeded observation age budget")}
	}
	at := fetchedAt.UTC().Truncate(time.Microsecond)
	reads.lastObservedAt = &at
	item := walletReadEntry{fetchedAt: fetchedAt}
	if err != nil {
		if prepared == nil {
			reads.metrics.Failures++
		}
		item.err = &walletReadFailure{cause: err}
		item.bytes = len(err.Error())
	} else if prepared != nil {
		item.data, item.bytes = prepared.data, prepared.bytes
	} else {
		item.data, err = json.Marshal(value)
		if err != nil {
			// Do not turn a successful source read into a failure just because it
			// cannot be cached. Existing consumers still receive its original value.
			reads.metrics.NotCached++
			return value, nil
		}
		item.bytes = len(item.data)
	}
	if len(reads.entries) < reads.maxKeys && item.bytes <= reads.maxBytes-int(reads.metrics.Bytes) {
		reads.entries[key] = item
		reads.metrics.Entries = len(reads.entries)
		reads.metrics.Bytes += int64(item.bytes)
	} else {
		reads.metrics.NotCached++
	}
	if item.err != nil {
		return zero, item.err
	}
	var owned T
	if err := decodeWalletRead(item.data, &owned); err != nil {
		return zero, fmt.Errorf("decode wallet read: %w", err)
	}
	return owned, nil
}

func decodeWalletRead(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(target)
}

func walletCanonicalKey(kind, address, tokenAddress, targetAddress, chainKey, fallback, version string) (walletReadKey, error) {
	tokenAddress, targetAddress = strings.TrimSpace(tokenAddress), strings.TrimSpace(targetAddress)
	profile, err := chain.ChainProfile(chainKey, fallback)
	if err != nil {
		return walletReadKey{}, &walletReadFailure{cause: err}
	}
	wallet, err := chain.ValidateAddress(address)
	if err != nil {
		return walletReadKey{}, &walletReadFailure{cause: err}
	}
	key := walletReadKey{chainID: profile.ChainID, kind: kind, wallet: wallet, version: version}
	if tokenAddress != "" {
		key.token, err = chain.ValidateAddress(tokenAddress)
		if err != nil {
			return walletReadKey{}, &walletReadFailure{cause: err}
		}
	}
	if targetAddress != "" {
		key.target, err = chain.ValidateAddress(targetAddress)
		if err != nil {
			return walletReadKey{}, &walletReadFailure{cause: err}
		}
	}
	return key, nil
}

func (r *walletCycleReads) Balance(ctx context.Context, address, token, chainKey, fallback string) (chain.BalanceResult, error) {
	key, err := walletBalanceKey(address, token, chainKey, fallback)
	if err != nil {
		return chain.BalanceResult{}, err
	}
	return walletMemoRead(ctx, r, key, func() (chain.BalanceResult, error) { return r.next.Balance(ctx, address, token, chainKey, fallback) })
}

func walletBalanceKey(address, token, chainKey, fallback string) (walletReadKey, error) {
	kind := "native_balance"
	version := "balance:v1"
	if strings.TrimSpace(token) != "" {
		kind = "token_balance"
		version = chain.WalletBalanceReadSource
	}
	return walletCanonicalKey(kind, address, token, "", chainKey, fallback, version)
}

func (r *walletCycleReads) TokenAllowance(ctx context.Context, owner, token, spender, chainKey, fallback string) (chain.AllowanceResult, error) {
	key, err := walletCanonicalKey("allowance", owner, token, spender, chainKey, fallback, "allowance:v1")
	if err != nil {
		return chain.AllowanceResult{}, err
	}
	return walletMemoRead(ctx, r, key, func() (chain.AllowanceResult, error) {
		return r.next.TokenAllowance(ctx, owner, token, spender, chainKey, fallback)
	})
}

type walletTransactionsReader interface {
	TransactionsByAccount(context.Context, string, string, string, int) ([]map[string]any, error)
}

func (r *walletCycleReads) LatestInteraction(ctx context.Context, address, target, chainKey, fallback string) (chain.InteractionResult, error) {
	key, err := walletCanonicalKey("interaction", address, "", target, chainKey, fallback, "interaction:v1:first-match:30")
	if err != nil {
		return chain.InteractionResult{}, err
	}
	if raw, ok := r.next.(walletTransactionsReader); ok {
		key.kind, key.target = "transactions_by_account", ""
		key.version = "rpp30:withLogs:true:withDecode:true:order:not_set:from:not_set:to:not_set:cursor:not_set:v1"
		transactions, err := walletMemoRead(ctx, r, key, func() ([]map[string]any, error) {
			transactions, err := raw.TransactionsByAccount(ctx, address, chainKey, fallback, 30)
			if err == nil && len(transactions) > 30 {
				return nil, errors.New("wallet transactions response exceeds requested 30 records")
			}
			return transactions, err
		})
		if err != nil {
			return chain.InteractionResult{}, err
		}
		return chain.MatchInteractionTransactions(transactions, address, target, chainKey, fallback)
	}
	return walletMemoRead(ctx, r, key, func() (chain.InteractionResult, error) {
		return r.next.LatestInteraction(ctx, address, target, chainKey, fallback)
	})
}

func walletReadCanDefer(err error) bool {
	var readFailure *walletReadFailure
	return errors.As(err, &readFailure)
}
