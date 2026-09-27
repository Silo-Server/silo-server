package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database/pglock"
	"github.com/Silo-Server/silo-server/internal/intromarkers"
)

type fakeMarkerAnalysisRunner struct {
	runs    int
	summary intromarkers.RunSummary
	err     error
	// block waits for ctx cancellation and returns its error.
	block bool
	// preflightErr is what this server's ffmpeg lacks for fingerprinting.
	preflightErr error
}

func (f *fakeMarkerAnalysisRunner) Preflight(context.Context) error { return f.preflightErr }

func (f *fakeMarkerAnalysisRunner) Run(ctx context.Context, _ intromarkers.ProgressFunc) (intromarkers.RunSummary, error) {
	f.runs++
	if f.block {
		<-ctx.Done()
		return f.summary, ctx.Err()
	}
	return f.summary, f.err
}

func newTestDetectMarkersTask(runner markerAnalysisRunner, lock clusterLock) *DetectIntroMarkersTask {
	task := NewDetectIntroMarkersTask(nil, nil, nil)
	task.analyzer = runner
	task.lock = lock
	return task
}

func assertDetectMarkersSkipped(t *testing.T, progress *fakeProgress) {
	t.Helper()
	var got detectMarkersSkipped
	if err := json.Unmarshal(progress.resultData, &got); err != nil {
		t.Fatalf("result data %q: %v", progress.resultData, err)
	}
	if !got.Skipped || got.Reason != detectMarkersRunningElsewhere {
		t.Fatalf("result data = %+v, want skipped with reason", got)
	}
}

func TestDetectIntroMarkersSkipsWhenAnotherServerHoldsLock(t *testing.T) {
	runner := &fakeMarkerAnalysisRunner{}
	progress := &fakeProgress{}
	if err := newTestDetectMarkersTask(runner, &fakeClusterLock{}).Execute(t.Context(), progress); err != nil {
		t.Fatalf("Execute = %v, want nil", err)
	}
	if runner.runs != 0 {
		t.Fatalf("analyzer runs = %d, want 0", runner.runs)
	}
	assertDetectMarkersSkipped(t, progress)
}

func TestDetectIntroMarkersWithoutChromaprintRunsWithoutLock(t *testing.T) {
	// The lock is held elsewhere; a server that cannot fingerprint must neither
	// take it nor skip its chapter-only pass because of it.
	runner := &fakeMarkerAnalysisRunner{preflightErr: errors.New("ffmpeg lacks chromaprint")}
	lock := &fakeClusterLock{err: errors.New("lock must not be consulted")}
	if err := newTestDetectMarkersTask(runner, lock).Execute(t.Context(), &fakeProgress{}); err != nil {
		t.Fatalf("Execute = %v, want nil", err)
	}
	if runner.runs != 1 || lock.released != 0 {
		t.Fatalf("runs=%d released=%d, want one unlocked run", runner.runs, lock.released)
	}
}

func TestDetectIntroMarkersPreflightCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	runner := &fakeMarkerAnalysisRunner{preflightErr: context.Canceled}
	err := newTestDetectMarkersTask(runner, &fakeClusterLock{acquired: true}).Execute(ctx, &fakeProgress{})
	if !errors.Is(err, context.Canceled) || runner.runs != 0 {
		t.Fatalf("Execute error=%v runs=%d, want context.Canceled and no run", err, runner.runs)
	}
}

func TestDetectIntroMarkersLockErrorFailsRun(t *testing.T) {
	runner := &fakeMarkerAnalysisRunner{}
	lockErr := errors.New("database unavailable")
	err := newTestDetectMarkersTask(runner, &fakeClusterLock{err: lockErr}).Execute(t.Context(), &fakeProgress{})
	if !errors.Is(err, lockErr) || runner.runs != 0 {
		t.Fatalf("Execute error=%v runs=%d, want lock error and no run", err, runner.runs)
	}
}

