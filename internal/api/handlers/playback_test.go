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
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchsync"
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

type firstBlockingSessionManager struct {
	*playback.SessionManager
	blocked atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (m *firstBlockingSessionManager) UpdateStreamState(sessionID string, state playback.SessionStreamState) error {
	return m.SessionManager.UpdateStreamState(sessionID, state)
}

func (m *firstBlockingSessionManager) ApplyReplacementIfRoute(
	sessionID string,
	expected playback.TranscodeRoute,
	replacement playback.SessionReplacement,
) (playback.SessionReplacementRollback, bool, error) {
	if m.blocked.CompareAndSwap(false, true) {
		close(m.entered)
		<-m.release
	}
	return m.SessionManager.ApplyReplacementIfRoute(sessionID, expected, replacement)
}

func (m *firstBlockingSessionManager) ApplyReplacement(
	sessionID string,
	replacement playback.SessionReplacement,
) (playback.SessionReplacementRollback, error) {
	if m.blocked.CompareAndSwap(false, true) {
		close(m.entered)
		<-m.release
	}
	return m.SessionManager.ApplyReplacement(sessionID, replacement)
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

type recordingPlaybackWatchScrobbler struct {
	starts []watchsync.ScrobbleEvent
	pauses []watchsync.ScrobbleEvent
	stops  []watchsync.ScrobbleEvent
}

func (s *recordingPlaybackWatchScrobbler) ScrobbleStart(_ context.Context, event watchsync.ScrobbleEvent) error {
	s.starts = append(s.starts, event)
	return nil
}

func (s *recordingPlaybackWatchScrobbler) ScrobblePause(_ context.Context, event watchsync.ScrobbleEvent) error {
	s.pauses = append(s.pauses, event)
	return nil
}

func (s *recordingPlaybackWatchScrobbler) ScrobbleStop(_ context.Context, event watchsync.ScrobbleEvent) error {
	s.stops = append(s.stops, event)
	return nil
}

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
func (failingSessionManager) SetTranscodeRoute(string, playback.TranscodeRoute) error {
	return nil
}
func (failingSessionManager) ApplyReplacement(string, playback.SessionReplacement) (playback.SessionReplacementRollback, error) {
	return playback.SessionReplacementRollback{}, nil
}
func (failingSessionManager) ApplyReplacementIfRoute(string, playback.TranscodeRoute, playback.SessionReplacement) (playback.SessionReplacementRollback, bool, error) {
	return playback.SessionReplacementRollback{}, true, nil
}
func (failingSessionManager) RollbackReplacement(string, playback.SessionReplacementRollback) error {
	return nil
}

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
	return writePlaybackTestFFmpegSleep(t, "30")
}

func writePlaybackTestFFmpegSleep(t *testing.T, sleepSeconds string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "fake-ffmpeg.sh")
	script := "#!/bin/sh\n" +
		"last=\"\"\n" +
		"for arg in \"$@\"; do last=\"$arg\"; done\n" +
		"case \"$last\" in\n" +
		"  *.m3u8) out=\"$(dirname \"$last\")\"; mkdir -p \"$out\"; " +
		"printf x > \"$out/init.mp4\"; printf x > \"$out/seg_0.m4s\"; " +
		"printf x > \"$out/seg_1.m4s\"; printf x > \"$out/seg_2.m4s\"; " +
		"printf '#EXTM3U\\n#EXT-X-VERSION:7\\n#EXT-X-TARGETDURATION:2\\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\\n#EXT-X-MAP:URI=\"init.mp4\"\\n" +
		"#EXTINF:2.0,\\nseg_0.m4s\\n#EXTINF:2.0,\\nseg_1.m4s\\n" +
		"#EXTINF:2.0,\\nseg_2.m4s\\n' > \"$last\" ;;\n" +
		"esac\n" +
		"sleep " + sleepSeconds + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return path
}

