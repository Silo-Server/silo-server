package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/intromarkers"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeIntroAnalyzer struct {
	started chan string
	release chan struct{}
	summary intromarkers.RunSummary
	err     error
	// kinds receives the kinds of each episode analysis.
	kinds chan intromarkers.EpisodeMarkerKinds
	// movies receives the content IDs of movie analyses.
	movies chan string
}

func (f *fakeIntroAnalyzer) AnalyzeMovie(ctx context.Context, contentID string) (intromarkers.RunSummary, error) {
	if f.movies != nil {
		f.movies <- contentID
	}
	return f.analyze(ctx, "")
}

func (f *fakeIntroAnalyzer) AnalyzeEpisodeKinds(ctx context.Context, episodeID string, kinds intromarkers.EpisodeMarkerKinds) (intromarkers.RunSummary, error) {
	if f.kinds != nil {
		f.kinds <- kinds
	}
	return f.analyze(ctx, episodeID)
}

func (f *fakeIntroAnalyzer) analyze(ctx context.Context, episodeID string) (intromarkers.RunSummary, error) {
	if f.started != nil && episodeID != "" {
		f.started <- episodeID
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return intromarkers.RunSummary{}, ctx.Err()
		}
	}
	if f.err != nil {
		return intromarkers.RunSummary{}, f.err
	}
	if f.summary.FilesConsidered != 0 {
		return f.summary, nil
	}
	return intromarkers.RunSummary{FilesConsidered: 1}, nil
}

type fakeIntroEligibility struct {
	result *intromarkers.MarkerItemEligibility
	err    error
}

