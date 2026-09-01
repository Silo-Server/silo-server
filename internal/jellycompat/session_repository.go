package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
)

const jellycompatSessionColumns = `compat_session.token, compat_session.username, compat_session.account_username,
	compat_session.profile_id, compat_session.profile_name, compat_session.pseudo_user_id,
	compat_session.streamapp_user_id, compat_session.streamapp_session_id, compat_session.auth_revision,
	compat_session.streamapp_access_token, compat_session.streamapp_refresh_token,
	compat_session.streamapp_token_expiry, compat_session.created_at, compat_session.expires_at`

// SessionRepository persists compat sessions in PostgreSQL.
type SessionRepository struct {
	pool   *pgxpool.Pool
	cipher *secret.Cipher
}

// NewSessionRepository creates a new compat session repository.
func NewSessionRepository(pool *pgxpool.Pool, cipher *secret.Cipher) *SessionRepository {
	return &SessionRepository{pool: pool, cipher: cipher}
}

// jellycompatTokenAAD binds a streamapp_* token ciphertext to its session row,
// keyed by the session token (the table's primary key). The session token
// itself is not encrypted (it is matched by value on lookup), so it is a stable,
// known identifier on both the read and write paths.
func jellycompatTokenAAD(column, token string) string {
	return secret.RowAAD("jellycompat_sessions", column, token)
}

func (r *SessionRepository) scanCompatSession(row pgx.Row) (*Session, error) {
	var session Session
	err := row.Scan(
		&session.Token,
		&session.Username,
		&session.AccountUsername,
		&session.ProfileID,
		&session.ProfileName,
		&session.PseudoUserID,
		&session.StreamAppUserID,
		&session.StreamAppSessionID,
		&session.AuthRevision,
		&session.StreamAppAccessToken,
		&session.StreamAppRefreshToken,
		&session.StreamAppTokenExpiry,
		&session.CreatedAt,
		&session.ExpiresAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("scan compat session: %w", err)
	}
	// Decrypt the bridged Silo access/refresh tokens (read-path contract).
	if session.StreamAppAccessToken, err = r.cipher.DecryptIfEncrypted(session.StreamAppAccessToken, jellycompatTokenAAD("streamapp_access_token", session.Token)); err != nil {
		return nil, fmt.Errorf("decrypt streamapp access token: %w", err)
	}
	if session.StreamAppRefreshToken, err = r.cipher.DecryptIfEncrypted(session.StreamAppRefreshToken, jellycompatTokenAAD("streamapp_refresh_token", session.Token)); err != nil {
		return nil, fmt.Errorf("decrypt streamapp refresh token: %w", err)
	}
	return &session, nil
}

