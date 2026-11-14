package notifications

import (
	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func (s *interestTrackingStore) CatalogStateInPostgres() bool {
	return userstore.HasCatalogSQLState(s.UserStore)
}

// QueueItemViewerState forwards the backing store's answer. The decorator
// hooks mutations only, so these reads pass through unchanged.
func (s *interestTrackingStore) QueueItemViewerState(b *pgx.Batch, q userstore.ItemViewerStateQuery, state *userstore.ItemViewerState) bool {
	return userstore.QueueItemViewerState(s.UserStore, b, q, state)
}
