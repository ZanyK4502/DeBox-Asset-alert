package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"golang.org/x/crypto/sha3"
)

const (
	CookieName   = "debox_asset_alert_session"
	ChallengeTTL = 5 * time.Minute
	SessionTTL   = 7 * 24 * time.Hour
)

var (
	ErrAuthentication = errors.New("authentication failed")
	ErrDeBoxIdentity  = errors.New("DeBox identity was not found")
)

type Error struct {
	Kind    error
	Message string
}

func (e Error) Error() string {
	return e.Message
}

func (e Error) Unwrap() error {
	return e.Kind
}

type Repository interface {
	CleanupAuthRecords(context.Context) error
	CreateAuthChallenge(context.Context, store.CreateAuthChallengeParams) (store.AuthChallenge, error)
	GetActiveAuthChallenge(context.Context, string, string) (*store.AuthChallenge, error)
	ConsumeAuthChallenge(context.Context, string, string) (bool, error)
	CreateAuthSession(context.Context, store.CreateAuthSessionParams) (store.AuthSession, error)
	GetActiveAuthSession(context.Context, string) (*store.AuthSession, error)
	RevokeAuthSession(context.Context, string) (bool, error)
}

type ProfileClient interface {
	UserInfo(context.Context, string, string) (map[string]any, error)
}

type Service struct {
	repository Repository
	profiles   ProfileClient
	now        func() time.Time
	random     io.Reader
}

func New(repository Repository, profiles ProfileClient) *Service {
	return &Service{
		repository: repository,
		profiles:   profiles,
		now:        func() time.Time { return time.Now().UTC() },
		random:     rand.Reader,
	}
}

type Challenge struct {
	ChallengeID   string `json:"challenge_id"`
	WalletAddress string `json:"wallet_address"`
	Message       string `json:"message"`
	ExpiresAt     string `json:"expires_at"`
}

func (s *Service) CreateWalletChallenge(
	ctx context.Context,
	walletAddress string,
	domain string,
) (Challenge, error) {
	wallet, err := chain.ValidateAddress(walletAddress)
	if err != nil {
		return Challenge{}, err
	}
	challengeID, err := s.randomToken(24)
	if err != nil {
		return Challenge{}, fmt.Errorf("create challenge ID: %w", err)
	}
	nonce, err := s.randomToken(32)
	if err != nil {
		return Challenge{}, fmt.Errorf("create challenge nonce: %w", err)
	}
	issuedAt := s.now().UTC()
	expiresAt := issuedAt.Add(ChallengeTTL)
	safeDomain := truncateRunes(strings.TrimSpace(domain), 255)
	if safeDomain == "" {
		safeDomain = "DeBox Asset Alert"
	}
	message := fmt.Sprintf(
		"DeBox Asset Alert login / 登录\n\n"+
			"Sign this message to verify wallet ownership. No transaction or gas fee will occur.\n"+
			"签名仅用于确认钱包归属，不会发起交易或产生 Gas 费。\n\n"+
			"Domain: %s\n"+
			"Wallet: %s\n"+
			"Nonce: %s\n"+
			"Issued At: %s\n"+
			"Expiration Time: %s",
		safeDomain,
		wallet,
		nonce,
		issuedAt.Format(time.RFC3339Nano),
		expiresAt.Format(time.RFC3339Nano),
	)
	if err := s.repository.CleanupAuthRecords(ctx); err != nil {
		return Challenge{}, err
	}
	if _, err := s.repository.CreateAuthChallenge(ctx, store.CreateAuthChallengeParams{
		ChallengeID:   challengeID,
		WalletAddress: wallet,
		NonceHash:     HashSecret(nonce),
		Message:       message,
		ExpiresAt:     expiresAt,
	}); err != nil {
		return Challenge{}, err
	}
	return Challenge{
		ChallengeID:   challengeID,
		WalletAddress: wallet,
		Message:       message,
		ExpiresAt:     expiresAt.Format(time.RFC3339Nano),
	}, nil
}

type Verification struct {
	SessionToken  string         `json:"-"`
	ExpiresAt     string         `json:"expires_at"`
	DeBoxUserID   string         `json:"debox_user_id"`
	WalletAddress string         `json:"wallet_address"`
	Profile       map[string]any `json:"profile"`
}

