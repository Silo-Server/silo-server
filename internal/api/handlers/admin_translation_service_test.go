package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/ai/jobrunner"
	"github.com/Silo-Server/silo-server/internal/metadata/translation"
)

type adminTranslationRepo struct {
	translation.JobRepository
	job       translation.Job
	cancelled int
}

func (r *adminTranslationRepo) GetJob(context.Context, int64) (*translation.Job, error) {
	return &r.job, nil
}
func (r *adminTranslationRepo) FailJob(_ context.Context, _ int64, status translation.JobStatus, _ string) error {
	r.cancelled++
	r.job.Status = status
	return nil
}

// A cancellation names the item it was authorized for, so a job ID from
// another item is not found and nothing is canceled.
func TestAdminTranslationCancelBindsAuthorizedItem(t *testing.T) {
	repo := &adminTranslationRepo{job: translation.Job{ID: 7, ContentID: "owned", Status: jobrunner.StatusPending}}
	service := translation.NewService(t.Context(), translation.Config{}, repo, nil, nil, nil, make(chan struct{}, 1), nil)
	h := NewMetadataAIHandler(service)

	err := h.CancelAdminMetadataTranslation(t.Context(), "other", 7)
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Status != 404 || repo.cancelled != 0 {
		t.Fatalf("cross-item cancellation: %v calls=%d", err, repo.cancelled)
	}
	if err := h.CancelAdminMetadataTranslation(t.Context(), "owned", 7); err != nil || repo.cancelled != 1 || repo.job.Status != jobrunner.StatusCancelled {
		t.Fatalf("cancel: %v %#v", err, repo)
	}
}
