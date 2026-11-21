package marketcollector

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

type scanReceiptCache struct {
	receipts   map[string][]byte
	identities map[string]map[string]any
	bytes      int
}

// An entire block's selected set fits, or its receipts use the individual
// path. A block rejected here does not prevent a later smaller set fitting.
func (cache *scanReceiptCache) retain(receipts map[string][]byte, size int) bool {
	if len(receipts) > maxScanSelectedReceipts-len(cache.receipts) ||
		size > maxScanSelectedReceiptBytes-cache.bytes {
		return false
	}
	for hash, receipt := range receipts {
		cache.receipts[hash] = receipt
	}
	cache.bytes += size
	return true
}

type scanReceiptEncoders struct {
	writers sync.Pool
}

func (encoders *scanReceiptEncoders) pack(receipts map[string]hydratedMarketTransaction) (map[string][]byte, int) {
	if len(receipts) == 0 {
		return nil, 0
	}
	encoder, _ := encoders.writers.Get().(*flate.Writer)
	if encoder == nil {
		encoder, _ = flate.NewWriter(io.Discard, flate.BestSpeed)
	}
	defer func() {
		encoder.Reset(io.Discard)
		encoders.writers.Put(encoder)
	}()
	var buffer bytes.Buffer
	result := make(map[string][]byte, len(receipts))
	size := 0
	for hash, receipt := range receipts {
		buffer.Reset()
		encoder.Reset(&buffer)
		if _, err := encoder.Write(receipt.receiptPayload); err != nil {
			return nil, 0
		}
		if err := encoder.Close(); err != nil {
			return nil, 0
		}
		result[hash] = bytes.Clone(buffer.Bytes())
		size += len(result[hash])
	}
	return result, size
}

func (service *Service) consumeBatchedScanTransactions(ctx context.Context, hashes []string, headers map[uint64]blockHeader, expected map[string]receiptExpectation, cache *scanReceiptCache, consume func(context.Context, hydratedMarketTransaction) error) error {
	var cacheMu sync.Mutex
	return consumeOrderedMarketReads(ctx, len(hashes), service.settings.HydrationConcurrency,
		func(readCtx context.Context, index int) (hydratedMarketTransaction, error) {
			hash := hashes[index]
			expectation := expected[hash]
			header, exists := headers[expectation.blockNumber]
			if !exists {
				return hydratedMarketTransaction{}, fmt.Errorf("scanner receipt lacks a verified range header")
			}
			cacheMu.Lock()
			packed, identity := cache.receipts[hash], cache.identities[hash]
			delete(cache.receipts, hash)
			delete(cache.identities, hash)
			cache.bytes -= len(packed)
			cacheMu.Unlock()
			if packed == nil {
				return service.readMarketTransactionUsingIdentity(readCtx, hash, &header, []receiptExpectation{expectation}, identity, true)
			}
			return service.unpackScanReceipt(packed, header)
		}, func(writeCtx context.Context, _ int, hydrated hydratedMarketTransaction) error {
			return consume(writeCtx, hydrated)
		})
}

func (service *Service) unpackScanReceipt(packed []byte, header blockHeader) (hydratedMarketTransaction, error) {
	reader := flate.NewReader(bytes.NewReader(packed))
	defer reader.Close()
	payload, err := io.ReadAll(io.LimitReader(reader, maxBatchSelectedReceiptBytes+1))
	if err != nil {
		return hydratedMarketTransaction{}, fmt.Errorf("read cached market receipt: %w", err)
	}
	if len(payload) > maxBatchSelectedReceiptBytes {
		return hydratedMarketTransaction{}, fmt.Errorf("cached market receipt exceeds bounded capacity")
	}
	// Use the same exact-key decoding as prefetch, including when a provider
	// includes an extra field whose name differs only in case.
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return hydratedMarketTransaction{}, fmt.Errorf("decode cached market receipt: %w", err)
	}
	receipt, err := decodeReceiptObject(object, service.settings.ChainID)
	if err != nil {
		return hydratedMarketTransaction{}, fmt.Errorf("decode cached market receipt: %w", err)
	}
	return hydratedMarketTransaction{receipt: receipt, receiptPayload: payload, header: header}, nil
}
