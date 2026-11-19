package handlers

import (
	"context"
	"log/slog"
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

type playbackMarkerPopulationFunc func(context.Context, *models.MediaFile) (*models.MediaFile, bool, error)

func (f playbackMarkerPopulationFunc) Populate(ctx context.Context, file *models.MediaFile) (*models.MediaFile, bool, error) {
	return f(ctx, file)
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

func newLazyMarkerTestHandler(
	file *models.MediaFile,
	analyzer *fakePlaybackIntroAnalyzer,
	notifier PlaybackMarkerUpdateNotifier,
) *PlaybackHandler {
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0), &fakePlaybackMarkerFileResolver{file: file})
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
		handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0), resolver)
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
