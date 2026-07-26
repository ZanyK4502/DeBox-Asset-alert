package httpapi

import (
	"errors"
	"io"
	"net/http"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketcollector"
)

func (h handler) postMarketWebhook(w http.ResponseWriter, r *http.Request) {
	if h.deps.MarketWebhook == nil {
		serviceUnavailable(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, marketcollector.MaxWebhookBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, marketcollector.ErrWebhookBodyTooLarge)
			return
		}
		writeError(w, http.StatusBadRequest, marketcollector.ErrInvalidWebhook)
		return
	}
	result, err := h.deps.MarketWebhook.AcceptWebhook(
		r.Context(),
		r.PathValue("category"),
		map[string][]string(r.Header),
		body,
	)
	if err != nil {
		switch {
		case errors.Is(err, marketcollector.ErrCollectorDisabled),
			errors.Is(err, marketcollector.ErrWebhookUnavailable):
			writeError(w, http.StatusServiceUnavailable, err)
		case errors.Is(err, marketcollector.ErrInvalidSignature),
			errors.Is(err, marketcollector.ErrExpiredWebhook):
			writeError(w, http.StatusUnauthorized, err)
		case errors.Is(err, marketcollector.ErrWebhookBodyTooLarge):
			writeError(w, http.StatusRequestEntityTooLarge, err)
		default:
			writeError(w, http.StatusBadRequest, err)
		}
		return
	}
	// Nodit treats an explicit 200 response as successful delivery.
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"inbox_id":  result.InboxID,
		"duplicate": result.Duplicate,
	})
}
