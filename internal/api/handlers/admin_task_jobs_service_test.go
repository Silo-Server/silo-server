package handlers

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

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

// A queued cleanup, new or yielded between slices, is ended by the cancel
// request itself rather than left canceling until a runner claims it.
func TestRequestAdminTaskJobCancellationEndsAQueuedImageCacheCleanup(t *testing.T) {
	job := &models.AdminJob{ID: "cleanup", JobType: adminjob.JobTypeImageCacheCleanup, Status: adminjob.StatusQueued, ProgressCurrent: 3, ProgressTotal: 8}
	h := NewAdminJobsHandler(&cancellationRequestingRepository{fakeAdminJobRepository{job: job}}, nil)

	got, err := h.RequestAdminTaskJobCancellation(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != adminjob.StatusCancelled || got.Message != adminjob.QueuedImageCacheCleanupCanceledMessage(3, 8) {
		t.Fatalf("status=%q message=%q, want the queued cleanup canceled at once", got.Status, got.Message)
	}
}

// When a runner claims the cleanup between the flag and the queued cancel, the
// request still succeeds and the claim ends the job from the flag.
func TestRequestAdminTaskJobCancellationLeavesAClaimedCleanupToItsRunner(t *testing.T) {
	job := &models.AdminJob{ID: "cleanup", JobType: adminjob.JobTypeImageCacheCleanup, Status: adminjob.StatusQueued}
	h := NewAdminJobsHandler(&cancellationRequestingRepository{fakeAdminJobRepository{job: job, promoteToRunningOnCancelQueued: true}}, nil)

	got, err := h.RequestAdminTaskJobCancellation(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == adminjob.StatusCancelled || !got.CancelRequested {
		t.Fatalf("status=%q cancel_requested=%v, want a pending cancel left to the runner", got.Status, got.CancelRequested)
	}
}

type claimCancelsFirstRepository struct {
	cancellationRequestingRepository
}

func (r *claimCancelsFirstRepository) CancelQueued(context.Context, string, string, time.Time) (*models.AdminJob, error) {
	cp := *r.job
	cp.Status = adminjob.StatusCancelled
	r.job = &cp
	return nil, adminjob.ErrJobNotCancellable
}

// When a runner claims the flagged cleanup and ends it before the queued
// cancel runs, the request reports the canceled job rather than the stale
// queued one.
func TestRequestAdminTaskJobCancellationReportsACleanupItsClaimEnded(t *testing.T) {
	job := &models.AdminJob{ID: "cleanup", JobType: adminjob.JobTypeImageCacheCleanup, Status: adminjob.StatusQueued}
	h := NewAdminJobsHandler(&claimCancelsFirstRepository{cancellationRequestingRepository{fakeAdminJobRepository{job: job}}}, nil)

	got, err := h.RequestAdminTaskJobCancellation(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != adminjob.StatusCancelled {
		t.Fatalf("status=%q, want the canceled job the claim recorded", got.Status)
	}
}
