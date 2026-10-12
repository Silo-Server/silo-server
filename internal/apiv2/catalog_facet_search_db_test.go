package apiv2

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

// allowlistCatalogAccess resolves every viewer to one library allowlist;
// nil means unrestricted.
type allowlistCatalogAccess struct{ allowed []int }

func (a *allowlistCatalogAccess) ContextAccessFilter(ctx context.Context, _ handlers.AccessFilterOptions) (catalogpkg.AccessFilter, error) {
	return catalogpkg.AccessFilter{UserID: claimsFrom(ctx).UserID, ProfileID: "p-owner", AllowedLibraryIDs: a.allowed}, nil
}

// TestSearchCatalogFacetLibraryIDsDB: library_ids narrows the facet scope to
// the named libraries the viewer may see and never widens it, and values
// carry each value's title count.
func TestSearchCatalogFacetLibraryIDsDB(t *testing.T) {
	pool := viewerAccessTestPool(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	libA := seedLibrary(t, pool, "facet-a-"+sfx)
	libB := seedLibrary(t, pool, "facet-b-"+sfx)
	ids := []string{"facet-a1-" + sfx, "facet-a2-" + sfx, "facet-b1-" + sfx}
	t.Cleanup(func() { mustExec(t, pool, `DELETE FROM media_items WHERE content_id = ANY($1)`, ids) })
	studioA, studioB := "Facet Studio A "+sfx, "Facet Studio B "+sfx
	mustExec(t, pool, `INSERT INTO media_items (content_id, type, title, status, studios) VALUES ($1, 'movie', 'A1', 'matched', $4), ($2, 'movie', 'A2', 'matched', $4), ($3, 'movie', 'B1', 'matched', $5)`,
		ids[0], ids[1], ids[2], []string{studioA}, []string{studioB})
	mustExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $3), ($2, $3)`, ids[0], ids[1], libA)
	mustExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, ids[2], libB)

	browseRepo := catalogpkg.NewBrowseRepository(pool)
	itemRepo := catalogpkg.NewItemRepository(pool)
	itemsH := handlers.NewItemsHandler(browseRepo, itemRepo, nil, nil, nil, nil, nil, nil, nil)
	access := &allowlistCatalogAccess{allowed: []int{libA}}
	deps, _ := catalogDeps(t)
	deps.CatalogAccess = access
	deps.CatalogBrowse = handlers.NewCatalogHandler(catalogpkg.NewCatalogResolver(browseRepo, itemRepo), itemsH)
	h := newTestHandler(t, deps)

	type answer struct {
		Matches []string            `json:"matches"`
		Values  []CatalogFacetValue `json:"values"`
		HasMore bool                `json:"has_more"`
	}
	search := func(query string) answer {
		t.Helper()
		rec := do(t, h, http.MethodGet, "/api/v2/catalog/filters/search?facet=studio&q=facet+studio&"+query, "", viewerHeaders())
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body.String())
		}
		var got answer
		decodeJSON(t, rec.Body, &got)
		return got
	}
	onlyA := answer{Matches: []string{studioA}, Values: []CatalogFacetValue{{Value: studioA, Count: 2}}}
	none := answer{Matches: []string{}, Values: []CatalogFacetValue{}}
	both := answer{Matches: []string{studioA, studioB}, Values: []CatalogFacetValue{{Value: studioA, Count: 2}, {Value: studioB, Count: 1}}}

	for _, tc := range []struct {
		name  string
		query string
		want  answer
	}{
		{"both requested", fmt.Sprintf("library_ids=%d&library_ids=%d", libA, libB), onlyA},
		{"only the hidden library", fmt.Sprintf("library_ids=%d", libB), none},
		{"hidden library through library_id", fmt.Sprintf("library_id=%d&library_ids=%d", libB, libB), none},
		{"no library_ids", "", onlyA},
	} {
		if got := search(tc.query); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("restricted viewer, %s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}

	// The same scope parameters from an unrestricted viewer reach both
	// libraries: the cache keys on the access predicates, not the request.
	access.allowed = nil
	if got := search(fmt.Sprintf("library_ids=%d&library_ids=%d", libA, libB)); !reflect.DeepEqual(got, both) {
		t.Errorf("unrestricted viewer: got %+v, want %+v", got, both)
	}
	if got := search(fmt.Sprintf("library_ids=%d", libB)); !reflect.DeepEqual(got, answer{Matches: []string{studioB}, Values: []CatalogFacetValue{{Value: studioB, Count: 1}}}) {
		t.Errorf("unrestricted viewer, library B: got %+v", got)
	}
}
