package chain

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	tokenHoldersOperation        = "token/getTokenHoldersByContract"
	maxTokenHoldersPageSize      = 100
	maxTokenHoldersResponseBytes = 1 << 20
)

type TokenHoldersOptions struct {
	Cursor    string
	RPP       int
	WithCount bool
}

type TokenHoldersPage struct {
	Page   *int64        `json:"page,omitempty"`
	RPP    int64         `json:"rpp"`
	Cursor string        `json:"cursor,omitempty"`
	Count  *int64        `json:"count,omitempty"`
	Items  []TokenHolder `json:"items"`
}

type TokenHolder struct {
	OwnerAddress      string     `json:"owner_address"`
	BalanceRaw        string     `json:"balance_raw"`
	LastTransferredAt *time.Time `json:"last_transferred_at,omitempty"`
}

func (c *Client) TokenHoldersByContract(
	ctx context.Context,
	tokenAddress, chainKey, fallback string,
	options TokenHoldersOptions,
) (TokenHoldersPage, error) {
	profile, err := ChainProfile(chainKey, fallback)
	if err != nil {
		return TokenHoldersPage{}, err
	}
	token, err := ValidateAddress(tokenAddress)
	if err != nil {
		return TokenHoldersPage{}, err
	}
	if options.RPP == 0 {
		options.RPP = maxTokenHoldersPageSize
	}
	if options.RPP < 1 || options.RPP > maxTokenHoldersPageSize {
		return TokenHoldersPage{}, fmt.Errorf(
			"holder results per page must be between 1 and %d",
			maxTokenHoldersPageSize,
		)
	}
	if len(options.Cursor) > 4096 {
		return TokenHoldersPage{}, fmt.Errorf("holder cursor exceeds byte budget")
	}
	payload := map[string]any{
		"contractAddress": token,
		"rpp":             options.RPP,
		"withCount":       options.WithCount,
	}
	if cursor := strings.TrimSpace(options.Cursor); cursor != "" {
		payload["cursor"] = cursor
	}
	// Only this small paginated endpoint uses the limit. Keep the shared CU
	// limiter and usage observer; do not change receipt/RPC response contracts.
	bounded := *c
	bounded.httpClient = holderPageHTTPDoer{next: c.httpClient}
	data, err := bounded.post(ctx, profile, tokenHoldersOperation, payload)
	if err != nil {
		return TokenHoldersPage{}, err
	}

	page := TokenHoldersPage{RPP: int64(options.RPP), Items: make([]TokenHolder, 0)}
	if raw, exists := data["rpp"]; exists && raw != nil {
		value, ok := optionalInt64(raw)
		if !ok || value < 1 || value > int64(options.RPP) {
			return TokenHoldersPage{}, fmt.Errorf("invalid token holder response page size")
		}
		page.RPP = value
	}
	if raw, exists := data["cursor"]; exists && raw != nil {
		value, ok := raw.(string)
		if !ok || len(value) > 4096 {
			return TokenHoldersPage{}, fmt.Errorf("invalid token holder response cursor")
		}
		page.Cursor = strings.TrimSpace(value)
	}
	for _, field := range []struct {
		name   string
		target **int64
	}{{"page", &page.Page}, {"count", &page.Count}} {
		if raw, exists := data[field.name]; exists && raw != nil {
			value, ok := optionalInt64(raw)
			if !ok || value < 0 {
				return TokenHoldersPage{}, fmt.Errorf("invalid token holder response %s", field.name)
			}
			*field.target = &value
		}
	}
	rawItems, ok := data["items"].([]any)
	if !ok || len(rawItems) > int(page.RPP) || len(rawItems) > options.RPP {
		return TokenHoldersPage{}, fmt.Errorf("missing or invalid token holder response items")
	}
	seen := make(map[string]struct{}, len(rawItems))
	if page.Count != nil && int64(len(rawItems)) > *page.Count {
		return TokenHoldersPage{}, fmt.Errorf("token holder items exceed reported holder count")
	}
	for _, rawItem := range rawItems {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return TokenHoldersPage{}, fmt.Errorf("unexpected token holder response")
		}
		owner, err := ValidateAddress(valueString(item["ownerAddress"]))
		if err != nil {
			return TokenHoldersPage{}, fmt.Errorf("invalid holder address from Nodit: %w", err)
		}
		if _, duplicate := seen[owner]; duplicate {
			return TokenHoldersPage{}, fmt.Errorf("duplicate address in token holder response")
		}
		seen[owner] = struct{}{}
		balance := strings.TrimSpace(valueString(item["balance"]))
		if len(balance) == 0 || len(balance) > 78 {
			return TokenHoldersPage{}, fmt.Errorf("invalid uint256 holder balance from Nodit")
		}
		for _, digit := range balance {
			if digit < '0' || digit > '9' {
				return TokenHoldersPage{}, fmt.Errorf("invalid uint256 holder balance from Nodit")
			}
		}
		number, valid := new(big.Int).SetString(balance, 10)
		if !valid || number.Sign() < 0 || number.BitLen() > 256 {
			return TokenHoldersPage{}, fmt.Errorf("invalid uint256 holder balance from Nodit")
		}
		balance = number.String()
		holder := TokenHolder{OwnerAddress: owner, BalanceRaw: balance}
		if transferredAt := strings.TrimSpace(valueString(item["lastTransferredAt"])); transferredAt != "" {
			parsed, err := time.Parse(time.RFC3339Nano, transferredAt)
			if err != nil {
				return TokenHoldersPage{}, fmt.Errorf("invalid holder transfer time from Nodit: %w", err)
			}
			holder.LastTransferredAt = &parsed
		}
		page.Items = append(page.Items, holder)
	}
	return page, nil
}

type holderPageHTTPDoer struct{ next HTTPDoer }

func (doer holderPageHTTPDoer) Do(request *http.Request) (*http.Response, error) {
	response, err := doer.next.Do(request)
	if err != nil {
		return response, err
	}
	if response == nil {
		return nil, fmt.Errorf("empty token holder HTTP response")
	}
	if response.Body == nil {
		return nil, fmt.Errorf("empty token holder response body")
	}
	if response.StatusCode >= http.StatusBadRequest {
		return response, nil
	}
	if response.ContentLength > maxTokenHoldersResponseBytes {
		_ = response.Body.Close()
		return nil, fmt.Errorf("token holder response exceeds byte budget")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxTokenHoldersResponseBytes+1))
	_ = response.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read token holder response: %w", err)
	}
	if len(body) > maxTokenHoldersResponseBytes {
		return nil, fmt.Errorf("token holder response exceeds byte budget")
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response, nil
}

func int64Value(value any) int64 {
	result, _ := strconv.ParseInt(valueString(value), 10, 64)
	return result
}

func optionalInt64(value any) (int64, bool) {
	if value == nil || strings.TrimSpace(valueString(value)) == "" {
		return 0, false
	}
	result, err := strconv.ParseInt(valueString(value), 10, 64)
	return result, err == nil
}
