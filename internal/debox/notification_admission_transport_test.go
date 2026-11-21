package debox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	boxbotapi "github.com/debox-pro/debox-chat-go-sdk/boxbotapi"
)

type messengerAdmissionBackend struct {
	mu                   sync.Mutex
	permits              map[string]notificationdelivery.AdmissionPermit
	next, consumed       int
	blocked              time.Time
	checkFail, blockFail bool
	observed             time.Duration
}

func (b *messengerAdmissionBackend) ReserveNotificationAdmission(_ context.Context, bot string, limit int) (notificationdelivery.AdmissionPermit, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.blocked.After(time.Now()) {
		return notificationdelivery.AdmissionPermit{}, &notificationdelivery.AdmissionDeferred{RetryAt: b.blocked, Reason: "server_retry_after"}
	}
	if len(b.permits) >= limit {
		return notificationdelivery.AdmissionPermit{}, &notificationdelivery.AdmissionDeferred{RetryAt: time.Now().Add(time.Minute), Reason: "full"}
	}
	if b.permits == nil {
		b.permits = map[string]notificationdelivery.AdmissionPermit{}
	}
	b.next++
	now := time.Now().UTC()
	p := notificationdelivery.AdmissionPermit{BotID: bot, Token: fmt.Sprint(b.next), GrantedAt: now, ExpiresAt: now.Add(time.Minute)}
	b.permits[p.Token] = p
	return p, nil
}
func (b *messengerAdmissionBackend) CheckNotificationAdmission(_ context.Context, p notificationdelivery.AdmissionPermit) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.checkFail {
		return errors.New("admission storage temporarily unavailable")
	}
	if b.blocked.After(time.Now()) {
		return &notificationdelivery.AdmissionDeferred{RetryAt: b.blocked, Reason: "server_retry_after"}
	}
	saved, ok := b.permits[p.Token]
	if !ok || saved.Consumed {
		return &notificationdelivery.AdmissionDeferred{RetryAt: time.Now().Add(time.Minute), Reason: "used"}
	}
	saved.Consumed = true
	b.permits[p.Token] = saved
	b.consumed++
	return nil
}
func (b *messengerAdmissionBackend) CheckNotificationHTTPAdmission(ctx context.Context, p notificationdelivery.AdmissionPermit, scopeHash string) error {
	if len(scopeHash) != 64 {
		return errors.New("invalid scope")
	}
	return b.CheckNotificationAdmission(ctx, p)
}
func (b *messengerAdmissionBackend) ReleaseNotificationAdmission(_ context.Context, p notificationdelivery.AdmissionPermit) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if saved := b.permits[p.Token]; !saved.Consumed {
		delete(b.permits, p.Token)
	}
	return nil
}
func (b *messengerAdmissionBackend) BlockNotificationAdmission(_ context.Context, _ string, delay time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.observed = delay
	if b.blockFail {
		return errors.New("quota observation unavailable")
	}
	if at := time.Now().Add(delay); at.After(b.blocked) {
		b.blocked = at
	}
	return nil
}

