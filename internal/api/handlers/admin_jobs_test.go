package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
)

func assertSafeStorageJobPayload(t *testing.T, payload string) {
	t.Helper()
	for _, private := range []string{"private request", "private source", "private key", "private object", "private endpoint", "private URL"} {
		if strings.Contains(payload, private) {
			t.Fatalf("storage job response leaked %q: %s", private, payload)
		}
	}
	var jobs []adminJobResponse
	if err := json.Unmarshal([]byte(payload), &jobs); err != nil || len(jobs) != 1 {
		t.Fatalf("storage job response=%s err=%v", payload, err)
	}
	job := jobs[0]
	var result struct {
		Phase           string `json:"phase"`
		VerifiedObjects int    `json:"verified_objects"`
		FailureCategory string `json:"failure_category"`
	}
	if err := json.Unmarshal(job.ResultPayload, &result); err != nil || result.Phase != "failed" || result.VerifiedObjects != 7 || result.FailureCategory != "copy_failed" || string(job.RequestPayload) != `{}` || job.Message != "" || job.ErrorMessage != "" {
		t.Fatalf("storage job projection=%+v result=%+v err=%v", job, result, err)
	}
}

// The realtime jobs snapshot projects a storage transition the way every
// other job read does: outcome and counts, never the paths, endpoints, or
// messages that name private storage.
func TestStorageTransitionJobSnapshotOmitsPrivateDetails(t *testing.T) {
	job := &models.AdminJob{
		ID: "transition", JobType: adminjob.JobTypeStorageTransition, Status: adminjob.StatusFailed,
		RequestedAt:    time.Now().UTC(),
		RequestPayload: json.RawMessage(`{"target":"private request"}`),
		ResultPayload:  json.RawMessage(`{"phase":"failed","verified_objects":7,"failure_category":"copy_failed","source_identity":"private source","skipped_keys":["private key"]}`),
		Message:        "private object", ErrorMessage: "private endpoint", PublicURL: "private URL",
	}
	jobs := NewAdminJobsHandler(&fakeAdminJobRepository{job: job}, nil)

	snapshot, err := (&EventsHandler{jobs: jobs}).snapshotForChannel(
		httptest.NewRequest(http.MethodGet, "/api/v2/events/ws", nil), nil, "", evt.ChannelJobs,
	)
	if err != nil {
		t.Fatal(err)
	}
	assertSafeStorageJobPayload(t, string(snapshot))
	if job.ErrorMessage != "private endpoint" {
		t.Fatal("outbound projection mutated the diagnostic job row")
	}
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
