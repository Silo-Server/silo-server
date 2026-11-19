package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
)

type fakeAdminSections struct {
	view                                                                  handlers.AdminSection
	bulkLibraryCount                                                      int
	revision, scopeRevision                                               int64
	caps                                                                  handlers.AdminSectionCapabilitiesView
	reads, updates, deletes, creates, reorders, restores, bulks, previews int
	mismatch, readMismatch, deleted                                       bool
	preview                                                               []*models.MediaItem
	previewPosters                                                        map[string]string
	previewRequest                                                        handlers.AdminSectionPreviewRequest
	recipeChecks                                                          []bool
	lastWebOnly                                                           *bool
}

func newFakeAdminSections() *fakeAdminSections {
	stamp := fixedTime().Format("2006-01-02T15:04:05Z07:00")
	return &fakeAdminSections{revision: 1, scopeRevision: 1, caps: handlers.AdminSectionCapabilitiesView{ResetProfiles: true, Preview: true}, view: handlers.AdminSection{ID: "s1", Scope: scopeHome, Position: 0, SectionType: "recently_added", Title: "Original", ItemLimit: 20, Config: json.RawMessage(`{}`), Enabled: false, CreatedAt: stamp, UpdatedAt: stamp}}
}
func (f *fakeAdminSections) ListAdminSections(context.Context, string, *int) ([]handlers.AdminSection, error) {
	if f.deleted {
		return []handlers.AdminSection{}, nil
	}
	return []handlers.AdminSection{f.view}, nil
}
func (f *fakeAdminSections) GetAdminSection(_ context.Context, id string) (handlers.AdminSection, int64, error) {
	f.reads++
	if f.readMismatch {
		return handlers.AdminSection{}, 0, sections.ErrSectionRevisionMismatch
	}
	if f.deleted || id != f.view.ID {
		return handlers.AdminSection{}, 0, sections.ErrSectionNotFound
	}
	return f.view, f.revision, nil
}
func (f *fakeAdminSections) AdminSectionOrder(_ context.Context, scope string, library *int) (handlers.AdminSectionOrderView, error) {
	ids := []string{}
	if !f.deleted {
		ids = append(ids, f.view.ID)
	}
	return handlers.AdminSectionOrderView{Scope: scope, LibraryID: library, OrderedIDs: ids, Revision: f.scopeRevision}, nil
}
func (f *fakeAdminSections) CreateAdminSection(_ context.Context, in handlers.AdminSectionCreate) (handlers.AdminSection, error) {
	f.creates++
	f.recipeChecks = append(f.recipeChecks, in.ValidateRecipe)
	f.view.Scope = in.Scope
	f.view.LibraryID = in.LibraryID
	f.view.Title = in.Title
	f.view.Config = in.Config
	f.view.Featured = in.Featured
	f.view.WebOnly = in.WebOnly
	return f.view, nil
}
func (f *fakeAdminSections) UpdateAdminSection(_ context.Context, _ string, in handlers.AdminSectionUpdate) (handlers.AdminSection, error) {
	f.updates++
	f.recipeChecks = append(f.recipeChecks, in.ValidateRecipe)
	f.revision++
	f.scopeRevision++
	if f.mismatch {
		f.mismatch = false
		return handlers.AdminSection{}, sections.ErrSectionRevisionMismatch
	}
	if in.Title != "" {
		f.view.Title = in.Title
	}
	if in.Enabled != nil {
		f.view.Enabled = *in.Enabled
	}
	if in.Featured != nil {
		f.view.Featured = *in.Featured
	}
	if in.Config != nil {
		f.view.Config = in.Config
	}
	f.lastWebOnly = in.WebOnly
	if in.WebOnly != nil {
		f.view.WebOnly = *in.WebOnly
	}
	return f.view, nil
}
func (f *fakeAdminSections) DeleteAdminSection(context.Context, string) error {
	f.deletes++
	f.revision++
	f.scopeRevision++
	if f.mismatch {
		f.mismatch = false
		return sections.ErrSectionRevisionMismatch
	}
	f.deleted = true
	return nil
}
func (f *fakeAdminSections) ReorderAdminSections(context.Context, string, *int, []string) error {
	f.reorders++
	f.scopeRevision++
	if f.mismatch {
		f.mismatch = false
		return sections.ErrSectionRevisionMismatch
	}
	return nil
}
func (f *fakeAdminSections) RestoreAdminSections(context.Context, handlers.AdminSectionRestore) ([]handlers.AdminSection, error) {
	f.restores++
	f.scopeRevision++
	if f.mismatch {
		f.mismatch = false
		return nil, sections.ErrSectionRevisionMismatch
	}
	return []handlers.AdminSection{f.view}, nil
}
func (f *fakeAdminSections) BulkCreateAdminSections(_ context.Context, req handlers.AdminSectionBulkCreate) (handlers.AdminSectionBulkResult, error) {
	f.bulks++
	f.bulkLibraryCount = len(req.LibraryIDs)
	return handlers.AdminSectionBulkResult{Created: 1}, nil
}
func (f *fakeAdminSections) PreviewAdminSection(_ context.Context, req handlers.AdminSectionPreviewRequest) (handlers.AdminSectionPreviewResult, error) {
	f.previews++
	f.previewRequest = req
	// The section fetcher reads a non-nil empty library_ids as "scoped to zero
	// libraries" and matches nothing; nil means unscoped.
	if req.LibraryIDs != nil && len(req.LibraryIDs) == 0 {
		return handlers.AdminSectionPreviewResult{}, nil
	}
	return handlers.AdminSectionPreviewResult{Items: f.preview, TotalCount: len(f.preview), PosterURLs: f.previewPosters}, nil
}
func (f *fakeAdminSections) AdminSectionCapabilities(context.Context) handlers.AdminSectionCapabilitiesView {
	return f.caps
}
func adminSectionsTestHandler(t *testing.T, f *fakeAdminSections) http.Handler {
	t.Helper()
	deps, _ := libraryDeps(t)
	deps.AdminSections = f
	return newTestHandler(t, deps)
}

func TestAdminSectionsDatabaseConflictAndReadRace(t *testing.T) {
	f := newFakeAdminSections()
	h := adminSectionsTestHandler(t, f)
	path := Prefix + "/admin/sections/s1"
	tag := do(t, h, http.MethodGet, path, "", bearer(adminToken)).Header().Get("ETag")
	f.mismatch = true
	failed := do(t, h, http.MethodPatch, path, `{"title":"Draft"}`, with(bearer(adminToken), "If-Match", tag))
	requireProblem(t, failed, TypePreconditionFailed)
	current := do(t, h, http.MethodGet, path, "", bearer(adminToken)).Header().Get("ETag")
	if current == tag || failed.Header().Get("ETag") != current || f.updates != 1 || f.view.Title != "Original" {
		t.Fatal("database mismatch lost current tag or retried draft")
	}
	wildcard := do(t, h, http.MethodPatch, path, `{"featured":false,"enabled":false,"config":{}}`, with(bearer(adminToken), "If-Match", "*"))
	if wildcard.Code != 200 {
		t.Fatalf("wildcard %d %s", wildcard.Code, wildcard.Body)
	}
	f.readMismatch = true
	requireProblem(t, do(t, h, http.MethodGet, path, "", bearer(adminToken)), TypeConflict)
}
