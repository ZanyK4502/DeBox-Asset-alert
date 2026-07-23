package debox

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAPIQueriesAndUnwrapsData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-API-KEY") != "debox-key" {
			t.Errorf("X-API-KEY = %q", request.Header.Get("X-API-KEY"))
		}
		switch request.URL.Path {
		case "/openapi/user/info":
			if request.URL.Query().Get("user_id") != "user-1" || request.URL.Query().Has("address") {
				t.Errorf("unexpected user query: %s", request.URL.RawQuery)
			}
			_, _ = io.WriteString(writer, `{"code":1,"success":true,"data":{"user_id":"user-1","name":"Alice"}}`)
		case "/openapi/token/info":
			if request.URL.Query().Get("contract_address") != "0xtoken" ||
				request.URL.Query().Get("chain_id") != "56" {
				t.Errorf("unexpected token query: %s", request.URL.RawQuery)
			}
			_, _ = io.WriteString(writer, `{"success":true,"data":{"symbol":"USDT","decimal":18}}`)
		case "/openapi/group/info":
			_, _ = io.WriteString(writer, `{"success":true,"data":{"gid":"group-1","group_name":"Builders"}}`)
		case "/openapi/group/is_join":
			if request.URL.Query().Get("walletAddress") != "0xwallet" {
				t.Errorf("unexpected group query: %s", request.URL.RawQuery)
			}
			_, _ = io.WriteString(writer, `{"code":1,"data":true}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := NewOpenAPIClient("debox-key", server.URL, server.Client())
	if err != nil {
		t.Fatalf("NewOpenAPIClient() error = %v", err)
	}
	user, err := client.UserInfo(context.Background(), "user-1", "")
	if err != nil || user["name"] != "Alice" {
		t.Fatalf("UserInfo() = %#v, %v", user, err)
	}
	token, err := client.TokenInfo(context.Background(), "0xtoken", 56)
	if err != nil || token["symbol"] != "USDT" {
		t.Fatalf("TokenInfo() = %#v, %v", token, err)
	}
	group, err := client.GroupInfo(context.Background(), "group-1")
	if err != nil || group["group_name"] != "Builders" {
		t.Fatalf("GroupInfo() = %#v, %v", group, err)
	}
	joined, err := client.IsGroupJoined(context.Background(), "group-1", "0xwallet")
	if err != nil || joined != true {
		t.Fatalf("IsGroupJoined() = %#v, %v", joined, err)
	}
}

func TestOpenAPIValidatesInputsAndErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/openapi/group/info" {
			_, _ = io.WriteString(writer, `{"success":false,"message":"No such group"}`)
			return
		}
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(writer, `{"message":"bad key"}`)
	}))
	defer server.Close()
	client, err := NewOpenAPIClient("debox-key", server.URL, server.Client())
	if err != nil {
		t.Fatalf("NewOpenAPIClient() error = %v", err)
	}
	if _, err := client.UserInfo(context.Background(), "", ""); err == nil {
		t.Fatal("UserInfo() accepted empty identity")
	}
	if _, err := client.GroupInfo(context.Background(), ""); err == nil {
		t.Fatal("GroupInfo() accepted empty gid")
	}
	if _, err := client.IsGroupJoined(context.Background(), "group", ""); err == nil {
		t.Fatal("IsGroupJoined() accepted empty wallet")
	}
	if _, err := client.GroupInfo(context.Background(), "missing"); err == nil ||
		!strings.Contains(err.Error(), "No such group") {
		t.Fatalf("GroupInfo() error = %v", err)
	}
	if _, err := client.TokenInfo(context.Background(), "0xtoken", 56); err == nil ||
		!strings.Contains(err.Error(), "DeBox OpenAPI error 401") {
		t.Fatalf("TokenInfo() error = %v", err)
	}
	if _, err := NewOpenAPIClient("", server.URL, nil); err == nil {
		t.Fatal("NewOpenAPIClient() accepted an empty API key")
	}
	defaultClient, err := NewOpenAPIClient("debox-key", "", nil)
	if err != nil {
		t.Fatalf("NewOpenAPIClient(defaults) error = %v", err)
	}
	httpClient, ok := defaultClient.httpClient.(*http.Client)
	if !ok || httpClient.Timeout != defaultOpenAPITimeout || defaultClient.baseURL != defaultOpenAPIBase {
		t.Fatalf("unexpected DeBox OpenAPI defaults: %#v", defaultClient)
	}
}
