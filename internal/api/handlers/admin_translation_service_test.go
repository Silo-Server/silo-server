package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/ai/jobrunner"
	"github.com/Silo-Server/silo-server/internal/metadata/translation"
	"github.com/go-chi/chi/v5"
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
func TestAdminTranslationCancelBindsAuthorizedItem(t *testing.T) {
	repo := &adminTranslationRepo{job: translation.Job{ID: 7, ContentID: "owned", Status: jobrunner.StatusPending}}
	service := translation.NewService(t.Context(), translation.Config{}, repo, nil, nil, nil, make(chan struct{}, 1), nil)
	h := NewMetadataAIHandler(service)
	err := h.CancelAdminMetadataTranslation(t.Context(), "other", 7)
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != 404 || repo.cancelled != 0 {
		t.Fatalf("cross-item cancellation: %v calls=%d", err, repo.cancelled)
	}
	router := chi.NewRouter()
	router.Post("/items/{id}/jobs/{job_id}/cancel", h.HandleCancelJob)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/items/other/jobs/7/cancel", nil))
	if rec.Code != 404 || repo.cancelled != 0 {
		t.Fatalf("v1 cross-item: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/items/owned/jobs/7/cancel", nil))
	if rec.Code != 204 || rec.Body.Len() != 0 || repo.cancelled != 1 || repo.job.Status != jobrunner.StatusCancelled {
		t.Fatalf("cancel: %d %s %#v", rec.Code, rec.Body, repo)
	}
}