func TestDetectIntroMarkersReleasesLock(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		lock := &fakeClusterLock{acquired: true}
		runner := &fakeMarkerAnalysisRunner{summary: intromarkers.RunSummary{LibrariesScanned: 2}}
		progress := &fakeProgress{}
		if err := newTestDetectMarkersTask(runner, lock).Execute(t.Context(), progress); err != nil {
			t.Fatalf("Execute = %v", err)
		}
		if runner.runs != 1 || lock.released != 1 {
			t.Fatalf("runs=%d released=%d, want 1/1", runner.runs, lock.released)
		}
		var summary intromarkers.RunSummary
		if err := json.Unmarshal(progress.resultData, &summary); err != nil || summary.LibrariesScanned != 2 {
			t.Fatalf("result data %q err=%v, want run summary", progress.resultData, err)
		}
	})
	t.Run("error", func(t *testing.T) {
		lock := &fakeClusterLock{acquired: true}
		runner := &fakeMarkerAnalysisRunner{err: errors.New("ffmpeg failed")}
		if err := newTestDetectMarkersTask(runner, lock).Execute(t.Context(), &fakeProgress{}); err == nil {
			t.Fatal("Execute = nil, want analyzer error")
		}
		if lock.released != 1 {
			t.Fatalf("released = %d, want 1", lock.released)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		lock := &fakeClusterLock{acquired: true}
		runner := &fakeMarkerAnalysisRunner{block: true}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := newTestDetectMarkersTask(runner, lock).Execute(ctx, &fakeProgress{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("Execute = %v, want context.Canceled", err)
		}
		if lock.released != 1 {
			t.Fatalf("released = %d, want 1", lock.released)
		}
	})
}

func TestDetectIntroMarkersNilPoolRunsWithoutLock(t *testing.T) {
	task := NewDetectIntroMarkersTask(nil, nil, nil)
	runner := &fakeMarkerAnalysisRunner{}
	task.analyzer = runner
	if err := task.Execute(t.Context(), &fakeProgress{}); err != nil {
		t.Fatalf("Execute = %v, want nil", err)
	}
	if runner.runs != 1 {
		t.Fatalf("analyzer runs = %d, want 1", runner.runs)
	}
}

func TestDetectIntroMarkersAdvisoryLockPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	newTask := func(runner markerAnalysisRunner) *DetectIntroMarkersTask {
		task := NewDetectIntroMarkersTask(pool, nil, nil)
		task.analyzer = runner
		return task
	}

	t.Run("held elsewhere skips", func(t *testing.T) {
		held, acquired, err := pglock.TryAcquire(t.Context(), pool, detectMarkersAdvisoryLock)
		if err != nil || !acquired {
			t.Fatalf("hold marker detection lock: acquired=%t err=%v", acquired, err)
		}
		defer func() { _ = held.Release(context.Background()) }()
		runner := &fakeMarkerAnalysisRunner{}
		progress := &fakeProgress{}
		if err := newTask(runner).Execute(t.Context(), progress); err != nil {
			t.Fatalf("Execute = %v, want nil", err)
		}
		if runner.runs != 0 {
			t.Fatalf("analyzer runs = %d, want 0", runner.runs)
		}
		assertDetectMarkersSkipped(t, progress)
	})

	t.Run("released after error", func(t *testing.T) {
		runner := &fakeMarkerAnalysisRunner{err: errors.New("ffmpeg failed")}
		if err := newTask(runner).Execute(t.Context(), &fakeProgress{}); err == nil || runner.runs != 1 {
			t.Fatalf("Execute error=%v runs=%d, want analyzer error after one run", err, runner.runs)
		}
		lock, acquired, err := pglock.TryAcquire(t.Context(), pool, detectMarkersAdvisoryLock)
		if err != nil || !acquired {
			t.Fatalf("lock after failed run: acquired=%t err=%v, want free", acquired, err)
		}
		if err := lock.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}
