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

func (h handler) getCombinationRules(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if h.deps.Management == nil {
		serviceUnavailable(w)
		return
	}
	rules, err := h.deps.Management.ListCombinationRules(r.Context(), session.DeBoxUserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"combination_rules": rules})
}

func (h handler) postCombinationRule(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if h.deps.Management == nil {
		serviceUnavailable(w)
		return
	}
	input := management.DefaultCombinationRuleInput()
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := h.deps.Management.CreateCombinationRule(
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

func (h handler) deleteCombinationRule(w http.ResponseWriter, r *http.Request) {
	session, combinationID, ok := h.managementRuleRequest(w, r, "combination_id")
	if !ok {
		return
	}
	result, err := h.deps.Management.DeleteCombinationRule(
		r.Context(),
		session.DeBoxUserID,
		combinationID,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h handler) postRestoreCombinationRule(w http.ResponseWriter, r *http.Request) {
	session, combinationID, ok := h.managementRuleRequest(w, r, "combination_id")
	if !ok {
		return
	}
	result, err := h.deps.Management.RestoreCombinationRule(
		r.Context(),
		session.DeBoxUserID,
		combinationID,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h handler) patchCombinationRuleLanguage(w http.ResponseWriter, r *http.Request) {
	session, combinationID, ok := h.managementRuleRequest(w, r, "combination_id")
	if !ok {
		return
	}
	var input ruleLanguageInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := h.deps.Management.UpdateCombinationRuleLanguage(
		r.Context(),
		session.DeBoxUserID,
		combinationID,
		input.Language,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h handler) getAggregateEvents(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if h.deps.Management == nil {
		serviceUnavailable(w)
		return
	}
	beforeID, err := optionalPositiveInt64(r.URL.Query().Get("before_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("before_id 必须是正整数。"))
		return
	}
	limit, err := optionalPositiveInt(r.URL.Query().Get("limit"))
	if err != nil || limit > 100 {
		writeError(w, http.StatusBadRequest, errors.New("limit 必须是 1 到 100 之间的整数。"))
		return
	}
	page, err := h.deps.Management.ListAggregationEventHistory(
		r.Context(),
		session.DeBoxUserID,
		beforeID,
		limit,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
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

func optionalPositiveInt64(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New("value must be a positive integer")
	}
	return value, nil
}

func optionalPositiveInt(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, errors.New("value must be a positive integer")
	}
	return value, nil
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
