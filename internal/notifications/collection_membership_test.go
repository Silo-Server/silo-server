package notifications

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore/storetest"
)

// TestInterestTrackingStorePreservesCollectionMembership keeps Add to
// collection's membership read working through the production decorator:
// without it, listCollections?contains_item answers 501 on every backend.
func TestInterestTrackingStorePreservesCollectionMembership(t *testing.T) {
	db := openUserDB(t)
	provider := WrapUserStoreProvider(preferenceTransactionTestProvider{store: userdb.NewSQLiteUserStore(db)}, &System{})
	wrapped, err := provider.ForUser(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	storetest.RunManualCollectionsHolding(t, wrapped)
}
