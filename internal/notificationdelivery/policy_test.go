package notificationdelivery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type recordingSender struct {
	calls   int
	payload Payload
	id      string
	err     error
}

func (s *recordingSender) SendNotification(chat, kind, text string) (string, error) {
	s.calls++
	s.payload = Payload{ChatID: chat, ChatType: kind, Text: text}
	return s.id, s.err
}
func (s *recordingSender) SendNotificationWithAction(chat, kind, text, action, url string) (string, error) {
	s.calls++
	s.payload = Payload{ChatID: chat, ChatType: kind, Text: text, ActionText: action, ActionURL: url}
	return s.id, s.err
}

func TestSingleAttemptUsesExactFrozenPayloadAndDoesNotGuessUnclassifiedFailure(t *testing.T) {
	p := Payload{ChatID: "original-owner", ChatType: "private", Text: "<b>原来的正文</b>\nunchanged", ActionText: "原按钮", ActionURL: "https://example.invalid/#original-detail"}
	s := &recordingSender{id: "known-message"}
	if id, err := Send(context.Background(), s, p); err != nil || id != s.id || s.calls != 1 || s.payload != p {
		t.Fatalf("frozen payload changed: %#v,%v", s, err)
	}
	s.err = errors.New("unclassified transport failure")
	_, err := Send(context.Background(), s, p)
	if s.calls != 2 || Classify(err).Kind != Unknown {
		t.Fatalf("failure retried or assumed definitely unsent: %#v,%v", s, err)
	}
	s.err = nil
	s.id = ""
	if _, err := Send(context.Background(), s, p); Classify(err).Kind != Unknown {
		t.Fatalf("empty id accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := s.calls
	if _, err := Send(ctx, s, p); !errors.Is(err, context.Canceled) || Classify(err).Kind != Retryable || s.calls != before {
		t.Fatalf("cancelled request was sent: %#v,%v", s, err)
	}
	if _, err := Send(context.Background(), s, Payload{ChatID: "x", ChatType: "private", Text: strings.Repeat("x", MaxPayloadBytes+1)}); !errors.Is(err, ErrCapacity) || s.calls != before {
		t.Fatalf("payload budget ignored: %v", err)
	}
}

func TestRetryDelayHonorsServerDelayAndPreservesFailureCause(t *testing.T) {
	if RetryDelay(1, 0) != 30*time.Second || RetryDelay(2, 0) != time.Minute || RetryDelay(100, 0) != 30*time.Minute || RetryDelay(1, 2*time.Hour) != 2*time.Hour {
		t.Fatal("retry schedule changed or server delay shortened")
	}
	cause := errors.New("original cause")
	err := &Failure{Kind: Retryable, Code: "rate_limited", Cause: cause, RequestID: "trace-1", RetryAfter: time.Minute}
	if !errors.Is(err, cause) || Classify(errors.Join(errors.New("context"), err)).RequestID != "trace-1" {
		t.Fatal("delivery evidence/cause lost")
	}
}
