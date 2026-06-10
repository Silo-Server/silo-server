package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type testUserStoreProvider struct {
	store userstore.UserStore
}

func (p testUserStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (p testUserStoreProvider) Close() error { return nil }

type testPlaybackFileResolver struct {
	file *models.MediaFile
}

func (r testPlaybackFileResolver) GetByID(context.Context, int) (*models.MediaFile, error) {
	return r.file, nil
}

type mapPlaybackFileResolver struct {
	files map[int]*models.MediaFile
}

func (r mapPlaybackFileResolver) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	return r.files[id], nil
}

type testPlaybackFileVersionFetcher struct {
	byContent map[string][]*models.MediaFile
	byEpisode map[string][]*models.MediaFile
}

func (f testPlaybackFileVersionFetcher) GetByContentID(_ context.Context, id string) ([]*models.MediaFile, error) {
	return f.byContent[id], nil
}

func (f testPlaybackFileVersionFetcher) GetByEpisodeID(_ context.Context, id string) ([]*models.MediaFile, error) {
	return f.byEpisode[id], nil
}

type testPlaybackSettingsRepo struct {
	values map[string]string
}

func (r testPlaybackSettingsRepo) Get(_ context.Context, key string) (string, error) {
	return r.values[key], nil
}

type allowAllPlaybackItemAccess struct{}

func (allowAllPlaybackItemAccess) EnsureAccessible(
	context.Context,
	string,
	catalog.AccessFilter,
) error {
	return nil
}

type noopPlaybackAdminStore struct{}

func (noopPlaybackAdminStore) RecordHistory(context.Context, AdminPlaybackHistoryEntry) error {
	return nil
}

func (noopPlaybackAdminStore) DeleteSession(context.Context, string) error { return nil }

type testEpisodeLookup struct {
	episode *models.Episode
}

func (l testEpisodeLookup) GetByID(context.Context, string) (*models.Episode, error) {
	return l.episode, nil
}

type failingSessionManager struct{}

func (failingSessionManager) StartSession(int, string, int, playback.PlayMethod, bool) (*playback.Session, error) {
	return nil, errors.New("boom")
}

func (failingSessionManager) StartSessionWithFiles(int, string, int, int, playback.PlayMethod, bool) (*playback.Session, error) {
	return nil, errors.New("boom")
}

func (failingSessionManager) UpdateProgress(string, float64, bool) error { return nil }

func (failingSessionManager) UpdateAudioTrack(string, int, playback.PlayMethod) error { return nil }

func (failingSessionManager) UpdateStreamState(string, playback.SessionStreamState) error {
	return nil
}

func (failingSessionManager) TouchActivity(string) error { return nil }

func (failingSessionManager) BeginTransport(string) error { return nil }

func (failingSessionManager) EndTransport(string) error { return nil }

func (failingSessionManager) SetEffectiveMediaFileID(string, int) error { return nil }

func (failingSessionManager) SetTranscodeNodeURL(string, string) error { return nil }

func (failingSessionManager) SetWebSocket(string, bool) error { return nil }

func (failingSessionManager) SetRealtimeConnection(string, bool) error { return nil }

func (failingSessionManager) SetProgressPersistenceDisabled(string, bool) error { return nil }

func (failingSessionManager) StopSession(string) error { return nil }

func (failingSessionManager) GetSession(string) (*playback.Session, error) { return nil, nil }

func newPlaybackTestStore(t *testing.T) userstore.UserStore {
	t.Helper()

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	store := userdb.NewSQLiteUserStore(db)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-1", Name: "Main"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}

	return store
}

func newAuthorizedPlaybackContext() context.Context {
	ctx := context.Background()
	ctx = apimw.SetClaims(ctx, &auth.Claims{UserID: 1, Role: "user", TokenType: auth.TokenTypeAccess})
	return apimw.SetProfileID(ctx, "profile-1")
}

func withPlaybackRouteParam(req *http.Request, key, value string) *http.Request {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add(key, value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

func writePlaybackTestFFmpeg(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "fake-ffmpeg.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return path
}

func writePlaybackTestMediaFile(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir media path: %v", err)
	}
	if err := os.WriteFile(path, []byte("video"), 0o644); err != nil {
		t.Fatalf("write media file: %v", err)
	}
	return path
}

type recordingMissingMarker struct {
	ids []int
}

func (m *recordingMissingMarker) MarkMissing(_ context.Context, id int, _ time.Time) error {
	m.ids = append(m.ids, id)
	return nil
}

type recordingSessionSyncer struct {
	calls int
}

func (s *recordingSessionSyncer) SyncNow(context.Context) error {
	s.calls++
	return nil
}

type recordingPlaybackAdminStore struct {
	history []AdminPlaybackHistoryEntry
	deleted []string
}

func (s *recordingPlaybackAdminStore) RecordHistory(_ context.Context, entry AdminPlaybackHistoryEntry) error {
	s.history = append(s.history, entry)
	return nil
}

func (s *recordingPlaybackAdminStore) DeleteSession(_ context.Context, sessionID string) error {
	s.deleted = append(s.deleted, sessionID)
	return nil
}

func TestHandleStartPlayback_UsesExplicitZeroStartPositionInsteadOfStoredResume(t *testing.T) {
	store := newPlaybackTestStore(t)
	if err := store.SetProgress(context.Background(), "profile-1", "movie-1", 900, 3600, userstore.ProgressThresholds{}); err != nil {
		t.Fatalf("seed progress: %v", err)
	}

	file := &models.MediaFile{
		ID:         42,
		ContentID:  "movie-1",
		FilePath:   writePlaybackTestMediaFile(t, "movie.mkv"),
		Resolution: "1080p",
		Duration:   3600,
	}
	sessionMgr := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.StoreProvider = testUserStoreProvider{store: store}
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	tests := []struct {
		name         string
		body         string
		wantPosition float64
	}{
		{
			name:         "restore saved resume when start_position is omitted",
			body:         `{"file_id":42,"profile_id":"profile-1","codecs_video":["h264"],"codecs_audio":["aac"],"containers":["mp4"],"max_resolution":"2160p","hdr":false}`,
			wantPosition: 900,
		},
		{
			name:         "respect explicit zero start position",
			body:         `{"file_id":42,"profile_id":"profile-1","start_position":0,"codecs_video":["h264"],"codecs_audio":["aac"],"containers":["mp4"],"max_resolution":"2160p","hdr":false}`,
			wantPosition: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/v1/playback/start", strings.NewReader(tt.body))
			req = req.WithContext(newAuthorizedPlaybackContext())

			rr := httptest.NewRecorder()
			handler.HandleStartPlayback(rr, req)

			if rr.Code != 201 {
				t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
			}

			var resp playbackSessionResponse
			if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}

			if resp.Position != tt.wantPosition {
				t.Fatalf("position = %v, want %v", resp.Position, tt.wantPosition)
			}
		})
	}
}

