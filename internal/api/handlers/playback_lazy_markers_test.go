package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Silo-Server/silo-server/internal/intromarkers"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type fakePlaybackMarkerProvider struct{}

func (fakePlaybackMarkerProvider) ID() string { return "fake-online" }

func (fakePlaybackMarkerProvider) FetchMarkers(context.Context, markers.Request) (markers.Result, error) {
	return markers.Result{}, nil
}

type fakePlaybackIntroAnalyzer struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
	onCall  func()
	summary intromarkers.RunSummary
	err     error
	kinds   []intromarkers.EpisodeMarkerKinds
	// movieFiles records AnalyzeMovieFile calls, and interactive whether
	// each ran with playback priority.
	movieFiles  []int
	interactive []bool
}

func (a *fakePlaybackIntroAnalyzer) AnalyzeMovieFile(ctx context.Context, fileID int) (intromarkers.RunSummary, error) {
	a.mu.Lock()
	a.calls++
	a.movieFiles = append(a.movieFiles, fileID)
	a.interactive = append(a.interactive, mediasample.Interactive(ctx))
	a.mu.Unlock()
	if a.onCall != nil {
		a.onCall()
	}
	if a.started != nil {
		select {
		case a.started <- struct{}{}:
		default:
		}
	}
	return intromarkers.RunSummary{FilesConsidered: 1}, a.err
}

func (a *fakePlaybackIntroAnalyzer) AnalyzeEpisodeForPlayback(_ context.Context, _ string, kinds intromarkers.EpisodeMarkerKinds) (intromarkers.RunSummary, error) {
	a.mu.Lock()
	a.calls++
	a.kinds = append(a.kinds, kinds)
	a.mu.Unlock()
	if a.onCall != nil {
		a.onCall()
	}
	if a.started != nil {
		select {
		case a.started <- struct{}{}:
		default:
		}
	}
	if a.release != nil {
		<-a.release
	}
	summary := a.summary
	if summary.FilesConsidered == 0 {
		summary = intromarkers.RunSummary{FilesConsidered: 1, ChapterMarkersWritten: 1}
	}
	return summary, a.err
}

func (a *fakePlaybackIntroAnalyzer) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func (a *fakePlaybackIntroAnalyzer) requestedKinds() []intromarkers.EpisodeMarkerKinds {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]intromarkers.EpisodeMarkerKinds(nil), a.kinds...)
}

type fakePlaybackIntroEligibility struct {
	eligible bool
	err      error
}

func (e fakePlaybackIntroEligibility) IntroDetectionEligibleForPlayback(context.Context, int) (bool, error) {
	return e.eligible, e.err
}

func (e fakePlaybackIntroEligibility) IsFileInEnabledLibrary(context.Context, int) (bool, error) {
	return e.eligible, e.err
}

type fakePlaybackMarkerFileResolver struct {
	mu   sync.Mutex
	file *models.MediaFile
}

func (r *fakePlaybackMarkerFileResolver) GetByID(context.Context, int) (*models.MediaFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil, nil
	}
	cp := *r.file
	return &cp, nil
}

func (r *fakePlaybackMarkerFileResolver) setFile(file *models.MediaFile) {
	r.mu.Lock()
	r.file = file
	r.mu.Unlock()
}

type fakePlaybackMarkerNotifier struct {
	ch chan *models.MediaFile
}

func (n fakePlaybackMarkerNotifier) MarkersUpdated(_ context.Context, file *models.MediaFile) {
	if n.ch == nil {
		return
	}
	n.ch <- file
}

