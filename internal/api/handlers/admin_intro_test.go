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
}

func (f *fakeIntroAnalyzer) AnalyzeEpisode(ctx context.Context, episodeID string) (intromarkers.RunSummary, error) {
	if f.started != nil {
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
	result *intromarkers.EpisodeIntroEligibility
	err    error
}

func (f fakeIntroEligibility) EpisodeIntroEligibility(context.Context, string) (*intromarkers.EpisodeIntroEligibility, error) {
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
		fakeIntroEligibility{result: &intromarkers.EpisodeIntroEligibility{
			EpisodeID:             "ep1",
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
				fakeIntroEligibility{result: &intromarkers.EpisodeIntroEligibility{
					EpisodeID:             "ep1",
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

func TestAdminIntroRedetectRejectsNonEpisode(t *testing.T) {
	handler := NewAdminIntroHandler(
		&fakeIntroAnalyzer{started: make(chan string, 1), release: make(chan struct{})},
		fakeIntroEligibility{err: intromarkers.ErrEpisodeNotFound},
		context.Background(),
		nil,
	)
	handler.Settings = fakeMarkerSettings{values: map[string]string{markers.SettingMode: string(markers.ModeLocal)}}
	router := chi.NewRouter()
	router.Post("/admin/items/{id}/redetect-intro", handler.HandleRedetectEpisodeIntro)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/items/movie1/redetect-intro", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminIntroRedetectRejectsIntroDisabledLibrary(t *testing.T) {
	handler := NewAdminIntroHandler(
		&fakeIntroAnalyzer{started: make(chan string, 1), release: make(chan struct{})},
		fakeIntroEligibility{result: &intromarkers.EpisodeIntroEligibility{
			EpisodeID:             "ep1",
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
		fakeIntroEligibility{result: &intromarkers.EpisodeIntroEligibility{
			EpisodeID:             "ep1",
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
		fakeIntroEligibility{result: &intromarkers.EpisodeIntroEligibility{
			EpisodeID:             "ep1",
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
		fakeIntroEligibility{result: &intromarkers.EpisodeIntroEligibility{
			EpisodeID:             "ep1",
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
		fakeIntroEligibility{result: &intromarkers.EpisodeIntroEligibility{
			EpisodeID:             "ep1",
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
		fakeIntroEligibility{result: &intromarkers.EpisodeIntroEligibility{
			EpisodeID:             "ep1",
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
