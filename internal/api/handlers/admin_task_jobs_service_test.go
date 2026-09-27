package handlers

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/blobstore/blobstoretest"
	"github.com/Silo-Server/silo-server/internal/models"
)

func completedArtifactJob() *models.AdminJob {
	return &models.AdminJob{
		ID:             "job-1",
		Status:         adminjob.StatusCompleted,
		ArtifactBucket: blobstore.LocalBucket,
		ArtifactKey:    "catalog-seeds/job-1.json.gz",
	}
}

func TestOpenAdminJobArtifactStreamsStoredObject(t *testing.T) {
	store := blobstoretest.New()
	if err := store.Put(context.Background(), "catalog-seeds/job-1.json.gz", []byte("gz")); err != nil {
		t.Fatal(err)
	}
	h := NewAdminJobsHandler(&fakeAdminJobRepository{job: completedArtifactJob()}, blobstore.NewBucketAPI(store))

	download, err := h.OpenAdminJobArtifact(context.Background(), "job-1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = download.Body.Close() }()
	body, _ := io.ReadAll(download.Body)
	if string(body) != "gz" || download.Filename != "job-1.json.gz" {
		t.Fatalf("body=%q filename=%q", body, download.Filename)
	}
}

// A retained job whose object was removed by cleanup or an operator is
// permanently gone. The route answers 404 for ErrJobArtifactNotFound and 503
// for anything else, so the store's not-found must not read as an outage.
func TestOpenAdminJobArtifactReportsMissingObjectAsNotFound(t *testing.T) {
	h := NewAdminJobsHandler(&fakeAdminJobRepository{job: completedArtifactJob()}, blobstore.NewBucketAPI(blobstoretest.New()))

	_, err := h.OpenAdminJobArtifact(context.Background(), "job-1")
	if !errors.Is(err, ErrJobArtifactNotFound) {
		t.Fatalf("err = %v, want ErrJobArtifactNotFound", err)
	}
}
