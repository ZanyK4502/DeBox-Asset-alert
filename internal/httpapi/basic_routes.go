package httpapi

import (
	"net/http"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/subscription"
)

func (h handler) getPlans(w http.ResponseWriter, _ *http.Request) {
	if h.deps.Catalog == nil {
		serviceUnavailable(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"plans":      h.deps.Catalog.PublicPlans(),
		"rule_types": plans.PublicRuleTypes(),
	})
}

func (h handler) getChains(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, chain.SupportedChains())
}

type currentSubscriptionResponse struct {
	subscription.Entitlement
	ComplimentaryAccess subscription.ComplimentaryAccess `json:"complimentary_access"`
}

func (h handler) getCurrentSubscription(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if h.deps.Subscriptions == nil {
		serviceUnavailable(w)
		return
	}
	entitlement, err := h.deps.Subscriptions.Entitlement(r.Context(), session.DeBoxUserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	access, err := h.deps.Subscriptions.ComplimentaryAccess(r.Context(), session.WalletAddress)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, currentSubscriptionResponse{
		Entitlement:         entitlement,
		ComplimentaryAccess: access,
	})
}

func (h handler) postFreePlan(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if h.deps.Subscriptions == nil {
		serviceUnavailable(w)
		return
	}
	if _, err := h.deps.Subscriptions.EnableFreePlan(r.Context(), session.DeBoxUserID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	entitlement, err := h.deps.Subscriptions.Entitlement(r.Context(), session.DeBoxUserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, entitlement)
}

type complimentaryPlanInput struct {
	PlanCode string `json:"plan_code"`
}

func (h handler) postComplimentaryPlan(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if h.deps.Subscriptions == nil {
		serviceUnavailable(w)
		return
	}
	var input complimentaryPlanInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(input.PlanCode) == "" {
		input.PlanCode = plans.Standard
	}
	activation, err := h.deps.Subscriptions.ActivateComplimentaryPlan(
		r.Context(),
		session.DeBoxUserID,
		session.WalletAddress,
		input.PlanCode,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	entitlement, err := h.deps.Subscriptions.Entitlement(r.Context(), session.DeBoxUserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	access, err := h.deps.Subscriptions.ComplimentaryAccess(r.Context(), session.WalletAddress)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"activation": activation,
		"entitlement": currentSubscriptionResponse{
			Entitlement:         entitlement,
			ComplimentaryAccess: access,
		},
	})
}

func (h handler) getBalance(w http.ResponseWriter, r *http.Request) {
	if h.deps.Chain == nil {
		serviceUnavailable(w)
		return
	}
	result, err := h.deps.Chain.Balance(
		r.Context(),
		r.URL.Query().Get("address"),
		r.URL.Query().Get("token_address"),
		r.URL.Query().Get("chain_key"),
		h.cfg.ChainKey,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h handler) getDeBoxUser(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, h.authenticatedUserPayload(r, session)["profile"])
}

func (h handler) getDeBoxToken(w http.ResponseWriter, r *http.Request) {
	if h.deps.DeBox == nil {
		serviceUnavailable(w)
		return
	}
	profile, err := chain.ChainProfile(r.URL.Query().Get("chain_key"), h.cfg.ChainKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := h.deps.DeBox.TokenInfo(
		r.Context(),
		r.URL.Query().Get("contract_address"),
		profile.ChainID,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
