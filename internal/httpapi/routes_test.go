package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/auth"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/subscription"
)

type fakeAuthService struct {
	session         *store.AuthSession
	challengeWallet string
	challengeDomain string
	verifyInput     [3]string
	revokedToken    string
	verifyErr       error
}

func (f *fakeAuthService) CreateWalletChallenge(
	_ context.Context,
	walletAddress string,
	domain string,
) (auth.Challenge, error) {
	f.challengeWallet = walletAddress
	f.challengeDomain = domain
	return auth.Challenge{
		ChallengeID:   "challenge-1",
		WalletAddress: walletAddress,
		Message:       "sign me",
		ExpiresAt:     "2026-07-23T12:05:00Z",
	}, nil
}

func (f *fakeAuthService) VerifyWalletChallenge(
	_ context.Context,
	challengeID string,
	walletAddress string,
	signature string,
) (auth.Verification, error) {
	f.verifyInput = [3]string{challengeID, walletAddress, signature}
	if f.verifyErr != nil {
		return auth.Verification{}, f.verifyErr
	}
	return auth.Verification{
		SessionToken:  "session-token",
		ExpiresAt:     "2026-07-30T12:00:00Z",
		DeBoxUserID:   "user-1",
		WalletAddress: walletAddress,
		Profile:       map[string]any{"user_id": "user-1"},
	}, nil
}

func (f *fakeAuthService) AuthenticatedSession(
	_ context.Context,
	token string,
) (*store.AuthSession, error) {
	if token != "session-token" || f.session == nil {
		return nil, nil
	}
	copy := *f.session
	return &copy, nil
}

func (f *fakeAuthService) RevokeSession(_ context.Context, token string) (bool, error) {
	f.revokedToken = token
	return token == "session-token", nil
}

type fakeSubscriptionService struct {
	entitlementUser string
	enabledUser     string
	activationInput [3]string
}

func (f *fakeSubscriptionService) Entitlement(
	_ context.Context,
	deboxUserID string,
) (subscription.Entitlement, error) {
	f.entitlementUser = deboxUserID
	return subscription.Entitlement{DeBoxUserID: deboxUserID}, nil
}

func (f *fakeSubscriptionService) EnableFreePlan(
	_ context.Context,
	deboxUserID string,
) (*store.Subscription, error) {
	f.enabledUser = deboxUserID
	return nil, nil
}

func (f *fakeSubscriptionService) ComplimentaryAccess(
	context.Context,
	string,
) (subscription.ComplimentaryAccess, error) {
	return subscription.ComplimentaryAccess{Eligible: true, Available: true}, nil
}

func (f *fakeSubscriptionService) ActivateComplimentaryPlan(
	_ context.Context,
	deboxUserID string,
	walletAddress string,
	planCode string,
) (store.ComplimentaryActivation, error) {
	f.activationInput = [3]string{deboxUserID, walletAddress, planCode}
	return store.ComplimentaryActivation{
		Grant: store.ComplimentaryGrant{
			DeBoxUserID: deboxUserID,
			PlanCode:    planCode,
		},
	}, nil
}

type fakeChainService struct {
	input [4]string
}

func (f *fakeChainService) Balance(
	_ context.Context,
	address, tokenAddress, chainKey, fallback string,
) (chain.BalanceResult, error) {
	f.input = [4]string{address, tokenAddress, chainKey, fallback}
	return chain.BalanceResult{Value: "1.5", Symbol: "BNB", ChainKey: "bsc"}, nil
}

type fakeDeBoxService struct {
	userInput  [2]string
	tokenInput struct {
		contract string
		chainID  int64
	}
}

func (f *fakeDeBoxService) UserInfo(
	_ context.Context,
	userID, walletAddress string,
) (map[string]any, error) {
	f.userInput = [2]string{userID, walletAddress}
	return map[string]any{"user_id": userID, "name": "Alice"}, nil
}

