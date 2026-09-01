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

type sessionQueryExecQuerier interface {
	sessionExecQuerier
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// SessionAuthorization identifies the non-impersonated native session whose
// authority permits creating another durable credential.
type SessionAuthorization struct {
	UserID       int
	SessionID    string
	RequireAdmin bool
}

// ErrSessionNotFound is returned when a session ID does not exist.
var ErrSessionNotFound = errors.New("session not found")

// IsSessionNotFound returns true if the error is a "session not found" error.
func IsSessionNotFound(err error) bool {
	return errors.Is(err, ErrSessionNotFound)
}

// sessionColumns is the list of columns returned by all session SELECT queries.
const sessionColumns = `id, user_id, device_name, COALESCE(host(ip_address), '') AS ip_address,
	created_at, expires_at, revoked_at, auth_revision,
	impersonator_user_id, impersonator_auth_revision, impersonation_started_at`

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
		&s.IPAddress,
		&s.CreatedAt,
		&s.ExpiresAt,
		&s.RevokedAt,
		&s.AuthRevision,
		&s.ImpersonatorUserID,
		&s.ImpersonatorAuthRevision,
		&s.ImpersonationStartedAt,
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
			&s.IPAddress,
			&s.CreatedAt,
			&s.ExpiresAt,
			&s.RevokedAt,
			&s.AuthRevision,
			&s.ImpersonatorUserID,
			&s.ImpersonatorAuthRevision,
			&s.ImpersonationStartedAt,
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
// is generated via crypto/rand (through github.com/google/uuid).
func (r *SessionRepository) Create(ctx context.Context, session models.AuthSession) error {
	return r.createWithQuerier(ctx, r.pool, session)
}

// CreateAuthorized inserts a new session only while the authorizing native
// session and the subject's authority snapshot are current. The transaction
// locks both user rows, serializing credential creation with authority-reset
// mutations: whichever commits second observes and honors the first.
func (r *SessionRepository) CreateAuthorized(
	ctx context.Context,
	session models.AuthSession,
	authorization SessionAuthorization,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin authorized session creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := r.createAuthorizedWithQuerier(ctx, tx, session, authorization); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit authorized session creation: %w", err)
	}
	return nil
}

func (r *SessionRepository) createAuthorizedWithQuerier(
	ctx context.Context,
	db sessionQueryExecQuerier,
	session models.AuthSession,
	authorization SessionAuthorization,
) error {
	if err := validateSessionAuthorization(ctx, db, authorization, session.UserID, session.AuthRevision); err != nil {
		return err
	}
	return r.createWithQuerier(ctx, db, session)
}

func validateSessionAuthorization(
	ctx context.Context,
	db sessionQueryExecQuerier,
	authorization SessionAuthorization,
	subjectUserID int,
	subjectAuthRevision int64,
) error {
	var sessionID string
	err := db.QueryRow(ctx, `
		SELECT source.id
		FROM auth_sessions AS source
		JOIN users AS authorizer ON authorizer.id = source.user_id
		JOIN users AS subject ON subject.id = $4
		WHERE source.id = $1
		  AND source.user_id = $2
		  AND source.revoked_at IS NULL
		  AND source.expires_at > NOW()
		  AND source.impersonator_user_id IS NULL
		  AND authorizer.enabled
		  AND authorizer.auth_revision = source.auth_revision
		  AND (NOT $3 OR authorizer.role = 'admin')
		  AND subject.enabled
		  AND subject.auth_revision = $5
		FOR UPDATE OF authorizer, subject
	`, authorization.SessionID, authorization.UserID, authorization.RequireAdmin, subjectUserID, subjectAuthRevision).Scan(&sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSessionNotFound
		}
		return fmt.Errorf("validating session authorization: %w", err)
	}
	return nil
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

	query := `INSERT INTO auth_sessions
		(id, user_id, device_name, ip_address, expires_at, auth_revision,
		 impersonator_user_id, impersonator_auth_revision, impersonation_started_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

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
		session.AuthRevision,
		session.ImpersonatorUserID,
		session.ImpersonatorAuthRevision,
		session.ImpersonationStartedAt,
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
		SELECT 1
		FROM auth_sessions AS session
		JOIN users AS account ON account.id = session.user_id
		LEFT JOIN users AS impersonator ON impersonator.id = session.impersonator_user_id
		WHERE session.id = $1
		  AND session.revoked_at IS NULL
		  AND session.expires_at > NOW()
		  AND account.enabled
		  AND account.auth_revision = session.auth_revision
		  AND (
			session.impersonator_user_id IS NULL OR (
				impersonator.enabled
				AND impersonator.role = 'admin'
				AND impersonator.auth_revision = session.impersonator_auth_revision
			)
		  )
	)`
	var valid bool
	err := r.pool.QueryRow(ctx, query, id).Scan(&valid)
	if err != nil {
		return false, fmt.Errorf("checking session validity: %w", err)
	}
	return valid, nil
}

// ExtendExpiresAt pushes expires_at forward for an active session. The update
// only applies when the session is not revoked and has not already expired, so
// a successful call implies the session is still usable at newExpiresAt.
func (r *SessionRepository) ExtendExpiresAt(ctx context.Context, id string, newExpiresAt time.Time) error {
	query := `UPDATE auth_sessions AS session
		SET expires_at = $2
		WHERE session.id = $1
		  AND session.revoked_at IS NULL
		  AND session.expires_at > NOW()
		  AND EXISTS (
			SELECT 1
			FROM users AS account
			LEFT JOIN users AS impersonator ON impersonator.id = session.impersonator_user_id
			WHERE account.id = session.user_id
			  AND account.enabled
			  AND account.auth_revision = session.auth_revision
			  AND (
				session.impersonator_user_id IS NULL OR (
					impersonator.enabled
					AND impersonator.role = 'admin'
					AND impersonator.auth_revision = session.impersonator_auth_revision
				)
			  )
		  )`
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
