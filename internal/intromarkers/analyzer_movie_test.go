package intromarkers

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/models"
)

// fakeMovieSampler answers movie tail passes with text on black over the
// last ten minutes of each movie, sampled every three seconds.
type fakeMovieSampler struct {
	mu        sync.Mutex
	preflight error
	errs      map[int]error
	silences  []mediasample.Interval
	tails     []int
	windows   []fingerprintWindow
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
	if err != nil {
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
	onSilences, silences := f.onSilences, f.silences
	f.mu.Unlock()
	if onSilences != nil {
		onSilences()
	}
	return silences, nil
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

	summary, err := analyzer.Run(context.Background(), nil)
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
	if artifact.Status != ArtifactComplete || artifact.ArtifactKey != movieCreditsTailKey() || artifact.ItemCount != 300 ||
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

// A sampled tail is stored only once the movie's credits are settled. A
// movie canceled while its credits are refined, or whose credits write
// fails, keeps no complete tail, so the scheduled run, which skips movies
// with one, takes it up again.
func TestAnalyzeMovieStoresTheTailOnlyOnceCreditsAreSettled(t *testing.T) {
	movie := movieCandidate(10, 7200)
	repo := &fakeIntroRepository{movieCandidates: []Candidate{movie}}
	ctx, cancel := context.WithCancel(context.Background())
	sampler := &fakeMovieSampler{onSilences: cancel}
	analyzer := movieAnalyzer(repo, sampler)

	if _, err := analyzer.AnalyzeMovie(ctx, "movie"); !errors.Is(err, context.Canceled) {
		t.Fatalf("AnalyzeMovie: %v, want canceled", err)
	}
	if artifact := repo.artifact(10, ArtifactKindCreditsTail); artifact.Status != "" || len(creditsPatches(repo)) != 0 {
		t.Fatalf("canceled during refinement: artifact status %q, patches %+v; want neither", artifact.Status, creditsPatches(repo))
	}

	sampler.onSilences = nil
	repo.patchErr = errors.New("database went away")
	summary, err := analyzer.AnalyzeMovie(context.Background(), "movie")
	if err != nil {
		t.Fatalf("AnalyzeMovie: %v", err)
	}
	if artifact := repo.artifact(10, ArtifactKindCreditsTail); artifact.Status != "" || len(summary.Errors) != 1 {
		t.Fatalf("failed write: artifact status %q, summary %+v; want no artifact and the error", artifact.Status, summary)
	}

	repo.patchErr = nil
	summary, err = analyzer.AnalyzeMovie(context.Background(), "movie")
	if err != nil {
		t.Fatalf("AnalyzeMovie: %v", err)
	}
	if artifact := repo.artifact(10, ArtifactKindCreditsTail); artifact.Status != ArtifactComplete ||
		summary.MovieCreditsMarkersWritten != 1 || sampler.tailCount() != 3 {
		t.Fatalf("artifact status %q, summary %+v, %d tail passes; want the third pass stored with its credits", artifact.Status, summary, sampler.tailCount())
	}
}

func TestAnalyzeMoviePrefersChapters(t *testing.T) {
	movie := movieCandidate(10, 7200)
	movie.Chapters = []models.MediaChapter{
		{Title: "Chapter 1", StartSeconds: 0, EndSeconds: 6700},
		{Title: "End Credits", StartSeconds: 6700, EndSeconds: 7200},
	}
	repo := &fakeIntroRepository{movieCandidates: []Candidate{movie}}
	sampler := &fakeMovieSampler{}
	summary, err := movieAnalyzer(repo, sampler).AnalyzeMovie(context.Background(), "movie")
	if err != nil {
		t.Fatalf("AnalyzeMovie: %v", err)
	}
	credits := creditsPatches(repo)
	if len(credits) != 1 || credits[0].Start != 6700 || credits[0].Algorithm != CreditsChapterAlgorithm ||
		summary.MovieCreditsMarkersWritten != 1 || sampler.tailCount() != 0 {
		t.Fatalf("credits %+v, summary %+v, %d tail passes; want the chapter without a pass", credits, summary, sampler.tailCount())
	}
}

func TestAnalyzeMovieLeavesHigherPriorityCredits(t *testing.T) {
	movie := movieCandidate(10, 7200)
	start, end, online := 6500.0, 7200.0, models.MarkerSourceOnline
	movie.CreditsStart, movie.CreditsEnd, movie.CreditsMarkersSource = &start, &end, &online
	repo := &fakeIntroRepository{movieCandidates: []Candidate{movie}}
	sampler := &fakeMovieSampler{}
	if _, err := movieAnalyzer(repo, sampler).AnalyzeMovie(context.Background(), "movie"); err != nil {
		t.Fatal(err)
	}
	if len(creditsPatches(repo)) != 0 || sampler.tailCount() != 0 {
		t.Fatalf("patches %+v, %d tail passes; want the online credits left alone", repo.patches, sampler.tailCount())
	}
}

func TestAnalyzeMovieStatuses(t *testing.T) {
	noVideo := movieCandidate(11, 7200)
	noVideo.CodecVideo = ""
	failing := movieCandidate(12, 7200)
	repo := &fakeIntroRepository{movieCandidates: []Candidate{noVideo, failing}}
	sampler := &fakeMovieSampler{errs: map[int]error{12: errors.New("network share went away")}}
	analyzer := movieAnalyzer(repo, sampler)

	summary, err := analyzer.AnalyzeMovie(context.Background(), "movie")
	if err != nil {
		t.Fatal(err)
	}
	if summary.CreditsTailUnusable != 1 || summary.CreditsTailScanErrors != 1 || len(creditsPatches(repo)) != 0 {
		t.Fatalf("summary %+v", summary)
	}
	if artifact := repo.artifact(11, ArtifactKindCreditsTail); artifact.Status != ArtifactUnusable || artifact.Detail != tailDetailNoVideo {
		t.Fatalf("no-video artifact %+v", artifact)
	}
	if artifact := repo.artifact(12, ArtifactKindCreditsTail); artifact.Status != ArtifactFailed || artifact.RecordedBy != "node-a" {
		t.Fatalf("failed artifact %+v", artifact)
	}

	// The next analysis skips both: one is unusable, the other backs off.
	summary, err = analyzer.AnalyzeMovie(context.Background(), "movie")
	if err != nil {
		t.Fatal(err)
	}
	if sampler.tailCount() != 1 || summary.CreditsTailUnusable != 1 || summary.CreditsTailScanErrors != 0 {
		t.Fatalf("%d tail passes, summary %+v; want no new pass", sampler.tailCount(), summary)
	}
}

func TestAnalyzeMovieWithoutVisualsUsesChaptersOnly(t *testing.T) {
	repo := &fakeIntroRepository{movieCandidates: []Candidate{movieCandidate(10, 7200)}}
	sampler := &fakeMovieSampler{preflight: errors.New("ffmpeg lacks signalstats")}
	summary, err := movieAnalyzer(repo, sampler).AnalyzeMovie(context.Background(), "movie")
	if err != nil {
		t.Fatal(err)
	}
	if sampler.tailCount() != 0 || summary.MovieCreditsMarkersWritten != 0 {
		t.Fatalf("%d tail passes, summary %+v", sampler.tailCount(), summary)
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

	summary, err := analyzer.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sampler.tailCount() != 1 || summary.MoviesConsidered != 3 || summary.MovieCreditsMarkersWritten != 1 || !summary.MovieBudgetExhausted {
		t.Fatalf("%d tail passes, summary %+v; want one movie before the budget ran out", sampler.tailCount(), summary)
	}
}