func TestMaybeQueueLazyPlaybackMarkersGates(t *testing.T) {
	tests := []struct {
		name     string
		lazy     string
		mode     string
		file     *models.MediaFile
		eligible bool
	}{
		{
			name:     "lazy disabled",
			lazy:     "false",
			mode:     "local",
			file:     lazyMarkerTestFile(),
			eligible: true,
		},
		{
			name:     "mode off",
			lazy:     "true",
			mode:     "off",
			file:     lazyMarkerTestFile(),
			eligible: true,
		},
		{
			name:     "online mode without providers",
			lazy:     "true",
			mode:     "online",
			file:     lazyMarkerTestFile(),
			eligible: true,
		},
		{
			name: "intro and credits already present",
			lazy: "true",
			mode: "local",
			file: func() *models.MediaFile {
				file := lazyMarkerTestFile()
				start, end := 10.0, 60.0
				creditsStart, creditsEnd := 1700.0, 1800.0
				file.IntroStart, file.IntroEnd = &start, &end
				file.CreditsStart, file.CreditsEnd = &creditsStart, &creditsEnd
				return file
			}(),
			eligible: true,
		},
		{
			name: "missing episode id",
			lazy: "true",
			mode: "local",
			file: func() *models.MediaFile {
				file := lazyMarkerTestFile()
				file.EpisodeID = ""
				return file
			}(),
			eligible: true,
		},
		{
			name: "movie with credits",
			lazy: "true",
			mode: "local",
			file: func() *models.MediaFile {
				file := lazyMarkerMovieFile()
				start, end := 6500.0, 7000.0
				file.CreditsStart, file.CreditsEnd = &start, &end
				return file
			}(),
			eligible: true,
		},
		{
			name:     "library ineligible",
			lazy:     "true",
			mode:     "local",
			file:     lazyMarkerTestFile(),
			eligible: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			analyzer := &fakePlaybackIntroAnalyzer{started: make(chan struct{}, 1)}
			handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), &fakePlaybackMarkerFileResolver{file: tt.file})
			handler.SettingsRepo = testPlaybackSettingsRepo{values: map[string]string{
				markers.SettingLazyPlayback: tt.lazy,
				markers.SettingMode:         tt.mode,
			}}
			handler.IntroRepository = fakePlaybackIntroEligibility{eligible: tt.eligible}
			handler.IntroAnalyzer = analyzer
			handler.MarkerLazyContext = context.Background()

			handler.maybeQueueLazyPlaybackMarkers(context.Background(), &playback.Session{ID: "session-1"}, tt.file)

			select {
			case <-analyzer.started:
				t.Fatal("AnalyzeEpisode started, want gated")
			case <-time.After(25 * time.Millisecond):
			}
			if got := analyzer.callCount(); got != 0 {
				t.Fatalf("AnalyzeEpisode calls = %d, want 0", got)
			}
		})
	}
}

func TestMaybeQueueLazyPlaybackMarkersLocalModeRunsAnalyzerAndEmitsUpdate(t *testing.T) {
	file := lazyMarkerTestFile()
	resolver := &fakePlaybackMarkerFileResolver{file: file}
	start := 12.0
	end := 75.0
	analyzer := &fakePlaybackIntroAnalyzer{
		started: make(chan struct{}, 1),
		onCall: func() {
			updated := *file
			updated.IntroStart = &start
			updated.IntroEnd = &end
			resolver.setFile(&updated)
		},
	}
	notifier := fakePlaybackMarkerNotifier{ch: make(chan *models.MediaFile, 1)}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), resolver)
	handler.SettingsRepo = testPlaybackSettingsRepo{values: map[string]string{
		markers.SettingLazyPlayback: "true",
		markers.SettingMode:         "local",
	}}
	handler.IntroRepository = fakePlaybackIntroEligibility{eligible: true}
	handler.IntroAnalyzer = analyzer
	handler.MarkerUpdateNotifier = notifier
	handler.MarkerLazyContext = context.Background()

	handler.maybeQueueLazyPlaybackMarkers(context.Background(), &playback.Session{ID: "session-1"}, file)

	select {
	case <-analyzer.started:
	case <-time.After(time.Second):
		t.Fatal("AnalyzeEpisode did not start")
	}

	select {
	case notified := <-notifier.ch:
		if notified.ID != file.ID {
			t.Fatalf("notified file ID = %d, want %d", notified.ID, file.ID)
		}
		if notified.IntroStart == nil || notified.IntroEnd == nil {
			t.Fatalf("notified file missing intro marker: %#v", notified)
		}
	case <-time.After(time.Second):
		t.Fatal("marker update was not emitted")
	}
	if got := analyzer.callCount(); got != 1 {
		t.Fatalf("AnalyzeEpisode calls = %d, want 1", got)
	}
}

