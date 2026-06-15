package ai

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/ai/jobrunner"
	"github.com/Silo-Server/silo-server/internal/models"
)

// recordingRepo is a JobRepository that records ResetStaleJobs calls and
// insert/quota-count activity and no-ops everything else, so service behavior
// can be tested in isolation.
type recordingRepo struct {
	mu         sync.Mutex
	resets     int
	lastBefore time.Time
	inserts    int
	quotaUsed  int // returned by CountTranscribeJobsByUserSince
	lastJob    Job
}

// InsertJob mirrors the Postgres repo's atomic quota guard: a non-nil quota
// is checked against quotaUsed before the insert, so the tests exercise the
// same enforcement path production uses.
func (r *recordingRepo) InsertJob(_ context.Context, job *Job, quota *JobQuota) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if quota != nil && r.quotaUsed >= quota.Limit {
		return &QuotaExceededError{Limit: quota.Limit, Used: r.quotaUsed, Period: quota.Period}
	}
	r.inserts++
	job.ID = int64(r.inserts)
	r.lastJob = *job
	return nil
}
func (r *recordingRepo) GetJob(context.Context, int64) (*Job, error) { return nil, nil }
func (r *recordingRepo) GetActiveJobByIdempotencyKey(context.Context, string) (*Job, error) {
	return nil, nil
}
func (r *recordingRepo) ListJobsByMediaFile(context.Context, int) ([]Job, error) { return nil, nil }
func (r *recordingRepo) CountTranscribeJobsByUserSince(context.Context, int, time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.quotaUsed, nil
}
func (r *recordingRepo) UpdateProgress(context.Context, int64, JobStatus, float64, string) error {
	return nil
}
func (r *recordingRepo) CompleteJob(context.Context, int64, int) error           { return nil }
func (r *recordingRepo) FailJob(context.Context, int64, JobStatus, string) error { return nil }
func (r *recordingRepo) Heartbeat(context.Context, int64) error                  { return nil }

func (r *recordingRepo) ResetStaleJobs(_ context.Context, before time.Time, _ string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resets++
	r.lastBefore = before
	return 0, nil
}

func (r *recordingRepo) snapshot() (int, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.resets, r.lastBefore
}

// stubTranscriber satisfies Transcriber so TranscribeEnabled() is true in
// tests; it returns no cues.
type stubTranscriber struct{}

func (stubTranscriber) Transcribe(context.Context, TranscribeJobRequest,
	func([]SubtitleCue, int, int)) ([]SubtitleCue, string, error) {
	return nil, "", nil
}

// nilFileResolver reports every media file as missing, so dispatched test jobs
// fail fast instead of touching the filesystem.
type nilFileResolver struct{}

func (nilFileResolver) GetByID(context.Context, int) (*models.MediaFile, error) { return nil, nil }

// newQuotaTestService builds a transcribe-ready service whose repo reports
// `used` transcription jobs in the current quota window.
func newQuotaTestService(t *testing.T, used, limit int) (*Service, *recordingRepo) {
	t.Helper()
	repo := &recordingRepo{quotaUsed: used}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cfg := Config{
		Configured:            true,
		TranscribeEnabled:     true,
		ASRModel:              "whisper-1",
		TranscribeQuotaJobs:   limit,
		TranscribeQuotaPeriod: QuotaPeriodDay,
	}
	svc := NewService(ctx, cfg, repo, nil, stubTranscriber{}, nil, nil, nilFileResolver{}, nil, "", nil, nil)
	return svc, repo
}

func transcribeRequest(userID int, exempt bool) JobRequest {
	return JobRequest{
		MediaFileID: 1,
		Kind:        JobKindTranscribe,
		SourceIndex: -1,
		RequestedBy: &userID,
		QuotaExempt: exempt,
	}
}

