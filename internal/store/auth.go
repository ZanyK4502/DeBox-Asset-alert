package store

import (
	"context"
	"fmt"
	"time"
)

type CreateAuthChallengeParams struct {
	ChallengeID   string
	WalletAddress string
	NonceHash     string
	Message       string
	ExpiresAt     time.Time
}

func (s *Store) CleanupAuthRecords(ctx context.Context) error {
	_, err := withTxValue(ctx, s.db, func(tx DBTX) (struct{}, error) {
		if _, err := tx.Exec(ctx, `
			DELETE FROM auth_challenges
			WHERE expires_at < NOW() - INTERVAL '1 day'
			   OR used_at < NOW() - INTERVAL '1 day'
		`); err != nil {
			return struct{}{}, fmt.Errorf("delete expired auth challenges: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM auth_sessions
			WHERE expires_at < NOW() - INTERVAL '30 days'
			   OR revoked_at < NOW() - INTERVAL '30 days'
		`); err != nil {
			return struct{}{}, fmt.Errorf("delete expired auth sessions: %w", err)
		}
		return struct{}{}, nil
	})
	return err
}

func (s *Store) CreateAuthChallenge(
	ctx context.Context,
	params CreateAuthChallengeParams,
) (AuthChallenge, error) {
	challenge, err := collectOne[AuthChallenge](ctx, s.db, `
		INSERT INTO auth_challenges (
			challenge_id, wallet_address, nonce_hash, message, expires_at
		)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+authChallengeColumns,
		params.ChallengeID,
		params.WalletAddress,
		params.NonceHash,
		params.Message,
		params.ExpiresAt,
	)
	if err != nil {
		return AuthChallenge{}, fmt.Errorf("create auth challenge: %w", err)
	}
	return challenge, nil
}

func (s *Store) GetActiveAuthChallenge(
	ctx context.Context,
	challengeID string,
	walletAddress string,
) (*AuthChallenge, error) {
	challenge, err := collectOptional[AuthChallenge](ctx, s.db, `
		SELECT `+authChallengeColumns+`
		FROM auth_challenges
		WHERE challenge_id = $1
		  AND LOWER(wallet_address) = LOWER($2)
		  AND used_at IS NULL
		  AND expires_at > NOW()
	`, challengeID, walletAddress)
	if err != nil {
		return nil, fmt.Errorf("get active auth challenge: %w", err)
	}
	return challenge, nil
}

func (s *Store) ConsumeAuthChallenge(
	ctx context.Context,
	challengeID string,
	walletAddress string,
) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE auth_challenges
		SET used_at = NOW()
		WHERE challenge_id = $1
		  AND LOWER(wallet_address) = LOWER($2)
		  AND used_at IS NULL
		  AND expires_at > NOW()
	`, challengeID, walletAddress)
	if err != nil {
		return false, fmt.Errorf("consume auth challenge: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

type CreateAuthSessionParams struct {
	TokenHash     string
	DeBoxUserID   string
	WalletAddress string
	ExpiresAt     time.Time
}

func (s *Store) CreateAuthSession(
	ctx context.Context,
	params CreateAuthSessionParams,
) (AuthSession, error) {
	session, err := collectOne[AuthSession](ctx, s.db, `
		INSERT INTO auth_sessions (
			token_hash, debox_user_id, wallet_address, expires_at
		)
		VALUES ($1, $2, $3, $4)
		RETURNING `+authSessionColumns,
		params.TokenHash,
		params.DeBoxUserID,
		params.WalletAddress,
		params.ExpiresAt,
	)
	if err != nil {
		return AuthSession{}, fmt.Errorf("create auth session: %w", err)
	}
	return session, nil
}

func (s *Store) GetActiveAuthSession(ctx context.Context, tokenHash string) (*AuthSession, error) {
	session, err := collectOptional[AuthSession](ctx, s.db, `
		UPDATE auth_sessions
		SET last_seen_at = NOW()
		WHERE token_hash = $1
		  AND revoked_at IS NULL
		  AND expires_at > NOW()
		RETURNING `+authSessionColumns,
		tokenHash,
	)
	if err != nil {
		return nil, fmt.Errorf("get active auth session: %w", err)
	}
	return session, nil
}

func (s *Store) RevokeAuthSession(ctx context.Context, tokenHash string) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE auth_sessions
		SET revoked_at = NOW()
		WHERE token_hash = $1 AND revoked_at IS NULL
	`, tokenHash)
	if err != nil {
		return false, fmt.Errorf("revoke auth session: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
