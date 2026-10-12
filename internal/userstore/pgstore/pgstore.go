package pgstore

import (
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// PostgresUserStore implements userstore.UserStore using shared Postgres tables.
// Each instance is scoped to a specific user via the userID field.
type PostgresUserStore struct {
	pool   *pgxpool.Pool
	userID int
}

// Compile-time interface check.
var _ userstore.UserStore = (*PostgresUserStore)(nil)
var _ userstore.DeviceRegistry = (*PostgresUserStore)(nil)
var _ userstore.WatchedBatchWriter = (*PostgresUserStore)(nil)
var _ userstore.SectionOverrideEnumerator = (*PostgresUserStore)(nil)

// newStore creates a PostgresUserStore scoped to a user.
func newStore(pool *pgxpool.Pool, userID int) *PostgresUserStore {
	return &PostgresUserStore{pool: pool, userID: userID}
}

// profilePlanArgs plans a profile-scoped read for its bound values on every
// execution. A cached statement switches to a generic plan after five runs,
// and the generic plan's row estimate for (user_id, profile_id) is the
// average across profiles: for a profile with a long watch history it
// materializes every history row instead of probing the item index, turning
// a millisecond lookup into hundreds of milliseconds.
func profilePlanArgs(args ...any) []any {
	return append([]any{pgx.QueryExecModeCacheDescribe}, args...)
}

func generateUUID() string {
	return uuid.New().String()
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// timeToString formats a time.Time as an RFC3339 string.
// Used when scanning TIMESTAMPTZ columns from Postgres into string fields.
func timeToString(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}