func TestEnqueueRejectsTranscribeOverQuota(t *testing.T) {
	svc, repo := newQuotaTestService(t, 3, 3)

	_, err := svc.Enqueue(context.Background(), transcribeRequest(42, false))
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("Enqueue error = %v, want ErrQuotaExceeded", err)
	}
	var qe *QuotaExceededError
	if !errors.As(err, &qe) {
		t.Fatalf("error %v does not unwrap to *QuotaExceededError", err)
	}
	if qe.Limit != 3 || qe.Used != 3 || qe.Period != QuotaPeriodDay {
		t.Errorf("quota error = %+v, want limit=3 used=3 period=day", qe)
	}
	if repo.inserts != 0 {
		t.Errorf("job was inserted despite exceeded quota (inserts=%d)", repo.inserts)
	}
}

func TestEnqueueAllowsTranscribeUnderQuota(t *testing.T) {
	svc, repo := newQuotaTestService(t, 2, 3)

	if _, err := svc.Enqueue(context.Background(), transcribeRequest(42, false)); err != nil {
		t.Fatalf("Enqueue under quota failed: %v", err)
	}
	if repo.inserts != 1 {
		t.Errorf("inserts = %d, want 1", repo.inserts)
	}
}

func TestEnqueueQuotaExemptions(t *testing.T) {
	t.Run("exempt", func(t *testing.T) {
		svc, repo := newQuotaTestService(t, 10, 1)
		if _, err := svc.Enqueue(context.Background(), transcribeRequest(42, true)); err != nil {
			t.Fatalf("exempt Enqueue blocked by quota: %v", err)
		}
		if repo.inserts != 1 {
			t.Errorf("inserts = %d, want 1", repo.inserts)
		}
	})
	t.Run("unlimited", func(t *testing.T) {
		svc, repo := newQuotaTestService(t, 10, 0)
		if _, err := svc.Enqueue(context.Background(), transcribeRequest(42, false)); err != nil {
			t.Fatalf("Enqueue with quota disabled failed: %v", err)
		}
		if repo.inserts != 1 {
			t.Errorf("inserts = %d, want 1", repo.inserts)
		}
	})
}

func TestEnqueueNormalizesSourceLanguageHint(t *testing.T) {
	svc, repo := newQuotaTestService(t, 0, 0)

	_, err := svc.Enqueue(context.Background(), JobRequest{
		MediaFileID:    1,
		Kind:           JobKindTranscribe,
		SourceIndex:    -1,
		SourceLanguage: "eng",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if repo.lastJob.SourceLanguage != "en" {
		t.Errorf("source language = %q, want en", repo.lastJob.SourceLanguage)
	}
}

func TestTranscribeQuotaStatus(t *testing.T) {
	svc, _ := newQuotaTestService(t, 2, 5)

	got, err := svc.TranscribeQuota(context.Background(), 42, false)
	if err != nil {
		t.Fatalf("TranscribeQuota: %v", err)
	}
	want := QuotaStatus{Limited: true, Limit: 5, Used: 2, Remaining: 3, Period: QuotaPeriodDay}
	if got != want {
		t.Errorf("TranscribeQuota = %+v, want %+v", got, want)
	}

	exempt, err := svc.TranscribeQuota(context.Background(), 42, true)
	if err != nil {
		t.Fatalf("TranscribeQuota (exempt): %v", err)
	}
	if exempt.Limited {
		t.Errorf("exempt quota = %+v, want unlimited", exempt)
	}
}

// Recover reaps immediately using a heartbeat cutoff of now-staleJobThreshold,
// not "every active job", so a live worker's jobs survive a peer's startup.
func TestRecoverReapsStaleJobsImmediately(t *testing.T) {
	repo := &recordingRepo{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // stops the reaper goroutine started by Recover

	svc := NewService(ctx, Config{}, repo, nil, nil, nil, nil, nil, nil, "", nil, nil)

	approxNow := time.Now()
	svc.Recover()

	resets, before := repo.snapshot()
	if resets < 1 {
		t.Fatalf("Recover did not reap immediately: resets=%d", resets)
	}
	want := approxNow.Add(-jobrunner.StaleJobThreshold)
	if diff := before.Sub(want); diff > 2*time.Second || diff < -2*time.Second {
		t.Errorf("stale cutoff = %v, want ~%v (now-staleJobThreshold)", before, want)
	}
}