func TestPlaybackSessionProgressPersistenceCanBeDisabled(t *testing.T) {
	store := newPlaybackTestStore(t)
	if err := store.SetProgress(context.Background(), "profile-1", "book-1", 500, 1200, userstore.ProgressThresholds{}); err != nil {
		t.Fatalf("seed progress: %v", err)
	}

	file := &models.MediaFile{
		ID:        42,
		ContentID: "book-1",
		FilePath:  writePlaybackTestMediaFile(t, "book.m4b"),
		Duration:  600,
	}
	sessionMgr := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.StoreProvider = testUserStoreProvider{store: store}
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	req := httptest.NewRequest("POST", "/api/v1/playback/start", strings.NewReader(`{"file_id":42,"profile_id":"profile-1","play_method":"direct","start_position":120,"disable_progress_persistence":true}`))
	req = req.WithContext(newAuthorizedPlaybackContext())

	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)
	if rr.Code != 201 {
		t.Fatalf("start status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp playbackSessionResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	progressReq := httptest.NewRequest("POST", "/api/v1/playback/"+resp.SessionID+"/progress", strings.NewReader(`{"position":240,"is_paused":false}`))
	progressReq = progressReq.WithContext(newAuthorizedPlaybackContext())
	progressReq = withPlaybackRouteParam(progressReq, "session_id", resp.SessionID)
	progressRR := httptest.NewRecorder()
	handler.HandleUpdateProgress(progressRR, progressReq)
	if progressRR.Code != 204 {
		t.Fatalf("progress status = %d, body = %s", progressRR.Code, progressRR.Body.String())
	}

	stopReq := httptest.NewRequest("DELETE", "/api/v1/playback/"+resp.SessionID, nil)
	stopReq = stopReq.WithContext(newAuthorizedPlaybackContext())
	stopReq = withPlaybackRouteParam(stopReq, "session_id", resp.SessionID)
	stopRR := httptest.NewRecorder()
	handler.HandleStopPlayback(stopRR, stopReq)
	if stopRR.Code != 204 {
		t.Fatalf("stop status = %d, body = %s", stopRR.Code, stopRR.Body.String())
	}

	progress, err := store.GetProgress(context.Background(), "profile-1", "book-1")
	if err != nil {
		t.Fatalf("get progress: %v", err)
	}
	if progress == nil || progress.PositionSeconds != 500 || progress.DurationSeconds != 1200 {
		t.Fatalf("progress after disabled session = %+v, want 500/1200", progress)
	}
}

func TestBuildTranscodeStartResponse_UnifiedSeekAnywhere(t *testing.T) {
	file := &models.MediaFile{Duration: 3600}

	copyResp := buildTranscodeStartResponse(
		transcodeStartRequest{
			SessionID:        "session-copy",
			SeekSeconds:      18.261,
			TargetCodecVideo: "copy",
		},
		file,
		nil,
		"/playback/transcode/session-copy/master.m3u8",
	)
	if copyResp.CanSeekAnywhere {
		t.Fatal("copy-mode response should require explicit restart seeks")
	}
	if copyResp.PlayerStartSeconds != 0 {
		t.Fatalf("copy-mode PlayerStartSeconds = %v, want 0", copyResp.PlayerStartSeconds)
	}
	if copyResp.StreamOriginSeconds != 18.261 {
		t.Fatalf("copy-mode StreamOriginSeconds = %v, want 18.261", copyResp.StreamOriginSeconds)
	}
	if copyResp.TimelineOffsetSeconds != 18.261 {
		t.Fatalf("copy-mode TimelineOffsetSeconds = %v, want 18.261", copyResp.TimelineOffsetSeconds)
	}

	encodedResp := buildTranscodeStartResponse(
		transcodeStartRequest{
			SessionID:        "session-encoded",
			SeekSeconds:      18.261,
			TargetCodecVideo: "h264",
		},
		file,
		nil,
		"/playback/transcode/session-encoded/master.m3u8",
	)
	if !encodedResp.CanSeekAnywhere {
		t.Fatal("encoded response should advertise seek-anywhere")
	}
	if encodedResp.PlayerStartSeconds != 18.261 {
		t.Fatalf("encoded PlayerStartSeconds = %v, want 18.261", encodedResp.PlayerStartSeconds)
	}
	if encodedResp.StreamOriginSeconds != 0 {
		t.Fatalf("encoded StreamOriginSeconds = %v, want 0", encodedResp.StreamOriginSeconds)
	}
	if encodedResp.TimelineOffsetSeconds != 0 {
		t.Fatalf("encoded TimelineOffsetSeconds = %v, want 0", encodedResp.TimelineOffsetSeconds)
	}
}

func TestHandleStartPlayback_PersistsSeriesPlaybackPreferenceForEpisodes(t *testing.T) {
	store := newPlaybackTestStore(t)
	file := &models.MediaFile{
		ID:         42,
		ContentID:  "movie-1",
		EpisodeID:  "episode-1",
		FilePath:   writePlaybackTestMediaFile(t, "episode.mkv"),
		Resolution: "1080p",
		CodecVideo: "h264",
		HDR:        false,
		Duration:   3600,
	}

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.StoreProvider = testUserStoreProvider{store: store}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.EpisodeLookup = testEpisodeLookup{episode: &models.Episode{ContentID: "episode-1", SeriesID: "series-1"}}

	req := httptest.NewRequest("POST", "/api/v1/playback/start", strings.NewReader(`{"file_id":42,"profile_id":"profile-1","codecs_video":["h264"],"codecs_audio":["aac"],"containers":["mp4"],"max_resolution":"2160p","hdr":false}`))
	req = req.WithContext(newAuthorizedPlaybackContext())

	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)

	if rr.Code != 201 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	pref, err := store.GetSeriesPlaybackPreference(context.Background(), "profile-1", "series-1")
	if err != nil {
		t.Fatalf("GetSeriesPlaybackPreference: %v", err)
	}
	if pref == nil {
		t.Fatal("expected series playback preference to be stored")
	}
	if pref.Resolution != "1080p" || pref.CodecVideo != "h264" || pref.HDR {
		t.Fatalf("stored pref = %+v, want 1080p/h264/false", pref)
	}
}

