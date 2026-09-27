package ai

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/ai/jobrunner"
)

type cancelRepository struct {
	JobRepository
	job         *Job
	failure     error
	transitions int
}

func (r *cancelRepository) GetJob(context.Context, int64) (*Job, error) {
	return r.job, nil
}

func (r *cancelRepository) FailJob(_ context.Context, id int64, status JobStatus, _ string) error {
	if id != r.job.ID || status != JobStatusCancelled {
		panic("unexpected cancellation transition")
	}
	r.transitions++
	return r.failure
}

func TestCancelPersistsBeforeStoppingLocalWork(t *testing.T) {
	writeFailure := errors.New("database reply lost")
	for _, failure := range []error{nil, writeFailure} {
		name := "confirmed"
		if failure != nil {
			name = "uncertain"
		}
		t.Run(name, func(t *testing.T) {
			repo := &cancelRepository{JobRepository: &recordingRepo{}, job: &Job{ID: 42, Status: JobStatusRunning}, failure: failure}
			runner := jobrunner.New(t.Context(), nil, repo, "subtitle test", nil)
			started := make(chan struct{})
			stopped := make(chan int, 1)
			runner.Dispatch(42, func(ctx context.Context) {
				close(started)
				<-ctx.Done()
				// Context cancellation synchronizes the preceding DB attempt.
				stopped <- repo.transitions
			}, nil)
			<-started
			svc := &Service{repo: repo, runner: runner}
			if err := svc.Cancel(t.Context(), 42); !errors.Is(err, failure) {
				t.Fatalf("Cancel error = %v, want %v", err, failure)
			}
			if transitions := <-stopped; transitions != 1 {
				t.Fatalf("local worker stopped after %d terminal transitions, want 1", transitions)
			}
		})
	}
}

func TestCancelWithoutLocalWorkAndTerminalReplay(t *testing.T) {
	for _, status := range []JobStatus{JobStatusPending, JobStatusRunning, JobStatusCompleted, JobStatusCancelled, JobStatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			repo := &cancelRepository{JobRepository: &recordingRepo{}, job: &Job{ID: 42, Status: status}}
			svc := &Service{repo: repo, runner: jobrunner.New(t.Context(), nil, repo, "subtitle test", nil)}
			if err := svc.Cancel(t.Context(), 42); err != nil {
				t.Fatal(err)
			}
			want := 1
			if status.Terminal() {
				want = 0
			}
			if repo.transitions != want {
				t.Fatalf("terminal transitions = %d, want %d", repo.transitions, want)
			}
		})
	}
}

func TestCancelMissingJob(t *testing.T) {
	repo := &cancelRepository{}
	svc := &Service{repo: repo}
	if err := svc.Cancel(t.Context(), 42); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("Cancel error = %v, want job not found", err)
	}
}
