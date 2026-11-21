package chain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

var ErrMarketBlockBatchUnsupported = errors.New("Nodit block batch is unsupported")

// MarketBlockAndReceipts requests two reads for the scanner's observed block.
// A failed pair leaves fallback policy to the caller.
func (c *Client) MarketBlockAndReceipts(ctx context.Context, number uint64, blockHash, key, fallback string) (map[string]any, []any, error) {
	var receipts []any
	full, err := c.marketBlockPair(ctx, number, blockHash, key, fallback, "eth_getBlockReceipts", blockHash, &receipts)
	if err != nil {
		return nil, nil, err
	}
	return full, receipts, nil
}

// MarketBlockAndTransactionReceipt pairs a full block with the one selected
// transaction receipt. A failed pair leaves fallback policy to the caller.
func (c *Client) MarketBlockAndTransactionReceipt(ctx context.Context, number uint64, blockHash, txHash, key, fallback string) (map[string]any, map[string]any, error) {
	txHash, err := ValidateTransactionHash(txHash)
	if err != nil {
		return nil, nil, err
	}
	blockHash, err = ValidateTransactionHash(blockHash)
	if err != nil {
		return nil, nil, err
	}
	var receipt map[string]any
	full, err := c.marketBlockPair(ctx, number, blockHash, key, fallback, "eth_getTransactionReceipt", txHash, &receipt)
	if err != nil {
		return nil, nil, err
	}
	if valueString(receipt["transactionHash"]) != txHash || valueString(receipt["blockHash"]) != blockHash ||
		valueString(receipt["blockNumber"]) != fmt.Sprintf("0x%x", number) {
		return nil, nil, errors.New("Nodit transaction receipt batch inclusion changed")
	}
	return full, receipt, nil
}

func (c *Client) marketBlockPair(ctx context.Context, number uint64, blockHash, key, fallback, receiptMethod, receiptID string, target any) (map[string]any, error) {
	profile, err := ChainProfile(key, fallback)
	if err != nil {
		return nil, err
	}
	if profile.Key != "base" {
		return nil, errors.New("Nodit block pair requires Base")
	}
	blockHash, err = ValidateTransactionHash(blockHash)
	if err != nil {
		return nil, err
	}
	const fullMethod = "eth_getBlockByNumber"
	if receiptMethod == "eth_getBlockReceipts" {
		receiptID = blockHash
	}
	height := fmt.Sprintf("0x%x", number)
	payload, err := json.Marshal([]map[string]any{
		{"jsonrpc": "2.0", "id": 1, "method": fullMethod, "params": []any{height, true}},
		{"jsonrpc": "2.0", "id": 2, "method": receiptMethod, "params": []any{receiptID}},
	})
	if err != nil {
		return nil, err
	}
	endpoint := c.rpcBaseURL
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://%s-%s.nodit.io", profile.Chain, profile.Network)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("invalid Nodit block batch request")
	}
	req.Header.Set("X-API-KEY", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	fullCU, receiptsCU := evmNodeCU(fullMethod), evmNodeCU(receiptMethod)
	if err := c.waitForCU(ctx, NoditUsage{CU: fullCU + receiptsCU}); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.observeUsage("evm_node", fullMethod, fullCU)
	c.observeUsage("evm_node", receiptMethod, receiptsCU)
	resp, err := c.boundedMarketClient().httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("Nodit block batch transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		c.cuLimiter.deferFor(retryDelay(resp.Header.Get("Retry-After"), 0, time.Now()))
	}
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusMethodNotAllowed, http.StatusUnsupportedMediaType:
		return nil, ErrMarketBlockBatchUnsupported
	case http.StatusOK:
	default:
		return nil, fmt.Errorf("Nodit block batch HTTP status %d", resp.StatusCode)
	}
	full, err := decodeMarketBlockPairInto(resp.Body, target)
	if err != nil {
		return nil, err
	}
	if valueString(full["hash"]) != blockHash || valueString(full["number"]) != height {
		return nil, errors.New("Nodit block batch inclusion changed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return full, nil
}

type marketBlockPairItem struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code int `json:"code"`
	} `json:"error"`
}

func (item marketBlockPairItem) unsupported() bool {
	return item.Version == "2.0" && item.Error != nil && (item.Error.Code == -32600 || item.Error.Code == -32601)
}

func decodeMarketBlockPairInto(body io.Reader, target any) (block map[string]any, err error) {
	const pairLimit = 2*maxMarketBlockReadResponseBytes + 3
	wire := &io.LimitedReader{R: body, N: pairLimit + 1}
	defer func() {
		if wire.N == 0 {
			block, err = nil, ErrMarketRPCResponseTooLarge
		}
	}()
	items := json.NewDecoder(wire)
	start, err := items.Token()
	if err != nil || start != json.Delim('[') {
		return nil, errors.New("expected a Nodit block batch response array")
	}
	var full map[string]any
	seen := make(map[string]bool, 2)
	for range 2 {
		var raw json.RawMessage
		if !items.More() || items.Decode(&raw) != nil {
			return nil, errors.New("expected two Nodit block batch results")
		}
		if int64(len(raw)) > maxMarketBlockReadResponseBytes {
			return nil, ErrMarketRPCResponseTooLarge
		}
		var item marketBlockPairItem
		if err := json.Unmarshal(raw, &item); err != nil || item.Version != "2.0" {
			return nil, errors.New("invalid Nodit block batch response object")
		}
		id := string(item.ID)
		if (id != "1" && id != "2") || seen[id] {
			return nil, errors.New("unexpected Nodit block batch response ID")
		}
		seen[id] = true
		if item.unsupported() {
			return nil, ErrMarketBlockBatchUnsupported
		}
		if item.Error != nil {
			return nil, errors.New("Nodit block batch RPC item failed")
		}
		decoder := json.NewDecoder(bytes.NewReader(item.Result))
		decoder.UseNumber()
		switch id {
		case "1":
			if err := decoder.Decode(&full); err != nil || full == nil {
				return nil, errors.New("invalid Nodit full block result")
			}
			if _, ok := full["transactions"].([]any); !ok {
				return nil, errors.New("invalid Nodit full block transactions")
			}
		case "2":
			if err := decoder.Decode(target); err != nil {
				return nil, errors.New("invalid Nodit batch receipt result")
			}
			switch receipts := target.(type) {
			case *[]any:
				if *receipts == nil {
					return nil, errors.New("invalid Nodit block receipts result")
				}
				for _, rawReceipt := range *receipts {
					if _, ok := rawReceipt.(map[string]any); !ok {
						return nil, errors.New("invalid Nodit block receipt object")
					}
				}
			case *map[string]any:
				if *receipts == nil {
					return nil, errors.New("invalid Nodit transaction receipt object")
				}
			}
		}
	}
	end, err := items.Token()
	if err != nil || end != json.Delim(']') || items.Decode(new(json.RawMessage)) != io.EOF {
		return nil, errors.New("unexpected data after two Nodit block batch results")
	}
	return full, nil
}