func writePlaybackTestFFmpegFailingAfterFirstStart(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "fake-ffmpeg.sh")
	countPath := filepath.Join(dir, "started")
	script := "#!/bin/sh\n" +
		"if [ -e \"" + countPath + "\" ]; then echo 'intentional successor failure' >&2; exit 1; fi\n" +
		": > \"" + countPath + "\"\n" +
		"last=\"\"\n" +
		"for arg in \"$@\"; do last=\"$arg\"; done\n" +
		"out=\"$(dirname \"$last\")\"; mkdir -p \"$out\"\n" +
		"printf x > \"$out/init.mp4\"; printf x > \"$out/seg_0.m4s\"; " +
		"printf x > \"$out/seg_1.m4s\"; printf x > \"$out/seg_2.m4s\"\n" +
		"printf '#EXTM3U\\n#EXT-X-VERSION:7\\n#EXT-X-TARGETDURATION:2\\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\\n#EXT-X-MAP:URI=\"init.mp4\"\\n" +
		"#EXTINF:2.0,\\nseg_0.m4s\\n#EXTINF:2.0,\\nseg_1.m4s\\n" +
		"#EXTINF:2.0,\\nseg_2.m4s\\n' > \"$last\"\n" +
		"sleep 30\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fail-after-first fake ffmpeg: %v", err)
	}
	return path
}

func writePlaybackTestFFmpegAlwaysFailing(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "failing-ffmpeg.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho 'intentional startup failure' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write failing fake ffmpeg: %v", err)
	}
	return path
}

func writePlaybackTestFFmpegNeverReady(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "not-ready-ffmpeg.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatalf("write not-ready fake ffmpeg: %v", err)
	}
	return path
}

func playbackTestConfig(ffmpegPath, transcodeDir string) func() config.PlaybackConfig {
	return func() config.PlaybackConfig {
		return config.PlaybackConfig{
			FFmpegPath:       ffmpegPath,
			TranscodeDir:     transcodeDir,
			TranscodeEnabled: true,
		}
	}
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

func TestFinalizeSessionStop_UsesProviderLifecycleSemantics(t *testing.T) {
	tests := []struct {
		name               string
		position           float64
		userInitiated      bool
		disablePersistence bool
		wantPauseCalls     int
		wantStopCalls      int
		wantEventPosition  float64
	}{
		{
			name:              "user exit stops below-resume-threshold scrobble",
			position:          120,
			userInitiated:     true,
			wantStopCalls:     1,
			wantEventPosition: 120,
		},
		{
			name:              "system teardown pauses reconstructable scrobble",
			position:          120,
			wantPauseCalls:    1,
			wantEventPosition: 120,
		},
		{
			name:              "system teardown pauses persisted incomplete scrobble",
			position:          600,
			wantPauseCalls:    1,
			wantEventPosition: 600,
		},
		{
			name:              "completed system teardown stops scrobble",
			position:          3500,
			wantStopCalls:     1,
			wantEventPosition: 3500,
		},
		{
			name:              "user exit at zero still stops scrobble",
			userInitiated:     true,
			wantStopCalls:     1,
			wantEventPosition: 0,
		},
		{
			name:               "disabled persistence does not scrobble",
			position:           120,
			userInitiated:      true,
			disablePersistence: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newPlaybackTestStore(t)
			file := &models.MediaFile{
				ID:        42,
				ContentID: "movie-1",
				Duration:  3600,
			}
			scrobbler := &recordingPlaybackWatchScrobbler{}
			handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
			handler.StoreProvider = testUserStoreProvider{store: store}
			handler.WatchScrobbler = scrobbler

			session := &playback.Session{
				ID:                         "session-1",
				UserID:                     1,
				ProfileID:                  "profile-1",
				MediaFileID:                file.ID,
				Position:                   tt.position,
				DisableProgressPersistence: tt.disablePersistence,
				StartedAt:                  time.Now().Add(-time.Minute),
			}
			handler.finalizeSessionStop(context.Background(), session, false, "", tt.userInitiated)

			if got := len(scrobbler.pauses); got != tt.wantPauseCalls {
				t.Fatalf("pause calls = %d, want %d", got, tt.wantPauseCalls)
			}
			if got := len(scrobbler.stops); got != tt.wantStopCalls {
				t.Fatalf("stop calls = %d, want %d", got, tt.wantStopCalls)
			}
			if len(scrobbler.pauses)+len(scrobbler.stops) == 1 {
				event := scrobbler.pauses
				if len(event) == 0 {
					event = scrobbler.stops
				}
				if event[0].MediaItemID != file.ContentID {
					t.Fatalf("media item ID = %q, want %q", event[0].MediaItemID, file.ContentID)
				}
				if event[0].PositionSeconds != tt.wantEventPosition {
					t.Fatalf("position = %v, want %v", event[0].PositionSeconds, tt.wantEventPosition)
				}
			}
		})
	}
}

