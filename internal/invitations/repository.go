// Package invitations implements emailed, pre-provisioned invitations: a
// single-use capability token bound to one email address, carrying the
// access decisions the admin made at send time. The invitee only chooses a
// password; their email address becomes their username.
//
// Design: docs/architecture/invitations-onboarding.md
package invitations

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Sentinel errors for invitation operations.
var (
	ErrNotFound = errors.New("invitation not found")
	// ErrNotClaimable is returned by Accept when the row exists but is no
	// longer pending (accepted, revoked, or expired). The public API maps
	// both errors to the same response so a token probe learns nothing.
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
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate invitation token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, HashToken(token), nil
}

// HashToken returns the at-rest digest of a claim token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
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
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	_, err = tx.Exec(ctx, `
		UPDATE invitations SET revoked_at = now(), updated_at = now()
		WHERE email = $1 AND accepted_at IS NULL AND revoked_at IS NULL`,
		input.Email)
	if err != nil {
		return nil, fmt.Errorf("superseding prior invitation: %w", err)
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
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create invitation: %w", err)
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

// Accept atomically claims a pending, unexpired invitation for the given
// user. The WHERE predicate is the concurrency guard: of two concurrent
// accepts, exactly one matches. Returns ErrNotClaimable when the row exists
// but is already accepted, revoked, or expired.
func (r *Repository) Accept(ctx context.Context, tokenHash string, userID int) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE invitations
		SET accepted_at = now(), accepted_user_id = $2, updated_at = now()
		WHERE token_hash = $1
		  AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()`,
		tokenHash, userID)
	if err != nil {
		return fmt.Errorf("accepting invitation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		if _, err := r.GetByTokenHash(ctx, tokenHash); err != nil {
			return ErrNotFound
		}
		return ErrNotClaimable
	}
	return nil
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
