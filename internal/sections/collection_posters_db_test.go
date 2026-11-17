package sections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// A row of collections references every server collection it lists: each
// one is counted and listed as shown by the row, can't be deleted while the
// row lists it, and must exist when a write adds it. A collection the row no
// longer lists can be deleted.
func TestRowOfCollectionsReferencesItsCollectionsPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	name := "collection-row-" + uuid.NewString()[:8]
	var library int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, name).Scan(&library); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM page_sections WHERE library_id = $1`, library)
		_, _ = pool.Exec(context.Background(), `DELETE FROM library_collections WHERE slug LIKE $1`, name+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, library)
	})
	collections := catalog.NewLibraryCollectionRepository(pool)
	create := func(slug string) string {
		t.Helper()
		c, err := collections.Create(ctx, catalog.CreateLibraryCollectionInput{LibraryID: library, LibraryIDs: []int{library}, Slug: name + "-" + slug, Title: slug, CollectionType: "manual", Visibility: "visible"})
		if err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	marvel, pixar, ghibli := create("marvel"), create("pixar"), create("ghibli")
	config := func(ids ...string) json.RawMessage {
		entries := make([]map[string]string, 0, len(ids))
		for _, id := range ids {
			entries = append(entries, map[string]string{"library_collection_id": id})
		}
		raw, _ := json.Marshal(map[string]any{"collections": entries})
		return raw
	}

	repo := NewRepository(pool)
	if _, err := repo.Create(ctx, &PageSection{Scope: "library", LibraryID: &library, SectionType: SectionCollectionPosters, Title: "Franchises", ItemLimit: 20,
		Config: config(marvel, "no-such-collection"), Enabled: true}); !errors.Is(err, catalog.ErrLibraryCollectionNotFound) {
		t.Fatalf("a row listing a missing collection saved: %v", err)
	}
	row, err := repo.Create(ctx, &PageSection{Scope: "library", LibraryID: &library, SectionType: SectionCollectionPosters, Title: "Franchises", ItemLimit: 20,
		Config: config(marvel, pixar), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	counts, err := repo.CountLibraryCollectionReferencesByID(ctx, []string{marvel, pixar, ghibli})
	if err != nil {
		t.Fatal(err)
	}
	if counts[marvel].Total != 1 || counts[pixar].Total != 1 || counts[ghibli].Total != 0 {
		t.Errorf("row counts = %+v", counts)
	}
	if n, err := repo.CountLibraryCollectionReferences(ctx, pixar, ""); err != nil || n != 1 {
		t.Errorf("pixar references = %d (%v), want 1", n, err)
	}
	refs, err := repo.ListLibraryCollectionReferences(ctx, pixar)
	if err != nil || len(refs) != 1 || refs[0].ID != row.ID {
		t.Fatalf("rows showing pixar = %+v (%v), want the row of collections", refs, err)
	}
	for _, id := range []string{marvel, pixar} {
		revision, err := collections.CollectionRevision(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := collections.DeleteIfRevision(ctx, id, revision); !errors.Is(err, catalog.ErrLibraryCollectionInUse) {
			t.Errorf("deleting %s, which the row lists = %v, want in use", id, err)
		}
	}

	row.Config = config(marvel, ghibli)
	if err := repo.Update(ctx, row); err != nil {
		t.Fatal(err)
	}
	revision, err := collections.CollectionRevision(ctx, pixar)
	if err != nil {
		t.Fatal(err)
	}
	if err := collections.DeleteIfRevision(ctx, pixar, revision); err != nil {
		t.Errorf("deleting a collection the row no longer lists: %v", err)
	}
	if got := fmt.Sprint(LibraryCollectionIDsOf(row.Config)); got != fmt.Sprint([]string{marvel, ghibli}) {
		t.Errorf("references = %s", got)
	}
}
