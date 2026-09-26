package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
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
	// movies receives the content IDs of movie analyses.
	movies chan string
}

func (f *fakeIntroAnalyzer) AnalyzeMovie(ctx context.Context, contentID string) (intromarkers.RunSummary, error) {
	if f.movies != nil {
		f.movies <- contentID
	}
	return f.analyze(ctx, "")
}

func (f *fakeIntroAnalyzer) AnalyzeEpisode(ctx context.Context, episodeID string) (intromarkers.RunSummary, error) {
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

// A movie refresh runs the movie analysis and notifies the movie's own files
// that have markers, not its extras.
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

	status, err := handler.RefreshEpisodeMarkers(t.Context(), "movie1", "redetect")
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
