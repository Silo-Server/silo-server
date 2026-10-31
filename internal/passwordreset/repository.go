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
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Sentinel errors for reset link operations.
var (
	// ErrNotFound covers every unusable link alike: unknown, expired, used,
	// replaced, or outdated by a password or recovery email change. A probe
	// learns nothing.
	ErrNotFound = errors.New("password reset link not found")
	// ErrNotEligible reports an account that cannot sign in with a local
	// password (disabled, or managed by an external provider), or whose
	// recovery email changed since the caller read it.
	ErrNotEligible = errors.New("account cannot use a password reset")
	// ErrScopedKeyAdmin refuses a scoped API key issuing a link for an admin
	// account: the link would carry the admin's full authority.
	ErrScopedKeyAdmin = errors.New("a scoped API key may not reset an admin account's password")
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

// Issuer is the account asking for an administrator's link, as the Owner
// rules judge it when the link is stored.
type Issuer struct {
	// ID is the issuing account; 0 is no account, which may issue only for
	// an ordinary account.
	ID int
	// ScopedKey marks a request made with a scoped API key, which may not
	// issue for an admin account.
	ScopedKey bool
}

// Issue stores the digest of a new link for the account, replacing any
// earlier one: an account has at most one live link. It refuses, with the
// auth Owner-rule errors or ErrScopedKeyAdmin, an issuer that may not reset
// the account's password, and with ErrNotEligible an account that cannot
// sign in with a local password or whose email no longer matches
// expectedEmail. The issuer and the account are locked in id order, as
// ownership transfers lock them, before the link. A promotion, an ownership
// move or an email change that commits first is judged here. A promotion of
// the account, an ownership move to it, or an email change that commits later
// deletes the link in its own transaction. The audit row names the delivery
// and expiry only, never the link or its digest.
func (r *Repository) Issue(ctx context.Context, userID int, expectedEmail, tokenHash string, issuer Issuer, expiresAt time.Time, delivery Delivery) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin password reset link: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // rollback after commit is a no-op

	// The issuer, and the audit row's accounts (an impersonator included),
	// are held FOR KEY SHARE: ownership moves lock both accounts FOR UPDATE
	// before changing is_owner, so this waits for them, but not for other
	// writers of the issuer's row, such as access-group updates that lock
	// many rows in no fixed order and would otherwise deadlock with this set.
	ids := append([]int{userID}, auditmutation.ActorIDs(ctx)...)
	if issuer.ID > 0 {
		ids = append(ids, issuer.ID)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	actor := auth.OwnerActor{ID: issuer.ID}
	var target *models.User
	for _, id := range ids {
		lock := "FOR NO KEY UPDATE"
		if id != userID {
			lock = "FOR KEY SHARE"
		}
		var user models.User
		err := tx.QueryRow(ctx, `SELECT id, role, is_owner FROM users WHERE id = $1 `+lock, id).Scan(&user.ID, &user.Role, &user.IsOwner)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("locking password reset accounts: %w", err)
		}
		if id == issuer.ID {
			actor.IsOwner = user.IsOwner
		}
		if id == userID {
			target = &user
		}
	}
	if target == nil {
		return ErrNotEligible
	}
	if err := auth.CheckOwnerTarget(actor, target); err != nil {
		return err
	}
	if issuer.ScopedKey && target.Role == models.RoleAdmin {
		return ErrScopedKeyAdmin
	}

	var issuedBy *int
	if issuer.ID > 0 {
		issuedBy = new(issuer.ID)
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO password_reset_tokens (user_id, token_hash, password_fingerprint, issued_by, expires_at)
		SELECT u.id, $2, `+passwordFingerprint+`, $3, $4 FROM users u
		WHERE u.id = $1 AND u.email = $5::citext AND `+eligibleAccount+`
		ON CONFLICT (user_id) DO UPDATE SET
			token_hash = EXCLUDED.token_hash,
			password_fingerprint = EXCLUDED.password_fingerprint,
			issued_by = EXCLUDED.issued_by,
			expires_at = EXCLUDED.expires_at,
			created_at = now()`,
		userID, tokenHash, issuedBy, expiresAt, expectedEmail)
	if err != nil {
		return fmt.Errorf("issuing password reset link: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotEligible
	}
	audit, err := auditmutation.RecordMutation(ctx, tx, auditmutation.ActionPasswordResetLinkCreated, auditmutation.TargetUser,
		strconv.Itoa(userID), http.StatusCreated, auditmutation.Changes(nil, map[string]any{
			"delivery": string(delivery), "expires_at": expiresAt.UTC().Format(time.RFC3339),
		}))
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit password reset link: %w", err)
	}
	auditmutation.CommitMutation(ctx, audit)
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
// not an error, for an account that cannot use a reset or whose email changed.
func (r *Repository) IssueUnlessRecent(ctx context.Context, userID int, expectedEmail, tokenHash string, expiresAt time.Time, minAge time.Duration) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		WITH account AS (
			SELECT u.id, u.password_hash FROM users u
			WHERE u.id = $1 AND u.email = $5::citext AND `+eligibleAccount+` FOR NO KEY UPDATE
		)
		INSERT INTO password_reset_tokens (user_id, token_hash, password_fingerprint, issued_by, expires_at)
		SELECT u.id, $2, `+passwordFingerprint+`, u.id, $3 FROM account u
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
		userID, tokenHash, expiresAt, minAge.Seconds(), expectedEmail)
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
// the account row serializes the reset with password and recovery email changes.
// The final wall-clock expiry check is the reset's linearization point.
func (r *Repository) Complete(ctx context.Context, tokenHash, newPassword string) (*models.User, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin password reset: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // rollback after commit is a no-op

	// Lock the account before the link, the order issuance, promotion and email
	// changes take them in, so completing a link cannot deadlock against them.
	// The claim below re-checks the link.
	var userID int
	err = tx.QueryRow(ctx, `SELECT user_id FROM password_reset_tokens WHERE token_hash = $1`, tokenHash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("finding password reset link: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, userID); err != nil {
		return nil, fmt.Errorf("locking password reset account: %w", err)
	}
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
	audit, err := auditmutation.RecordMutation(ctx, tx, auditmutation.ActionPasswordResetCompleted, auditmutation.TargetUser,
		strconv.Itoa(userID), http.StatusOK, auditmutation.SecretChanged(auditmutation.FieldPassword))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit password reset: %w", err)
	}
	auditmutation.CommitMutation(ctx, audit)
	return user, nil
}
