package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	boxbotapi "github.com/debox-pro/debox-chat-go-sdk/boxbotapi"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/subscription"
)

var ErrInvalidWebhookPayload = errors.New("Webhook 请求体必须是 JSON 对象。")

type Client interface {
	Send(boxbotapi.Chattable) (boxbotapi.Message, error)
	GetUpdates(boxbotapi.UpdateConfig) ([]boxbotapi.Update, error)
	Self() boxbotapi.User
}

type Repository interface {
	GetUserPreferences(context.Context, string) (store.UserPreference, error)
	SetBotLanguage(context.Context, string, string) (store.UserPreference, error)
}

type SubscriptionService interface {
	Entitlement(context.Context, string) (subscription.Entitlement, error)
}

type DeBoxService interface {
	UserInfo(context.Context, string, string) (map[string]any, error)
}

type ChainService interface {
	Balance(context.Context, string, string, string, string) (chain.BalanceResult, error)
}

type Settings struct {
	PublicAppURL             string
	BotUserID                string
	DefaultChainKey          string
	SubscriptionTokenAddress string
}

type Dependencies struct {
	Client        Client
	Repository    Repository
	Subscriptions SubscriptionService
	DeBox         DeBoxService
	Chain         ChainService
	Catalog       *plans.Catalog
	Settings      Settings
}

type Service struct {
	deps         Dependencies
	publicAppURL string
}

func New(dependencies Dependencies) *Service {
	dependencies.Settings.DefaultChainKey = strings.ToLower(
		strings.TrimSpace(dependencies.Settings.DefaultChainKey),
	)
	if dependencies.Settings.DefaultChainKey == "" {
		dependencies.Settings.DefaultChainKey = "bsc"
	}
	return &Service{
		deps:         dependencies,
		publicAppURL: ResolvePublicAppURL(dependencies.Settings.PublicAppURL),
	}
}

type HandleResult struct {
	OK       bool   `json:"ok"`
	Kind     string `json:"kind"`
	UpdateID int    `json:"update_id,omitempty"`
}

func (s *Service) HandleUpdate(
	ctx context.Context,
	update boxbotapi.Update,
) (HandleResult, error) {
	if update.Message != nil {
		if err := s.handleMessage(ctx, update.Message); err != nil {
			return HandleResult{}, err
		}
		return HandleResult{OK: true, Kind: "message", UpdateID: update.Id}, nil
	}
	if update.CallbackQuery != nil {
		if err := s.handleCallback(ctx, update.CallbackQuery); err != nil {
			return HandleResult{}, err
		}
		return HandleResult{OK: true, Kind: "callback", UpdateID: update.Id}, nil
	}
	return HandleResult{OK: true, Kind: "ignored"}, nil
}

func (s *Service) HandleWebhookPayload(
	ctx context.Context,
	payload []byte,
) (HandleResult, error) {
	var update boxbotapi.Update
	if err := json.Unmarshal(payload, &update); err != nil {
		return HandleResult{}, fmt.Errorf("%w: %v", ErrInvalidWebhookPayload, err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil || object == nil {
		return HandleResult{}, ErrInvalidWebhookPayload
	}
	return s.HandleUpdate(ctx, update)
}

func (s *Service) handleMessage(ctx context.Context, message *boxbotapi.Message) error {
	if message == nil || message.Chat == nil {
		return nil
	}
	text := strings.ToLower(strings.TrimSpace(firstNonEmpty(message.Text, message.TextRaw)))
	if text != "start" && text != "/start" {
		return nil
	}
	if message.Chat.Type == "group" {
		return s.sendGroupEntry(ctx, message)
	}
	language, err := s.languageForUser(ctx, userIDFromMessage(message))
	if err != nil {
		return err
	}
	return s.sendMenu(message.Chat.ID, message.Chat.Type, language)
}

func (s *Service) handleCallback(
	ctx context.Context,
	query *boxbotapi.CallbackQuery,
) error {
	if query == nil || query.Message == nil || query.Message.Chat == nil {
		return nil
	}
	userID := userIDFromQuery(query)
	data := strings.TrimSpace(query.Data)
	language, err := s.languageForUser(ctx, userID)
	if err != nil {
		return err
	}
	if strings.HasPrefix(data, "alert:language:") {
		language = normalizeLanguage(data[strings.LastIndex(data, ":")+1:])
		if userID != "" {
			if _, err := s.deps.Repository.SetBotLanguage(ctx, userID, language); err != nil {
				return err
			}
		}
	}

	text, textErr := s.callbackText(ctx, data, userID, language)
	if textErr != nil {
		if language == "en" {
			text = "Operation failed. Please try again later."
		} else {
			text = "操作失败：" + escapeText(textErr.Error())
		}
	}
	message := boxbotapi.NewEditMessageTextAndMarkup(
		query.Message.Chat.ID,
		query.Message.Chat.Type,
		query.Message.MessageID,
		text,
		s.callbackMarkup(data, language),
	)
	message.ParseMode = boxbotapi.ModeHTML
	_, err = s.deps.Client.Send(message)
	return err
}

func (s *Service) sendMenu(chatID, chatType, language string) error {
	message := boxbotapi.NewMessage(chatID, chatType, menuText(language))
	message.ParseMode = boxbotapi.ModeHTML
	message.ReplyMarkup = s.menuMarkup(language)
	_, err := s.deps.Client.Send(message)
	return err
}

func (s *Service) sendGroupEntry(
	ctx context.Context,
	message *boxbotapi.Message,
) error {
	language, err := s.languageForUser(ctx, userIDFromMessage(message))
	if err != nil {
		return err
	}
	response := boxbotapi.NewMessage(
		message.Chat.ID,
		message.Chat.Type,
		groupEntryText(message),
	)
	response.ParseMode = boxbotapi.ModeHTML
	response.ReplyMarkup = s.groupEntryMarkup(language)
	_, err = s.deps.Client.Send(response)
	return err
}

func (s *Service) languageForUser(ctx context.Context, userID string) (string, error) {
	if strings.TrimSpace(userID) == "" {
		return "zh", nil
	}
	preferences, err := s.deps.Repository.GetUserPreferences(ctx, userID)
	if err != nil {
		return "", err
	}
	return normalizeLanguage(preferences.BotLanguage), nil
}

func (s *Service) botPrivateChatURL() string {
	botUserID := strings.TrimSpace(s.deps.Settings.BotUserID)
	if botUserID == "" && s.deps.Client != nil {
		botUserID = strings.TrimSpace(s.deps.Client.Self().UserId)
	}
	if botUserID == "" {
		return ""
	}
	return "https://m.debox.pro/user/chat?id=" + botUserID + "&start="
}

func ResolvePublicAppURL(configured string) string {
	configured = strings.TrimRight(strings.TrimSpace(configured), "/")
	if strings.HasPrefix(configured, "https://") {
		return configured
	}
	value, err := os.ReadFile("data/public_url.txt")
	if err != nil {
		return configured
	}
	publicURL := strings.TrimRight(strings.TrimSpace(string(value)), "/")
	if strings.HasPrefix(publicURL, "https://") {
		return publicURL
	}
	return configured
}

func userIDFromMessage(message *boxbotapi.Message) string {
	if message == nil || message.From == nil {
		return ""
	}
	return strings.TrimSpace(message.From.UserId)
}

func userIDFromQuery(query *boxbotapi.CallbackQuery) string {
	if query == nil || query.From == nil {
		return ""
	}
	return strings.TrimSpace(query.From.UserId)
}

func normalizeLanguage(language string) string {
	if strings.EqualFold(strings.TrimSpace(language), "en") {
		return "en"
	}
	return "zh"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
