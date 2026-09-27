// Package invitations implements emailed, pre-provisioned invitations: a
// single-use capability token bound to one email address, carrying the
// access decisions the admin made at send time. The invitee only chooses a
// password; their email address becomes their username.
//
// Design: docs/architecture/invitations-onboarding.md
package invitations

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

// Sentinel errors for invitation operations.
var (
	ErrNotFound = errors.New("invitation not found")
	// ErrNotClaimable reports a lost final claim or stale resend admission.
	// Initial public token lookup/accept eligibility uses ErrNotFound.
	ErrNotClaimable = errors.New("invitation is no longer claimable")
)

// Repository owns the invitations table.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a Repository backed by the given pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// NewToken mints a raw claim token and its SHA-256 hex digest for at-rest
// storage. The raw token is embedded in the emailed link and never stored.
func NewToken() (token, tokenHash string, err error) {
	token, tokenHash, err = auth.NewLinkToken()
	if err != nil {
		return "", "", fmt.Errorf("generate invitation token: %w", err)
	}
	return token, tokenHash, nil
}

// HashToken returns the at-rest digest of a claim token.
func HashToken(token string) string {
	return auth.HashLinkToken(token)
}

// invitationColumns are the table columns plus the joined inviter name.
const invitationColumns = `
	i.id, i.email, i.token_hash, i.role, i.access_group_id, i.library_ids,
	i.create_profile, i.show_tour, i.note, i.invited_by,
	COALESCE(u.username, ''),
	i.expires_at, i.accepted_at, i.accepted_user_id, i.revoked_at,
	i.created_at, i.updated_at`

const invitationFrom = ` FROM invitations i LEFT JOIN users u ON u.id = i.invited_by `

func scanInvitation(row pgx.Row) (*models.Invitation, error) {
	var inv models.Invitation
	err := row.Scan(
		&inv.ID, &inv.Email, &inv.TokenHash, &inv.Role, &inv.AccessGroupID,
		&inv.LibraryIDs, &inv.CreateProfile, &inv.ShowTour, &inv.Note,
		&inv.InvitedBy, &inv.InvitedByName,
		&inv.ExpiresAt, &inv.AcceptedAt, &inv.AcceptedUserID, &inv.RevokedAt,
		&inv.CreatedAt, &inv.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scanning invitation: %w", err)
	}
	return &inv, nil
}

// Create inserts a new invitation, first revoking any live invitation for
// the same address so "re-invite supersedes" holds atomically (backed by the
// invitations_one_pending_idx partial unique index). Returns the stored row;
// the raw token is the caller's to deliver and is not retrievable later.
func (r *Repository) Create(ctx context.Context, input models.CreateInvitationInput, tokenHash string) (*models.Invitation, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create invitation: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // rollback after commit is a no-op

	inv, err := createInvitation(ctx, tx, input, tokenHash)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create invitation: %w", err)
	}
	return inv, nil
}

// Resend only replaces the requested current invitation. A stale administrator
// request cannot revive revoked history or supersede a newer link.
func (r *Repository) Resend(ctx context.Context, id int64, input models.CreateInvitationInput, tokenHash string) (*models.Invitation, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	prior, err := scanInvitation(tx.QueryRow(ctx, `SELECT `+invitationColumns+invitationFrom+`WHERE i.id=$1 FOR UPDATE OF i`, id))
	if err != nil {
		return nil, err
	}
	if prior.AcceptedAt != nil || prior.RevokedAt != nil {
		return nil, ErrNotClaimable
	}
	// Access choices come from the locked source, never a stale service read.
	input.Email, input.Role, input.AccessGroupID = prior.Email, prior.Role, prior.AccessGroupID
	input.LibraryIDs, input.CreateProfile, input.ShowTour, input.Note = prior.LibraryIDs, prior.CreateProfile, prior.ShowTour, prior.Note
	inv, err := createInvitation(ctx, tx, input, tokenHash)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit resend invitation: %w", err)
	}
	return inv, nil
}

func createInvitation(ctx context.Context, tx pgx.Tx, input models.CreateInvitationInput, tokenHash string) (*models.Invitation, error) {
	// Lock/supersede first, so an acceptance winning this row lock is visible
	// to the following account check. A failure rolls the supersession back.
	_, err := tx.Exec(ctx, `UPDATE invitations SET revoked_at=clock_timestamp(), updated_at=clock_timestamp()
 WHERE email=$1 AND accepted_at IS NULL AND revoked_at IS NULL`, input.Email)
	if err != nil {
		return nil, fmt.Errorf("superseding prior invitation: %w", err)
	}
	var taken bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE email=$1 OR username=$2)`, auth.NormalizeEmail(input.Email), auth.NormalizeUsername(input.Email)).Scan(&taken); err != nil {
		return nil, err
	}
	if taken {
		return nil, ErrEmailTaken
	}
	row := tx.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO invitations (
				email, token_hash, role, access_group_id, library_ids,
				create_profile, show_tour, note, invited_by, expires_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			RETURNING *
		)
		SELECT `+invitationColumns+` FROM inserted i LEFT JOIN users u ON u.id = i.invited_by`,
		input.Email, tokenHash, input.Role, input.AccessGroupID, input.LibraryIDs,
		input.CreateProfile, input.ShowTour, input.Note, input.InvitedBy, input.ExpiresAt,
	)
	inv, err := scanInvitation(row)
	if err != nil {
		return nil, err
	}
	return inv, nil
}

