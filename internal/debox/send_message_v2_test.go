package debox

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	boxbotapi "github.com/debox-pro/debox-chat-go-sdk/boxbotapi"
)

func TestSendMessageV2SignsEveryOrdinaryAndNotificationHTTPAndPreservesPayload(t *testing.T) {
	seen := map[string]bool{}
	var sends atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi/bot/getMe" {
			_, _ = io.WriteString(w, `{"ok":true,"result":{"user_id":"v2-bot"}}`)
			return
		}
		if r.URL.Path != sendMessageV2Path {
			t.Errorf("unexpected wire path=%s", r.URL.Path)
		}
		nonce, stamp := r.Header.Get("nonce"), r.Header.Get("timestamp")
		ms, err := strconv.ParseInt(stamp, 10, 64)
		if err != nil || len(stamp) != 13 || time.Since(time.UnixMilli(ms)).Abs() > time.Second || len(nonce) != 64 || seen[nonce] {
			t.Errorf("stale/reused signature timestamp or nonce")
		}
		seen[nonce] = true
		expected := sha1.Sum([]byte("fixture-secret" + nonce + stamp))
		if r.Header.Get("signature") != hex.EncodeToString(expected[:]) || r.Header.Get("X-API-KEY") != "fixture-key" {
			t.Errorf("incorrect v2 auth")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("encoding=%s", r.Header.Get("Content-Type"))
		}
		raw, _ := io.ReadAll(r.Body)
		if int64(len(raw)) != r.ContentLength {
			t.Errorf("content length=%d actual=%d", r.ContentLength, len(raw))
		}
		var body map[string]any
		if json.Unmarshal(raw, &body) != nil || body["content"] != "保持 <b>原文</b>" || body["chat_id"] != "target" || body["chat_type"] != "private" || body["parse_mode"] != "HTML" || body["text"] != nil {
			t.Errorf("changed payload=%s", raw)
		}
		if sends.Add(1) == 1 {
			markup, ok := body["reply_markup"].(map[string]any)
			if !ok || markup["inline_keyboard"] == nil {
				t.Errorf("markup not preserved as object")
			}
			if body["mention_type"] != float64(1) {
				t.Errorf("mention type=%v", body["mention_type"])
			}
			ids, ok := body["mention_ids"].([]any)
			if !ok || len(ids) != 1 || ids[0] != "user" {
				t.Errorf("mention IDs=%v", body["mention_ids"])
			}
		}
		_, _ = io.WriteString(w, `{"ok":true,"success":true,"result":{"message_id":"receipt","chat":null}}`)
	}))
	defer server.Close()
	m, err := NewMessenger("fixture-key", "fixture-secret", server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	message := boxbotapi.NewMessage("target", "private", "保持 <b>原文</b>")
	message.ParseMode = "HTML"
	message.MentionType = 1
	message.MentionIds = []string{"user"}
	message.ReplyMarkup = boxbotapi.NewInlineKeyboardMarkup(boxbotapi.NewInlineKeyboardRow(boxbotapi.NewInlineKeyboardButtonData("原按钮", "original")))
	if _, err = m.Send(message); err != nil {
		t.Fatal(err)
	}
	if _, err = m.SendNotificationContext(context.Background(), "target", "private", "保持 <b>原文</b>"); err != nil {
		t.Fatal(err)
	}
	if sends.Load() != 2 || len(seen) != 2 {
		t.Fatalf("HTTP sends=%d unique nonces=%d", sends.Load(), len(seen))
	}
}

type delayedSignatureBackend struct {
	messengerAdmissionBackend
	admittedAt atomic.Int64
}

func (b *delayedSignatureBackend) CheckNotificationHTTPAdmission(ctx context.Context, p notificationdelivery.AdmissionPermit, scope string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(20 * time.Millisecond):
	}
	if err := b.messengerAdmissionBackend.CheckNotificationHTTPAdmission(ctx, p, scope); err != nil {
		return err
	}
	b.admittedAt.Store(time.Now().UnixMilli())
	return nil
}

func TestSendMessageV2SignsAfterDurableAdmissionAndFailsClosedWithoutSecret(t *testing.T) {
	backend := &delayedSignatureBackend{}
	var sends atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi/bot/getMe" {
			_, _ = io.WriteString(w, `{"ok":true,"result":{"user_id":"same-bot"}}`)
			return
		}
		sends.Add(1)
		stamp, _ := strconv.ParseInt(r.Header.Get("timestamp"), 10, 64)
		if stamp < backend.admittedAt.Load() {
			t.Error("signature created before admission wait finished")
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":"sent"}}`)
	}))
	defer server.Close()
	if _, err := NewMessenger("key", "", server.URL, server.Client()); err == nil || sends.Load() != 0 {
		t.Fatal("missing secret accepted")
	}
	m, err := NewMessenger("key", "secret", server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	admission, err := notificationdelivery.NewAdmission(backend, "same-bot", 100)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.SetNotificationAdmission(admission); err != nil {
		t.Fatal(err)
	}
	if _, err = m.SendNotificationContext(context.Background(), "user", "private", "text"); err != nil {
		t.Fatal(err)
	}
	if sends.Load() != 1 {
		t.Fatalf("requests=%d", sends.Load())
	}
}