// The next episode of a series should open on the version the viewer settled
// on, so a successful start records the version traits it actually played.
func TestHandleStartPlaybackV3_PersistsSeriesPlaybackPreferenceForEpisodes(t *testing.T) {
	store := newPlaybackTestStore(t)
	file := v3HandlerFixtureFile(t)
	file.EpisodeID = "episode-1"

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.StoreProvider = testUserStoreProvider{store: store}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.EpisodeLookup = testEpisodeLookup{episode: &models.Episode{ContentID: "episode-1", SeriesID: "series-1"}}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start",
		strings.NewReader(marshalV3StartRequest(t, v3HandlerStartRequest()))).WithContext(newAuthorizedPlaybackContext())
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)
	if rr.Code != http.StatusCreated {
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

// A start that never produced a session played nothing, so it must not teach
// the series what the viewer prefers.
func TestHandleStartPlaybackV3_DoesNotPersistSeriesPlaybackPreferenceOnFailure(t *testing.T) {
	store := newPlaybackTestStore(t)
	file := v3HandlerFixtureFile(t)
	file.EpisodeID = "episode-1"

	handler := NewPlaybackHandler(failingSessionManager{}, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.StoreProvider = testUserStoreProvider{store: store}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.EpisodeLookup = testEpisodeLookup{episode: &models.Episode{ContentID: "episode-1", SeriesID: "series-1"}}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start",
		strings.NewReader(marshalV3StartRequest(t, v3HandlerStartRequest()))).WithContext(newAuthorizedPlaybackContext())
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)

	// v3 reports playback outcomes in the body; the request itself was fine.
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response playback.DecisionResponseV3
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Terminal == nil || response.PlaybackPlan != nil {
		t.Fatalf("expected a terminal with no plan, got %#v", response)
	}

	pref, err := store.GetSeriesPlaybackPreference(context.Background(), "profile-1", "series-1")
	if err != nil {
		t.Fatalf("GetSeriesPlaybackPreference: %v", err)
	}
	if pref != nil {
		t.Fatalf("expected no persisted preference on failure, got %+v", pref)
	}
}

