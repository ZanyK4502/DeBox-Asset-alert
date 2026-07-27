package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/config"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketcollector"
)

type marketWebhookStub struct {
	chain    string
	category string
	body     string
}

func (stub *marketWebhookStub) AcceptWebhookForChain(
	_ context.Context,
	chainKey string,
	category string,
	_ map[string][]string,
	body []byte,
) (marketcollector.WebhookAcceptance, error) {
	stub.chain = chainKey
	stub.category = category
	stub.body = string(body)
	return marketcollector.WebhookAcceptance{InboxID: 42, Created: true}, nil
}

func TestMarketWebhookRoutesExplicitChain(t *testing.T) {
	stub := &marketWebhookStub{}
	handler := New(config.Config{}, Dependencies{MarketWebhook: stub})
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/market/webhook/base/transfer",
		strings.NewReader(`{"ok":true}`),
	)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || stub.chain != "base" ||
		stub.category != "transfer" {
		t.Fatalf(
			"explicit-chain webhook = status:%d chain:%q category:%q",
			response.Code,
			stub.chain,
			stub.category,
		)
	}
}

func TestMarketWebhookReturnsNoditCompatibleOK(t *testing.T) {
	stub := &marketWebhookStub{}
	handler := New(config.Config{}, Dependencies{MarketWebhook: stub})
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/market/webhook/v2-v3",
		strings.NewReader(`{"ok":true}`),
	)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.category != "v2-v3" || stub.body != `{"ok":true}` {
		t.Fatalf("webhook input = %q/%q", stub.category, stub.body)
	}
}

func TestMarketWebhookRejectsOversizedBodyBeforeService(t *testing.T) {
	stub := &marketWebhookStub{}
	handler := New(config.Config{}, Dependencies{MarketWebhook: stub})
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/market/webhook/transfer",
		strings.NewReader(strings.Repeat("x", marketcollector.MaxWebhookBodyBytes+1)),
	)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.category != "" {
		t.Fatal("oversized webhook reached service")
	}
}
