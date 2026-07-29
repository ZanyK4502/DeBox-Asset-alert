package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketview"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func (h handler) getMarketCatalog(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireMarketSession(w, r)
	if !ok {
		return
	}
	result, err := h.deps.Market.Catalog(r.Context(), session.DeBoxUserID)
	if err != nil {
		writeMarketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h handler) postMarketQuery(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireMarketSession(w, r)
	if !ok {
		return
	}
	var input marketview.TokenQueryInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := h.deps.Market.QueryToken(r.Context(), session.DeBoxUserID, input)
	if err != nil {
		writeMarketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h handler) postMarketPoolsDiscover(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireMarketSession(w, r)
	if !ok {
		return
	}
	var input marketview.MultiTokenQueryInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := h.deps.Market.QueryTokens(
		r.Context(),
		session.DeBoxUserID,
		input,
	)
	if err != nil {
		writeMarketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h handler) getMarketProjects(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireMarketSession(w, r)
	if !ok {
		return
	}
	includeArchived := strings.EqualFold(r.URL.Query().Get("include_archived"), "true")
	projects, err := h.deps.Market.ListProjects(
		r.Context(), session.DeBoxUserID, includeArchived,
	)
	if err != nil {
		writeMarketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

func (h handler) postMarketProject(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireMarketSession(w, r)
	if !ok {
		return
	}
	var input marketview.CreateProjectInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := h.deps.Market.CreateProject(r.Context(), session.DeBoxUserID, input)
	if err != nil {
		writeMarketError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (h handler) getMarketProject(w http.ResponseWriter, r *http.Request) {
	session, projectID, ok := h.marketResourceRequest(w, r, "project_id")
	if !ok {
		return
	}
	result, err := h.deps.Market.Project(r.Context(), session.DeBoxUserID, projectID)
	if err != nil {
		writeMarketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h handler) deleteMarketProject(w http.ResponseWriter, r *http.Request) {
	session, projectID, ok := h.marketResourceRequest(w, r, "project_id")
	if !ok {
		return
	}
	project, err := h.deps.Market.ArchiveProject(
		r.Context(), session.DeBoxUserID, projectID,
	)
	if err != nil {
		writeMarketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "project": project})
}

func (h handler) deleteArchivedMarketProject(w http.ResponseWriter, r *http.Request) {
	session, projectID, ok := h.marketResourceRequest(w, r, "project_id")
	if !ok {
		return
	}
	if err := h.deps.Market.DeleteArchivedProject(
		r.Context(), session.DeBoxUserID, projectID,
	); err != nil {
		writeMarketError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h handler) postRestoreMarketProject(w http.ResponseWriter, r *http.Request) {
	session, projectID, ok := h.marketResourceRequest(w, r, "project_id")
	if !ok {
		return
	}
	project, err := h.deps.Market.RestoreProject(
		r.Context(), session.DeBoxUserID, projectID,
	)
	if err != nil {
		writeMarketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "project": project})
}

func (h handler) patchMarketProjectPool(w http.ResponseWriter, r *http.Request) {
	session, projectID, ok := h.marketResourceRequest(w, r, "project_id")
	if !ok {
		return
	}
	var input marketview.PoolSelectionInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := h.deps.Market.SelectPool(
		r.Context(), session.DeBoxUserID, projectID, input,
	)
	if err != nil {
		writeMarketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h handler) getMarketRecommendations(w http.ResponseWriter, r *http.Request) {
	session, projectID, ok := h.marketResourceRequest(w, r, "project_id")
	if !ok {
		return
	}
	recommendations, err := h.deps.Market.Recommendations(
		r.Context(), session.DeBoxUserID, projectID,
	)
	if err != nil {
		writeMarketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"recommendations": recommendations,
		"generated_at":    time.Now().UTC(),
	})
}

func (h handler) postMarketRecommendationPreview(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireMarketSession(w, r)
	if !ok {
		return
	}
	var input marketview.RecommendationPreviewInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	recommendations, err := h.deps.Market.PreviewRecommendations(
		r.Context(), session.DeBoxUserID, input,
	)
	if err != nil {
		writeMarketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"recommendations": recommendations,
		"generated_at":    time.Now().UTC(),
	})
}

func (h handler) getMarketEvents(w http.ResponseWriter, r *http.Request) {
	session, projectID, ok := h.marketResourceRequest(w, r, "project_id")
	if !ok {
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
	if limit == 0 {
		limit = 50
	}
	poolID, err := optionalPositiveInt64(r.URL.Query().Get("pool_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("pool_id 必须是正整数。"))
		return
	}
	events, err := h.deps.Market.Events(
		r.Context(),
		session.DeBoxUserID,
		projectID,
		marketview.EventFilterInput{
			BeforeID:      beforeID,
			Limit:         limit,
			ChainKey:      r.URL.Query().Get("chain_key"),
			RuleType:      r.URL.Query().Get("rule_type"),
			MarketPoolID:  poolID,
			WalletAddress: r.URL.Query().Get("address"),
		},
	)
	if err != nil {
		writeMarketError(w, err)
		return
	}
	var nextBeforeID *int64
	if len(events) == limit && len(events) > 0 {
		value := events[len(events)-1].ID
		nextBeforeID = &value
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events": events, "next_before_id": nextBeforeID,
	})
}

func (h handler) postMarketRule(w http.ResponseWriter, r *http.Request) {
	session, projectID, ok := h.marketResourceRequest(w, r, "project_id")
	if !ok {
		return
	}
	var input marketview.CreateRuleInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	rule, err := h.deps.Market.CreateRule(
		r.Context(), session.DeBoxUserID, projectID, input,
	)
	if err != nil {
		writeMarketError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"rule": rule})
}

func (h handler) deleteMarketRule(w http.ResponseWriter, r *http.Request) {
	session, ruleID, ok := h.marketResourceRequest(w, r, "rule_id")
	if !ok {
		return
	}
	if err := h.deps.Market.DeleteRule(r.Context(), session.DeBoxUserID, ruleID); err != nil {
		writeMarketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h handler) postRestoreMarketRule(w http.ResponseWriter, r *http.Request) {
	session, ruleID, ok := h.marketResourceRequest(w, r, "rule_id")
	if !ok {
		return
	}
	rule, err := h.deps.Market.RestoreRule(r.Context(), session.DeBoxUserID, ruleID)
	if err != nil {
		writeMarketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rule": rule})
}

func (h handler) getMarketCombinations(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireMarketSession(w, r)
	if !ok {
		return
	}
	values, err := h.deps.Market.ListCombinations(r.Context(), session.DeBoxUserID)
	if err != nil {
		writeMarketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"combinations": values})
}

func (h handler) postMarketCombination(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireMarketSession(w, r)
	if !ok {
		return
	}
	var input marketview.CreateCombinationInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	value, err := h.deps.Market.CreateCombination(
		r.Context(), session.DeBoxUserID, input,
	)
	if err != nil {
		writeMarketError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"combination": value})
}

func (h handler) deleteMarketCombination(w http.ResponseWriter, r *http.Request) {
	session, combinationID, ok := h.marketResourceRequest(w, r, "combination_id")
	if !ok {
		return
	}
	if err := h.deps.Market.ArchiveCombination(
		r.Context(), session.DeBoxUserID, combinationID,
	); err != nil {
		writeMarketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h handler) postRestoreMarketCombination(w http.ResponseWriter, r *http.Request) {
	session, combinationID, ok := h.marketResourceRequest(w, r, "combination_id")
	if !ok {
		return
	}
	value, err := h.deps.Market.RestoreCombination(
		r.Context(), session.DeBoxUserID, combinationID,
	)
	if err != nil {
		writeMarketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "combination": value,
	})
}

func (h handler) postMarketAddressLabel(w http.ResponseWriter, r *http.Request) {
	session, projectID, ok := h.marketResourceRequest(w, r, "project_id")
	if !ok {
		return
	}
	var input marketview.AddressLabelInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	label, err := h.deps.Market.SaveAddressLabel(
		r.Context(), session.DeBoxUserID, projectID, input,
	)
	if err != nil {
		writeMarketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"label": label})
}

func (h handler) deleteMarketAddressLabel(w http.ResponseWriter, r *http.Request) {
	session, labelID, ok := h.marketResourceRequest(w, r, "label_id")
	if !ok {
		return
	}
	if err := h.deps.Market.DeleteAddressLabel(
		r.Context(), session.DeBoxUserID, labelID,
	); err != nil {
		writeMarketError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h handler) requireMarketSession(
	w http.ResponseWriter,
	r *http.Request,
) (*store.AuthSession, bool) {
	session, ok := h.requireSession(w, r)
	if !ok {
		return nil, false
	}
	if h.deps.Market == nil {
		serviceUnavailable(w)
		return nil, false
	}
	return session, true
}

func (h handler) marketResourceRequest(
	w http.ResponseWriter,
	r *http.Request,
	pathName string,
) (*store.AuthSession, int64, bool) {
	session, ok := h.requireMarketSession(w, r)
	if !ok {
		return nil, 0, false
	}
	value, err := strconv.ParseInt(r.PathValue(pathName), 10, 64)
	if err != nil || value <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("资源 ID 必须是正整数。"))
		return nil, 0, false
	}
	return session, value, true
}

func writeMarketError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, marketview.ErrMarketDataUnavailable) {
		status = http.StatusServiceUnavailable
	} else if errors.Is(err, store.ErrNotFound) {
		status = http.StatusNotFound
	}
	writeError(w, status, err)
}
