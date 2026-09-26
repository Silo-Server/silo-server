package apiv2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogsvc "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeAdminCollections struct {
	AdminCollectionService
	AdminCollectionExtrasService
	view                                               handlers.AdminCollection
	revision                                           int64
	reads, updates, deletes, creates, imports, applies int
	mismatch                                           bool
	deleted                                            bool
	job                                                *models.AdminJob
	syncErr                                            error
	template                                           handlers.AdminCollectionTemplateResult
}

func newFakeAdminCollections() *fakeAdminCollections {
	return &fakeAdminCollections{revision: 1, view: handlers.AdminCollection{ID: "c1", LibraryID: 1, LibraryIDs: []int{1}, Title: "Original", CollectionType: "manual", CreatedAt: fixedTime().Format("2006-01-02T15:04:05Z07:00"), UpdatedAt: fixedTime().Format("2006-01-02T15:04:05Z07:00")}}
}
func (f *fakeAdminCollections) GetAdminCollection(_ context.Context, id string) (handlers.AdminCollection, int64, error) {
	f.reads++
	if f.deleted || id != "c1" {
		return handlers.AdminCollection{}, 0, catalogsvc.ErrLibraryCollectionNotFound
	}
	return f.view, f.revision, nil
}
func (f *fakeAdminCollections) UpdateAdminCollection(_ context.Context, _ string, cmd handlers.AdminCollectionUpdate) (handlers.AdminCollection, error) {
	f.updates++
	f.revision++
	if f.mismatch {
		f.mismatch = false
		return handlers.AdminCollection{}, catalogsvc.ErrLibraryCollectionRevisionMismatch
	}
	if cmd.Title != nil {
		f.view.Title = *cmd.Title
	}
	return f.view, nil
}
func (f *fakeAdminCollections) DeleteAdminCollection(context.Context, string) error {
	f.deletes++
	if f.mismatch {
		f.mismatch = false
		f.revision++
		return catalogsvc.ErrLibraryCollectionRevisionMismatch
	}
	f.deleted = true
	return nil
}
func (f *fakeAdminCollections) CreateAdminCollection(context.Context, handlers.AdminCollectionCreate) (handlers.AdminCollection, error) {
	f.creates++
	return f.view, nil
}
func (f *fakeAdminCollections) ImportAdminMDBList(context.Context, handlers.AdminCollectionImportMDBList) (handlers.AdminCollectionImportResult, error) {
	f.imports++
	return handlers.AdminCollectionImportResult{Collection: f.view}, nil
}
func (f *fakeAdminCollections) ApplyAdminCollectionTemplate(context.Context, string, handlers.AdminCollectionTemplateApply) (handlers.AdminCollectionTemplateResult, error) {
	f.applies++
	if f.template.BundleID != "" {
		return f.template, nil
	}
	return handlers.AdminCollectionTemplateResult{BundleID: "bundle"}, nil
}
func (f *fakeAdminCollections) SyncAdminCollection(context.Context, string) (*models.LibraryCollectionSyncRun, error) {
	if f.syncErr != nil {
		return nil, f.syncErr
	}
	return &models.LibraryCollectionSyncRun{ID: "run", CollectionID: "c1", Status: "completed", CreatedAt: fixedTime()}, nil
}
func (f *fakeAdminCollections) QueueAdminCollectionTemplate(context.Context, string, handlers.AdminCollectionTemplateApply, int) (*models.AdminJob, error) {
	return f.job, nil
}
func adminCollectionsTestHandler(t *testing.T, f *fakeAdminCollections) http.Handler {
	t.Helper()
	deps, _ := libraryDeps(t)
	deps.AdminCollections = f
	return newTestHandler(t, deps)
}

