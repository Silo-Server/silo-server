package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogsvc "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections"
)

func (f *fakeAdminCollections) ListAdminCollections(context.Context, *int) (handlers.AdminCollectionsList, error) {
	f.listReads++
	return handlers.AdminCollectionsList{Collections: f.listed}, nil
}
func (f *fakeAdminCollections) AdminCollectionSections(_ context.Context, id string) ([]handlers.AdminCollectionSection, error) {
	f.sectionReads++
	if id != "c1" {
		return nil, catalogsvc.ErrLibraryCollectionNotFound
	}
	return f.sections, nil
}
func (f *fakeAdminCollections) AdminCollectionRowCounts(_ context.Context, ids []string) (map[string]handlers.AdminCollectionRowCount, error) {
	f.countReads++
	f.countedIDs = append(f.countedIDs, ids...)
	return f.rowCounts, nil
}

// adminCollectionsWithoutRows hides the row-reference methods, like a service
// that cannot report rows.
type adminCollectionsWithoutRows struct {
	AdminCollectionService
}

const (
	adminCollectionsPath        = "/api/v2/admin/collections"
	adminCollectionSectionsPath = "/api/v2/admin/collections/c1/sections"
)

func fakeRowReferences() *fakeAdminCollections {
	f := newFakeAdminCollections()
	stamp := fixedTime().Format("2006-01-02T15:04:05Z07:00")
	listed := func(id, slug, title, kind string) handlers.AdminCollection {
		empty := json.RawMessage(`{}`)
		return handlers.AdminCollection{ID: id, LibraryID: 1, LibraryIDs: []int{1}, Slug: slug, Title: title, CollectionType: kind, Visibility: "visible", QueryDefinition: empty, SortConfig: empty, SourceConfig: empty, ManagementMode: "manual", CreatedAt: stamp, UpdatedAt: stamp}
	}
	f.listed = []handlers.AdminCollection{
		listed("c1", "on-home", "On Home", "manual"),
		listed("c2", "library-page-only", "Library page only", "smart"),
		listed("c3", "unused", "Unused", "manual"),
	}
	f.rowCounts = map[string]handlers.AdminCollectionRowCount{"c1": {Home: 2, Total: 3}, "c2": {Home: 0, Total: 1}}
	kids := 7
	f.sections = []handlers.AdminCollectionSection{
		{PageSection: sections.PageSection{ID: "s-hero", Scope: "home", Position: 0, SectionType: sections.SectionCollection, Title: "Starter pack hero", Featured: true, Enabled: true}, PageRowCount: 9},
		{PageSection: sections.PageSection{ID: "s-home", Scope: "home", Position: 4, SectionType: sections.SectionCollection, Title: "On Home", Enabled: true}, PageRowCount: 9},
		{PageSection: sections.PageSection{ID: "s-kids", Scope: "library", LibraryID: &kids, Position: 2, SectionType: sections.SectionCollection, Title: "Kids", Enabled: false}, PageRowCount: 3},
	}
	return f
}

type listedRowCounts struct {
	Items []struct {
		ID           string `json:"id"`
		HomeRowCount *int   `json:"home_row_count"`
		RowCount     *int   `json:"row_count"`
	} `json:"items"`
}

