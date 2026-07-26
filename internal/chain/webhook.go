package chain

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	NoditWebhookSignatureHeader = "x-signature"
	maxWebhookPageSize          = 100
)

type WebhookNotification struct {
	WebhookURL string `json:"webhookUrl"`
}

type WebhookCreateRequest struct {
	EventType    string              `json:"eventType"`
	Description  string              `json:"description,omitempty"`
	Notification WebhookNotification `json:"notification"`
	IsInstant    bool                `json:"isInstant"`
	Condition    map[string]any      `json:"condition"`
}

type WebhookUpdateRequest struct {
	IsActive     *bool                `json:"isActive,omitempty"`
	Description  *string              `json:"description,omitempty"`
	Notification *WebhookNotification `json:"notification,omitempty"`
	Condition    map[string]any       `json:"condition,omitempty"`
}

type WebhookSubscription struct {
	SubscriptionID   string              `json:"subscriptionId"`
	Description      string              `json:"description"`
	Protocol         string              `json:"protocol"`
	Network          string              `json:"network"`
	SubscriptionType string              `json:"subscriptionType"`
	EventType        string              `json:"eventType"`
	Notification     WebhookNotification `json:"notification"`
	SigningKey       string              `json:"signingKey,omitempty"`
	IsInstant        bool                `json:"isInstant"`
	IsActive         bool                `json:"isActive"`
	Condition        json.RawMessage     `json:"condition"`
	CreatedAt        *time.Time          `json:"createdAt,omitempty"`
	UpdatedAt        *time.Time          `json:"updatedAt,omitempty"`
}

type WebhookListOptions struct {
	SubscriptionID string
	Page           int
	RPP            int
}

type WebhookList struct {
	Total int64                 `json:"total"`
	Page  int64                 `json:"page"`
	RPP   int64                 `json:"rpp"`
	Items []WebhookSubscription `json:"items"`
}

func NewLogWebhookRequest(
	description, webhookURL, contractAddress string,
	topics []string,
	isInstant bool,
) (WebhookCreateRequest, error) {
	address, err := ValidateAddress(contractAddress)
	if err != nil {
		return WebhookCreateRequest{}, err
	}
	if len(topics) < 1 || len(topics) > 4 {
		return WebhookCreateRequest{}, fmt.Errorf("LOG webhook requires between 1 and 4 topics")
	}
	normalizedTopics := make([]string, len(topics))
	for index, topic := range topics {
		normalizedTopics[index], err = normalizeTopic(topic)
		if err != nil {
			return WebhookCreateRequest{}, err
		}
	}
	request := WebhookCreateRequest{
		EventType:    "LOG",
		Description:  strings.TrimSpace(description),
		Notification: WebhookNotification{WebhookURL: strings.TrimSpace(webhookURL)},
		IsInstant:    isInstant,
		Condition: map[string]any{
			"address": address,
			"topics":  normalizedTopics,
		},
	}
	if err := validateWebhookCreateRequest(request); err != nil {
		return WebhookCreateRequest{}, err
	}
	return request, nil
}

func (c *Client) CreateWebhook(
	ctx context.Context,
	chainKey, fallback string,
	input WebhookCreateRequest,
) (WebhookSubscription, error) {
	profile, err := ChainProfile(chainKey, fallback)
	if err != nil {
		return WebhookSubscription{}, err
	}
	if err := validateWebhookCreateRequest(input); err != nil {
		return WebhookSubscription{}, err
	}
	var result WebhookSubscription
	if err := c.doNoditJSON(
		ctx,
		http.MethodPost,
		c.webhookEndpoint(profile, ""),
		input,
		&result,
	); err != nil {
		return WebhookSubscription{}, err
	}
	if strings.TrimSpace(result.SubscriptionID) == "" {
		return WebhookSubscription{}, fmt.Errorf("unexpected Nodit webhook response")
	}
	return result, nil
}

