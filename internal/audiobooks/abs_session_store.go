package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
)

// ABSSessionStore implements abs.TokenStore on the abs_sessions table
// (migration 139). Each JTI is stored as the `token` column; revocation sets
// `revoked_at`. The table stores integer user_id, so we parse the string
// UserID from ABSToken back to int.
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
		  (user_id, token, device_id, device_name, client_name, client_version, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (token) DO NOTHING`,
		uid,
		tok.JTI,
		tok.JTI,   // device_id: use the JTI as a stable device key
		"",        // device_name: unknown at login time
		"abs-compat",
		"",
	)
	if err != nil {
		return fmt.Errorf("abs_session_store: insert token: %w", err)
	}
	return nil
}

// GetTokenByJTI looks up an abs_sessions row by its JTI (stored in `token`).
// Returns abs.ErrNotFound when the row doesn't exist.
func (s *ABSSessionStore) GetTokenByJTI(ctx context.Context, jti string) (abs.ABSToken, error) {
	var (
		uid       int
		profileID *string
		tok       abs.ABSToken
	)
	row := s.Pool.QueryRow(ctx, `
		SELECT user_id, token, revoked_at
		FROM abs_sessions
		WHERE token = $1`, jti)
	err := row.Scan(&uid, &tok.JTI, &tok.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return abs.ABSToken{}, abs.ErrNotFound
	}
	if err != nil {
		return abs.ABSToken{}, fmt.Errorf("abs_session_store: get token: %w", err)
	}
	tok.ID = tok.JTI
	tok.UserID = strconv.Itoa(uid)
	if profileID != nil {
		tok.ProfileID = *profileID
	}
	return tok, nil
}

// RevokeTokenByJTI sets revoked_at to now() for the given JTI.
// Idempotent: revoking an already-revoked token is a no-op.
func (s *ABSSessionStore) RevokeTokenByJTI(ctx context.Context, jti string) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE abs_sessions SET revoked_at = now()
		WHERE token = $1 AND revoked_at IS NULL`, jti)
	if err != nil {
		return fmt.Errorf("abs_session_store: revoke token: %w", err)
	}
	return nil
}

// TouchToken bumps last_seen_at for active-session bookkeeping.
// Errors are logged by the caller; we never gate a valid request on this.
func (s *ABSSessionStore) TouchToken(ctx context.Context, jti string) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE abs_sessions SET last_seen_at = now()
		WHERE token = $1`, jti)
	if err != nil {
		return fmt.Errorf("abs_session_store: touch token: %w", err)
	}
	return nil
}
