package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
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
	previewRequest                                                        handlers.AdminSectionPreviewRequest
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
	f.view.Scope = in.Scope
	f.view.LibraryID = in.LibraryID
	f.view.Title = in.Title
	f.view.Config = in.Config
	return f.view, nil
}
func (f *fakeAdminSections) UpdateAdminSection(_ context.Context, _ string, in handlers.AdminSectionUpdate) (handlers.AdminSection, error) {
	f.updates++
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
	return handlers.AdminSectionPreviewResult{Items: f.preview, TotalCount: len(f.preview)}, nil
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

func TestAdminSectionsCanonicalAndGuards(t *testing.T) {
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			f := newFakeAdminSections()
			h := adminSectionsTestHandler(t, f)
			path := Prefix + "/admin/sections/s1"
			read := do(t, h, http.MethodGet, path, "", bearer(adminToken))
			tag := read.Header().Get("ETag")
			if read.Code != 200 || tag == "" || strings.HasPrefix(tag, "W/") {
				t.Fatalf("canonical %d %s tag%s", read.Code, read.Body, tag)
			}
			again := do(t, h, http.MethodGet, path, "", bearer(adminToken))
			if again.Body.String() != read.Body.String() || again.Header().Get("ETag") != tag {
				t.Fatal("same canonical witness produced different bytes")
			}
			if !strings.Contains(read.Body.String(), `"enabled":false`) || !strings.Contains(read.Body.String(), `"config":{}`) || !strings.Contains(read.Body.String(), `.000Z`) {
				t.Fatalf("canonical fields: %s", read.Body)
			}
			body := ""
			if method == http.MethodPatch {
				body = `{"title":"Edited"}`
			}
			requireProblem(t, do(t, h, method, path, body, bearer(adminToken)), TypePreconditionRequired)
			stale := do(t, h, method, path, body, with(bearer(adminToken), "If-Match", `"stale"`))
			requireProblem(t, stale, TypePreconditionFailed)
			if stale.Header().Get("ETag") != tag || f.updates+f.deletes != 0 {
				t.Fatal("stale request reached writer or omitted current validator")
			}
			fresh := do(t, h, method, path, body, with(bearer(adminToken), "If-Match", tag))
			want := 200
			if method == http.MethodDelete {
				want = 204
			}
			if fresh.Code != want {
				t.Fatalf("fresh %d %s", fresh.Code, fresh.Body)
			}
			if method == http.MethodPatch && (f.view.Title != "Edited" || fresh.Header().Get("ETag") == tag) {
				t.Fatal("fresh update lost value or revision")
			}
		})
	}
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
func TestAdminSectionsNullsScopeAndConfig(t *testing.T) {
	f := newFakeAdminSections()
	h := adminSectionsTestHandler(t, f)
	path := Prefix + "/admin/sections/s1"
	for _, field := range []string{"position", "section_type", "title", "featured", "item_limit", "enabled", "config"} {
		response := do(t, h, http.MethodPatch, path, `{"`+field+`":null}`, with(bearer(adminToken), "If-Match", "*"))
		if response.Code != 422 {
			t.Fatalf("null %s: %d %s", field, response.Code, response.Body)
		}
	}
	if f.updates != 0 {
		t.Fatal("null patch reached service")
	}
	for _, query := range []string{"?scope=library", "?scope=home&library_id=1"} {
		requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/sections/order"+query, "", bearer(adminToken)), TypeValidationFailed)
	}
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/sections/order?scope=library&library_id=invalid", "", bearer(adminToken)), TypeNotFound)
	payload := `{"scope":"library","library_id":"7","section_type":"recently_added","title":"Recipe","config":{"filter_library_ids":[7,8],"groups":[{"rules":[{"field":"year","op":"between","value":[2000,2020]}]}]}}`
	response := do(t, h, http.MethodPost, Prefix+"/admin/sections", payload, bearer(adminToken))
	if response.Code != 201 {
		t.Fatalf("create %d %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), `"library_id":"7"`) || !strings.Contains(response.Body.String(), `"filter_library_ids":[7,8]`) {
		t.Fatalf("ID/config roundtrip %s", response.Body)
	}
	if f.view.LibraryID == nil || *f.view.LibraryID != 7 {
		t.Fatal("top-level library ID not converted")
	}
}
func TestAdminSectionsOrderAndDefaultsGuards(t *testing.T) {
	for _, path := range []string{"/order", "/defaults"} {
		t.Run(path, func(t *testing.T) {
			f := newFakeAdminSections()
			h := adminSectionsTestHandler(t, f)
			base := Prefix + "/admin/sections"
			tag := do(t, h, http.MethodGet, base+"/order?scope=home", "", bearer(adminToken)).Header().Get("ETag")
			body := `{"ordered_ids":["s1"]}`
			if path == "/defaults" {
				body = `{"reset_profiles":false}`
			}
			requireProblem(t, do(t, h, http.MethodPut, base+path+"?scope=home", body, bearer(adminToken)), TypePreconditionRequired)
			f.mismatch = true
			failed := do(t, h, http.MethodPut, base+path+"?scope=home", body, with(bearer(adminToken), "If-Match", tag))
			requireProblem(t, failed, TypePreconditionFailed)
			current := do(t, h, http.MethodGet, base+"/order?scope=home", "", bearer(adminToken)).Header().Get("ETag")
			if current == tag || failed.Header().Get("ETag") != current || f.reorders+f.restores != 1 {
				t.Fatal("scope mismatch replayed or missed current validator")
			}
			success := do(t, h, http.MethodPut, base+path+"?scope=home", body, with(bearer(adminToken), "If-Match", current))
			if success.Code != 200 {
				t.Fatalf("fresh scope mutation %d %s", success.Code, success.Body)
			}
		})
	}
}
func TestAdminSectionsCapabilitiesPreviewAndResetPreflight(t *testing.T) {
	f := newFakeAdminSections()
	f.caps.ResetProfiles = false
	h := adminSectionsTestHandler(t, f)
	base := Prefix + "/admin/sections"
	caps := do(t, h, http.MethodGet, base+"/capabilities", "", bearer(adminToken))
	if caps.Code != 200 || !strings.Contains(caps.Body.String(), `"reset_profiles":false`) {
		t.Fatalf("capabilities %d %s", caps.Code, caps.Body)
	}
	tag := do(t, h, http.MethodGet, base+"/order?scope=home", "", bearer(adminToken)).Header().Get("ETag")
	requireProblem(t, do(t, h, http.MethodPut, base+"/defaults?scope=home", `{"reset_profiles":true}`, with(bearer(adminToken), "If-Match", tag)), TypeCapabilityUnsupported)
	if f.restores != 0 {
		t.Fatal("unsupported provider reset reached writer")
	}
	f.preview = []*models.MediaItem{{ContentID: "item1", Title: "Preview", Type: "movie", PosterPath: "private/storage/path", MetadataS3Path: "private/metadata"}}
	preview := do(t, h, http.MethodPost, base+"/preview", `{"section_type":"recently_added","config":{}}`, bearer(adminToken))
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), `"content_id":"item1"`) || strings.Contains(preview.Body.String(), "private/") {
		t.Fatalf("preview transport %d %s", preview.Code, preview.Body)
	}
	f.caps.Preview = false
	requireProblem(t, do(t, h, http.MethodPost, base+"/preview", `{"section_type":"recently_added"}`, bearer(adminToken)), TypeCapabilityUnsupported)
	if f.previews != 1 {
		t.Fatal("unsupported preview reached fetcher")
	}
	deps, _ := libraryDeps(t)
	empty := newTestHandler(t, deps)
	response := do(t, empty, http.MethodGet, base+"/capabilities", "", bearer(adminToken))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"available":false`) {
		t.Fatalf("missing service caps %d %s", response.Code, response.Body)
	}
}
func TestAdminSectionsActingAdminAndDemo(t *testing.T) {
	f := newFakeAdminSections()
	deps, _ := libraryDeps(t)
	deps.AdminSections = f
	deps.DemoSettings = fakeSettings{demo: true}
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/sections/s1"
	for _, headers := range []map[string]string{bearer(memberToken), with(bearer(adminToken), "X-Profile-Id", "p-owner")} {
		requireProblem(t, do(t, h, http.MethodGet, path, "", headers), TypePermissionDenied)
	}
	response := do(t, h, http.MethodDelete, path, "", with(bearer(adminToken), "If-Match", "*"))
	if response.Code != 204 || f.deletes != 1 {
		t.Fatalf("demo must permit acting admin: %d %s", response.Code, response.Body)
	}
}

func TestAdminSectionsListAndBulkTransport(t *testing.T) {
	f := newFakeAdminSections()
	h := adminSectionsTestHandler(t, f)
	path := Prefix + "/admin/sections"
	listed := do(t, h, http.MethodGet, path+"?scope=home", "", bearer(adminToken))
	if listed.Code != 200 || !strings.Contains(listed.Body.String(), `"items":[`) || !strings.Contains(listed.Body.String(), `"enabled":false`) {
		t.Fatalf("definition collection %d %s", listed.Code, listed.Body)
	}
	for _, body := range []string{
		`{"scope":"library","library_ids":[],"section_type":"recently_added","title":"Bulk"}`,
		`{"scope":"home","library_ids":["1"],"section_type":"recently_added","title":"Bulk"}`,
		`{"scope":"library","library_ids":[1],"section_type":"recently_added","title":"Bulk"}`,
		`{"scope":"library","library_ids":["1"],"section_type":"recently_added","title":"Bulk","config":null}`,
	} {
		response := do(t, h, http.MethodPost, path+"/bulk", body, bearer(adminToken))
		if response.Code != 422 {
			t.Fatalf("invalid bulk %d %s", response.Code, response.Body)
		}
	}
	if f.bulks != 0 {
		t.Fatal("invalid bulk reached service")
	}
	valid := do(t, h, http.MethodPost, path+"/bulk", `{"scope":"library","library_ids":["1","2"],"section_type":"recently_added","title":"Bulk","config":{}}`, bearer(adminToken))
	if valid.Code != 200 || f.bulks != 1 || !strings.Contains(valid.Body.String(), `"created":1`) {
		t.Fatalf("valid bulk %d %s", valid.Code, valid.Body)
	}
	f.deleted = true
	empty := do(t, h, http.MethodGet, path+"?scope=home", "", bearer(adminToken))
	if empty.Code != 200 || !strings.Contains(empty.Body.String(), `"items":[]`) {
		t.Fatalf("empty list %d %s", empty.Code, empty.Body)
	}
	requireProblem(t, do(t, h, http.MethodGet, path+"/s1", "", bearer(adminToken)), TypeNotFound)
}

func TestAdminSectionsSynchronousBulkLimit(t *testing.T) {
	for _, count := range []int{100, 101} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			f := newFakeAdminSections()
			h := adminSectionsTestHandler(t, f)
			ids := make([]ID, count)
			for i := range ids {
				ids[i] = ID(strconv.Itoa(i + 1))
			}
			body, err := json.Marshal(AdminSectionBulkCreate{Scope: "library", LibraryIDs: ids, SectionType: "recently_added", Title: "Bulk", Config: SectionConfig{}})
			if err != nil {
				t.Fatal(err)
			}
			response := do(t, h, http.MethodPost, Prefix+"/admin/sections/bulk", string(body), bearer(adminToken))
			if count == 100 {
				if response.Code != http.StatusOK || f.bulks != 1 || f.bulkLibraryCount != 100 {
					t.Fatalf("100-library boundary: status=%d calls=%d body=%s", response.Code, f.bulks, response.Body)
				}
			} else if response.Code != http.StatusUnprocessableEntity || f.bulks != 0 {
				t.Fatalf("oversized bulk reached service: status=%d calls=%d body=%s", response.Code, f.bulks, response.Body)
			}
		})
	}
}

// An omitted, empty, or scalar library scope must not collapse into a non-nil
// empty library_ids slice, which the fetcher reads as "no libraries" and
// answers with nothing.
func TestAdminSectionPreviewLeavesAbsentLibraryScopeUnscoped(t *testing.T) {
	f := newFakeAdminSections()
	f.preview = []*models.MediaItem{{ContentID: "item1", Title: "Preview", Type: "movie"}}
	h := adminSectionsTestHandler(t, f)
	path := Prefix + "/admin/sections/preview"
	for _, tc := range []struct {
		name   string
		body   string
		want   []int
		scalar bool
	}{
		{name: "omitted", body: `{"section_type":"recently_added","config":{}}`},
		{name: "empty list", body: `{"section_type":"recently_added","config":{},"library_ids":[]}`},
		{name: "scalar", body: `{"section_type":"recently_added","config":{},"library_id":"7"}`, scalar: true},
		{name: "explicit list", body: `{"section_type":"recently_added","config":{},"library_ids":["7"]}`, want: []int{7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, http.MethodPost, path, tc.body, bearer(adminToken))
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"content_id":"item1"`) {
				t.Fatalf("preview %d %s", rec.Code, rec.Body)
			}
			if got := f.previewRequest.LibraryIDs; !slices.Equal(got, tc.want) || (tc.want == nil && got != nil) {
				t.Fatalf("library_ids = %v (nil=%t), want %v", got, got == nil, tc.want)
			}
			if scalar := f.previewRequest.LibraryID; tc.scalar != (scalar != nil) || (tc.scalar && *scalar != 7) {
				t.Fatalf("library_id = %v, scalar request %t", scalar, tc.scalar)
			}
		})
	}
}