// Local analysis also finds credits, so an episode whose intro is known still
// runs it while its credits are missing, for credits only.
func TestMaybeQueueLazyPlaybackMarkersRunsLocalForMissingCredits(t *testing.T) {
	file := lazyMarkerTestFile()
	start, end := 10.0, 60.0
	file.IntroStart, file.IntroEnd = &start, &end
	online := models.MarkerSourceOnline
	file.IntroMarkersSource = &online
	analyzer := &fakePlaybackIntroAnalyzer{started: make(chan struct{}, 1)}
	handler := newLazyMarkerTestHandler(file, analyzer, nil)

	handler.maybeQueueLazyPlaybackMarkers(context.Background(), &playback.Session{ID: "session-1"}, file)

	select {
	case <-analyzer.started:
	case <-time.After(time.Second):
		t.Fatal("AnalyzeEpisode did not start for an episode without credits")
	}
	want := intromarkers.EpisodeMarkerKinds{Credits: true}
	if got := analyzer.requestedKinds(); len(got) != 1 || got[0] != want {
		t.Fatalf("requested kinds = %+v, want [%+v]", got, want)
	}
}

// The server-wide detection settings narrow what playback analyzes: a kind
// turned off is never looked for, and a file with nothing left to find gets
// no analysis.
func TestLazyPlaybackMarkersHonorDetectionKindSettings(t *testing.T) {
	type kinds = intromarkers.EpisodeMarkerKinds
	withIntro := func(file *models.MediaFile) *models.MediaFile {
		return withMarker(file, models.MarkerSegmentIntro, 10, 60, models.MarkerSourceOnline)
	}
	cases := []struct {
		name       string
		file       *models.MediaFile
		intros     string
		credits    string
		wantKinds  []kinds
		wantMovies int
	}{
		{name: "episode with credits off", file: lazyMarkerTestFile(), credits: "false", wantKinds: []kinds{{Intro: true}}},
		{name: "episode with intros off", file: lazyMarkerTestFile(), intros: "false", wantKinds: []kinds{{Credits: true}}},
		{name: "episode with its intro and credits off", file: withIntro(lazyMarkerTestFile()), credits: "false"},
		{name: "episode with both off", file: lazyMarkerTestFile(), intros: "false", credits: "false"},
		{name: "movie with credits off", file: lazyMarkerMovieFile(), credits: "false"},
		{name: "movie with intros off", file: lazyMarkerMovieFile(), intros: "false", wantMovies: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				analyzer := &fakePlaybackIntroAnalyzer{}
				handler := newLazyMarkerTestHandler(tc.file, analyzer, nil)
				handler.MarkerLazyContext = t.Context()
				handler.SettingsRepo = testPlaybackSettingsRepo{values: map[string]string{
					markers.SettingLazyPlayback:  "true",
					markers.SettingMode:          "local",
					markers.SettingDetectIntros:  tc.intros,
					markers.SettingDetectCredits: tc.credits,
				}}

				handler.maybeQueueLazyPlaybackMarkers(t.Context(), &playback.Session{ID: "session-1"}, tc.file)
				synctest.Wait()

				if got := analyzer.requestedKinds(); !slices.Equal(got, tc.wantKinds) {
					t.Fatalf("episode analyses = %+v, want %+v", got, tc.wantKinds)
				}
				analyzer.mu.Lock()
				movies := len(analyzer.movieFiles)
				analyzer.mu.Unlock()
				if movies != tc.wantMovies {
					t.Fatalf("movie analyses = %d, want %d", movies, tc.wantMovies)
				}
			})
		})
	}
}