func TestHandleStartPlayback_DoesNotPersistSeriesPlaybackPreferenceOnFailure(t *testing.T) {
	store := newPlaybackTestStore(t)
	file := &models.MediaFile{
		ID:         42,
		ContentID:  "movie-1",
		EpisodeID:  "episode-1",
		FilePath:   writePlaybackTestMediaFile(t, "episode.mkv"),
		Resolution: "1080p",
		CodecVideo: "h264",
		HDR:        false,
		Duration:   3600,
	}

	handler := NewPlaybackHandler(failingSessionManager{}, testPlaybackFileResolver{file: file})
	handler.StoreProvider = testUserStoreProvider{store: store}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.EpisodeLookup = testEpisodeLookup{episode: &models.Episode{ContentID: "episode-1", SeriesID: "series-1"}}

	req := httptest.NewRequest("POST", "/api/v1/playback/start", strings.NewReader(`{"file_id":42,"profile_id":"profile-1","codecs_video":["h264"],"codecs_audio":["aac"],"containers":["mp4"],"max_resolution":"2160p","hdr":false}`))
	req = req.WithContext(newAuthorizedPlaybackContext())

	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)

	if rr.Code != 500 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	pref, err := store.GetSeriesPlaybackPreference(context.Background(), "profile-1", "series-1")
	if err != nil {
		t.Fatalf("GetSeriesPlaybackPreference: %v", err)
	}
	if pref != nil {
		t.Fatalf("expected no persisted preference on failure, got %+v", pref)
	}
}

func TestHandleChangeAudioTrack_PersistsSeriesAudioPreferenceSignature(t *testing.T) {
	store := newPlaybackTestStore(t)
	file := &models.MediaFile{
		ID:         42,
		ContentID:  "movie-1",
		EpisodeID:  "episode-1",
		FilePath:   writePlaybackTestMediaFile(t, "episode.mkv"),
		Resolution: "1080p",
		CodecVideo: "h264",
		CodecAudio: "aac",
		Container:  "mp4",
		Duration:   3600,
		AudioTracks: []models.AudioTrack{
			{Language: "eng", Codec: "aac", Layout: "stereo", Channels: 2, Title: "English Stereo", Default: true},
			{Language: "eng", Codec: "aac", Layout: "5.1", Channels: 6, Title: "English 5.1", EmbeddedTitle: "Surround"},
		},
	}

	sessionMgr := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.StoreProvider = testUserStoreProvider{store: store}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.EpisodeLookup = testEpisodeLookup{episode: &models.Episode{ContentID: "episode-1", SeriesID: "series-1"}}

	startReq := httptest.NewRequest(
		"POST",
		"/api/v1/playback/start",
		strings.NewReader(`{"file_id":42,"profile_id":"profile-1","codecs_video":["h264"],"codecs_audio":["aac"],"containers":["mp4"],"max_resolution":"2160p","hdr":false}`),
	)
	startReq = startReq.WithContext(newAuthorizedPlaybackContext())

	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, startReq)
	if startRR.Code != http.StatusCreated {
		t.Fatalf("start status = %d, body = %s", startRR.Code, startRR.Body.String())
	}

	var startResp playbackSessionResponse
	if err := json.NewDecoder(startRR.Body).Decode(&startResp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}

	changeReq := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/playback/"+startResp.SessionID+"/audio",
		strings.NewReader(`{"audio_track_index":1,"position":120}`),
	)
	changeReq = changeReq.WithContext(newAuthorizedPlaybackContext())
	changeReq = withPlaybackRouteParam(changeReq, "session_id", startResp.SessionID)

	changeRR := httptest.NewRecorder()
	handler.HandleChangeAudioTrack(changeRR, changeReq)
	if changeRR.Code != http.StatusOK {
		t.Fatalf("change status = %d, body = %s", changeRR.Code, changeRR.Body.String())
	}

	pref, err := store.GetAudioPreference(context.Background(), "profile-1", "series-1")
	if err != nil {
		t.Fatalf("GetAudioPreference: %v", err)
	}
	if pref == nil {
		t.Fatal("expected series audio preference to be stored")
	}
	if pref.AudioTrackIndex != 1 {
		t.Fatalf("AudioTrackIndex = %d, want 1", pref.AudioTrackIndex)
	}
	if pref.AudioLanguage != "eng" {
		t.Fatalf("AudioLanguage = %q, want eng", pref.AudioLanguage)
	}
	if pref.TrackSignature == nil {
		t.Fatal("expected audio track signature to be stored")
	}
	if pref.TrackSignature.Title != "English 5.1" {
		t.Fatalf("TrackSignature.Title = %q, want %q", pref.TrackSignature.Title, "English 5.1")
	}
	if pref.TrackSignature.EmbeddedTitle != "Surround" {
		t.Fatalf("TrackSignature.EmbeddedTitle = %q, want %q", pref.TrackSignature.EmbeddedTitle, "Surround")
	}
	if pref.TrackSignature.Codec != "aac" {
		t.Fatalf("TrackSignature.Codec = %q, want aac", pref.TrackSignature.Codec)
	}
	if pref.TrackSignature.Layout != "5.1" {
		t.Fatalf("TrackSignature.Layout = %q, want 5.1", pref.TrackSignature.Layout)
	}
	if pref.TrackSignature.Channels != 6 {
		t.Fatalf("TrackSignature.Channels = %d, want 6", pref.TrackSignature.Channels)
	}
	if pref.TrackSignature.Default {
		t.Fatal("TrackSignature.Default = true, want false")
	}
}

