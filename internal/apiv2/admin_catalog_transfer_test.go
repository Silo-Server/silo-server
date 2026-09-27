package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogsvc "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/catalogseed"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeAdminCatalogTransfer struct {
	calls  int
	err    error
	source handlers.CatalogImportSourceSelection
}

func (f *fakeAdminCatalogTransfer) ExportCatalog(context.Context, catalogseed.ExportOptions) ([]byte, error) {
	f.calls++
	return []byte{31, 139, 8, 0, 1, 2, 3}, f.err
}
func (f *fakeAdminCatalogTransfer) CreateCatalogExportJob(context.Context, int, catalogseed.ExportOptions) (*models.AdminJob, error) {
	f.calls++
	return &models.AdminJob{ID: "catalog-job", JobType: adminjob.JobTypeCatalogExport, Status: adminjob.StatusQueued, RequestedAt: fixedTime()}, f.err
}
func (f *fakeAdminCatalogTransfer) ImportCatalog(_ context.Context, source handlers.CatalogImportSourceSelection, _ catalogseed.ImportOptions) (*catalogseed.ImportResult, error) {
	f.calls++
	f.source = source
	return &catalogseed.ImportResult{ItemsCreated: 3}, f.err
}
func (f *fakeAdminCatalogTransfer) CreateCatalogImportJob(_ context.Context, _ int, source handlers.CatalogImportSourceSelection, _ catalogseed.ImportOptions) (*models.AdminJob, error) {
	f.calls++
	f.source = source
	return &models.AdminJob{ID: "catalog-job", JobType: adminjob.JobTypeCatalogImport, Status: adminjob.StatusQueued, RequestedAt: fixedTime()}, f.err
}
func (f *fakeAdminCatalogTransfer) PublishCatalogExportJob(context.Context, string) (*models.AdminJob, error) {
	f.calls++
	return &models.AdminJob{ID: "catalog-job", PublishedAt: new(fixedTime()), PublicURL: "https://example.invalid/download"}, f.err
}
func (f *fakeAdminCatalogTransfer) GetCatalogSearchStatus(context.Context) catalogsvc.CatalogSearchRuntimeStatus {
	return catalogsvc.CatalogSearchRuntimeStatus{Index: catalogsvc.CatalogSearchIndexStateStatus{LastProcessedEventID: 9007199254740993}}
}
func catalogTransferHandler(t *testing.T, f *fakeAdminCatalogTransfer) http.Handler {
	deps, _ := libraryDeps(t)
	deps.AdminCatalogTransfer = f
	deps.AdminTaskJobs = newFakeAdminTasks()
	deps.AdminCatalogSearch = f
	return newTestHandler(t, deps)
}

func TestCatalogExportDeclaredBinaryNegotiation(t *testing.T) {
	f := &fakeAdminCatalogTransfer{}
	h := catalogTransferHandler(t, f)
	for _, accept := range []string{"", "*/*", "application/*", "application/gzip", "application/json;q=0, application/gzip", "*/*;q=0,application/gzip"} {
		rec := do(t, h, "POST", Prefix+"/admin/catalog/export", `{}`, with(bearer(adminToken), "Accept", accept))
		if rec.Code != 200 || rec.Header().Get("Content-Type") != mediaTypeCatalogGzip || !bytes.Equal(rec.Body.Bytes(), []byte{31, 139, 8, 0, 1, 2, 3}) {
			t.Fatalf("accept %q: %d %v %q", accept, rec.Code, rec.Header(), rec.Body.Bytes())
		}
	}
	before := f.calls
	for _, accept := range []string{"application/json", "text/plain", "application/gzip;q=0,*/*", "*/*;q=0"} {
		rec := do(t, h, "POST", Prefix+"/admin/catalog/export", `{}`, with(bearer(adminToken), "Accept", accept))
		if rec.Code != 406 || rec.Header().Get("Content-Type") != problemContentType || f.calls != before {
			t.Fatalf("refusal %q: %d %s", accept, rec.Code, rec.Body)
		}
	}
	f.err = errors.New("private storage endpoint")
	rec := do(t, h, "POST", Prefix+"/admin/catalog/export", `{}`, with(bearer(adminToken), "Accept", "application/gzip"))
	if rec.Code != 500 || rec.Header().Get("Content-Type") != problemContentType || bytes.Contains(rec.Body.Bytes(), []byte("private storage")) {
		t.Fatalf("binary error: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	rec = do(t, h, "GET", Prefix+"/admin/catalog/search/status", "", with(bearer(adminToken), "Accept", "application/gzip"))
	if rec.Code != 406 {
		t.Fatalf("JSON route accepted gzip: %d", rec.Code)
	}
}

func TestCatalogImportRejectsInvalidSourcesBeforeExecution(t *testing.T) {
	f := &fakeAdminCatalogTransfer{}
	h := catalogTransferHandler(t, f)
	for _, suffix := range []string{"/import", "/import-jobs"} {
		for _, body := range []string{`{"conflict_mode":"skip_existing","path_rewrites":[]}`, `{"local_path":"seed.json.gz","remote_url":"https://example.invalid/seed.json.gz","conflict_mode":"skip_existing","path_rewrites":[]}`, `{"local_path":null,"conflict_mode":"skip_existing","path_rewrites":[]}`, `{"local_path":"seed.json.gz","conflict_mode":"skip_existing","path_rewrites":[null]}`, `{"local_path":"seed.json.gz","conflict_mode":"skip_existing","path_rewrites":[{"from":null,"to":"/new"}]}`} {
			rec := do(t, h, "POST", Prefix+"/admin/catalog"+suffix, body, bearer(adminToken))
			if rec.Code != 422 || f.calls != 0 {
				t.Fatalf("invalid import reached service: %d %s", rec.Code, rec.Body)
			}
		}
	}
	body := `{"local_path":"/seed.json.gz","conflict_mode":"skip_existing","path_rewrites":[]}`
	direct := do(t, h, "POST", Prefix+"/admin/catalog/import", body, bearer(adminToken))
	if direct.Code != 200 || !bytes.Contains(direct.Body.Bytes(), []byte(`"items_created":3`)) {
		t.Fatalf("direct import: %d %s", direct.Code, direct.Body)
	}
	queued := do(t, h, "POST", Prefix+"/admin/catalog/import-jobs", body, bearer(adminToken))
	if queued.Code != 202 || queued.Header().Get("Location") != Prefix+"/admin/jobs/catalog-job" || f.source.LocalPath != "/seed.json.gz" {
		t.Fatalf("queued import: %d %s", queued.Code, queued.Body)
	}
}

func TestCatalogPublishInvalidJobRetainsV2Conflict(t *testing.T) {
	f := &fakeAdminCatalogTransfer{err: &handlers.APIError{Status: http.StatusConflict, Code: "conflict", Message: "A completed catalog export artifact is required"}}
	h := catalogTransferHandler(t, f)
	rec := do(t, h, http.MethodPost, Prefix+"/admin/catalog/export-jobs/catalog-job/publish", "", bearer(adminToken))
	var body struct {
		Type   string `json:"type"`
		Status int    `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusConflict || rec.Header().Get("Content-Type") != problemContentType || body.Type != TypeConflict.URI() || body.Status != http.StatusConflict || f.calls != 1 {
		t.Fatalf("v2 publish = %d %v %s; calls=%d", rec.Code, rec.Header(), rec.Body, f.calls)
	}
}
