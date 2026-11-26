package auth

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/profileedits"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// Delete removes a user by their ID.
func (r *UserRepository) Delete(ctx context.Context, id int) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		// The account's profile locks first, as MutateAdminAccount takes them.
		if err := pgstore.LockAccountForImport(ctx, tx, id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, "DELETE FROM users WHERE id = $1", id)
		if err != nil {
			return fmt.Errorf("deleting user: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		// After the account, as MutateAdminAccount does.
		return profileedits.DeleteAccount(ctx, tx, id)
	})
}
