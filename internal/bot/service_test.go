package bot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	boxbotapi "github.com/debox-pro/debox-chat-go-sdk/boxbotapi"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/subscription"
)

type fakeClient struct {
	mu          sync.Mutex
	sent        []boxbotapi.Chattable
	updates     [][]boxbotapi.Update
	updateCalls []boxbotapi.UpdateConfig
	self        boxbotapi.User
	sendErr     error
	onUpdate    func(int)
}

func (f *fakeClient) Send(config boxbotapi.Chattable) (boxbotapi.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, config)
	return boxbotapi.Message{MessageID: "sent"}, f.sendErr
}

func (f *fakeClient) GetUpdates(config boxbotapi.UpdateConfig) ([]boxbotapi.Update, error) {
	f.mu.Lock()
	call := len(f.updateCalls)
	f.updateCalls = append(f.updateCalls, config)
	var updates []boxbotapi.Update
	if call < len(f.updates) {
		updates = f.updates[call]
	}
	onUpdate := f.onUpdate
	f.mu.Unlock()
	if onUpdate != nil {
		onUpdate(call)
	}
	return updates, nil
}

func (f *fakeClient) Self() boxbotapi.User {
	return f.self
}

func (f *fakeClient) sentConfigs() []boxbotapi.Chattable {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]boxbotapi.Chattable(nil), f.sent...)
}

type fakeRepository struct {
	languages map[string]string
	setUserID string
	setValue  string
	err       error
}

func (f *fakeRepository) GetUserPreferences(
	_ context.Context,
	userID string,
) (store.UserPreference, error) {
	if f.err != nil {
		return store.UserPreference{}, f.err
	}
	return store.UserPreference{
		DeBoxUserID: userID,
		BotLanguage: f.languages[userID],
	}, nil
}

func (f *fakeRepository) SetBotLanguage(
	_ context.Context,
	userID string,
	language string,
) (store.UserPreference, error) {
	f.setUserID = userID
	f.setValue = language
	if f.err != nil {
		return store.UserPreference{}, f.err
	}
	return store.UserPreference{DeBoxUserID: userID, BotLanguage: language}, nil
}

type fakeSubscriptions struct {
	value subscription.Entitlement
	err   error
}

func (f fakeSubscriptions) Entitlement(
	context.Context,
	string,
) (subscription.Entitlement, error) {
	return f.value, f.err
}

type fakeDeBox struct {
	value map[string]any
	err   error
}

func (f fakeDeBox) UserInfo(context.Context, string, string) (map[string]any, error) {
	return f.value, f.err
}

type fakeChain struct {
	results []chain.BalanceResult
	calls   int
	err     error
}

func (f *fakeChain) Balance(
	context.Context,
	string,
	string,
	string,
	string,
) (chain.BalanceResult, error) {
	if f.err != nil {
		return chain.BalanceResult{}, f.err
	}
	result := f.results[f.calls]
	f.calls++
	return result, nil
}

func newTestService(t *testing.T) (*Service, *fakeClient, *fakeRepository, *fakeChain) {
	t.Helper()
	catalog, err := plans.NewCatalog("10", 30, "USDT")
	if err != nil {
		t.Fatalf("create catalog: %v", err)
	}
	client := &fakeClient{self: boxbotapi.User{UserId: "bot-id", Name: "Monitor"}}
	repository := &fakeRepository{languages: map[string]string{}}
	chainService := &fakeChain{results: []chain.BalanceResult{
		{Value: "12.5", Symbol: "USDT", ChainName: "BNB Chain"},
		{Value: "0.08", Symbol: "BNB", ChainName: "BNB Chain"},
	}}
	service := New(Dependencies{
		Client:        client,
		Repository:    repository,
		Subscriptions: fakeSubscriptions{},
		DeBox: fakeDeBox{value: map[string]any{
			"data": map[string]any{"address": "0x1234567890abcdef1234567890abcdef12345678"},
		}},
		Chain:   chainService,
		Catalog: catalog,
		Settings: Settings{
			PublicAppURL:             "https://example.test",
			BotUserID:                "bot-id",
			DefaultChainKey:          "bsc",
			SubscriptionTokenAddress: "0xToken",
		},
	})
	return service, client, repository, chainService
}

func testMessage(text, chatType, userID string) *boxbotapi.Message {
	return &boxbotapi.Message{
		Text: text,
		From: &boxbotapi.User{UserId: userID, Name: "Tester"},
		Chat: &boxbotapi.Chat{ID: "chat-id", Type: chatType},
	}
}

func TestPrivateStartCommandsSendSavedLanguageMenu(t *testing.T) {
	for _, command := range []string{"start", "/start", " /START "} {
		t.Run(strings.TrimSpace(command), func(t *testing.T) {
			service, client, repository, _ := newTestService(t)
			repository.languages["user-id"] = "en"

			_, err := service.HandleUpdate(context.Background(), boxbotapi.Update{
				Id:      7,
				Message: testMessage(command, "private", "user-id"),
			})
			if err != nil {
				t.Fatalf("handle update: %v", err)
			}
			sent := client.sentConfigs()
			if len(sent) != 1 {
				t.Fatalf("sent messages = %d, want 1", len(sent))
			}
			message, ok := sent[0].(boxbotapi.MessageConfig)
			if !ok {
				t.Fatalf("sent config type = %T", sent[0])
			}
			if !strings.Contains(message.Text, "Monitor on-chain addresses") {
				t.Fatalf("menu text does not use saved English: %q", message.Text)
			}
			if message.ChatID != "chat-id" || message.ChatType != "private" {
				t.Fatalf("unexpected chat target: %+v", message.BaseChat)
			}
		})
	}
}

