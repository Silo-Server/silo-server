package userdb

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore/storetest"
)

func TestSQLiteAtomicJellycompatFavoriteRollback(t *testing.T) {
	store, ok := newConformanceStore(t).(*SQLiteUserStore)
	if !ok {
		t.Fatal("SQLite fixture unavailable")
	}
	storetest.RunAtomicJellycompatFavorite(t, store, func(reject bool) {
		t.Helper()
		for _, operation := range []string{"INSERT", "DELETE"} {
			statement := "DROP TRIGGER fail_favorite_" + operation
			if reject {
				statement = "CREATE TRIGGER fail_favorite_" + operation + " BEFORE " + operation + " ON favorites BEGIN SELECT RAISE(ABORT, 'forced favorite failure'); END"
			}
			if _, err := store.db.ExecContext(t.Context(), statement); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestSQLiteAtomicJellycompatParentFavoriteRollback(t *testing.T) {
	store, ok := newConformanceStore(t).(*SQLiteUserStore)
	if !ok {
		t.Fatal("SQLite fixture unavailable")
	}
	storetest.RunAtomicJellycompatParentFavorite(t, store, func(reject bool) {
		t.Helper()
		for _, operation := range []string{"INSERT", "DELETE"} {
			statement := "DROP TRIGGER fail_favorite_" + operation
			if reject {
				statement = "CREATE TRIGGER fail_favorite_" + operation + " BEFORE " + operation + " ON favorites BEGIN SELECT RAISE(ABORT, 'forced favorite failure'); END"
			}
			if _, err := store.db.ExecContext(t.Context(), statement); err != nil {
				t.Fatal(err)
			}
		}
	})
}
