package userstore

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// UserStoreProvider returns a UserStore scoped to a specific user.
// For SQLite, this returns a store wrapping the per-user SQLite DB from the pool.
// For Postgres, this returns a store scoped to the user_id in shared tables.
type UserStoreProvider interface {
	ForUser(ctx context.Context, userID int) (UserStore, error)
	Close() error
}

// SectionOverrideEnumerator is the optional read-only capability used by
// account-wide maintenance tasks that must discover profile-created sections.
// Both production user-store implementations provide it.
type SectionOverrideEnumerator interface {
	ListAllSectionOverrides(ctx context.Context) ([]SectionOverride, error)
}

// SectionProfileResetProvider reports whether every account's section overrides
// live in the supplied PostgreSQL transaction domain. Callers must check the
// result; implementing this interface alone does not advertise support.
// Decorators may forward it only when they preserve the underlying provider's
// storage for every account. Mixed providers must not infer it from one store.
type SectionProfileResetProvider interface {
	SupportsAtomicSectionProfileReset(pool *pgxpool.Pool) bool
}
