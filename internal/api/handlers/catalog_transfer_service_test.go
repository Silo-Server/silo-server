package handlers

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/blobstore/blobstoretest"
	"github.com/Silo-Server/silo-server/internal/catalogseed"
	"github.com/Silo-Server/silo-server/internal/s3client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type catalogTransferStore struct {
	signed int
	expiry time.Duration
}

func (*catalogTransferStore) Bucket() string { return "fixture" }
func (s *catalogTransferStore) PresignGetURL(_ context.Context, _, _ string, expiry time.Duration) (string, error) {
	s.signed++
	s.expiry = expiry
	return "https://example.invalid/seed.json.gz", nil
}
func (*catalogTransferStore) GetObject(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("unexpected storage read")
}
func (*catalogTransferStore) UploadFile(context.Context, string, string, string, string) (int64, error) {
	return 0, errors.New("unexpected upload")
}
func (*catalogTransferStore) ListObjectInfos(context.Context, string, string) ([]s3client.ObjectInfo, error) {
	return nil, nil
}
func (*catalogTransferStore) DeleteObject(context.Context, string, string) error { return nil }
func (*catalogTransferStore) MakeObjectPublic(context.Context, string, string) error {
	return errors.New("ACL changes are forbidden in this test")
}
func (*catalogTransferStore) PublicURL(string, string) (string, error) {
	return "", errors.New("public ACL URL is forbidden in this test")
}

func catalogTransferPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
func catalogTransferAdmin(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var userID int
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(username, role) VALUES($1, 'admin') RETURNING id`, "catalog-transfer-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID); err != nil {
			t.Errorf("delete fixture account: %v", err)
		}
	})
	return userID
}

func TestCatalogTransferPersistsJobsAndSignedLink(t *testing.T) {
	pool := catalogTransferPool(t)
	userID := catalogTransferAdmin(t, pool)
	repo := adminjob.NewRepository(pool)
	store := &catalogTransferStore{}
	h := NewCatalogSeedHandler(nil, repo, store)
	export, err := h.CreateCatalogExportJob(t.Context(), userID, catalogseed.ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM admin_jobs WHERE id=$1`, export.ID) })
	saved, err := repo.GetByID(t.Context(), export.ID)
	if err != nil || saved.Status != adminjob.StatusQueued || saved.CreatedByUserID != userID {
		t.Fatalf("unpersisted job: %#v %v", saved, err)
	}
	if _, err := h.CreateCatalogExportJob(t.Context(), userID, catalogseed.ExportOptions{}); !errors.Is(err, adminjob.ErrActiveJobConflict) {
		t.Fatalf("duplicate queue: %v", err)
	}
	if _, err := h.PublishCatalogExportJob(t.Context(), export.ID); err == nil {
		t.Fatal("published queued job")
	}
	if _, err := pool.Exec(t.Context(), `UPDATE admin_jobs SET status='completed',artifact_bucket='fixture',artifact_key='seed.json.gz' WHERE id=$1`, export.ID); err != nil {
		t.Fatal(err)
	}
	first, err := h.PublishCatalogExportJob(t.Context(), export.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.PublishCatalogExportJob(t.Context(), export.ID)
	if err != nil {
		t.Fatal(err)
	}
	if store.signed != 1 || store.expiry != 7*24*time.Hour || first.PublicURL != second.PublicURL || !first.PublishedAt.Equal(*second.PublishedAt) {
		t.Fatalf("link renewed: %#v %#v", first, second)
	}
	path := filepath.Join(t.TempDir(), "seed.json.gz")
	if err := os.WriteFile(path, []byte("worker reads later"), 0600); err != nil {
		t.Fatal(err)
	}
	job, err := h.CreateCatalogImportJob(t.Context(), userID, CatalogImportSourceSelection{LocalPath: path}, catalogseed.ImportOptions{ConflictMode: catalogseed.ConflictModeSkipExisting})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM admin_jobs WHERE id=$1`, job.ID) })
	saved, err = repo.GetByID(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	var request adminjob.CatalogImportRequest
	if err := json.Unmarshal(saved.RequestPayload, &request); err != nil {
		t.Fatal(err)
	}
	if request.LocalPath != path || saved.Status != adminjob.StatusQueued || saved.CompletedAt != nil || saved.CreatedByUserID != userID {
		t.Fatalf("unexpected queued import: %#v", saved)
	}
}

func TestCatalogTransferSynchronousImportCommitsBeforeReturning(t *testing.T) {
	pool := catalogTransferPool(t)
	h := NewCatalogSeedHandler(catalogseed.NewService(pool, nil, nil), nil, nil)
	root := t.TempDir()
	name := "catalog-transfer-" + filepath.Base(root)
	bundle := catalogseed.Bundle{Manifest: catalogseed.Manifest{FormatVersion: catalogseed.CurrentBundleVersion}, Libraries: []catalogseed.LibraryRecord{{ExportedID: 1, Paths: []string{root}, Type: "movies", Name: name, Enabled: true}}}
	var data bytes.Buffer
	writer := gzip.NewWriter(&data)
	if err := json.NewEncoder(writer).Encode(bundle); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "seed.json.gz")
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE name=$1`, name) })
	result, err := h.ImportCatalog(t.Context(), CatalogImportSourceSelection{LocalPath: path}, catalogseed.ImportOptions{ConflictMode: catalogseed.ConflictModeSkipExisting})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM media_folders WHERE name=$1`, name).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if result.LibrariesCreated != 1 || count != 1 {
		t.Fatalf("import returned before persistence: %#v count=%d", result, count)
	}
}

func TestCatalogPublishInvalidJobPreservesV1BadRequest(t *testing.T) {
	pool := catalogTransferPool(t)
	userID := catalogTransferAdmin(t, pool)
	repo := adminjob.NewRepository(pool)
	store := &catalogTransferStore{}
	h := NewCatalogSeedHandler(nil, repo, store)
	job, err := h.CreateCatalogExportJob(t.Context(), userID, catalogseed.ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM admin_jobs WHERE id=$1`, job.ID) })
	router := chi.NewRouter()
	router.Post("/api/v1/admin/catalog/export-jobs/{id}/publish", h.HandlePublishExportJob)
	for _, tc := range []struct{ name, kind, status, bucket, key string }{
		{"incomplete", adminjob.JobTypeCatalogExport, adminjob.StatusQueued, "fixture", "seed.json.gz"},
		{"non-export", adminjob.JobTypeCatalogImport, adminjob.StatusCompleted, "fixture", "seed.json.gz"},
		{"missing-bucket", adminjob.JobTypeCatalogExport, adminjob.StatusCompleted, "", "seed.json.gz"},
		{"missing-key", adminjob.JobTypeCatalogExport, adminjob.StatusCompleted, "fixture", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pool.Exec(t.Context(), `UPDATE admin_jobs SET job_type=$2,status=$3,artifact_bucket=$4,artifact_key=$5 WHERE id=$1`, job.ID, tc.kind, tc.status, tc.bucket, tc.key); err != nil {
				t.Fatal(err)
			}
			_, err := h.PublishCatalogExportJob(t.Context(), job.ID)
			apiErr, ok := errors.AsType[*APIError](err)
			if !ok || apiErr.Status != http.StatusConflict || apiErr.Code != "conflict" {
				t.Fatalf("shared service error = %#v", err)
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/catalog/export-jobs/"+job.ID+"/publish", nil)
			router.ServeHTTP(rec, req)
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusBadRequest || body.Error != "bad_request" {
				t.Fatalf("v1 publish = %d %s", rec.Code, rec.Body)
			}
			if store.signed != 0 {
				t.Fatal("invalid job reached signing")
			}
		})
	}
}

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