// A v3 client sends a track identity only when the viewer picked one. With none
// sent, the server resolves this profile's audio language through the settings
// contract — the same resolution the catalog publishes as
// effective_audio_track_index — instead of falling to ordinal zero.
//
// The legacy user_profiles.language column always carries a different track's
// language than the canonical answer, so a regression to reading it flips the
// selected index.
func TestHandleStartPlaybackV3_AudioLanguageResolvesCanonically(t *testing.T) {
	newFile := func(t *testing.T) *models.MediaFile {
		file := v3HandlerFixtureFile(t)
		file.AudioTracks = []models.AudioTrack{
			{Language: "eng", Codec: "aac", Channels: 2, Layout: "stereo", Default: true},
			{Language: "jpn", Codec: "aac", Channels: 2, Layout: "stereo"},
			{Language: "spa", Codec: "aac", Channels: 2, Layout: "stereo"},
			{Language: "fra", Codec: "aac", Channels: 2, Layout: "stereo"},
		}
		return file
	}

	setLegacyLanguage := func(t *testing.T, store userstore.UserStore, language string) {
		t.Helper()
		if err := store.UpdateProfile(context.Background(), "profile-1", userstore.UpdateProfileInput{
			Language: &language,
		}); err != nil {
			t.Fatalf("seed legacy language column: %v", err)
		}
	}

	setCanonicalLanguage := func(t *testing.T, store userstore.UserStore, identity userstore.SettingIdentity, language string) {
		t.Helper()
		identity.Key = settingskeys.PlaybackAudioLanguage
		identity.ProfileID = "profile-1"
		encoded, err := json.Marshal(language)
		if err != nil {
			t.Fatalf("encode canonical audio language: %v", err)
		}
		if _, err := store.UpsertSettingValue(context.Background(), identity, encoded); err != nil {
			t.Fatalf("seed canonical audio language: %v", err)
		}
	}

	startPlayback := func(
		t *testing.T,
		store userstore.UserStore,
		file *models.MediaFile,
		deviceID string,
		mutate func(*playback.StartRequestV3),
	) int {
		t.Helper()
		manager := playback.NewSessionManager(0, 0)
		handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
		handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
		handler.StoreProvider = testUserStoreProvider{store: store}
		handler.ItemAccess = allowAllPlaybackItemAccess{}
		if file.EpisodeID != "" {
			handler.EpisodeLookup = testEpisodeLookup{episode: &models.Episode{ContentID: file.EpisodeID, SeriesID: "series-1"}}
		}

		startRequest := v3HandlerStartRequest()
		startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassProgressiveV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true}
		if mutate != nil {
			mutate(&startRequest)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start",
			strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext())
		// A client name or user agent is not a stable settings identity. Only
		// the explicit device header may select a profile_device row.
		req.Header.Set("User-Agent", "SiloTV/apple-tv")
		if deviceID != "" {
			req.Header.Set(deviceIDHeader, deviceID)
		}
		rr := httptest.NewRecorder()
		handler.HandleStartPlayback(rr, req)
		if rr.Code != http.StatusCreated {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
		var response playback.DecisionResponseV3
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if response.PlaybackPlan == nil {
			t.Fatalf("response = %#v", response)
		}
		session, err := manager.GetSession(response.PlaybackPlan.SessionID)
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		return session.AudioTrackIndex
	}

	t.Run("canonical value wins over legacy column", func(t *testing.T) {
		store := newPlaybackTestStore(t)
		setLegacyLanguage(t, store, "eng")
		setCanonicalLanguage(t, store, userstore.SettingIdentity{
			Scope: settingscontract.ScopeProfile,
		}, "ja")

		if index := startPlayback(t, store, newFile(t), "", nil); index != 1 {
			t.Fatalf("AudioTrackIndex = %d, want 1 (canonical \"ja\" track)", index)
		}
	})

	t.Run("legacy column alone no longer selects a track", func(t *testing.T) {
		store := newPlaybackTestStore(t)
		setLegacyLanguage(t, store, "jpn")

		// No canonical value stored: the contract default is "no preference",
		// so selection falls to the file's default track, not the column's.
		if index := startPlayback(t, store, newFile(t), "", nil); index != 0 {
			t.Fatalf("AudioTrackIndex = %d, want 0 (file default track)", index)
		}
	})

	t.Run("device value applies only to the identified device", func(t *testing.T) {
		store := newPlaybackTestStore(t)
		setCanonicalLanguage(t, store, userstore.SettingIdentity{Scope: settingscontract.ScopeProfile}, "en")
		setCanonicalLanguage(t, store, userstore.SettingIdentity{
			Scope:    settingscontract.ScopeProfileDevice,
			DeviceID: "apple-tv",
		}, "ja")

		if index := startPlayback(t, store, newFile(t), "apple-tv", nil); index != 1 {
			t.Fatalf("AudioTrackIndex = %d, want 1 (device \"ja\" track)", index)
		}
		if index := startPlayback(t, store, newFile(t), "", nil); index != 0 {
			t.Fatalf("AudioTrackIndex = %d, want 0 (profile \"en\" track without device identity)", index)
		}
		if index := startPlayback(t, store, newFile(t), "iphone", nil); index != 0 {
			t.Fatalf("AudioTrackIndex = %d, want 0 (profile \"en\" track on another device)", index)
		}
	})

	t.Run("series and library values outrank device value", func(t *testing.T) {
		store := newPlaybackTestStore(t)
		setCanonicalLanguage(t, store, userstore.SettingIdentity{Scope: settingscontract.ScopeProfile}, "en")
		setCanonicalLanguage(t, store, userstore.SettingIdentity{
			Scope:    settingscontract.ScopeProfileDevice,
			DeviceID: "apple-tv",
		}, "ja")
		setCanonicalLanguage(t, store, userstore.SettingIdentity{
			Scope:     settingscontract.ScopeProfileLibrary,
			LibraryID: 12,
		}, "es")
		setCanonicalLanguage(t, store, userstore.SettingIdentity{
			Scope:    settingscontract.ScopeProfileSeries,
			SeriesID: "series-1",
		}, "fr")

		movie := newFile(t)
		movie.MediaFolderID = 12
		if index := startPlayback(t, store, movie, "apple-tv", nil); index != 2 {
			t.Fatalf("AudioTrackIndex = %d, want 2 (library \"es\" track)", index)
		}

		episode := newFile(t)
		episode.MediaFolderID = 12
		episode.EpisodeID = "episode-1"
		if index := startPlayback(t, store, episode, "apple-tv", nil); index != 3 {
			t.Fatalf("AudioTrackIndex = %d, want 3 (series \"fr\" track)", index)
		}
	})

	t.Run("explicit track selection outranks saved settings", func(t *testing.T) {
		store := newPlaybackTestStore(t)
		setCanonicalLanguage(t, store, userstore.SettingIdentity{
			Scope:    settingscontract.ScopeProfileDevice,
			DeviceID: "apple-tv",
		}, "ja")
		file := newFile(t)
		if index := startPlayback(t, store, file, "apple-tv", func(request *playback.StartRequestV3) {
			explicit := 0
			request.AudioTrackIndex = &explicit
			request.AudioTrackID = playback.TrackIDV3(file.ID, "audio", explicit)
		}); index != 0 {
			t.Fatalf("AudioTrackIndex = %d, want 0 (explicit \"en\" track)", index)
		}
	})
}

