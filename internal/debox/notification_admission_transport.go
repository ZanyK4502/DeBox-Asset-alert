package debox

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	boxbotapi "github.com/debox-pro/debox-chat-go-sdk/boxbotapi"
)

// All ordinary and notification sendMessage requests cross this one boundary.
// Polling, getMe and edits retain their existing client and wire behavior.
type notificationAdmissionHTTPClient struct {
	next      boxbotapi.HTTPClient
	admission *notificationdelivery.Admission
	scopeHash string
}

func (c notificationAdmissionHTTPClient) Do(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodPost || (!strings.HasSuffix(request.URL.Path, "/openapi/bot/sendMessage") && !strings.HasSuffix(request.URL.Path, sendMessageV2Path)) {
		return c.next.Do(request)
	}
	ctx, cancel := context.WithTimeout(request.Context(), defaultBotTimeout)
	cancelOnReturn := true
	defer func() {
		if cancelOnReturn {
			cancel()
		}
	}()
	permitted, err := c.admission.BeforeHTTPPath(ctx, defaultBotTimeout, c.scopeHash)
	if err != nil {
		return nil, err
	}
	if err := permitted.Err(); err != nil {
		return nil, &notificationdelivery.AdmissionDeferred{RetryAt: time.Now().UTC().Add(30 * time.Second), Reason: "cancelled_before_transport", Cause: err}
	}
	observation := &notificationRetryObservation{}
	permitted = context.WithValue(permitted, notificationRetryObservationKey{}, observation)
	response, err := notificationAttemptClient(c.next).Do(request.Clone(permitted))
	if observation.delay > 0 {
		c.observe(ctx, observation.delay)
	}
	if err != nil {
		if response != nil {
			c.observe(ctx, parseNotificationRetryAfter(response.Header.Get("Retry-After"), time.Now().UTC()))
			if response.Body != nil {
				_ = response.Body.Close()
			}
		}
		return nil, err
	}
	if response == nil {
		return response, nil
	}
	if _, ok := response.Body.(validatedNotificationBody); ok {
		return response, nil
	}
	delay := parseNotificationRetryAfter(response.Header.Get("Retry-After"), time.Now().UTC())
	if response.Body != nil {
		original := response.Body
		prefix, readErr := io.ReadAll(io.LimitReader(original, maxNotificationResponseBytes+1))
		if readErr == nil && len(prefix) <= maxNotificationResponseBytes {
			_ = original.Close()
			response.Body = io.NopCloser(bytes.NewReader(prefix))
			var envelope struct {
				Parameters struct {
					RetryAfter int64 `json:"retry_after"`
				} `json:"parameters"`
			}
			if json.Unmarshal(prefix, &envelope) == nil && envelope.Parameters.RetryAfter > 0 {
				seconds := envelope.Parameters.RetryAfter
				if seconds > math.MaxInt64/int64(time.Second) {
					seconds = math.MaxInt64 / int64(time.Second)
				}
				if bodyDelay := time.Duration(seconds) * time.Second; bodyDelay > delay {
					delay = bodyDelay
				}
			}
		} else {
			// Observation is bounded. Preserve the full SDK stream and its
			// original read error rather than truncating or inventing success.
			tail := io.Reader(original)
			if readErr != nil {
				tail = notificationAdmissionReadError{err: readErr}
			}
			response.Body = notificationAdmissionReplay{Reader: io.MultiReader(bytes.NewReader(prefix), tail), Closer: original, cancel: cancel}
			cancelOnReturn = false
		}
	}
	c.observe(ctx, delay)
	return response, nil
}

func (c notificationAdmissionHTTPClient) observe(ctx context.Context, delay time.Duration) {
	if delay <= 0 {
		return
	}
	observeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := c.admission.ObserveRetryAfter(observeCtx, delay); err != nil {
		// Delivery evidence always wins over a failed quota observation write.
		slog.WarnContext(observeCtx, "notification quota backoff persistence failed", "error", err)
	}
}

type notificationAdmissionReplay struct {
	io.Reader
	io.Closer
	cancel context.CancelFunc
}

func (r notificationAdmissionReplay) Close() error {
	err := r.Closer.Close()
	r.cancel()
	return err
}

type notificationAdmissionReadError struct{ err error }

func (r notificationAdmissionReadError) Read([]byte) (int, error) { return 0, r.err }
