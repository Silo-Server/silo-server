package apiv2

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

// TestLibraryCollectionItemsHonorViewerAccessDB: a manual collection's stored
// items are answered through the access-filtered lookup, so an item above the
// profile's rating ceiling or in a library the profile cannot see is dropped
// while the curated order of the rest survives.
func TestLibraryCollectionItemsHonorViewerAccessDB(t *testing.T) {
	pool := viewerAccessTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	libraryID := seedLibrary(t, pool, fmt.Sprintf("scope-coll-a-%d", suffix))
	otherLibraryID := seedLibrary(t, pool, fmt.Sprintf("scope-coll-b-%d", suffix))

	pg, r, other, pg2 := fmt.Sprintf("coll-pg-%d", suffix), fmt.Sprintf("coll-r-%d", suffix), fmt.Sprintf("coll-other-%d", suffix), fmt.Sprintf("coll-pg2-%d", suffix)
	for _, row := range []struct{ id, rating string }{{pg, "PG"}, {r, "R"}, {other, "PG"}, {pg2, "PG"}} {
		mustExec(t, pool, `INSERT INTO media_items (content_id, type, title, content_rating) VALUES ($1, 'movie', $1, $2)`, row.id, row.rating)
	}
	t.Cleanup(func() {
		mustExec(t, pool, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{pg, r, other, pg2})
	})
	mustExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $4), ($2, $4), ($3, $5), ($6, $4)`, pg, r, other, libraryID, otherLibraryID, pg2)

	repo := catalogpkg.NewLibraryCollectionRepository(pool)
	collection, err := repo.Create(ctx, catalogpkg.CreateLibraryCollectionInput{LibraryID: libraryID, Slug: fmt.Sprintf("scope-%d", suffix), Title: "Scoped", CollectionType: "manual"})
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, collection.ID) })
	items := []catalogpkg.LibraryCollectionItemInput{{MediaItemID: pg2, Position: 0}, {MediaItemID: r, Position: 1}, {MediaItemID: other, Position: 2}, {MediaItemID: pg, Position: 3}}
	if err := repo.ReplaceItems(ctx, collection.ID, items); err != nil {
		t.Fatalf("replace items: %v", err)
	}

	policy := &access.Scope{AllowedLibraryIDs: []int{libraryID}, LibrariesRestricted: true, MaxContentRating: "PG-13"}
	svc := handlers.NewLibraryCollectionHandler(repo, nil, catalogpkg.NewItemRepository(pool), 0, nil, nil)
	h := newTestHandler(t, scopedViewerDeps(t, policy, &fakeLibraryViews{}, svc))
	path := fmt.Sprintf("/api/v2/library/%d/collections/%s/items", libraryID, collection.ID)

	ids := func(t *testing.T) []string {
		t.Helper()
		rec := do(t, h, http.MethodGet, path, "", viewerHeaders())
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Items []struct {
				ContentID string `json:"content_id"`
			} `json:"items"`
		}
		decodeJSON(t, rec.Body, &body)
		out := make([]string, 0, len(body.Items))
		for _, item := range body.Items {
			out = append(out, item.ContentID)
		}
		return out
	}

	if got := ids(t); fmt.Sprint(got) != fmt.Sprint([]string{pg2, pg}) {
		t.Fatalf("restricted viewer got %v, want %v", got, []string{pg2, pg})
	}
	policy.MaxContentRating = ""
	policy.AllowedLibraryIDs = nil
	policy.LibrariesRestricted = false
	if got := ids(t); fmt.Sprint(got) != fmt.Sprint([]string{pg2, r, other, pg}) {
		t.Fatalf("unrestricted viewer got %v, want stored order", got)
	}
}
