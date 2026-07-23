package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/auth"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/bot"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/config"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/management"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/payment"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/subscription"
)

type handler struct {
	cfg  config.Config
	deps Dependencies
}

type AuthService interface {
	CreateWalletChallenge(context.Context, string, string) (auth.Challenge, error)
	VerifyWalletChallenge(context.Context, string, string, string) (auth.Verification, error)
	AuthenticatedSession(context.Context, string) (*store.AuthSession, error)
	RevokeSession(context.Context, string) (bool, error)
}

type SubscriptionService interface {
	Entitlement(context.Context, string) (subscription.Entitlement, error)
	EnableFreePlan(context.Context, string) (*store.Subscription, error)
	ComplimentaryAccess(context.Context, string) (subscription.ComplimentaryAccess, error)
	ActivateComplimentaryPlan(context.Context, string, string, string) (store.ComplimentaryActivation, error)
}

type ChainService interface {
	Balance(context.Context, string, string, string, string) (chain.BalanceResult, error)
}

type DeBoxService interface {
	UserInfo(context.Context, string, string) (map[string]any, error)
	TokenInfo(context.Context, string, int64) (map[string]any, error)
}

type ManagementService interface {
	ListWatchRules(context.Context, string) ([]store.WatchRule, error)
	CreateWatchRule(context.Context, string, management.WatchRuleInput) (management.WatchRuleCreation, error)
	DeletePausedWatchRules(context.Context, string) (management.EntitlementResult, error)
	DeleteWatchRule(context.Context, string, int64) (management.EntitlementResult, error)
	ChooseFreeWatchRule(context.Context, string, int64) (subscription.Entitlement, error)
	RestoreWatchRule(context.Context, string, int64) (subscription.Entitlement, error)
	UpdateWatchRuleLanguage(context.Context, string, int64, string) (management.WatchRuleUpdate, error)
	SaveSummarySettings(context.Context, string, management.SummarySettingsInput) (management.SummarySettingsResult, error)
	ListNotificationGroups(context.Context, string) ([]store.NotificationGroup, error)
	CreateNotificationGroup(
		context.Context,
		string,
		string,
		management.NotificationGroupInput,
	) (management.NotificationGroupCreation, error)
	DeleteNotificationGroup(context.Context, string, int64) (management.NotificationGroupDeletion, error)
}

type PaymentService interface {
	Configuration(string) (payment.Configuration, error)
	Prepare(context.Context, string, string, string) (payment.PrepareResult, error)
	Verify(context.Context, int64, string, string, string) (payment.VerifyResult, error)
}

type BotService interface {
	HandleWebhookPayload(context.Context, []byte) (bot.HandleResult, error)
}

type Dependencies struct {
	Auth          AuthService
	Subscriptions SubscriptionService
	Chain         ChainService
	DeBox         DeBoxService
	Management    ManagementService
	Payments      PaymentService
	Bot           BotService
	Catalog       *plans.Catalog
	ReadyCheck    func(context.Context) error
}

func New(cfg config.Config, dependencies ...Dependencies) http.Handler {
	deps := Dependencies{}
	if len(dependencies) > 0 {
		deps = dependencies[0]
	}
	if deps.Catalog == nil {
		deps.Catalog, _ = plans.NewCatalog(
			cfg.SubscriptionPrice,
			cfg.SubscriptionDays,
			cfg.SubscriptionTokenSymbol,
		)
	}
	h := handler{cfg: cfg, deps: deps}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", h.health)
	mux.HandleFunc("GET /api/ready", h.ready)
	mux.HandleFunc("GET /api/plans", h.getPlans)
	mux.HandleFunc("GET /api/chains", h.getChains)
	mux.HandleFunc("POST /api/auth/challenge", h.postAuthChallenge)
	mux.HandleFunc("POST /api/auth/verify", h.postAuthVerify)
	mux.HandleFunc("GET /api/auth/session", h.getAuthSession)
	mux.HandleFunc("POST /api/auth/logout", h.postAuthLogout)
	mux.HandleFunc("GET /api/subscription/current", h.getCurrentSubscription)
	mux.HandleFunc("POST /api/subscription/free-trial", h.postFreePlan)
	mux.HandleFunc("POST /api/subscription/complimentary", h.postComplimentaryPlan)
	mux.HandleFunc("POST /api/subscription/summary-settings", h.postSummarySettings)
	mux.HandleFunc("GET /api/watch-rules", h.getWatchRules)
	mux.HandleFunc("POST /api/watch-rules", h.postWatchRule)
	mux.HandleFunc("DELETE /api/watch-rules/paused", h.deletePausedWatchRules)
	mux.HandleFunc("DELETE /api/watch-rules/{rule_id}", h.deleteWatchRule)
	mux.HandleFunc("POST /api/watch-rules/{rule_id}/free-monitor", h.postFreeMonitorRule)
	mux.HandleFunc("POST /api/watch-rules/{rule_id}/restore", h.postRestoreWatchRule)
	mux.HandleFunc("PATCH /api/watch-rules/{rule_id}/notification-language", h.patchWatchRuleLanguage)
	mux.HandleFunc("GET /api/chain/balance", h.getBalance)
	mux.HandleFunc("GET /api/debox/user", h.getDeBoxUser)
	mux.HandleFunc("GET /api/debox/token", h.getDeBoxToken)
	mux.HandleFunc("GET /api/payment/config", h.getPaymentConfig)
	mux.HandleFunc("POST /api/payment/prepare", h.postPreparePayment)
	mux.HandleFunc("POST /api/payment/verify", h.postVerifyPayment)
	mux.HandleFunc("GET /api/bot/webhook-status", h.getBotWebhookStatus)
	mux.HandleFunc("POST /bot/webhook", h.postBotWebhook)
	mux.HandleFunc("GET /api/notification-groups", h.getNotificationGroups)
	mux.HandleFunc("POST /api/notification-groups", h.postNotificationGroup)
	mux.HandleFunc("DELETE /api/notification-groups/{group_id}", h.deleteNotificationGroup)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir(cfg.StaticDir))))
	mux.HandleFunc("GET /", h.index)
	return mux
}

func (h handler) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(h.cfg.StaticDir, "index.html"))
}

func (h handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"app":          h.cfg.AppName,
		"environment":  h.cfg.Environment,
		"receive_mode": h.cfg.ReceiveMode,
	})
}

func (h handler) ready(w http.ResponseWriter, r *http.Request) {
	if h.deps.ReadyCheck != nil {
		if err := h.deps.ReadyCheck(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"ok":     false,
				"status": "database_unavailable",
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"status": "ready",
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"detail": err.Error()})
}

func serviceUnavailable(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, errors.New("服务尚未完成初始化。"))
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := decoder.Decode(target); err != nil {
		return errors.New("请求体必须是有效的 JSON。")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("请求体只能包含一个 JSON 对象。")
	}
	return nil
}
