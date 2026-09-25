package intromarkers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// concurrencyProbeExtractor records how many extractions overlap. Each call
// waits until another is in flight (or a timeout passes) so a serialized run
// cannot look parallel by accident.
type concurrencyProbeExtractor struct {
	mu       sync.Mutex
	inFlight int
	peak     int
	overlap  chan struct{}
	once     sync.Once
}

// failingExtractor fails the listed files and finds no audio in the rest.
type failingExtractor struct{ errFor map[int]error }

func (failingExtractor) Preflight(context.Context) error { return nil }

func (e failingExtractor) Extract(_ context.Context, candidate Candidate) (Fingerprint, bool, error) {
	return Fingerprint{}, false, e.errFor[candidate.FileID]
}

func (e *concurrencyProbeExtractor) Preflight(context.Context) error { return nil }

func (e *concurrencyProbeExtractor) Extract(context.Context, Candidate) (Fingerprint, bool, error) {
	e.mu.Lock()
	e.inFlight++
	e.peak = max(e.peak, e.inFlight)
	if e.inFlight >= 2 {
		e.once.Do(func() { close(e.overlap) })
	}
	e.mu.Unlock()
	select {
	case <-e.overlap:
	case <-time.After(2 * time.Second):
	}
	e.mu.Lock()
	e.inFlight--
	e.mu.Unlock()
	return Fingerprint{}, false, nil
}

func TestRunAnalyzesGroupsInParallelWithinFFmpegLimit(t *testing.T) {
	var candidates []Candidate
	for season := 1; season <= 6; season++ {
		for episode := 1; episode <= 2; episode++ {
			candidates = append(candidates, Candidate{
				FileID:          season*10 + episode,
				EpisodeID:       fmt.Sprintf("s%de%d", season, episode),
				SeasonID:        fmt.Sprintf("season-%d", season),
				MediaFolderID:   1,
				DurationSeconds: 1800,
			})
		}
	}
	repo := &fakeIntroRepository{enabledLibraries: 1, eligibleCandidates: candidates}
	extractor := &concurrencyProbeExtractor{overlap: make(chan struct{})}
	cfg := DefaultConfig("ffmpeg")
	cfg.MaxParallelFFmpeg = 2
	analyzer := &Analyzer{
		repo: repo, extractor: extractor, config: cfg,
		logger: slog.New(slog.DiscardHandler), ffmpegSlots: newSlotLimiter(2),
	}

	summary, err := analyzer.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.GroupsNotFound != 6 {
		t.Fatalf("groups not found = %d, want all 6 analyzed", summary.GroupsNotFound)
	}
	if extractor.peak != 2 {
		t.Fatalf("peak concurrent extractions = %d, want exactly the 2-slot limit", extractor.peak)
	}
}

func TestAnalyzeGroupMarksFailedExtractionsPartial(t *testing.T) {
	candidates := []Candidate{
		{FileID: 1, EpisodeID: "e1", SeasonID: "s1", MediaFolderID: 1, DurationSeconds: 1800},
		{FileID: 2, EpisodeID: "e2", SeasonID: "s1", MediaFolderID: 1, DurationSeconds: 1800},
		{FileID: 3, EpisodeID: "e3", SeasonID: "s1", MediaFolderID: 1, DurationSeconds: 1800},
	}
	repo := &fakeIntroRepository{}
	extractor := failingExtractor{errFor: map[int]error{2: errors.New("read failed")}}
	analyzer := &Analyzer{repo: repo, extractor: extractor, config: DefaultConfig("ffmpeg"), logger: slog.New(slog.DiscardHandler)}

	summary, err := analyzer.analyzeGroup(context.Background(), candidateGroup{
		SeasonID: "s1", MediaFolderID: 1, AnalysisGroupKey: candidates[0].AnalysisGroupKey(), Candidates: candidates,
	}, analyzeGroupOptions{persistState: true})
	if err != nil {
		t.Fatalf("analyzeGroup: %v", err)
	}
	if summary.FingerprintExtractionErrors != 1 {
		t.Fatalf("extraction errors = %d, want 1", summary.FingerprintExtractionErrors)
	}
	if len(repo.upsertedStates) != 1 || repo.upsertedStates[0].Status != seasonStatusPartial {
		t.Fatalf("stored states = %+v, want one partial state", repo.upsertedStates)
	}
}