// Upsert inserts or updates a compat session.
func (r *SessionRepository) Upsert(ctx context.Context, session Session) error {
	if session.Token == "" {
		session.Token = uuid.NewString()
	}

	accessToken, err := r.cipher.Encrypt(session.StreamAppAccessToken, jellycompatTokenAAD("streamapp_access_token", session.Token))
	if err != nil {
		return fmt.Errorf("encrypt streamapp access token: %w", err)
	}
	refreshToken, err := r.cipher.Encrypt(session.StreamAppRefreshToken, jellycompatTokenAAD("streamapp_refresh_token", session.Token))
	if err != nil {
		return fmt.Errorf("encrypt streamapp refresh token: %w", err)
	}

	tag, err := r.pool.Exec(ctx, `
			INSERT INTO jellycompat_sessions (
				token, username, account_username, profile_id, profile_name, pseudo_user_id,
				streamapp_user_id, streamapp_session_id, auth_revision,
				streamapp_access_token, streamapp_refresh_token,
				streamapp_token_expiry, created_at, expires_at
			) SELECT
				$1, $2, $3, $4, $5, $6,
				$7, $8, $9,
				$10, $11,
				$12, $13, $14
			FROM auth_sessions AS native_session
			JOIN users AS account ON account.id = native_session.user_id
			LEFT JOIN users AS impersonator ON impersonator.id = native_session.impersonator_user_id
			WHERE native_session.id = $8
			  AND native_session.user_id = $7
			  AND native_session.revoked_at IS NULL
			  AND native_session.expires_at > NOW()
			  AND native_session.auth_revision = $9
			  AND account.enabled
			  AND account.auth_revision = $9
			  AND (
				native_session.impersonator_user_id IS NULL OR (
					impersonator.enabled
					AND impersonator.role = 'admin'
					AND impersonator.auth_revision = native_session.impersonator_auth_revision
				)
			  )
			ON CONFLICT (token) DO UPDATE SET
			username = EXCLUDED.username,
			account_username = EXCLUDED.account_username,
			profile_id = EXCLUDED.profile_id,
			profile_name = EXCLUDED.profile_name,
				pseudo_user_id = EXCLUDED.pseudo_user_id,
				streamapp_user_id = EXCLUDED.streamapp_user_id,
				streamapp_session_id = EXCLUDED.streamapp_session_id,
				auth_revision = EXCLUDED.auth_revision,
				streamapp_access_token = EXCLUDED.streamapp_access_token,
			streamapp_refresh_token = EXCLUDED.streamapp_refresh_token,
			streamapp_token_expiry = EXCLUDED.streamapp_token_expiry,
			created_at = EXCLUDED.created_at,
			expires_at = EXCLUDED.expires_at
	`,
		session.Token,
		session.Username,
		session.AccountUsername,
		session.ProfileID,
		session.ProfileName,
		session.PseudoUserID,
		session.StreamAppUserID,
		session.StreamAppSessionID,
		session.AuthRevision,
		accessToken,
		refreshToken,
		session.StreamAppTokenExpiry,
		session.CreatedAt,
		session.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("upsert compat session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// GetByToken loads an active compat session by token.
func (r *SessionRepository) GetByToken(ctx context.Context, token string, now time.Time) (*Session, error) {
	return r.scanCompatSession(r.pool.QueryRow(ctx,
		`SELECT `+jellycompatSessionColumns+`
			FROM jellycompat_sessions AS compat_session
			JOIN auth_sessions AS native_session ON native_session.id = compat_session.streamapp_session_id
			JOIN users AS account ON account.id = compat_session.streamapp_user_id
			LEFT JOIN users AS impersonator ON impersonator.id = native_session.impersonator_user_id
			WHERE compat_session.token = $1
			  AND compat_session.expires_at > $2
			  AND native_session.revoked_at IS NULL
			  AND native_session.expires_at > $2
			  AND native_session.user_id = compat_session.streamapp_user_id
			  AND native_session.auth_revision = compat_session.auth_revision
			  AND account.enabled
			  AND account.auth_revision = compat_session.auth_revision
			  AND (
				native_session.impersonator_user_id IS NULL OR (
					impersonator.enabled
					AND impersonator.role = 'admin'
					AND impersonator.auth_revision = native_session.impersonator_auth_revision
				)
			  )`,
		token, now,
	))
}

// IsActive checks shared durable authority for a cached compat session.
func (r *SessionRepository) IsActive(ctx context.Context, token string, now time.Time) (bool, error) {
	var active bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1
		FROM jellycompat_sessions AS compat_session
		JOIN auth_sessions AS native_session ON native_session.id = compat_session.streamapp_session_id
		JOIN users AS account ON account.id = compat_session.streamapp_user_id
		LEFT JOIN users AS impersonator ON impersonator.id = native_session.impersonator_user_id
		WHERE compat_session.token = $1
		  AND compat_session.expires_at > $2
		  AND native_session.revoked_at IS NULL
		  AND native_session.expires_at > $2
		  AND native_session.user_id = compat_session.streamapp_user_id
		  AND native_session.auth_revision = compat_session.auth_revision
		  AND account.enabled
		  AND account.auth_revision = compat_session.auth_revision
		  AND (
			native_session.impersonator_user_id IS NULL OR (
				impersonator.enabled
				AND impersonator.role = 'admin'
				AND impersonator.auth_revision = native_session.impersonator_auth_revision
			)
		  )
	)`, token, now).Scan(&active)
	if err != nil {
		return false, fmt.Errorf("check compat session authority: %w", err)
	}
	return active, nil
}

// DeleteByToken removes a compat session by token.
func (r *SessionRepository) DeleteByToken(ctx context.Context, token string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM jellycompat_sessions WHERE token = $1`, token)
	if err != nil {
		return fmt.Errorf("delete compat session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// DeleteExpired removes expired compat sessions.
func (r *SessionRepository) DeleteExpired(ctx context.Context, now time.Time) (int, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM jellycompat_sessions WHERE expires_at <= $1`, now)
	if err != nil {
		return 0, fmt.Errorf("delete expired compat sessions: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// DeleteByUserID removes all compat sessions for a given Silo user.
func (r *SessionRepository) DeleteByUserID(ctx context.Context, userID int) (int, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM jellycompat_sessions WHERE streamapp_user_id = $1`, userID)
	if err != nil {
		return 0, fmt.Errorf("delete compat sessions by user: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