func TestAdminCollectionsCanonicalAndMutationGuards(t *testing.T) {
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			f := newFakeAdminCollections()
			h := adminCollectionsTestHandler(t, f)
			path := "/api/v2/admin/collections/c1"
			read := do(t, h, http.MethodGet, path, "", bearer(adminToken))
			tag := read.Header().Get("ETag")
			if read.Code != 200 || tag == "" || strings.HasPrefix(tag, "W/") {
				t.Fatalf("canonical read %d %s tag=%s", read.Code, read.Body, tag)
			}
			again := do(t, h, http.MethodGet, path, "", bearer(adminToken))
			if again.Body.String() != read.Body.String() || again.Header().Get("ETag") != tag {
				t.Fatal("unchanged canonical read changed")
			}
			body := ""
			if method == http.MethodPatch {
				body = `{"title":"Edited"}`
			}
			if rec := do(t, h, method, path, body, bearer(adminToken)); rec.Code != 428 {
				t.Fatalf("missing guard %d %s", rec.Code, rec.Body)
			}
			if rec := do(t, h, method, path, body, with(bearer(adminToken), "If-Match", `"stale"`)); rec.Code != 412 {
				t.Fatalf("stale guard %d %s", rec.Code, rec.Body)
			}
			if f.updates+f.deletes != 0 {
				t.Fatal("rejected precondition reached writer")
			}
			rec := do(t, h, method, path, body, with(bearer(adminToken), "If-Match", tag))
			want := 200
			if method == http.MethodDelete {
				want = 204
			}
			if rec.Code != want {
				t.Fatalf("exact guard %d %s", rec.Code, rec.Body)
			}
			if method == http.MethodPatch && (f.view.Title != "Edited" || rec.Header().Get("ETag") == tag) {
				t.Fatal("successful update lost result or new tag")
			}
		})
	}
}
func TestAdminCollectionsDatabaseMismatchRefreshesCurrentValidator(t *testing.T) {
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			f := newFakeAdminCollections()
			h := adminCollectionsTestHandler(t, f)
			path := "/api/v2/admin/collections/c1"
			tag := do(t, h, http.MethodGet, path, "", bearer(adminToken)).Header().Get("ETag")
			f.mismatch = true
			body := ""
			if method == http.MethodPatch {
				body = `{"title":"Draft"}`
			}
			rec := do(t, h, method, path, body, with(bearer(adminToken), "If-Match", tag))
			if rec.Code != 412 || f.updates+f.deletes != 1 || f.view.Title != "Original" || f.deleted {
				t.Fatalf("DB conflict replayed or lost %d %s", rec.Code, rec.Body)
			}
			now := do(t, h, http.MethodGet, path, "", bearer(adminToken)).Header().Get("ETag")
			if now == tag || rec.Header().Get("ETag") != now {
				t.Fatalf("conflict missing current validator: %s versus %s", rec.Body, now)
			}
			rec = do(t, h, method, path, body, with(bearer(adminToken), "If-Match", "*"))
			want := 200
			if method == http.MethodDelete {
				want = 204
			}
			if rec.Code != want {
				t.Fatalf("wildcard %d %s", rec.Code, rec.Body)
			}
		})
	}
}
func TestAdminCollectionsActingAdminAndDemo(t *testing.T) {
	f := newFakeAdminCollections()
	deps, _ := libraryDeps(t)
	deps.AdminCollections = f
	deps.DemoSettings = fakeSettings{demo: true}
	h := newTestHandler(t, deps)
	path := "/api/v2/admin/collections/c1"
	for _, headers := range []map[string]string{bearer(memberToken), with(bearer(adminToken), "X-Profile-Id", "p-owner")} {
		requireProblem(t, do(t, h, http.MethodGet, path, "", headers), TypePermissionDenied)
	}
	if f.reads != 0 {
		t.Fatal("unauthorized request reached service")
	}
	if rec := do(t, h, http.MethodPatch, path, `{"title":"Demo admin"}`, with(bearer(adminToken), "If-Match", "*")); rec.Code != 200 {
		t.Fatalf("demo blocked acting admin %d %s", rec.Code, rec.Body)
	}
}
func TestAdminCollectionsRejectExplicitNullBeforeWrites(t *testing.T) {
	f := newFakeAdminCollections()
	h := adminCollectionsTestHandler(t, f)
	for _, tc := range []struct{ path, body string }{
		{"/api/v2/admin/collections", `{"title":"Create","library_id":"1","description":null}`},
		{"/api/v2/admin/collections/import/mdblist", `{"title":"Import","library_id":"1","url":"https://example.invalid/list","limit":null}`},
		{"/api/v2/admin/collections/template-bundles/bundle/apply", `{"library_ids":["1"],"featured":null}`},
		{"/api/v2/admin/collections/template-bundles/bundle/apply", `{"library_ids":["1"],"featured":{"home":null}}`},
		{"/api/v2/admin/collections/template-bundles/bundle/apply", `{"library_ids":["1"],"featured":{"libraries":null}}`},
	} {
		t.Run(tc.body, func(t *testing.T) {
			requireProblem(t, do(t, h, http.MethodPost, tc.path, tc.body, bearer(adminToken)), TypeValidationFailed)
		})
	}
	if f.creates+f.imports+f.applies != 0 {
		t.Fatal("null validation reached writer")
	}
}
func TestAdminCollectionJobSafeConditionalKindScopedMonitor(t *testing.T) {
	f := newFakeAdminCollections()
	f.job = &models.AdminJob{ID: "collection-job", JobType: adminjob.JobTypeTemplateBundleApply, Status: adminjob.StatusQueued, RequestedAt: fixedTime(), RequestPayload: json.RawMessage(`{"secret":"PRIVATE_PAYLOAD"}`), Message: "PRIVATE_MESSAGE", ErrorMessage: "PRIVATE_ERROR"}
	deps, _ := libraryDeps(t)
	deps.AdminCollections = f
	deps.LibraryJobs = &fakeLibraryJobs{job: f.job}
	h := newTestHandler(t, deps)
	accepted := do(t, h, http.MethodPost, "/api/v2/admin/collections/template-bundles/bundle/apply-job", `{"library_ids":["1"]}`, bearer(adminToken))
	if accepted.Code != 202 {
		t.Fatalf("accept %d %s", accepted.Code, accepted.Body)
	}
	location := accepted.Header().Get("Location")
	if location != "/api/v2/admin/collection-jobs/collection-job" {
		t.Fatal(location)
	}
	poll := do(t, h, http.MethodGet, location, "", bearer(adminToken))
	if poll.Code != 200 || poll.Header().Get("ETag") == "" || strings.Contains(poll.Body.String(), "PRIVATE_") {
		t.Fatalf("unsafe poll %d %s", poll.Code, poll.Body)
	}
	conditional := do(t, h, http.MethodGet, location, "", with(bearer(adminToken), "If-None-Match", poll.Header().Get("ETag")))
	if conditional.Code != 304 || conditional.Body.Len() != 0 {
		t.Fatalf("conditional %d %s", conditional.Code, conditional.Body)
	}

	f.job.Status = adminjob.StatusCompleted
	f.job.ResultPayload = json.RawMessage(`{"bundle_id":"bundle","failed":[{"template_id":"one","library_id":1,"reason":"Collection already exists"}]}`)
	completed := do(t, h, http.MethodGet, location, "", with(bearer(adminToken), "If-None-Match", poll.Header().Get("ETag")))
	if completed.Code != 200 || completed.Header().Get("Retry-After") != "" || strings.Contains(completed.Body.String(), "PRIVATE_") {
		t.Fatalf("terminal projection %d %s", completed.Code, completed.Body)
	}
	var result AdminJob
	decodeJSON(t, completed.Body, &result)
	if !result.Terminal || result.Cancelable || result.TemplateResult == nil || result.TemplateResult.Failed[0].LibraryID != "1" || result.TemplateResult.Failed[0].Reason != "operation_failed" {
		t.Fatalf("terminal job lost typed result: %+v", result)
	}
	f.job.JobType = adminjob.JobTypeDeleteLibrary
	requireProblem(t, do(t, h, http.MethodGet, location, "", bearer(adminToken)), TypeNotFound)
}