func TestSeasonStateSettled(t *testing.T) {
	now := time.Date(2026, 9, 25, 3, 30, 0, 0, time.UTC)
	cases := []struct {
		state SeasonState
		want  bool
	}{
		{SeasonState{Status: seasonStatusComplete}, true},
		{SeasonState{Status: seasonStatusNotFound}, true},
		{SeasonState{Status: seasonStatusFailed}, false},
		{SeasonState{Status: seasonStatusPartial, AnalyzedAt: now.Add(-24 * time.Hour)}, true},
		{SeasonState{Status: seasonStatusPartial, AnalyzedAt: now.Add(-partialSeasonRetryInterval)}, false},
	}
	for _, tc := range cases {
		if got := tc.state.settled(now); got != tc.want {
			t.Errorf("settled(%+v) = %v, want %v", tc.state, got, tc.want)
		}
	}
}

func TestDetectionRunsOneWorkerByDefault(t *testing.T) {
	if got := DefaultConfig("ffmpeg").MaxParallelFFmpeg; got != 1 {
		t.Fatalf("default MaxParallelFFmpeg = %d, want 1", got)
	}
}

func TestInteractiveAnalysisUsesReservedFFmpegSlot(t *testing.T) {
	analyzer := &Analyzer{ffmpegSlots: newSlotLimiter(1), interactiveSlots: make(chan struct{}, 1)}
	if ok, _ := analyzer.ffmpegSlots.tryAcquire(); !ok { // the nightly run holds every shared slot
		t.Fatal("could not take the only shared slot")
	}

	interactive := WithPlaybackPriority(context.Background())
	release, err := analyzer.acquireFFmpeg(interactive)
	if err != nil {
		t.Fatalf("interactive acquire: %v", err)
	}
	release()

	nightly, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := analyzer.acquireFFmpeg(nightly); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("nightly acquire with every shared slot busy = %v, want it to wait", err)
	}
}

func TestSilenceRefinementSkipsFileWhenDeadlinePassesWhileWaiting(t *testing.T) {
	refiner := &fakeBoundaryRefiner{}
	analyzer := &Analyzer{refiner: refiner, config: DefaultConfig("ffmpeg"), logger: slog.New(slog.DiscardHandler)}
	var summary RunSummary
	_, refined := analyzer.refineChapterSegment(context.Background(), Candidate{FileID: 1}, Segment{Start: 60, End: 120},
		time.Now().Add(-time.Second), &summary)
	if refined || refiner.calls != 0 || summary.SilenceRefinementsAttempted != 0 {
		t.Fatalf("refined=%v calls=%d attempted=%d, want the file skipped without running ffmpeg",
			refined, refiner.calls, summary.SilenceRefinementsAttempted)
	}
}

func TestAnalyzeEpisodeReportsExtractionFailures(t *testing.T) {
	target := Candidate{FileID: 1, EpisodeID: "e1", SeasonID: "s1", MediaFolderID: 1, DurationSeconds: 1800}
	sibling := Candidate{FileID: 2, EpisodeID: "e2", SeasonID: "s1", MediaFolderID: 1, DurationSeconds: 1800}
	repo := &fakeIntroRepository{
		episodeCandidates: map[string][]Candidate{"e1": {target}},
		groupCandidates:   map[string][]Candidate{groupKey(1, "s1", target.AnalysisGroupKey()): {target, sibling}},
	}
	analyzer := &Analyzer{repo: repo, extractor: failingExtractor{errFor: map[int]error{2: errors.New("read failed")}},
		config: DefaultConfig("ffmpeg"), logger: slog.New(slog.DiscardHandler)}

	summary, err := analyzer.AnalyzeEpisode(context.Background(), "e1")
	if err != nil {
		t.Fatalf("AnalyzeEpisode: %v", err)
	}
	if summary.FingerprintExtractionErrors != 1 {
		t.Fatalf("extraction errors = %d, want 1", summary.FingerprintExtractionErrors)
	}
}

func TestSetWorkersResizesTheFFmpegLimit(t *testing.T) {
	analyzer := &Analyzer{config: DefaultConfig("ffmpeg"), ffmpegSlots: newSlotLimiter(2), workers: 2}

	// An extraction started under the old limit releases that limit.
	release, err := analyzer.acquireFFmpeg(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	analyzer.SetWorkers(3)
	release()
	if got := analyzer.workerCount(); got != 3 {
		t.Fatalf("workerCount() = %d, want 3", got)
	}
	var releases []func()
	for range 3 {
		r, err := analyzer.acquireFFmpeg(context.Background())
		if err != nil {
			t.Fatalf("acquire under the new limit: %v", err)
		}
		releases = append(releases, r)
	}
	busy, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := analyzer.acquireFFmpeg(busy); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("fourth acquire = %v, want it to wait at the 3-slot limit", err)
	}
	for _, r := range releases {
		r()
	}

	analyzer.SetWorkers(0)
	if got := analyzer.workerCount(); got != DefaultDetectionWorkers {
		t.Fatalf("workerCount() after SetWorkers(0) = %d, want the default %d", got, DefaultDetectionWorkers)
	}
}

