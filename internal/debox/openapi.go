package debox

import (
	"context"
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
	defaultOpenAPIBase    = "https://open.debox.pro"
	defaultOpenAPITimeout = 15 * time.Second
	maxErrorBodyLength    = 300
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type OpenAPIClient struct {
	apiKey     string
	baseURL    string
	httpClient HTTPDoer
}

func NewOpenAPIClient(apiKey, baseURL string, httpClient HTTPDoer) (*OpenAPIClient, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("DEBOX_BOT_API_KEY is required")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultOpenAPIBase
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultOpenAPITimeout}
	}
	return &OpenAPIClient{
		apiKey:     strings.TrimSpace(apiKey),
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		httpClient: httpClient,
	}, nil
}

func (c *OpenAPIClient) get(ctx context.Context, path string, params url.Values) (any, error) {
	endpoint := c.baseURL + "/" + strings.TrimLeft(path, "/")
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create DeBox OpenAPI request: %w", err)
	}
	request.Header.Set("X-API-KEY", c.apiKey)

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("DeBox OpenAPI request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusBadRequest {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyLength))
		return nil, fmt.Errorf("DeBox OpenAPI error %d: %s", response.StatusCode, string(detail))
	}

	var payload map[string]any
	decoder := json.NewDecoder(response.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("unexpected DeBox OpenAPI response: %w", err)
	}
	if payload == nil {
		return nil, fmt.Errorf("unexpected DeBox OpenAPI response")
	}
	if success, ok := payload["success"].(bool); ok && !success {
		message := firstString(payload, "message", "msg")
		if message == "" {
			message = fmt.Sprint(payload)
		}
		return nil, fmt.Errorf("%s", message)
	}
	if data, ok := payload["data"]; ok {
		return data, nil
	}
	return payload, nil
}

func (c *OpenAPIClient) UserInfo(
	ctx context.Context,
	userID, walletAddress string,
) (map[string]any, error) {
	params := url.Values{}
	if value := strings.TrimSpace(userID); value != "" {
		params.Set("user_id", value)
	}
	if value := strings.TrimSpace(walletAddress); value != "" {
		params.Set("address", value)
	}
	if len(params) == 0 {
		return nil, fmt.Errorf("user_id or wallet_address is required")
	}
	return c.getObject(ctx, "/openapi/user/info", params)
}

func (c *OpenAPIClient) TokenInfo(
	ctx context.Context,
	contractAddress string,
	chainID int64,
) (map[string]any, error) {
	params := url.Values{
		"contract_address": {strings.TrimSpace(contractAddress)},
		"chain_id":         {strconv.FormatInt(chainID, 10)},
	}
	return c.getObject(ctx, "/openapi/token/info", params)
}

func (c *OpenAPIClient) GroupInfo(ctx context.Context, gid string) (map[string]any, error) {
	if strings.TrimSpace(gid) == "" {
		return nil, fmt.Errorf("gid is required")
	}
	return c.getObject(ctx, "/openapi/group/info", url.Values{"gid": {strings.TrimSpace(gid)}})
}

func (c *OpenAPIClient) IsGroupJoined(
	ctx context.Context,
	gid, walletAddress string,
) (any, error) {
	if strings.TrimSpace(gid) == "" || strings.TrimSpace(walletAddress) == "" {
		return nil, fmt.Errorf("gid and wallet_address are required")
	}
	return c.get(ctx, "/openapi/group/is_join", url.Values{
		"gid":           {strings.TrimSpace(gid)},
		"walletAddress": {strings.TrimSpace(walletAddress)},
	})
}

func (c *OpenAPIClient) getObject(
	ctx context.Context,
	path string,
	params url.Values,
) (map[string]any, error) {
	value, err := c.get(ctx, path, params)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected DeBox OpenAPI response")
	}
	return object, nil
}

func firstString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}