func TestMissingLocalMarkersPerKind(t *testing.T) {
	marker := func(v float64) *float64 { return &v }
	type kinds = intromarkers.EpisodeMarkerKinds
	cases := []struct {
		name      string
		file      *models.MediaFile
		isEpisode bool
		want      kinds
	}{
		{"episode without markers", &models.MediaFile{}, true, kinds{Intro: true, Credits: true}},
		{"episode with only an intro", &models.MediaFile{IntroStart: marker(0), IntroEnd: marker(60)}, true, kinds{Credits: true}},
		{"episode with only credits", &models.MediaFile{CreditsStart: marker(1700), CreditsEnd: marker(1800)}, true, kinds{Intro: true}},
		{"episode with both", &models.MediaFile{IntroStart: marker(0), IntroEnd: marker(60), CreditsStart: marker(1700), CreditsEnd: marker(1800)}, true, kinds{}},
		{"movie without markers", &models.MediaFile{}, false, kinds{Credits: true}},
		{"movie with only an intro", &models.MediaFile{IntroStart: marker(0), IntroEnd: marker(60)}, false, kinds{Credits: true}},
		{"movie with credits", &models.MediaFile{CreditsStart: marker(6500), CreditsEnd: marker(7000)}, false, kinds{}},
	}
	for _, tc := range cases {
		if got := missingLocalMarkers(tc.file, tc.isEpisode); got != tc.want {
			t.Errorf("%s: missingLocalMarkers = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// A played movie without credits gets local credits analysis of that file,
// with playback priority; movies never get local intros.
func TestMaybeQueueLazyPlaybackMarkersRunsMovieCredits(t *testing.T) {
	file := lazyMarkerMovieFile()
	analyzer := &fakePlaybackIntroAnalyzer{started: make(chan struct{}, 1)}
	handler := newLazyMarkerTestHandler(file, analyzer, nil)

	handler.maybeQueueLazyPlaybackMarkers(context.Background(), &playback.Session{ID: "session-1"}, file)

	select {
	case <-analyzer.started:
	case <-time.After(time.Second):
		t.Fatal("movie analysis did not start")
	}
	analyzer.mu.Lock()
	defer analyzer.mu.Unlock()
	if len(analyzer.movieFiles) != 1 || analyzer.movieFiles[0] != file.ID || !analyzer.interactive[0] || len(analyzer.kinds) != 0 {
		t.Fatalf("movie files %v (interactive %v), episode analyses %v; want one playback-priority analysis of file %d",
			analyzer.movieFiles, analyzer.interactive, analyzer.kinds, file.ID)
	}
}

func TestMaybeQueueLazyPlaybackMarkersBothModeFallsBackToLocalWithoutProviders(t *testing.T) {
	analyzer := &fakePlaybackIntroAnalyzer{started: make(chan struct{}, 1)}
	file := lazyMarkerTestFile()
	handler := newLazyMarkerTestHandler(file, analyzer, nil)
	handler.SettingsRepo = testPlaybackSettingsRepo{values: map[string]string{
		markers.SettingLazyPlayback: "true",
		markers.SettingMode:         "both",
	}}

	handler.maybeQueueLazyPlaybackMarkers(context.Background(), &playback.Session{ID: "session-1"}, file)

	select {
	case <-analyzer.started:
	case <-time.After(time.Second):
		t.Fatal("AnalyzeEpisode did not start")
	}
	if got := analyzer.callCount(); got != 1 {
		t.Fatalf("AnalyzeEpisode calls = %d, want 1", got)
	}
}

type playbackMarkerPopulationFunc func(context.Context, *models.MediaFile) (*models.MediaFile, bool, error)

func (f playbackMarkerPopulationFunc) Populate(ctx context.Context, file *models.MediaFile) (*models.MediaFile, bool, error) {
	return f(ctx, file)
}

func TestOnDemandPlaybackMarkersHonorsLocalAnalysisSetting(t *testing.T) {
	for _, lazy := range []string{"false", "true"} {
		t.Run(lazy, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				file := lazyMarkerTestFile()
				analyzer := &fakePlaybackIntroAnalyzer{}
				handler := newLazyMarkerTestHandler(file, analyzer, nil)
				handler.MarkerLazyContext = t.Context()
				handler.SettingsRepo = testPlaybackSettingsRepo{values: map[string]string{
					markers.SettingLazyPlayback:  lazy,
					markers.SettingMode:          "both",
					markers.SettingOnlineStorage: "on_demand",
				}}
				handler.MarkerRegistry = markers.NewRegistry(slog.Default())
				if err := handler.MarkerRegistry.Register(fakePlaybackMarkerProvider{}); err != nil {
					t.Fatal(err)
				}
				lookups := 0
				handler.MarkerPopulation = playbackMarkerPopulationFunc(func(_ context.Context, file *models.MediaFile) (*models.MediaFile, bool, error) {
					lookups++
					return file, false, nil
				})

				handler.maybeQueueLazyPlaybackMarkers(t.Context(), &playback.Session{ID: "session-1"}, file)
				synctest.Wait()

				if lookups != 1 {
					t.Fatalf("online lookups = %d, want 1", lookups)
				}
				wantLocal := 0
				if lazy == "true" {
					wantLocal = 1
				}
				if got := analyzer.callCount(); got != wantLocal {
					t.Fatalf("local analysis calls = %d, want %d", got, wantLocal)
				}
			})
		})
	}
}

func TestMaybeQueueLazyPlaybackMarkersDedupesInFlightFile(t *testing.T) {
	analyzer := &fakePlaybackIntroAnalyzer{started: make(chan struct{}, 1), release: make(chan struct{})}
	file := lazyMarkerTestFile()
	handler := newLazyMarkerTestHandler(file, analyzer, nil)

	session := &playback.Session{ID: "session-1"}
	handler.maybeQueueLazyPlaybackMarkers(context.Background(), session, file)
	handler.maybeQueueLazyPlaybackMarkers(context.Background(), session, file)
	time.Sleep(25 * time.Millisecond)

	if got := analyzer.callCount(); got != 1 {
		t.Fatalf("AnalyzeEpisode calls = %d, want 1", got)
	}
	close(analyzer.release)
}

func TestMaybeQueueLazyPlaybackMarkersOnlineModeWithProviderDoesNotRunLocalAnalyzer(t *testing.T) {
	analyzer := &fakePlaybackIntroAnalyzer{started: make(chan struct{}, 1)}
	file := lazyMarkerTestFile()
	handler := newLazyMarkerTestHandler(file, analyzer, nil)
	registry := markers.NewRegistry(slog.Default())
	if err := registry.Register(fakePlaybackMarkerProvider{}); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	handler.MarkerRegistry = registry
	handler.SettingsRepo = testPlaybackSettingsRepo{values: map[string]string{
		markers.SettingLazyPlayback: "true",
		markers.SettingMode:         "online",
	}}

	handler.maybeQueueLazyPlaybackMarkers(context.Background(), &playback.Session{ID: "session-1"}, file)

	select {
	case <-analyzer.started:
		t.Fatal("AnalyzeEpisode started, want online-only provider mode to skip local analyzer")
	case <-time.After(25 * time.Millisecond):
	}
	if got := analyzer.callCount(); got != 0 {
		t.Fatalf("AnalyzeEpisode calls = %d, want 0", got)
	}
}

func TestMaybeQueueLazyPlaybackMarkersAnalyzerSuccessWithoutMarkerDoesNotEmitUpdate(t *testing.T) {
	analyzer := &fakePlaybackIntroAnalyzer{started: make(chan struct{}, 1)}
	file := lazyMarkerTestFile()
	notifier := fakePlaybackMarkerNotifier{ch: make(chan *models.MediaFile, 1)}
	handler := newLazyMarkerTestHandler(file, analyzer, notifier)

	handler.maybeQueueLazyPlaybackMarkers(context.Background(), &playback.Session{ID: "session-1"}, file)

	select {
	case <-analyzer.started:
	case <-time.After(time.Second):
		t.Fatal("AnalyzeEpisode did not start")
	}
	select {
	case notified := <-notifier.ch:
		t.Fatalf("unexpected marker update: %#v", notified)
	case <-time.After(25 * time.Millisecond):
	}
}

func newLazyMarkerTestHandler(
	file *models.MediaFile,
	analyzer *fakePlaybackIntroAnalyzer,
	notifier PlaybackMarkerUpdateNotifier,
) *PlaybackHandler {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), &fakePlaybackMarkerFileResolver{file: file})
	handler.SettingsRepo = testPlaybackSettingsRepo{values: map[string]string{
		markers.SettingLazyPlayback: "true",
		markers.SettingMode:         "local",
	}}
	handler.IntroRepository = fakePlaybackIntroEligibility{eligible: true}
	handler.IntroAnalyzer = analyzer
	handler.MarkerUpdateNotifier = notifier
	handler.MarkerLazyContext = context.Background()
	return handler
}

