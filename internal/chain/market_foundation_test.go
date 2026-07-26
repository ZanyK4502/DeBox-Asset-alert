package chain

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testTopicA = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testTopicB = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestTokenHoldersByContractUsesCursorPagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/bnb/mainnet/token/getTokenHoldersByContract" {
			t.Errorf("path = %s", request.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if payload["contractAddress"] != testToken || payload["cursor"] != "next-cursor" ||
			payload["rpp"] != float64(100) || payload["withCount"] != true {
			t.Errorf("payload = %#v", payload)
		}
		_, _ = io.WriteString(writer, `{
			"rpp":100,
			"cursor":"following-cursor",
			"count":2,
			"items":[{
				"ownerAddress":"0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
				"balance":"123456789012345678901234567890",
				"lastTransferredAt":"2026-07-26T01:02:03.123456789Z"
			}]
		}`)
	}))
	defer server.Close()
	client, err := NewClient("nodit-key", server.URL, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	page, err := client.TokenHoldersByContract(
		context.Background(),
		testToken,
		"bsc",
		"",
		TokenHoldersOptions{Cursor: " next-cursor ", WithCount: true},
	)
	if err != nil {
		t.Fatalf("TokenHoldersByContract() error = %v", err)
	}
	if page.Cursor != "following-cursor" || page.Count == nil || *page.Count != 2 ||
		len(page.Items) != 1 ||
		page.Items[0].OwnerAddress != "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ||
		page.Items[0].BalanceRaw != "123456789012345678901234567890" ||
		page.Items[0].LastTransferredAt == nil ||
		page.Items[0].LastTransferredAt.Nanosecond() != 123456789 {
		t.Fatalf("unexpected holder page: %#v", page)
	}
	if _, err := client.TokenHoldersByContract(
		context.Background(),
		testToken,
		"bsc",
		"",
		TokenHoldersOptions{RPP: 101},
	); err == nil {
		t.Fatal("TokenHoldersByContract() accepted rpp over 100")
	}
}

func TestMarketRPCLogsAndBlocks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		switch payload.Method {
		case "eth_getLogs":
			filter := payload.Params[0].(map[string]any)
			if filter["fromBlock"] != "0xa" || filter["toBlock"] != "0x14" {
				t.Errorf("block filter = %#v", filter)
			}
			addresses, _ := filter["address"].([]any)
			if len(addresses) != 2 {
				t.Errorf("addresses = %#v", filter["address"])
			}
			topics, _ := filter["topics"].([]any)
			alternatives, _ := topics[0].([]any)
			if len(alternatives) != 2 || topics[1] != nil {
				t.Errorf("topics = %#v", topics)
			}
			_, _ = io.WriteString(writer, `{"jsonrpc":"2.0","id":1,"result":[{
				"address":"0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
				"topics":["0x`+strings.ToUpper(testTopicA[2:])+`"],
				"data":"0x01",
				"blockNumber":"0xa",
				"transactionHash":"0x`+strings.ToUpper(testHash[2:])+`",
				"transactionIndex":"0x1",
				"blockHash":"`+testTopicB+`",
				"logIndex":"0x2",
				"removed":false
			}]}`)
		case "eth_getBlockByNumber":
			if payload.Params[0] != "0x2a" || payload.Params[1] != true {
				t.Errorf("block params = %#v", payload.Params)
			}
			_, _ = io.WriteString(writer, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x2a"}}`)
		case "eth_getBlockByHash":
			if payload.Params[0] != testTopicB || payload.Params[1] != false {
				t.Errorf("block hash params = %#v", payload.Params)
			}
			_, _ = io.WriteString(writer, `{"jsonrpc":"2.0","id":1,"result":null}`)
		case "eth_call":
			call := payload.Params[0].(map[string]any)
			if call["to"] != testToken || payload.Params[1] != "latest" {
				t.Errorf("eth_call params = %#v", payload.Params)
			}
			var address string
			switch call["data"] {
			case "0x0dfe1681":
				address = testWallet
			case "0xd21220a7":
				address = testTarget
			default:
				t.Errorf("selector = %v", call["data"])
			}
			_, _ = io.WriteString(
				writer,
				`{"jsonrpc":"2.0","id":1,"result":"0x000000000000000000000000`+
					address[2:]+`"}`,
			)
		default:
			t.Errorf("method = %s", payload.Method)
		}
	}))
	defer server.Close()
	client, err := NewClient(
		"nodit-key",
		server.URL,
		WithHTTPClient(server.Client()),
		WithRPCBaseURL(server.URL),
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	from, to := uint64(10), uint64(20)
	logs, err := client.Logs(context.Background(), "bsc", "", LogFilter{
		FromBlock: &from,
		ToBlock:   &to,
		Addresses: []string{testWallet, testToken, "0x" + strings.ToUpper(testWallet[2:])},
		Topics:    []LogTopic{{testTopicA, testTopicB}, nil},
	})
	if err != nil {
		t.Fatalf("Logs() error = %v", err)
	}
	if len(logs) != 1 ||
		logs[0].Address != "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ||
		logs[0].Topics[0] != testTopicA || logs[0].TransactionHash != testHash {
		t.Fatalf("unexpected logs: %#v", logs)
	}
	block, err := client.BlockByNumber(context.Background(), 42, true, "bsc", "")
	if err != nil || block["number"] != "0x2a" {
		t.Fatalf("BlockByNumber() = %#v, %v", block, err)
	}
	block, err = client.BlockByHash(context.Background(), testTopicB, false, "bsc", "")
	if err != nil || block != nil {
		t.Fatalf("BlockByHash() = %#v, %v", block, err)
	}
	token0, token1, err := client.PoolTokens(
		context.Background(),
		testToken,
		"bsc",
		"",
	)
	if err != nil || token0 != testWallet || token1 != testTarget {
		t.Fatalf("PoolTokens() = %s/%s, %v", token0, token1, err)
	}
	if _, err := client.Logs(context.Background(), "bsc", "", LogFilter{
		FromBlock: &to,
		ToBlock:   &from,
	}); err == nil {
		t.Fatal("Logs() accepted reversed range")
	}
	if _, err := client.Logs(context.Background(), "bsc", "", LogFilter{
		FromBlock: &from,
		BlockHash: testTopicB,
	}); err == nil {
		t.Fatal("Logs() accepted block hash with range")
	}
}

