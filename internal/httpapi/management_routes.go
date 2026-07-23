package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/management"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func (h handler) postSummarySettings(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if h.deps.Management == nil {
		serviceUnavailable(w)
		return
	}
	input := management.DefaultSummarySettingsInput()
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := h.deps.Management.SaveSummarySettings(
		r.Context(),
		session.DeBoxUserID,
		input,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h handler) getWatchRules(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if h.deps.Management == nil {
		serviceUnavailable(w)
		return
	}
	rules, err := h.deps.Management.ListWatchRules(r.Context(), session.DeBoxUserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
}

func (h handler) postWatchRule(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if h.deps.Management == nil {
		serviceUnavailable(w)
		return
	}
	input := management.DefaultWatchRuleInput()
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := h.deps.Management.CreateWatchRule(
		r.Context(),
		session.DeBoxUserID,
		input,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h handler) deletePausedWatchRules(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if h.deps.Management == nil {
		serviceUnavailable(w)
		return
	}
	result, err := h.deps.Management.DeletePausedWatchRules(r.Context(), session.DeBoxUserID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h handler) deleteWatchRule(w http.ResponseWriter, r *http.Request) {
	session, ruleID, ok := h.managementRuleRequest(w, r, "rule_id")
	if !ok {
		return
	}
	result, err := h.deps.Management.DeleteWatchRule(r.Context(), session.DeBoxUserID, ruleID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h handler) postFreeMonitorRule(w http.ResponseWriter, r *http.Request) {
	session, ruleID, ok := h.managementRuleRequest(w, r, "rule_id")
	if !ok {
		return
	}
	result, err := h.deps.Management.ChooseFreeWatchRule(r.Context(), session.DeBoxUserID, ruleID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h handler) postRestoreWatchRule(w http.ResponseWriter, r *http.Request) {
	session, ruleID, ok := h.managementRuleRequest(w, r, "rule_id")
	if !ok {
		return
	}
	result, err := h.deps.Management.RestoreWatchRule(r.Context(), session.DeBoxUserID, ruleID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type ruleLanguageInput struct {
	Language string `json:"language"`
}

func (h handler) patchWatchRuleLanguage(w http.ResponseWriter, r *http.Request) {
	session, ruleID, ok := h.managementRuleRequest(w, r, "rule_id")
	if !ok {
		return
	}
	var input ruleLanguageInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := h.deps.Management.UpdateWatchRuleLanguage(
		r.Context(),
		session.DeBoxUserID,
		ruleID,
		input.Language,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h handler) getNotificationGroups(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if h.deps.Management == nil {
		serviceUnavailable(w)
		return
	}
	groups, err := h.deps.Management.ListNotificationGroups(r.Context(), session.DeBoxUserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

func (h handler) postNotificationGroup(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if h.deps.Management == nil {
		serviceUnavailable(w)
		return
	}
	var input management.NotificationGroupInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := h.deps.Management.CreateNotificationGroup(
		r.Context(),
		session.DeBoxUserID,
		session.WalletAddress,
		input,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h handler) deleteNotificationGroup(w http.ResponseWriter, r *http.Request) {
	session, groupID, ok := h.managementRuleRequest(w, r, "group_id")
	if !ok {
		return
	}
	result, err := h.deps.Management.DeleteNotificationGroup(
		r.Context(),
		session.DeBoxUserID,
		groupID,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h handler) managementRuleRequest(
	w http.ResponseWriter,
	r *http.Request,
	pathName string,
) (*store.AuthSession, int64, bool) {
	session, ok := h.requireSession(w, r)
	if !ok {
		return nil, 0, false
	}
	if h.deps.Management == nil {
		serviceUnavailable(w)
		return nil, 0, false
	}
	value, err := strconv.ParseInt(r.PathValue(pathName), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("资源 ID 必须是整数。"))
		return nil, 0, false
	}
	return session, value, true
}
