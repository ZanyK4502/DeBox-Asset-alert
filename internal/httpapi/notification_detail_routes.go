package httpapi

import (
	"errors"
	"net/http"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdetail"
)

func (h handler) getNotificationDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Vary", "Cookie")
	session, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if h.deps.NotificationDetails == nil {
		serviceUnavailable(w)
		return
	}
	detail, err := h.deps.NotificationDetails.Detail(
		r.Context(),
		session.DeBoxUserID,
		r.PathValue("notification_id"),
	)
	if err != nil {
		switch {
		case errors.Is(err, notificationdetail.ErrInvalidNotificationID):
			writeError(w, http.StatusBadRequest, notificationdetail.ErrInvalidNotificationID)
		case errors.Is(err, notificationdetail.ErrNotificationNotFound):
			writeError(w, http.StatusNotFound, notificationdetail.ErrNotificationNotFound)
		case errors.Is(err, notificationdetail.ErrNotificationExpired):
			writeError(w, http.StatusGone, notificationdetail.ErrNotificationExpired)
		default:
			writeError(w, http.StatusInternalServerError, errors.New("通知详情暂时无法读取。"))
		}
		return
	}
	writeJSON(w, http.StatusOK, detail)
}
