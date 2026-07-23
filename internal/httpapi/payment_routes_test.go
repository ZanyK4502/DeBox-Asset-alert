package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/auth"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/payment"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type fakePaymentService struct {
	configurationPlan string
	prepareInput      [3]string
	verifyOrderID     int64
	verifyInput       [3]string
	verifyErr         error
}

func (f *fakePaymentService) Configuration(planCode string) (payment.Configuration, error) {
	f.configurationPlan = planCode
	return payment.Configuration{
		Mode:                  "live",
		Plan:                  plans.Plan{Code: planCode},
		Chain:                 chain.Profile{Key: "bsc", ChainID: 56},
		Ready:                 true,
		RequiredConfirmations: 3,
	}, nil
}

func (f *fakePaymentService) Prepare(
	_ context.Context,
	deboxUserID string,
	walletAddress string,
	planCode string,
) (payment.PrepareResult, error) {
	f.prepareInput = [3]string{deboxUserID, walletAddress, planCode}
	return payment.PrepareResult{
		Order: store.Order{ID: 7, DeBoxUserID: deboxUserID, PayerAddress: walletAddress},
	}, nil
}

func (f *fakePaymentService) Verify(
	_ context.Context,
	orderID int64,
	transactionHash string,
	deboxUserID string,
	walletAddress string,
) (payment.VerifyResult, error) {
	f.verifyOrderID = orderID
	f.verifyInput = [3]string{transactionHash, deboxUserID, walletAddress}
	if f.verifyErr != nil {
		return payment.VerifyResult{}, f.verifyErr
	}
	return payment.VerifyResult{
		PaymentStatus:         "confirming",
		Order:                 store.Order{ID: orderID},
		Confirmations:         1,
		RequiredConfirmations: 3,
	}, nil
}

func TestPaymentRoutesUseAuthenticatedSessionIdentity(t *testing.T) {
	wallet := "0x1111111111111111111111111111111111111111"
	authService := &fakeAuthService{session: &store.AuthSession{
		DeBoxUserID:   "session-user",
		WalletAddress: wallet,
		ExpiresAt:     time.Now().Add(time.Hour),
	}}
	payments := &fakePaymentService{}
	handler := New(testConfig(t), Dependencies{
		Auth:     authService,
		Payments: payments,
	})

	configRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/payment/config?plan_code=professional",
		nil,
	)
	configRecorder := httptest.NewRecorder()
	handler.ServeHTTP(configRecorder, configRequest)
	if configRecorder.Code != http.StatusOK || payments.configurationPlan != "professional" {
		t.Fatalf("config response = %d/%s", configRecorder.Code, configRecorder.Body)
	}

	prepareRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/payment/prepare",
		strings.NewReader(`{
			"plan_code":"professional",
			"debox_user_id":"forged-user",
			"payer_address":"0x9999999999999999999999999999999999999999"
		}`),
	)
	prepareRequest.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "session-token"})
	prepareRecorder := httptest.NewRecorder()
	handler.ServeHTTP(prepareRecorder, prepareRequest)
	if prepareRecorder.Code != http.StatusOK ||
		payments.prepareInput != [3]string{"session-user", wallet, "professional"} {
		t.Fatalf(
			"prepare response = %d/%s, input = %#v",
			prepareRecorder.Code,
			prepareRecorder.Body,
			payments.prepareInput,
		)
	}

	transactionHash := "0x" + strings.Repeat("ab", 32)
	verifyRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/payment/verify",
		strings.NewReader(`{"order_id":7,"tx_hash":"`+transactionHash+`"}`),
	)
	verifyRequest.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "session-token"})
	verifyRecorder := httptest.NewRecorder()
	handler.ServeHTTP(verifyRecorder, verifyRequest)
	if verifyRecorder.Code != http.StatusOK ||
		payments.verifyOrderID != 7 ||
		payments.verifyInput != [3]string{transactionHash, "session-user", wallet} {
		t.Fatalf(
			"verify response = %d/%s, input = %d/%#v",
			verifyRecorder.Code,
			verifyRecorder.Body,
			payments.verifyOrderID,
			payments.verifyInput,
		)
	}
}

func TestPaymentRoutesRequireAuthentication(t *testing.T) {
	handler := New(testConfig(t), Dependencies{
		Auth:     &fakeAuthService{},
		Payments: &fakePaymentService{},
	})
	for _, path := range []string{"/api/payment/prepare", "/api/payment/verify"} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d, body = %s", path, recorder.Code, recorder.Body)
		}
	}
}

func TestVerifyPaymentMapsNodeFailureToServiceUnavailable(t *testing.T) {
	wallet := "0x1111111111111111111111111111111111111111"
	payments := &fakePaymentService{
		verifyErr: errors.Join(payment.ErrChainUnavailable, errors.New("timeout")),
	}
	handler := New(testConfig(t), Dependencies{
		Auth: &fakeAuthService{session: &store.AuthSession{
			DeBoxUserID:   "session-user",
			WalletAddress: wallet,
			ExpiresAt:     time.Now().Add(time.Hour),
		}},
		Payments: payments,
	})
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/payment/verify",
		strings.NewReader(`{"order_id":7,"tx_hash":"0x`+strings.Repeat("ab", 32)+`"}`),
	)
	request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "session-token"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
}