// An omitted start_position asks the server for its resume policy; an explicit
// zero means "start over". The distinction is settled before planning, because
// the plan's timeline is cut at the start position — a route chosen for zero
// and then seeked to the resume point is a different route.
func TestHandleStartPlaybackV3_ResumePolicyAppliesOnlyToAnOmittedStartPosition(t *testing.T) {
	zero := 0.0
	multipart := func(file *models.MediaFile) {
		file.PresentationKind = "multipart"
		file.PresentationGroupKey = file.ContentID
		file.PresentationPartIndex = 4
		file.PresentationPartTotal = 6
	}

	for _, tc := range []struct {
		name          string
		startPosition *float64
		mutate        func(*models.MediaFile)
		wantPosition  float64
	}{
		{name: "omitted start position restores the saved resume point", wantPosition: 900},
		{name: "explicit zero starts over", startPosition: &zero, wantPosition: 0},
		// Every part of a multipart item shares one resume point with the item,
		// so a part-local seek to it would land somewhere arbitrary.
		{name: "a multipart part does not resume into itself", mutate: multipart, wantPosition: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newPlaybackTestStore(t)
			if err := store.SetProgress(context.Background(), "profile-1", "movie-1", 900, 3600, userstore.ProgressThresholds{}); err != nil {
				t.Fatalf("seed progress: %v", err)
			}
			file := v3HandlerFixtureFile(t)
			if tc.mutate != nil {
				tc.mutate(file)
			}

			manager := playback.NewSessionManager(0, 0)
			handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
			handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
			handler.StoreProvider = testUserStoreProvider{store: store}
			handler.ItemAccess = allowAllPlaybackItemAccess{}

			request := v3HandlerStartRequest()
			request.StartPosition = tc.startPosition
			req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start",
				strings.NewReader(marshalV3StartRequest(t, request))).WithContext(newAuthorizedPlaybackContext())
			rr := httptest.NewRecorder()
			handler.HandleStartPlayback(rr, req)
			if rr.Code != http.StatusCreated {
				t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
			}
			var response playback.DecisionResponseV3
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.PlaybackPlan == nil {
				t.Fatalf("response = %#v", response)
			}
			// The plan and the session have to agree on where playback begins.
			if got := response.PlaybackPlan.Timeline.SourceStartSeconds; got != tc.wantPosition {
				t.Fatalf("plan source start = %v, want %v", got, tc.wantPosition)
			}
			session, err := manager.GetSession(response.PlaybackPlan.SessionID)
			if err != nil {
				t.Fatalf("GetSession: %v", err)
			}
			if session.Position != tc.wantPosition {
				t.Fatalf("session position = %v, want %v", session.Position, tc.wantPosition)
			}
		})
	}
}

