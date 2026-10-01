package pgstore

import (
	"time"

	"github.com/google/uuid"
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

// SupportsCatalogPlayableTargets allows catalog to select profile-aware
// playback targets in SQL only when both repositories share this account's
// database. Separate databases and other progress backends use their own reads.
func (s *PostgresUserStore) SupportsCatalogPlayableTargets(pool *pgxpool.Pool, userID int) bool {
	return s != nil && pool != nil && s.pool == pool && s.userID == userID
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