func lazyMarkerMovieFile() *models.MediaFile {
	return &models.MediaFile{
		ID:            43,
		ContentID:     "movie-1",
		MediaFolderID: 7,
		Duration:      7200,
	}
}

func lazyMarkerTestFile() *models.MediaFile {
	return &models.MediaFile{
		ID:            42,
		EpisodeID:     "episode-1",
		MediaFolderID: 7,
		Duration:      1800,
	}
}

// onDemandOverlayRun plays file with on-demand online markers and local
// analysis both enabled. The online lookup returns view (an in-memory overlay,
// never saved); local analysis stores afterLocal. It returns every marker
// update players were sent and the kinds local analysis was asked for.
type onDemandOverlayRun struct {
	storage    string
	file       *models.MediaFile
	view       func(*models.MediaFile) *models.MediaFile
	afterLocal func(*models.MediaFile) *models.MediaFile
}

func (r onDemandOverlayRun) run(t *testing.T) ([]*models.MediaFile, *fakePlaybackIntroAnalyzer) {
	t.Helper()
	var notified []*models.MediaFile
	var analyzer *fakePlaybackIntroAnalyzer
	synctest.Test(t, func(t *testing.T) {
		resolver := &fakePlaybackMarkerFileResolver{file: r.file}
		analyzer = &fakePlaybackIntroAnalyzer{onCall: func() {
			if r.afterLocal != nil {
				stored, _ := resolver.GetByID(context.Background(), r.file.ID)
				resolver.setFile(r.afterLocal(stored))
			}
		}}
		notifier := fakePlaybackMarkerNotifier{ch: make(chan *models.MediaFile, 16)}
		handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), resolver)
		handler.SettingsRepo = testPlaybackSettingsRepo{values: map[string]string{
			markers.SettingLazyPlayback:  "true",
			markers.SettingMode:          "both",
			markers.SettingOnlineStorage: r.storage,
		}}
		handler.IntroRepository = fakePlaybackIntroEligibility{eligible: true}
		handler.IntroAnalyzer = analyzer
		handler.MarkerUpdateNotifier = notifier
		handler.MarkerLazyContext = t.Context()
		handler.MarkerRegistry = markers.NewRegistry(slog.Default())
		if err := handler.MarkerRegistry.Register(fakePlaybackMarkerProvider{}); err != nil {
			t.Fatal(err)
		}
		handler.MarkerPopulation = playbackMarkerPopulationFunc(func(ctx context.Context, file *models.MediaFile) (*models.MediaFile, bool, error) {
			stored, _ := resolver.GetByID(ctx, file.ID)
			return r.view(stored), true, nil
		})

		handler.maybeQueueLazyPlaybackMarkers(t.Context(), &playback.Session{ID: "session-1"}, r.file)
		synctest.Wait()
		close(notifier.ch)
		for file := range notifier.ch {
			notified = append(notified, file)
		}
	})
	return notified, analyzer
}

