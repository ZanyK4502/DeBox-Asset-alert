package debox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	boxbotapi "github.com/debox-pro/debox-chat-go-sdk/boxbotapi"
)

type compatHTTPClientFunc func(*http.Request) (*http.Response, error)

func (f compatHTTPClientFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

type compatTrackedBody struct {
	io.Reader
	reads  int
	closes int
}

func (b *compatTrackedBody) Read(p []byte) (int, error) {
	b.reads++
	return b.Reader.Read(p)
}

func (b *compatTrackedBody) Close() error { b.closes++; return nil }

type compatErrorReader struct{ err error }

func (r compatErrorReader) Read(p []byte) (int, error) {
	return copy(p, `{"ok":true,"result":true}`), r.err
}

func TestMessengerEditsExistingMessageWithoutResending(t *testing.T) {
	cases := []struct {
		name          string
		status        int
		response      string
		wantError     bool
		wantMessageID string
	}{
		{name: "boolean success", response: `{"ok":true,"success":true,"result":true}`},
		{name: "legacy boolean success", response: `{"ok":true,"result":true}`},
		{name: "message result", response: `{"ok":true,"result":{"message_id":"existing-message","text":"edited"}}`, wantMessageID: "existing-message"},
		{name: "false result", response: `{"ok":true,"result":false}`, wantError: true},
		{name: "business failure", response: `{"ok":false,"success":false,"error_code":400,"message":"edit rejected","result":false}`, wantError: true},
		{name: "contradictory success", response: `{"ok":true,"success":false,"result":true}`, wantError: true},
		{name: "missing ok", response: `{"success":true,"result":true}`, wantError: true},
		{name: "invalid JSON", response: `{"ok":true,"result":`, wantError: true},
		{name: "not found", status: http.StatusNotFound, response: `{"ok":true,"result":true}`, wantError: true},
		{name: "rate limited", status: http.StatusTooManyRequests, response: `{"ok":true,"result":true}`, wantError: true},
		{name: "server failure", status: http.StatusInternalServerError, response: `{"ok":true,"result":true}`, wantError: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			for _, chatType := range []string{"private", "group"} {
				t.Run(chatType, func(t *testing.T) {
					markup := boxbotapi.NewInlineKeyboardMarkup(boxbotapi.NewInlineKeyboardRow(
						boxbotapi.NewInlineKeyboardButtonData("Back", "alert:intro:zh"),
						boxbotapi.NewInlineKeyboardButtonURL("Dashboard", "https://app.example/#market"),
					))
					wantMarkup, err := json.Marshal(markup)
					if err != nil {
						t.Fatal(err)
					}
					var operationPaths []string
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/openapi/bot/getMe" {
							_, _ = io.WriteString(w, `{"ok":true,"result":{"user_id":"bot-1","name":"Monitor"}}`)
							return
						}
						operationPaths = append(operationPaths, r.URL.Path)
						if r.URL.Path != "/openapi/bot/editMessage" {
							http.NotFound(w, r)
							return
						}
						if r.Method != http.MethodPost || r.Header.Get("X-API-KEY") != "test-key" ||
							r.Header.Get("signature") == "" || r.Header.Get("X-Request-Id") == "" {
							t.Error("SDK request method or authentication headers changed")
						}
						if err := r.ParseForm(); err != nil {
							t.Error(err)
						}
						wantFields := map[string]string{
							"chat_id": "original-chat", "chat_type": chatType,
							"message_id": "existing-message", "text": "<b>Original menu content</b>",
							"parse_mode": "HTML", "reply_markup": string(wantMarkup),
						}
						if len(r.PostForm) != len(wantFields) {
							t.Errorf("unexpected request fields: %v", r.PostForm)
						}
						for key, want := range wantFields {
							if got := r.PostForm.Get(key); got != want {
								t.Errorf("%s = %q, want %q", key, got, want)
							}
						}
						if test.status != 0 {
							w.WriteHeader(test.status)
						}
						_, _ = io.WriteString(w, test.response)
					}))
					defer server.Close()
					messenger, err := NewMessenger("test-key", "test-secret", server.URL, server.Client())
					if err != nil {
						t.Fatal(err)
					}
					message := boxbotapi.NewEditMessageTextAndMarkup("original-chat", chatType, "existing-message", "<b>Original menu content</b>", markup)
					message.ParseMode = boxbotapi.ModeHTML
					sent, err := messenger.Send(message)
					if (err != nil) != test.wantError {
						t.Errorf("Send() error = %v, wantError %v", err, test.wantError)
					}
					if !test.wantError && sent.MessageID != test.wantMessageID {
						t.Errorf("MessageID = %q, want %q", sent.MessageID, test.wantMessageID)
					}
					if test.name == "business failure" && (err == nil || !strings.Contains(err.Error(), "edit rejected")) {
						t.Errorf("business error was not preserved: %v", err)
					}
					if want := []string{"/openapi/bot/editMessage"}; !reflect.DeepEqual(operationPaths, want) {
						t.Errorf("operation requests = %v, want exactly %v", operationPaths, want)
					}
				})
			}
		})
	}
}