func (f fakeIntroEligibility) MarkerItemEligibility(context.Context, string) (*intromarkers.MarkerItemEligibility, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

type fakeMarkerSettings struct {
	values map[string]string
	err    error
}

func (f fakeMarkerSettings) Get(_ context.Context, key string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.values[key], nil
}

func (f fakeMarkerSettings) GetMany(_ context.Context, keys ...string) (map[string]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := f.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

type fakeAdminIntroFileResolver struct {
	files  []*models.MediaFile
	err    error
	called chan string
	// byContent answers GetByContentID.
	byContent map[string][]*models.MediaFile
}

func (f fakeAdminIntroFileResolver) GetByContentID(_ context.Context, contentID string) ([]*models.MediaFile, error) {
	if f.called != nil {
		f.called <- contentID
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.byContent[contentID], nil
}

func (f fakeAdminIntroFileResolver) GetByEpisodeID(_ context.Context, episodeID string) ([]*models.MediaFile, error) {
	if f.called != nil {
		f.called <- episodeID
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.files, nil
}

type fakeAdminIntroMarkerNotifier struct {
	ch chan *models.MediaFile
}

type markerRefreshFunc func(context.Context, *models.MediaFile) (*models.MediaFile, bool, error)

func (f markerRefreshFunc) Refresh(ctx context.Context, file *models.MediaFile) (*models.MediaFile, bool, error) {
	return f(ctx, file)
}

func TestAdminMarkerRefreshOnlineDoesNotRequireLocalDetection(t *testing.T) {
	started := make(chan int, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	handler := NewAdminIntroHandler(nil, nil, ctx, nil)
	handler.Settings = fakeMarkerSettings{values: map[string]string{markers.SettingMode: string(markers.ModeOnline)}}
	handler.FileResolver = fakeAdminIntroFileResolver{files: []*models.MediaFile{{ID: 42, EpisodeID: "ep1"}}}
	handler.OnlineMarkers = markerRefreshFunc(func(ctx context.Context, file *models.MediaFile) (*models.MediaFile, bool, error) {
		started <- file.ID
		<-ctx.Done()
		return file, false, ctx.Err()
	})
	status, err := handler.RefreshEpisodeMarkers(t.Context(), "ep1", "refresh-v2")
	if err != nil || status != "queued" {
		t.Fatalf("refresh: status=%q err=%v", status, err)
	}
	select {
	case id := <-started:
		if id != 42 {
			t.Fatalf("refreshed file %d, want 42", id)
		}
	case <-time.After(time.Second):
		t.Fatal("online refresh did not start")
	}
	status, err = handler.RefreshEpisodeMarkers(t.Context(), "ep1", "refresh-v2")
	if err != nil || status != "already_running" {
		t.Fatalf("duplicate refresh: status=%q err=%v", status, err)
	}
}

// In both mode, an online intro without online credits still runs local
// analysis, which now finds credits.
func TestAdminMarkerRefreshBothRunsLocalForMissingCredits(t *testing.T) {
	analyzer := &fakeIntroAnalyzer{started: make(chan string, 1)}
	handler := NewAdminIntroHandler(analyzer, fakeIntroEligibility{result: &intromarkers.MarkerItemEligibility{
		ItemID: "ep1", HasMediaFiles: true, IntroDetectionEnabled: true,
	}}, t.Context(), nil)
	handler.Settings = fakeMarkerSettings{values: map[string]string{markers.SettingMode: string(markers.ModeBoth)}}
	handler.FileResolver = fakeAdminIntroFileResolver{files: []*models.MediaFile{{ID: 42, EpisodeID: "ep1"}}}
	handler.OnlineMarkers = markerRefreshFunc(func(_ context.Context, file *models.MediaFile) (*models.MediaFile, bool, error) {
		start, end := 0.0, 60.0
		refreshed := *file
		refreshed.IntroStart, refreshed.IntroEnd = &start, &end
		return &refreshed, true, nil
	})
	status, err := handler.RefreshEpisodeMarkers(t.Context(), "ep1", "refresh-v2")
	if err != nil || status != "queued" {
		t.Fatalf("refresh: status=%q err=%v", status, err)
	}
	select {
	case episodeID := <-analyzer.started:
		if episodeID != "ep1" {
			t.Fatalf("analyzed episode %q, want ep1", episodeID)
		}
	case <-time.After(time.Second):
		t.Fatal("local analysis did not run for an episode without credits")
	}
}

func (n fakeAdminIntroMarkerNotifier) MarkersUpdated(_ context.Context, file *models.MediaFile) {
	if n.ch == nil {
		return
	}
	n.ch <- file
}

func TestAdminIntroRedetectQueuesAndDedupsInFlightEpisode(t *testing.T) {
	analyzer := &fakeIntroAnalyzer{
		started: make(chan string, 1),
		release: make(chan struct{}),
	}
	handler := NewAdminIntroHandler(
		analyzer,
		fakeIntroEligibility{result: &intromarkers.MarkerItemEligibility{
			ItemID:                "ep1",
			HasMediaFiles:         true,
			IntroDetectionEnabled: true,
		}},
		context.Background(),
		nil,
	)
	handler.Settings = fakeMarkerSettings{values: map[string]string{markers.SettingMode: string(markers.ModeLocal)}}
	router := chi.NewRouter()
	router.Post("/admin/items/{id}/redetect-intro", handler.HandleRedetectEpisodeIntro)

	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/admin/items/ep1/redetect-intro", nil))
	if first.Code != http.StatusAccepted {
		t.Fatalf("expected first request 202, got %d: %s", first.Code, first.Body.String())
	}
	if status := decodeRedetectStatus(t, first); status != "queued" {
		t.Fatalf("expected queued, got %q", status)
	}

	select {
	case id := <-analyzer.started:
		if id != "ep1" {
			t.Fatalf("expected analyzer to start ep1, got %q", id)
		}
	case <-time.After(time.Second):
		t.Fatal("analyzer did not start")
	}

	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/admin/items/ep1/redetect-intro", nil))
	if second.Code != http.StatusAccepted {
		t.Fatalf("expected second request 202, got %d: %s", second.Code, second.Body.String())
	}
	if status := decodeRedetectStatus(t, second); status != "already_running" {
		t.Fatalf("expected already_running, got %q", status)
	}

	close(analyzer.release)
}

func TestAdminIntroRedetectRejectsModesWithoutLocalAnalysis(t *testing.T) {
	for _, tt := range []struct {
		name string
		mode string
	}{
		{name: "off", mode: string(markers.ModeOff)},
		{name: "online", mode: string(markers.ModeOnline)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			analyzer := &fakeIntroAnalyzer{started: make(chan string, 1), release: make(chan struct{})}
			handler := NewAdminIntroHandler(
				analyzer,
				fakeIntroEligibility{result: &intromarkers.MarkerItemEligibility{
					ItemID:                "ep1",
					HasMediaFiles:         true,
					IntroDetectionEnabled: true,
				}},
				context.Background(),
				nil,
			)
			handler.Settings = fakeMarkerSettings{values: map[string]string{markers.SettingMode: tt.mode}}
			router := chi.NewRouter()
			router.Post("/admin/items/{id}/redetect-intro", handler.HandleRedetectEpisodeIntro)

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/items/ep1/redetect-intro", nil))
			if rec.Code != http.StatusConflict {
				t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
			}

			select {
			case id := <-analyzer.started:
				t.Fatalf("expected analyzer not to start, got %q", id)
			case <-time.After(50 * time.Millisecond):
			}
		})
	}
}

func TestAdminIntroRedetectRejectsItemsOtherThanEpisodesAndMovies(t *testing.T) {
	handler := NewAdminIntroHandler(
		&fakeIntroAnalyzer{started: make(chan string, 1), release: make(chan struct{})},
		fakeIntroEligibility{err: intromarkers.ErrMarkerItemNotFound},
		context.Background(),
		nil,
	)
	handler.Settings = fakeMarkerSettings{values: map[string]string{markers.SettingMode: string(markers.ModeLocal)}}
	router := chi.NewRouter()
	router.Post("/admin/items/{id}/redetect-intro", handler.HandleRedetectEpisodeIntro)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/items/series1/redetect-intro", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminIntroRedetectRejectsIntroDisabledLibrary(t *testing.T) {
	handler := NewAdminIntroHandler(
		&fakeIntroAnalyzer{started: make(chan string, 1), release: make(chan struct{})},
		fakeIntroEligibility{result: &intromarkers.MarkerItemEligibility{
			ItemID:                "ep1",
			HasMediaFiles:         true,
			IntroDetectionEnabled: false,
		}},
		context.Background(),
		nil,
	)
	handler.Settings = fakeMarkerSettings{values: map[string]string{markers.SettingMode: string(markers.ModeLocal)}}
	router := chi.NewRouter()
	router.Post("/admin/items/{id}/redetect-intro", handler.HandleRedetectEpisodeIntro)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/items/ep1/redetect-intro", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminIntroRedetectRejectsEpisodeWithoutMediaFiles(t *testing.T) {
	analyzer := &fakeIntroAnalyzer{started: make(chan string, 1), release: make(chan struct{})}
	handler := NewAdminIntroHandler(
		analyzer,
		fakeIntroEligibility{result: &intromarkers.MarkerItemEligibility{
			ItemID:                "ep1",
			HasMediaFiles:         false,
			IntroDetectionEnabled: false,
		}},
		context.Background(),
		nil,
	)
	handler.Settings = fakeMarkerSettings{values: map[string]string{markers.SettingMode: string(markers.ModeLocal)}}
	router := chi.NewRouter()
	router.Post("/admin/items/{id}/redetect-intro", handler.HandleRedetectEpisodeIntro)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/items/ep1/redetect-intro", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}

	select {
	case id := <-analyzer.started:
		t.Fatalf("expected analyzer not to start, got %q", id)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestAdminIntroRedetectRejectsMissingSettings(t *testing.T) {
	handler := NewAdminIntroHandler(
		&fakeIntroAnalyzer{started: make(chan string, 1), release: make(chan struct{})},
		fakeIntroEligibility{result: &intromarkers.MarkerItemEligibility{
			ItemID:                "ep1",
			HasMediaFiles:         true,
			IntroDetectionEnabled: true,
		}},
		context.Background(),
		nil,
	)
	router := chi.NewRouter()
	router.Post("/admin/items/{id}/redetect-intro", handler.HandleRedetectEpisodeIntro)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/items/ep1/redetect-intro", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminIntroRedetectNotifiesMarkedFilesAfterAnalyzerSuccess(t *testing.T) {
	start := 12.0
	end := 75.0
	analyzer := &fakeIntroAnalyzer{started: make(chan string, 1)}
	handler := NewAdminIntroHandler(
		analyzer,
		fakeIntroEligibility{result: &intromarkers.MarkerItemEligibility{
			ItemID:                "ep1",
			HasMediaFiles:         true,
			IntroDetectionEnabled: true,
		}},
		context.Background(),
		nil,
	)
	handler.Settings = fakeMarkerSettings{values: map[string]string{markers.SettingMode: string(markers.ModeLocal)}}
	handler.FileResolver = fakeAdminIntroFileResolver{files: []*models.MediaFile{
		{ID: 1, EpisodeID: "ep1", IntroStart: &start, IntroEnd: &end},
		{ID: 2, EpisodeID: "ep1"},
	}}
	notifier := fakeAdminIntroMarkerNotifier{ch: make(chan *models.MediaFile, 1)}
	handler.MarkerUpdateNotifier = notifier
	router := chi.NewRouter()
	router.Post("/admin/items/{id}/redetect-intro", handler.HandleRedetectEpisodeIntro)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/items/ep1/redetect-intro", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected request 202, got %d: %s", rec.Code, rec.Body.String())
	}

	select {
	case id := <-analyzer.started:
		if id != "ep1" {
			t.Fatalf("expected analyzer to start ep1, got %q", id)
		}
	case <-time.After(time.Second):
		t.Fatal("analyzer did not start")
	}

	select {
	case notified := <-notifier.ch:
		if notified.ID != 1 {
			t.Fatalf("notified file ID = %d, want 1", notified.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("expected marker update notification")
	}
	select {
	case notified := <-notifier.ch:
		t.Fatalf("unexpected second notification: %#v", notified)
	case <-time.After(25 * time.Millisecond):
	}
}

func TestAdminIntroRedetectDoesNotNotifyFilesStillMissingMarkers(t *testing.T) {
	analyzer := &fakeIntroAnalyzer{started: make(chan string, 1)}
	resolverCalled := make(chan string, 1)
	handler := NewAdminIntroHandler(
		analyzer,
		fakeIntroEligibility{result: &intromarkers.MarkerItemEligibility{
			ItemID:                "ep1",
			HasMediaFiles:         true,
			IntroDetectionEnabled: true,
		}},
		context.Background(),
		nil,
	)
	handler.Settings = fakeMarkerSettings{values: map[string]string{markers.SettingMode: string(markers.ModeLocal)}}
	handler.FileResolver = fakeAdminIntroFileResolver{
		files:  []*models.MediaFile{{ID: 1, EpisodeID: "ep1"}},
		called: resolverCalled,
	}
	notifier := fakeAdminIntroMarkerNotifier{ch: make(chan *models.MediaFile, 1)}
	handler.MarkerUpdateNotifier = notifier
	router := chi.NewRouter()
	router.Post("/admin/items/{id}/redetect-intro", handler.HandleRedetectEpisodeIntro)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/items/ep1/redetect-intro", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected request 202, got %d: %s", rec.Code, rec.Body.String())
	}
	select {
	case <-resolverCalled:
	case <-time.After(time.Second):
		t.Fatal("file resolver was not called")
	}
	select {
	case notified := <-notifier.ch:
		t.Fatalf("unexpected marker update: %#v", notified)
	case <-time.After(25 * time.Millisecond):
	}
}

func TestAdminIntroRedetectNotificationReloadFailureDoesNotFailRequest(t *testing.T) {
	analyzer := &fakeIntroAnalyzer{started: make(chan string, 1)}
	resolverCalled := make(chan string, 1)
	handler := NewAdminIntroHandler(
		analyzer,
		fakeIntroEligibility{result: &intromarkers.MarkerItemEligibility{
			ItemID:                "ep1",
			HasMediaFiles:         true,
			IntroDetectionEnabled: true,
		}},
		context.Background(),
		nil,
	)
	handler.Settings = fakeMarkerSettings{values: map[string]string{markers.SettingMode: string(markers.ModeLocal)}}
	handler.FileResolver = fakeAdminIntroFileResolver{
		err:    errors.New("reload failed"),
		called: resolverCalled,
	}
	handler.MarkerUpdateNotifier = fakeAdminIntroMarkerNotifier{ch: make(chan *models.MediaFile, 1)}
	router := chi.NewRouter()
	router.Post("/admin/items/{id}/redetect-intro", handler.HandleRedetectEpisodeIntro)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/items/ep1/redetect-intro", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected request 202, got %d: %s", rec.Code, rec.Body.String())
	}
	select {
	case <-resolverCalled:
	case <-time.After(time.Second):
		t.Fatal("file resolver was not called")
	}
}

func decodeRedetectStatus(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var response redetectIntroResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return response.Status
}

// A v2 refresh-markers of a movie in local mode runs the movie analysis and
// notifies the movie's own files that have markers, not its extras.
func TestAdminIntroRefreshAnalyzesMovieCredits(t *testing.T) {
	start, end := 6500.0, 7000.0
	analyzer := &fakeIntroAnalyzer{started: make(chan string, 1), movies: make(chan string, 1)}
	handler := NewAdminIntroHandler(analyzer, fakeIntroEligibility{result: &intromarkers.MarkerItemEligibility{
		ItemID: "movie1", Kind: intromarkers.MarkerItemMovie, HasMediaFiles: true, IntroDetectionEnabled: true,
	}}, context.Background(), nil)
	handler.Settings = fakeMarkerSettings{values: map[string]string{markers.SettingMode: string(markers.ModeLocal)}}
	handler.FileResolver = fakeAdminIntroFileResolver{byContent: map[string][]*models.MediaFile{"movie1": {
		{ID: 1, ContentID: "movie1", CreditsStart: &start, CreditsEnd: &end},
		{ID: 2, ContentID: "movie1", ExtraID: "extra1", CreditsStart: &start, CreditsEnd: &end},
	}}}
	notifier := fakeAdminIntroMarkerNotifier{ch: make(chan *models.MediaFile, 2)}
	handler.MarkerUpdateNotifier = notifier

	status, err := handler.RefreshEpisodeMarkers(t.Context(), "movie1", "refresh-v2")
	if err != nil || status != "queued" {
		t.Fatalf("refresh: status=%q err=%v", status, err)
	}
	select {
	case id := <-analyzer.movies:
		if id != "movie1" {
			t.Fatalf("analyzed movie %q, want movie1", id)
		}
	case <-time.After(time.Second):
		t.Fatal("movie analysis did not run")
	}
	select {
	case id := <-analyzer.started:
		t.Fatalf("episode analysis ran for %q", id)
	case notified := <-notifier.ch:
		if notified.ID != 1 {
			t.Fatalf("notified file %d, want the movie's own file 1", notified.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("expected a marker update for the movie file")
	}
	select {
	case notified := <-notifier.ch:
		t.Fatalf("unexpected notification for file %d", notified.ID)
	case <-time.After(25 * time.Millisecond):
	}
}

// In both mode, a movie without online credits runs local movie analysis;
// one whose online refresh found credits does not.
func TestAdminMarkerRefreshBothRunsLocalForMovieWithoutCredits(t *testing.T) {
	for _, online := range []bool{false, true} {
		analyzer := &fakeIntroAnalyzer{movies: make(chan string, 1)}
		handler := NewAdminIntroHandler(analyzer, fakeIntroEligibility{result: &intromarkers.MarkerItemEligibility{
			ItemID: "movie1", Kind: intromarkers.MarkerItemMovie, HasMediaFiles: true, IntroDetectionEnabled: true,
		}}, t.Context(), nil)
		handler.Settings = fakeMarkerSettings{values: map[string]string{markers.SettingMode: string(markers.ModeBoth)}}
		handler.FileResolver = fakeAdminIntroFileResolver{byContent: map[string][]*models.MediaFile{"movie1": {{ID: 7, ContentID: "movie1"}}}}
		refreshed := make(chan int, 1)
		handler.OnlineMarkers = markerRefreshFunc(func(_ context.Context, file *models.MediaFile) (*models.MediaFile, bool, error) {
			refreshed <- file.ID
			if !online {
				return file, false, nil
			}
			start, end := 6500.0, 7000.0
			withCredits := *file
			withCredits.CreditsStart, withCredits.CreditsEnd = &start, &end
			return &withCredits, true, nil
		})
		status, err := handler.RefreshEpisodeMarkers(t.Context(), "movie1", "refresh-v2")
		if err != nil || status != "queued" {
			t.Fatalf("online=%t refresh: status=%q err=%v", online, status, err)
		}
		if id := <-refreshed; id != 7 {
			t.Fatalf("online=%t refreshed file %d, want 7", online, id)
		}
		select {
		case id := <-analyzer.movies:
			if online {
				t.Fatalf("local analysis ran for %q although online credits were found", id)
			}
		case <-time.After(100 * time.Millisecond):
			if !online {
				t.Fatal("local analysis did not run for a movie without credits")
			}
		}
	}
}

// An online refresh that changes a movie's markers tells active playback,
// in online mode and in both mode when no local analysis follows; one that
// changes nothing does not.
func TestAdminMarkerRefreshNotifiesOnlineMovieChanges(t *testing.T) {
	for _, tc := range []struct {
		mode    markers.Mode
		changed bool
	}{{markers.ModeOnline, true}, {markers.ModeBoth, true}, {markers.ModeOnline, false}} {
		analyzer := &fakeIntroAnalyzer{movies: make(chan string, 1)}
		handler := NewAdminIntroHandler(analyzer, fakeIntroEligibility{result: &intromarkers.MarkerItemEligibility{
			ItemID: "movie1", Kind: intromarkers.MarkerItemMovie, HasMediaFiles: true, IntroDetectionEnabled: true,
		}}, t.Context(), nil)
		handler.Settings = fakeMarkerSettings{values: map[string]string{markers.SettingMode: string(tc.mode)}}
		handler.FileResolver = fakeAdminIntroFileResolver{byContent: map[string][]*models.MediaFile{"movie1": {{ID: 7, ContentID: "movie1"}}}}
		notifier := fakeAdminIntroMarkerNotifier{ch: make(chan *models.MediaFile, 2)}
		handler.MarkerUpdateNotifier = notifier
		done := make(chan struct{})
		handler.OnlineMarkers = markerRefreshFunc(func(_ context.Context, file *models.MediaFile) (*models.MediaFile, bool, error) {
			defer close(done)
			start, end := 6500.0, 7000.0
			withCredits := *file
			withCredits.CreditsStart, withCredits.CreditsEnd = &start, &end
			return &withCredits, tc.changed, nil
		})
		status, err := handler.RefreshEpisodeMarkers(t.Context(), "movie1", "refresh-v2")
		if err != nil || status != "queued" {
			t.Fatalf("%s: refresh: status=%q err=%v", tc.mode, status, err)
		}
		<-done
		if !tc.changed {
			waitForRefreshIdle(t, handler, "movie1")
			if len(notifier.ch) != 0 {
				t.Fatalf("%s: notified although the online refresh changed nothing", tc.mode)
			}
			continue
		}
		select {
		case notified := <-notifier.ch:
			if notified.ID != 7 || notified.CreditsStart == nil || *notified.CreditsStart != 6500 {
				t.Fatalf("%s: notified %+v, want file 7 with the online credits", tc.mode, notified)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s: no marker update for the online credits", tc.mode)
		}
		waitForRefreshIdle(t, handler, "movie1")
		if len(analyzer.movies) != 0 || len(notifier.ch) != 0 {
			t.Fatalf("%s: %d local analyses, %d more updates; want none", tc.mode, len(analyzer.movies), len(notifier.ch))
		}
	}
}

// stagedMovieFiles serves a movie's stored files: before, until local
// analysis has run, and after from then on.
type stagedMovieFiles struct {
	mu            sync.Mutex
	analyzed      bool
	before, after []*models.MediaFile
}

func (s *stagedMovieFiles) GetByContentID(context.Context, string) ([]*models.MediaFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.analyzed {
		return append([]*models.MediaFile(nil), s.after...), nil
	}
	return append([]*models.MediaFile(nil), s.before...), nil
}

func (s *stagedMovieFiles) GetByEpisodeID(context.Context, string) ([]*models.MediaFile, error) {
	return nil, nil
}

// stagingAnalyzer marks the staged files analyzed when a movie analysis runs.
type stagingAnalyzer struct {
	*fakeIntroAnalyzer
	files *stagedMovieFiles
}

func (a stagingAnalyzer) AnalyzeMovie(ctx context.Context, contentID string) (intromarkers.RunSummary, error) {
	a.files.mu.Lock()
	a.files.analyzed = true
	a.files.mu.Unlock()
	return a.fakeIntroAnalyzer.AnalyzeMovie(ctx, contentID)
}

// With on-demand online storage, a refresh of a two-version movie that finds
// online credits for one version and runs local analysis for the other keeps
// the first version's unsaved online credits in the update sent after the
// analysis, instead of a stored snapshot without them.
func TestAdminMarkerRefreshKeepsOnDemandOverlayAfterLocalAnalysis(t *testing.T) {
	introStart, introEnd := 0.0, 90.0
	manual := models.MarkerSourceManual
	withIntro := &models.MediaFile{ID: 1, ContentID: "movie1", IntroStart: &introStart, IntroEnd: &introEnd, IntroMarkersSource: &manual}
	localStart, localEnd := 6400.0, 6900.0
	scanner := models.MarkerSourceScanner
	files := &stagedMovieFiles{
		before: []*models.MediaFile{withIntro, {ID: 2, ContentID: "movie1"}},
		after: []*models.MediaFile{withIntro, {
			ID: 2, ContentID: "movie1", CreditsStart: &localStart, CreditsEnd: &localEnd, CreditsMarkersSource: &scanner,
		}},
	}
	analyzer := stagingAnalyzer{fakeIntroAnalyzer: &fakeIntroAnalyzer{movies: make(chan string, 1)}, files: files}
	handler := NewAdminIntroHandler(analyzer, fakeIntroEligibility{result: &intromarkers.MarkerItemEligibility{
		ItemID: "movie1", Kind: intromarkers.MarkerItemMovie, HasMediaFiles: true, IntroDetectionEnabled: true,
	}}, t.Context(), nil)
	handler.Settings = fakeMarkerSettings{values: map[string]string{
		markers.SettingMode:          string(markers.ModeBoth),
		markers.SettingOnlineStorage: string(markers.OnlineStorageOnDemand),
	}}
	handler.FileResolver = files
	notifier := fakeAdminIntroMarkerNotifier{ch: make(chan *models.MediaFile, 8)}
	handler.MarkerUpdateNotifier = notifier
	handler.OnlineMarkers = markerRefreshFunc(func(_ context.Context, file *models.MediaFile) (*models.MediaFile, bool, error) {
		if file.ID != 1 {
			return file, false, nil
		}
		start, end := 6500.0, 7000.0
		online := models.MarkerSourceOnline
		view := *file
		view.CreditsStart, view.CreditsEnd, view.CreditsMarkersSource = &start, &end, &online
		return &view, true, nil
	})

	status, err := handler.RefreshEpisodeMarkers(t.Context(), "movie1", "refresh-v2")
	if err != nil || status != "queued" {
		t.Fatalf("refresh: status=%q err=%v", status, err)
	}
	select {
	case <-analyzer.movies:
	case <-time.After(time.Second):
		t.Fatal("local analysis did not run for the version without online credits")
	}
	waitForRefreshIdle(t, handler, "movie1")
	close(notifier.ch)
	last := map[int]*models.MediaFile{}
	for notified := range notifier.ch {
		last[notified.ID] = notified
	}
	if got := last[1]; got == nil || got.CreditsStart == nil || *got.CreditsStart != 6500 || got.IntroEnd == nil {
		t.Fatalf("last update for version 1 lacks its manual intro or online credits at 6500: %v", markerSummary(got))
	}
	if got := last[2]; got == nil || got.CreditsStart == nil || *got.CreditsStart != 6400 {
		t.Fatalf("last update for version 2 lacks its local credits at 6400: %v", markerSummary(got))
	}
}

// markerSummary describes a file's intro and credits for a test failure.
func markerSummary(file *models.MediaFile) string {
	if file == nil {
		return "no update"
	}
	value := func(v *float64) string {
		if v == nil {
			return "none"
		}
		return strconv.FormatFloat(*v, 'f', -1, 64)
	}
	return "intro_end=" + value(file.IntroEnd) + " credits_start=" + value(file.CreditsStart)
}

// waitForRefreshIdle waits until no refresh of itemID is in flight.
func waitForRefreshIdle(t *testing.T, handler *AdminIntroHandler, itemID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		if !handler.itemRunning(itemID) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("refresh of %s still running", itemID)
		}
		time.Sleep(time.Millisecond)
	}
}

// An item that is neither an episode nor a movie, such as an audiobook, has
// no marker files to refresh online, even though it owns media files.
func TestAdminMarkerRefreshOnlineRejectsItemsOtherThanEpisodesAndMovies(t *testing.T) {
	handler := NewAdminIntroHandler(nil, fakeIntroEligibility{err: intromarkers.ErrMarkerItemNotFound}, t.Context(), nil)
	handler.Settings = fakeMarkerSettings{values: map[string]string{markers.SettingMode: string(markers.ModeOnline)}}
	handler.FileResolver = fakeAdminIntroFileResolver{byContent: map[string][]*models.MediaFile{"book1": {{ID: 9, ContentID: "book1"}}}}
	handler.OnlineMarkers = markerRefreshFunc(func(_ context.Context, file *models.MediaFile) (*models.MediaFile, bool, error) {
		t.Errorf("refreshed file %d of an item that is not an episode or a movie", file.ID)
		return file, false, nil
	})
	status, err := handler.RefreshEpisodeMarkers(t.Context(), "book1", "refresh-v2")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Fatalf("refresh: status=%q err=%v, want 409", status, err)
	}
}

// The v2 redetect-intro operation analyzes episode intros only, so a movie,
// which never gets intros, is rejected with the message it had before
// movies were analyzed, and never analyzed.
func TestAdminIntroV2RedetectRejectsMovies(t *testing.T) {
	analyzer := &fakeIntroAnalyzer{movies: make(chan string, 1)}
	handler := NewAdminIntroHandler(analyzer, fakeIntroEligibility{result: &intromarkers.MarkerItemEligibility{
		ItemID: "movie1", Kind: intromarkers.MarkerItemMovie, HasMediaFiles: true, IntroDetectionEnabled: true,
	}}, context.Background(), nil)
	handler.Settings = fakeMarkerSettings{values: map[string]string{markers.SettingMode: string(markers.ModeLocal)}}
	status, err := handler.RefreshEpisodeMarkers(t.Context(), "movie1", "redetect")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest || apiErr.Message != episodeMarkerMessages.wrongKind {
		t.Fatalf("redetect: status=%q err=%v, want 400 %q", status, err, episodeMarkerMessages.wrongKind)
	}
	select {
	case id := <-analyzer.movies:
		t.Fatalf("redetect analyzed movie %q", id)
	case <-time.After(25 * time.Millisecond):
	}
}

// The frozen /api/v1 endpoints analyze episodes only: a movie is rejected
// with the original message and never analyzed.
func TestAdminIntroV1RejectsMovies(t *testing.T) {
	analyzer := &fakeIntroAnalyzer{movies: make(chan string, 2)}
	handler := NewAdminIntroHandler(analyzer, fakeIntroEligibility{result: &intromarkers.MarkerItemEligibility{
		ItemID: "movie1", Kind: intromarkers.MarkerItemMovie, HasMediaFiles: true, IntroDetectionEnabled: true,
	}}, context.Background(), nil)
	handler.Settings = fakeMarkerSettings{values: map[string]string{markers.SettingMode: string(markers.ModeLocal)}}
	router := chi.NewRouter()
	router.Post("/admin/items/{id}/refresh-markers", handler.HandleRefreshEpisodeMarkers)
	router.Post("/admin/items/{id}/redetect-intro", handler.HandleRedetectEpisodeIntro)

	for _, path := range []string{"/admin/items/movie1/refresh-markers", "/admin/items/movie1/redetect-intro"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		var body struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: decode %q: %v", path, rec.Body.String(), err)
		}
		if rec.Code != http.StatusBadRequest || body.Message != "Item must be an episode" {
			t.Fatalf("%s: got %d %q, want 400 \"Item must be an episode\"", path, rec.Code, body.Message)
		}
	}
	select {
	case id := <-analyzer.movies:
		t.Fatalf("v1 analyzed movie %q", id)
	case <-time.After(25 * time.Millisecond):
	}
}

// The frozen v1 routes and v2 redetect-intro analyze episode intros only,
// never credits; v2 refresh-markers in local mode analyzes both.
func TestAdminIntroEndpointsKeepTheirAnalysisScope(t *testing.T) {
	localHandler := func(analyzer *fakeIntroAnalyzer) *AdminIntroHandler {
		handler := NewAdminIntroHandler(analyzer, fakeIntroEligibility{result: &intromarkers.MarkerItemEligibility{
			ItemID: "ep1", Kind: intromarkers.MarkerItemEpisode, HasMediaFiles: true, IntroDetectionEnabled: true,
		}}, context.Background(), nil)
		handler.Settings = fakeMarkerSettings{values: map[string]string{markers.SettingMode: string(markers.ModeLocal)}}
		handler.FileResolver = fakeAdminIntroFileResolver{}
		return handler
	}
	receiveKinds := func(t *testing.T, analyzer *fakeIntroAnalyzer) intromarkers.EpisodeMarkerKinds {
		t.Helper()
		select {
		case kinds := <-analyzer.kinds:
			return kinds
		case <-time.After(time.Second):
			t.Fatal("episode analysis did not run")
			return intromarkers.EpisodeMarkerKinds{}
		}
	}

	intro := intromarkers.EpisodeMarkerKinds{Intro: true}
	for _, path := range []string{"/admin/items/ep1/refresh-markers", "/admin/items/ep1/redetect-intro"} {
		analyzer := &fakeIntroAnalyzer{kinds: make(chan intromarkers.EpisodeMarkerKinds, 1)}
		handler := localHandler(analyzer)
		router := chi.NewRouter()
		router.Post("/admin/items/{id}/refresh-markers", handler.HandleRefreshEpisodeMarkers)
		router.Post("/admin/items/{id}/redetect-intro", handler.HandleRedetectEpisodeIntro)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("v1 %s: %d %s", path, rec.Code, rec.Body.String())
		}
		if got := receiveKinds(t, analyzer); got != intro {
			t.Fatalf("v1 %s analyzed %+v, want intros only", path, got)
		}
	}
	for _, tc := range []struct {
		action string
		want   intromarkers.EpisodeMarkerKinds
	}{
		{"redetect", intro},
		{"refresh-v2", intromarkers.EpisodeMarkerKinds{Intro: true, Credits: true}},
	} {
		analyzer := &fakeIntroAnalyzer{kinds: make(chan intromarkers.EpisodeMarkerKinds, 1)}
		status, err := localHandler(analyzer).RefreshEpisodeMarkers(t.Context(), "ep1", tc.action)
		if err != nil || status != "queued" {
			t.Fatalf("v2 %s: status=%q err=%v", tc.action, status, err)
		}
		if got := receiveKinds(t, analyzer); got != tc.want {
			t.Fatalf("v2 %s analyzed %+v, want %+v", tc.action, got, tc.want)
		}
	}
}