func TestBuildAdminHistoryEntry_UsesRequestedMediaFileID(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), mapPlaybackFileResolver{
		files: map[int]*models.MediaFile{
			42: {
				ID:         42,
				ContentID:  "movie-1",
				FilePath:   "/media/movie-4k.mkv",
				Resolution: "2160p",
				CodecVideo: "hevc",
				Duration:   3600,
			},
			99: {
				ID:         99,
				ContentID:  "movie-1",
				FilePath:   "/media/movie-1080p.mkv",
				Resolution: "1080p",
				CodecVideo: "h264",
				Duration:   3600,
			},
		},
	})
	handler.AdminStore = noopPlaybackAdminStore{}

	entry, err := handler.buildAdminHistoryEntry(context.Background(), &playback.Session{
		ID:                   "session-1",
		UserID:               1,
		ProfileID:            "profile-1",
		MediaFileID:          99,
		RequestedMediaFileID: 42,
		PlayMethod:           playback.PlayTranscode,
		Position:             120,
		StartedAt:            time.Now().Add(-2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("buildAdminHistoryEntry: %v", err)
	}
	if entry == nil {
		t.Fatal("expected admin history entry")
	}
	if entry.MediaFileID != 42 {
		t.Fatalf("MediaFileID = %d, want 42", entry.MediaFileID)
	}
	if entry.MediaItemID != "movie-1" {
		t.Fatalf("MediaItemID = %q, want movie-1", entry.MediaItemID)
	}
}

func TestPersistStopAndHistory_SkipsZeroProgressStops(t *testing.T) {
	store := newPlaybackTestStore(t)
	if err := store.SetProgress(context.Background(), "profile-1", "movie-1", 900, 3600, userstore.ProgressThresholds{}); err != nil {
		t.Fatalf("seed progress: %v", err)
	}

	file := &models.MediaFile{
		ID:         42,
		ContentID:  "movie-1",
		FilePath:   "/media/movie.mkv",
		Resolution: "1080p",
		Duration:   3600,
	}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.StoreProvider = testUserStoreProvider{store: store}

	handler.persistStopAndHistory(context.Background(), &playback.Session{
		ID:          "session-1",
		UserID:      1,
		ProfileID:   "profile-1",
		MediaFileID: 42,
		Position:    0,
	})

	progress, err := store.GetProgress(context.Background(), "profile-1", "movie-1")
	if err != nil {
		t.Fatalf("get progress: %v", err)
	}
	if progress == nil || progress.PositionSeconds != 900 {
		t.Fatalf("position after zero stop = %v, want 900", progress)
	}

	history, err := store.ListHistory(context.Background(), "profile-1", 10, 0)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("history len = %d, want 0", len(history))
	}
}

func TestPersistStopAndHistory_PersistsPositiveProgressStops(t *testing.T) {
	store := newPlaybackTestStore(t)

	file := &models.MediaFile{
		ID:         42,
		ContentID:  "movie-1",
		FilePath:   "/media/movie.mkv",
		Resolution: "1080p",
		Duration:   3600,
	}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.StoreProvider = testUserStoreProvider{store: store}

	handler.persistStopAndHistory(context.Background(), &playback.Session{
		ID:          "session-2",
		UserID:      1,
		ProfileID:   "profile-1",
		MediaFileID: 42,
		Position:    240,
	})

	progress, err := store.GetProgress(context.Background(), "profile-1", "movie-1")
	if err != nil {
		t.Fatalf("get progress: %v", err)
	}
	if progress == nil || progress.PositionSeconds != 240 {
		t.Fatalf("position after positive stop = %v, want 240", progress)
	}

	history, err := store.ListHistory(context.Background(), "profile-1", 10, 0)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history len = %d, want 1", len(history))
	}
	if history[0].Source != userstore.WatchHistorySourcePlayback {
		t.Fatalf("history source = %q, want %q", history[0].Source, userstore.WatchHistorySourcePlayback)
	}
}

func TestHandleStartPlayback_Recomputes4KFallbackAsRemux(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	requested := &models.MediaFile{
		ID:         42,
		ContentID:  "movie-1",
		FilePath:   writePlaybackTestMediaFile(t, "movie-4k.mkv"),
		Resolution: "2160p",
		CodecVideo: "hevc",
		CodecAudio: "eac3",
		Container:  "mkv",
		HDR:        true,
		Bitrate:    15000,
		Duration:   3600,
		AudioTracks: []models.AudioTrack{
			{Codec: "eac3", Default: true},
		},
	}
	effective := &models.MediaFile{
		ID:         99,
		ContentID:  "movie-1",
		FilePath:   writePlaybackTestMediaFile(t, "movie-1080p.mkv"),
		Resolution: "1080p",
		CodecVideo: "h264",
		CodecAudio: "eac3",
		Container:  "mkv",
		Bitrate:    8000,
		Duration:   3600,
		AudioTracks: []models.AudioTrack{
			{Codec: "eac3", Default: true},
		},
	}

	handler := NewPlaybackHandler(sessionMgr, mapPlaybackFileResolver{
		files: map[int]*models.MediaFile{
			42: requested,
			99: effective,
		},
	})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.SettingsRepo = testPlaybackSettingsRepo{
		values: map[string]string{"allow_4k_transcode": "false"},
	}
	handler.FileVersionFetcher = testPlaybackFileVersionFetcher{
		byContent: map[string][]*models.MediaFile{
			"movie-1": {requested, effective},
		},
	}

	req := httptest.NewRequest("POST", "/api/v1/playback/start", strings.NewReader(`{"file_id":42,"profile_id":"profile-1","codecs_video":["h264"],"codecs_audio":["aac"],"containers":["mp4"],"max_resolution":"2160p","hdr":false}`))
	req = req.WithContext(newAuthorizedPlaybackContext())

	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)

	if rr.Code != 201 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp playbackSessionResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.MediaFileID != 99 {
		t.Fatalf("MediaFileID = %d, want 99", resp.MediaFileID)
	}
	if resp.PlayMethod != string(playback.PlayRemux) {
		t.Fatalf("PlayMethod = %q, want %q", resp.PlayMethod, playback.PlayRemux)
	}

	session, err := sessionMgr.GetSession(resp.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if session.MediaFileID != 99 {
		t.Fatalf("session.MediaFileID = %d, want 99", session.MediaFileID)
	}
	if session.RequestedMediaFileID != 42 {
		t.Fatalf("session.RequestedMediaFileID = %d, want 42", session.RequestedMediaFileID)
	}
	if session.BasePlayMethod != playback.PlayRemux {
		t.Fatalf("session.BasePlayMethod = %q, want %q", session.BasePlayMethod, playback.PlayRemux)
	}
}

