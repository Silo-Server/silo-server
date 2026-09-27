package notifications

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

type catalogSQLTestStore struct {
	userstore.UserStore
	postgres bool
}

func (s catalogSQLTestStore) CatalogStateInPostgres() bool { return s.postgres }

func TestCatalogSQLStateForwarding(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		store := &interestTrackingStore{UserStore: catalogSQLTestStore{postgres: enabled}}
		if got := userstore.HasCatalogSQLState(store); got != enabled {
			t.Fatalf("wrapped state = %v, want %v", got, enabled)
		}
	}
	if userstore.HasCatalogSQLState(&interestTrackingStore{}) {
		t.Fatal("an absent capability must not enable catalog SQL")
	}
}
