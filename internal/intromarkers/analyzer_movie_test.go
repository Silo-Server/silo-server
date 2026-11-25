package intromarkers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediaartifact"
	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/models"
)

// fakeMovieSampler answers movie tail passes with text on black over the
// last ten minutes of each movie, sampled every three seconds.
type fakeMovieSampler struct {
	mu        sync.Mutex
	preflight error
	errs      map[int]error
	// noFrames answers every tail pass cleanly without a frame.
	noFrames bool
	silences []mediasample.Interval
	// silenceErr fails every silence read.
	silenceErr error
	tails      []int
	windows    []fingerprintWindow
	// onSample runs on every tail pass.
	onSample func()
	// onSilences runs on every silence read.
	onSilences func()
}

func (f *fakeMovieSampler) PreflightMovieTail(context.Context) error { return f.preflight }

func (f *fakeMovieSampler) SampleMovieTail(_ context.Context, candidate Candidate) (creditsTail, error) {
	f.mu.Lock()
	f.tails = append(f.tails, candidate.FileID)
	onSample := f.onSample
	err := f.errs[candidate.FileID]
	f.mu.Unlock()
	if onSample != nil {
		onSample()
	}
	if err != nil || f.noFrames {
		return creditsTail{}, err
	}
	var frames []mediasample.FrameStats
	for _, at := range movieTailSamples(movieTailWindow(candidate)) {
		frame := stats(5, 20, 40, [5]float32{16, 40, 80, 120, 230}, [4]float32{2, 20, 40, 80})
		if at >= candidate.DurationSeconds-600 {
			frame = stats(99, 99, 99, [5]float32{16, 16, 18, 16, 200}, [4]float32{})
		}
		frame.Seconds = at
		frames = append(frames, frame)
	}
	return creditsTail{Frames: frames}, nil
}

func (f *fakeMovieSampler) SampleMovieSilences(_ context.Context, _ Candidate, window fingerprintWindow) ([]mediasample.Interval, error) {
	f.mu.Lock()
	f.windows = append(f.windows, window)
	onSilences, silences, err := f.onSilences, f.silences, f.silenceErr
	f.mu.Unlock()
	if onSilences != nil {
		onSilences()
	}
	return silences, err
}

func (f *fakeMovieSampler) tailCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tails)
}

func movieCandidate(fileID int, duration float64) Candidate {
	return Candidate{
		FileID: fileID, ContentID: "movie", MediaFolderID: 2, FilePath: "/media/movie.mkv", FileHash: "hash",
		FileSize: int64(fileID), DurationSeconds: duration, CodecVideo: "hevc", CodecAudio: "truehd",
	}
}

func movieAnalyzer(repo *fakeIntroRepository, sampler *fakeMovieSampler) *Analyzer {
	return &Analyzer{
		repo: repo, extractor: &fakeFingerprintExtractor{}, movieSampler: sampler, config: DefaultConfig("ffmpeg"),
		node: "node-a", logger: slog.New(slog.DiscardHandler),
	}
}

func creditsPatches(repo *fakeIntroRepository) []MarkerPatch {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	var credits []MarkerPatch
	for _, patch := range repo.patches {
		if patch.Kind == kindCredits {
			credits = append(credits, patch)
		}
	}
	return credits
}

