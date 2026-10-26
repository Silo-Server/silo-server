package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
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