// acquireAsync starts acquireFFmpeg in the background and returns a channel
// that delivers its release once a slot is granted.
func acquireAsync(t *testing.T, analyzer *Analyzer, ctx context.Context) <-chan func() {
	t.Helper()
	granted := make(chan func(), 1)
	go func() {
		release, err := analyzer.acquireFFmpeg(ctx)
		if err != nil {
			return
		}
		granted <- release
	}()
	return granted
}

func assertWaiting(t *testing.T, granted <-chan func(), why string) {
	t.Helper()
	select {
	case release := <-granted:
		release()
		t.Fatalf("acquire was granted, want it to wait: %s", why)
	case <-time.After(20 * time.Millisecond):
	}
}

func assertGranted(t *testing.T, granted <-chan func(), why string) func() {
	t.Helper()
	select {
	case release := <-granted:
		return release
	case <-time.After(2 * time.Second):
		t.Fatalf("acquire still waiting: %s", why)
		return nil
	}
}

func TestSetWorkersLoweringTheLimitWaitsForHoldersToDrain(t *testing.T) {
	analyzer := &Analyzer{config: DefaultConfig("ffmpeg"), ffmpegSlots: newSlotLimiter(3), workers: 3}
	var held []func()
	for range 3 {
		release, err := analyzer.acquireFFmpeg(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, release)
	}

	analyzer.SetWorkers(1)
	held[0]()
	held[1]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	granted := acquireAsync(t, analyzer, ctx)
	assertWaiting(t, granted, "one extraction from the old limit still runs and the new limit is 1")

	held[2]()
	assertGranted(t, granted, "every old holder released")()
}

func TestSetWorkersRaisingTheLimitWakesWaiters(t *testing.T) {
	analyzer := &Analyzer{config: DefaultConfig("ffmpeg"), ffmpegSlots: newSlotLimiter(1), workers: 1}
	release, err := analyzer.acquireFFmpeg(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	granted := acquireAsync(t, analyzer, ctx)
	assertWaiting(t, granted, "the only slot is held")

	analyzer.SetWorkers(2)
	assertGranted(t, granted, "the limit grew to 2")()
}

// lookupProbeRepository records how many LoadFingerprint calls overlap. Each
// call waits until the lookup limit is reached (or a timeout passes), so a
// bounded run peaks exactly at the limit rather than by scheduling luck.
type lookupProbeRepository struct {
	*fakeIntroRepository
	mu       sync.Mutex
	inFlight int
	peak     int
	full     chan struct{}
	once     sync.Once
}

func (r *lookupProbeRepository) LoadFingerprint(ctx context.Context, candidate Candidate, cfg Config) (*Fingerprint, error) {
	r.mu.Lock()
	r.inFlight++
	r.peak = max(r.peak, r.inFlight)
	if r.inFlight >= maxConcurrentFingerprintLookups {
		r.once.Do(func() { close(r.full) })
	}
	r.mu.Unlock()
	select {
	case <-r.full:
		// Stay in flight briefly so lookups past the limit, if allowed,
		// overlap and show in the peak.
		time.Sleep(5 * time.Millisecond)
	case <-time.After(2 * time.Second):
	}
	r.mu.Lock()
	r.inFlight--
	r.mu.Unlock()
	return r.fakeIntroRepository.LoadFingerprint(ctx, candidate, cfg)
}

func TestFingerprintLookupsShareOneBoundAcrossGroups(t *testing.T) {
	// Three seasons of three episodes on three workers want nine lookups at
	// once; no single season can reach the limit alone.
	var candidates []Candidate
	for season := 1; season <= 3; season++ {
		for episode := 1; episode <= 3; episode++ {
			candidates = append(candidates, Candidate{
				FileID:          season*10 + episode,
				EpisodeID:       fmt.Sprintf("s%de%d", season, episode),
				SeasonID:        fmt.Sprintf("season-%d", season),
				MediaFolderID:   1,
				DurationSeconds: 1800,
			})
		}
	}
	repo := &lookupProbeRepository{
		fakeIntroRepository: &fakeIntroRepository{enabledLibraries: 1, eligibleCandidates: candidates},
		full:                make(chan struct{}),
	}
	cfg := DefaultConfig("ffmpeg")
	analyzer := &Analyzer{
		repo: repo, extractor: failingExtractor{}, config: cfg,
		logger: slog.New(slog.DiscardHandler), ffmpegSlots: newSlotLimiter(3), workers: 3,
	}

	if _, err := analyzer.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if repo.peak != maxConcurrentFingerprintLookups {
		t.Fatalf("peak concurrent lookups = %d, want exactly the %d-lookup limit", repo.peak, maxConcurrentFingerprintLookups)
	}
}
