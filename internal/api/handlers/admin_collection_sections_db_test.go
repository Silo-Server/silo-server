package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// TestAdminCollectionRowsDB checks the rows that show a server collection
// through the handler: the rows list and the grouped counts agree, an unknown
// collection is not found, and the frozen /api/v1 admin list stays
// byte-identical when rows start to show the collection.
func TestAdminCollectionRowsDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	sectionRepo := sections.NewRepository(f.pool)
	h := NewLibraryCollectionHandler(catalog.NewLibraryCollectionRepository(f.pool), nil, catalog.NewItemRepository(f.pool), nil)
	h.SectionRepo = sectionRepo
	shown, err := h.CreateAdminCollection(t.Context(), AdminCollectionCreate{LibraryID: f.library, Title: "Shown", CollectionType: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	unused, err := h.CreateAdminCollection(t.Context(), AdminCollectionCreate{LibraryID: f.library, Title: "Unused", CollectionType: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, ids := context.Background(), []string{shown.ID, unused.ID}
		_, _ = f.pool.Exec(ctx, `DELETE FROM library_collections WHERE id = ANY($1)`, ids)
		_, _ = f.pool.Exec(ctx, `DELETE FROM library_collection_revisions WHERE collection_id = ANY($1)`, ids)
	})

	v1List := func() []byte {
		t.Helper()
		rec := httptest.NewRecorder()
		h.HandleListAdminCollections(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/admin/collections?library_id=%d", f.library), nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("v1 list: %d %s", rec.Code, rec.Body)
		}
		return rec.Body.Bytes()
	}
	before := v1List()

	config := json.RawMessage(fmt.Sprintf(`{"library_collection_id":%q}`, shown.ID))
	var rowIDs []string
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM page_sections WHERE id = ANY($1)`, rowIDs)
	})
	for _, row := range []*sections.PageSection{
		{Scope: "home", Position: 900, SectionType: sections.SectionCollection, Title: "On Home", Config: config, Enabled: true},
		{Scope: "library", LibraryID: &f.library, SectionType: sections.SectionCollection, Title: "On the library page", Config: config, Enabled: false},
	} {
		created, err := sectionRepo.Create(t.Context(), row)
		if err != nil {
			t.Fatal(err)
		}
		rowIDs = append(rowIDs, created.ID)
	}

	after := v1List()
	if string(after) != string(before) {
		t.Fatalf("v1 admin list changed when rows started to show a collection:\nbefore %s\nafter  %s", before, after)
	}
	var v1 struct {
		Collections []map[string]json.RawMessage `json:"collections"`
	}
	if err := json.Unmarshal(after, &v1); err != nil {
		t.Fatal(err)
	}
	if len(v1.Collections) != 2 {
		t.Fatalf("v1 list has %d collections, want 2: %s", len(v1.Collections), after)
	}
	for _, c := range v1.Collections {
		for _, key := range []string{"home_row_count", "row_count"} {
			if _, ok := c[key]; ok {
				t.Fatalf("v1 admin list carries %s: %s", key, after)
			}
		}
	}

	refs, err := h.AdminCollectionSections(t.Context(), shown.ID)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(refs))
	for _, r := range refs {
		ids = append(ids, r.ID)
	}
	if !slices.Equal(ids, rowIDs) || refs[1].PageRowCount != 1 || refs[1].Enabled {
		t.Fatalf("rows = %+v, want the Home row then the turned-off library row", refs)
	}
	counts, err := h.AdminCollectionRowCounts(t.Context(), []string{shown.ID, unused.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := counts[shown.ID]; got != (AdminCollectionRowCount{Home: 1, Total: 2}) {
		t.Fatalf("counts for the shown collection = %+v, want 1 on Home of 2", got)
	}
	if _, ok := counts[unused.ID]; ok {
		t.Fatalf("an unused collection has counts: %+v", counts)
	}
	if empty, err := h.AdminCollectionSections(t.Context(), unused.ID); err != nil || len(empty) != 0 {
		t.Fatalf("unused collection rows = %+v, %v", empty, err)
	}

	if _, err := h.AdminCollectionSections(t.Context(), "missing-collection"); !errors.Is(err, catalog.ErrLibraryCollectionNotFound) {
		t.Fatalf("unknown collection: %v, want not found", err)
	}
	h.SectionRepo = nil
	if counts, err := h.AdminCollectionRowCounts(t.Context(), []string{shown.ID}); err != nil || counts != nil {
		t.Fatalf("counts without sections = %v, %v; want none", counts, err)
	}
}
