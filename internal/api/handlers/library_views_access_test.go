package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/access"
)

// TestLibraryViewsRefuseLibraryOutsideViewerScope: the v1 library section
// handlers share their seams with v2, and every seam refuses a library the
// viewer's allowlist excludes with the same 404 the collection handlers
// answer. The handlers are built on nil repositories, so a skipped check
// would panic rather than pass.
func TestLibraryViewsRefuseLibraryOutsideViewerScope(t *testing.T) {
	sectionsHandler := NewSectionHandler(nil, nil)
	collections := NewLibraryCollectionHandler(nil, nil, nil, 0, nil, nil)
	router := chi.NewRouter()
	router.Get("/library/{id}/layout", sectionsHandler.HandleLibraryLayout)
	router.Get("/library/{id}/sections", sectionsHandler.HandleLibrarySections)
	router.Get("/library/{id}/sections/{sectionId}/items", sectionsHandler.HandleLibrarySectionItems)
	router.Get("/library/{id}/collections/{collection_id}/items", collections.HandleGetLibraryCollectionItems)

	scope := access.Scope{UserID: 1, ProfileID: "p-kid", ProfileVerified: true, AllowedLibraryIDs: []int{2}, LibrariesRestricted: true}
	for _, path := range []string{
		"/library/1/layout",
		"/library/1/sections",
		"/library/1/sections/default-recently-added/items",
		"/library/1/collections/c1/items",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(access.SetScope(context.Background(), scope))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"not_found"`) {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}
