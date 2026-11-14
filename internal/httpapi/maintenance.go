package httpapi

import "net/http"

// This guard applies only to user configuration routes. Payment reconciliation,
// webhooks and subscription lifecycle work do not pass through it.
func (h handler) userWrite(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.deps.UserWriteMaintenance {
			writeMaintenanceError(w)
			return
		}
		next(w, r)
	}
}

func writeMaintenanceError(w http.ResponseWriter) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{
		"code":   "maintenance",
		"detail": "服务升级中，暂不接受新订单或监控配置调整。已有订单核验、查看和停止监控仍可使用。",
	})
}
