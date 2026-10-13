package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/migrations"
)

func newCollectionMembersTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := database.RunMigrations(t.Context(), pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return pool
}

// A server collection scoped to a library the viewer cannot reach is as
// missing to CollectionMembers as it is to the catalog's collection route.
func TestCollectionMembersHidesLibraryCollectionsOutsideTheViewersLibraries(t *testing.T) {
	pool := newCollectionMembersTestPool(t)
	ctx := t.Context()
	prefix := fmt.Sprintf("collection-members-%d-", time.Now().UnixNano())

	var allowed, other int
	for name, id := range map[string]*int{"allowed": &allowed, "other": &other} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, prefix+name).Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = ANY($1)`, []int{allowed, other})
	})
	repo := NewLibraryCollectionRepository(pool)
	collection, err := repo.Create(ctx, CreateLibraryCollectionInput{
		LibraryID: other, LibraryIDs: []int{other}, Slug: prefix + "picks", Title: "Staff Picks",
		CollectionType: "manual", Visibility: LibraryCollectionVisibilityVisible,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM library_collections WHERE id = $1`, collection.ID)
	})
	resolver := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool))

	if _, _, err := resolver.CollectionMembers(ctx, CatalogSourceLibraryCollection, collection.ID, AccessFilter{AllowedLibraryIDs: []int{allowed}}); !errors.Is(err, ErrCatalogSourceNotFound) {
		t.Fatalf("collection outside the viewer's libraries: err = %v", err)
	}
	title, _, err := resolver.CollectionMembers(ctx, CatalogSourceLibraryCollection, collection.ID, AccessFilter{AllowedLibraryIDs: []int{other}})
	if err != nil || title != "Staff Picks" {
		t.Fatalf("collection in the viewer's libraries: title %q, err %v", title, err)
	}
}