func withMarker(file *models.MediaFile, kind string, start, end float64, source string) *models.MediaFile {
	next := *file
	next.MarkerSegments = append(slices.Clone(file.MarkerSegments), models.MarkerSegment{Kind: kind, StartSeconds: start, EndSeconds: end})
	switch kind {
	case models.MarkerSegmentIntro:
		next.IntroStart, next.IntroEnd, next.IntroMarkersSource = &start, &end, &source
	case models.MarkerSegmentCredits:
		next.CreditsStart, next.CreditsEnd, next.CreditsMarkersSource = &start, &end, &source
	}
	return &next
}

// firstSegment returns the notified range of kind, or nil when the update
// would clear it.
func firstSegment(file *models.MediaFile, kind string) *models.MarkerSegment {
	for _, segment := range models.EffectiveMarkerSegments(file) {
		if segment.Kind == kind {
			return &segment
		}
	}
	return nil
}

func assertSegment(t *testing.T, label string, file *models.MediaFile, kind string, start, end float64) {
	t.Helper()
	got := firstSegment(file, kind)
	if got == nil || got.StartSeconds != start || got.EndSeconds != end {
		t.Fatalf("%s: %s = %+v, want %v-%v", label, kind, got, start, end)
	}
}

func assertRequestedKinds(t *testing.T, analyzer *fakePlaybackIntroAnalyzer, want intromarkers.EpisodeMarkerKinds) {
	t.Helper()
	if got := analyzer.requestedKinds(); len(got) != 1 || got[0] != want {
		t.Fatalf("local analysis kinds = %+v, want [%+v]", got, want)
	}
}

