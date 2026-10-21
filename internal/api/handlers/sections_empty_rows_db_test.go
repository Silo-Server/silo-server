package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// A row whose titles all sit in a library the viewer cannot open has nothing
// to show, so the v2 aggregate Home and library reads leave it out rather than
// answering it with no items. Asked for by ID, the row still answers empty,
// and the frozen v1 aggregate keeps answering it.
func TestAggregateSectionsOmitRowsWithNoAccessibleItemsDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	ctx := t.Context()
	insertRow := func(scope string, library *int, title, contentID string) string {
		t.Helper()
		id := fmt.Sprintf("empty-rows-%s-%s", f.ids[0], title)
		config := fmt.Sprintf(`{"item_ids":["%s"]}`, contentID)
		f.exec(t, `INSERT INTO page_sections (id, scope, library_id, position, section_type, title, item_limit, config, enabled)
			VALUES ($1, $2, $3, 0, 'admin_curated_list', $4, 20, $5::jsonb, true)`, id, scope, library, title, config)
		t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM page_sections WHERE id = $1`, id) })
		return id
	}
	hiddenOnly := f.ids[0] // the fixture puts its first title in the hidden library
	visible := f.ids[1]

	homeVisible := insertRow("home", nil, "home-visible", visible)
	homeEmpty := insertRow("home", nil, "home-empty", hiddenOnly)
	libraryVisible := insertRow("library", &f.library, "library-visible", visible)
	libraryEmpty := insertRow("library", &f.library, "library-empty", hiddenOnly)

	h := NewSectionHandler(sections.NewRepository(f.pool), sections.NewFetcher(f.pool))
	h.FolderRepo = catalog.NewFolderRepository(f.pool)
	viewerCtx := apimw.SetClaims(ctx, &auth.Claims{UserID: f.account})
	viewerCtx = apimw.SetProfileID(viewerCtx, "")
	viewerCtx = access.SetScope(viewerCtx, access.Scope{UserID: f.account, AllowedLibraryIDs: []int{f.library}, LibrariesRestricted: true})
	viewer := SectionViewer{Access: catalog.AccessFilter{UserID: f.account, AllowedLibraryIDs: []int{f.library}}}

	rowIDs := func(view SectionsView) []string {
		ids := make([]string, 0, len(view.Sections))
		for _, s := range view.Sections {
			ids = append(ids, s.ID)
		}
		return ids
	}

	home, err := h.HomeSections(viewerCtx, viewer)
	if err != nil {
		t.Fatal(err)
	}
	if got := rowIDs(home); !slices.Contains(got, homeVisible) || slices.Contains(got, homeEmpty) {
		t.Fatalf("home rows = %v, want %s and not %s", got, homeVisible, homeEmpty)
	}

	library, err := h.LibrarySections(viewerCtx, f.library, viewer)
	if err != nil {
		t.Fatal(err)
	}
	if got := rowIDs(library); !slices.Contains(got, libraryVisible) || slices.Contains(got, libraryEmpty) {
		t.Fatalf("library rows = %v, want %s and not %s", got, libraryVisible, libraryEmpty)
	}

	v1 := httptest.NewRecorder()
	h.HandleHomeSections(v1, httptest.NewRequest(http.MethodGet, "/home/sections", nil).WithContext(viewerCtx))
	var v1Body homeSectionsResponse
	if err := json.Unmarshal(v1.Body.Bytes(), &v1Body); err != nil {
		t.Fatalf("v1 home: %d %s: %v", v1.Code, v1.Body.String(), err)
	}
	if got := rowIDs(v1Body); !slices.Contains(got, homeEmpty) {
		t.Fatalf("v1 home rows = %v, want the empty row %s kept", got, homeEmpty)
	}

	direct, err := h.HomeSectionItems(viewerCtx, homeEmpty, viewer)
	if err != nil {
		t.Fatal(err)
	}
	if direct.ID != homeEmpty || len(direct.Items) != 0 {
		t.Fatalf("direct read = %+v, want the row with no items", direct)
	}
}
