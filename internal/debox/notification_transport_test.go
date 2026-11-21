package debox

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
)

func TestNotificationTransportRequiresActualAcknowledgementAndPreservesRejectionEvidence(t *testing.T) {
	for _, test := range []struct {
		name              string
		status            int
		body, after, want string
		delay             time.Duration
	}{
		{"confirmed", 200, `{"ok":true,"success":true,"result":{"message_id":"accepted-1"}}`, "", "", 0},
		{"legacy success envelope", 200, `{"ok":true,"result":{"message_id":"accepted-1"}}`, "", "", 0},
		{"business failure", 200, `{"ok":false,"success":false,"message":"bot not joined"}`, "", notificationdelivery.Rejected, 0},
		{"energy account temporarily unavailable", 200, `{"ok":false,"success":false,"message":"Energy account is not ready"}`, "", notificationdelivery.Retryable, 0},
		{"energy balance lookup temporarily unavailable", 200, `{"ok":false,"success":false,"message":"Failed to retrieve energy balance"}`, "", notificationdelivery.Retryable, 0},
		{"energy account error with message id", 200, `{"ok":false,"success":false,"message":"Energy account is not ready","result":{"message_id":"maybe-sent"}}`, "", notificationdelivery.Unknown, 0},
		{"energy lookup error with message id", 200, `{"ok":false,"success":false,"message":"Failed to retrieve energy balance","result":{"message_id":"maybe-sent"}}`, "", notificationdelivery.Unknown, 0},
		{"insufficient energy remains permanent", 200, `{"ok":false,"success":false,"message":"Insufficient energy: sending a message requires 0.12 energy"}`, "", notificationdelivery.Rejected, 0},
		{"invalid signature remains permanent", 200, `{"ok":false,"success":false,"message":"Invalid request signature. Check App Secret"}`, "", notificationdelivery.Rejected, 0},
		{"busy with server delay", 200, `{"ok":false,"success":false,"message":"The system is busy, please try again later!"}`, "120", notificationdelivery.Retryable, 2 * time.Minute},
		{"pure text rate limit", 429, "Too many requests", "90", notificationdelivery.Retryable, 90 * time.Second},
		{"json rate limit parameter delay", 429, `{"ok":false,"parameters":{"retry_after":120}}`, "90", notificationdelivery.Retryable, 2 * time.Minute},
		{"rate limit with accepted id", 429, `{"ok":false,"result":{"message_id":"maybe-sent"}}`, "90", notificationdelivery.Unknown, 0},
		{"http rejection with success flag", 403, `{"ok":true,"result":{}}`, "", notificationdelivery.Unknown, 0},
		{"missing message id", 200, `{"ok":true,"success":true,"result":{}}`, "", notificationdelivery.Unknown, 0},
		{"contradictory flags", 200, `{"ok":true,"success":false,"result":{"message_id":"maybe-sent"}}`, "", notificationdelivery.Unknown, 0},
		{"rejected with message id", 200, `{"ok":false,"success":false,"result":{"message_id":"maybe-sent"},"message":"busy"}`, "30", notificationdelivery.Unknown, 0},
		{"rejected with numeric id", 200, `{"ok":false,"result":{"message_id":123},"message":"busy"}`, "30", notificationdelivery.Unknown, 0},
		{"malformed accepted response", 200, "{", "", notificationdelivery.Unknown, 0},
		{"gateway timeout", 504, "timeout", "", notificationdelivery.Unknown, 0},
		{"http rejection", 403, "forbidden", "", notificationdelivery.Rejected, 0},
		{"over-budget response", 200, strings.Repeat("x", maxNotificationResponseBytes+1), "", notificationdelivery.Unknown, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "getMe") {
					_, _ = io.WriteString(w, `{"ok":true,"result":{"user_id":"fixture-bot"}}`)
					return
				}
				calls++
				if r.URL.Path != "/openapi/v2/bot/sendMessage" {
					t.Errorf("notification changed original route: %s", r.URL.Path)
				}
				if test.after != "" {
					w.Header().Set("Retry-After", test.after)
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			messenger, err := NewMessenger("fixture-key", "fixture-secret", server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			id, err := messenger.SendNotificationContext(context.Background(), "fixture-user", "private", "<b>Frozen notification</b>")
			if calls != 1 {
				t.Fatalf("transport retried internally: %d", calls)
			}
			if test.want == "" {
				if err != nil || id != "accepted-1" {
					t.Fatalf("acknowledged=%q,%v", id, err)
				}
				return
			}
			if err == nil {
				t.Fatal("unconfirmed/rejected send was accepted")
			}
			failure := notificationdelivery.Classify(err)
			if failure.Kind != test.want || failure.RetryAfter != test.delay || failure.RequestID == "" {
				t.Fatalf("failure=%#v err=%v", failure, err)
			}
		})
	}
}

func TestNotificationTransportDoesNotReplayRedirectOrChangeSharedClient(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var sent, replayed atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/openapi/bot/getMe":
					_, _ = io.WriteString(w, `{"ok":true,"result":{"user_id":"fixture-bot"}}`)
				case "/openapi/v2/bot/sendMessage":
					sent.Add(1)
					http.Redirect(w, r, "/replayed", status)
				case "/original-client":
					http.Redirect(w, r, "/replayed", status)
				case "/replayed":
					replayed.Add(1)
					_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":"redirected"}}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client := server.Client()
			messenger, err := NewMessenger("fixture-key", "fixture-secret", server.URL, client)
			if err != nil {
				t.Fatal(err)
			}
			_, err = messenger.SendNotificationWithActionContext(context.Background(), "fixture-user", "private", "Original frozen body", "Details", "https://example.test/details")
			if err == nil || notificationdelivery.Classify(err).Kind != notificationdelivery.Unknown || sent.Load() != 1 || replayed.Load() != 0 {
				t.Fatalf("redirect replayed notification: sent=%d replayed=%d err=%v", sent.Load(), replayed.Load(), err)
			}
			if client.CheckRedirect != nil {
				t.Fatal("notification mutated the original client redirect policy")
			}
			response, err := client.Get(server.URL + "/original-client")
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if replayed.Load() != 1 {
				t.Fatal("original client no longer follows its existing redirect policy")
			}
		})
	}
}

func TestNotificationTransportTimeoutAfterRequestIsUnknownAndNeverResends(t *testing.T) {
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "getMe") {
			_, _ = io.WriteString(w, `{"ok":true,"result":{"user_id":"fixture-bot"}}`)
			return
		}
		started <- struct{}{}
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	messenger, err := NewMessenger("fixture-key", "fixture-secret", server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = messenger.SendNotificationContext(ctx, "fixture-user", "private", "Original body")
	if !errors.Is(err, context.DeadlineExceeded) || notificationdelivery.Classify(err).Kind != notificationdelivery.Unknown {
		t.Fatalf("accepted request timeout treated as unsent: %v", err)
	}
	if len(started) != 1 {
		t.Fatalf("expected exactly one accepted attempt, got %d", len(started))
	}
}