func (c *Client) ListWebhooks(
	ctx context.Context,
	chainKey, fallback string,
	options WebhookListOptions,
) (WebhookList, error) {
	profile, err := ChainProfile(chainKey, fallback)
	if err != nil {
		return WebhookList{}, err
	}
	if options.Page < 0 {
		return WebhookList{}, fmt.Errorf("webhook page must not be negative")
	}
	if options.RPP < 0 || options.RPP > maxWebhookPageSize {
		return WebhookList{}, fmt.Errorf(
			"webhook results per page must be between 1 and %d",
			maxWebhookPageSize,
		)
	}
	endpoint, err := url.Parse(c.webhookEndpoint(profile, ""))
	if err != nil {
		return WebhookList{}, fmt.Errorf("create Nodit webhook request: %w", err)
	}
	query := endpoint.Query()
	if value := strings.TrimSpace(options.SubscriptionID); value != "" {
		query.Set("subscriptionId", value)
	}
	if options.Page > 0 {
		query.Set("page", strconv.Itoa(options.Page))
	}
	if options.RPP > 0 {
		query.Set("rpp", strconv.Itoa(options.RPP))
	}
	endpoint.RawQuery = query.Encode()
	var result WebhookList
	if err := c.doNoditJSON(ctx, http.MethodGet, endpoint.String(), nil, &result); err != nil {
		return WebhookList{}, err
	}
	return result, nil
}

func (c *Client) UpdateWebhook(
	ctx context.Context,
	chainKey, fallback, subscriptionID string,
	input WebhookUpdateRequest,
) error {
	profile, err := ChainProfile(chainKey, fallback)
	if err != nil {
		return err
	}
	if err := validateSubscriptionID(subscriptionID); err != nil {
		return err
	}
	if input.IsActive == nil && input.Description == nil &&
		input.Notification == nil && input.Condition == nil {
		return fmt.Errorf("webhook update must contain at least one change")
	}
	if input.Notification != nil {
		if err := validateWebhookURL(input.Notification.WebhookURL); err != nil {
			return err
		}
	}
	var response struct {
		Result bool `json:"result"`
	}
	if err := c.doNoditJSON(
		ctx,
		http.MethodPatch,
		c.webhookEndpoint(profile, subscriptionID),
		input,
		&response,
	); err != nil {
		return err
	}
	if !response.Result {
		return fmt.Errorf("Nodit rejected webhook update")
	}
	return nil
}

func (c *Client) DeleteWebhook(
	ctx context.Context,
	chainKey, fallback, subscriptionID string,
) error {
	profile, err := ChainProfile(chainKey, fallback)
	if err != nil {
		return err
	}
	if err := validateSubscriptionID(subscriptionID); err != nil {
		return err
	}
	var response struct {
		Result bool `json:"result"`
	}
	if err := c.doNoditJSON(
		ctx,
		http.MethodDelete,
		c.webhookEndpoint(profile, subscriptionID),
		nil,
		&response,
	); err != nil {
		return err
	}
	if !response.Result {
		return fmt.Errorf("Nodit rejected webhook deletion")
	}
	return nil
}

func VerifyWebhookSignature(rawBody []byte, signature, signingKey string) bool {
	signature = strings.TrimSpace(signature)
	signature = strings.TrimPrefix(signature, "sha256=")
	key := strings.TrimSpace(signingKey)
	if len(rawBody) == 0 || signature == "" || key == "" {
		return false
	}
	provided, err := hex.DecodeString(signature)
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write(rawBody)
	return hmac.Equal(provided, mac.Sum(nil))
}

func (c *Client) webhookEndpoint(profile Profile, subscriptionID string) string {
	endpoint := fmt.Sprintf(
		"%s/%s/%s/webhooks",
		c.baseURL,
		profile.Chain,
		profile.Network,
	)
	if subscriptionID != "" {
		endpoint += "/" + url.PathEscape(strings.TrimSpace(subscriptionID))
	}
	return endpoint
}

func (c *Client) doNoditJSON(
	ctx context.Context,
	method, endpoint string,
	input, output any,
) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode Nodit webhook request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("create Nodit webhook request: %w", err)
	}
	request.Header.Set("X-API-KEY", c.apiKey)
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("Nodit webhook request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusBadRequest {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyLength))
		return fmt.Errorf(
			"Nodit webhook API error %d: %s",
			response.StatusCode,
			strings.TrimSpace(string(detail)),
		)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	decoder := json.NewDecoder(response.Body)
	decoder.UseNumber()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("unexpected Nodit webhook response: %w", err)
	}
	return nil
}

func validateWebhookCreateRequest(input WebhookCreateRequest) error {
	if strings.TrimSpace(input.EventType) == "" {
		return fmt.Errorf("webhook event type is required")
	}
	if err := validateWebhookURL(input.Notification.WebhookURL); err != nil {
		return err
	}
	if input.Condition == nil {
		return fmt.Errorf("webhook condition is required")
	}
	return nil
}

func validateWebhookURL(value string) error {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return fmt.Errorf("webhook URL must be an absolute HTTP(S) URL")
	}
	return nil
}

func validateSubscriptionID(value string) error {
	if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "/?#") {
		return fmt.Errorf("invalid webhook subscription ID")
	}
	return nil
}