func TestHandleStartPlayback_Recomputes4KFallbackAsDirect(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	requested := &models.MediaFile{
		ID:         42,
		ContentID:  "movie-1",
		FilePath:   writePlaybackTestMediaFile(t, "movie-4k.mkv"),
		Resolution: "2160p",
		CodecVideo: "hevc",
		CodecAudio: "eac3",
		Container:  "mkv",
		HDR:        true,
		Bitrate:    15000,
		Duration:   3600,
		AudioTracks: []models.AudioTrack{
			{Codec: "eac3", Default: true},
		},
	}
	effective := &models.MediaFile{
		ID:         99,
		ContentID:  "movie-1",
		FilePath:   writePlaybackTestMediaFile(t, "movie-1080p.mp4"),
		Resolution: "1080p",
		CodecVideo: "h264",
		CodecAudio: "aac",
		Container:  "mp4",
		Bitrate:    8000,
		Duration:   3600,
		AudioTracks: []models.AudioTrack{
			{Codec: "aac", Default: true},
		},
	}

	handler := NewPlaybackHandler(sessionMgr, mapPlaybackFileResolver{
		files: map[int]*models.MediaFile{
			42: requested,
			99: effective,
		},
	})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.SettingsRepo = testPlaybackSettingsRepo{
		values: map[string]string{"allow_4k_transcode": "false"},
	}
	handler.FileVersionFetcher = testPlaybackFileVersionFetcher{
		byContent: map[string][]*models.MediaFile{
			"movie-1": {requested, effective},
		},
	}

	req := httptest.NewRequest("POST", "/api/v1/playback/start", strings.NewReader(`{"file_id":42,"profile_id":"profile-1","codecs_video":["h264"],"codecs_audio":["aac"],"containers":["mp4"],"max_resolution":"2160p","hdr":false}`))
	req = req.WithContext(newAuthorizedPlaybackContext())

	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)

	if rr.Code != 201 {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp playbackSessionResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.MediaFileID != 99 {
		t.Fatalf("MediaFileID = %d, want 99", resp.MediaFileID)
	}
	if resp.PlayMethod != string(playback.PlayDirect) {
		t.Fatalf("PlayMethod = %q, want %q", resp.PlayMethod, playback.PlayDirect)
	}
}

