package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database/pglock"
	"github.com/Silo-Server/silo-server/internal/intromarkers"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/mediasample"
)

type fakeMarkerAnalysisRunner struct {
	// runs counts full runs; episodeRuns and movieRuns count episode-only
	// and movie-only runs.
	runs        int
	episodeRuns int
	movieRuns   int
	// kinds is what the last full or episode-only run analyzed; movieKinds
	// is what the last movie-only run was given.
	kinds      intromarkers.EpisodeMarkerKinds
	movieKinds intromarkers.EpisodeMarkerKinds
	// calls records the runs in order, sharing its log with a recordingLock.
	calls   *[]string
	summary intromarkers.RunSummary
	// movieSummary is what a movie-only run returns.
	movieSummary intromarkers.RunSummary
	err          error
	// block waits for ctx cancellation and returns its error.
	block bool
	// preflightErr is what this server's ffmpeg lacks for fingerprinting.
	preflightErr error
}

func (f *fakeMarkerAnalysisRunner) Preflight(context.Context) error { return f.preflightErr }

func (f *fakeMarkerAnalysisRunner) Run(ctx context.Context, kinds intromarkers.EpisodeMarkerKinds, _ intromarkers.ProgressFunc) (intromarkers.RunSummary, error) {
	f.runs++
	f.kinds = kinds
	return f.result(ctx)
}

func (f *fakeMarkerAnalysisRunner) RunEpisodes(ctx context.Context, kinds intromarkers.EpisodeMarkerKinds, _ intromarkers.ProgressFunc) (intromarkers.RunSummary, error) {
	f.episodeRuns++
	f.kinds = kinds
	f.record("episodes")
	return f.result(ctx)
}

func (f *fakeMarkerAnalysisRunner) RunMovies(ctx context.Context, kinds intromarkers.EpisodeMarkerKinds, _ intromarkers.ProgressFunc) (intromarkers.RunSummary, error) {
	f.movieRuns++
	f.movieKinds = kinds
	f.record("movies")
	summary, err := f.result(ctx)
	if err == nil {
		summary = f.movieSummary
	}
	return summary, err
}

func (f *fakeMarkerAnalysisRunner) record(call string) {
	if f.calls != nil {
		*f.calls = append(*f.calls, call)
	}
}

// recordingLock records its acquisition and release in a shared call log.
type recordingLock struct {
	fakeClusterLock
	calls *[]string
}

func (l *recordingLock) TryAcquire(ctx context.Context) (func(), bool, error) {
	release, acquired, err := l.fakeClusterLock.TryAcquire(ctx)
	*l.calls = append(*l.calls, fmt.Sprintf("lock acquired=%t", acquired))
	if !acquired {
		return release, acquired, err
	}
	return func() {
		*l.calls = append(*l.calls, "unlock")
		release()
	}, true, nil
}

