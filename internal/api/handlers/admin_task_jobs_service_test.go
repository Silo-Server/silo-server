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

type cancellationRequestingRepository struct {
	fakeAdminJobRepository
}

func (r *cancellationRequestingRepository) RequestCancellation(_ context.Context, id string) (*models.AdminJob, error) {
	if r.job == nil || r.job.ID != id {
		return nil, adminjob.ErrJobNotFound
	}
	r.job.CancelRequested = true
	cp := *r.job
	return &cp, nil
}

// The cancel endpoint sets the shared flag that any node's runner polls. A
// cleanup running in this process is also stopped through the cancel
// registry, so it does not keep deleting until the next poll.
func TestRequestAdminTaskJobCancellationStopsALocalImageCacheCleanup(t *testing.T) {
	job := &models.AdminJob{ID: "cleanup", JobType: adminjob.JobTypeImageCacheCleanup, Status: adminjob.StatusRunning}
	h := NewAdminJobsHandler(&cancellationRequestingRepository{fakeAdminJobRepository{job: job}}, nil)
	h.CancelRegistry = adminjob.NewCancelRegistry()
	stopped := false
	defer h.CancelRegistry.Register(job.ID, func() { stopped = true })()

	got, err := h.RequestAdminTaskJobCancellation(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CancelRequested || !stopped {
		t.Fatalf("cancel_requested=%v stopped=%v, want the flag set and the local cleanup stopped", got.CancelRequested, stopped)
	}
}