func TestHandleStartPlayback_AppleExplicitDirectPreservesSelectedAudio(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	file := &models.MediaFile{
		ID:         42,
		ContentID:  "movie-1",
		FilePath:   writePlaybackTestMediaFile(t, "movie-h264.mkv"),
		Resolution: "1080p",
		CodecVideo: "h264",
		CodecAudio: "aac",
		Container:  "mkv",
		Bitrate:    12000,
		Duration:   3600,
		AudioTracks: []models.AudioTrack{
			{Codec: "aac", Default: true},
			{Codec: "dts"},
		},
	}

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	req := httptest.NewRequest(
		"POST",
		"/api/v1/playback/start",
		strings.NewReader(`{"file_id":42,"profile_id":"profile-1","play_method":"direct","audio_track_index":1,"preserve_direct_audio_selection":true,"codecs_video":["h264","hevc"],"codecs_audio":["aac"],"containers":["mp4"],"max_resolution":"2160p","hdr":false}`),
	)
	req = req.WithContext(newAuthorizedPlaybackContext())

	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp playbackSessionResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.PlayMethod != string(playback.PlayDirect) {
		t.Fatalf("PlayMethod = %q, want %q", resp.PlayMethod, playback.PlayDirect)
	}
	if resp.AudioTrackIndex != 1 {
		t.Fatalf("AudioTrackIndex = %d, want 1", resp.AudioTrackIndex)
	}

	session, err := sessionMgr.GetSession(resp.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if session.TranscodeAudio {
		t.Fatal("TranscodeAudio = true, want false")
	}
	if session.AudioTrackIndex != 1 {
		t.Fatalf("session.AudioTrackIndex = %d, want 1", session.AudioTrackIndex)
	}
}

func TestHandleStartPlayback_ExplicitDirectPromotesSelectedAudioWithoutApplePreserveFlag(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	file := &models.MediaFile{
		ID:         42,
		ContentID:  "movie-1",
		FilePath:   writePlaybackTestMediaFile(t, "movie-h264.mkv"),
		Resolution: "1080p",
		CodecVideo: "h264",
		CodecAudio: "aac",
		Container:  "mkv",
		Bitrate:    12000,
		Duration:   3600,
		AudioTracks: []models.AudioTrack{
			{Codec: "aac", Default: true},
			{Codec: "dts"},
		},
	}

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	req := httptest.NewRequest(
		"POST",
		"/api/v1/playback/start",
		strings.NewReader(`{"file_id":42,"profile_id":"profile-1","play_method":"direct","audio_track_index":1,"codecs_video":["h264","hevc"],"codecs_audio":["aac"],"containers":["mp4"],"max_resolution":"2160p","hdr":false}`),
	)
	req = req.WithContext(newAuthorizedPlaybackContext())

	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp playbackSessionResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.PlayMethod != string(playback.PlayRemux) {
		t.Fatalf("PlayMethod = %q, want %q", resp.PlayMethod, playback.PlayRemux)
	}
	if resp.AudioTrackIndex != 1 {
		t.Fatalf("AudioTrackIndex = %d, want 1", resp.AudioTrackIndex)
	}

	session, err := sessionMgr.GetSession(resp.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !session.TranscodeAudio {
		t.Fatal("TranscodeAudio = false, want true for unsupported selected audio")
	}
	if session.AudioTrackIndex != 1 {
		t.Fatalf("session.AudioTrackIndex = %d, want 1", session.AudioTrackIndex)
	}
}

func TestHandleStartPlayback_AutoDirectIgnoresApplePreserveFlagForSelectedAudio(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	file := &models.MediaFile{
		ID:         42,
		ContentID:  "movie-1",
		FilePath:   writePlaybackTestMediaFile(t, "movie-h264.mp4"),
		Resolution: "1080p",
		CodecVideo: "h264",
		CodecAudio: "aac",
		Container:  "mp4",
		Bitrate:    12000,
		Duration:   3600,
		AudioTracks: []models.AudioTrack{
			{Codec: "aac", Default: true},
			{Codec: "dts"},
		},
	}

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	req := httptest.NewRequest(
		"POST",
		"/api/v1/playback/start",
		strings.NewReader(`{"file_id":42,"profile_id":"profile-1","audio_track_index":1,"preserve_direct_audio_selection":true,"codecs_video":["h264","hevc"],"codecs_audio":["aac"],"containers":["mp4"],"max_resolution":"2160p","hdr":false}`),
	)
	req = req.WithContext(newAuthorizedPlaybackContext())

	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var resp playbackSessionResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.PlayMethod != string(playback.PlayRemux) {
		t.Fatalf("PlayMethod = %q, want %q", resp.PlayMethod, playback.PlayRemux)
	}
	if resp.AudioTrackIndex != 1 {
		t.Fatalf("AudioTrackIndex = %d, want 1", resp.AudioTrackIndex)
	}

	session, err := sessionMgr.GetSession(resp.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !session.TranscodeAudio {
		t.Fatal("TranscodeAudio = false, want true for unsupported selected audio")
	}
	if session.AudioTrackIndex != 1 {
		t.Fatalf("session.AudioTrackIndex = %d, want 1", session.AudioTrackIndex)
	}
}

func TestHandleStartTranscode_PreservesRecomputedBaseMethodAfterFallback(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	requested := &models.MediaFile{
		ID:         42,
		ContentID:  "movie-1",
		FilePath:   writePlaybackTestMediaFile(t, "movie-4k.mkv"),
		Resolution: "2160p",
		CodecVideo: "hevc",
		CodecAudio: "eac3",
		Container:  "mkv",
		HDR:        true,
		Bitrate:    15000,
		Duration:   3600,
		AudioTracks: []models.AudioTrack{
			{Codec: "eac3", Default: true},
		},
	}
	effective := &models.MediaFile{
		ID:         99,
		ContentID:  "movie-1",
		FilePath:   writePlaybackTestMediaFile(t, "movie-1080p.mkv"),
		Resolution: "1080p",
		CodecVideo: "h264",
		CodecAudio: "eac3",
		Container:  "mkv",
		Bitrate:    8000,
		Duration:   3600,
		AudioTracks: []models.AudioTrack{
			{Codec: "eac3", Default: true},
		},
	}

	handler := NewPlaybackHandler(sessionMgr, mapPlaybackFileResolver{
		files: map[int]*models.MediaFile{
			42: requested,
			99: effective,
		},
	})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.SettingsRepo = testPlaybackSettingsRepo{
		values: map[string]string{"allow_4k_transcode": "false"},
	}
	handler.FileVersionFetcher = testPlaybackFileVersionFetcher{
		byContent: map[string][]*models.MediaFile{
			"movie-1": {requested, effective},
		},
	}

	req := httptest.NewRequest("POST", "/api/v1/playback/start", strings.NewReader(`{"file_id":42,"profile_id":"profile-1","codecs_video":["h264"],"codecs_audio":["aac"],"containers":["mp4"],"max_resolution":"2160p","hdr":false}`))
	req = req.WithContext(newAuthorizedPlaybackContext())

	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)
	if rr.Code != 201 {
		t.Fatalf("start status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var startResp playbackSessionResponse
	if err := json.NewDecoder(rr.Body).Decode(&startResp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	if startResp.PlayMethod != string(playback.PlayRemux) {
		t.Fatalf("start PlayMethod = %q, want %q", startResp.PlayMethod, playback.PlayRemux)
	}
	if err := sessionMgr.UpdateAudioTrack(startResp.SessionID, 1, playback.PlayRemux); err != nil {
		t.Fatalf("UpdateAudioTrack: %v", err)
	}

	var remoteStartRequests int
	var remoteStartReq transcodenode.TranscodeStartRequest
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/transcode/start" {
			t.Fatalf("unexpected remote request %s %s", r.Method, r.URL.Path)
		}
		remoteStartRequests++
		if err := json.NewDecoder(r.Body).Decode(&remoteStartReq); err != nil {
			t.Fatalf("decode remote start request: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer remote.Close()

	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{
		Name:       "transcode-1",
		Type:       nodepool.NodeTypeTranscode,
		URL:        remote.URL,
		Enabled:    true,
		Healthy:    true,
		ActiveJobs: 0,
	}})
	handler.NodePlanner = nodepool.NewPlanner(nodepool.NewProxyPool(), pool)

	transcodeReq := httptest.NewRequest(
		"POST",
		"/api/v1/playback/transcode/start",
		strings.NewReader(`{"session_id":"`+startResp.SessionID+`","seek_seconds":0,"target_resolution":"720p","target_codec_video":"h264","target_codec_audio":"aac","target_bitrate_kbps":2000,"segment_duration":2,"subtitle_track_index":-1,"subtitle_burn_in":false}`),
	)
	transcodeReq = transcodeReq.WithContext(newAuthorizedPlaybackContext())

	transcodeRR := httptest.NewRecorder()
	handler.HandleStartTranscode(transcodeRR, transcodeReq)

	if transcodeRR.Code != 202 {
		t.Fatalf("transcode status = %d, body = %s", transcodeRR.Code, transcodeRR.Body.String())
	}
	if remoteStartRequests != 1 {
		t.Fatalf("remote start requests = %d, want 1", remoteStartRequests)
	}
	if remoteStartReq.AudioTrackIndex != 1 {
		t.Fatalf("remote audio_track_index = %d, want 1", remoteStartReq.AudioTrackIndex)
	}

	session, err := sessionMgr.GetSession(startResp.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if session.PlayMethod != playback.PlayTranscode {
		t.Fatalf("session.PlayMethod = %q, want %q", session.PlayMethod, playback.PlayTranscode)
	}
	if session.BasePlayMethod != playback.PlayRemux {
		t.Fatalf("session.BasePlayMethod = %q, want %q", session.BasePlayMethod, playback.PlayRemux)
	}
}

func TestHandleStartTranscode_LocalPathPropagatesSelectedAudioTrack(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	filePath := writePlaybackTestMediaFile(t, "movie.mkv")

	file := &models.MediaFile{
		ID:         42,
		ContentID:  "movie-1",
		FilePath:   filePath,
		Resolution: "1080p",
		CodecVideo: "h264",
		CodecAudio: "aac",
		Container:  "mkv",
		Bitrate:    8000,
		Duration:   3600,
		AudioTracks: []models.AudioTrack{
			{Codec: "ac3", Default: true},
			{Codec: "aac"},
		},
	}

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.FFmpegPath = writePlaybackTestFFmpeg(t)
	handler.TranscodeDir = t.TempDir()

	req := httptest.NewRequest("POST", "/api/v1/playback/start", strings.NewReader(`{"file_id":42,"profile_id":"profile-1","audio_track_index":1,"codecs_video":["h264"],"codecs_audio":["aac"],"containers":["mp4"],"max_resolution":"2160p","hdr":false}`))
	req = req.WithContext(newAuthorizedPlaybackContext())

	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)
	if rr.Code != 201 {
		t.Fatalf("start status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var startResp playbackSessionResponse
	if err := json.NewDecoder(rr.Body).Decode(&startResp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}

	transcodeReq := httptest.NewRequest(
		"POST",
		"/api/v1/playback/transcode/start",
		strings.NewReader(`{"session_id":"`+startResp.SessionID+`","seek_seconds":0,"target_resolution":"720p","target_codec_video":"h264","target_codec_audio":"aac","target_bitrate_kbps":2000,"segment_duration":2,"subtitle_track_index":-1,"subtitle_burn_in":false}`),
	)
	transcodeReq = transcodeReq.WithContext(newAuthorizedPlaybackContext())

	transcodeRR := httptest.NewRecorder()
	handler.HandleStartTranscode(transcodeRR, transcodeReq)
	if transcodeRR.Code != 202 {
		t.Fatalf("transcode status = %d, body = %s", transcodeRR.Code, transcodeRR.Body.String())
	}

	transcodeSession := handler.getTranscodeSession(startResp.SessionID)
	if transcodeSession == nil {
		t.Fatal("expected local transcode session")
	}
	t.Cleanup(func() {
		_ = transcodeSession.Close()
	})
	if got := transcodeSession.Opts().AudioTrackIndex; got != 1 {
		t.Fatalf("local transcode audio track index = %d, want 1", got)
	}
}

func TestHandleStartTranscode_MPEG2SeekedCopyRemainsCopyVideo(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	filePath := writePlaybackTestMediaFile(t, "movie-mpeg2.mkv")
	file := &models.MediaFile{
		ID:         42,
		ContentID:  "movie-1",
		FilePath:   filePath,
		Resolution: "1080p",
		CodecVideo: "mpeg2video",
		CodecAudio: "dts",
		Container:  "mkv",
		Bitrate:    25000,
		Duration:   3600,
		AudioTracks: []models.AudioTrack{
			{Codec: "dts", Default: true},
		},
	}
	session, err := sessionMgr.StartSession(1, "profile-1", file.ID, playback.PlayRemux, true)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.FFmpegPath = writePlaybackTestFFmpeg(t)
	handler.TranscodeDir = t.TempDir()

	transcodeReq := httptest.NewRequest(
		"POST",
		"/api/v1/playback/transcode/start",
		strings.NewReader(`{"session_id":"`+session.ID+`","seek_seconds":18.261,"target_resolution":"1080p","target_codec_video":"copy","target_codec_audio":"aac","target_bitrate_kbps":0,"segment_duration":2,"subtitle_track_index":-1,"subtitle_burn_in":false}`),
	)
	transcodeReq = transcodeReq.WithContext(newAuthorizedPlaybackContext())

	transcodeRR := httptest.NewRecorder()
	handler.HandleStartTranscode(transcodeRR, transcodeReq)
	if transcodeRR.Code != 202 {
		t.Fatalf("transcode status = %d, body = %s", transcodeRR.Code, transcodeRR.Body.String())
	}

	transcodeSession := handler.getTranscodeSession(session.ID)
	if transcodeSession == nil {
		t.Fatal("expected local transcode session")
	}
	t.Cleanup(func() {
		_ = transcodeSession.Close()
	})
	opts := transcodeSession.Opts()
	if got := opts.TargetCodecVideo; got != "copy" {
		t.Fatalf("mpeg2 seeked copy target video codec = %q, want copy", got)
	}
	if got := opts.SourceVideoCodec; got != "mpeg2video" {
		t.Fatalf("mpeg2 seeked copy source video codec = %q, want mpeg2video", got)
	}
	if got := opts.TargetCodecAudio; got != "aac" {
		t.Fatalf("mpeg2 seeked copy target audio codec = %q, want aac", got)
	}
}

func TestHandleStartPlayback_MarksMissingFileAndSkipsSessionCreation(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	marker := &recordingMissingMarker{}
	file := &models.MediaFile{
		ID:        42,
		ContentID: "movie-1",
		FilePath:  filepath.Join(t.TempDir(), "missing.mkv"),
		Duration:  3600,
	}

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.MissingMarker = marker

	req := httptest.NewRequest("POST", "/api/v1/playback/start", strings.NewReader(`{"file_id":42,"profile_id":"profile-1","codecs_video":["h264"],"codecs_audio":["aac"],"containers":["mp4"],"max_resolution":"2160p","hdr":false}`))
	req = req.WithContext(newAuthorizedPlaybackContext())

	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if got := len(sessionMgr.AllSessions()); got != 0 {
		t.Fatalf("session count = %d, want 0", got)
	}
	if len(marker.ids) != 1 || marker.ids[0] != 42 {
		t.Fatalf("marked ids = %v, want [42]", marker.ids)
	}
}

func TestHandleStartTranscode_AbortsSessionWhenBackingFileIsMissing(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	filePath := writePlaybackTestMediaFile(t, "movie.mkv")
	file := &models.MediaFile{
		ID:         42,
		ContentID:  "movie-1",
		FilePath:   filePath,
		Resolution: "1080p",
		CodecVideo: "h264",
		CodecAudio: "aac",
		Container:  "mkv",
		Bitrate:    8000,
		Duration:   3600,
		AudioTracks: []models.AudioTrack{
			{Codec: "aac", Default: true},
		},
	}
	marker := &recordingMissingMarker{}
	syncer := &recordingSessionSyncer{}
	adminStore := &recordingPlaybackAdminStore{}

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.MissingMarker = marker
	handler.SessionSyncer = syncer
	handler.AdminStore = adminStore

	startReq := httptest.NewRequest("POST", "/api/v1/playback/start", strings.NewReader(`{"file_id":42,"profile_id":"profile-1","codecs_video":["h264"],"codecs_audio":["aac"],"containers":["mp4"],"max_resolution":"2160p","hdr":false}`))
	startReq = startReq.WithContext(newAuthorizedPlaybackContext())

	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, startReq)
	if startRR.Code != http.StatusCreated {
		t.Fatalf("start status = %d, body = %s", startRR.Code, startRR.Body.String())
	}

	var startResp playbackSessionResponse
	if err := json.NewDecoder(startRR.Body).Decode(&startResp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	if err := os.Remove(filePath); err != nil {
		t.Fatalf("remove file: %v", err)
	}

	transcodeReq := httptest.NewRequest(
		"POST",
		"/api/v1/playback/transcode/start",
		strings.NewReader(`{"session_id":"`+startResp.SessionID+`","seek_seconds":0,"target_resolution":"720p","target_codec_video":"h264","target_codec_audio":"aac","target_bitrate_kbps":2000,"segment_duration":2,"subtitle_track_index":-1,"subtitle_burn_in":false}`),
	)
	transcodeReq = transcodeReq.WithContext(newAuthorizedPlaybackContext())

	transcodeRR := httptest.NewRecorder()
	handler.HandleStartTranscode(transcodeRR, transcodeReq)

	if transcodeRR.Code != http.StatusNotFound {
		t.Fatalf("transcode status = %d, body = %s", transcodeRR.Code, transcodeRR.Body.String())
	}
	if _, err := sessionMgr.GetSession(startResp.SessionID); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("GetSession error = %v, want %v", err, playback.ErrSessionNotFound)
	}
	if len(marker.ids) != 1 || marker.ids[0] != 42 {
		t.Fatalf("marked ids = %v, want [42]", marker.ids)
	}
	if len(adminStore.deleted) != 1 || adminStore.deleted[0] != startResp.SessionID {
		t.Fatalf("deleted sessions = %v, want [%s]", adminStore.deleted, startResp.SessionID)
	}
	if len(adminStore.history) != 0 {
		t.Fatalf("history entries = %d, want 0", len(adminStore.history))
	}
	if syncer.calls == 0 {
		t.Fatal("expected session sync after abort")
	}
}

func TestHandleStartTranscode_KeepsSessionWhenStartupFailsForNonMissingReason(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	filePath := writePlaybackTestMediaFile(t, "movie.mkv")
	file := &models.MediaFile{
		ID:         42,
		ContentID:  "movie-1",
		FilePath:   filePath,
		Resolution: "1080p",
		CodecVideo: "h264",
		CodecAudio: "aac",
		Container:  "mkv",
		Bitrate:    8000,
		Duration:   3600,
		AudioTracks: []models.AudioTrack{
			{Codec: "aac", Default: true},
		},
	}
	syncer := &recordingSessionSyncer{}
	adminStore := &recordingPlaybackAdminStore{}

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.SessionSyncer = syncer
	handler.AdminStore = adminStore
	handler.TranscodeDir = writePlaybackTestMediaFile(t, "occupied-transcode-dir")

	startReq := httptest.NewRequest("POST", "/api/v1/playback/start", strings.NewReader(`{"file_id":42,"profile_id":"profile-1","codecs_video":["h264"],"codecs_audio":["aac"],"containers":["mp4"],"max_resolution":"2160p","hdr":false}`))
	startReq = startReq.WithContext(newAuthorizedPlaybackContext())

	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, startReq)
	if startRR.Code != http.StatusCreated {
		t.Fatalf("start status = %d, body = %s", startRR.Code, startRR.Body.String())
	}
	initialSyncCalls := syncer.calls

	var startResp playbackSessionResponse
	if err := json.NewDecoder(startRR.Body).Decode(&startResp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}

	transcodeReq := httptest.NewRequest(
		"POST",
		"/api/v1/playback/transcode/start",
		strings.NewReader(`{"session_id":"`+startResp.SessionID+`","seek_seconds":0,"target_resolution":"720p","target_codec_video":"h264","target_codec_audio":"aac","target_bitrate_kbps":2000,"segment_duration":2,"subtitle_track_index":-1,"subtitle_burn_in":false}`),
	)
	transcodeReq = transcodeReq.WithContext(newAuthorizedPlaybackContext())

	transcodeRR := httptest.NewRecorder()
	handler.HandleStartTranscode(transcodeRR, transcodeReq)

	if transcodeRR.Code != http.StatusInternalServerError {
		t.Fatalf("transcode status = %d, body = %s", transcodeRR.Code, transcodeRR.Body.String())
	}
	if _, err := sessionMgr.GetSession(startResp.SessionID); err != nil {
		t.Fatalf("GetSession error = %v, want live session", err)
	}
	if len(adminStore.deleted) != 0 {
		t.Fatalf("deleted sessions = %v, want none", adminStore.deleted)
	}
	if len(adminStore.history) != 0 {
		t.Fatalf("history entries = %d, want 0", len(adminStore.history))
	}
	if syncer.calls != initialSyncCalls {
		t.Fatalf("sync calls = %d, want unchanged value %d", syncer.calls, initialSyncCalls)
	}
}

func TestFindAlternateFile_DoesNotCrossEdition(t *testing.T) {
	source := &models.MediaFile{
		ID:         1,
		ContentID:  "movie-1",
		Resolution: "2160p",
		HDR:        true,
		Bitrate:    30_000_000,
		EditionKey: "final_cut",
	}

	handler := &PlaybackHandler{
		FileVersionFetcher: testPlaybackFileVersionFetcher{
			byContent: map[string][]*models.MediaFile{
				"movie-1": {
					source,
					{
						ID:         2,
						ContentID:  "movie-1",
						Resolution: "1080p",
						HDR:        false,
						Bitrate:    12_000_000,
						EditionKey: "theatrical",
					},
					{
						ID:         3,
						ContentID:  "movie-1",
						Resolution: "1080p",
						HDR:        false,
						Bitrate:    10_000_000,
						EditionKey: "final_cut",
					},
				},
			},
		},
	}

	alternate, err := handler.findAlternateFile(context.Background(), source)
	if err != nil {
		t.Fatalf("findAlternateFile: %v", err)
	}
	if alternate == nil {
		t.Fatal("expected alternate file")
	}
	if alternate.ID != 3 {
		t.Fatalf("alternate.ID = %d, want 3", alternate.ID)
	}
}
