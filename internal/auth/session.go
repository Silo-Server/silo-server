package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

type sessionExecQuerier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// ErrSessionNotFound is returned when a session ID does not exist.
var ErrSessionNotFound = errors.New("session not found")

// IsSessionNotFound returns true if the error is a "session not found" error.
func IsSessionNotFound(err error) bool {
	return errors.Is(err, ErrSessionNotFound)
}

// sessionColumns is the list of columns returned by all session SELECT queries.
const sessionColumns = `id, user_id, device_name, COALESCE(device_id, '') AS device_id, COALESCE(device_platform, '') AS device_platform, COALESCE(host(ip_address), '') AS ip_address, created_at, expires_at, revoked_at, impersonator_user_id, impersonation_started_at, identity_id, provider_since, last_seen_at`

// SessionRepository provides CRUD operations for the auth_sessions table.
type SessionRepository struct {
	pool *pgxpool.Pool
}

// NewSessionRepository creates a new SessionRepository backed by the given pool.
func NewSessionRepository(pool *pgxpool.Pool) *SessionRepository {
	return &SessionRepository{pool: pool}
}

// scanSession scans a single row into a *models.AuthSession.
func scanSession(row pgx.Row) (*models.AuthSession, error) {
	var s models.AuthSession
	err := row.Scan(
		&s.ID,
		&s.UserID,
		&s.DeviceName,
		&s.DeviceID,
		&s.DevicePlatform,
		&s.IPAddress,
		&s.CreatedAt,
		&s.ExpiresAt,
		&s.RevokedAt,
		&s.ImpersonatorUserID,
		&s.ImpersonationStartedAt,
		&s.IdentityID,
		&s.ProviderSince,
		&s.LastSeenAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("scanning session: %w", err)
	}
	return &s, nil
}

// scanSessions scans multiple rows into a []*models.AuthSession slice.
func scanSessions(rows pgx.Rows) ([]*models.AuthSession, error) {
	var sessions []*models.AuthSession
	for rows.Next() {
		var s models.AuthSession
		err := rows.Scan(
			&s.ID,
			&s.UserID,
			&s.DeviceName,
			&s.DeviceID,
			&s.DevicePlatform,
			&s.IPAddress,
			&s.CreatedAt,
			&s.ExpiresAt,
			&s.RevokedAt,
			&s.ImpersonatorUserID,
			&s.ImpersonationStartedAt,
			&s.IdentityID,
			&s.ProviderSince,
			&s.LastSeenAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning session row: %w", err)
		}
		sessions = append(sessions, &s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating session rows: %w", err)
	}
	return sessions, nil
}

// Create inserts a new auth session. If the session's ID is empty, a new UUID
// is generated via crypto/rand (through github.com/google/uuid). The device a
// sign-in transport attached to ctx (WithClientDevice) is recorded on it.
func (r *SessionRepository) Create(ctx context.Context, session models.AuthSession) error {
	return r.createWithQuerier(ctx, r.pool, session)
}

// createWithQuerier inserts a new auth session using the provided exec-capable
// database handle so callers can participate in an existing transaction.
func (r *SessionRepository) createWithQuerier(
	ctx context.Context,
	db sessionExecQuerier,
	session models.AuthSession,
) error {
	if session.ID == "" {
		session.ID = uuid.New().String()
	}
	applyClientDevice(ctx, &session)

	// A session opened through a provider identity is vouched for from now
	// unless it continues an older chain (a device sign-in approved from a
	// provider session).
	if session.IdentityID != nil && session.ProviderSince == nil {
		now := time.Now()
		session.ProviderSince = &now
	}

	query := `INSERT INTO auth_sessions
		(id, user_id, device_name, ip_address, expires_at, impersonator_user_id, impersonation_started_at, identity_id, provider_since, device_id, device_platform)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, ''), NULLIF($11, ''))`

	// ip_address is a Postgres inet column; an empty string fails the
	// inet input parser (SQLSTATE 22P02). Pass NULL when the caller
	// couldn't determine a client IP — e.g. ABS-compat logins that
	// validate creds in-process without a real request to read from.
	var ipArg any
	if session.IPAddress != "" {
		ipArg = session.IPAddress
	}

	_, err := db.Exec(ctx, query,
		session.ID,
		session.UserID,
		session.DeviceName,
		ipArg,
		session.ExpiresAt,
		session.ImpersonatorUserID,
		session.ImpersonationStartedAt,
		session.IdentityID,
		session.ProviderSince,
		session.DeviceID,
		session.DevicePlatform,
	)
	if err != nil {
		return fmt.Errorf("creating session: %w", err)
	}
	return nil
}

// GetByID retrieves a session by its ID.
func (r *SessionRepository) GetByID(ctx context.Context, id string) (*models.AuthSession, error) {
	query := `SELECT ` + sessionColumns + ` FROM auth_sessions WHERE id = $1`
	return scanSession(r.pool.QueryRow(ctx, query, id))
}

// ListByUser returns all sessions for a given user, ordered by created_at
// descending (newest first).
func (r *SessionRepository) ListByUser(ctx context.Context, userID int) ([]*models.AuthSession, error) {
	query := `SELECT ` + sessionColumns + ` FROM auth_sessions WHERE user_id = $1 ORDER BY created_at DESC`
	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("listing sessions for user %d: %w", userID, err)
	}
	defer rows.Close()

	return scanSessions(rows)
}