func (f *fakeDeBoxService) TokenInfo(
	_ context.Context,
	contractAddress string,
	chainID int64,
) (map[string]any, error) {
	f.tokenInput.contract = contractAddress
	f.tokenInput.chainID = chainID
	return map[string]any{"symbol": "USDT", "chain_id": chainID}, nil
}

func TestPublicPlansAndChains(t *testing.T) {
	handler := New(testConfig(t))
	for _, path := range []string{"/api/plans", "/api/chains"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %s", path, recorder.Code, recorder.Body)
		}
	}
	var plansBody struct {
		Plans     []plans.Plan     `json:"plans"`
		RuleTypes []plans.RuleType `json:"rule_types"`
	}
	request := httptest.NewRequest(http.MethodGet, "/api/plans", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if err := json.NewDecoder(recorder.Body).Decode(&plansBody); err != nil {
		t.Fatalf("decode plans: %v", err)
	}
	if len(plansBody.Plans) != 3 || len(plansBody.RuleTypes) != 7 {
		t.Fatalf("unexpected plan catalog: %#v", plansBody)
	}
}

func TestAuthenticationRoutesSetServerSessionCookie(t *testing.T) {
	authService := &fakeAuthService{
		session: &store.AuthSession{
			DeBoxUserID:   "user-1",
			WalletAddress: "0x1111111111111111111111111111111111111111",
			ExpiresAt:     time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
		},
	}
	deboxService := &fakeDeBoxService{}
	handler := New(testConfig(t), Dependencies{Auth: authService, DeBox: deboxService})

	challengeRecorder := performJSON(
		t,
		handler,
		http.MethodPost,
		"/api/auth/challenge",
		`{"wallet_address":"0x1111111111111111111111111111111111111111"}`,
	)
	if challengeRecorder.Code != http.StatusOK ||
		authService.challengeDomain != "example.com" ||
		authService.challengeWallet == "" {
		t.Fatalf("challenge response = %d/%s, auth = %#v", challengeRecorder.Code, challengeRecorder.Body, authService)
	}

	verifyRequest := httptest.NewRequest(
		http.MethodPost,
		"https://example.com/api/auth/verify",
		strings.NewReader(`{
			"challenge_id":"challenge-1",
			"wallet_address":"0x1111111111111111111111111111111111111111",
			"signature":"0xsigned"
		}`),
	)
	verifyRequest.Header.Set("Content-Type", "application/json")
	verifyRequest.Header.Set("X-Forwarded-Proto", "https")
	verifyRecorder := httptest.NewRecorder()
	handler.ServeHTTP(verifyRecorder, verifyRequest)
	if verifyRecorder.Code != http.StatusOK {
		t.Fatalf("verify status = %d, body = %s", verifyRecorder.Code, verifyRecorder.Body)
	}
	cookies := verifyRecorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("verify cookies = %#v", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != auth.CookieName || cookie.Value != "session-token" ||
		!cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode ||
		cookie.MaxAge != int(auth.SessionTTL.Seconds()) {
		t.Fatalf("unexpected auth cookie: %#v", cookie)
	}
	if bytes.Contains(verifyRecorder.Body.Bytes(), []byte("session-token")) {
		t.Fatal("session token leaked into the JSON response")
	}

	sessionRequest := httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	sessionRequest.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "session-token"})
	sessionRecorder := httptest.NewRecorder()
	handler.ServeHTTP(sessionRecorder, sessionRequest)
	if sessionRecorder.Code != http.StatusOK {
		t.Fatalf("session status = %d, body = %s", sessionRecorder.Code, sessionRecorder.Body)
	}
	var sessionBody map[string]any
	if err := json.NewDecoder(sessionRecorder.Body).Decode(&sessionBody); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if sessionBody["debox_user_id"] != "user-1" ||
		sessionBody["wallet_address"] != authService.session.WalletAddress ||
		deboxService.userInput != [2]string{"user-1", ""} {
		t.Fatalf("unexpected session response: %#v", sessionBody)
	}

	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	logoutRequest.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "session-token"})
	logoutRecorder := httptest.NewRecorder()
	handler.ServeHTTP(logoutRecorder, logoutRequest)
	if logoutRecorder.Code != http.StatusOK || authService.revokedToken != "session-token" {
		t.Fatalf("logout response = %d/%s", logoutRecorder.Code, logoutRecorder.Body)
	}
	if deleted := logoutRecorder.Result().Cookies()[0]; deleted.MaxAge != -1 || !deleted.HttpOnly {
		t.Fatalf("logout cookie = %#v", deleted)
	}
}

