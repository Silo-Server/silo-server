package metadata

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// The collector must count a collection collage row as a reference. A collage
// retired to the collector and then rebuilt under the same key names the same
// objects, and they have to survive.
func TestArtworkReferencesIncludeCollectionCollagesDB(t *testing.T) {
	pool := artworkRevisionGCTestPool(t)
	ctx := context.Background()

	var libraryID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, TRUE) RETURNING id
	`, fmt.Sprintf("collage-refs-%d", time.Now().UnixNano())).Scan(&libraryID); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, libraryID)
	})
	repo := catalog.NewLibraryCollectionRepository(pool)
	collection, err := repo.Create(ctx, catalog.CreateLibraryCollectionInput{
		LibraryID:      libraryID,
		Slug:           fmt.Sprintf("collage-refs-%d", libraryID),
		Title:          "Collage references",
		CollectionType: "manual",
	})
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(context.Background(), collection.ID) })

	const key = "0123456789abcdef"
	path := fmt.Sprintf("collection-images/%s/collage/original.%s.webp", collection.ID, key)
	ref := catalog.CollectionCollageRef{CollectionID: collection.ID, Key: key}
	if err := repo.SaveCollectionCollage(ctx, catalog.CollectionCollage{CollectionCollageRef: ref, Path: path}); err != nil {
		t.Fatalf("save collage: %v", err)
	}
	referenced, err := referencedArtworkPaths(ctx, pool, []string{path})
	if err != nil {
		t.Fatalf("reference check: %v", err)
	}
	if _, ok := referenced[path]; !ok {
		t.Fatal("the collector does not see a stored collage as referenced")
	}
}
