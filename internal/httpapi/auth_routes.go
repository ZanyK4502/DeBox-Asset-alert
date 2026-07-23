package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/auth"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type authChallengeInput struct {
	WalletAddress string `json:"wallet_address"`
}

func (h handler) postAuthChallenge(w http.ResponseWriter, r *http.Request) {
	if h.deps.Auth == nil {
		serviceUnavailable(w)
		return
	}
	var input authChallengeInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	challenge, err := h.deps.Auth.CreateWalletChallenge(r.Context(), input.WalletAddress, requestDomain(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, challenge)
}

type authVerifyInput struct {
	ChallengeID   string `json:"challenge_id"`
	WalletAddress string `json:"wallet_address"`
	Signature     string `json:"signature"`
}

func (h handler) postAuthVerify(w http.ResponseWriter, r *http.Request) {
	if h.deps.Auth == nil {
		serviceUnavailable(w)
		return
	}
	var input authVerifyInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := h.deps.Auth.VerifyWalletChallenge(
		r.Context(),
		input.ChallengeID,
		input.WalletAddress,
		input.Signature,
	)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, auth.ErrDeBoxIdentity) {
			status = http.StatusForbidden
		}
		writeError(w, status, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    result.SessionToken,
		Path:     "/",
		MaxAge:   int(auth.SessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   h.secureAuthCookie(r),
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, result)
}

func (h handler) getAuthSession(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, h.authenticatedUserPayload(r, session))
}

func (h handler) postAuthLogout(w http.ResponseWriter, r *http.Request) {
	if h.deps.Auth == nil {
		serviceUnavailable(w)
		return
	}
	cookie, _ := r.Cookie(auth.CookieName)
	if cookie != nil {
		if _, err := h.deps.Auth.RevokeSession(r.Context(), cookie.Value); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(1, 0).UTC(),
		HttpOnly: true,
		Secure:   h.secureAuthCookie(r),
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h handler) requireSession(w http.ResponseWriter, r *http.Request) (*store.AuthSession, bool) {
	if h.deps.Auth == nil {
		serviceUnavailable(w)
		return nil, false
	}
	cookie, _ := r.Cookie(auth.CookieName)
	token := ""
	if cookie != nil {
		token = cookie.Value
	}
	session, err := h.deps.Auth.AuthenticatedSession(r.Context(), token)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return nil, false
	}
	if session == nil {
		writeError(w, http.StatusUnauthorized, errors.New("登录状态已失效，请重新连接钱包。"))
		return nil, false
	}
	return session, true
}

func (h handler) authenticatedUserPayload(
	r *http.Request,
	session *store.AuthSession,
) map[string]any {
	profile := map[string]any{"user_id": session.DeBoxUserID}
	if h.deps.DeBox != nil {
		if value, err := h.deps.DeBox.UserInfo(r.Context(), session.DeBoxUserID, ""); err == nil {
			profile = value
		}
	}
	return map[string]any{
		"debox_user_id":  session.DeBoxUserID,
		"wallet_address": session.WalletAddress,
		"expires_at":     session.ExpiresAt,
		"profile":        profile,
	}
}

func (h handler) secureAuthCookie(r *http.Request) bool {
	forwardedProtocol := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]))
	return forwardedProtocol == "https" ||
		r.TLS != nil ||
		strings.EqualFold(h.cfg.Environment, "production") ||
		strings.HasPrefix(strings.ToLower(h.cfg.PublicAppURL), "https://")
}

func requestDomain(r *http.Request) string {
	if host := strings.TrimSpace(r.Host); host != "" {
		return host
	}
	return "DeBox Asset Alert"
}