func TestUnrelatedMessageIsIgnored(t *testing.T) {
	service, client, _, _ := newTestService(t)
	result, err := service.HandleUpdate(context.Background(), boxbotapi.Update{
		Message: testMessage("hello", "private", "user-id"),
	})
	if err != nil {
		t.Fatalf("handle update: %v", err)
	}
	if !result.OK || result.Kind != "message" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(client.sentConfigs()) != 0 {
		t.Fatal("unrelated message produced a response")
	}
}

func TestGroupStartUsesBilingualSharedEntry(t *testing.T) {
	service, client, repository, _ := newTestService(t)
	repository.languages["user-id"] = "en"
	_, err := service.HandleUpdate(context.Background(), boxbotapi.Update{
		Message: testMessage("/start", "group", "user-id"),
	})
	if err != nil {
		t.Fatalf("handle group start: %v", err)
	}
	message := client.sentConfigs()[0].(boxbotapi.MessageConfig)
	if !strings.Contains(message.Text, "链上监控助理") ||
		!strings.Contains(message.Text, "monitoring assistant") {
		t.Fatalf("group entry is not bilingual: %q", message.Text)
	}
	markup := message.ReplyMarkup.(boxbotapi.InlineKeyboardMarkup)
	if len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 2 {
		t.Fatalf("unexpected group buttons: %+v", markup.InlineKeyboard)
	}
}

func TestLanguageCallbackPersistsForClickingUser(t *testing.T) {
	service, client, repository, _ := newTestService(t)
	query := &boxbotapi.CallbackQuery{
		From: &boxbotapi.User{UserId: "second-user"},
		Data: "alert:language:en",
		Message: &boxbotapi.Message{
			MessageID: "message-id",
			Chat:      &boxbotapi.Chat{ID: "chat-id", Type: "private"},
		},
	}
	_, err := service.HandleUpdate(context.Background(), boxbotapi.Update{
		CallbackQuery: query,
	})
	if err != nil {
		t.Fatalf("handle callback: %v", err)
	}
	if repository.setUserID != "second-user" || repository.setValue != "en" {
		t.Fatalf("saved language = %q/%q", repository.setUserID, repository.setValue)
	}
	edit := client.sentConfigs()[0].(boxbotapi.EditMessageTextConfig)
	if !strings.Contains(edit.Text, "Monitor on-chain addresses") {
		t.Fatalf("edited menu is not English: %q", edit.Text)
	}
}

func TestBalanceCallbackIncludesTokenAndGas(t *testing.T) {
	service, client, _, chainService := newTestService(t)
	query := &boxbotapi.CallbackQuery{
		From: &boxbotapi.User{UserId: "user-id"},
		Data: "alert:balance",
		Message: &boxbotapi.Message{
			MessageID: "message-id",
			Chat:      &boxbotapi.Chat{ID: "chat-id", Type: "private"},
		},
	}
	_, err := service.HandleUpdate(context.Background(), boxbotapi.Update{
		CallbackQuery: query,
	})
	if err != nil {
		t.Fatalf("handle balance callback: %v", err)
	}
	if chainService.calls != 2 {
		t.Fatalf("balance calls = %d, want 2", chainService.calls)
	}
	edit := client.sentConfigs()[0].(boxbotapi.EditMessageTextConfig)
	if !strings.Contains(edit.Text, "12.5 USDT") ||
		!strings.Contains(edit.Text, "0.08 BNB") {
		t.Fatalf("balance response missing values: %q", edit.Text)
	}
}

func TestCallbackFailureProducesLocalizedMessage(t *testing.T) {
	service, client, repository, _ := newTestService(t)
	repository.languages["user-id"] = "en"
	service.deps.Subscriptions = fakeSubscriptions{err: errors.New("database down")}
	query := &boxbotapi.CallbackQuery{
		From: &boxbotapi.User{UserId: "user-id"},
		Data: "alert:subscription",
		Message: &boxbotapi.Message{
			MessageID: "message-id",
			Chat:      &boxbotapi.Chat{ID: "chat-id", Type: "private"},
		},
	}
	_, err := service.HandleUpdate(context.Background(), boxbotapi.Update{
		CallbackQuery: query,
	})
	if err != nil {
		t.Fatalf("handle callback: %v", err)
	}
	edit := client.sentConfigs()[0].(boxbotapi.EditMessageTextConfig)
	if edit.Text != "Operation failed. Please try again later." {
		t.Fatalf("unexpected failure text: %q", edit.Text)
	}
}

func TestWebhookPayloadValidationAndDispatch(t *testing.T) {
	service, client, _, _ := newTestService(t)
	if _, err := service.HandleWebhookPayload(context.Background(), []byte(`[]`)); !errors.Is(
		err,
		ErrInvalidWebhookPayload,
	) {
		t.Fatalf("array payload error = %v", err)
	}
	result, err := service.HandleWebhookPayload(
		context.Background(),
		[]byte(`{"id":9,"message":{"text":"/start","from":{"user_id":"user-id"},"chat":{"id":"chat-id","type":"private"}}}`),
	)
	if err != nil {
		t.Fatalf("handle webhook payload: %v", err)
	}
	if result.Kind != "message" || result.UpdateID != 9 || len(client.sentConfigs()) != 1 {
		t.Fatalf("unexpected webhook result: %+v", result)
	}
}