// An on-demand online intro is never saved. Every update players get keeps it,
// local analysis is asked only for the missing credits, and the final update
// carries the online intro with the local credits. The setting is read the way
// the marker lookup reads it, whatever its case.
func TestOnDemandPlaybackMarkersKeepOnlineIntroWhileLocalFindsCredits(t *testing.T) {
	for _, storage := range []string{"on_demand", " ON_DEMAND "} {
		t.Run(storage, func(t *testing.T) {
			notified, analyzer := onDemandOverlayRun{
				storage: storage,
				file:    lazyMarkerTestFile(),
				view: func(stored *models.MediaFile) *models.MediaFile {
					return withMarker(stored, models.MarkerSegmentIntro, 20, 80, models.MarkerSourceOnline)
				},
				afterLocal: func(stored *models.MediaFile) *models.MediaFile {
					return withMarker(stored, models.MarkerSegmentCredits, 1700, 1780, models.MarkerSourceScanner)
				},
			}.run(t)

			assertRequestedKinds(t, analyzer, intromarkers.EpisodeMarkerKinds{Credits: true})
			if len(notified) < 2 {
				t.Fatalf("marker updates = %d, want the online update and the local one", len(notified))
			}
			for i, file := range notified {
				assertSegment(t, fmt.Sprintf("update %d", i), file, models.MarkerSegmentIntro, 20, 80)
			}
			final := notified[len(notified)-1]
			assertSegment(t, "final update", final, models.MarkerSegmentCredits, 1700, 1780)
			if final.IntroMarkersSource == nil || *final.IntroMarkersSource != models.MarkerSourceOnline {
				t.Fatalf("final intro source = %v, want online", final.IntroMarkersSource)
			}
		})
	}
}

// A movie in on-demand mode keeps its online intro while local analysis
// finds its credits.
func TestOnDemandPlaybackMarkersKeepOnlineIntroForMovies(t *testing.T) {
	notified, analyzer := onDemandOverlayRun{
		storage: "on_demand",
		file:    lazyMarkerMovieFile(),
		view: func(stored *models.MediaFile) *models.MediaFile {
			return withMarker(stored, models.MarkerSegmentIntro, 30, 90, models.MarkerSourceOnline)
		},
		afterLocal: func(stored *models.MediaFile) *models.MediaFile {
			return withMarker(stored, models.MarkerSegmentCredits, 6800, 7100, models.MarkerSourceScanner)
		},
	}.run(t)

	if len(analyzer.movieFiles) != 1 || len(analyzer.kinds) != 0 {
		t.Fatalf("movie analyses %v, episode analyses %v; want one movie analysis", analyzer.movieFiles, analyzer.kinds)
	}
	for i, file := range notified {
		assertSegment(t, fmt.Sprintf("update %d", i), file, models.MarkerSegmentIntro, 30, 90)
	}
	assertSegment(t, "final update", notified[len(notified)-1], models.MarkerSegmentCredits, 6800, 7100)
}

