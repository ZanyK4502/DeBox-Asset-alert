package httpapi

import (
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/bot"
)

const maxBotWebhookBody = 1 << 20

func (h handler) getBotWebhookStatus(w http.ResponseWriter, _ *http.Request) {
	baseURL := bot.ResolvePublicAppURL(h.cfg.PublicAppURL)
	webhookURL := "/bot/webhook"
	if baseURL != "" {
		webhookURL = baseURL + webhookURL
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":        h.cfg.ReceiveMode,
		"configured":  strings.TrimSpace(h.cfg.DeBoxWebhookKey) != "",
		"webhook_url": webhookURL,
	})
}

func (h handler) postBotWebhook(w http.ResponseWriter, r *http.Request) {
	if h.cfg.ReceiveMode != "webhook" {
		writeError(w, http.StatusConflict, errors.New("当前未启用 Webhook 接收模式。"))
		return
	}
	expectedKey := strings.TrimSpace(h.cfg.DeBoxWebhookKey)
	if expectedKey == "" {
		writeError(w, http.StatusServiceUnavailable, errors.New("Webhook 尚未配置。"))
		return
	}
	providedKey := strings.TrimSpace(r.Header.Get("X-API-KEY"))
	if len(providedKey) != len(expectedKey) ||
		subtle.ConstantTimeCompare([]byte(providedKey), []byte(expectedKey)) != 1 {
		writeError(w, http.StatusUnauthorized, errors.New("Webhook 凭证无效。"))
		return
	}
	if h.deps.Bot == nil {
		serviceUnavailable(w)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBotWebhookBody+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("无法读取 Webhook 请求体。"))
		return
	}
	if len(body) > maxBotWebhookBody {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("Webhook 请求体过大。"))
		return
	}
	result, err := h.deps.Bot.HandleWebhookPayload(r.Context(), body)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, bot.ErrInvalidWebhookPayload) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
