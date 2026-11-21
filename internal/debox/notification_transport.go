package debox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	boxbotapi "github.com/debox-pro/debox-chat-go-sdk/boxbotapi"
)

const maxNotificationResponseBytes = 256 << 10

type validatedNotificationBody struct {
	io.ReadCloser
	retryAfter time.Duration
}

type notificationRetryObservationKey struct{}
type notificationRetryObservation struct{ delay time.Duration }

// Applied to a per-attempt copy of BotAPI. Menu edits, polling and Bot startup
// keep their existing client and wire behavior. All sendMessage calls use the
// signed v2 boundary; there is no fallback to the free endpoint.
type notificationHTTPClient struct {
	next boxbotapi.HTTPClient
	ctx  context.Context
}

// A redirect may replay a POST before the durable sender can inspect the
// result. Copy only the notification client's policy; never mutate the shared
// client used for menus, edits, polling or startup.
func notificationAttemptClient(client boxbotapi.HTTPClient) boxbotapi.HTTPClient {
	switch value := client.(type) {
	case *http.Client:
		copy := *value
		copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return &copy
	case editMessageCompatClient:
		return editMessageCompatClient{client: notificationAttemptClient(value.client)}
	case sendMessageV2Client:
		return sendMessageV2Client{next: notificationAttemptClient(value.next), secret: value.secret}
	case notificationAdmissionHTTPClient:
		return notificationAdmissionHTTPClient{next: notificationAttemptClient(value.next), admission: value.admission, scopeHash: value.scopeHash}
	default:
		return client
	}
}

func (c notificationHTTPClient) Do(request *http.Request) (_ *http.Response, resultErr error) {
	var observedRetryAfter time.Duration
	defer func() {
		if observation, ok := c.ctx.Value(notificationRetryObservationKey{}).(*notificationRetryObservation); ok && observedRetryAfter > observation.delay {
			observation.delay = observedRetryAfter
		}
		var failure *notificationdelivery.Failure
		if errors.As(resultErr, &failure) {
			copy := *failure
			copy.RequestID = request.Header.Get("X-Request-Id")
			if len(copy.RequestID) > 128 {
				copy.RequestID = copy.RequestID[:128]
			}
			resultErr = &copy
		}
	}()
	if err := c.ctx.Err(); err != nil {
		return nil, &notificationdelivery.AdmissionDeferred{RetryAt: time.Now().UTC().Add(30 * time.Second), Reason: "cancelled_before_transport", Cause: err}
	}
	targetID, targetType, targetKnown := notificationRequestDestination(request)
	response, err := c.next.Do(request.Clone(c.ctx))
	if err != nil {
		if response != nil {
			observedRetryAfter = parseNotificationRetryAfter(response.Header.Get("Retry-After"), time.Now().UTC())
		}
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if _, waiting := notificationdelivery.AdmissionRetryAt(err); waiting {
			return nil, err
		}
		var knownFailure *notificationdelivery.Failure
		if errors.As(err, &knownFailure) {
			return nil, err
		}
		var operation *net.OpError
		if errors.As(err, &operation) && operation.Op == "dial" {
			return nil, notificationFailure(notificationdelivery.Retryable, "connection_not_established", 0, err)
		}
		return nil, notificationFailure(notificationdelivery.Unknown, "transport_result_unconfirmed", 0, err)
	}
	if response == nil || response.Body == nil {
		return nil, notificationFailure(notificationdelivery.Unknown, "empty_notification_response", 0, nil)
	}
	if body, validated := response.Body.(validatedNotificationBody); validated {
		observedRetryAfter = body.retryAfter
		return response, nil
	}
	retryAfter := parseNotificationRetryAfter(response.Header.Get("Retry-After"), time.Now().UTC())
	observedRetryAfter = retryAfter
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxNotificationResponseBytes+1))
	_ = response.Body.Close()
	if readErr != nil || len(body) > maxNotificationResponseBytes {
		return nil, notificationFailure(notificationdelivery.Unknown, "notification_response_unreadable", 0, readErr)
	}
	var envelope struct {
		OK         *bool           `json:"ok"`
		Success    *bool           `json:"success"`
		Message    string          `json:"message"`
		Result     json.RawMessage `json:"result"`
		Parameters struct {
			RetryAfter int64 `json:"retry_after"`
		} `json:"parameters"`
	}
	parseErr := json.Unmarshal(body, &envelope)
	if parseErr == nil && envelope.Parameters.RetryAfter > 0 {
		seconds := envelope.Parameters.RetryAfter
		if seconds > math.MaxInt64/int64(time.Second) {
			seconds = math.MaxInt64 / int64(time.Second)
		}
		if delay := time.Duration(seconds) * time.Second; delay > retryAfter {
			retryAfter = delay
		}
	}
	observedRetryAfter = retryAfter
	if response.StatusCode != http.StatusOK {
		// An explicit HTTP rejection cannot override evidence in the same
		// response that a message may already have been accepted.
		if envelope.OK != nil && *envelope.OK || envelope.Success != nil && *envelope.Success || notificationHasMessageID(envelope.Result) {
			return nil, notificationFailure(notificationdelivery.Unknown, "http_rejection_with_acceptance_evidence", 0, nil)
		}
		if response.StatusCode == http.StatusTooManyRequests {
			return nil, notificationFailure(notificationdelivery.Retryable, "debox_rate_limited", retryAfter, fmt.Errorf("HTTP %d", response.StatusCode))
		}
		switch response.StatusCode {
		case 400, 401, 403, 404, 405, 413, 415, 422:
			return nil, notificationFailure(notificationdelivery.Rejected, "debox_http_rejected", 0, fmt.Errorf("HTTP %d", response.StatusCode))
		}
		return nil, notificationFailure(notificationdelivery.Unknown, "notification_http_result_unconfirmed", 0, fmt.Errorf("HTTP %d", response.StatusCode))
	}
	if parseErr != nil {
		return nil, notificationFailure(notificationdelivery.Unknown, "malformed_notification_response", 0, parseErr)
	}
	if envelope.OK == nil || envelope.Success != nil && *envelope.Success != *envelope.OK {
		return nil, notificationFailure(notificationdelivery.Unknown, "contradictory_notification_response", 0, nil)
	}
	if !*envelope.OK {
		if notificationHasMessageID(envelope.Result) {
			return nil, notificationFailure(notificationdelivery.Unknown, "rejection_with_message_id", 0, nil)
		}
		kind, code := notificationdelivery.Rejected, "debox_business_rejected"
		if retryAfter > 0 || strings.Contains(strings.ToLower(envelope.Message), "system is busy") {
			kind, code = notificationdelivery.Retryable, "debox_business_busy"
		} else if envelope.Message == "Energy account is not ready" || envelope.Message == "Failed to retrieve energy balance" {
			// The v2 contract explicitly identifies these two pre-send energy
			// failures as temporary. The durable sender retains its attempt cap;
			// insufficient balance and authentication failures stay rejected.
			kind, code = notificationdelivery.Retryable, "debox_energy_temporarily_unavailable"
		}
		return nil, notificationFailure(kind, code, retryAfter, errors.New(envelope.Message))
	}
	var result struct {
		MessageID string `json:"message_id"`
	}
	if json.Unmarshal(envelope.Result, &result) != nil || strings.TrimSpace(result.MessageID) == "" {
		return nil, notificationFailure(notificationdelivery.Unknown, "success_without_message_id", 0, nil)
	}
	if notificationReceiptConflicts(envelope.Result, targetID, targetType, targetKnown) {
		return nil, notificationFailure(notificationdelivery.Unknown, "success_receipt_target_unconfirmed", 0, nil)
	}
	response.Body = validatedNotificationBody{ReadCloser: io.NopCloser(bytes.NewReader(body)), retryAfter: retryAfter}
	response.ContentLength = int64(len(body))
	return response, nil
}