func TestAuthenticationErrorsAndProtectedRoutes(t *testing.T) {
	authService := &fakeAuthService{
		verifyErr: auth.Error{Kind: auth.ErrDeBoxIdentity, Message: "未识别到 DeBox 账号。"},
	}
	handler := New(testConfig(t), Dependencies{Auth: authService})

	verify := performJSON(
		t,
		handler,
		http.MethodPost,
		"/api/auth/verify",
		`{"challenge_id":"c","wallet_address":"0x1","signature":"0x2"}`,
	)
	if verify.Code != http.StatusForbidden || !strings.Contains(verify.Body.String(), "未识别到 DeBox 账号") {
		t.Fatalf("identity error response = %d/%s", verify.Code, verify.Body)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized ||
		!strings.Contains(recorder.Body.String(), "登录状态已失效") {
		t.Fatalf("unauthorized response = %d/%s", recorder.Code, recorder.Body)
	}
}

func TestSubscriptionAndExternalDataRoutesUseAuthenticatedIdentity(t *testing.T) {
	wallet := "0x1111111111111111111111111111111111111111"
	authService := &fakeAuthService{
		session: &store.AuthSession{
			DeBoxUserID:   "user-1",
			WalletAddress: wallet,
			ExpiresAt:     time.Now().Add(time.Hour),
		},
	}
	subscriptions := &fakeSubscriptionService{}
	chainService := &fakeChainService{}
	deboxService := &fakeDeBoxService{}
	handler := New(testConfig(t), Dependencies{
		Auth:          authService,
		Subscriptions: subscriptions,
		Chain:         chainService,
		DeBox:         deboxService,
	})

	for _, test := range []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: "/api/subscription/current"},
		{method: http.MethodPost, path: "/api/subscription/free-trial"},
		{
			method: http.MethodPost,
			path:   "/api/subscription/complimentary",
			body:   `{"plan_code":"professional"}`,
		},
		{
			method: http.MethodGet,
			path:   "/api/chain/balance?address=" + wallet + "&chain_key=bsc",
		},
		{method: http.MethodGet, path: "/api/debox/user"},
		{
			method: http.MethodGet,
			path:   "/api/debox/token?contract_address=0xtoken&chain_key=bsc",
		},
	} {
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "session-token"})
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s %s status = %d, body = %s", test.method, test.path, recorder.Code, recorder.Body)
		}
	}
	if subscriptions.entitlementUser != "user-1" ||
		subscriptions.enabledUser != "user-1" ||
		subscriptions.activationInput != [3]string{"user-1", wallet, "professional"} {
		t.Fatalf("subscription identity inputs = %#v", subscriptions)
	}
	if chainService.input != [4]string{wallet, "", "bsc", "bsc"} {
		t.Fatalf("chain input = %#v", chainService.input)
	}
	if deboxService.tokenInput.contract != "0xtoken" || deboxService.tokenInput.chainID != 56 {
		t.Fatalf("token input = %#v", deboxService.tokenInput)
	}
}

func TestMalformedJSONIsRejected(t *testing.T) {
	handler := New(testConfig(t), Dependencies{Auth: &fakeAuthService{}})
	recorder := performJSON(t, handler, http.MethodPost, "/api/auth/challenge", `{`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func performJSON(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "https://example.com"+path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
