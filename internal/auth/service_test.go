package auth

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type fakeRepository struct {
	cleanupCalls int
	challenge    *store.AuthChallenge
	session      *store.AuthSession
	consumed     bool
	revoked      bool
}

func (f *fakeRepository) CleanupAuthRecords(context.Context) error {
	f.cleanupCalls++
	return nil
}

func (f *fakeRepository) CreateAuthChallenge(
	_ context.Context,
	params store.CreateAuthChallengeParams,
) (store.AuthChallenge, error) {
	value := store.AuthChallenge{
		ChallengeID:   params.ChallengeID,
		WalletAddress: params.WalletAddress,
		NonceHash:     params.NonceHash,
		Message:       params.Message,
		ExpiresAt:     params.ExpiresAt,
	}
	f.challenge = &value
	return value, nil
}

func (f *fakeRepository) GetActiveAuthChallenge(
	_ context.Context,
	challengeID string,
	walletAddress string,
) (*store.AuthChallenge, error) {
	if f.challenge == nil || f.consumed ||
		f.challenge.ChallengeID != challengeID ||
		f.challenge.WalletAddress != walletAddress {
		return nil, nil
	}
	copy := *f.challenge
	return &copy, nil
}

func (f *fakeRepository) ConsumeAuthChallenge(
	_ context.Context,
	challengeID string,
	walletAddress string,
) (bool, error) {
	if f.challenge == nil || f.consumed ||
		f.challenge.ChallengeID != challengeID ||
		f.challenge.WalletAddress != walletAddress {
		return false, nil
	}
	f.consumed = true
	return true, nil
}

func (f *fakeRepository) CreateAuthSession(
	_ context.Context,
	params store.CreateAuthSessionParams,
) (store.AuthSession, error) {
	value := store.AuthSession{
		TokenHash:     params.TokenHash,
		DeBoxUserID:   params.DeBoxUserID,
		WalletAddress: params.WalletAddress,
		ExpiresAt:     params.ExpiresAt,
	}
	f.session = &value
	return value, nil
}

func (f *fakeRepository) GetActiveAuthSession(
	_ context.Context,
	tokenHash string,
) (*store.AuthSession, error) {
	if f.session == nil || f.revoked || f.session.TokenHash != tokenHash {
		return nil, nil
	}
	copy := *f.session
	return &copy, nil
}

func (f *fakeRepository) RevokeAuthSession(
	_ context.Context,
	tokenHash string,
) (bool, error) {
	if f.session == nil || f.revoked || f.session.TokenHash != tokenHash {
		return false, nil
	}
	f.revoked = true
	return true, nil
}

type fakeProfileClient struct {
	profile map[string]any
	err     error
}

func (f fakeProfileClient) UserInfo(
	context.Context,
	string,
	string,
) (map[string]any, error) {
	return f.profile, f.err
}

