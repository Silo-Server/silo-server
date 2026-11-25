package auth

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
)

var ErrAdminUserRevision = errors.New("administrator account revision changed")

type AdminUserSnapshot struct {
	User     *models.User
	Revision int64
}
type adminUserRow struct {
	pgx.Row
	revision *int64
}

func (r adminUserRow) Scan(dest ...any) error { return r.Row.Scan(append(dest, r.revision)...) }
func adminUserSnapshot(row pgx.Row) (AdminUserSnapshot, error) {
	var result AdminUserSnapshot
	user, err := scanUser(adminUserRow{Row: row, revision: &result.Revision})
	result.User = user
	return result, err
}
func (r *UserRepository) GetAdminSnapshot(ctx context.Context, id int) (AdminUserSnapshot, error) {
	return adminUserSnapshot(r.pool.QueryRow(ctx, `SELECT `+allColumns+`, admin_revision FROM users WHERE id=$1`, id))
}

// MutateAdminAccount holds the configuration precondition, target-dependent
// validation and session revocation in the same transaction. A nil input deletes.
// Group locking precedes account locking, matching group policy propagation.
func (r *UserRepository) MutateAdminAccount(ctx context.Context, id int, revision int64, input *models.UpdateUserInput, validate func(*models.User, pgx.Tx) (bool, error)) (AdminUserSnapshot, error) {
	passwordHash, err := hashUpdatePassword(ctx, input)
	if err != nil {
		return AdminUserSnapshot{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return AdminUserSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err = tx.Exec(ctx, `LOCK TABLE access_groups IN SHARE MODE`); err != nil {
		return AdminUserSnapshot{}, err
	}
	current, err := adminUserSnapshot(tx.QueryRow(ctx, `SELECT `+allColumns+`, admin_revision FROM users WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return AdminUserSnapshot{}, err
	}
	if revision != -1 && current.Revision != revision {
		return current, ErrAdminUserRevision
	}
	before := current.User
	revoke, err := validate(current.User, tx)
	if err != nil {
		return current, err
	}
	// App keys go before sessions, the order every sweep takes. Deleting
	// the account deletes them outright: revoked rows would be updated again
	// by the identity trigger when the delete cascades to the account's
	// identities, and that second update re-checks a user_id whose row is
	// already gone. A promotion revokes them
	// (revokeCredentialsIssuedToNonAdmin).
	switch {
	case input == nil:
		if err = deleteAccountKeysTx(ctx, tx, id); err != nil {
			return current, err
		}
	case promotes(before, input):
		if err = revokeAccountKeysTx(ctx, tx, id); err != nil {
			return current, err
		}
	}
	if input == nil || revoke {
		if err = RevokeSignInsInTransaction(ctx, tx, id); err != nil {
			return current, err
		}
	}
	if input == nil {
		_, err = tx.Exec(ctx, `DELETE FROM users WHERE id=$1`, id)
	} else {
		err = updateUser(ctx, tx, id, *input, passwordHash)
		if err == nil {
			current, err = adminUserSnapshot(tx.QueryRow(ctx, `SELECT `+allColumns+`, admin_revision FROM users WHERE id=$1`, id))
		}
	}
	if err != nil {
		return current, err
	}
	after := current.User
	if input == nil {
		after = nil
	}
	audit, err := auditmutation.RecordUserMutation(ctx, tx, before, after, input != nil && input.Password != nil)
	if err != nil {
		return current, err
	}
	if err = tx.Commit(ctx); err != nil {
		return current, err
	}
	auditmutation.CommitMutation(ctx, audit)
	return current, nil
}

// promotes reports whether input makes a non-admin an admin.
func promotes(before *models.User, input *models.UpdateUserInput) bool {
	return input != nil && input.Role != nil && *input.Role == models.RoleAdmin && before != nil && before.Role != models.RoleAdmin
}
