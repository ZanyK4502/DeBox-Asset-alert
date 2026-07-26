package chain

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const maxTokenHoldersPageSize = 100

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
	payload := map[string]any{
		"contractAddress": token,
		"rpp":             options.RPP,
		"withCount":       options.WithCount,
	}
	if cursor := strings.TrimSpace(options.Cursor); cursor != "" {
		payload["cursor"] = cursor
	}
	data, err := c.post(ctx, profile, "token/getTokenHoldersByContract", payload)
	if err != nil {
		return TokenHoldersPage{}, err
	}

	page := TokenHoldersPage{
		RPP:    int64Value(data["rpp"]),
		Cursor: strings.TrimSpace(valueString(data["cursor"])),
		Items:  make([]TokenHolder, 0),
	}
	if page.RPP == 0 {
		page.RPP = int64(options.RPP)
	}
	if value, ok := optionalInt64(data["page"]); ok {
		page.Page = &value
	}
	if value, ok := optionalInt64(data["count"]); ok {
		page.Count = &value
	}
	rawItems, _ := data["items"].([]any)
	for _, rawItem := range rawItems {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return TokenHoldersPage{}, fmt.Errorf("unexpected token holder response")
		}
		owner, err := ValidateAddress(valueString(item["ownerAddress"]))
		if err != nil {
			return TokenHoldersPage{}, fmt.Errorf("invalid holder address from Nodit: %w", err)
		}
		balance := strings.TrimSpace(valueString(item["balance"]))
		if balance == "" {
			balance = "0"
		}
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
