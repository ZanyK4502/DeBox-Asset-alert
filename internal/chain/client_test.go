package chain

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testWallet = "0x1111111111111111111111111111111111111111"
	testToken  = "0x2222222222222222222222222222222222222222"
	testTarget = "0x3333333333333333333333333333333333333333"
	testHash   = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestNoditBalancesPreserveRequestAndResponseShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-API-KEY") != "nodit-key" || request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected headers: %#v", request.Header)
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		switch request.URL.Path {
		case "/bnb/mainnet/native/getNativeBalanceByAccount":
			if payload["accountAddress"] != testWallet {
				t.Errorf("accountAddress = %v", payload["accountAddress"])
			}
			_, _ = io.WriteString(writer, `{"balance":"1234500000000000000"}`)
		case "/ethereum/mainnet/token/getTokensOwnedByAccount":
			if payload["rpp"] != float64(1) {
				t.Errorf("rpp = %v", payload["rpp"])
			}
			_, _ = io.WriteString(writer, `{
				"items":[{
					"contract":{"address":"`+testToken+`","decimals":6,"symbol":"USDT"},
					"balance":"12345678"
				}]
			}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := NewClient("nodit-key", server.URL, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	native, err := client.NativeBalance(context.Background(), "0x"+strings.ToUpper(testWallet[2:]), "bsc", "")
	if err != nil {
		t.Fatalf("NativeBalance() error = %v", err)
	}
	if native.Value != "1.2345" || native.Symbol != "BNB" || native.TokenAddress != nil {
		t.Fatalf("unexpected native balance: %#v", native)
	}
	token, err := client.TokenBalance(context.Background(), testWallet, "0x"+strings.ToUpper(testToken[2:]), "ethereum", "")
	if err != nil {
		t.Fatalf("TokenBalance() error = %v", err)
	}
	if token.Value != "12.345678" || token.Symbol != "USDT" || token.Decimals != 6 ||
		token.TokenAddress == nil || *token.TokenAddress != testToken {
		t.Fatalf("unexpected token balance: %#v", token)
	}
}

func TestNoditRPCMethodsAndErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		switch payload.Method {
		case "eth_blockNumber":
			_, _ = io.WriteString(writer, `{"jsonrpc":"2.0","id":1,"result":"0x2a"}`)
		case "eth_getTransactionByHash":
			_, _ = io.WriteString(writer, `{"jsonrpc":"2.0","id":1,"result":{"hash":"`+testHash+`"}}`)
		case "eth_getTransactionReceipt":
			_, _ = io.WriteString(writer, `{"jsonrpc":"2.0","id":1,"result":null}`)
		default:
			_, _ = io.WriteString(writer, `{"jsonrpc":"2.0","id":1,"error":{"message":"method failed"}}`)
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
	block, err := client.LatestBlockNumber(context.Background(), "base", "")
	if err != nil || block != 42 {
		t.Fatalf("LatestBlockNumber() = %d, %v", block, err)
	}
	transaction, err := client.RPCTransactionByHash(context.Background(), testHash, "bsc", "")
	if err != nil || transaction["hash"] != testHash {
		t.Fatalf("RPCTransactionByHash() = %#v, %v", transaction, err)
	}
	receipt, err := client.TransactionReceipt(context.Background(), testHash, "bsc", "")
	if err != nil || receipt != nil {
		t.Fatalf("TransactionReceipt() = %#v, %v", receipt, err)
	}
	if _, err := client.rpc(context.Background(), profiles["bsc"], "unknown", nil); err == nil ||
		!strings.Contains(err.Error(), "method failed") {
		t.Fatalf("rpc error = %v", err)
	}
}

func TestNoditAllowanceTransactionsAndInteraction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/polygon/mainnet/token/getTokenAllowance":
			_, _ = io.WriteString(writer, `{
				"data":{"allowance":"2500000","token":{"decimal":"6","tokenSymbol":"USDC"}}
			}`)
		case "/polygon/mainnet/blockchain/getTransactionsByAccount":
			var payload map[string]any
			_ = json.NewDecoder(request.Body).Decode(&payload)
			if payload["rpp"] != float64(30) || payload["withLogs"] != true || payload["withDecode"] != true {
				t.Errorf("unexpected transactions payload: %#v", payload)
			}
			_, _ = io.WriteString(writer, `{
				"data":{"transactions":[
					{"transactionHash":"`+testHash+`","to":"`+strings.ToUpper(testTarget)+`"}
				]}
			}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := NewClient("nodit-key", server.URL, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	allowance, err := client.TokenAllowance(
		context.Background(), testWallet, testToken, testTarget, "polygon", "",
	)
	if err != nil {
		t.Fatalf("TokenAllowance() error = %v", err)
	}
	if allowance.Value != "2.5" || allowance.Raw != "2500000" || allowance.Symbol != "USDC" {
		t.Fatalf("unexpected allowance: %#v", allowance)
	}
	interaction, err := client.LatestInteraction(
		context.Background(), testWallet, testTarget, "polygon", "",
	)
	if err != nil {
		t.Fatalf("LatestInteraction() error = %v", err)
	}
	if !interaction.Matched || interaction.Cursor != testHash || interaction.Transaction == nil {
		t.Fatalf("unexpected interaction: %#v", interaction)
	}
}

func TestNoditRejectsHTTPAndResponseErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(writer, strings.Repeat("x", 400))
	}))
	defer server.Close()
	client, err := NewClient("nodit-key", server.URL, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = client.NativeBalance(context.Background(), testWallet, "bsc", "")
	if err == nil || !strings.Contains(err.Error(), "Nodit API error 502") {
		t.Fatalf("NativeBalance() error = %v", err)
	}
	if len(err.Error()) > 350 {
		t.Fatalf("error body was not truncated: %d characters", len(err.Error()))
	}
	if _, err := NewClient("", server.URL); err == nil {
		t.Fatal("NewClient() accepted an empty API key")
	}
	defaultClient, err := NewClient("nodit-key", "")
	if err != nil {
		t.Fatalf("NewClient(defaults) error = %v", err)
	}
	httpClient, ok := defaultClient.httpClient.(*http.Client)
	if !ok || httpClient.Timeout != defaultHTTPTimeout || defaultClient.baseURL != defaultNoditBaseURL {
		t.Fatalf("unexpected Nodit defaults: %#v", defaultClient)
	}
}
