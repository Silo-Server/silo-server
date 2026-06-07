package audiobooks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
)

// ABSSessionStore implements abs.TokenStore on the abs_sessions table
// (migration 147). Each JTI is stored as a SHA-256 `token_hash`; revocation
// sets `revoked_at`. The table stores integer user_id, so we parse the
// string UserID from ABSToken back to int.
type ABSSessionStore struct {
	Pool *pgxpool.Pool
}

// InsertToken inserts a newly minted JTI into abs_sessions.
// Duplicate tokens are silently ignored (ON CONFLICT DO NOTHING) so
// concurrent requests using the same JTI are idempotent.
func (s *ABSSessionStore) InsertToken(ctx context.Context, tok abs.ABSToken) error {
	uid, err := strconv.Atoi(tok.UserID)
	if err != nil {
		return fmt.Errorf("abs_session_store: invalid user id %q: %w", tok.UserID, err)
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO abs_sessions
		  (user_id, profile_id, token_hash, token_type, expires_at, device_id, device_name, client_name, client_version, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
		ON CONFLICT (token_hash) DO NOTHING`,
		uid,
		tok.ProfileID,
		absTokenHash(tok.JTI),
		tok.Type,
		tok.ExpiresAt,
		tok.JTI, // device_id: use the JTI as a stable device key
		"",      // device_name: unknown at login time
		"abs-compat",
		"",
	)
	if err != nil {
		return fmt.Errorf("abs_session_store: insert token: %w", err)
	}
	return nil
}

// GetTokenByJTI looks up an abs_sessions row by the SHA-256 hash of its JTI.
// Returns abs.ErrNotFound when the row doesn't exist.
func (s *ABSSessionStore) GetTokenByJTI(ctx context.Context, jti string) (abs.ABSToken, error) {
	var (
		uid       int
		profileID string
		expiresAt *time.Time
		tok       abs.ABSToken
	)
	row := s.Pool.QueryRow(ctx, `
		SELECT user_id, profile_id, token_type, expires_at, revoked_at
		FROM abs_sessions
		WHERE token_hash = $1`, absTokenHash(jti))
	err := row.Scan(&uid, &profileID, &tok.Type, &expiresAt, &tok.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return abs.ABSToken{}, abs.ErrNotFound
	}
	if err != nil {
		return abs.ABSToken{}, fmt.Errorf("abs_session_store: get token: %w", err)
	}
	tok.JTI = jti
	tok.ID = jti
	tok.UserID = strconv.Itoa(uid)
	tok.ProfileID = profileID
	if expiresAt != nil {
		tok.ExpiresAt = *expiresAt
	}
	return tok, nil
}

// RevokeTokenByJTI sets revoked_at to now() for the given JTI.
// Idempotent: revoking an already-revoked token is a no-op.
func (s *ABSSessionStore) RevokeTokenByJTI(ctx context.Context, jti string) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE abs_sessions SET revoked_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL`, absTokenHash(jti))
	if err != nil {
		return fmt.Errorf("abs_session_store: revoke token: %w", err)
	}
	return nil
}

func (s *ABSSessionStore) RevokeTokenIfActive(ctx context.Context, jti string) (abs.ABSToken, error) {
	var (
		uid       int
		profileID string
		expiresAt *time.Time
		tok       abs.ABSToken
	)
	row := s.Pool.QueryRow(ctx, `
		UPDATE abs_sessions
		SET revoked_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL
		RETURNING user_id, profile_id, token_type, expires_at, revoked_at`, absTokenHash(jti))
	err := row.Scan(&uid, &profileID, &tok.Type, &expiresAt, &tok.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return abs.ABSToken{}, abs.ErrNotFound
	}
	if err != nil {
		return abs.ABSToken{}, fmt.Errorf("abs_session_store: revoke active token: %w", err)
	}
	tok.JTI = jti
	tok.ID = jti
	tok.UserID = strconv.Itoa(uid)
	tok.ProfileID = profileID
	if expiresAt != nil {
		tok.ExpiresAt = *expiresAt
	}
	return tok, nil
}

func (s *ABSSessionStore) RevokeTokensForPrincipal(ctx context.Context, userID, profileID string) error {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return fmt.Errorf("abs_session_store: invalid user id %q: %w", userID, err)
	}
	_, err = s.Pool.Exec(ctx, `
		UPDATE abs_sessions
		SET revoked_at = now()
		WHERE user_id = $1 AND profile_id = $2 AND revoked_at IS NULL`,
		uid, profileID,
	)
	if err != nil {
		return fmt.Errorf("abs_session_store: revoke principal tokens: %w", err)
	}
	return nil
}

// TouchToken bumps last_seen_at for active-session bookkeeping.
// Errors are logged by the caller; we never gate a valid request on this.
func (s *ABSSessionStore) TouchToken(ctx context.Context, jti string) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE abs_sessions SET last_seen_at = now()
		WHERE token_hash = $1`, absTokenHash(jti))
	if err != nil {
		return fmt.Errorf("abs_session_store: touch token: %w", err)
	}
	return nil
}

func absTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
