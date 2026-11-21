package marketcollector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
)

type boundedMarketTransactionReader interface {
	MarketRPCTransactionByHash(context.Context, string, string, string) (map[string]any, error)
	MarketTransactionReceipt(context.Context, string, string, string) (map[string]any, error)
}

// A credit remains held while the serial consumer parses and persists this
// value. Two workers therefore never mean two prefetched values plus a third
// transaction already being processed.
type hydratedMarketTransaction struct {
	receipt        marketparse.Receipt
	receiptPayload json.RawMessage
	header         blockHeader
}

func (service *Service) readMarketTransaction(ctx context.Context, hash string, verifiedHeader *blockHeader, expected []receiptExpectation) (hydratedMarketTransaction, error) {
	return service.readMarketTransactionWithIdentity(ctx, hash, verifiedHeader, expected, nil)
}

func (service *Service) readMarketTransactionWithIdentity(ctx context.Context, hash string, verifiedHeader *blockHeader, expected []receiptExpectation, transaction map[string]any) (hydratedMarketTransaction, error) {
	return service.readMarketTransactionUsingIdentity(ctx, hash, verifiedHeader, expected, transaction, false)
}

func (service *Service) readMarketTransactionUsingIdentity(ctx context.Context, hash string, verifiedHeader *blockHeader, expected []receiptExpectation, transaction map[string]any, recheckCachedIdentity bool) (hydratedMarketTransaction, error) {
	readTransaction := service.chain.RPCTransactionByHash
	readReceipt := service.chain.TransactionReceipt
	if bounded, ok := service.chain.(boundedMarketTransactionReader); ok {
		readTransaction = bounded.MarketRPCTransactionByHash
		readReceipt = bounded.MarketTransactionReceipt
	}
	loadIdentity := func() (map[string]any, error) {
		object, err := readTransaction(ctx, hash, service.settings.ChainKey, service.settings.ChainFallback)
		if err != nil {
			return nil, fmt.Errorf("get market transaction %s: %w", hash, err)
		}
		return compactMarketTransactionIdentity(object)
	}
	hadCachedIdentity := transaction != nil
	if !hadCachedIdentity {
		var err error
		transaction, err = loadIdentity()
		if err != nil {
			return hydratedMarketTransaction{}, err
		}
	}
	receiptObject, err := readReceipt(ctx, hash, service.settings.ChainKey, service.settings.ChainFallback)
	if err != nil {
		return hydratedMarketTransaction{}, fmt.Errorf("get market receipt %s: %w", hash, err)
	}
	hydrated, err := service.hydrateMarketReceipt(ctx, hash, verifiedHeader, expected, transaction, receiptObject)
	var identityMismatch marketReceiptIdentityMismatch
	if recheckCachedIdentity && hadCachedIdentity && errors.As(err, &identityMismatch) {
		transaction, err = loadIdentity()
		if err != nil {
			return hydratedMarketTransaction{}, err
		}
		// The independent transaction lookup reuses the already read receipt.
		return service.hydrateMarketReceipt(ctx, hash, verifiedHeader, expected, transaction, receiptObject)
	}
	return hydrated, err
}

func (service *Service) hydrateMarketReceipt(ctx context.Context, hash string, verifiedHeader *blockHeader, expected []receiptExpectation, transaction, receiptObject map[string]any) (hydratedMarketTransaction, error) {
	receiptPayload, err := json.Marshal(receiptObject)
	if err != nil || len(receiptPayload) > 16<<20 {
		return hydratedMarketTransaction{}, fmt.Errorf("receipt exceeds bounded encoding capacity")
	}
	receipt, err := decodeReceiptObject(receiptObject, service.settings.ChainID)
	if err != nil {
		return hydratedMarketTransaction{}, fmt.Errorf("decode market receipt %s: %w", hash, err)
	}
	if err := validateHydratedReceipt(hash, transaction, receipt, expected); err != nil {
		return hydratedMarketTransaction{}, fmt.Errorf("validate market receipt: %w", err)
	}
	var header blockHeader
	if verifiedHeader != nil {
		header = *verifiedHeader
	} else {
		header, err = service.fetchBlockHeader(ctx, receipt.BlockNumber)
		if err != nil {
			return hydratedMarketTransaction{}, err
		}
	}
	if receipt.BlockNumber != header.Number || receipt.BlockHash != header.Hash || header.Timestamp.IsZero() {
		return hydratedMarketTransaction{}, fmt.Errorf("transaction receipt block hash is no longer canonical")
	}
	// The response object maps are task-local and are never retained in the
	// prefetch queue. Only the one persisted encoding and typed log view remain.
	return hydratedMarketTransaction{receipt: receipt, receiptPayload: receiptPayload, header: header}, nil
}

func compactMarketTransactionIdentity(transaction map[string]any) (map[string]any, error) {
	if transaction == nil {
		return nil, nil
	}
	// Retain only the inclusion fields used by the receipt validator.
	identity := make(map[string]any, 5)
	for _, field := range []string{"hash", "blockHash", "blockNumber", "transactionIndex", "from"} {
		text := rpcString(transaction, field)
		if len(text) > 80 {
			return nil, fmt.Errorf("transaction inclusion field exceeds bounded identity")
		}
		identity[field] = text
	}
	return identity, nil
}

type marketReadResult[T any] struct {
	index int
	value T
	err   error
}

func consumeOrderedMarketReads[T any](ctx context.Context, count, concurrency int, read func(context.Context, int) (T, error), consume func(context.Context, int, T) error) error {
	if count == 0 {
		return nil
	}
	if concurrency < 1 || concurrency > DefaultHydrationConcurrency {
		concurrency = DefaultHydrationConcurrency
	}
	if concurrency > count {
		concurrency = count
	}
	workCtx, cancel := context.WithCancel(ctx)
	credits := make(chan struct{}, concurrency)
	for range concurrency {
		credits <- struct{}{}
	}
	results := make(chan marketReadResult[T], concurrency)
	var jobs sync.Mutex
	next := 0
	var workers sync.WaitGroup
	workers.Add(concurrency)
	for range concurrency {
		go func() {
			defer workers.Done()
			var zero T
			for {
				if workCtx.Err() != nil {
					return
				}
				select {
				case <-workCtx.Done():
					return
				case <-credits:
				}
				jobs.Lock()
				index := next
				next++
				jobs.Unlock()
				if index >= count {
					credits <- struct{}{}
					return
				}
				value, err := read(workCtx, index)
				select {
				case results <- marketReadResult[T]{index: index, value: value, err: err}:
				case <-workCtx.Done():
					credits <- struct{}{}
					return
				}
				value = zero
				if err != nil {
					return
				}
			}
		}()
	}
	defer func() {
		cancel()
		workers.Wait()
	}()
	pending := make(map[int]marketReadResult[T], concurrency)
	for index := 0; index < count; index++ {
		for {
			if _, present := pending[index]; present {
				break
			}
			select {
			case <-workCtx.Done():
				return workCtx.Err()
			case result := <-results:
				pending[result.index] = result
			}
		}
		result := pending[index]
		delete(pending, index)
		if result.err != nil {
			return result.err
		}
		if err := workCtx.Err(); err != nil {
			return err
		}
		if err := consume(workCtx, index, result.value); err != nil {
			return err
		}
		var zero T
		result.value = zero
		credits <- struct{}{}
	}
	return nil
}
