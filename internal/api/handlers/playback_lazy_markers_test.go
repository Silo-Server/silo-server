package handlers

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/intromarkers"
	"github.com/Silo-Server/silo-server/internal/markers"
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
}

func (a *fakePlaybackIntroAnalyzer) AnalyzeEpisode(context.Context, string) (intromarkers.RunSummary, error) {
	a.mu.Lock()
	a.calls++
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
			name: "intro already present",
			lazy: "true",
			mode: "local",
			file: func() *models.MediaFile {
				file := lazyMarkerTestFile()
				start := 10.0
				end := 60.0
				file.IntroStart = &start
				file.IntroEnd = &end
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

func lazyMarkerTestFile() *models.MediaFile {
	return &models.MediaFile{
		ID:            42,
		EpisodeID:     "episode-1",
		MediaFolderID: 7,
		Duration:      1800,
	}
}
