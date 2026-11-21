package httpapi

import (
	"errors"
	"net/http"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func writeMarketScopeError(w http.ResponseWriter, err error) bool {
	status, code, detail := 0, "", ""
	switch {
	case errors.Is(err, store.ErrMarketChainNotAllowed):
		status, code = http.StatusForbidden, "market_chain_not_allowed"
		detail = "所选链不在当前持续行情监控范围内。已有记录与历史保留，请按页面显示的支持链选择。"
	case errors.Is(err, store.ErrMarketPoolLimitReached):
		status, code = http.StatusConflict, "market_pool_limit_reached"
		detail = "所选池数量超过当前套餐的项目池上限。原设置已保留，请按页面显示的限额调整。"
	case errors.Is(err, store.ErrMarketPoolMismatch):
		status, code = http.StatusBadRequest, "market_pool_scope_not_allowed"
		detail = "所选交易池范围不符合当前项目或套餐要求。标准版使用一个主池；原设置已保留。"
	default:
		return false
	}
	writeJSON(w, status, map[string]string{"code": code, "detail": detail})
	return true
}
