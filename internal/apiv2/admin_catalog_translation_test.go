package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/metadata/translation"
	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/go-chi/chi/v5"
)

type fakeAdminTranslation struct {
	calls, userID int
	content       string
	jobID         int64
	request       handlers.TranslateMetadataRequest
}

func (f *fakeAdminTranslation) TranslateAdminMetadata(_ context.Context, id string, req handlers.TranslateMetadataRequest, userID int) (*translation.Job, error) {
	f.calls++
	f.content, f.request, f.userID = id, req, userID
	return &translation.Job{ID: 7, ContentID: id, TargetLanguage: req.TargetLanguage, CreatedAt: fixedTime(), UpdatedAt: fixedTime()}, nil
}
func (f *fakeAdminTranslation) ListAdminMetadataTranslationJobs(_ context.Context, id string) ([]translation.Job, error) {
	f.calls++
	f.content = id
	return nil, nil
}
func (f *fakeAdminTranslation) CancelAdminMetadataTranslation(_ context.Context, id string, jobID int64) error {
	f.calls++
	f.content, f.jobID = id, jobID
	return nil
}
func adminTranslationGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if chi.URLParam(r, "id") != "item-1" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func TestAdminTranslationPermissionAndTransport(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeAdminTranslation{}
	deps.AdminMetadataTranslation = f
	deps.PermissionGates[policy.PermissionMetadataCuration] = adminTranslationGate
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/items/item-1/metadata-translation"
	rec := do(t, h, "POST", path, `{"target_language":"de","include_children":false,"force":true}`, bearer(memberToken))
	if rec.Code != 202 || f.userID != 1 || f.calls != 1 || f.request.IncludeChildren == nil || *f.request.IncludeChildren || !f.request.Force || rec.Header().Get("Location") != path+"/jobs" || !strings.Contains(rec.Body.String(), `"id":"7"`) {
		t.Fatalf("enqueue: %d %s %#v", rec.Code, rec.Body, f)
	}
	rec = do(t, h, "GET", path+"/jobs", "", bearer(memberToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"jobs":[]`) {
		t.Fatalf("jobs: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, "POST", path+"/jobs/7/cancel", "", bearer(memberToken))
	if rec.Code != 204 || rec.Body.Len() != 0 || f.jobID != 7 || f.content != "item-1" {
		t.Fatalf("cancel: %d %s %#v", rec.Code, rec.Body, f)
	}
	before := f.calls
	for _, tc := range []struct{ method, path, body string }{{"POST", "", `{"target_language":"de"}`}, {"GET", "/jobs", ""}, {"POST", "/jobs/7/cancel", ""}} {
		rec = do(t, h, tc.method, Prefix+"/admin/items/forbidden/metadata-translation"+tc.path, tc.body, bearer(adminToken))
		if rec.Code != 403 || f.calls != before {
			t.Fatalf("gate bypass: %d calls=%d", rec.Code, f.calls)
		}
	}
	rec = do(t, h, "POST", path, `{"target_language":""}`, bearer(memberToken))
	if rec.Code != 422 || f.calls != before {
		t.Fatalf("invalid reached service: %d", rec.Code)
	}
	deps.PermissionGates = nil
	h = newTestHandler(t, deps)
	rec = do(t, h, "GET", path+"/jobs", "", bearer(adminToken))
	if rec.Code != 503 || f.calls != before {
		t.Fatalf("missing gate: %d %s", rec.Code, rec.Body)
	}
}
func adminCatalogTranslationFixtureCases() []fixtureCase {
	path := Prefix + "/admin/items/item-1/metadata-translation"
	return []fixtureCase{
		{name: "admin_metadata_translation_queued", operationID: "translateAdminItemMetadata", method: "POST", path: path, body: `{"target_language":"de"}`, headers: bearer(memberToken), status: 202, schema: "#/components/schemas/MetadataTranslationJob", assertHeaders: []string{"Content-Type", "Location"}, scenario: "A delegated curator queues the persisted translation job for an authorized item."},
		{name: "admin_metadata_translation_jobs", operationID: "listAdminMetadataTranslationJobs", method: "GET", path: path + "/jobs", headers: bearer(memberToken), status: 200, schema: "#/components/schemas/AdminMetadataTranslationJobs", assertHeaders: []string{"Content-Type"}, scenario: "The recent-job list is bounded and empty arrays remain arrays."},
		{name: "admin_metadata_translation_cancel", operationID: "cancelAdminMetadataTranslation", method: "POST", path: path + "/jobs/7/cancel", headers: bearer(memberToken), status: 204, scenario: "Cancellation binds both authorized content and exact job identity."},
	}
}