func TestAdminCollectionOrdersRejectDuplicatesBeforeService(t *testing.T) {
	f := newFakeAdminCollections()
	h := adminCollectionsTestHandler(t, f)
	for _, tc := range []struct{ path, body string }{
		{"/api/v2/admin/collections/c1/items/order", `{"ordered_ids":["item","item"]}`},
		{"/api/v2/admin/libraries/1/collection-groups/order", `{"ordered_ids":["group","group"]}`},
		{"/api/v2/admin/collections/order", `{"library_id":"1","ordered_ids":["c1","c1"]}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			requireProblem(t, do(t, h, http.MethodPut, tc.path, tc.body, with(bearer(adminToken), "If-Match", "*")), TypeValidationFailed)
		})
	}
	// Order methods remain unimplemented: reaching any domain dispatch would panic.
	if f.reads != 0 {
		t.Fatal("duplicate order reached a service read")
	}
}

// A manual collection has no import source, so a sync request is a caller
// mistake rather than a server fault: it must not answer 500.
func TestAdminCollectionSyncRejectsUnsupportedMode(t *testing.T) {
	f := newFakeAdminCollections()
	f.syncErr = fmt.Errorf("%w: %s", catalogsvc.ErrLibraryCollectionSyncModeUnsupported, "")
	h := adminCollectionsTestHandler(t, f)
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/admin/collections/c1/sync", "", bearer(adminToken)), TypeValidationFailed)
	if strings.Contains(p.Detail, "unsupported collection sync mode") {
		t.Fatalf("leaked service diagnostic: %s", p.Detail)
	}
	f.syncErr = nil
	if rec := do(t, h, http.MethodPost, "/api/v2/admin/collections/c1/sync", "", bearer(adminToken)); rec.Code != 200 {
		t.Fatalf("sync %d %s", rec.Code, rec.Body)
	}
}

// The per-entry reason explains a skipped or failed template; v1 returns it and
// v2 must reach the wire with it too.
func TestAdminCollectionTemplateApplyKeepsEntryReason(t *testing.T) {
	f := newFakeAdminCollections()
	f.template = handlers.AdminCollectionTemplateResult{BundleID: "bundle"}
	if err := json.Unmarshal([]byte(`{"bundle_id":"bundle","skipped":[{"template_id":"existing","template_title":"Existing","library_id":1,"library_name":"Movies","reason":"Collection already exists"}],"delete_skipped":[{"library_id":1,"library_name":"Movies","collection_id":"c1","collection_title":"Existing","reason":"in_use_by_section"}],"featured_failed":[{"surface":"home","template_id":"existing","template_title":"Existing","reason":"Library has no featured slot"}]}`), &f.template); err != nil {
		t.Fatal(err)
	}
	h := adminCollectionsTestHandler(t, f)
	rec := do(t, h, http.MethodPost, "/api/v2/admin/collections/template-bundles/bundle/apply", `{"library_ids":["1"]}`, bearer(adminToken))
	if rec.Code != 200 {
		t.Fatalf("apply %d %s", rec.Code, rec.Body)
	}
	// Raw service text is replaced by a stable public code; only the
	// documented reason codes pass through verbatim.
	for _, want := range []string{`"reason":"operation_failed"`, `"reason":"in_use_by_section"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("missing %s in %s", want, rec.Body)
		}
	}
}