func (s *Service) VerifyWalletChallenge(
	ctx context.Context,
	challengeID string,
	walletAddress string,
	signature string,
) (Verification, error) {
	wallet, err := chain.ValidateAddress(walletAddress)
	if err != nil {
		return Verification{}, err
	}
	challenge, err := s.repository.GetActiveAuthChallenge(
		ctx,
		strings.TrimSpace(challengeID),
		wallet,
	)
	if err != nil {
		return Verification{}, err
	}
	if challenge == nil {
		return Verification{}, authenticationError("签名请求已失效，请重新连接钱包。")
	}
	recovered, err := recoverAddress(challenge.Message, signature)
	if err != nil {
		return Verification{}, authenticationError("钱包签名无效，请重新连接钱包。")
	}
	if recovered != wallet {
		return Verification{}, authenticationError("签名钱包与连接钱包不一致。")
	}

	profile, err := s.profiles.UserInfo(ctx, "", wallet)
	if err != nil {
		return Verification{}, authenticationError("暂时无法验证 DeBox 账号，请稍后重试。")
	}
	deboxUserID := DeBoxUserIDFromProfile(profile)
	if deboxUserID == "" {
		return Verification{}, Error{Kind: ErrDeBoxIdentity, Message: "未识别到 DeBox 账号。"}
	}
	consumed, err := s.repository.ConsumeAuthChallenge(ctx, challenge.ChallengeID, wallet)
	if err != nil {
		return Verification{}, err
	}
	if !consumed {
		return Verification{}, authenticationError("签名请求已使用，请重新连接钱包。")
	}

	sessionToken, err := s.randomToken(48)
	if err != nil {
		return Verification{}, fmt.Errorf("create session token: %w", err)
	}
	expiresAt := s.now().UTC().Add(SessionTTL)
	if _, err := s.repository.CreateAuthSession(ctx, store.CreateAuthSessionParams{
		TokenHash:     HashSecret(sessionToken),
		DeBoxUserID:   deboxUserID,
		WalletAddress: wallet,
		ExpiresAt:     expiresAt,
	}); err != nil {
		return Verification{}, err
	}
	return Verification{
		SessionToken:  sessionToken,
		ExpiresAt:     expiresAt.Format(time.RFC3339Nano),
		DeBoxUserID:   deboxUserID,
		WalletAddress: wallet,
		Profile:       profile,
	}, nil
}

func (s *Service) AuthenticatedSession(
	ctx context.Context,
	sessionToken string,
) (*store.AuthSession, error) {
	token := strings.TrimSpace(sessionToken)
	if token == "" {
		return nil, nil
	}
	return s.repository.GetActiveAuthSession(ctx, HashSecret(token))
}

func (s *Service) RevokeSession(ctx context.Context, sessionToken string) (bool, error) {
	token := strings.TrimSpace(sessionToken)
	if token == "" {
		return false, nil
	}
	return s.repository.RevokeAuthSession(ctx, HashSecret(token))
}

func HashSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func DeBoxUserIDFromProfile(profile any) string {
	object, ok := profile.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"user_id", "userId", "uid", "id"} {
		if value := strings.TrimSpace(fmt.Sprint(object[key])); value != "" && value != "<nil>" {
			return value
		}
	}
	return DeBoxUserIDFromProfile(object["data"])
}

func recoverAddress(message string, signature string) (string, error) {
	value := strings.TrimSpace(signature)
	if !strings.HasPrefix(value, "0x") {
		if len(value) != 130 {
			return "", ErrAuthentication
		}
		value = "0x" + value
	}
	if len(value) != 132 {
		return "", ErrAuthentication
	}
	raw, err := hex.DecodeString(value[2:])
	if err != nil || len(raw) != 65 {
		return "", ErrAuthentication
	}
	if raw[64] >= 27 {
		raw[64] -= 27
	}
	if raw[64] > 1 {
		return "", ErrAuthentication
	}
	hash := legacyKeccak256(
		[]byte("\x19Ethereum Signed Message:\n" + strconv.Itoa(len([]byte(message))) + message),
	)
	publicKey, err := recoverSecp256k1PublicKey(
		hash,
		new(big.Int).SetBytes(raw[:32]),
		new(big.Int).SetBytes(raw[32:64]),
		raw[64],
	)
	if err != nil {
		return "", ErrAuthentication
	}
	serialized := make([]byte, 64)
	publicKey.x.FillBytes(serialized[:32])
	publicKey.y.FillBytes(serialized[32:])
	addressHash := legacyKeccak256(serialized)
	return chain.ValidateAddress("0x" + hex.EncodeToString(addressHash[len(addressHash)-20:]))
}

func authenticationError(message string) error {
	return Error{Kind: ErrAuthentication, Message: message}
}

func (s *Service) randomToken(byteCount int) (string, error) {
	value := make([]byte, byteCount)
	if _, err := io.ReadFull(s.random, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func legacyKeccak256(values ...[]byte) []byte {
	hasher := sha3.NewLegacyKeccak256()
	for _, value := range values {
		_, _ = hasher.Write(value)
	}
	return hasher.Sum(nil)
}
