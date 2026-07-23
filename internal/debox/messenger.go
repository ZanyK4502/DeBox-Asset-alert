package debox

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	boxbotapi "github.com/debox-pro/debox-chat-go-sdk/boxbotapi"
)

const defaultBotTimeout = 20 * time.Second

type Messenger struct {
	bot *boxbotapi.BotAPI
}

func NewMessenger(
	apiKey, apiSecret, baseURL string,
	httpClient boxbotapi.HTTPClient,
) (*Messenger, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("DEBOX_BOT_API_KEY is required")
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
		httpClient,
	)
	if err != nil {
		return nil, fmt.Errorf("initialize DeBox bot: %w", err)
	}
	return &Messenger{bot: bot}, nil
}

func (m *Messenger) SendNotification(chatID, chatType, text string) (string, error) {
	message := boxbotapi.NewMessage(chatID, chatType, text)
	message.ParseMode = boxbotapi.ModeHTML
	sent, err := m.bot.Send(message)
	if err != nil {
		return "", fmt.Errorf("send DeBox notification: %w", err)
	}
	return sent.MessageID, nil
}