func notificationRequestDestination(request *http.Request) (string, string, bool) {
	if request.GetBody == nil {
		return "", "", false
	}
	body, err := request.GetBody()
	if err != nil {
		return "", "", false
	}
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, maxSendMessageRequestBytes+1))
	if err != nil || len(raw) > maxSendMessageRequestBytes {
		return "", "", false
	}
	var id, kind string
	if strings.HasPrefix(request.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		values, err := url.ParseQuery(string(raw))
		if err != nil || len(values["chat_id"]) != 1 || len(values["chat_type"]) != 1 {
			return "", "", false
		}
		id, kind = values.Get("chat_id"), values.Get("chat_type")
	} else {
		var value struct {
			ChatID   string `json:"chat_id"`
			ChatType string `json:"chat_type"`
		}
		if json.Unmarshal(raw, &value) != nil {
			return "", "", false
		}
		id, kind = value.ChatID, value.ChatType
	}
	return id, kind, id != "" && (kind == "private" || kind == "group")
}

// v2 permits chat:null. Explicit receipt targets must agree with the request;
// an ID alone never overrides contradictory target evidence.
func notificationReceiptConflicts(raw json.RawMessage, id, kind string, known bool) bool {
	var result map[string]json.RawMessage
	if json.Unmarshal(raw, &result) != nil {
		return true
	}
	check := func(value json.RawMessage, want string) bool {
		if len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
		var got string
		return !known || json.Unmarshal(value, &got) != nil || got != want
	}
	if check(result["chat_id"], id) || check(result["chat_type"], kind) {
		return true
	}
	chat := bytes.TrimSpace(result["chat"])
	if len(chat) == 0 || bytes.Equal(chat, []byte("null")) {
		return false
	}
	var nested map[string]json.RawMessage
	if json.Unmarshal(chat, &nested) != nil || nested == nil {
		return true
	}
	return check(nested["id"], id) || check(nested["chat_id"], id) || check(nested["type"], kind) || check(nested["chat_type"], kind)
}

func notificationHasMessageID(result json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(result, &fields) != nil {
		return false
	}
	id, exists := fields["message_id"]
	if !exists || string(bytes.TrimSpace(id)) == "null" {
		return false
	}
	var text string
	if json.Unmarshal(id, &text) == nil {
		return strings.TrimSpace(text) != ""
	}
	// A non-string ID is not a valid success acknowledgement, but is also
	// insufficient evidence that a rejection was definitely not delivered.
	return true
}

func notificationFailure(kind, code string, retryAfter time.Duration, cause error) error {
	return &notificationdelivery.Failure{Kind: kind, Code: code, RetryAfter: retryAfter, Cause: cause}
}

func parseNotificationRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if len(value) > 128 || value == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
		if seconds > math.MaxInt64/int64(time.Second) {
			seconds = math.MaxInt64 / int64(time.Second)
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}
