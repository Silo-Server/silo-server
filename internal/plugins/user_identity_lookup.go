package plugins

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UserIdentity is the public-facing identity surfaced to plugins via the
// X-Silo-User-Name / X-Silo-Profile-Name / X-Silo-Profile-Primary
// headers. Plugins use it to render "user#profile" strings in app-pairing
// UIs without reaching back into the browser's localStorage.
type UserIdentity struct {
	Username         string
	Email            string
	ProfileName      string
	ProfileIsPrimary bool
}

// UserIdentityLookup resolves a user id + active profile id into a
// presentable identity. Either field may be returned empty when the row is
// missing — callers omit the corresponding header.
type UserIdentityLookup interface {
	LookupIdentity(ctx context.Context, userID int, profileID string) (UserIdentity, error)
}

// PgUserIdentityLookup reads username from `users` and profile name +
// is_primary from `user_profiles`. Hits the same tables silo's web
// client uses for `/auth/me` and `/profiles`, so the plugin proxy returns
// the same names operators see in the main UI.
type PgUserIdentityLookup struct {
	pool *pgxpool.Pool
}

func NewPgUserIdentityLookup(pool *pgxpool.Pool) *PgUserIdentityLookup {
	return &PgUserIdentityLookup{pool: pool}
}

func (l *PgUserIdentityLookup) LookupIdentity(ctx context.Context, userID int, profileID string) (UserIdentity, error) {
	var out UserIdentity
	if l == nil || l.pool == nil || userID <= 0 {
		return out, nil
	}

	err := l.pool.QueryRow(ctx,
		"SELECT username, COALESCE(email, '') FROM users WHERE id = $1", userID,
	).Scan(&out.Username, &out.Email)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}

	if profileID == "" {
		return out, nil
	}
	// Verify the profile belongs to this user before returning its name —
	// otherwise a client could stamp another user's profile_id and have the
	// plugin display someone else's profile name.
	err = l.pool.QueryRow(ctx,
		"SELECT name, is_primary FROM user_profiles WHERE id = $1 AND user_id = $2",
		profileID, userID,
	).Scan(&out.ProfileName, &out.ProfileIsPrimary)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	return out, nil
}

// RequesterIdentityFromLookup adapts a UserIdentityLookup into the requests
// service's requester-identity resolver (email + username by user id).
type requesterIdentityAdapter struct{ lookup UserIdentityLookup }

func RequesterIdentityFromLookup(l UserIdentityLookup) *requesterIdentityAdapter {
	return &requesterIdentityAdapter{lookup: l}
}

func (a *requesterIdentityAdapter) ResolveRequester(ctx context.Context, userID int) (string, string, error) {
	if a == nil || a.lookup == nil || userID <= 0 {
		return "", "", nil
	}
	id, err := a.lookup.LookupIdentity(ctx, userID, "")
	if err != nil {
		return "", "", err
	}
	return id.Email, id.Username, nil
}
