package debox

import (
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	boxbotapi "github.com/debox-pro/debox-chat-go-sdk/boxbotapi"
)

const sendMessageV2Path = "/openapi/v2/bot/sendMessage"
const maxSendMessageRequestBytes = 256 << 10

// The pinned SDK still emits the free route, form/text and a seconds-based
// signature. Adapt only sendMessage at the final HTTP boundary, after durable
// admission has waited, so every actual attempt receives fresh signed headers.
type sendMessageV2Client struct {
	next   boxbotapi.HTTPClient
	secret string
}

func sendMessageScope(key string) string {
	hash := sha256.Sum256([]byte(key + "\x00" + sendMessageV2Path))
	return hex.EncodeToString(hash[:])
}

func noV2HTTPRequest(reason string) error {
	return &notificationdelivery.AdmissionDeferred{RetryAt: time.Now().UTC().Add(time.Minute), Reason: reason}
}

func (c sendMessageV2Client) Do(request *http.Request) (*http.Response, error) {
	const oldPath = "/openapi/bot/sendMessage"
	if request.Method != http.MethodPost || (!strings.HasSuffix(request.URL.Path, oldPath) && !strings.HasSuffix(request.URL.Path, sendMessageV2Path)) {
		return c.next.Do(request)
	}
	if request.Body != nil {
		defer request.Body.Close()
	}
	if request.Context().Err() != nil {
		return nil, noV2HTTPRequest("cancelled_before_v2_transport")
	}
	if c.secret == "" {
		return nil, notificationFailure(notificationdelivery.Rejected, "v2_app_secret_required", 0, nil)
	}
	body, err := sendMessageV2Body(request)
	if err != nil {
		return nil, notificationFailure(notificationdelivery.Rejected, "invalid_v2_message_payload", 0, nil)
	}
	var random [32]byte
	if _, err = rand.Read(random[:]); err != nil {
		return nil, notificationFailure(notificationdelivery.Retryable, "v2_nonce_unavailable_before_http", 0, nil)
	}
	nonce := hex.EncodeToString(random[:])
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	if len(timestamp) != 13 {
		return nil, notificationFailure(notificationdelivery.Rejected, "invalid_v2_signature_timestamp", 0, nil)
	}
	signature := sha1.Sum([]byte(c.secret + nonce + timestamp))
	clone := request.Clone(request.Context())
	if strings.HasSuffix(clone.URL.Path, oldPath) {
		clone.URL.Path = strings.TrimSuffix(clone.URL.Path, oldPath) + sendMessageV2Path
		clone.URL.RawPath = ""
	}
	clone.Body = io.NopCloser(bytes.NewReader(body))
	clone.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	clone.ContentLength = int64(len(body))
	clone.TransferEncoding = nil
	clone.Header.Set("Content-Type", "application/json")
	clone.Header.Set("Content-Length", strconv.Itoa(len(body)))
	clone.Header.Set("nonce", nonce)
	clone.Header.Set("timestamp", timestamp)
	clone.Header.Set("signature", hex.EncodeToString(signature[:]))
	if clone.Context().Err() != nil {
		return nil, noV2HTTPRequest("cancelled_before_v2_transport")
	}
	// Ordinary replies use the same bounded receipt validation as durable
	// notifications. The private body marker prevents outer admission/delivery
	// wrappers from decoding and copying this successful response again.
	return (notificationHTTPClient{next: notificationAttemptClient(c.next), ctx: clone.Context()}).Do(clone)
}

