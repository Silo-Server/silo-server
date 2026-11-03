package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/blobstore/blobstoretest"
)

// The v1 job projection only presigns, so a v1 export on a store that cannot
// presign would complete with no way to retrieve it. v1 keeps refusing, before
// any job is queued; v2 streams the artifact through its signed route instead.
func TestCatalogV1ExportJobRefusesStoreThatCannotPresign(t *testing.T) {
	h := NewCatalogSeedHandler(nil, adminjob.NewRepository(nil), blobstore.NewBucketAPI(blobstoretest.New()))
	rec := httptest.NewRecorder()
	h.HandleCreateExportJob(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/catalog/export-jobs", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body)
	}
}
