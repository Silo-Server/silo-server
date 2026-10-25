package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/blobstore/blobstoretest"
	"github.com/Silo-Server/silo-server/internal/catalogseed"
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

// A remote import URL that reaches a blocked address is reported as an
// invalid remote URL, the same as one of the wrong shape.
func TestCatalogRemoteImportRefusesMetadataAddress(t *testing.T) {
	_, err := readImportDataFromRemoteURL(context.Background(), "http://169.254.169.254/latest/seed.json.gz")
	if !errors.Is(err, errCatalogSeedImportInvalidRemoteURL) || !errors.Is(err, catalogseed.ErrRemoteAddressNotAllowed) {
		t.Fatalf("metadata address error = %v, want errCatalogSeedImportInvalidRemoteURL", err)
	}
}

func TestCatalogImportSourceProblemNamesDownloadLimit(t *testing.T) {
	apiErr, ok := errors.AsType[*APIError](catalogImportSourceProblem(catalogseed.ErrRemoteBundleTooLarge))
	if !ok || apiErr.Status != http.StatusBadRequest || !strings.Contains(apiErr.Message, "GiB download limit") {
		t.Fatalf("oversize source problem = %+v, want a 400 naming the download limit", apiErr)
	}
}