// GetByID retrieves an invitation by its numeric ID.
func (r *Repository) GetByID(ctx context.Context, id int64) (*models.Invitation, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+invitationColumns+invitationFrom+`WHERE i.id = $1`, id)
	return scanInvitation(row)
}

// GetByTokenHash retrieves an invitation by the digest of its claim token.
func (r *Repository) GetByTokenHash(ctx context.Context, tokenHash string) (*models.Invitation, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+invitationColumns+invitationFrom+`WHERE i.token_hash = $1`, tokenHash)
	return scanInvitation(row)
}

// List returns all invitations, newest first.
func (r *Repository) List(ctx context.Context) ([]*models.Invitation, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+invitationColumns+invitationFrom+`ORDER BY i.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("listing invitations: %w", err)
	}
	defer rows.Close()

	var invitations []*models.Invitation
	for rows.Next() {
		inv, err := scanInvitation(rows)
		if err != nil {
			return nil, err
		}
		invitations = append(invitations, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating invitations: %w", err)
	}
	return invitations, nil
}

// Accept serializes token eligibility with account/profile provisioning. The
// final wall-clock expiry check is the claim's linearization point; all effects
// roll back together if the invitation expires while the transaction waits.
func (r *Repository) Accept(ctx context.Context, tokenHash string, provision func(*models.Invitation, pgx.Tx) (*models.User, error)) (*models.User, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	inv, err := scanInvitation(tx.QueryRow(ctx, `SELECT `+invitationColumns+invitationFrom+`WHERE i.token_hash=$1 FOR UPDATE OF i`, tokenHash))
	if err != nil {
		return nil, err
	}
	var eligible bool
	if err := tx.QueryRow(ctx, `SELECT accepted_at IS NULL AND revoked_at IS NULL AND expires_at > clock_timestamp() FROM invitations WHERE id=$1`, inv.ID).Scan(&eligible); err != nil {
		return nil, err
	}
	if !eligible {
		return nil, ErrNotFound
	}
	user, err := provision(inv, tx)
	if err != nil {
		return nil, err
	}
	tag, err := tx.Exec(ctx, `UPDATE invitations SET accepted_at=clock_timestamp(), accepted_user_id=$2, updated_at=clock_timestamp()
 WHERE id=$1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > clock_timestamp()`, inv.ID, user.ID)
	if err != nil {
		return nil, fmt.Errorf("accepting invitation: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return nil, ErrNotClaimable
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit accept invitation: %w", err)
	}
	return user, nil
}

// Revoke marks an invitation revoked. Idempotent: revoking an already
// revoked or accepted invitation succeeds without changing its state.
func (r *Repository) Revoke(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE invitations SET revoked_at = now(), updated_at = now()
		WHERE id = $1 AND accepted_at IS NULL AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("revoking invitation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		if _, err := r.GetByID(ctx, id); err != nil {
			return ErrNotFound
		}
	}
	return nil
}

// Delete removes an invitation row entirely. Used by admins to clear
// history; revocation is the normal path.
func (r *Repository) Delete(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM invitations WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting invitation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// PageKey preserves database precision when continuing the administrator list.
type PageKey struct {
	CreatedAt time.Time `json:"created_at"`
	ID        int64     `json:"id"`
}

// ListPage returns a bounded keyset page without loading the entire history.
func (r *Repository) ListPage(ctx context.Context, after *PageKey, limit int) ([]*models.Invitation, bool, error) {
	if limit < 1 || limit > 200 {
		return nil, false, errors.New("invalid invitation page limit")
	}
	query := `SELECT ` + invitationColumns + invitationFrom
	args := []any{limit + 1}
	if after != nil {
		query += `WHERE (i.created_at,i.id) < ($2,$3) `
		args = append(args, after.CreatedAt, after.ID)
	}
	rows, err := r.pool.Query(ctx, query+`ORDER BY i.created_at DESC,i.id DESC LIMIT $1`, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := make([]*models.Invitation, 0, limit+1)
	for rows.Next() {
		inv, err := scanInvitation(rows)
		if err != nil {
			return nil, false, err
		}
		result = append(result, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(result) > limit
	if more {
		result = result[:limit]
	}
	return result, more, nil
}
