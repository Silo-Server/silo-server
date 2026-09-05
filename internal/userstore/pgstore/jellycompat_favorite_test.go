package pgstore

import (
	"context"
	"fmt"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore/storetest"
)

func TestAtomicJellycompatFavoriteRollback(t *testing.T) {
	pool, userID := newConstraintTestUser(t)
	name := fmt.Sprintf("test_favorite_failure_%d", userID)
	if _, err := pool.Exec(t.Context(), fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced favorite failure'; END $$`, name)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP FUNCTION "+name+"() CASCADE")
	})
	storetest.RunAtomicJellycompatFavorite(t, newStore(pool, userID), func(reject bool) {
		t.Helper()
		for _, operation := range []string{"INSERT", "DELETE"} {
			trigger := name + "_" + operation
			statement := "DROP TRIGGER " + trigger + " ON user_favorites"
			if reject {
				row := "NEW"
				if operation == "DELETE" {
					row = "OLD"
				}
				statement = fmt.Sprintf("CREATE TRIGGER %s BEFORE %s ON user_favorites FOR EACH ROW WHEN (%s.user_id = %d) EXECUTE FUNCTION %s()", trigger, operation, row, userID, name)
			}
			if _, err := pool.Exec(t.Context(), statement); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestAtomicJellycompatParentFavoriteRollback(t *testing.T) {
	pool, userID := newConstraintTestUser(t)
	name := fmt.Sprintf("test_favorite_failure_%d", userID)
	if _, err := pool.Exec(t.Context(), fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced favorite failure'; END $$`, name)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP FUNCTION "+name+"() CASCADE")
	})
	storetest.RunAtomicJellycompatParentFavorite(t, newStore(pool, userID), func(reject bool) {
		t.Helper()
		for _, operation := range []string{"INSERT", "DELETE"} {
			trigger := name + "_" + operation
			statement := "DROP TRIGGER " + trigger + " ON user_favorites"
			if reject {
				row := "NEW"
				if operation == "DELETE" {
					row = "OLD"
				}
				statement = fmt.Sprintf("CREATE TRIGGER %s BEFORE %s ON user_favorites FOR EACH ROW WHEN (%s.user_id = %d) EXECUTE FUNCTION %s()", trigger, operation, row, userID, name)
			}
			if _, err := pool.Exec(t.Context(), statement); err != nil {
				t.Fatal(err)
			}
		}
	})
}
