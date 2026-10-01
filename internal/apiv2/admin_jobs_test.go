package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeLibraryJobs struct{ job *models.AdminJob }

func (f *fakeLibraryJobs) GetByID(_ context.Context, id string) (*models.AdminJob, error) {
	if f.job == nil || f.job.ID != id {
		return nil, adminjob.ErrJobNotFound
	}
	return f.job, nil
}
func (f *fakeLibraryJobs) RequestCancellation(ctx context.Context, id string) (*models.AdminJob, error) {
	job, err := f.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if job.Status == adminjob.StatusCancelled {
		return job, nil
	}
	if job.JobType != adminjob.JobTypeLibraryRefresh || job.Status == adminjob.StatusCompleted || job.Status == adminjob.StatusFailed {
		return nil, adminjob.ErrJobNotCancellable
	}
	job.CancelRequested = true
	return job, nil
}
func TestLibraryJobAcceptanceMonitorAndSafePolling(t *testing.T) {
	deps, _ := libraryDeps(t)
	job := &models.AdminJob{ID: "job-1", JobType: adminjob.JobTypeDeleteLibrary, Status: adminjob.StatusQueued, RequestedAt: fixedTime(), RequestPayload: json.RawMessage(`{"secret":"private-path"}`), Message: "private-path", ErrorMessage: "private-path"}
	jobs := &fakeLibraryJobs{job: job}
	deps.LibraryJobs = jobs
	h := newTestHandler(t, deps)
	accepted := do(t, h, http.MethodDelete, "/api/v2/libraries/1", "", bearer(adminToken))
	if accepted.Code != 202 || accepted.Header().Get("Retry-After") == "" {
		t.Fatalf("acceptance %d %s", accepted.Code, accepted.Body)
	}
	location := accepted.Header().Get("Location")
	poll := do(t, h, http.MethodGet, location, "", bearer(adminToken))
	if poll.Code != 200 || poll.Header().Get("ETag") == "" || poll.Header().Get("Retry-After") == "" {
		t.Fatalf("poll %d %s", poll.Code, poll.Body)
	}
	if accepted.Body.String() != poll.Body.String() {
		t.Fatalf("acceptance and monitor differ: %s / %s", accepted.Body, poll.Body)
	}
	headers := bearer(adminToken)
	headers["If-None-Match"] = poll.Header().Get("ETag")
	conditional := do(t, h, http.MethodGet, location, "", headers)
	if conditional.Code != 304 || conditional.Body.Len() != 0 {
		t.Fatalf("conditional %d %s", conditional.Code, conditional.Body)
	}
	hidden := bearer(memberToken)
	hidden["If-None-Match"] = poll.Header().Get("ETag")
	requireProblem(t, do(t, h, http.MethodGet, location, "", hidden), TypeNotFound)
	job.Status = adminjob.StatusFailed
	failure := do(t, h, http.MethodGet, location, "", headers)
	if failure.Code != 200 || failure.Header().Get("Retry-After") != "" {
		t.Fatalf("failure poll %d %s", failure.Code, failure.Body)
	}
	if strings.Contains(failure.Body.String(), "private-path") || strings.Contains(failure.Body.String(), "request_payload") {
		t.Fatal("private job data leaked")
	}
	var body AdminJob
	decodeJSON(t, failure.Body, &body)
	if body.Failure == nil || !body.Terminal || body.State != "failed" {
		t.Fatalf("failure shape %+v", body)
	}
	job.Status = adminjob.StatusCompleted
	job.ResultPayload = json.RawMessage(`{"library_id":1,"deleted_media_files":3,"private":"private-path"}`)
	success := do(t, h, http.MethodGet, location, "", bearer(adminToken))
	decodeJSON(t, success.Body, &body)
	if success.Code != 200 || body.DeletionResult == nil || body.DeletionResult.DeletedMediaFiles != 3 || strings.Contains(success.Body.String(), "private-path") {
		t.Fatalf("result %s", success.Body)
	}
}
func TestLibraryJobCancellationContract(t *testing.T) {
	deps, _ := libraryDeps(t)
	job := &models.AdminJob{ID: "refresh", JobType: adminjob.JobTypeLibraryRefresh, Status: adminjob.StatusRunning, RequestedAt: fixedTime()}
	deps.LibraryJobs = &fakeLibraryJobs{job: job}
	h := newTestHandler(t, deps)
	path := "/api/v2/library-jobs/refresh/cancel"
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/library-jobs/missing/cancel", "", with(bearer(adminToken), "X-Profile-Id", "p-primary")), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPost, path, "", bearer(memberToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodPost, path, "", with(bearer(adminToken), "X-Profile-Id", "p-owner")), TypePermissionDenied)
	for range 2 {
		rec := do(t, h, http.MethodPost, path, "", bearer(adminToken))
		var body AdminJob
		decodeJSON(t, rec.Body, &body)
		if rec.Code != 202 || body.State != "canceling" || body.Terminal || rec.Header().Get("Location") != "/api/v2/library-jobs/refresh" {
			t.Fatalf("cancel %d %s", rec.Code, rec.Body)
		}
	}
	job.Status = adminjob.StatusCancelled
	rec := do(t, h, http.MethodPost, path, "", bearer(adminToken))
	if rec.Code != 200 {
		t.Fatalf("repeat canceled %d %s", rec.Code, rec.Body)
	}
	for _, state := range []string{adminjob.StatusCompleted, adminjob.StatusFailed} {
		job.Status = state
		requireProblem(t, do(t, h, http.MethodPost, path, "", bearer(adminToken)), TypeJobNotCancelable)
	}
}