func TestNoditWebhookLifecycleAndSignature(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-API-KEY") != "nodit-key" ||
			request.Header.Get("Accept") != "application/json" {
			t.Errorf("headers = %#v", request.Header)
		}
		calls = append(calls, request.Method+" "+request.URL.RequestURI())
		switch request.Method {
		case http.MethodPost:
			var payload WebhookCreateRequest
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Errorf("decode create: %v", err)
			}
			if payload.EventType != "LOG" || payload.IsInstant ||
				payload.Condition["address"] != testToken {
				t.Errorf("create payload = %#v", payload)
			}
			writer.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(writer, `{
				"subscriptionId":"4975",
				"eventType":"LOG",
				"signingKey":"signing-secret",
				"isActive":true,
				"createdAt":"2026-07-26T00:00:00Z"
			}`)
		case http.MethodGet:
			if request.URL.Query().Get("subscriptionId") != "4975" ||
				request.URL.Query().Get("page") != "1" ||
				request.URL.Query().Get("rpp") != "10" {
				t.Errorf("query = %s", request.URL.RawQuery)
			}
			_, _ = io.WriteString(writer, `{
				"total":1,"page":1,"rpp":10,
				"items":[{"subscriptionId":"4975","eventType":"LOG","isActive":true}]
			}`)
		case http.MethodPatch, http.MethodDelete:
			if request.URL.Path != "/bnb/mainnet/webhooks/4975" {
				t.Errorf("path = %s", request.URL.Path)
			}
			_, _ = io.WriteString(writer, `{"result":true}`)
		}
	}))
	defer server.Close()
	client, err := NewClient("nodit-key", server.URL, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	request, err := NewLogWebhookRequest(
		"pool swaps",
		"https://example.test/api/nodit/webhook",
		testToken,
		[]string{testTopicA},
		false,
	)
	if err != nil {
		t.Fatalf("NewLogWebhookRequest() error = %v", err)
	}
	created, err := client.CreateWebhook(context.Background(), "bsc", "", request)
	if err != nil || created.SubscriptionID != "4975" ||
		created.SigningKey != "signing-secret" || created.CreatedAt == nil {
		t.Fatalf("CreateWebhook() = %#v, %v", created, err)
	}
	list, err := client.ListWebhooks(context.Background(), "bsc", "", WebhookListOptions{
		SubscriptionID: "4975",
		Page:           1,
		RPP:            10,
	})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("ListWebhooks() = %#v, %v", list, err)
	}
	active := false
	if err := client.UpdateWebhook(
		context.Background(),
		"bsc",
		"",
		"4975",
		WebhookUpdateRequest{IsActive: &active},
	); err != nil {
		t.Fatalf("UpdateWebhook() error = %v", err)
	}
	if err := client.DeleteWebhook(context.Background(), "bsc", "", "4975"); err != nil {
		t.Fatalf("DeleteWebhook() error = %v", err)
	}
	if len(calls) != 4 {
		t.Fatalf("calls = %#v", calls)
	}

	body := []byte("The quick brown fox jumps over the lazy dog")
	signature := "f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8"
	if !VerifyWebhookSignature(body, signature, "key") ||
		!VerifyWebhookSignature(body, "sha256="+signature, "key") ||
		VerifyWebhookSignature(append(body, '!'), signature, "key") {
		t.Fatal("VerifyWebhookSignature() did not enforce HMAC-SHA256")
	}
}

func TestWebhookValidation(t *testing.T) {
	if _, err := NewLogWebhookRequest("", "not-a-url", testToken, []string{testTopicA}, false); err == nil {
		t.Fatal("NewLogWebhookRequest() accepted invalid URL")
	}
	if _, err := NewLogWebhookRequest("", "https://example.test", testToken, nil, false); err == nil {
		t.Fatal("NewLogWebhookRequest() accepted empty topics")
	}
	if _, err := NewLogWebhookRequest(
		"",
		"https://example.test",
		testToken,
		[]string{"invalid"},
		false,
	); err == nil {
		t.Fatal("NewLogWebhookRequest() accepted invalid topic")
	}
	if VerifyWebhookSignature(nil, "", "") {
		t.Fatal("VerifyWebhookSignature() accepted empty input")
	}
}

func TestHolderTransferTimeUsesUTCInstant(t *testing.T) {
	value := "2026-07-26T09:00:00+08:00"
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || !parsed.Equal(time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC)) {
		t.Fatalf("time.Parse() = %v, %v", parsed, err)
	}
}
