// Package passwordreset implements password reset links: a single-use,
// time-limited capability to replace one account's local password, so an
// administrator can help a locked-out account without handling its password,
// and, when the server allows it, an account holder can reset their own from
// the sign-in page.
//
// Invariants: docs/architecture/password-resets.md
package passwordreset

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Sentinel errors for reset link operations.
var (
	// ErrNotFound covers every unusable link alike: unknown, expired, used,
	// replaced, or outdated by a password change. A probe learns nothing.
	ErrNotFound = errors.New("password reset link not found")
	// ErrNotEligible reports an account that cannot sign in with a local
	// password (disabled, or managed by an external provider).
	ErrNotEligible = errors.New("account cannot use a password reset")
)

// The account state a link needs, both to be issued and to be used, over the
// users alias u.
const eligibleAccount = `u.enabled AND u.local_password_login_enabled AND u.password_hash <> ''`

// passwordFingerprint is the digest of the account's current password hash.
// A link records it at issue time and only works while it still matches, so a
// password changed any other way retires the link.
const passwordFingerprint = `encode(sha256(convert_to(u.password_hash, 'UTF8')), 'hex')`

// usableLink selects the live link with digest $1 and its account (aliases t
// and u). now is the SQL clock the expiry is judged by.
func usableLink(now string) string {
	return ` FROM password_reset_tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = $1 AND t.expires_at > ` + now + `
		AND t.password_fingerprint = ` + passwordFingerprint + ` AND ` + eligibleAccount
}

// Repository owns the password_reset_tokens table.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a Repository backed by the given pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Link is a usable reset link as its landing screen needs it.
type Link struct {
	UserID    int
	Username  string
	ExpiresAt time.Time
}

// Issue stores the digest of a new link for the account, replacing any
// earlier one: an account has at most one live link. It refuses an account
// that cannot sign in with a local password.
func (r *Repository) Issue(ctx context.Context, userID int, tokenHash string, issuedBy *int, expiresAt time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO password_reset_tokens (user_id, token_hash, password_fingerprint, issued_by, expires_at)
		SELECT u.id, $2, `+passwordFingerprint+`, $3, $4 FROM users u WHERE u.id = $1 AND `+eligibleAccount+`
		ON CONFLICT (user_id) DO UPDATE SET
			token_hash = EXCLUDED.token_hash,
			password_fingerprint = EXCLUDED.password_fingerprint,
			issued_by = EXCLUDED.issued_by,
			expires_at = EXCLUDED.expires_at,
			created_at = now()`,
		userID, tokenHash, issuedBy, expiresAt)
	if err != nil {
		return fmt.Errorf("issuing password reset link: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotEligible
	}
	return nil
}

// IssueUnlessRecent is Issue for a link the account holder asked for
// themselves, recorded with the account as its own issuer. An administrator's
// link keeps another issuer, or none once that administrator is deleted, so
// it stays protected either way. It reports whether it stored a new
// link, and leaves the account's link alone when that link is younger than
// minAge, so repeated requests can neither flood the mailbox nor keep
// replacing a link that was just sent. Nor does it replace a live link an
// administrator issued, which anyone who knows the account name could
// otherwise retire. The checks and the replacement are one statement, so
// concurrent requests across nodes still store one link. It reports false,
// not an error, for an account that cannot use a reset.
func (r *Repository) IssueUnlessRecent(ctx context.Context, userID int, tokenHash string, expiresAt time.Time, minAge time.Duration) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO password_reset_tokens (user_id, token_hash, password_fingerprint, issued_by, expires_at)
		SELECT u.id, $2, `+passwordFingerprint+`, u.id, $3 FROM users u WHERE u.id = $1 AND `+eligibleAccount+`
		ON CONFLICT (user_id) DO UPDATE SET
			token_hash = EXCLUDED.token_hash,
			password_fingerprint = EXCLUDED.password_fingerprint,
			issued_by = EXCLUDED.issued_by,
			expires_at = EXCLUDED.expires_at,
			created_at = now()
		WHERE password_reset_tokens.created_at <= now() - make_interval(secs => $4)
			AND (password_reset_tokens.issued_by = password_reset_tokens.user_id
				OR password_reset_tokens.expires_at <= now()
				OR password_reset_tokens.password_fingerprint <> EXCLUDED.password_fingerprint)`,
		userID, tokenHash, expiresAt, minAge.Seconds())
	if err != nil {
		return false, fmt.Errorf("issuing requested password reset link: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// Withdraw deletes the account's link if it is still the one with digest
// tokenHash, so a link whose email was never sent does not hold the cooldown.
func (r *Repository) Withdraw(ctx context.Context, userID int, tokenHash string) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM password_reset_tokens WHERE user_id = $1 AND token_hash = $2`, userID, tokenHash); err != nil {
		return fmt.Errorf("withdrawing password reset link: %w", err)
	}
	return nil
}

// Lookup resolves a usable link to its account.
func (r *Repository) Lookup(ctx context.Context, tokenHash string) (*Link, error) {
	var link Link
	err := r.pool.QueryRow(ctx, `SELECT u.id, u.username, t.expires_at`+usableLink("now()"), tokenHash).
		Scan(&link.UserID, &link.Username, &link.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("looking up password reset link: %w", err)
	}
	return &link, nil
}

// Complete spends a usable link: the link is deleted, the password replaced,
// and every session of the account revoked, all in one transaction. Locking
// the account row serializes the reset with any other password change, and
// the final wall-clock expiry check is the reset's linearization point.
func (r *Repository) Complete(ctx context.Context, tokenHash, newPassword string) (*models.User, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin password reset: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // rollback after commit is a no-op

	var userID int
	err = tx.QueryRow(ctx, `SELECT t.user_id`+usableLink("clock_timestamp()")+` FOR UPDATE OF t, u`, tokenHash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("claiming password reset link: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM password_reset_tokens WHERE user_id = $1`, userID); err != nil {
		return nil, fmt.Errorf("spending password reset link: %w", err)
	}
	if err := auth.ResetPasswordInTransaction(ctx, tx, userID, newPassword); err != nil {
		return nil, err
	}
	user, err := auth.UserInTransaction(ctx, tx, userID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit password reset: %w", err)
	}
	return user, nil
}