// redetectHandler returns a handler in local mode whose eligibility reports
// item as an eligible item of kind.
func redetectHandler(analyzer *fakeIntroAnalyzer, item, kind string) *AdminIntroHandler {
	handler := NewAdminIntroHandler(analyzer, fakeIntroEligibility{result: &intromarkers.MarkerItemEligibility{
		ItemID: item, Kind: kind, HasMediaFiles: true, IntroDetectionEnabled: true,
	}}, context.Background(), nil)
	handler.Settings = fakeMarkerSettings{values: map[string]string{markers.SettingMode: string(markers.ModeLocal)}}
	return handler
}

func requireAPIError(t *testing.T, err error, status int, field string) {
	t.Helper()
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != status || apiErr.Field != field {
		t.Fatalf("err = %v, want %d with field %q", err, status, field)
	}
}

// Re-detection of an episode runs only the kinds requested; all, the
// default, runs both.
func TestAdminRedetectItemMarkersRunsTheRequestedEpisodeKinds(t *testing.T) {
	for _, tc := range []struct {
		kind string
		want intromarkers.EpisodeMarkerKinds
	}{
		{RedetectMarkersIntro, intromarkers.EpisodeMarkerKinds{Intro: true}},
		{RedetectMarkersCredits, intromarkers.EpisodeMarkerKinds{Credits: true}},
		{RedetectMarkersAll, intromarkers.EpisodeMarkerKinds{Intro: true, Credits: true}},
		{"", intromarkers.EpisodeMarkerKinds{Intro: true, Credits: true}},
	} {
		analyzer := &fakeIntroAnalyzer{kinds: make(chan intromarkers.EpisodeMarkerKinds, 1), movies: make(chan string, 1)}
		handler := redetectHandler(analyzer, "ep1", intromarkers.MarkerItemEpisode)
		status, err := handler.RedetectItemMarkers(t.Context(), "ep1", tc.kind)
		if err != nil || status != "queued" {
			t.Fatalf("kind %q: status=%q err=%v", tc.kind, status, err)
		}
		select {
		case got := <-analyzer.kinds:
			if got != tc.want {
				t.Fatalf("kind %q analyzed %+v, want %+v", tc.kind, got, tc.want)
			}
		case id := <-analyzer.movies:
			t.Fatalf("kind %q ran movie analysis for %q", tc.kind, id)
		case <-time.After(time.Second):
			t.Fatalf("kind %q: episode analysis did not run", tc.kind)
		}
	}
}

