package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

func TestCanReadAdminJob_AdminCanReadAnyJob(t *testing.T) {
	claims := &auth.Claims{UserID: 1, Role: "admin"}
	job := &models.AdminJob{CreatedByUserID: 2, JobType: adminjob.JobTypeCatalogExport}
	if !canReadAdminJob(claims, job) {
		t.Fatal("admin should be allowed to read any job")
	}
}

func TestCanReadAdminJob_CreatorCanReadOwnItemRefreshJob(t *testing.T) {
	claims := &auth.Claims{UserID: 2, Role: "user"}
	job := &models.AdminJob{CreatedByUserID: 2, JobType: adminjob.JobTypeItemRefresh}
	if !canReadAdminJob(claims, job) {
		t.Fatal("creator should be allowed to read own item refresh job")
	}
}

func TestCanReadAdminJob_CreatorCannotReadOwnNonItemRefreshJob(t *testing.T) {
	claims := &auth.Claims{UserID: 2, Role: "user"}
	job := &models.AdminJob{CreatedByUserID: 2, JobType: adminjob.JobTypeCatalogExport}
	if canReadAdminJob(claims, job) {
		t.Fatal("non-admin should not read non-item-refresh jobs")
	}
}

func TestCanReadAdminJob_OtherUserCannotReadItemRefreshJob(t *testing.T) {
	claims := &auth.Claims{UserID: 3, Role: "user"}
	job := &models.AdminJob{CreatedByUserID: 2, JobType: adminjob.JobTypeItemRefresh}
	if canReadAdminJob(claims, job) {
		t.Fatal("non-admin should not read another user's item refresh job")
	}
}

func TestAdminJobToResponseForClaims_NonAdminSanitizesItemRefreshPayloads(t *testing.T) {
	claims := &auth.Claims{UserID: 2, Role: "user"}
	job := &models.AdminJob{
		JobType:         adminjob.JobTypeItemRefresh,
		CreatedByUserID: 2,
		RequestPayload: json.RawMessage(
			`{"requested_content_id":"item-1","scan_path":"/srv/media/private/movie"}`,
		),
		ResultPayload: json.RawMessage(
			`{"requested_content_id":"item-1","detail_content_id":"item-2","scan_path":"/srv/media/private/movie","scan_result":{"New":1,"RootObservations":[{"RootPath":"/srv/media/private","SampleFilePath":"/srv/media/private/movie.mkv"}]}}`,
		),
		ErrorMessage: "scan scope: stat /srv/media/private/movie: permission denied",
		PublicURL:    "https://example.test/public",
	}

	resp := adminJobToResponseForClaims(nil, job, nil, claims)

	if string(resp.RequestPayload) != `{}` {
		t.Fatalf("RequestPayload = %s, want sanitized empty object", resp.RequestPayload)
	}
	if resp.PublicURL != "" || resp.DownloadURL != "" || resp.DownloadExpiresAt != nil {
		t.Fatalf("expected non-admin URLs to be stripped, got public=%q download=%q", resp.PublicURL, resp.DownloadURL)
	}
	if bytes.Contains(resp.ResultPayload, []byte("/srv/media")) ||
		bytes.Contains(resp.ResultPayload, []byte("scan_path")) ||
		bytes.Contains(resp.ResultPayload, []byte("RootObservations")) ||
		bytes.Contains(resp.ResultPayload, []byte("SampleFilePath")) {
		t.Fatalf("ResultPayload leaked sensitive data: %s", resp.ResultPayload)
	}
	if !bytes.Contains(resp.ResultPayload, []byte("requested_content_id")) ||
		!bytes.Contains(resp.ResultPayload, []byte("detail_content_id")) {
		t.Fatalf("ResultPayload = %s, want safe item refresh summary fields", resp.ResultPayload)
	}
	if resp.ErrorMessage != "" {
		t.Fatalf("ErrorMessage = %q, want stripped for non-admin", resp.ErrorMessage)
	}
}