func sendMessageV2Body(request *http.Request) ([]byte, error) {
	var body io.ReadCloser
	var err error
	if request.GetBody != nil {
		body, err = request.GetBody()
	} else {
		body = request.Body
	}
	if err != nil || body == nil {
		return nil, fmt.Errorf("missing message body")
	}
	if request.GetBody != nil {
		defer body.Close()
	}
	raw, err := io.ReadAll(io.LimitReader(body, maxSendMessageRequestBytes+1))
	if err != nil || len(raw) > maxSendMessageRequestBytes {
		return nil, fmt.Errorf("message body exceeds wire bound")
	}
	media, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil {
		return nil, fmt.Errorf("unsupported message encoding")
	}
	var values map[string]json.RawMessage
	switch media {
	case "application/x-www-form-urlencoded":
		form, err := url.ParseQuery(string(raw))
		if err != nil {
			return nil, err
		}
		values = make(map[string]json.RawMessage, len(form))
		for key, list := range form {
			if len(list) != 1 {
				return nil, fmt.Errorf("duplicate message parameter")
			}
			value := list[0]
			if !utf8.ValidString(value) {
				return nil, fmt.Errorf("invalid UTF-8 message parameter")
			}
			switch key {
			case "reply_markup", "user_action_markup":
				var object map[string]json.RawMessage
				if json.Unmarshal([]byte(value), &object) != nil || object == nil {
					return nil, fmt.Errorf("invalid message markup")
				}
				values[key] = json.RawMessage(value)
			case "mention_ids":
				var ids []string
				if json.Unmarshal([]byte(value), &ids) != nil {
					return nil, fmt.Errorf("invalid message mentions")
				}
				values[key] = json.RawMessage(value)
			case "mention_type":
				n, err := strconv.Atoi(value)
				if err != nil {
					return nil, fmt.Errorf("invalid mention type")
				}
				values[key], _ = json.Marshal(n)
			case "text", "content", "chat_id", "chat_type", "parse_mode", "message_id":
				values[key], _ = json.Marshal(value)
			default:
				return nil, fmt.Errorf("unsupported sendMessage field")
			}
		}
	case "application/json":
		if !utf8.Valid(raw) || json.Unmarshal(raw, &values) != nil || values == nil {
			return nil, fmt.Errorf("invalid message JSON")
		}
		for key := range values {
			switch key {
			case "text", "content", "chat_id", "chat_type", "parse_mode", "message_id", "reply_markup", "user_action_markup", "mention_type", "mention_ids":
			default:
				return nil, fmt.Errorf("unsupported sendMessage field")
			}
		}
	default:
		return nil, fmt.Errorf("unsupported message encoding")
	}
	for _, key := range []string{"reply_markup", "user_action_markup"} {
		if value, ok := values[key]; ok {
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				delete(values, key)
				continue
			}
			var object map[string]json.RawMessage
			if json.Unmarshal(value, &object) != nil || object == nil {
				return nil, fmt.Errorf("invalid message markup")
			}
		}
	}
	if value, ok := values["mention_ids"]; ok {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			delete(values, "mention_ids")
		} else {
			var ids []string
			if json.Unmarshal(value, &ids) != nil {
				return nil, fmt.Errorf("invalid message mentions")
			}
		}
	}
	if value, ok := values["mention_type"]; ok {
		var n int
		if json.Unmarshal(value, &n) != nil {
			return nil, fmt.Errorf("invalid mention type")
		}
	}
	if value, ok := values["message_id"]; ok {
		var id string
		if json.Unmarshal(value, &id) != nil {
			return nil, fmt.Errorf("invalid message identifier")
		}
	}
	if text, exists := values["text"]; exists {
		if content, ok := values["content"]; ok && !bytes.Equal(content, text) {
			return nil, fmt.Errorf("ambiguous message content")
		}
		values["content"] = text
		delete(values, "text")
	}
	var content, chatID, chatType string
	if json.Unmarshal(values["content"], &content) != nil || content == "" || !utf8.ValidString(content) ||
		json.Unmarshal(values["chat_id"], &chatID) != nil || chatID == "" || json.Unmarshal(values["chat_type"], &chatType) != nil || (chatType != "private" && chatType != "group") {
		return nil, fmt.Errorf("invalid required message fields")
	}
	parseMode := "richtext"
	if rawMode, ok := values["parse_mode"]; ok && (json.Unmarshal(rawMode, &parseMode) != nil) {
		return nil, fmt.Errorf("invalid message parse mode")
	}
	switch parseMode {
	case "richtext", "text", "Markdown", "MarkdownV2", "HTML":
		if utf8.RuneCountInString(content) > 5000 {
			return nil, fmt.Errorf("message text exceeds supported length")
		}
	case "image", "video", "file":
	default:
		return nil, fmt.Errorf("unsupported message parse mode")
	}
	encoded, err := json.Marshal(values)
	if err != nil || len(encoded) > maxSendMessageRequestBytes {
		return nil, fmt.Errorf("encoded message exceeds wire bound")
	}
	return encoded, nil
}
