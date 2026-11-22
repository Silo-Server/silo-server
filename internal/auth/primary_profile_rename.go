package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
// default. A primary profile the household renamed keeps its name, and so
// does one whose new name another profile on the account already uses, which
// the profile API would refuse. Profiles kept in the SQLite user store are not
// in user_profiles, so they keep their names.
//
// The account row is already locked, and a profile save locks the profile row
// before it touches the account row, so waiting here for a profile a save
// holds could deadlock. A primary profile being saved at that moment is
// skipped instead and keeps its name.
func renamePrimaryProfileWithAccount(ctx context.Context, db interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}, id int, previous, next string) error {
	var profileID, name string
	var others []string
	err := db.QueryRow(ctx, `
		SELECT p.id, p.name, ARRAY(
			SELECT o.name FROM user_profiles o WHERE o.user_id = p.user_id AND o.id <> p.id
		)
		FROM user_profiles p
		WHERE p.user_id = $1 AND p.is_primary
		FOR NO KEY UPDATE OF p SKIP LOCKED`, id).Scan(&profileID, &name, &others)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading profiles for a username change: %w", err)
	}
	if !sameProfileName(name, previous) {
		return nil
	}
	for _, other := range others {
		if sameProfileName(other, next) {
			return nil
		}
	}
	if _, err := db.Exec(ctx, `
		UPDATE user_profiles SET name = $3, updated_at = NOW()
		WHERE user_id = $1 AND id = $2`, id, profileID, next); err != nil {
		return fmt.Errorf("renaming primary profile after a username change: %w", err)
	}
	return nil
}

// sameProfileName reports whether two profile names count as the same, by the
// rule of the profile API's name conflict check (profileNameConflicts in
// internal/api/handlers): trimmed and Unicode case-folded.
func sameProfileName(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