// Online credits without an intro send local analysis after the intro only,
// and the final update keeps the online credits.
func TestOnDemandPlaybackMarkersKeepOnlineCreditsWhileLocalFindsIntro(t *testing.T) {
	notified, analyzer := onDemandOverlayRun{
		storage: "on_demand",
		file:    lazyMarkerTestFile(),
		view: func(stored *models.MediaFile) *models.MediaFile {
			return withMarker(stored, models.MarkerSegmentCredits, 1700, 1780, models.MarkerSourceOnline)
		},
		afterLocal: func(stored *models.MediaFile) *models.MediaFile {
			return withMarker(stored, models.MarkerSegmentIntro, 12, 75, models.MarkerSourceScanner)
		},
	}.run(t)

	assertRequestedKinds(t, analyzer, intromarkers.EpisodeMarkerKinds{Intro: true})
	for i, file := range notified {
		assertSegment(t, fmt.Sprintf("update %d", i), file, models.MarkerSegmentCredits, 1700, 1780)
	}
	assertSegment(t, "final update", notified[len(notified)-1], models.MarkerSegmentIntro, 12, 75)
}

// A manual marker saved while local analysis ran outranks the online overlay.
func TestOnDemandPlaybackMarkersKeepManualMarkersOverOnline(t *testing.T) {
	notified, _ := onDemandOverlayRun{
		storage: "on_demand",
		file:    lazyMarkerTestFile(),
		view: func(stored *models.MediaFile) *models.MediaFile {
			return withMarker(stored, models.MarkerSegmentIntro, 20, 80, models.MarkerSourceOnline)
		},
		afterLocal: func(stored *models.MediaFile) *models.MediaFile {
			edited := withMarker(stored, models.MarkerSegmentIntro, 5, 45, models.MarkerSourceManual)
			return withMarker(edited, models.MarkerSegmentCredits, 1700, 1780, models.MarkerSourceScanner)
		},
	}.run(t)

	final := notified[len(notified)-1]
	assertSegment(t, "final update", final, models.MarkerSegmentIntro, 5, 45)
	assertSegment(t, "final update", final, models.MarkerSegmentCredits, 1700, 1780)
}

// Stored mode saves what the lookup found, so the stored row stays the whole
// answer: nothing from the lookup is laid over it.
func TestStoredPlaybackMarkersUseStoredRowOnly(t *testing.T) {
	notified, analyzer := onDemandOverlayRun{
		storage: "stored",
		file:    lazyMarkerTestFile(),
		view: func(stored *models.MediaFile) *models.MediaFile {
			// The lookup's intro did not reach the stored row (cleared in
			// between), so later reloads do not carry it.
			return withMarker(stored, models.MarkerSegmentIntro, 20, 80, models.MarkerSourceOnline)
		},
		afterLocal: func(stored *models.MediaFile) *models.MediaFile {
			return withMarker(stored, models.MarkerSegmentCredits, 1700, 1780, models.MarkerSourceScanner)
		},
	}.run(t)

	assertRequestedKinds(t, analyzer, intromarkers.EpisodeMarkerKinds{Intro: true, Credits: true})
	final := notified[len(notified)-1]
	if got := firstSegment(final, models.MarkerSegmentIntro); got != nil {
		t.Fatalf("final intro = %+v, want the stored row without the lookup's intro", got)
	}
	assertSegment(t, "final update", final, models.MarkerSegmentCredits, 1700, 1780)
}
