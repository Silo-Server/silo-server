package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeArtworkStorageAccountant struct {
	result metadata.ArtworkStorageAccounting
	called int
}

func (f *fakeArtworkStorageAccountant) Accounting(context.Context) (metadata.ArtworkStorageAccounting, error) {
	f.called++
	return f.result, nil
}

type fakeArtworkStorageJobs struct {
	input adminjob.CreateJobInput
}

type fakeArtworkStorageRebuilder struct {
	fakeArtworkStorageAccountant
	result metadata.ArtworkStorageAccounting
	err    error
	called int
}

func (f *fakeArtworkStorageRebuilder) RebuildEmpty(context.Context) (metadata.ArtworkStorageAccounting, error) {
	f.called++
	return f.result, f.err
}

func (f *fakeArtworkStorageJobs) Create(_ context.Context, input adminjob.CreateJobInput) (*models.AdminJob, error) {
	f.input = input
	return &models.AdminJob{
		ID: "job-1", JobType: input.JobType, Status: adminjob.StatusQueued,
		DryRun: input.DryRun, RequestedAt: time.Now(),
	}, nil
}

func TestAdminArtworkRebuildReturnsNewState(t *testing.T) {
	rebuilder := &fakeArtworkStorageRebuilder{result: metadata.ArtworkStorageAccounting{
		Backend: "local", StoreHealth: "empty_rebuilding",
	}}
	handler := NewAdminArtworkStorageHandler(rebuilder, &fakeArtworkStorageJobs{})
	recorder := httptest.NewRecorder()
	handler.HandleRebuild(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/admin/artwork/rebuild", nil))
	if recorder.Code != http.StatusOK || rebuilder.called != 1 || !strings.Contains(recorder.Body.String(), `"store_health":"empty_rebuilding"`) {
		t.Fatalf("rebuild response = %d %s, calls = %d", recorder.Code, recorder.Body.String(), rebuilder.called)
	}
}

func TestAdminArtworkRebuildRejectsUnsupportedBackend(t *testing.T) {
	rebuilder := &fakeArtworkStorageRebuilder{err: artworkstore.ErrRebuildUnsupported}
	handler := NewAdminArtworkStorageHandler(rebuilder, &fakeArtworkStorageJobs{})
	recorder := httptest.NewRecorder()
	handler.HandleRebuild(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/admin/artwork/rebuild", nil))
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "unsupported_backend") {
		t.Fatalf("rebuild response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestAdminArtworkStorageEndpointsReadSnapshotAndQueueAsyncJobs(t *testing.T) {
	accountant := &fakeArtworkStorageAccountant{result: metadata.ArtworkStorageAccounting{
		Backend: "local", Complete: false, KnownBytes: 123,
	}}
	jobs := &fakeArtworkStorageJobs{}
	handler := NewAdminArtworkStorageHandler(accountant, jobs)

	recorder := httptest.NewRecorder()
	handler.HandleStorage(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/artwork/storage", nil))
	if recorder.Code != http.StatusOK || accountant.called != 1 || !strings.Contains(recorder.Body.String(), `"known_bytes":123`) {
		t.Fatalf("storage response = %d %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	handler.HandleRefresh(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/admin/artwork/storage/refresh", nil))
	if recorder.Code != http.StatusAccepted || jobs.input.JobType != adminjob.JobTypeArtworkStorageRefresh || !jobs.input.ResumeCheckpoint {
		t.Fatalf("refresh response = %d %s, job = %#v", recorder.Code, recorder.Body.String(), jobs.input)
	}
	recorder = httptest.NewRecorder()
	handler.HandleImport(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/admin/artwork/storage/import", nil))
	if recorder.Code != http.StatusAccepted || jobs.input.JobType != adminjob.JobTypeArtworkStorageImport || !jobs.input.ResumeCheckpoint {
		t.Fatalf("import response = %d %s, job = %#v", recorder.Code, recorder.Body.String(), jobs.input)
	}

	recorder = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/artwork/purge", strings.NewReader(
		`{"scope":{"library_id":12},"mode":"safe_materialized","dry_run":true}`,
	))
	handler.HandlePurge(recorder, request)
	if recorder.Code != http.StatusAccepted || jobs.input.JobType != adminjob.JobTypeArtworkPurge || !jobs.input.DryRun || !jobs.input.ResumeCheckpoint {
		t.Fatalf("purge response = %d %s, job = %#v", recorder.Code, recorder.Body.String(), jobs.input)
	}
}

func TestAdminArtworkPurgeRejectsAmbiguousScope(t *testing.T) {
	handler := NewAdminArtworkStorageHandler(&fakeArtworkStorageAccountant{}, &fakeArtworkStorageJobs{})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/artwork/purge", strings.NewReader(
		`{"scope":{"library_id":12,"server":true},"mode":"safe_materialized","dry_run":true}`,
	))
	handler.HandlePurge(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestAdminArtworkPurgeNormalizesEdgeOnlyBeforeQueueing(t *testing.T) {
	jobs := &fakeArtworkStorageJobs{}
	handler := NewAdminArtworkStorageHandler(&fakeArtworkStorageAccountant{}, jobs)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/artwork/purge", strings.NewReader(
		`{"scope":{"server":true},"mode":"Edge_Only","dry_run":false}`,
	))
	handler.HandlePurge(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	queued, ok := jobs.input.RequestPayload.(adminjob.ArtworkPurgeRequest)
	if !ok {
		t.Fatalf("queued payload type = %T", jobs.input.RequestPayload)
	}
	if queued.Mode != adminjob.ArtworkPurgeModeEdgeOnly {
		t.Fatalf("queued mode = %q, want %q", queued.Mode, adminjob.ArtworkPurgeModeEdgeOnly)
	}
}