func TestCreateAndVerifyWalletChallenge(t *testing.T) {
	privateKey := big.NewInt(1)
	address := addressForPrivateKey(privateKey)
	repository := &fakeRepository{}
	service := New(repository, fakeProfileClient{
		profile: map[string]any{"data": map[string]any{"uid": "debox-user"}},
	})
	now := time.Date(2026, 7, 23, 12, 0, 0, 123000000, time.UTC)
	service.now = func() time.Time { return now }
	service.random = bytes.NewReader(bytes.Repeat([]byte{0x42}, 24+32+48))

	challenge, err := service.CreateWalletChallenge(
		context.Background(),
		strings.ToUpper(address[:2])+address[2:],
		"app.example:443",
	)
	if err == nil {
		t.Fatal("CreateWalletChallenge() accepted an uppercase 0X prefix")
	}
	challenge, err = service.CreateWalletChallenge(
		context.Background(),
		"0x"+strings.ToUpper(address[2:]),
		"app.example:443",
	)
	if err != nil {
		t.Fatalf("CreateWalletChallenge() error = %v", err)
	}
	if repository.cleanupCalls != 1 || repository.challenge == nil {
		t.Fatalf("challenge was not persisted: %#v", repository)
	}
	if challenge.WalletAddress != address ||
		!strings.Contains(challenge.Message, "Domain: app.example:443") ||
		!strings.Contains(challenge.Message, "不会发起交易或产生 Gas 费") {
		t.Fatalf("unexpected challenge: %#v", challenge)
	}
	if repository.challenge.NonceHash == "" || strings.Contains(challenge.Message, repository.challenge.NonceHash) {
		t.Fatal("nonce hash was not stored separately")
	}

	signature := signMessage(t, privateKey, challenge.Message)
	verification, err := service.VerifyWalletChallenge(
		context.Background(),
		challenge.ChallengeID,
		address,
		strings.TrimPrefix(signature, "0x"),
	)
	if err != nil {
		t.Fatalf("VerifyWalletChallenge() error = %v", err)
	}
	if verification.DeBoxUserID != "debox-user" ||
		verification.WalletAddress != address ||
		verification.SessionToken == "" ||
		verification.ExpiresAt != now.Add(SessionTTL).Format(time.RFC3339Nano) {
		t.Fatalf("unexpected verification: %#v", verification)
	}
	if repository.session == nil ||
		repository.session.TokenHash != HashSecret(verification.SessionToken) ||
		!repository.consumed {
		t.Fatalf("session/challenge state = %#v", repository)
	}

	session, err := service.AuthenticatedSession(context.Background(), verification.SessionToken)
	if err != nil || session == nil || session.DeBoxUserID != "debox-user" {
		t.Fatalf("AuthenticatedSession() = %#v, %v", session, err)
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

func TestVerifyRejectsWrongWalletAndMissingDeBoxIdentity(t *testing.T) {
	privateKey := big.NewInt(1)
	otherKey := big.NewInt(2)
	repository := &fakeRepository{}
	service := New(repository, fakeProfileClient{profile: map[string]any{"name": "No ID"}})
	service.random = bytes.NewReader(bytes.Repeat([]byte{0x24}, 24+32+48))
	challenge, err := service.CreateWalletChallenge(
		context.Background(),
		addressForPrivateKey(privateKey),
		"",
	)
	if err != nil {
		t.Fatalf("CreateWalletChallenge() error = %v", err)
	}
	_, err = service.VerifyWalletChallenge(
		context.Background(),
		challenge.ChallengeID,
		challenge.WalletAddress,
		signMessage(t, otherKey, challenge.Message),
	)
	if !errors.Is(err, ErrAuthentication) || err.Error() != "签名钱包与连接钱包不一致。" {
		t.Fatalf("wrong wallet error = %v", err)
	}

	_, err = service.VerifyWalletChallenge(
		context.Background(),
		challenge.ChallengeID,
		challenge.WalletAddress,
		signMessage(t, privateKey, challenge.Message),
	)
	if !errors.Is(err, ErrDeBoxIdentity) || err.Error() != "未识别到 DeBox 账号。" {
		t.Fatalf("missing identity error = %v", err)
	}
	if repository.consumed {
		t.Fatal("challenge was consumed before DeBox identity was confirmed")
	}
}

func TestDeBoxUserIDFromProfile(t *testing.T) {
	tests := []struct {
		profile any
		want    string
	}{
		{profile: map[string]any{"user_id": " user-1 "}, want: "user-1"},
		{profile: map[string]any{"userId": "user-2"}, want: "user-2"},
		{profile: map[string]any{"data": map[string]any{"uid": "user-3"}}, want: "user-3"},
		{profile: map[string]any{"data": "invalid"}, want: ""},
	}
	for _, test := range tests {
		if got := DeBoxUserIDFromProfile(test.profile); got != test.want {
			t.Fatalf("DeBoxUserIDFromProfile(%#v) = %q, want %q", test.profile, got, test.want)
		}
	}
}

func TestRecoverAddressMatchesEthereumPersonalSign(t *testing.T) {
	const (
		message   = "DeBox Asset Alert"
		signature = "eb8797d26c4fb8fff2306b6027d7aec89a72490fea69632fc224ac3c0574c549" +
			"62a5aeb7ee07dadfc5bf471ea69cd056daa6186b1b35432492f2a7ad804a10e21c"
		wantAddress = "0x7e5f4552091a69125d5dfcb7b8c2659029395bdf"
	)
	address, err := recoverAddress(message, signature)
	if err != nil {
		t.Fatalf("recoverAddress() error = %v", err)
	}
	if address != wantAddress {
		t.Fatalf("recoverAddress() = %q, want %q", address, wantAddress)
	}
}

func signMessage(t *testing.T, privateKey *big.Int, message string) string {
	t.Helper()
	hash := legacyKeccak256([]byte(
		"\x19Ethereum Signed Message:\n" +
			strconv.Itoa(len([]byte(message))) +
			message,
	))
	z := new(big.Int).SetBytes(hash)
	z.Mod(z, secp256k1N)
	nonce := big.NewInt(3)
	point := secp256k1ScalarBaseMult(nonce)
	r := new(big.Int).Mod(point.x, secp256k1N)
	nonceInverse := new(big.Int).ModInverse(nonce, secp256k1N)
	s := new(big.Int).Mul(r, privateKey)
	s.Add(s, z)
	s.Mul(s, nonceInverse)
	s.Mod(s, secp256k1N)
	recoveryID := byte(point.y.Bit(0))
	halfOrder := new(big.Int).Rsh(new(big.Int).Set(secp256k1N), 1)
	if s.Cmp(halfOrder) > 0 {
		s.Sub(secp256k1N, s)
		recoveryID ^= 1
	}
	signature := make([]byte, 65)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:64])
	signature[64] = recoveryID
	return "0x" + hex.EncodeToString(signature)
}

func addressForPrivateKey(privateKey *big.Int) string {
	publicKey := secp256k1ScalarBaseMult(privateKey)
	serialized := make([]byte, 64)
	publicKey.x.FillBytes(serialized[:32])
	publicKey.y.FillBytes(serialized[32:])
	hash := legacyKeccak256(serialized)
	return "0x" + hex.EncodeToString(hash[len(hash)-20:])
}