// A movie has credits only: credits and all run the movie analysis, and
// intro takes episodes only, so a movie is rejected as not an episode
// without analyzing anything.
func TestAdminRedetectItemMarkersMovieCreditsOnly(t *testing.T) {
	for _, kind := range []string{RedetectMarkersCredits, RedetectMarkersAll} {
		analyzer := &fakeIntroAnalyzer{movies: make(chan string, 1), kinds: make(chan intromarkers.EpisodeMarkerKinds, 1)}
		handler := redetectHandler(analyzer, "movie1", intromarkers.MarkerItemMovie)
		status, err := handler.RedetectItemMarkers(t.Context(), "movie1", kind)
		if err != nil || status != "queued" {
			t.Fatalf("kind %q: status=%q err=%v", kind, status, err)
		}
		select {
		case id := <-analyzer.movies:
			if id != "movie1" {
				t.Fatalf("kind %q analyzed movie %q", kind, id)
			}
		case kinds := <-analyzer.kinds:
			t.Fatalf("kind %q ran episode analysis for %+v", kind, kinds)
		case <-time.After(time.Second):
			t.Fatalf("kind %q: movie analysis did not run", kind)
		}
	}

	analyzer := &fakeIntroAnalyzer{movies: make(chan string, 1), kinds: make(chan intromarkers.EpisodeMarkerKinds, 1)}
	handler := redetectHandler(analyzer, "movie1", intromarkers.MarkerItemMovie)
	_, err := handler.RedetectItemMarkers(t.Context(), "movie1", RedetectMarkersIntro)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest || apiErr.Message != episodeMarkerMessages.wrongKind {
		t.Fatalf("err = %v, want 400 %q", err, episodeMarkerMessages.wrongKind)
	}
	select {
	case id := <-analyzer.movies:
		t.Fatalf("intro re-detection analyzed movie %q", id)
	case <-time.After(25 * time.Millisecond):
	}
}

