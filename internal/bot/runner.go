package bot

import (
	"context"
	"log/slog"
	"strings"
	"time"

	boxbotapi "github.com/debox-pro/debox-chat-go-sdk/boxbotapi"
)

const (
	pollTimeout   = 10
	retryDelay    = 2 * time.Second
	healthLogRate = 60 * time.Second
)

type Lock interface {
	Unlock(context.Context) error
}

type TryLockFunc func(context.Context) (Lock, bool, error)

type Runner struct {
	service       *Service
	client        Client
	receiveMode   string
	tryLock       TryLockFunc
	pollTimeout   int
	retryDelay    time.Duration
	healthLogRate time.Duration
}

func NewRunner(
	service *Service,
	client Client,
	receiveMode string,
	tryLock TryLockFunc,
) *Runner {
	return &Runner{
		service:       service,
		client:        client,
		receiveMode:   strings.ToLower(strings.TrimSpace(receiveMode)),
		tryLock:       tryLock,
		pollTimeout:   pollTimeout,
		retryDelay:    retryDelay,
		healthLogRate: healthLogRate,
	}
}

func (r *Runner) Run(ctx context.Context, logger *slog.Logger) {
	if r.receiveMode != "polling" {
		logger.Info("bot polling disabled", "receive_mode", r.receiveMode)
		return
	}
	lock, ok := r.acquireLock(ctx, logger)
	if !ok {
		return
	}
	defer func() {
		if err := lock.Unlock(ctx); err != nil {
			logger.Error("bot polling unlock failed", "error", err)
		}
	}()

	self := r.client.Self()
	logger.Info("bot listener started", "name", self.Name, "user_id", self.UserId)
	updateConfig := boxbotapi.NewUpdate(0)
	updateConfig.Timeout = r.pollTimeout
	lastHealthLog := time.Time{}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		updates, err := r.client.GetUpdates(updateConfig)
		if err != nil {
			logger.Error("bot polling request failed", "error", err)
			if !waitForContext(ctx, r.retryDelay) {
				return
			}
			continue
		}
		now := time.Now()
		if len(updates) > 0 || now.Sub(lastHealthLog) >= r.healthLogRate {
			logger.Info(
				"bot polling healthy",
				"updates", len(updates),
				"offset", updateConfig.Offset,
			)
			lastHealthLog = now
		}
		for _, update := range updates {
			if update.Id >= updateConfig.Offset {
				updateConfig.Offset = update.Id + 1
			}
			if update.Message != nil {
				messageText := strings.ToLower(strings.TrimSpace(
					firstNonEmpty(update.Message.Text, update.Message.TextRaw),
				))
				chatType := ""
				if update.Message.Chat != nil {
					chatType = update.Message.Chat.Type
				}
				logger.Info(
					"received bot message",
					"update_id", update.Id,
					"has_chat", update.Message.Chat != nil,
					"chat_type", chatType,
					"has_text", messageText != "",
					"is_start", messageText == "start" || messageText == "/start",
				)
			} else if update.CallbackQuery != nil {
				logger.Info("received bot callback", "update_id", update.Id)
			}
			if _, err := r.service.HandleUpdate(ctx, update); err != nil {
				logger.Error(
					"bot update failed",
					"update_id", update.Id,
					"error", err,
				)
			}
		}
	}
}

func (r *Runner) acquireLock(
	ctx context.Context,
	logger *slog.Logger,
) (Lock, bool) {
	if r.tryLock == nil {
		logger.Error("bot polling lock is not configured")
		return nil, false
	}
	for {
		lock, acquired, err := r.tryLock(ctx)
		if err != nil {
			logger.Error("bot polling lock failed", "error", err)
		} else if acquired {
			return lock, true
		}
		if !waitForContext(ctx, r.retryDelay) {
			return nil, false
		}
	}
}

func waitForContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
