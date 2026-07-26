package httpapi

import (
	"net/http"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
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

func (h handler) getCurrentSubscription(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if h.deps.Subscriptions == nil {
		serviceUnavailable(w)
		return
	}
	if _, err := h.deps.Subscriptions.BindPermanentWallet(
		r.Context(),
		session.DeBoxUserID,
		session.WalletAddress,
	); err != nil {
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