func (f *fakeMarkerAnalysisRunner) result(ctx context.Context) (intromarkers.RunSummary, error) {
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

func TestDetectIntroMarkersWithoutChromaprint(t *testing.T) {
	// A server that cannot fingerprint runs its chapter-only episode pass
	// before it consults the lock, so it never makes a capable server skip,
	// then runs movies, which need no Chromaprint, only under the lock.
	unsupported := fmt.Errorf("ffmpeg lacks chromaprint: %w", mediasample.ErrUnsupported)
	t.Run("lock free runs movies under it", func(t *testing.T) {
		var calls []string
		runner := &fakeMarkerAnalysisRunner{
			preflightErr: unsupported,
			calls:        &calls,
			summary:      intromarkers.RunSummary{LibrariesScanned: 2, FilesConsidered: 5},
			movieSummary: intromarkers.RunSummary{LibrariesScanned: 2, MoviesConsidered: 3, MovieCreditsMarkersWritten: 1},
		}
		lock := &recordingLock{fakeClusterLock: fakeClusterLock{acquired: true}, calls: &calls}
		progress := &fakeProgress{}
		if err := newTestDetectMarkersTask(runner, lock).Execute(t.Context(), progress); err != nil {
			t.Fatalf("Execute = %v, want nil", err)
		}
		want := []string{"episodes", "lock acquired=true", "movies", "unlock"}
		if fmt.Sprint(calls) != fmt.Sprint(want) || runner.runs != 0 {
			t.Fatalf("calls = %v full runs = %d, want %v and no full run", calls, runner.runs, want)
		}
		var summary intromarkers.RunSummary
		if err := json.Unmarshal(progress.resultData, &summary); err != nil {
			t.Fatalf("result data %q: %v", progress.resultData, err)
		}
		if summary.LibrariesScanned != 2 || summary.FilesConsidered != 5 || summary.MoviesConsidered != 3 || summary.MovieCreditsMarkersWritten != 1 {
			t.Fatalf("result summary = %+v, want the episode and movie passes merged", summary)
		}
	})
	t.Run("lock held elsewhere skips movies", func(t *testing.T) {
		var calls []string
		runner := &fakeMarkerAnalysisRunner{preflightErr: unsupported, calls: &calls, summary: intromarkers.RunSummary{FilesConsidered: 5}}
		lock := &recordingLock{calls: &calls}
		progress := &fakeProgress{}
		if err := newTestDetectMarkersTask(runner, lock).Execute(t.Context(), progress); err != nil {
			t.Fatalf("Execute = %v, want nil", err)
		}
		want := []string{"episodes", "lock acquired=false"}
		if fmt.Sprint(calls) != fmt.Sprint(want) || runner.runs != 0 {
			t.Fatalf("calls = %v full runs = %d, want %v and no full run", calls, runner.runs, want)
		}
		var summary intromarkers.RunSummary
		if err := json.Unmarshal(progress.resultData, &summary); err != nil || summary.FilesConsidered != 5 {
			t.Fatalf("result data %q err=%v, want the episode summary", progress.resultData, err)
		}
	})
	t.Run("episode failure skips the lock", func(t *testing.T) {
		var calls []string
		runner := &fakeMarkerAnalysisRunner{preflightErr: unsupported, calls: &calls, err: errors.New("database unavailable")}
		lock := &recordingLock{fakeClusterLock: fakeClusterLock{acquired: true}, calls: &calls}
		if err := newTestDetectMarkersTask(runner, lock).Execute(t.Context(), &fakeProgress{}); err == nil {
			t.Fatal("Execute = nil, want the episode pass error")
		}
		if fmt.Sprint(calls) != "[episodes]" {
			t.Fatalf("calls = %v, want only the episode pass", calls)
		}
	})
	t.Run("without a lock runs both passes", func(t *testing.T) {
		runner := &fakeMarkerAnalysisRunner{preflightErr: unsupported}
		task := NewDetectIntroMarkersTask(nil, nil, nil)
		task.analyzer = runner
		if err := task.Execute(t.Context(), &fakeProgress{}); err != nil {
			t.Fatalf("Execute = %v, want nil", err)
		}
		if runner.runs != 1 || runner.episodeRuns != 0 || runner.movieRuns != 0 {
			t.Fatalf("runs=%d episode runs=%d movie runs=%d, want one full run", runner.runs, runner.episodeRuns, runner.movieRuns)
		}
	})
}

func TestDetectIntroMarkersPreflightFailureKeepsLock(t *testing.T) {
	// A failed capability listing proves nothing about ffmpeg, and Run probes
	// again, so the run must stay under the lock.
	preflightErr := errors.New("ffmpeg listing timed out")
	t.Run("acquired", func(t *testing.T) {
		runner := &fakeMarkerAnalysisRunner{preflightErr: preflightErr}
		lock := &fakeClusterLock{acquired: true}
		if err := newTestDetectMarkersTask(runner, lock).Execute(t.Context(), &fakeProgress{}); err != nil {
			t.Fatalf("Execute = %v, want nil", err)
		}
		if runner.runs != 1 || lock.released != 1 {
			t.Fatalf("runs=%d released=%d, want one locked run", runner.runs, lock.released)
		}
	})
	t.Run("held elsewhere", func(t *testing.T) {
		runner := &fakeMarkerAnalysisRunner{preflightErr: preflightErr}
		progress := &fakeProgress{}
		if err := newTestDetectMarkersTask(runner, &fakeClusterLock{}).Execute(t.Context(), progress); err != nil {
			t.Fatalf("Execute = %v, want nil", err)
		}
		if runner.runs != 0 {
			t.Fatalf("analyzer runs = %d, want 0", runner.runs)
		}
		assertDetectMarkersSkipped(t, progress)
	})
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
		if runner.runs != 1 || runner.episodeRuns != 0 || runner.movieRuns != 0 || lock.released != 1 {
			t.Fatalf("runs=%d episode runs=%d movie runs=%d released=%d, want one full locked run", runner.runs, runner.episodeRuns, runner.movieRuns, lock.released)
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

func TestDetectIntroMarkersRunsTheEnabledKinds(t *testing.T) {
	cases := []struct {
		name     string
		settings map[string]string
		want     intromarkers.EpisodeMarkerKinds
	}{
		{name: "defaults", settings: map[string]string{}, want: intromarkers.EpisodeMarkerKinds{Intro: true, Credits: true}},
		{name: "credits off", settings: map[string]string{markers.SettingDetectCredits: "false"}, want: intromarkers.EpisodeMarkerKinds{Intro: true}},
		{name: "intros off", settings: map[string]string{markers.SettingDetectIntros: "false"}, want: intromarkers.EpisodeMarkerKinds{Credits: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.settings[markers.SettingMode] = "local"
			task := NewDetectIntroMarkersTask(nil, nil, &fakeSettingsStore{values: tc.settings})
			runner := &fakeMarkerAnalysisRunner{}
			task.analyzer = runner
			if err := task.Execute(t.Context(), &fakeProgress{}); err != nil {
				t.Fatalf("Execute = %v", err)
			}
			if runner.runs != 1 || runner.kinds != tc.want {
				t.Fatalf("runs=%d kinds=%+v, want one run for %+v", runner.runs, runner.kinds, tc.want)
			}
		})
	}
}

// A server without Chromaprint runs its chapter-only episode pass and its
// movie pass for the enabled kinds too. Movies are credits work, so with
// credits off it stops after the episodes and never claims the lock.
func TestDetectIntroMarkersWithoutChromaprintRunsTheEnabledKinds(t *testing.T) {
	cases := []struct {
		name      string
		setting   string
		want      intromarkers.EpisodeMarkerKinds
		wantCalls []string
	}{
		{
			name:      "intros off",
			setting:   markers.SettingDetectIntros,
			want:      intromarkers.EpisodeMarkerKinds{Credits: true},
			wantCalls: []string{"episodes", "lock acquired=true", "movies", "unlock"},
		},
		{
			name:      "credits off",
			setting:   markers.SettingDetectCredits,
			want:      intromarkers.EpisodeMarkerKinds{Intro: true},
			wantCalls: []string{"episodes"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			runner := &fakeMarkerAnalysisRunner{
				preflightErr: fmt.Errorf("ffmpeg lacks chromaprint: %w", mediasample.ErrUnsupported),
				calls:        &calls,
			}
			lock := &recordingLock{fakeClusterLock: fakeClusterLock{acquired: true}, calls: &calls}
			task := newTestDetectMarkersTask(runner, lock)
			task.settings = &fakeSettingsStore{values: map[string]string{
				markers.SettingMode: "local",
				tc.setting:          "false",
			}}
			progress := &fakeProgress{}
			if err := task.Execute(t.Context(), progress); err != nil {
				t.Fatalf("Execute = %v", err)
			}
			if fmt.Sprint(calls) != fmt.Sprint(tc.wantCalls) || runner.runs != 0 {
				t.Fatalf("calls = %v full runs = %d, want %v and no full run", calls, runner.runs, tc.wantCalls)
			}
			if runner.kinds != tc.want {
				t.Fatalf("episode kinds = %+v, want %+v", runner.kinds, tc.want)
			}
			if runner.movieRuns > 0 && runner.movieKinds != tc.want {
				t.Fatalf("movie kinds = %+v, want %+v", runner.movieKinds, tc.want)
			}
			if !tc.want.Credits && progress.lastMessage != detectMarkersMoviesCreditsOff {
				t.Fatalf("progress = %q, want %q", progress.lastMessage, detectMarkersMoviesCreditsOff)
			}
		})
	}
}

func TestDetectIntroMarkersSkipsWhenBothKindsAreOff(t *testing.T) {
	lock := &fakeClusterLock{acquired: true}
	runner := &fakeMarkerAnalysisRunner{}
	task := newTestDetectMarkersTask(runner, lock)
	task.settings = &fakeSettingsStore{values: map[string]string{
		markers.SettingMode:          "both",
		markers.SettingDetectIntros:  "false",
		markers.SettingDetectCredits: "false",
	}}
	progress := &fakeProgress{}
	if err := task.Execute(t.Context(), progress); err != nil {
		t.Fatalf("Execute = %v, want nil", err)
	}
	if runner.runs != 0 || lock.released != 0 {
		t.Fatalf("runs=%d released=%d, want no run and no lock taken", runner.runs, lock.released)
	}
	if progress.lastMessage != "Marker detection skipped; intro and credits detection are turned off" {
		t.Fatalf("progress = %q", progress.lastMessage)
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
