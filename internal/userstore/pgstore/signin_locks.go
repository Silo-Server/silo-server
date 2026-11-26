package pgstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
)

// LockSignInIdentities locks one account's provider identities in id order:
// FOR UPDATE when forDelete, otherwise FOR KEY SHARE, which only stops an
// uninstall or unlink from deleting them meanwhile. It is the identity step
// of the lock order a plugin uninstall meets ("Lock order with a plugin
// uninstall" in docs/architecture/external-sign-in.md); the auth package
// takes it through lockIdentitiesBeforeSessions, and a profile delete here
// takes it before the profile's row goes.
func LockSignInIdentities(ctx context.Context, db interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, userID int, forDelete bool) error {
	lock := "FOR KEY SHARE"
	if forDelete {
		lock = "FOR UPDATE"
	}
	if _, err := db.Exec(ctx, `SELECT id FROM plugin_auth_identities WHERE user_id = $1 ORDER BY id `+lock, userID); err != nil {
		return fmt.Errorf("locking provider identities: %w", err)
	}
	return nil
}

// lockAccountForProfileDelete takes a KEY SHARE lock on the account row, and
// on the audit row's accounts when withActors (auditmutation.LockActors), in
// one id order. Every transaction that writes the account's sign-in requests
// or sessions holds the account row FOR UPDATE, so a profile delete, which
// clears the requests approved for the profile, runs before or after it. It
// comes before the profile's row: approving a request for a profile locks the
// account row and then, through the request's foreign key, the profile row.
func lockAccountForProfileDelete(ctx context.Context, tx pgx.Tx, userID int, withActors bool) error {
	ids := []int{userID}
	if withActors {
		ids = append(ids, auditmutation.ActorIDs(ctx)...)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = ANY($1) ORDER BY id FOR KEY SHARE`, ids); err != nil {
		return fmt.Errorf("locking the account: %w", err)
	}
	return nil
}