// A missing backing file is not something a client can be handed a plan for,
// and it must not leave a session behind that nothing will ever stop.
func TestHandleStartPlaybackV3_MarksMissingFileAndSkipsSessionCreation(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	marker := &recordingMissingMarker{}
	file := v3HandlerFixtureFile(t)
	file.FilePath = filepath.Join(t.TempDir(), "missing.mkv")

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.MissingMarker = marker

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start",
		strings.NewReader(marshalV3StartRequest(t, v3HandlerStartRequest()))).WithContext(newAuthorizedPlaybackContext())
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

func TestTranscodeResolutionHeight(t *testing.T) {
	tests := []struct {
		resolution string
		wantHeight int
		wantKnown  bool
	}{
		{resolution: transcodeResolution2160p, wantHeight: 2160, wantKnown: true},
		{resolution: transcodeResolution1080p, wantHeight: 1080, wantKnown: true},
		{resolution: transcodeResolution720p, wantHeight: 720, wantKnown: true},
		{resolution: transcodeResolution480p, wantHeight: 480, wantKnown: true},
		{resolution: transcodeResolution420p, wantHeight: 420, wantKnown: true},
		{resolution: transcodeResolution328p, wantHeight: 328, wantKnown: true},
		{resolution: "unrecognized-tier", wantHeight: 0, wantKnown: false},
	}

	for _, tt := range tests {
		t.Run(tt.resolution, func(t *testing.T) {
			gotHeight, gotKnown := transcodeResolutionHeight(tt.resolution)
			if gotHeight != tt.wantHeight || gotKnown != tt.wantKnown {
				t.Fatalf(
					"transcodeResolutionHeight(%q) = (%d, %t), want (%d, %t)",
					tt.resolution,
					gotHeight,
					gotKnown,
					tt.wantHeight,
					tt.wantKnown,
				)
			}
		})
	}
}

func TestResolutionRank(t *testing.T) {
	tests := []struct {
		resolution string
		want       int
	}{
		{resolution: transcodeResolution2160p, want: 4},
		{resolution: transcodeResolution1080p, want: 3},
		{resolution: transcodeResolution720p, want: 2},
		{resolution: transcodeResolution480p, want: 1},
		{resolution: transcodeResolution420p, want: 0},
		{resolution: transcodeResolution328p, want: 0},
		{resolution: "unrecognized-tier", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.resolution, func(t *testing.T) {
			if got := resolutionRank(tt.resolution); got != tt.want {
				t.Fatalf("resolutionRank(%q) = %d, want %d", tt.resolution, got, tt.want)
			}
		})
	}
}

func TestAlignedSeekSeconds(t *testing.T) {
	tests := []struct {
		name        string
		seek        float64
		segDur      int
		targetVideo string
		want        float64
	}{
		// Encoded transcodes snap down to the declared segment boundary so the
		// synthetic manifest's timeline matches the produced content exactly.
		{"encoded mid-segment seek snaps down", 1158.673, 2, "h264", 1158},
		{"encoded boundary seek unchanged", 1158, 2, "h264", 1158},
		{"encoded zero seek unchanged", 0, 2, "h264", 0},
		{"segment duration defaults to 2", 1158.673, 0, "h264", 1158},
		// Copy-mode serves ffmpeg's real manifest; raw seek stands.
		{"copy keeps raw seek", 1158.673, 2, "copy", 1158.673},
		{"copy case-insensitive", 1158.673, 2, "COPY", 1158.673},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := alignedSeekSeconds(tt.seek, tt.segDur, tt.targetVideo); got != tt.want {
				t.Fatalf("alignedSeekSeconds(%v, %d, %q) = %v, want %v",
					tt.seek, tt.segDur, tt.targetVideo, got, tt.want)
			}
		})
	}
}
