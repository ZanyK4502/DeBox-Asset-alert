package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/payment"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
)

func (h handler) getPaymentConfig(w http.ResponseWriter, r *http.Request) {
	if h.deps.Payments == nil {
		serviceUnavailable(w)
		return
	}
	configuration, err := h.deps.Payments.Configuration(
		r.URL.Query().Get("plan_code"),
		r.URL.Query().Get("billing_cycle"),
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, configuration)
}

type preparePaymentInput struct {
	PlanCode     string `json:"plan_code"`
	BillingCycle string `json:"billing_cycle"`
}

func (h handler) postPreparePayment(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if h.deps.Payments == nil {
		serviceUnavailable(w)
		return
	}
	if h.deps.Subscriptions != nil {
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
		if entitlement.Permanent {
			writeError(w, http.StatusBadRequest, errors.New("永久白名单套餐无需购买或续费"))
			return
		}
	}
	var input preparePaymentInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(input.PlanCode) == "" {
		input.PlanCode = plans.Standard
	}
	result, err := h.deps.Payments.Prepare(
		r.Context(),
		session.DeBoxUserID,
		session.WalletAddress,
		input.PlanCode,
		input.BillingCycle,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type verifyPaymentInput struct {
	OrderID int64  `json:"order_id"`
	TxHash  string `json:"tx_hash"`
}

func (h handler) postVerifyPayment(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if h.deps.Payments == nil {
		serviceUnavailable(w)
		return
	}
	var input verifyPaymentInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.OrderID < 1 {
		writeError(w, http.StatusBadRequest, errors.New("order_id must be greater than zero"))
		return
	}
	result, err := h.deps.Payments.Verify(
		r.Context(),
		input.OrderID,
		input.TxHash,
		session.DeBoxUserID,
		session.WalletAddress,
	)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, payment.ErrChainUnavailable) {
			status = http.StatusServiceUnavailable
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