// SessionKey is the keyset position of one session in the
// (created_at DESC, id DESC) order ListByUserPage serves.
type SessionKey struct {
	CreatedAt time.Time
	ID        string
}

// ListByUserPage returns up to limit of the user's live sessions (not
// expired, not revoked), newest first by (created_at DESC, id DESC), and
// strictly after the key when one is given. v2 listSessions pages with it;
// v1's ListByUser is unchanged. Callers that need has_more ask for limit+1.
func (r *SessionRepository) ListByUserPage(ctx context.Context, userID int, after *SessionKey, limit int) ([]*models.AuthSession, error) {
	args := []any{userID}
	query := `SELECT ` + sessionColumns + ` FROM auth_sessions
		WHERE user_id = $1 AND expires_at > NOW() AND revoked_at IS NULL`
	if after != nil {
		args = append(args, after.CreatedAt, after.ID)
		query += fmt.Sprintf(` AND (created_at, id) < ($%d::timestamptz, $%d)`, len(args)-1, len(args))
	}
	args = append(args, limit)
	query += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d`, len(args))
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing session page for user %d: %w", userID, err)
	}
	defer rows.Close()

	return scanSessions(rows)
}

// Revoke sets revoked_at to NOW() for the given session.
func (r *SessionRepository) Revoke(ctx context.Context, id string) error {
	query := `UPDATE auth_sessions SET revoked_at = NOW() WHERE id = $1`
	tag, err := r.pool.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("revoking session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// RevokeAllByUser sets revoked_at to NOW() for all active sessions owned by a user.
func (r *SessionRepository) RevokeAllByUser(ctx context.Context, userID int) error {
	query := `UPDATE auth_sessions SET revoked_at = NOW() WHERE user_id = $1 AND revoked_at IS NULL`
	if _, err := r.pool.Exec(ctx, query, userID); err != nil {
		return fmt.Errorf("revoking sessions for user %d: %w", userID, err)
	}
	return nil
}

// RevokeAllByImpersonator sets revoked_at to NOW() for all active impersonation
// sessions started by the given impersonator.
func (r *SessionRepository) RevokeAllByImpersonator(ctx context.Context, userID int) error {
	query := `UPDATE auth_sessions SET revoked_at = NOW() WHERE impersonator_user_id = $1 AND revoked_at IS NULL`
	if _, err := r.pool.Exec(ctx, query, userID); err != nil {
		return fmt.Errorf("revoking impersonation sessions for user %d: %w", userID, err)
	}
	return nil
}

// IsValid checks whether a session is active: it must exist, not be revoked
// (revoked_at IS NULL), and not be expired (expires_at > NOW()).
func (r *SessionRepository) IsValid(ctx context.Context, id string) (bool, error) {
	query := `SELECT EXISTS(
		SELECT 1 FROM auth_sessions
		WHERE id = $1 AND revoked_at IS NULL AND expires_at > NOW()
	)`
	var valid bool
	err := r.pool.QueryRow(ctx, query, id).Scan(&valid)
	if err != nil {
		return false, fmt.Errorf("checking session validity: %w", err)
	}
	return valid, nil
}

// ActiveSessionRole reports whether a session is active, as IsValid does, and
// returns the current role of the account it belongs to. Both come from one
// indexed lookup, so the per-request authentication check costs no extra
// round trip. For an impersonation session the account is the one being
// viewed as. active is false, with no error, for a missing, revoked or
// expired session.
func (r *SessionRepository) ActiveSessionRole(ctx context.Context, id string) (role string, active bool, err error) {
	query := `SELECT u.role FROM auth_sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.id = $1 AND s.revoked_at IS NULL AND s.expires_at > NOW()`
	err = r.pool.QueryRow(ctx, query, id).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("checking session validity: %w", err)
	}
	return role, true, nil
}

// UpdateLastSeen records actual request activity. The database predicate also
// coalesces writes across API nodes and prevents an older observation from
// replacing a newer one. It never extends or reactivates a session.
func (r *SessionRepository) UpdateLastSeen(ctx context.Context, id string, observedAt time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE auth_sessions SET last_seen_at = $2
		WHERE id = $1 AND revoked_at IS NULL AND expires_at > NOW()
		AND (last_seen_at IS NULL OR last_seen_at <= $2::timestamptz - INTERVAL '1 minute')`, id, observedAt)
	return err
}

