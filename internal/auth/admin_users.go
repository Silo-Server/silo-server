package auth

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/profileedits"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
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
	// A deletion takes the account's profile locks before its row, as every
	// profile writer and the profile set take them first, so it runs before
	// or after them, never between a write and the edit it records. A
	// username change takes them too: it may rename the primary profile,
	// and checks the new name against the other profiles under them, as a
	// profile create, a profile rename, the profile set and the owner's
	// clear of a name do. They are taken whenever
	// the update carries a username, even one equal to the current name.
	if input == nil || input.Username != nil {
		if err = pgstore.LockAccountForImport(ctx, tx, id); err != nil {
			return AdminUserSnapshot{}, err
		}
	}
	// The revision is compared on the row read under those locks, so an
	// update that waited for them is judged on what is true when it runs.
	current, err := adminUserSnapshot(tx.QueryRow(ctx, `SELECT `+allColumns+`, admin_revision FROM users WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return AdminUserSnapshot{}, err
	}
	if revision != -1 && current.Revision != revision {
		return current, ErrAdminUserRevision
	}
	if input == nil {
		if err = lockIdentitiesBeforeSessions(ctx, tx, id, true); err != nil {
			return current, err
		}
	}
	before := current.User
	revoke, err := validate(current.User, tx)
	if err != nil {
		return current, err
	}
	// App keys go before sessions, the order every sweep takes. Deleting
	// the account deletes them outright, ending the sessions they opened by
	// cascade; a promotion revokes them (revokeCredentialsIssuedToNonAdmin).
	switch {
	case input == nil:
		// Every session of the account, in id order, before the deletes
		// whose cascades end them.
		if err = lockSessionsInOrder(ctx, tx, `s.user_id = $1 OR s.impersonator_user_id = $1`, id); err != nil {
			return current, err
		}
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
		if err == nil {
			// After the account, so the delete sees every edit a writer it
			// waited on committed; the table has no foreign key to cascade.
			err = profileedits.DeleteAccount(ctx, tx, id)
		}
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