func TestRunPlacesMovieCreditsFromVideo(t *testing.T) {
	movie := movieCandidate(10, 7200)
	repo := &fakeIntroRepository{enabledLibraries: 1, movieCandidates: []Candidate{movie}}
	sampler := &fakeMovieSampler{silences: []mediasample.Interval{{Start: 6597.5, End: 6598.5}}}
	analyzer := movieAnalyzer(repo, sampler)

	summary, err := analyzer.Run(context.Background(), allMarkerKinds, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.MoviesConsidered != 1 || summary.MovieCreditsMarkersWritten != 1 || summary.CreditsTailScansComputed != 1 || summary.MovieBudgetExhausted {
		t.Fatalf("summary %+v, want one movie placed by video", summary)
	}
	credits := creditsPatches(repo)
	// Text from 6600 s, and a silence ending at 6598.5 s between the last
	// story keyframe (6597 s) and it.
	if len(credits) != 1 || credits[0].Start != 6598.5 || credits[0].End != 7200 ||
		credits[0].Algorithm != CreditsVideoAlgorithm || credits[0].Source != models.MarkerSourceScanner {
		t.Fatalf("credits patches %+v", credits)
	}
	if len(sampler.windows) != 1 || sampler.windows[0] != (fingerprintWindow{Start: 6590, End: 6602}) {
		t.Fatalf("silence windows %+v, want one around the video start", sampler.windows)
	}
	artifact := repo.artifact(10, ArtifactKindCreditsTail)
	if artifact.Status != mediaartifact.StatusComplete || artifact.Key != movieCreditsTailKey() || artifact.ItemCount != 300 ||
		artifact.WindowStartSeconds != 6300 || artifact.WindowEndSeconds != 7200 {
		t.Fatalf("movie tail artifact %+v", artifact)
	}

	// Playback analysis of the same file reuses the stored tail.
	summary, err = analyzer.AnalyzeMovieFile(WithPlaybackPriority(context.Background()), 10)
	if err != nil {
		t.Fatalf("AnalyzeMovieFile: %v", err)
	}
	if sampler.tailCount() != 1 || summary.CreditsTailCacheHits != 1 || summary.MovieCreditsMarkersWritten != 1 {
		t.Fatalf("%d tail passes, summary %+v; want the cached tail", sampler.tailCount(), summary)
	}
}

// A silence pass killed at its deadline keeps the video start but leaves
// the movie unsettled: its tail is recorded as failed with a backoff, not
// stored complete, so the scheduled run takes it up again. A silence pass
// that fails for another reason still settles the movie.
func TestAnalyzeMovieSilenceTimeoutLeavesTheMovieUnsettled(t *testing.T) {
	movie := movieCandidate(10, 7200)
	repo := &fakeIntroRepository{movieCandidates: []Candidate{movie}}
	timeout := &mediasample.Error{Reason: mediasample.ReasonTimeout, Attempts: []mediasample.AttemptError{{
		Decoder: "software", Reason: mediasample.ReasonTimeout, Err: context.DeadlineExceeded,
	}}}
	sampler := &fakeMovieSampler{silenceErr: fmt.Errorf("ffmpeg did not finish within its 2m12s deadline: %w", timeout)}
	analyzer := movieAnalyzer(repo, sampler)

	summary, err := analyzer.AnalyzeMovie(context.Background(), "movie")
	if err != nil {
		t.Fatalf("AnalyzeMovie: %v", err)
	}
	credits := creditsPatches(repo)
	if summary.MovieCreditsMarkersWritten != 1 || summary.CreditsTailScanErrors != 1 || len(credits) != 1 || credits[0].Start != 6600 {
		t.Fatalf("summary %+v, credits %+v; want the video start written", summary, credits)
	}
	artifact := repo.artifact(10, ArtifactKindCreditsTail)
	if artifact.Status != mediaartifact.StatusFailed || artifact.RetryAfter == nil || artifact.Key != movieCreditsTailKey() {
		t.Fatalf("movie tail artifact status %q, retry after %v; want failed with a backoff", artifact.Status, artifact.RetryAfter)
	}

	// Inside the backoff the movie's tail is not sampled again.
	if _, err := analyzer.AnalyzeMovie(context.Background(), "movie"); err != nil || sampler.tailCount() != 1 {
		t.Fatalf("AnalyzeMovie: %v, %d tail passes; want the backoff to hold", err, sampler.tailCount())
	}

	repo = &fakeIntroRepository{movieCandidates: []Candidate{movie}}
	sampler = &fakeMovieSampler{silenceErr: errors.New("ffmpeg exit status 1")}
	if _, err := movieAnalyzer(repo, sampler).AnalyzeMovie(context.Background(), "movie"); err != nil {
		t.Fatalf("AnalyzeMovie: %v", err)
	}
	if artifact := repo.artifact(10, ArtifactKindCreditsTail); artifact.Status != mediaartifact.StatusComplete {
		t.Fatalf("movie tail artifact status %q after a failed silence pass, want complete", artifact.Status)
	}

	// A cached tail an invalidation left stale settles on the video start
	// when its silence pass times out: a failure would not replace the
	// complete row, so the movie would come back on every run.
	invalidated := time.Date(2026, 10, 25, 21, 0, 0, 0, time.UTC)
	repo.movieCandidates[0].MarkersInvalidatedAt = &invalidated
	sampler.silenceErr = fmt.Errorf("ffmpeg did not finish within its 2m12s deadline: %w", timeout)
	if _, err := movieAnalyzer(repo, sampler).AnalyzeMovie(context.Background(), "movie"); err != nil {
		t.Fatalf("AnalyzeMovie: %v", err)
	}
	artifact = repo.artifact(10, ArtifactKindCreditsTail)
	if artifact.Status != mediaartifact.StatusComplete || artifact.MarkersInvalidatedAt == nil || !artifact.MarkersInvalidatedAt.Equal(invalidated) ||
		len(repo.artifactFailures) != 0 || sampler.tailCount() != 1 {
		t.Fatalf("stale tail after a timed out silence pass: status %q, invalidation time %v, %d failures, %d tail passes; want it stored again with the file's time",
			artifact.Status, artifact.MarkersInvalidatedAt, len(repo.artifactFailures), sampler.tailCount())
	}
}

// Issue #5260: a movie tail pass that runs cleanly without a frame is stored
// as sparse and not sampled again, rather than failed and retried.
func TestMovieTailWithoutFramesSettlesAsSparse(t *testing.T) {
	repo := &fakeIntroRepository{movieCandidates: []Candidate{movieCandidate(10, 7200)}}
	sampler := &fakeMovieSampler{noFrames: true}
	analyzer := movieAnalyzer(repo, sampler)

	summary, err := analyzer.AnalyzeMovie(context.Background(), "movie")
	if err != nil {
		t.Fatalf("AnalyzeMovie: %v", err)
	}
	artifact := repo.artifact(10, ArtifactKindCreditsTail)
	if summary.CreditsTailScanErrors != 0 || artifact.Status != mediaartifact.StatusUnusable || artifact.Detail != tailDetailSparse {
		t.Fatalf("summary %+v, tail artifact %q (%q); want it unusable as sparse", summary, artifact.Status, artifact.Detail)
	}
	if _, err := analyzer.AnalyzeMovie(context.Background(), "movie"); err != nil || sampler.tailCount() != 1 {
		t.Fatalf("AnalyzeMovie: %v, %d tail passes; want the settled tail left alone", err, sampler.tailCount())
	}
}

// stallingEpisodes is a repository whose episode count, the episode pass's
// first read, waits for its context to end, like an episode pass on a library
// too large to finish. It then calls afterStall, if set, and returns err, or
// the context's error when err is nil.
type stallingEpisodes struct {
	*fakeIntroRepository
	err        error
	afterStall func()
}

func (s stallingEpisodes) CountEligibleCandidates(ctx context.Context) (int, error) {
	<-ctx.Done()
	if s.afterStall != nil {
		s.afterStall()
	}
	if s.err != nil {
		return 0, s.err
	}
	return 0, ctx.Err()
}

// Issue #3421: a run with a deadline stops its episodes early enough for the
// movies to get their budget, and so does the episode pass a server without
// Chromaprint runs before its separate movie pass. A cancel, or an error
// other than the deadline, during the episodes still stops the run.
func TestRunLeavesMoviesTheirShareOfTheDeadline(t *testing.T) {
	errDatabase := errors.New("database unavailable")
	run := func(a *Analyzer, ctx context.Context) (RunSummary, error) { return a.Run(ctx, allMarkerKinds, nil) }
	runEpisodes := func(a *Analyzer, ctx context.Context) (RunSummary, error) {
		return a.RunEpisodes(ctx, allMarkerKinds, nil)
	}
	for _, tc := range []struct {
		name     string
		run      func(*Analyzer, context.Context) (RunSummary, error)
		cancelAt time.Duration
		// cancelAsShareEnds cancels the run as the episodes' share ends.
		cancelAsShareEnds bool
		// noMovies leaves the movie pass nothing to analyze.
		noMovies    bool
		episodesErr error
		wantErr     error
		wantMovies  int
		wantSpent   bool
	}{
		{name: "episode share spent", run: run, wantMovies: 1, wantSpent: true},
		{name: "episode pass alone leaves the movie share", run: runEpisodes, wantSpent: true},
		{name: "repository error at the end of the share", run: run, episodesErr: errDatabase, wantErr: errDatabase},
		{name: "canceled during episodes", run: run, cancelAt: 50 * time.Millisecond, wantErr: context.Canceled},
		{name: "episode pass alone canceled as its share ends", run: runEpisodes, cancelAsShareEnds: true, wantErr: context.Canceled, wantSpent: true},
		{name: "no movies to analyze leaves the episodes the whole run", run: run, noMovies: true, wantErr: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := &fakeIntroRepository{enabledLibraries: 1, movieCandidates: []Candidate{movieCandidate(10, 7200)}}
			if tc.noMovies {
				repo.movieCandidates = nil
			}
			sampler := &fakeMovieSampler{}
			analyzer := movieAnalyzer(repo, sampler)
			analyzer.repo = stallingEpisodes{fakeIntroRepository: repo, err: tc.episodesErr}
			// The episodes' share ends a second before the deadline, which
			// leaves the movie pass a wide margin on a loaded runner.
			analyzer.movieBudget = time.Second
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if tc.cancelAt > 0 {
				stop := time.AfterFunc(tc.cancelAt, cancel)
				defer stop.Stop()
			}
			if tc.cancelAsShareEnds {
				analyzer.repo = stallingEpisodes{fakeIntroRepository: repo, err: tc.episodesErr, afterStall: cancel}
			}

			summary, err := tc.run(analyzer, ctx)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if summary.EpisodeBudgetExhausted != tc.wantSpent || sampler.tailCount() != tc.wantMovies || summary.MovieCreditsMarkersWritten != tc.wantMovies {
				t.Fatalf("summary %+v, %d tail passes; want episode_budget_exhausted %v and %d movies", summary, sampler.tailCount(), tc.wantSpent, tc.wantMovies)
			}
			if tc.wantErr == nil && ctx.Err() != nil {
				t.Fatalf("ctx %v; want the run finished before its deadline", ctx.Err())
			}
		})
	}
}

// The scheduled run stops starting movies once the movie budget is spent;
// the movie in progress finishes and the rest wait for the next run.
func TestRunStopsMoviesAtTheBudget(t *testing.T) {
	repo := &fakeIntroRepository{enabledLibraries: 1, movieCandidates: []Candidate{
		movieCandidate(10, 7200), movieCandidate(11, 7200), movieCandidate(12, 7200),
	}}
	var mu sync.Mutex
	now := time.Date(2026, 9, 26, 3, 30, 0, 0, time.UTC)
	sampler := &fakeMovieSampler{onSample: func() {
		mu.Lock()
		now = now.Add(61 * time.Minute)
		mu.Unlock()
	}}
	analyzer := movieAnalyzer(repo, sampler)
	analyzer.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}

	summary, err := analyzer.Run(context.Background(), allMarkerKinds, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sampler.tailCount() != 1 || summary.MoviesConsidered != 3 || summary.MovieCreditsMarkersWritten != 1 || !summary.MovieBudgetExhausted {
		t.Fatalf("%d tail passes, summary %+v; want one movie before the budget ran out", sampler.tailCount(), summary)
	}
}
