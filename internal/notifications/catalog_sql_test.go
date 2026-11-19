package notifications

import (
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type catalogSQLTestStore struct {
	userstore.UserStore
	postgres bool
}

func (s catalogSQLTestStore) CatalogStateInPostgres() bool { return s.postgres }