// RevokeAsAdmin locks the actor and target in the same order as ownership
// transfers, so a transfer cannot race the owner-protection check. A nil
// sessionID revokes all live login sessions held by or impersonated from the
// target account, and withdraws its uncollected device sign-in approvals.
func (r *SessionRepository) RevokeAsAdmin(ctx context.Context, actorID, userID int, sessionID *string) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	rows, err := tx.Query(ctx, `SELECT id, role, enabled, is_owner FROM users
		WHERE id = ANY($1) ORDER BY id FOR UPDATE`, []int{actorID, userID})
	if err != nil {
		return 0, err
	}
	var actor, target *models.User
	for rows.Next() {
		var user models.User
		if err := rows.Scan(&user.ID, &user.Role, &user.Enabled, &user.IsOwner); err != nil {
			rows.Close()
			return 0, err
		}
		if user.ID == actorID {
			actor = &user
		}
		if user.ID == userID {
			target = &user
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if target == nil {
		return 0, ErrNotFound
	}
	if actor == nil || actor.Role != models.RoleAdmin || !actor.Enabled {
		return 0, ErrAdminProtected
	}
	if err := CheckOwnerTarget(OwnerActor{ID: actor.ID, IsOwner: actor.IsOwner}, target); err != nil {
		return 0, err
	}
	query := `UPDATE auth_sessions SET revoked_at = NOW()
		WHERE (user_id = $1 OR impersonator_user_id = $1)
		AND revoked_at IS NULL AND expires_at > NOW()`
	args := []any{userID}
	if sessionID != nil {
		query = `UPDATE auth_sessions SET revoked_at = COALESCE(revoked_at, NOW()) WHERE user_id = $1 AND id = $2`
		args = append(args, *sessionID)
	}
	tag, err := tx.Exec(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	if sessionID != nil && tag.RowsAffected() == 0 {
		return 0, ErrSessionNotFound
	}
	if sessionID == nil {
		if err := revokeAudiobookshelfSessionsInTransaction(ctx, tx, []int{userID}); err != nil {
			return 0, err
		}
		if err := withdrawDeviceSignInApprovalsInTransaction(ctx, tx, []int{userID}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ExtendExpiresAt pushes expires_at forward for an active session. The update
// only applies when the session is not revoked and has not already expired, so
// a successful call implies the session is still usable at newExpiresAt.
func (r *SessionRepository) ExtendExpiresAt(ctx context.Context, id string, newExpiresAt time.Time) error {
	query := `UPDATE auth_sessions
		SET expires_at = $2
		WHERE id = $1 AND revoked_at IS NULL AND expires_at > NOW()`
	tag, err := r.pool.Exec(ctx, query, id, newExpiresAt)
	if err != nil {
		return fmt.Errorf("extending session expiry: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// DeleteExpired removes all sessions whose expires_at is in the past,
// regardless of their revocation status. Returns the number of deleted rows.
func (r *SessionRepository) DeleteExpired(ctx context.Context) (int, error) {
	query := `DELETE FROM auth_sessions WHERE expires_at < NOW()`
	tag, err := r.pool.Exec(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("deleting expired sessions: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
