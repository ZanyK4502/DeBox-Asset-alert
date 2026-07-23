package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/bot"
)

type fakeBotService struct {
	payload []byte
	result  bot.HandleResult
	err     error
}

func (f *fakeBotService) HandleWebhookPayload(
	_ context.Context,
	payload []byte,
) (bot.HandleResult, error) {
	f.payload = append([]byte(nil), payload...)
	return f.result, f.err
}

func TestBotWebhookStatusReflectsPollingConfiguration(t *testing.T) {
	cfg := testConfig(t)
	cfg.PublicAppURL = "https://example.test/"
	request := httptest.NewRequest(http.MethodGet, "/api/bot/webhook-status", nil)
	recorder := httptest.NewRecorder()
	New(cfg).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var body map[string]any
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["mode"] != "polling" || body["configured"] != false ||
		body["webhook_url"] != "https://example.test/bot/webhook" {
		t.Fatalf("unexpected status: %+v", body)
	}
}

func TestBotWebhookIsRejectedWhilePolling(t *testing.T) {
	cfg := testConfig(t)
	cfg.DeBoxWebhookKey = "unused"
	request := httptest.NewRequest(http.MethodPost, "/bot/webhook", strings.NewReader(`{}`))
	request.Header.Set("X-API-KEY", "unused")
	recorder := httptest.NewRecorder()
	New(cfg).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusConflict)
	}
}

func TestBotWebhookCompatibilityRouteRequiresKeyAndForwardsPayload(t *testing.T) {
	cfg := testConfig(t)
	cfg.ReceiveMode = "webhook"
	cfg.DeBoxWebhookKey = "secret"
	service := &fakeBotService{result: bot.HandleResult{OK: true, Kind: "message", UpdateID: 12}}
	handler := New(cfg, Dependencies{Bot: service})

	badRequest := httptest.NewRequest(http.MethodPost, "/bot/webhook", strings.NewReader(`{}`))
	badRequest.Header.Set("X-API-KEY", "wrong")
	badRecorder := httptest.NewRecorder()
	handler.ServeHTTP(badRecorder, badRequest)
	if badRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("bad key status = %d, want %d", badRecorder.Code, http.StatusUnauthorized)
	}

	payload := `{"id":12}`
	request := httptest.NewRequest(http.MethodPost, "/bot/webhook", strings.NewReader(payload))
	request.Header.Set("X-API-KEY", "secret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if string(service.payload) != payload {
		t.Fatalf("forwarded payload = %q, want %q", service.payload, payload)
	}
}

func TestBotWebhookSeparatesInvalidPayloadFromServiceFailure(t *testing.T) {
	cfg := testConfig(t)
	cfg.ReceiveMode = "webhook"
	cfg.DeBoxWebhookKey = "secret"
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "invalid payload", err: bot.ErrInvalidWebhookPayload, want: http.StatusBadRequest},
		{name: "service failure", err: errors.New("send failed"), want: http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeBotService{err: test.err}
			request := httptest.NewRequest(http.MethodPost, "/bot/webhook", strings.NewReader(`{}`))
			request.Header.Set("X-API-KEY", "secret")
			recorder := httptest.NewRecorder()
			New(cfg, Dependencies{Bot: service}).ServeHTTP(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
		})
	}
}
