package auth

import (
	"context"
	"math/big"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/testdb"
)

func TestAcceptanceWalletLoginPersistsAndRevokesSession(t *testing.T) {
	database := testdb.Open(t)
	service := New(database, fakeProfileClient{
		profile: map[string]any{"data": map[string]any{"uid": "acceptance-user"}},
	})
	privateKey := big.NewInt(1)
	wallet := addressForPrivateKey(privateKey)

	challenge, err := service.CreateWalletChallenge(
		context.Background(),
		wallet,
		"acceptance.local",
	)
	if err != nil {
		t.Fatalf("CreateWalletChallenge() error = %v", err)
	}
	verification, err := service.VerifyWalletChallenge(
		context.Background(),
		challenge.ChallengeID,
		wallet,
		signMessage(t, privateKey, challenge.Message),
	)
	if err != nil {
		t.Fatalf("VerifyWalletChallenge() error = %v", err)
	}
	if verification.DeBoxUserID != "acceptance-user" || verification.SessionToken == "" {
		t.Fatalf("unexpected verification: %#v", verification)
	}

	session, err := service.AuthenticatedSession(context.Background(), verification.SessionToken)
	if err != nil || session == nil || session.DeBoxUserID != "acceptance-user" {
		t.Fatalf("AuthenticatedSession() = %#v, %v", session, err)
	}
	if _, err := service.VerifyWalletChallenge(
		context.Background(),
		challenge.ChallengeID,
		wallet,
		signMessage(t, privateKey, challenge.Message),
	); err == nil {
		t.Fatal("VerifyWalletChallenge() reused a consumed challenge")
	}

	revoked, err := service.RevokeSession(context.Background(), verification.SessionToken)
	if err != nil || !revoked {
		t.Fatalf("RevokeSession() = %v, %v", revoked, err)
	}
	session, err = service.AuthenticatedSession(context.Background(), verification.SessionToken)
	if err != nil || session != nil {
		t.Fatalf("revoked AuthenticatedSession() = %#v, %v", session, err)
	}
}
