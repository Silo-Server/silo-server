package notifications

import "github.com/Silo-Server/silo-server/internal/userstore"

func (s *interestTrackingStore) CatalogStateInPostgres() bool {
	return userstore.HasCatalogSQLState(s.UserStore)
}
