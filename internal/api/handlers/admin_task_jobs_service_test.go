package handlers

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/models"
)

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