func attachMessengerAdmission(t *testing.T, messenger *Messenger, backend *messengerAdmissionBackend) {
	t.Helper()
	admission, err := notificationdelivery.NewAdmission(backend, messenger.Self().UserId, 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := messenger.SetNotificationAdmission(admission); err != nil {
		t.Fatal(err)
	}
}

func TestMessengerAdmissionSharesOrdinaryNotificationBudgetAndBypassesPollingEdits(t *testing.T) {
	var sends, edits, polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openapi/bot/getMe":
			_, _ = io.WriteString(w, `{"ok":true,"result":{"user_id":"same-bot"}}`)
		case "/openapi/v2/bot/sendMessage":
			sends.Add(1)
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":"accepted"}}`)
		case "/openapi/bot/editMessage":
			edits.Add(1)
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
		case "/openapi/bot/getUpdates":
			polls.Add(1)
			_, _ = io.WriteString(w, `{"ok":true,"result":[]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	first, err := NewMessenger("first-key", "fixture-secret", server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewMessenger("second-key", "fixture-secret", server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	backend := &messengerAdmissionBackend{}
	attachMessengerAdmission(t, first, backend)
	attachMessengerAdmission(t, second, backend)
	for i := range 5 {
		if i%2 == 0 {
			if _, err := first.Send(boxbotapi.NewMessage("fixture", "private", "normal reply")); err != nil {
				t.Fatal(err)
			}
		} else if _, err := second.SendNotificationContext(context.Background(), "fixture", "private", "frozen notification"); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := second.SendNotificationContext(ctx, "fixture", "private", "wait locally"); err == nil {
		t.Fatal("sixth request entered HTTP")
	} else if _, ok := notificationdelivery.AdmissionRetryAt(err); !ok {
		t.Fatalf("SDK/notification wrapper lost no-send proof: %v", err)
	}
	if _, err := first.GetUpdates(boxbotapi.NewUpdate(0)); err != nil {
		t.Fatal(err)
	}
	edit := boxbotapi.NewEditMessageText("fixture", "private", "old-message", "edited")
	if _, err := first.Send(edit); err != nil {
		t.Fatal(err)
	}
	if sends.Load() != 5 || backend.consumed != 5 || edits.Load() != 1 || polls.Load() != 1 {
		t.Fatalf("sends=%d consumed=%d edits=%d polls=%d", sends.Load(), backend.consumed, edits.Load(), polls.Load())
	}
	if err := first.SetNotificationAdmission(first.NotificationAdmission()); err == nil {
		t.Fatal("gate attached twice")
	}
}

func TestMessengerAdmissionObserverPreservesSuccessUnknownAndSDKResponse(t *testing.T) {
	for _, test := range []struct {
		name, body, header string
		status             int
		observerFail       bool
		wantKind           string
		wantDelay          time.Duration
	}{
		{"success despite observer failure", `{"ok":true,"result":{"message_id":"accepted"}}`, "90", 200, true, "", 90 * time.Second},
		{"unknown acceptance evidence stays unknown", `{"ok":false,"result":{"message_id":"maybe"},"parameters":{"retry_after":120}}`, "30", 429, false, notificationdelivery.Unknown, 120 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "getMe") {
					_, _ = io.WriteString(w, `{"ok":true,"result":{"user_id":"same-bot"}}`)
					return
				}
				calls.Add(1)
				w.Header().Set("Retry-After", test.header)
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			messenger, _ := NewMessenger("fixture-key", "fixture-secret", server.URL, server.Client())
			backend := &messengerAdmissionBackend{blockFail: test.observerFail}
			attachMessengerAdmission(t, messenger, backend)
			id, err := messenger.SendNotificationContext(context.Background(), "fixture", "private", "frozen")
			if test.wantKind == "" && (err != nil || id != "accepted") || test.wantKind != "" && (err == nil || notificationdelivery.Classify(err).Kind != test.wantKind) || calls.Load() != 1 || backend.observed != test.wantDelay {
				t.Fatalf("id=%s err=%v calls=%d observed=%s", id, err, calls.Load(), backend.observed)
			}
		})
	}
}

func TestMessengerAdmissionLatePreflightFailureAndCancelledPolicyPreserveNoSendProof(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "getMe") {
			_, _ = io.WriteString(w, `{"ok":true,"result":{"user_id":"same-bot"}}`)
			return
		}
		calls.Add(1)
	}))
	defer server.Close()
	messenger, _ := NewMessenger("fixture-key", "fixture-secret", server.URL, server.Client())
	backend := &messengerAdmissionBackend{checkFail: true}
	attachMessengerAdmission(t, messenger, backend)
	ctx, reservation, err := notificationdelivery.Acquire(context.Background(), messenger)
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.Release(context.Background())
	if _, err := messenger.SendNotificationContext(ctx, "fixture", "private", "frozen"); err == nil {
		t.Fatal("failed preflight reached transport")
	} else if _, ok := notificationdelivery.AdmissionRetryAt(err); !ok {
		t.Fatalf("preflight was incorrectly unknown: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = notificationdelivery.Send(cancelled, messenger, notificationdelivery.Payload{ChatID: "fixture", ChatType: "private", Text: "frozen"})
	if _, ok := notificationdelivery.AdmissionRetryAt(err); !ok || calls.Load() != 0 {
		t.Fatalf("cancelled policy lost no-send proof: %v calls=%d", err, calls.Load())
	}
}

func TestMessengerAdmissionOrdinaryV2ResponseCapReturnsUnknownWithoutRetry(t *testing.T) {
	body := `{"ok":true,"result":{"message_id":"accepted"},"padding":"` + strings.Repeat("x", maxNotificationResponseBytes+1024) + `"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "getMe") {
			_, _ = io.WriteString(w, `{"ok":true,"result":{"user_id":"same-bot"}}`)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	messenger, _ := NewMessenger("fixture-key", "fixture-secret", server.URL, server.Client())
	attachMessengerAdmission(t, messenger, &messengerAdmissionBackend{})
	message, err := messenger.Send(boxbotapi.NewMessage("fixture", "private", "ordinary"))
	if notificationdelivery.Classify(err).Kind != notificationdelivery.Unknown || message.MessageID != "" {
		t.Fatalf("oversized ordinary response was accepted: %+v %v", message, err)
	}
}

func TestMessengerAdmissionDoesNotReplayRedirectAndPreservesOriginalClient(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var sent, replayed atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/openapi/bot/getMe":
					_, _ = io.WriteString(w, `{"ok":true,"result":{"user_id":"same-bot"}}`)
				case "/openapi/v2/bot/sendMessage":
					sent.Add(1)
					http.Redirect(w, r, "/replayed", status)
				case "/original":
					http.Redirect(w, r, "/replayed", status)
				case "/replayed":
					replayed.Add(1)
					_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":"replayed"}}`)
				}
			}))
			defer server.Close()
			client := server.Client()
			messenger, _ := NewMessenger("fixture-key", "fixture-secret", server.URL, client)
			backend := &messengerAdmissionBackend{}
			attachMessengerAdmission(t, messenger, backend)
			if _, err := messenger.Send(boxbotapi.NewMessage("fixture", "private", "original")); err == nil || sent.Load() != 1 || replayed.Load() != 0 || backend.consumed != 1 {
				t.Fatalf("ordinary redirect bypassed admission: sends=%d replayed=%d consumed=%d err=%v", sent.Load(), replayed.Load(), backend.consumed, err)
			}
			if client.CheckRedirect != nil {
				t.Fatal("shared client was mutated")
			}
			response, err := client.Get(server.URL + "/original")
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if replayed.Load() != 1 {
				t.Fatal("original client redirect policy changed")
			}
		})
	}
}