// TestAdminCollectionListCarriesRowCounts checks that every listed collection
// carries its Home and total row counts from one grouped count for the whole
// page, with zero for a collection no row shows.
func TestAdminCollectionListCarriesRowCounts(t *testing.T) {
	f := fakeRowReferences()
	rec := do(t, adminCollectionsTestHandler(t, f), http.MethodGet, adminCollectionsPath+"?library_id=1", "", bearer(adminToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var body listedRowCounts
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string][2]int{"c1": {2, 3}, "c2": {0, 1}, "c3": {0, 0}}
	if len(body.Items) != len(want) {
		t.Fatalf("listed %d collections, want %d: %s", len(body.Items), len(want), rec.Body)
	}
	for _, item := range body.Items {
		if item.HomeRowCount == nil || item.RowCount == nil {
			t.Fatalf("%s: counts missing: %s", item.ID, rec.Body)
		}
		if got := [2]int{*item.HomeRowCount, *item.RowCount}; got != want[item.ID] {
			t.Errorf("%s: home_row_count, row_count = %v, want %v", item.ID, got, want[item.ID])
		}
	}
	if f.countReads != 1 || !slices.Equal(f.countedIDs, []string{"c1", "c2", "c3"}) {
		t.Fatalf("counted %d times for %v; want one count for the whole page", f.countReads, f.countedIDs)
	}
}

// TestAdminCollectionRowCountsStayOnTheList checks that the canonical editor
// read, whose ETag covers only the collection's own revision, never carries
// row counts, and that a service that cannot count rows leaves them out of the
// list instead of reporting zero.
func TestAdminCollectionRowCountsStayOnTheList(t *testing.T) {
	f := fakeRowReferences()
	rec := do(t, adminCollectionsTestHandler(t, f), http.MethodGet, "/api/v2/admin/collections/c1", "", bearer(adminToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var one map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &one); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"home_row_count", "row_count"} {
		if _, ok := one[key]; ok {
			t.Errorf("getAdminCollection carries %s: %s", key, rec.Body)
		}
	}
	if f.countReads != 0 {
		t.Fatal("getAdminCollection counted rows")
	}

	deps, _ := libraryDeps(t)
	deps.AdminCollections = adminCollectionsWithoutRows{f}
	rec = do(t, newTestHandler(t, deps), http.MethodGet, adminCollectionsPath, "", bearer(adminToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var body listedRowCounts
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, item := range body.Items {
		if item.HomeRowCount != nil || item.RowCount != nil {
			t.Fatalf("%s: counts reported without a row service: %s", item.ID, rec.Body)
		}
	}
}

// TestAdminCollectionSectionsListTheRows checks the rows that show a
// collection: Home rows with a null library, library page rows with the
// library's string ID, the hero flag, turned-off rows and each page's row
// count, in the order the service returns them.
func TestAdminCollectionSectionsListTheRows(t *testing.T) {
	f := fakeRowReferences()
	rec := do(t, adminCollectionsTestHandler(t, f), http.MethodGet, adminCollectionSectionsPath, "", bearer(adminToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"id": "s-hero", "scope": "home", "library_id": nil, "section_type": "collection", "title": "Starter pack hero", "featured": true, "enabled": true, "position": 0.0, "page_row_count": 9.0},
		{"id": "s-home", "scope": "home", "library_id": nil, "section_type": "collection", "title": "On Home", "featured": false, "enabled": true, "position": 4.0, "page_row_count": 9.0},
		{"id": "s-kids", "scope": "library", "library_id": "7", "section_type": "collection", "title": "Kids", "featured": false, "enabled": false, "position": 2.0, "page_row_count": 3.0},
	}
	if len(body.Items) != len(want) {
		t.Fatalf("got %d rows, want %d: %s", len(body.Items), len(want), rec.Body)
	}
	for i, w := range want {
		got := body.Items[i]
		if len(got) != len(w) {
			t.Errorf("row %d members = %v, want %v", i, got, w)
		}
		for k, v := range w {
			if got[k] != v {
				t.Errorf("row %d %s = %v, want %v", i, k, got[k], v)
			}
		}
	}

	f.sections = nil
	rec = do(t, adminCollectionsTestHandler(t, f), http.MethodGet, adminCollectionSectionsPath, "", bearer(adminToken))
	var empty struct {
		Items []json.RawMessage `json:"items"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &empty) != nil || empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("unused collection: %d %s, want an empty items list", rec.Code, rec.Body)
	}
}

func TestAdminCollectionSectionsUnknownCollectionIsNotFound(t *testing.T) {
	f := fakeRowReferences()
	rec := do(t, adminCollectionsTestHandler(t, f), http.MethodGet, "/api/v2/admin/collections/missing/sections", "", bearer(adminToken))
	requireProblem(t, rec, TypeNotFound)
	if f.sectionReads != 1 {
		t.Fatalf("the service was asked %d times, want once", f.sectionReads)
	}
}

// TestAdminCollectionRowsRequireActingAdmin restates #193 S5 for the rows
// list and the counted collection list: a regular account and a non-primary
// profile on an admin account are refused before anything is read.
func TestAdminCollectionRowsRequireActingAdmin(t *testing.T) {
	f := fakeRowReferences()
	h := adminCollectionsTestHandler(t, f)
	for _, path := range []string{adminCollectionSectionsPath, adminCollectionsPath} {
		for _, headers := range []map[string]string{bearer(memberToken), with(bearer(adminToken), "X-Profile-Id", "p-owner")} {
			requireProblem(t, do(t, h, http.MethodGet, path, "", headers), TypePermissionDenied)
		}
	}
	if f.sectionReads+f.countReads+f.listReads+f.reads != 0 {
		t.Fatalf("refused requests read rows %d, counts %d, lists %d, collections %d", f.sectionReads, f.countReads, f.listReads, f.reads)
	}
}

// TestAdminCollectionCapabilityReportsSectionReferences checks that the
// capability follows whether the service can report rows.
func TestAdminCollectionCapabilityReportsSectionReferences(t *testing.T) {
	read := func(svc AdminCollectionService) bool {
		t.Helper()
		deps, _ := libraryDeps(t)
		deps.AdminCollections = svc
		rec := do(t, newTestHandler(t, deps), http.MethodGet, adminCollectionCapabilitiesPath, "", bearer(adminToken))
		if rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		var body struct {
			SectionReferences *bool `json:"section_references"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.SectionReferences == nil {
			t.Fatalf("section_references missing: %s", rec.Body)
		}
		return *body.SectionReferences
	}
	if !read(fakeRowReferences()) {
		t.Error("section_references = false with a row service")
	}
	if read(adminCollectionsWithoutRows{fakeRowReferences()}) {
		t.Error("section_references = true without a row service")
	}
}
