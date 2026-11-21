// Package notificationdelivery describes delivery evidence and shared send
// admission, not detection or entitlement rules. Admission grants use a durable
// backend; its process-local deadline is only a bounded conservative hint.
package notificationdelivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	Retryable        = "retryable" // explicit rejection or proof the request was not sent
	Rejected         = "rejected"  // explicit, permanent rejection
	Unknown          = "unknown"   // may have been delivered; do not resend automatically
	MaxAttempts      = 5
	MaxPayloadBytes  = 64 << 10
	MaxSnapshotBytes = 2 << 20
	MaxDetailEvents  = 2000
	MaxDetailMembers = 200
	MaxBatch         = 200
)

var ErrCapacity = errors.New("notification payload exceeds bounded capacity")

type Failure struct {
	Kind       string
	Code       string
	RetryAfter time.Duration
	RequestID  string
	Cause      error
}

func (f *Failure) Error() string {
	trace := ""
	if f.RequestID != "" {
		trace = " [request_id=" + f.RequestID + "]"
	}
	if f.Cause != nil {
		return fmt.Sprintf("%s%s: %v", f.Code, trace, f.Cause)
	}
	return f.Code + trace
}
func (f *Failure) Unwrap() error { return f.Cause }

// Unclassified errors cannot prove that a receiver did not accept a message.
func Classify(err error) Failure {
	var failure *Failure
	if errors.As(err, &failure) && (failure.Kind == Retryable || failure.Kind == Rejected || failure.Kind == Unknown) {
		return *failure
	}
	return Failure{Kind: Unknown, Code: "delivery_result_unconfirmed", Cause: err}
}

func RetryDelay(attempt int, serverDelay time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 7 {
		attempt = 7
	}
	delay := 30 * time.Second * time.Duration(1<<uint(attempt-1))
	if delay > 30*time.Minute {
		delay = 30 * time.Minute
	}
	if serverDelay > delay {
		delay = serverDelay
	}
	return delay
}

// Payload is frozen once per logical notification. Retries use these exact
// values rather than today's rule, quotation, locale or public URL setting.
type Payload struct {
	ChatID     string `json:"chat_id"`
	ChatType   string `json:"chat_type"`
	Text       string `json:"text"`
	ActionText string `json:"action_text,omitempty"`
	ActionURL  string `json:"action_url,omitempty"`
}

func (p Payload) Validate() error {
	if len(p.ChatID)+len(p.ChatType)+len(p.Text)+len(p.ActionText)+len(p.ActionURL) > MaxPayloadBytes {
		return ErrCapacity
	}
	if strings.TrimSpace(p.ChatID) == "" || (p.ChatType != "private" && p.ChatType != "group") || strings.TrimSpace(p.Text) == "" ||
		(p.ActionURL == "") != (p.ActionText == "") {
		return errors.New("invalid frozen notification payload")
	}
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if len(body) > MaxPayloadBytes {
		return ErrCapacity
	}
	return nil
}

type Sender interface {
	SendNotification(string, string, string) (string, error)
}
type ActionSender interface {
	SendNotificationWithAction(string, string, string, string, string) (string, error)
}
type ContextSender interface {
	SendNotificationContext(context.Context, string, string, string) (string, error)
}
type ContextActionSender interface {
	SendNotificationWithActionContext(context.Context, string, string, string, string, string) (string, error)
}

// Send performs exactly one attempt. The durable worker owns all retries.
func Send(ctx context.Context, sender Sender, p Payload) (string, error) {
	if err := p.Validate(); err != nil {
		return "", &Failure{Kind: Rejected, Code: "invalid_frozen_payload", Cause: err}
	}
	if err := ctx.Err(); err != nil {
		if provider, ok := sender.(AdmissionProvider); ok && provider.NotificationAdmission() != nil {
			return "", cancelledAdmission(err)
		}
		return "", &Failure{Kind: Retryable, Code: "cancelled_before_send", Cause: err}
	}
	var id string
	var err error
	if p.ActionURL != "" {
		if s, ok := sender.(ContextActionSender); ok {
			id, err = s.SendNotificationWithActionContext(ctx, p.ChatID, p.ChatType, p.Text, p.ActionText, p.ActionURL)
		} else if s, ok := sender.(ActionSender); ok {
			id, err = s.SendNotificationWithAction(p.ChatID, p.ChatType, p.Text, p.ActionText, p.ActionURL)
		} else if s, ok := sender.(ContextSender); ok {
			id, err = s.SendNotificationContext(ctx, p.ChatID, p.ChatType, p.Text)
		} else {
			id, err = sender.SendNotification(p.ChatID, p.ChatType, p.Text)
		}
	} else if s, ok := sender.(ContextSender); ok {
		id, err = s.SendNotificationContext(ctx, p.ChatID, p.ChatType, p.Text)
	} else {
		id, err = sender.SendNotification(p.ChatID, p.ChatType, p.Text)
	}
	if err != nil {
		return id, err
	}
	if strings.TrimSpace(id) == "" {
		return "", &Failure{Kind: Unknown, Code: "success_without_message_id"}
	}
	return id, nil
}