func TestEditCompatibilityLeavesOtherRequestsAndHTTPFailuresUntouched(t *testing.T) {
	cases := []struct {
		method string
		path   string
		status int
	}{
		{http.MethodPost, "/openapi/bot/getMe", 200},
		{http.MethodPost, "/openapi/bot/sendMessage", 200},
		{http.MethodPost, "/openapi/bot/getUpdates", 200},
		{http.MethodPost, "/openapi/bot/editMessage", 200},
		{http.MethodGet, "/openapi/bot/editMessageText", 200},
		{http.MethodPost, "/openapi/bot/editMessageText/extra", 200},
		{http.MethodPost, "/openapi/bot/editMessageText", 429},
	}
	for _, test := range cases {
		t.Run(test.method+test.path+http.StatusText(test.status), func(t *testing.T) {
			request := httptest.NewRequest(test.method, "https://api.example"+test.path, nil)
			body := &compatTrackedBody{Reader: strings.NewReader(`{"ok":true,"result":true}`)}
			response := &http.Response{StatusCode: test.status, Body: body}
			calls := 0
			client := editMessageCompatClient{client: compatHTTPClientFunc(func(got *http.Request) (*http.Response, error) {
				calls++
				if test.status == 200 && got != request {
					t.Error("non-edit request was replaced")
				}
				return response, nil
			})}
			got, err := client.Do(request)
			if err != nil || got != response || calls != 1 || body.reads != 0 || body.closes != 0 {
				t.Fatalf("response/error/calls/reads/closes = %p/%v/%d/%d/%d", got, err, calls, body.reads, body.closes)
			}
			_ = got.Body.Close()
		})
	}
}

func TestEditCompatibilityPreservesRequestAndLegacyResponse(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "original-context")
	const oldURL = "https://api.example/prefix/openapi/bot/editMessageText?keep=yes"
	const originalForm = "chat_id=original&text=%3Cb%3Emenu%3C%2Fb%3E&reply_markup=%7B%7D"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, oldURL, strings.NewReader(originalForm))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-API-KEY", "test-key")
	request.Header.Set("signature", "original-signature")
	const originalResponse = " {\"ok\": true, \"result\": {\"message_id\":\"original\"}, \"unknown\": [1,2]} \n"
	body := &compatTrackedBody{Reader: strings.NewReader(originalResponse)}
	response := &http.Response{StatusCode: 200, Body: body, ContentLength: int64(len(originalResponse)), Header: http.Header{"X-Request-Id": {"trace-1"}}}
	calls := 0
	client := editMessageCompatClient{client: compatHTTPClientFunc(func(got *http.Request) (*http.Response, error) {
		calls++
		if got == request || got.URL.String() != "https://api.example/prefix/openapi/bot/editMessage?keep=yes" ||
			got.Context().Value(contextKey{}) != "original-context" || !reflect.DeepEqual(got.Header, request.Header) {
			t.Error("edit request was not cloned with its metadata preserved")
		}
		gotForm, err := io.ReadAll(got.Body)
		if err != nil || string(gotForm) != originalForm {
			t.Errorf("request body/error = %q/%v", gotForm, err)
		}
		return response, nil
	})}
	got, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	replayed, err := io.ReadAll(got.Body)
	if err != nil || string(replayed) != originalResponse || body.closes != 1 || calls != 1 ||
		request.URL.String() != oldURL || got.Header.Get("X-Request-Id") != "trace-1" {
		t.Fatalf("legacy response or request changed: body=%q error=%v closes=%d calls=%d url=%s", replayed, err, body.closes, calls, request.URL)
	}
}

func TestEditCompatibilityPreservesSuccessEnvelope(t *testing.T) {
	body := &compatTrackedBody{Reader: strings.NewReader(`{"ok":true,"success":true,"result":true,"trace":"keep","unknown":[1,2]}`)}
	response := &http.Response{StatusCode: 200, Body: body, Header: http.Header{"Retry-After": {"5"}}}
	client := editMessageCompatClient{client: compatHTTPClientFunc(func(*http.Request) (*http.Response, error) { return response, nil })}
	got, err := client.Do(httptest.NewRequest(http.MethodPost, "https://api.example/openapi/bot/editMessageText", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	data, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if string(envelope["ok"]) != "true" || string(envelope["success"]) != "true" || string(envelope["result"]) != "{}" ||
		string(envelope["trace"]) != `"keep"` || string(envelope["unknown"]) != "[1,2]" ||
		got.ContentLength != int64(len(data)) || got.Header.Get("Content-Length") == "" || got.Header.Get("Retry-After") != "5" || body.closes != 1 {
		t.Fatalf("unexpected adapted response: %s headers=%v length=%d closes=%d", data, got.Header, got.ContentLength, body.closes)
	}
}

func TestEditCompatibilityDoesNotRetryOrHideIOErrors(t *testing.T) {
	for _, readFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "transport", true: "response read"}[readFailure], func(t *testing.T) {
			failure := errors.New("test I/O failure")
			body := &compatTrackedBody{Reader: compatErrorReader{err: failure}}
			calls := 0
			client := editMessageCompatClient{client: compatHTTPClientFunc(func(*http.Request) (*http.Response, error) {
				calls++
				if readFailure {
					return &http.Response{StatusCode: 200, Body: body}, nil
				}
				return nil, failure
			})}
			_, err := client.Do(httptest.NewRequest(http.MethodPost, "https://api.example/openapi/bot/editMessageText", nil))
			if !errors.Is(err, failure) || calls != 1 || (readFailure && body.closes != 1) {
				t.Fatalf("error/calls/closes = %v/%d/%d", err, calls, body.closes)
			}
		})
	}
}