func TestAdminRedetectItemMarkersRejectsUnknownKind(t *testing.T) {
	analyzer := &fakeIntroAnalyzer{kinds: make(chan intromarkers.EpisodeMarkerKinds, 1)}
	handler := redetectHandler(analyzer, "ep1", intromarkers.MarkerItemEpisode)
	_, err := handler.RedetectItemMarkers(t.Context(), "ep1", "outro")
	requireAPIError(t, err, http.StatusBadRequest, "kind")
	select {
	case kinds := <-analyzer.kinds:
		t.Fatalf("unknown kind analyzed %+v", kinds)
	case <-time.After(25 * time.Millisecond):
	}
}

// Re-detection keeps the eligibility and mode checks of the other local
// analysis endpoints.
func TestAdminRedetectItemMarkersRejectsIneligibleItemsAndModes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		eligibility fakeIntroEligibility
		mode        markers.Mode
		status      int
	}{
		{"disabled library", fakeIntroEligibility{result: &intromarkers.MarkerItemEligibility{ItemID: "ep1", HasMediaFiles: true}}, markers.ModeLocal, http.StatusConflict},
		{"no files", fakeIntroEligibility{result: &intromarkers.MarkerItemEligibility{ItemID: "ep1", IntroDetectionEnabled: true}}, markers.ModeLocal, http.StatusConflict},
		{"not an episode or a movie", fakeIntroEligibility{err: intromarkers.ErrMarkerItemNotFound}, markers.ModeLocal, http.StatusBadRequest},
		{"mode off", fakeIntroEligibility{result: &intromarkers.MarkerItemEligibility{ItemID: "ep1", HasMediaFiles: true, IntroDetectionEnabled: true}}, markers.ModeOff, http.StatusConflict},
		{"mode online", fakeIntroEligibility{result: &intromarkers.MarkerItemEligibility{ItemID: "ep1", HasMediaFiles: true, IntroDetectionEnabled: true}}, markers.ModeOnline, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			analyzer := &fakeIntroAnalyzer{kinds: make(chan intromarkers.EpisodeMarkerKinds, 1)}
			handler := NewAdminIntroHandler(analyzer, tc.eligibility, context.Background(), nil)
			handler.Settings = fakeMarkerSettings{values: map[string]string{markers.SettingMode: string(tc.mode)}}
			_, err := handler.RedetectItemMarkers(t.Context(), "ep1", RedetectMarkersCredits)
			requireAPIError(t, err, tc.status, "")
			select {
			case kinds := <-analyzer.kinds:
				t.Fatalf("analyzed %+v", kinds)
			case <-time.After(25 * time.Millisecond):
			}
		})
	}
}

