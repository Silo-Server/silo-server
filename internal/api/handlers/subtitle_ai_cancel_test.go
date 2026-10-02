package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles/ai"
)

type subtitleCancelRepo struct {
	ai.JobRepository
	job     *ai.Job
	failure error
	writes  int
}

func (r *subtitleCancelRepo) GetJob(context.Context, int64) (*ai.Job, error) { return r.job, nil }
func (r *subtitleCancelRepo) FailJob(_ context.Context, id int64, status ai.JobStatus, _ string) error {
	if id != r.job.ID || status != ai.JobStatusCancelled {
		panic("wrong cancellation identity")
	}
	r.writes++
	return r.failure
}
func TestSubtitleAICancelAuthorizesBeforeTransition(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		repo := &subtitleCancelRepo{job: &ai.Job{ID: 9007199254740993, MediaFileID: 42, RequestedBy: new(99), Status: ai.JobStatusRunning}}
		svc := ai.NewService(t.Context(), ai.Config{}, repo, nil, nil, nil, nil, nil, nil, "", nil, nil)
		h := NewSubtitleAIHandler(svc)
		var denied error
		if hidden {
			denied = catalog.ErrItemNotFound
		}
		h.FileAuthorizer = &MediaFileAuthorizer{FileResolver: stubMediaFileResolver{file: &models.MediaFile{ID: 42, ContentID: "movie"}}, ItemAccess: stubItemAccessChecker{err: denied}}
		err := h.CancelSubtitleAIJob(t.Context(), catalog.AccessFilter{UserID: 7}, repo.job.ID)
		if hidden {
			if e, ok := errors.AsType[*APIError](err); !ok || e.Status != 404 || repo.writes != 0 {
				t.Fatalf("hidden=%v writes=%d", err, repo.writes)
			}
			continue
		}
		if err != nil || repo.writes != 1 {
			t.Fatalf("accessible shared-file cancellation=%v writes=%d", err, repo.writes)
		}
		repo.job.Status = ai.JobStatusCompleted
		if err := h.CancelSubtitleAIJob(t.Context(), catalog.AccessFilter{UserID: 7}, repo.job.ID); err != nil || repo.writes != 1 {
			t.Fatal("terminal replay changed state", err)
		}
		repo.job.Status = ai.JobStatusRunning
		repo.failure = errors.New("PRIVATE SQL reply")
		if e, ok := errors.AsType[*APIError](h.CancelSubtitleAIJob(t.Context(), catalog.AccessFilter{UserID: 7}, repo.job.ID)); !ok || e.Status != 500 {
			t.Fatal("uncertain cancellation acknowledged")
		}
	}
}
