package debox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	boxbotapi "github.com/debox-pro/debox-chat-go-sdk/boxbotapi"
)

const defaultBotTimeout = 20 * time.Second

type Messenger struct {
	mu        sync.RWMutex
	bot       *boxbotapi.BotAPI
	admission *notificationdelivery.Admission
	sendScope string
}

// SetNotificationAdmission attaches once during startup, before any runners.
// The public getMe identity is shared across keys; keys are never budget keys.
func (m *Messenger) SetNotificationAdmission(admission *notificationdelivery.Admission) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if admission == nil || m.admission != nil || admission.BotID() != strings.TrimSpace(m.bot.Self.UserId) {
		return fmt.Errorf("notification admission must match getMe bot identity and attach once")
	}
	m.bot.Client = notificationAdmissionHTTPClient{next: m.bot.Client, admission: admission, scopeHash: m.sendScope}
	m.admission = admission
	return nil
}

func (m *Messenger) NotificationAdmission() *notificationdelivery.Admission {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.admission
}

func (m *Messenger) botCopy() boxbotapi.BotAPI {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return *m.bot
}

func NewMessenger(
	apiKey, apiSecret, baseURL string,
	httpClient boxbotapi.HTTPClient,
) (*Messenger, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("DEBOX_BOT_API_KEY is required")
	}
	if strings.TrimSpace(apiSecret) == "" {
		return nil, fmt.Errorf("DEBOX_BOT_API_SECRET is required for signed v2 sending")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultOpenAPIBase
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultBotTimeout}
	}
	endpoint := strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/openapi/%s"
	bot, err := boxbotapi.NewBotAPIWithClient(
		strings.TrimSpace(apiKey),
		strings.TrimSpace(apiSecret),
		endpoint,
		editMessageCompatClient{client: sendMessageV2Client{next: httpClient, secret: strings.TrimSpace(apiSecret)}},
	)
	if err != nil {
		return nil, fmt.Errorf("initialize DeBox bot: %w", err)
	}
	return &Messenger{bot: bot, sendScope: sendMessageScope(strings.TrimSpace(apiKey))}, nil
}

func (m *Messenger) SendNotification(chatID, chatType, text string) (string, error) {
	return m.SendNotificationContext(context.Background(), chatID, chatType, text)
}

func (m *Messenger) SendNotificationContext(ctx context.Context, chatID, chatType, text string) (string, error) {
	message := boxbotapi.NewMessage(chatID, chatType, text)
	message.ParseMode = boxbotapi.ModeHTML
	bot := m.botCopy()
	bot.Client = notificationHTTPClient{next: notificationAttemptClient(bot.Client), ctx: ctx}
	sent, err := bot.Send(message)
	if err != nil {
		return "", fmt.Errorf("send DeBox notification: %w", err)
	}
	return sent.MessageID, nil
}

func (m *Messenger) SendNotificationWithAction(
	chatID, chatType, text, actionText, actionURL string,
) (string, error) {
	return m.SendNotificationWithActionContext(context.Background(), chatID, chatType, text, actionText, actionURL)
}

func (m *Messenger) SendNotificationWithActionContext(ctx context.Context, chatID, chatType, text, actionText, actionURL string) (string, error) {
	message := boxbotapi.NewMessage(chatID, chatType, text)
	message.ParseMode = boxbotapi.ModeHTML
	message.ReplyMarkup = boxbotapi.NewInlineKeyboardMarkup(
		boxbotapi.NewInlineKeyboardRow(
			boxbotapi.NewInlineKeyboardButtonURL(actionText, actionURL),
		),
	)
	bot := m.botCopy()
	bot.Client = notificationHTTPClient{next: notificationAttemptClient(bot.Client), ctx: ctx}
	sent, err := bot.Send(message)
	if err != nil {
		return "", fmt.Errorf("send DeBox notification with action: %w", err)
	}
	return sent.MessageID, nil
}

func (m *Messenger) Send(config boxbotapi.Chattable) (boxbotapi.Message, error) {
	bot := m.botCopy()
	return bot.Send(config)
}

func (m *Messenger) GetUpdates(
	config boxbotapi.UpdateConfig,
) ([]boxbotapi.Update, error) {
	bot := m.botCopy()
	return bot.GetUpdates(config)
}

func (m *Messenger) Self() boxbotapi.User {
	return m.botCopy().Self
}

// The pinned SDK uses the retired edit route and expects a Message result.
// Keep its request fields and all other operations unchanged, while accepting
// the current API's boolean confirmation of an edit to an existing message.
type editMessageCompatClient struct {
	client boxbotapi.HTTPClient
}

func (c editMessageCompatClient) Do(request *http.Request) (*http.Response, error) {
	const oldPath = "/openapi/bot/editMessageText"
	const newPath = "/openapi/bot/editMessage"
	if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, oldPath) {
		return c.client.Do(request)
	}

	editRequest := request.Clone(request.Context())
	editRequest.URL.Path = strings.TrimSuffix(editRequest.URL.Path, oldPath) + newPath
	if editRequest.URL.RawPath != "" {
		editRequest.URL.RawPath = strings.TrimSuffix(editRequest.URL.RawPath, oldPath) + newPath
	}
	response, err := c.client.Do(editRequest)
	if err != nil || response.StatusCode != http.StatusOK {
		return response, err
	}

	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read DeBox edit response: %w", err)
	}
	response.Body = io.NopCloser(bytes.NewReader(body))

	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil ||
		!bytes.Equal(bytes.TrimSpace(envelope["ok"]), []byte("true")) ||
		!bytes.Equal(bytes.TrimSpace(envelope["result"]), []byte("true")) ||
		bytes.Equal(bytes.TrimSpace(envelope["success"]), []byte("false")) {
		return response, nil
	}

	// An edit confirmation contains no new Message. An empty object lets the
	// SDK report that success without inventing a new message or resending it.
	envelope["result"] = json.RawMessage(`{}`)
	adapted, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("adapt DeBox edit response: %w", err)
	}
	response.Body = io.NopCloser(bytes.NewReader(adapted))
	response.ContentLength = int64(len(adapted))
	if response.Header == nil {
		response.Header = make(http.Header)
	}
	response.Header.Set("Content-Length", strconv.Itoa(len(adapted)))
	return response, nil
}