// One local analysis runs per item at a time. A request for kinds the running
// analysis, with what is queued behind it, already covers reports
// already_running; one for further kinds queues just those to run next.
func TestAdminRedetectItemMarkersQueuesKindsTheRunningAnalysisLacks(t *testing.T) {
	type kinds = intromarkers.EpisodeMarkerKinds
	analyzer := &fakeIntroAnalyzer{
		started: make(chan string, 2),
		release: make(chan struct{}),
		kinds:   make(chan intromarkers.EpisodeMarkerKinds, 2),
	}
	handler := redetectHandler(analyzer, "ep1", intromarkers.MarkerItemEpisode)
	status, err := handler.RedetectItemMarkers(t.Context(), "ep1", RedetectMarkersIntro)
	if err != nil || status != "queued" {
		t.Fatalf("first: status=%q err=%v", status, err)
	}
	if got := receiveEpisodeKinds(t, analyzer); got != (kinds{Intro: true}) {
		t.Fatalf("first analysis ran %+v, want intro", got)
	}
	<-analyzer.started

	for _, step := range []struct {
		name, kind string
		redetect   bool
		want       string
	}{
		{"intro again", RedetectMarkersIntro, false, "already_running"},
		{"redetect-intro", "", true, "already_running"},
		{"all", RedetectMarkersAll, false, "queued"},
		{"credits after all", RedetectMarkersCredits, false, "already_running"},
		{"all again", RedetectMarkersAll, false, "already_running"},
	} {
		if step.redetect {
			status, err = handler.RefreshEpisodeMarkers(t.Context(), "ep1", "redetect")
		} else {
			status, err = handler.RedetectItemMarkers(t.Context(), "ep1", step.kind)
		}
		if err != nil || status != step.want {
			t.Fatalf("%s while running: status=%q err=%v, want %q", step.name, status, err, step.want)
		}
	}

	close(analyzer.release)
	// The intro was running when all was asked for, so only credits run next.
	if got := receiveEpisodeKinds(t, analyzer); got != (kinds{Credits: true}) {
		t.Fatalf("queued analysis ran %+v, want credits only", got)
	}
	waitForRefreshIdle(t, handler, "ep1")
	select {
	case got := <-analyzer.kinds:
		t.Fatalf("ran a third analysis for %+v", got)
	default:
	}

	status, err = handler.RedetectItemMarkers(t.Context(), "ep1", RedetectMarkersIntro)
	if err != nil || status != "queued" {
		t.Fatalf("after idle: status=%q err=%v", status, err)
	}
	if got := receiveEpisodeKinds(t, analyzer); got != (kinds{Intro: true}) {
		t.Fatalf("analysis after idle ran %+v, want intro", got)
	}
}