func TestSendMessageV2RejectsUnsupportedPayloadBeforeHTTPAndPreservesFailureClass(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi/bot/getMe" {
			_, _ = io.WriteString(w, `{"ok":true,"result":{"user_id":"same-bot"}}`)
			return
		}
		calls.Add(1)
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":"sent"}}`)
	}))
	defer server.Close()
	m, err := NewMessenger("key", "secret", server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.SendNotificationContext(context.Background(), "user", "private", strings.Repeat("界", 5000)); err != nil {
		t.Fatal(err)
	}
	if _, err = m.SendNotificationContext(context.Background(), "user", "private", strings.Repeat("界", 5001)); notificationdelivery.Classify(err).Kind != notificationdelivery.Rejected {
		t.Fatalf("long message error=%v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("invalid message entered HTTP=%d", calls.Load())
	}
	for _, body := range []string{`{"chat_id":"a","chat_type":"private","content":"x","unsupported":1}`, `{"chat_id":"a","chat_type":"private","content":"x","text":"y"}`} {
		req, _ := http.NewRequest(http.MethodPost, "http://local"+sendMessageV2Path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if _, err := sendMessageV2Body(req); err == nil {
			t.Fatal("silently changed unsupported/ambiguous fields")
		}
	}
}

func TestV2ReceiptExplicitTargetConflictsAreUnknown(t *testing.T) {
	for _, test := range []struct {
		name, result string
		unknown      bool
	}{
		{"null-chat", `{"message_id":"sent","chat":null}`, false},
		{"matched-flat", `{"message_id":"sent","chat_id":"user","chat_type":"private"}`, false},
		{"matched-chat", `{"message_id":"sent","chat":{"id":"user","type":"private"}}`, false},
		{"wrong-flat", `{"message_id":"sent","chat_id":"other"}`, true},
		{"wrong-type", `{"message_id":"sent","chat_type":"group"}`, true},
		{"wrong-chat", `{"message_id":"sent","chat":{"id":"other","type":"private"}}`, true},
		{"malformed-chat", `{"message_id":"sent","chat":false}`, true},
		{"numeric-target", `{"message_id":"sent","chat_id":123}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var sends atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "getMe") {
					_, _ = io.WriteString(w, `{"ok":true,"result":{"user_id":"same-bot"}}`)
					return
				}
				sends.Add(1)
				_, _ = io.WriteString(w, `{"ok":true,"success":true,"result":`+test.result+`}`)
			}))
			defer server.Close()
			m, err := NewMessenger("key", "secret", server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			id, err := m.SendNotificationContext(context.Background(), "user", "private", "text")
			if test.unknown {
				if notificationdelivery.Classify(err).Kind != notificationdelivery.Unknown || id != "" {
					t.Fatalf("misclassified=%s/%v", id, err)
				}
			} else if err != nil || id != "sent" {
				t.Fatalf("matched=%s/%v", id, err)
			}
			if sends.Load() != 1 {
				t.Fatalf("receipt caused resubmit=%d", sends.Load())
			}
		})
	}
}

type v2TrackedRequestBody struct {
	io.Reader
	closed *atomic.Int64
}

func (b v2TrackedRequestBody) Close() error { b.closed.Add(1); return nil }

func TestV2WireClosesOriginalAndCopiedRequestBodiesAndRebuildsReplay(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(strconv.FormatBool(invalid), func(t *testing.T) {
			raw := `{"chat_id":"user","chat_type":"private","content":"text"}`
			if invalid {
				raw = `{"chat_id":"user","chat_type":"private","content":"text","unexpected":true}`
			}
			var originalClosed, copyClosed, calls atomic.Int64
			req, _ := http.NewRequest(http.MethodPost, "http://local/openapi/bot/sendMessage", strings.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("timestamp", "old")
			req.Body = v2TrackedRequestBody{Reader: strings.NewReader(raw), closed: &originalClosed}
			req.GetBody = func() (io.ReadCloser, error) {
				return v2TrackedRequestBody{Reader: strings.NewReader(raw), closed: &copyClosed}, nil
			}
			client := sendMessageV2Client{secret: "secret", next: compatHTTPClientFunc(func(got *http.Request) (*http.Response, error) {
				calls.Add(1)
				body, _ := io.ReadAll(got.Body)
				_ = got.Body.Close()
				replay, _ := got.GetBody()
				again, _ := io.ReadAll(replay)
				_ = replay.Close()
				if string(body) != string(again) || got.ContentLength != int64(len(body)) || got.URL.Path != sendMessageV2Path {
					t.Error("rebuilt payload cannot replay correctly")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":"sent"}}`))}, nil
			})}
			response, err := client.Do(req)
			if response != nil {
				_ = response.Body.Close()
			}
			if invalid {
				if notificationdelivery.Classify(err).Kind != notificationdelivery.Rejected || calls.Load() != 0 {
					t.Fatalf("invalid payload=%v calls%d", err, calls.Load())
				}
			} else if err != nil || calls.Load() != 1 {
				t.Fatalf("valid=%v calls%d", err, calls.Load())
			}
			if originalClosed.Load() != 1 || copyClosed.Load() != 1 || req.Header.Get("timestamp") != "old" || req.URL.Path != "/openapi/bot/sendMessage" {
				t.Fatalf("original mutated/leaked close=%d copyclose=%d", originalClosed.Load(), copyClosed.Load())
			}
		})
	}
}
