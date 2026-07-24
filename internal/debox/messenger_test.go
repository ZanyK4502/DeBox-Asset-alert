package debox

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMessengerSendsHTMLNotificationWithOfficialSDK(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.Header.Get("X-API-KEY") != "debox-key" {
			t.Errorf("X-API-KEY = %q", request.Header.Get("X-API-KEY"))
		}
		if request.Header.Get("nonce") == "" || request.Header.Get("timestamp") == "" ||
			request.Header.Get("signature") == "" || request.Header.Get("X-Request-Id") == "" {
			t.Errorf("missing signed SDK headers: %#v", request.Header)
		}
		switch request.URL.Path {
		case "/openapi/bot/getMe":
			_, _ = io.WriteString(writer, `{"ok":true,"result":{"user_id":"bot-1","name":"Monitor"}}`)
		case "/openapi/bot/sendMessage":
			if err := request.ParseForm(); err != nil {
				t.Errorf("ParseForm() error = %v", err)
			}
			chatType := request.Form.Get("chat_type")
			expectedID := map[string]string{"private": "user-1", "group": "group-1"}[chatType]
			if request.Form.Get("chat_id") != expectedID ||
				request.Form.Get("text") != "<b>Alert</b>" ||
				request.Form.Get("parse_mode") != "HTML" {
				encoded, _ := json.Marshal(request.Form)
				t.Errorf("unexpected message form: %s", encoded)
			}
			_, _ = io.WriteString(writer, `{"ok":true,"result":{"message_id":"message-`+chatType+`"}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	messenger, err := NewMessenger("debox-key", "debox-secret", server.URL, server.Client())
	if err != nil {
		t.Fatalf("NewMessenger() error = %v", err)
	}
	messageID, err := messenger.SendNotification("user-1", "private", "<b>Alert</b>")
	if err != nil {
		t.Fatalf("SendNotification() error = %v", err)
	}
	if messageID != "message-private" {
		t.Fatalf("private messageID = %q", messageID)
	}
	messageID, err = messenger.SendNotification("group-1", "group", "<b>Alert</b>")
	if err != nil {
		t.Fatalf("SendNotification(group) error = %v", err)
	}
	if messageID != "message-group" || requests != 3 {
		t.Fatalf("group messageID/requests = %q/%d", messageID, requests)
	}
}

func TestMessengerSendsNotificationWithURLAction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/openapi/bot/getMe":
			_, _ = io.WriteString(writer, `{"ok":true,"result":{"user_id":"bot-1","name":"Monitor"}}`)
		case "/openapi/bot/sendMessage":
			if err := request.ParseForm(); err != nil {
				t.Errorf("ParseForm() error = %v", err)
			}
			markup := request.Form.Get("reply_markup")
			if request.Form.Get("text") != "<b>Stage alert</b>" ||
				request.Form.Get("parse_mode") != "HTML" ||
				!strings.Contains(markup, "View all events") ||
				!strings.Contains(markup, "https://app.example/#aggregateEventsSection") {
				encoded, _ := json.Marshal(request.Form)
				t.Errorf("unexpected action message form: %s", encoded)
			}
			_, _ = io.WriteString(writer, `{"ok":true,"result":{"message_id":"message-action"}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	messenger, err := NewMessenger("debox-key", "debox-secret", server.URL, server.Client())
	if err != nil {
		t.Fatalf("NewMessenger() error = %v", err)
	}
	messageID, err := messenger.SendNotificationWithAction(
		"user-1",
		"private",
		"<b>Stage alert</b>",
		"View all events",
		"https://app.example/#aggregateEventsSection",
	)
	if err != nil {
		t.Fatalf("SendNotificationWithAction() error = %v", err)
	}
	if messageID != "message-action" {
		t.Fatalf("messageID = %q", messageID)
	}
}
