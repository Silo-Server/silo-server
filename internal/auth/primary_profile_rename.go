package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// updatePreviousUsername returns account id's username before an update that
// sets one, locking the row so the primary profile rename that follows reads
// the name it replaces; "" for an update that leaves the username alone, and
// for an account that has none (a restored email-only account).
func updatePreviousUsername(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id int, username *string) (string, error) {
	if username == nil {
		return "", nil
	}
	var previous string
	err := db.QueryRow(ctx, `SELECT COALESCE(username, '') FROM users WHERE id=$1 FOR UPDATE`, id).Scan(&previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return previous, err
}

// renamePrimaryProfileWithAccount gives account id's primary profile the new
// username while that profile still carries the previous one, which is what
// password signup, first-run setup and admin account creation name it by
// default. The account row is already locked, so this takes the account
// before its profile, the order deleting an account takes them in. A primary
// profile the household renamed keeps its name, and so does one whose new
// name another profile on the account already uses, which the profile API
// would refuse. Names compare trimmed and case-insensitively, as that API's
// conflict check does. Profiles kept in the SQLite user store are not in
// user_profiles, so they keep their names.
func renamePrimaryProfileWithAccount(ctx context.Context, db interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, id int, previous, next string) error {
	if _, err := db.Exec(ctx, `
		UPDATE user_profiles p
		SET name = $3, updated_at = NOW()
		WHERE p.user_id = $1
		  AND p.is_primary
		  AND lower(btrim(p.name)) = lower(btrim($2))
		  AND NOT EXISTS (
			SELECT 1 FROM user_profiles o
			WHERE o.user_id = p.user_id
			  AND o.id <> p.id
			  AND lower(btrim(o.name)) = lower(btrim($3))
		  )`, id, previous, next); err != nil {
		return fmt.Errorf("renaming primary profile after a username change: %w", err)
	}
	return nil
}