// The frozen /api/v1 routes keep reporting already_running while any analysis
// of the episode runs, and queue nothing.
func TestAdminIntroV1RedetectDoesNotQueueBehindRunningAnalysis(t *testing.T) {
	analyzer := &fakeIntroAnalyzer{
		started: make(chan string, 1),
		release: make(chan struct{}),
		kinds:   make(chan intromarkers.EpisodeMarkerKinds, 2),
	}
	handler := redetectHandler(analyzer, "ep1", intromarkers.MarkerItemEpisode)
	router := chi.NewRouter()
	router.Post("/admin/items/{id}/redetect-intro", handler.HandleRedetectEpisodeIntro)

	if status, err := handler.RedetectItemMarkers(t.Context(), "ep1", RedetectMarkersCredits); err != nil || status != "queued" {
		t.Fatalf("credits: status=%q err=%v", status, err)
	}
	receiveEpisodeKinds(t, analyzer)
	<-analyzer.started

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/items/ep1/redetect-intro", nil))
	if rec.Code != http.StatusAccepted || decodeRedetectStatus(t, rec) != "already_running" {
		t.Fatalf("v1 redetect while running: %d %s", rec.Code, rec.Body.String())
	}

	close(analyzer.release)
	waitForRefreshIdle(t, handler, "ep1")
	select {
	case got := <-analyzer.kinds:
		t.Fatalf("v1 request queued an analysis for %+v", got)
	default:
	}
}

// An online refresh and local analysis of the same item exclude each other
// without queuing: the online refresh claims every kind, and itself does not
// wait behind local analysis.
func TestAdminMarkerRefreshOnlineAndLocalAnalysisDoNotQueue(t *testing.T) {
	newHandler := func(analyzer *fakeIntroAnalyzer, online markerRefreshFunc) *AdminIntroHandler {
		handler := redetectHandler(analyzer, "ep1", intromarkers.MarkerItemEpisode)
		handler.Settings.(fakeMarkerSettings).values[markers.SettingMode] = string(markers.ModeBoth)
		handler.FileResolver = fakeAdminIntroFileResolver{files: []*models.MediaFile{{ID: 42, EpisodeID: "ep1"}}}
		handler.OnlineMarkers = online
		return handler
	}

	t.Run("local analysis running", func(t *testing.T) {
		analyzer := &fakeIntroAnalyzer{started: make(chan string, 1), release: make(chan struct{})}
		handler := newHandler(analyzer, func(context.Context, *models.MediaFile) (*models.MediaFile, bool, error) {
			t.Error("online refresh ran while local analysis was running")
			return nil, false, nil
		})
		if status, err := handler.RedetectItemMarkers(t.Context(), "ep1", RedetectMarkersIntro); err != nil || status != "queued" {
			t.Fatalf("intro: status=%q err=%v", status, err)
		}
		<-analyzer.started
		if status, err := handler.RefreshEpisodeMarkers(t.Context(), "ep1", "refresh-v2"); err != nil || status != "already_running" {
			t.Fatalf("online refresh while running: status=%q err=%v", status, err)
		}
		close(analyzer.release)
		waitForRefreshIdle(t, handler, "ep1")
	})

	t.Run("online refresh running", func(t *testing.T) {
		analyzer := &fakeIntroAnalyzer{kinds: make(chan intromarkers.EpisodeMarkerKinds, 1)}
		onlineStarted, releaseOnline := make(chan struct{}), make(chan struct{})
		handler := newHandler(analyzer, func(_ context.Context, file *models.MediaFile) (*models.MediaFile, bool, error) {
			close(onlineStarted)
			<-releaseOnline
			start, end := 0.0, 60.0
			refreshed := *file
			refreshed.IntroStart, refreshed.IntroEnd = &start, &end
			refreshed.CreditsStart, refreshed.CreditsEnd = &start, &end
			return &refreshed, true, nil
		})
		if status, err := handler.RefreshEpisodeMarkers(t.Context(), "ep1", "refresh-v2"); err != nil || status != "queued" {
			t.Fatalf("online refresh: status=%q err=%v", status, err)
		}
		<-onlineStarted
		if status, err := handler.RedetectItemMarkers(t.Context(), "ep1", RedetectMarkersCredits); err != nil || status != "already_running" {
			t.Fatalf("credits while online refresh runs: status=%q err=%v", status, err)
		}
		close(releaseOnline)
		waitForRefreshIdle(t, handler, "ep1")
		select {
		case got := <-analyzer.kinds:
			t.Fatalf("ran local analysis for %+v after a complete online refresh", got)
		default:
		}
	})
}

// receiveEpisodeKinds waits for an episode analysis and returns its kinds.
func receiveEpisodeKinds(t *testing.T, analyzer *fakeIntroAnalyzer) intromarkers.EpisodeMarkerKinds {
	t.Helper()
	select {
	case kinds := <-analyzer.kinds:
		return kinds
	case <-time.After(time.Second):
		t.Fatal("episode analysis did not run")
		return intromarkers.EpisodeMarkerKinds{}
	}
}

// setDetectionKinds stores markers.detect_intros and markers.detect_credits
// in the handler's fake settings.
func setDetectionKinds(handler *AdminIntroHandler, intros, credits string) {
	values := handler.Settings.(fakeMarkerSettings).values
	values[markers.SettingDetectIntros] = intros
	values[markers.SettingDetectCredits] = credits
}

// settledAnalyses waits, inside a synctest bubble, until every goroutine the
// handler started has finished or is durably blocked, then returns the
// analyses the analyzer received: the kinds of an episode analysis, or the
// content ID of a movie analysis. Both are empty when nothing ran.
func settledAnalyses(analyzer *fakeIntroAnalyzer) (*intromarkers.EpisodeMarkerKinds, string) {
	synctest.Wait()
	var episode *intromarkers.EpisodeMarkerKinds
	select {
	case kinds := <-analyzer.kinds:
		episode = &kinds
	default:
	}
	var movie string
	select {
	case movie = <-analyzer.movies:
	default:
	}
	return episode, movie
}