func TestAdminJobsHandleCancel_RunningRegistryMissDoesNotUpdateProgress(t *testing.T) {
	repo := &fakeAdminJobRepository{
		job: &models.AdminJob{
			ID:              "job-1",
			JobType:         adminjob.JobTypeLibraryRefresh,
			Status:          adminjob.StatusRunning,
			CreatedByUserID: 1,
			Message:         "Running",
			RequestedAt:     time.Now().UTC(),
		},
	}
	handler := NewAdminJobsHandler(repo, nil)

	rec := httptest.NewRecorder()
	handler.HandleCancel(rec, adminJobCancelRequest("job-1"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if repo.updateProgressCalls != 0 {
		t.Fatalf("UpdateProgress called %d times, want 0", repo.updateProgressCalls)
	}
}

func TestAdminJobsHandleCancel_QueuedPublishesCancelledEvent(t *testing.T) {
	repo := &fakeAdminJobRepository{
		job: &models.AdminJob{
			ID:              "job-1",
			JobType:         adminjob.JobTypeLibraryRefresh,
			Status:          adminjob.StatusQueued,
			CreatedByUserID: 1,
			Message:         "Queued",
			RequestedAt:     time.Now().UTC(),
		},
	}
	hub := notifications.NewHub("test", &cache.NoopEventBus{})
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	handler := NewAdminJobsHandler(repo, nil)
	handler.RealtimeHub = hub

	rec := httptest.NewRecorder()
	handler.HandleCancel(rec, adminJobCancelRequest("job-1"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	select {
	case event := <-events:
		if event.Type != notifications.TypeJobCancelled {
			t.Fatalf("event.Type = %q, want %q", event.Type, notifications.TypeJobCancelled)
		}
		if event.Job == nil || event.Job.ID != "job-1" {
			t.Fatalf("event.Job = %#v, want job-1", event.Job)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for job.cancelled event")
	}
}

func TestAdminJobsHandleCancel_QueuedSnapshotFallsBackToRunningCancellation(t *testing.T) {
	repo := &fakeAdminJobRepository{
		job: &models.AdminJob{
			ID:              "job-1",
			JobType:         adminjob.JobTypeLibraryRefresh,
			Status:          adminjob.StatusQueued,
			CreatedByUserID: 1,
			Message:         "Queued",
			RequestedAt:     time.Now().UTC(),
			ProgressCurrent: 3,
			ProgressTotal:   10,
		},
		promoteToRunningOnCancelQueued: true,
	}
	cancelled := false
	registry := adminjob.NewCancelRegistry()
	unregister := registry.Register("job-1", func() {
		cancelled = true
	})
	defer unregister()

	handler := NewAdminJobsHandler(repo, nil)
	handler.CancelRegistry = registry

	rec := httptest.NewRecorder()
	handler.HandleCancel(rec, adminJobCancelRequest("job-1"))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	if !cancelled {
		t.Fatal("expected running job cancel registry to be invoked")
	}
	if repo.cancelQueuedCalls != 1 {
		t.Fatalf("CancelQueued called %d times, want 1", repo.cancelQueuedCalls)
	}
	if repo.cancelCalls != 0 {
		t.Fatalf("Cancel called %d times, want 0", repo.cancelCalls)
	}
	if repo.updateProgressCalls != 1 {
		t.Fatalf("UpdateProgress called %d times, want 1", repo.updateProgressCalls)
	}
	if repo.job.Status != adminjob.StatusRunning {
		t.Fatalf("job status = %q, want %q", repo.job.Status, adminjob.StatusRunning)
	}
	if repo.job.Message != "Cancellation requested" {
		t.Fatalf("job message = %q, want cancellation request", repo.job.Message)
	}
}

func adminJobCancelRequest(id string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/admin/jobs/"+id+"/cancel", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

type fakeAdminJobRepository struct {
	job                            *models.AdminJob
	updateProgressCalls            int
	cancelCalls                    int
	cancelQueuedCalls              int
	promoteToRunningOnCancelQueued bool
}

func (r *fakeAdminJobRepository) List(context.Context, adminjob.ListJobsOptions) ([]*models.AdminJob, error) {
	if r.job == nil {
		return nil, nil
	}
	return []*models.AdminJob{r.job}, nil
}

func (r *fakeAdminJobRepository) GetByID(_ context.Context, id string) (*models.AdminJob, error) {
	if r.job == nil || r.job.ID != id {
		return nil, adminjob.ErrJobNotFound
	}
	cp := *r.job
	return &cp, nil
}

func (r *fakeAdminJobRepository) Cancel(_ context.Context, id, message string, _ time.Time) (*models.AdminJob, error) {
	r.cancelCalls++
	if r.job == nil || r.job.ID != id {
		return nil, adminjob.ErrJobNotFound
	}
	if r.job.Status != adminjob.StatusQueued && r.job.Status != adminjob.StatusRunning {
		return nil, adminjob.ErrJobNotCancellable
	}
	cp := *r.job
	cp.Status = adminjob.StatusCancelled
	cp.Message = message
	r.job = &cp
	return &cp, nil
}

func (r *fakeAdminJobRepository) CancelQueued(_ context.Context, id, message string, _ time.Time) (*models.AdminJob, error) {
	r.cancelQueuedCalls++
	if r.job == nil || r.job.ID != id {
		return nil, adminjob.ErrJobNotFound
	}
	if r.promoteToRunningOnCancelQueued {
		cp := *r.job
		cp.Status = adminjob.StatusRunning
		r.job = &cp
		return nil, adminjob.ErrJobNotCancellable
	}
	if r.job.Status != adminjob.StatusQueued {
		return nil, adminjob.ErrJobNotCancellable
	}
	cp := *r.job
	cp.Status = adminjob.StatusCancelled
	cp.Message = message
	r.job = &cp
	return &cp, nil
}

func (r *fakeAdminJobRepository) UpdateProgress(_ context.Context, id string, current, total int, message string) error {
	r.updateProgressCalls++
	if r.job == nil || r.job.ID != id {
		return adminjob.ErrJobNotFound
	}
	if message == "" {
		return errors.New("message is required")
	}
	r.job.ProgressCurrent = current
	r.job.ProgressTotal = total
	r.job.Message = message
	return nil
}
