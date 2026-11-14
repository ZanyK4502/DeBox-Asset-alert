package httpapi

import (
	"errors"
	"net/http"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/management"
)

func (h handler) disableSummaryDuringMaintenance(w http.ResponseWriter, r *http.Request, userID string) {
	// The pointer distinguishes an explicit stop from an omitted/default field.
	// Unlike the ordinary settings route, no scheduling defaults are applied.
	var input struct {
		management.SummarySettingsInput
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.Enabled != nil && *input.Enabled {
		writeMaintenanceError(w)
		return
	}
	if input.Enabled == nil && len(input.Targets) == 0 {
		writeMaintenanceError(w)
		return
	}
	for _, target := range input.Targets {
		if target.Enabled != nil && *target.Enabled || input.Enabled == nil && target.Enabled == nil {
			writeMaintenanceError(w)
			return
		}
	}
	result, err := h.deps.Management.DisableSummarySettings(r.Context(), userID, input.SummarySettingsInput)
	if err != nil {
		if errors.Is(err, management.ErrSummaryMaintenanceChange) {
			writeMaintenanceError(w)
		} else {
			writeError(w, http.StatusBadRequest, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}