// Re-detection runs the requested kinds the detection settings leave on,
// and rejects the request with a conflict naming the kinds when none remain.
func TestAdminRedetectItemMarkersFollowsDetectionSettings(t *testing.T) {
	type kinds = intromarkers.EpisodeMarkerKinds
	for _, tc := range []struct {
		name            string
		item, itemKind  string
		kind            string
		intros, credits string
		want            *kinds
		movie           bool
		conflict        string
	}{
		{name: "all with credits off", item: "ep1", itemKind: intromarkers.MarkerItemEpisode, kind: RedetectMarkersAll, credits: "false", want: &kinds{Intro: true}},
		{name: "all with intros off", item: "ep1", itemKind: intromarkers.MarkerItemEpisode, kind: RedetectMarkersAll, intros: "false", want: &kinds{Credits: true}},
		{name: "intro with intros off", item: "ep1", itemKind: intromarkers.MarkerItemEpisode, kind: RedetectMarkersIntro, intros: "false", conflict: markerKindsOffIntro},
		{name: "credits with credits off", item: "ep1", itemKind: intromarkers.MarkerItemEpisode, kind: RedetectMarkersCredits, credits: "false", conflict: markerKindsOffCredits},
		{name: "all with both off", item: "ep1", itemKind: intromarkers.MarkerItemEpisode, kind: RedetectMarkersAll, intros: "false", credits: "false", conflict: markerKindsOffBoth},
		{name: "movie with credits off", item: "movie1", itemKind: intromarkers.MarkerItemMovie, kind: RedetectMarkersAll, credits: "false", conflict: markerKindsOffCredits},
		{name: "movie with intros off", item: "movie1", itemKind: intromarkers.MarkerItemMovie, kind: RedetectMarkersAll, intros: "false", movie: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				analyzer := &fakeIntroAnalyzer{kinds: make(chan kinds, 1), movies: make(chan string, 1)}
				handler := redetectHandler(analyzer, tc.item, tc.itemKind)
				setDetectionKinds(handler, tc.intros, tc.credits)
				status, err := handler.RedetectItemMarkers(t.Context(), tc.item, tc.kind)
				if tc.conflict != "" {
					var apiErr *APIError
					if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Message != tc.conflict {
						t.Fatalf("status=%q err=%v, want 409 %q", status, err, tc.conflict)
					}
				} else if err != nil || status != "queued" {
					t.Fatalf("status=%q err=%v", status, err)
				}
				episode, movie := settledAnalyses(analyzer)
				if (episode == nil) != (tc.want == nil) || (episode != nil && *episode != *tc.want) {
					t.Fatalf("analyzed episode kinds %+v, want %+v", episode, tc.want)
				}
				if wantMovie := map[bool]string{true: tc.item}[tc.movie]; movie != wantMovie {
					t.Fatalf("analyzed movie %q, want %q", movie, wantMovie)
				}
			})
		})
	}
}

// refresh-markers in local mode runs the kinds the detection settings leave
// on and rejects an item with none left.
func TestAdminMarkerRefreshLocalFollowsDetectionSettings(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		analyzer := &fakeIntroAnalyzer{kinds: make(chan intromarkers.EpisodeMarkerKinds, 1)}
		handler := redetectHandler(analyzer, "ep1", intromarkers.MarkerItemEpisode)
		handler.FileResolver = fakeAdminIntroFileResolver{}
		setDetectionKinds(handler, "true", "false")
		status, err := handler.RefreshEpisodeMarkers(t.Context(), "ep1", "refresh-v2")
		if err != nil || status != "queued" {
			t.Fatalf("episode: status=%q err=%v", status, err)
		}
		if got, _ := settledAnalyses(analyzer); got == nil || *got != (intromarkers.EpisodeMarkerKinds{Intro: true}) {
			t.Fatalf("analyzed %+v, want intros only", got)
		}

		movieAnalyzer := &fakeIntroAnalyzer{movies: make(chan string, 1)}
		movie := redetectHandler(movieAnalyzer, "movie1", intromarkers.MarkerItemMovie)
		movie.FileResolver = fakeAdminIntroFileResolver{}
		setDetectionKinds(movie, "true", "false")
		_, err = movie.RefreshEpisodeMarkers(t.Context(), "movie1", "refresh-v2")
		requireAPIError(t, err, http.StatusConflict, "")
		if _, id := settledAnalyses(movieAnalyzer); id != "" {
			t.Fatalf("analyzed movie %q with credits detection off", id)
		}
	})
}

// refresh-markers in both mode still refreshes online markers, and local
// analysis fills only the missing kinds the detection settings leave on.
func TestAdminMarkerRefreshBothFollowsDetectionSettings(t *testing.T) {
	withIntro := func(file *models.MediaFile) *models.MediaFile {
		start, end := 0.0, 60.0
		refreshed := *file
		refreshed.IntroStart, refreshed.IntroEnd = &start, &end
		return &refreshed
	}
	for _, tc := range []struct {
		name            string
		kind            string
		online          func(*models.MediaFile) *models.MediaFile
		intros, credits string
		want            *intromarkers.EpisodeMarkerKinds
		movie           bool
	}{
		{name: "episode missing only credits, credits off", kind: intromarkers.MarkerItemEpisode, online: withIntro, credits: "false"},
		{name: "episode missing both, intros off", kind: intromarkers.MarkerItemEpisode, intros: "false", want: &intromarkers.EpisodeMarkerKinds{Credits: true}},
		{name: "movie, credits off", kind: intromarkers.MarkerItemMovie, credits: "false"},
		{name: "movie, intros off", kind: intromarkers.MarkerItemMovie, intros: "false", movie: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				analyzer := &fakeIntroAnalyzer{kinds: make(chan intromarkers.EpisodeMarkerKinds, 1), movies: make(chan string, 1)}
				handler := NewAdminIntroHandler(analyzer, fakeIntroEligibility{result: &intromarkers.MarkerItemEligibility{
					ItemID: "item1", Kind: tc.kind, HasMediaFiles: true, IntroDetectionEnabled: true,
				}}, t.Context(), nil)
				handler.Settings = fakeMarkerSettings{values: map[string]string{markers.SettingMode: string(markers.ModeBoth)}}
				setDetectionKinds(handler, tc.intros, tc.credits)
				file := &models.MediaFile{ID: 42, EpisodeID: "item1"}
				if tc.kind == intromarkers.MarkerItemMovie {
					file = &models.MediaFile{ID: 42, ContentID: "item1"}
				}
				handler.FileResolver = fakeAdminIntroFileResolver{files: []*models.MediaFile{file}, byContent: map[string][]*models.MediaFile{"item1": {file}}}
				refreshed := make(chan int, 1)
				handler.OnlineMarkers = markerRefreshFunc(func(_ context.Context, file *models.MediaFile) (*models.MediaFile, bool, error) {
					refreshed <- file.ID
					if tc.online != nil {
						return tc.online(file), true, nil
					}
					return file, false, nil
				})
				status, err := handler.RefreshEpisodeMarkers(t.Context(), "item1", "refresh-v2")
				if err != nil || status != "queued" {
					t.Fatalf("status=%q err=%v", status, err)
				}
				episode, movie := settledAnalyses(analyzer)
				select {
				case <-refreshed:
				default:
					t.Fatal("online refresh did not run")
				}
				if (episode == nil) != (tc.want == nil) || (episode != nil && *episode != *tc.want) {
					t.Fatalf("analyzed episode kinds %+v, want %+v", episode, tc.want)
				}
				if (movie != "") != tc.movie {
					t.Fatalf("analyzed movie %q, want movie analysis %v", movie, tc.movie)
				}
			})
		})
	}
}

// The v1 routes and v2 redetect-intro predate the detection settings and
// keep analyzing intros whatever they say.
func TestAdminIntroEndpointsIgnoreDetectionSettings(t *testing.T) {
	analyzer := &fakeIntroAnalyzer{kinds: make(chan intromarkers.EpisodeMarkerKinds, 1)}
	handler := redetectHandler(analyzer, "ep1", intromarkers.MarkerItemEpisode)
	setDetectionKinds(handler, "false", "false")
	status, err := handler.RefreshEpisodeMarkers(t.Context(), "ep1", "redetect")
	if err != nil || status != "queued" {
		t.Fatalf("redetect-intro: status=%q err=%v", status, err)
	}
	if got := receiveEpisodeKinds(t, analyzer); got != (intromarkers.EpisodeMarkerKinds{Intro: true}) {
		t.Fatalf("redetect-intro analyzed %+v, want intros", got)
	}
}
