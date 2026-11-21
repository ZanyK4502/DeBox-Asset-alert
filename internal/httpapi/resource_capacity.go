package httpapi

import (
	"errors"
	"net/http"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

// Resource admission is enforced in the service/store transaction. This is
// response mapping only, including for an already-paid order's verification;
// it never introduces an admission check or changes payment processing.
func writeResourceCapacityError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, store.ErrResourceCapacityState):
		// Corrupt capacity state and wrapped storage failures are server errors,
		// not a normal sold-out condition that a customer can resolve.
		writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	case errors.Is(err, store.ErrResourceCapacityUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"code": "capacity_not_ready", "detail": "新增服务名额尚未核定，暂不能新购买或扩大监控。已有监控继续运行，已有订单仍可核验。",
		})
	case errors.Is(err, store.ErrSubscriptionSeatsReached):
		writeJSON(w, http.StatusConflict, map[string]string{
			"code": "subscription_seats_full", "detail": "所选套餐的名额已满。已有订单仍可核验。",
		})
	case errors.Is(err, store.ErrMarketHolderTargetsReached):
		writeJSON(w, http.StatusConflict, map[string]string{
			"code": "holder_targets_full", "detail": "全站大户监控名额已满。已有大户监控继续运行。",
		})
	case errors.Is(err, store.ErrResourceCapacityReached):
		writeJSON(w, http.StatusConflict, map[string]string{
			"code": "capacity_full", "detail": "当前可用服务名额已满，暂不能新购买或扩大监控。已有监控继续运行，已有订单仍可核验。",
		})
	default:
		return false
	}
	return true
}
