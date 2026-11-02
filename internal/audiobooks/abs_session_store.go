package audiobooks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/cache"
)

// absTokenTouchThrottle bounds how often a token's last_seen_at is refreshed.
// TouchToken fires on every authenticated /abs/* request and updates a single
// per-token row; without throttling a request burst serialized on that row's
// lock (the same hot-row pattern as device last_seen).
const absTokenTouchThrottle = 5 * time.Minute

// ABSSessionStore implements abs.TokenStore on the abs_sessions table
// (migration 147). Each JTI is stored as a SHA-256 `token_hash`; revocation
// sets `revoked_at`. The table stores integer user_id, so we parse the
// string UserID from ABSToken back to int.
type ABSSessionStore struct {
	Pool *pgxpool.Pool

	touchOnce sync.Once
	touched   *cache.TTLCache[struct{}]
}

// IssueTokens inserts newly minted JTIs into abs_sessions in one transaction
// that holds the account's users row FOR SHARE. Every sign-out everywhere
// (auth.RevokeSignInsInTransaction, an admin's sign-out) locks that row
// before it revokes the account's tokens and login sessions. So either the
// revocation committed first and the check of from below sees it, or it
// waits for this commit and its revoking UPDATE sees the new rows. A
// disabled account gets no new tokens. Duplicate tokens are silently ignored
// (ON CONFLICT DO NOTHING).
func (s *ABSSessionStore) IssueTokens(ctx context.Context, from abs.TokenSource, tokens ...abs.ABSToken) error {
	if len(tokens) == 0 {
		return errors.New("abs_session_store: no tokens to issue")
	}
	uid, err := strconv.Atoi(tokens[0].UserID)
	if err != nil {
		return fmt.Errorf("abs_session_store: invalid user id %q: %w", tokens[0].UserID, err)
	}
	for _, tok := range tokens[1:] {
		if tok.UserID != tokens[0].UserID {
			return fmt.Errorf("abs_session_store: tokens for users %q and %q issued together", tokens[0].UserID, tok.UserID)
		}
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var enabled bool
		err := tx.QueryRow(ctx, `SELECT enabled FROM users WHERE id = $1 FOR SHARE`, uid).Scan(&enabled)
		if errors.Is(err, pgx.ErrNoRows) {
			return abs.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("abs_session_store: lock account: %w", err)
		}
		if !enabled {
			return abs.ErrNotFound
		}
		live, err := tokenSourceLive(ctx, tx, uid, from)
		if err != nil {
			return err
		}
		if !live {
			return abs.ErrNotFound
		}
		for _, tok := range tokens {
			if _, err := tx.Exec(ctx, `
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
			); err != nil {
				return fmt.Errorf("abs_session_store: insert token: %w", err)
			}
		}
		return nil
	})
}

// tokenSourceLive reports whether the credential new tokens for uid are
// minted from is still live, revoking from.JTI when from.Rotate is set. It
// runs after IssueTokens locked the account, in separate statements, so it
// sees any revocation that committed while it waited.
func tokenSourceLive(ctx context.Context, tx pgx.Tx, uid int, from abs.TokenSource) (bool, error) {
	var (
		live bool
		err  error
	)
	switch {
	case from.JTI != "" && from.Rotate:
		var tag pgconn.CommandTag
		tag, err = tx.Exec(ctx, `
			UPDATE abs_sessions SET revoked_at = now()
			WHERE token_hash = $1 AND user_id = $2 AND revoked_at IS NULL`, absTokenHash(from.JTI), uid)
		live = tag.RowsAffected() == 1
	case from.JTI != "":
		err = tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM abs_sessions
			WHERE token_hash = $1 AND user_id = $2 AND revoked_at IS NULL)`, absTokenHash(from.JTI), uid).Scan(&live)
	case from.LoginSessionID != "":
		// auth.SessionRepository.IsValid's predicate, read in this transaction.
		err = tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM auth_sessions
			WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL AND expires_at > NOW())`, from.LoginSessionID, uid).Scan(&live)
	default:
		return false, errors.New("abs_session_store: tokens issued with no source credential")
	}
	if err != nil {
		return false, fmt.Errorf("abs_session_store: check token source: %w", err)
	}
	return live, nil
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
	hash := absTokenHash(jti)
	s.touchOnce.Do(func() { s.touched = cache.NewTTLCache[struct{}]() })
	if _, seen := s.touched.Get(hash); seen {
		// last_seen_at was refreshed within the throttle window; skip the
		// per-request hot-row UPDATE.
		return nil
	}
	s.touched.Set(hash, struct{}{}, absTokenTouchThrottle)
	_, err := s.Pool.Exec(ctx, `
		UPDATE abs_sessions SET last_seen_at = now()
		WHERE token_hash = $1`, hash)
	if err != nil {
		return fmt.Errorf("abs_session_store: touch token: %w", err)
	}
	return nil
}

func absTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
