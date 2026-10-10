package notifications

import (
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

func (s *interestTrackingStore) ProgressSnapshotDatabase() (*pgxpool.Pool, int) {
	if source, ok := s.UserStore.(userstore.ProgressSnapshotSource); ok {
		return source.ProgressSnapshotDatabase()
	}
	return nil, 0
}

// CatalogProgressRelation forwards the backing store's answer. Every wrapper
// variant embeds interestTrackingStore, so without this the PostgreSQL
// play-target path would never see the store behind the wrapper.
func (s *interestTrackingStore) CatalogProgressRelation(pool *pgxpool.Pool, userID int, profileID string, firstArg int) (string, []any, bool) {
	if store, ok := s.UserStore.(userstore.CatalogProgressRelationStore); ok {
		return store.CatalogProgressRelation(pool, userID, profileID, firstArg)
	}
	return "", nil, false
}
