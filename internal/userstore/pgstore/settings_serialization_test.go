package pgstore

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/storetest"
)

func TestPostgresSettingsWriterSerialization(t *testing.T) {
	storetest.RunSettingsWriterSerialization(t, func(t *testing.T) userstore.UserStore {
		pool, userID := newConstraintTestUser(t)
		return newStore(pool, userID)
	})
}
