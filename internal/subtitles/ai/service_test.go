package ai

import (
	"context"
	"sync"
	"testing"
	"time"
)

// recordingRepo is a JobRepository that records ResetStaleJobs calls and no-ops
// everything else, so the recovery/reaper behavior can be tested in isolation.
type recordingRepo struct {
	mu         sync.Mutex
	resets     int
	lastBefore time.Time
}

func (r *recordingRepo) InsertJob(context.Context, *Job) error       { return nil }
func (r *recordingRepo) GetJob(context.Context, int64) (*Job, error) { return nil, nil }
func (r *recordingRepo) GetActiveJobByIdempotencyKey(context.Context, string) (*Job, error) {
	return nil, nil
}
func (r *recordingRepo) ListJobsByMediaFile(context.Context, int) ([]Job, error) { return nil, nil }
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

// Recover reaps immediately using a heartbeat cutoff of now-staleJobThreshold,
// not "every active job", so a live worker's jobs survive a peer's startup.
func TestRecoverReapsStaleJobsImmediately(t *testing.T) {
	repo := &recordingRepo{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // stops the reaper goroutine started by Recover

	svc := NewService(ctx, Config{}, repo, nil, nil, nil, nil, nil, "", nil)

	approxNow := time.Now()
	svc.Recover()

	resets, before := repo.snapshot()
	if resets < 1 {
		t.Fatalf("Recover did not reap immediately: resets=%d", resets)
	}
	want := approxNow.Add(-staleJobThreshold)
	if diff := before.Sub(want); diff > 2*time.Second || diff < -2*time.Second {
		t.Errorf("stale cutoff = %v, want ~%v (now-staleJobThreshold)", before, want)
	}
}
