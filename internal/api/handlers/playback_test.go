package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
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
		"sleep 30\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return path
}

func TestNativeTranscodeServeRejectsStaleLiveGenerationToken(t *testing.T) {
	var proxyRequests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyRequests.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("g2-content"))
	}))
	t.Cleanup(proxy.Close)
	mgr := playback.NewSessionManager(0, 0)
	if got, err := mgr.RegisterReconstructedChecked(t.Context(), &playback.Session{
		ID: "shared-native", Generation: "g2", UserID: 1, MediaFileID: 99,
		PlayMethod: playback.PlayTranscode, TranscodeNodeURL: proxy.URL,
	}); err != nil || got == nil {
		t.Fatalf("register G2: session=%+v err=%v", got, err)
	}
	h := NewPlaybackHandler(mgr)
	h.JWTSecret = "native-token-secret"
	g1 := playback.NewRecipeCard(1, "profile-1", 42, "", playback.TranscodeOpts{SessionID: "shared-native"}).
		WithSessionIdentity("g1", time.Now().UTC().Add(-time.Minute))
	staleToken := h.signSessionToken(g1)

	for _, tc := range []struct {
		name   string
		path   string
		params map[string]string
		handle func(http.ResponseWriter, *http.Request)
	}{
		{name: "manifest", path: "/playback/transcode/shared-native/master.m3u8", params: map[string]string{"session_id": "shared-native"}, handle: h.HandleGetTranscodeManifest},
		{name: "segment", path: "/playback/transcode/shared-native/segment/seg_0.ts", params: map[string]string{"session_id": "shared-native", "name": "seg_0.ts"}, handle: h.HandleGetTranscodeSegment},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			tc.handle(recorder, playbackTestRequest(http.MethodGet, tc.path+"?st="+url.QueryEscape(staleToken), nil, tc.params))
			if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), "g2-content") {
				t.Fatalf("stale %s response status=%d body=%q", tc.name, recorder.Code, recorder.Body.String())
			}
		})
	}
	if proxyRequests.Load() != 0 {
		t.Fatalf("stale G1 token served G2 through %d proxy requests", proxyRequests.Load())
	}
	invalid := httptest.NewRecorder()
	h.HandleGetTranscodeManifest(invalid, playbackTestRequest(http.MethodGet,
		"/playback/transcode/shared-native/master.m3u8?st=invalid", nil,
		map[string]string{"session_id": "shared-native"}))
	if invalid.Code != http.StatusUnauthorized || proxyRequests.Load() != 0 {
		t.Fatalf("invalid supplied token status=%d proxy_requests=%d body=%q", invalid.Code, proxyRequests.Load(), invalid.Body.String())
	}
	streamHandler := NewStreamHandler(mgr, nil)
	streamHandler.JWTSecret = h.JWTSecret
	invalidStream := httptest.NewRecorder()
	streamHandler.HandleStream(invalidStream, playbackTestRequest(http.MethodGet,
		"/stream/shared-native?st=invalid", nil,
		map[string]string{"session_id": "shared-native"}))
	if invalidStream.Code != http.StatusUnauthorized {
		t.Fatalf("invalid direct stream token status=%d body=%q", invalidStream.Code, invalidStream.Body.String())
	}
	matching := playback.NewRecipeCard(1, "profile-1", 99, "", playback.TranscodeOpts{SessionID: "shared-native"}).
		WithSessionIdentity("g2", time.Now().UTC())
	for _, tc := range []struct {
		name  string
		token string
	}{
		{name: "matching token", token: h.signSessionToken(matching)},
		{name: "absent legacy token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := "/playback/transcode/shared-native/master.m3u8"
			if tc.token != "" {
				target += "?st=" + url.QueryEscape(tc.token)
			}
			recorder := httptest.NewRecorder()
			h.HandleGetTranscodeManifest(recorder, playbackTestRequest(http.MethodGet, target, nil, map[string]string{"session_id": "shared-native"}))
			if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "g2-content") {
				t.Fatalf("live control status=%d body=%q", recorder.Code, recorder.Body.String())
			}
		})
	}
	if proxyRequests.Load() != 2 {
		t.Fatalf("valid live controls made %d proxy requests, want 2", proxyRequests.Load())
	}
	live, err := mgr.GetSession("shared-native")
	if err != nil || live.Generation != "g2" || live.MediaFileID != 99 {
		t.Fatalf("stale token changed G2: session=%+v err=%v", live, err)
	}
}

func TestLoadNativeTranscodeLiveSessionTokenControls(t *testing.T) {
	mgr := playback.NewSessionManager(0, 0)
	startedAt := time.Now().UTC().Add(-time.Minute)
	if got, err := mgr.RegisterReconstructedChecked(t.Context(), &playback.Session{
		ID: "native-live", Generation: "g2", UserID: 1, MediaFileID: 99, StartedAt: startedAt,
	}); err != nil || got == nil {
		t.Fatalf("register live: session=%+v err=%v", got, err)
	}
	h := NewPlaybackHandler(mgr)
	h.JWTSecret = "native-token-secret"
	matching := playback.NewRecipeCard(1, "profile-1", 99, "", playback.TranscodeOpts{SessionID: "native-live"}).WithSessionIdentity("g2", startedAt)
	for _, tc := range []struct {
		name  string
		token string
	}{
		{name: "matching token", token: h.signSessionToken(matching)},
		{name: "absent legacy token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := playbackTestRequest(http.MethodGet, "/playback/transcode/native-live/master.m3u8", nil, map[string]string{"session_id": "native-live"})
			if tc.token != "" {
				query := req.URL.Query()
				query.Set(streamTokenParam, tc.token)
				req.URL.RawQuery = query.Encode()
			}
			got, status, card, err := h.loadTranscodeServeSession(req, "native-live")
			if err != nil || status != playback.SessionLoaded || got == nil || got.Generation != "g2" {
				t.Fatalf("load live: session=%+v status=%v card=%+v err=%v", got, status, card, err)
			}
			if tc.token == "" && card != nil {
				t.Fatalf("absent token unexpectedly decoded card %+v", card)
			}
		})
	}
}

func TestNativeTranscodeServeHoldsGenerationThroughProcessSelection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		path   string
		params map[string]string
		handle func(*PlaybackHandler, http.ResponseWriter, *http.Request)
	}{
		{name: "manifest", path: "/playback/transcode/shared-barrier/master.m3u8", params: map[string]string{"session_id": "shared-barrier"}, handle: func(h *PlaybackHandler, w http.ResponseWriter, r *http.Request) { h.HandleGetTranscodeManifest(w, r) }},
		{name: "segment", path: "/playback/transcode/shared-barrier/segment/seg_0.m4s", params: map[string]string{"session_id": "shared-barrier", "name": "seg_0.m4s"}, handle: func(h *PlaybackHandler, w http.ResponseWriter, r *http.Request) { h.HandleGetTranscodeSegment(w, r) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g1Proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("g1-content"))
			}))
			t.Cleanup(g1Proxy.Close)
			mgr := playback.NewSessionManager(0, 0)
			startedAt := time.Now().UTC().Add(-time.Minute)
			if got, err := mgr.RegisterReconstructedChecked(t.Context(), &playback.Session{
				ID: "shared-barrier", Generation: "g1", UserID: 1, MediaFileID: 42,
				PlayMethod: playback.PlayTranscode, TranscodeNodeURL: g1Proxy.URL, StartedAt: startedAt,
			}); err != nil || got == nil {
				t.Fatalf("register G1: session=%+v err=%v", got, err)
			}
			h := NewPlaybackHandler(mgr)
			h.JWTSecret = "barrier-secret"
			g1Card := playback.NewRecipeCard(1, "profile-1", 42, g1Proxy.URL, playback.TranscodeOpts{SessionID: "shared-barrier"}).WithSessionIdentity("g1", startedAt)
			token := h.signSessionToken(g1Card)

			g2Script := filepath.Join(t.TempDir(), "g2-ffmpeg.sh")
			if err := os.WriteFile(g2Script, []byte("#!/bin/sh\nlast=''\nfor arg in \"$@\"; do last=\"$arg\"; done\nout=$(dirname \"$last\")\nmkdir -p \"$out\"\nprintf '#EXTM3U\\n#G2-MANIFEST\\n' > \"$last\"\nprintf 'g2-segment' > \"$out/seg_0.m4s\"\nsleep 30\n"), 0o755); err != nil {
				t.Fatalf("write G2 ffmpeg: %v", err)
			}
			g2Transcode, err := playback.StartTranscode(t.Context(), playback.TranscodeOpts{
				SessionID: "shared-barrier", InputPath: "ignored", OutputDir: t.TempDir(), FFmpegPath: g2Script,
				TargetCodecVideo: "copy", TargetCodecAudio: "copy", SegmentDuration: 2,
			})
			if err != nil {
				t.Fatalf("start G2 transcode: %v", err)
			}
			t.Cleanup(func() {
				h.tm.CloseTranscodeSession("shared-barrier", "")
				_ = g2Transcode.CloseProcess()
			})

			selectionEntered := make(chan struct{})
			releaseSelection := make(chan struct{})
			h.transcodeServeSelectionHook = func() {
				close(selectionEntered)
				<-releaseSelection
			}
			recorder := httptest.NewRecorder()
			requestDone := make(chan struct{})
			go func() {
				defer close(requestDone)
				tc.handle(h, recorder, playbackTestRequest(http.MethodGet, tc.path+"?st="+url.QueryEscape(token), nil, tc.params))
			}()
			select {
			case <-selectionEntered:
			case <-time.After(2 * time.Second):
				t.Fatal("serve request did not reach process-selection barrier")
			}

			replacementDone := make(chan error, 1)
			go func() {
				err := mgr.TerminateSessionGeneration(t.Context(), "shared-barrier", "g1", func() error {
					h.tm.RegisterTranscodeSession("shared-barrier", g2Transcode)
					return nil
				})
				if err == nil {
					_, err = mgr.RegisterReconstructedChecked(t.Context(), &playback.Session{
						ID: "shared-barrier", Generation: "g2", UserID: 1, MediaFileID: 99, PlayMethod: playback.PlayTranscode,
					})
				}
				replacementDone <- err
			}()
			select {
			case err := <-replacementDone:
				t.Fatalf("G2 replacement completed before G1 process capture: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			close(releaseSelection)
			select {
			case <-requestDone:
			case <-time.After(3 * time.Second):
				t.Fatal("serve request did not complete")
			}
			if err := <-replacementDone; err != nil {
				t.Fatalf("install G2: %v", err)
			}
			if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "g1-content") || strings.Contains(recorder.Body.String(), "g2") {
				t.Fatalf("stale G1 serve status=%d body=%q", recorder.Code, recorder.Body.String())
			}
			if live, err := mgr.GetSession("shared-barrier"); err != nil || live.Generation != "g2" || live.MediaFileID != 99 {
				t.Fatalf("G2 native state: session=%+v err=%v", live, err)
			}
			if h.tm.GetTranscodeSession("shared-barrier") != g2Transcode {
				t.Fatal("G1 request changed G2 transcode")
			}
		})
	}
}

func writeNativeTranscodeFiles(outputDir, label string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create transcode fixture directory: %w", err)
	}
	manifest := "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n#" + label +
		"-MANIFEST\n#EXTINF:2.0,\nseg_0.m4s\n#EXTINF:2.0,\nseg_1.m4s\n"
	for name, body := range map[string]string{
		"stream.m3u8": manifest,
		"seg_0.m4s":   label + "-segment-bytes",
		"seg_1.m4s":   label + "-lookahead",
	} {
		if err := os.WriteFile(filepath.Join(outputDir, name), []byte(body), 0o644); err != nil {
			return fmt.Errorf("write %s transcode fixture: %w", name, err)
		}
	}
	return nil
}

func startNativeFileTranscode(t *testing.T, sessionID, outputDir string) *playback.TranscodeSession {
	t.Helper()
	session, err := playback.StartTranscode(t.Context(), playback.TranscodeOpts{
		SessionID: sessionID, InputPath: "ignored", OutputDir: outputDir, FFmpegPath: "/bin/true",
		TargetCodecVideo: "copy", TargetCodecAudio: "copy", SegmentDuration: 2,
	})
	if err != nil {
		t.Fatalf("start file-backed transcode fixture: %v", err)
	}
	t.Cleanup(func() { _ = session.CloseProcess() })
	return session
}

func TestNativeTranscodeServeCapturesResourceBeforeSameDirectoryReplacement(t *testing.T) {
	for _, tc := range []struct {
		name        string
		path        string
		params      map[string]string
		rangeHeader string
		wantStatus  int
		wantBody    string
		handle      func(*PlaybackHandler, http.ResponseWriter, *http.Request)
	}{
		{name: "manifest", path: "/playback/transcode/shared-files/master.m3u8", params: map[string]string{"session_id": "shared-files"}, wantStatus: http.StatusOK, wantBody: "G1-MANIFEST", handle: func(h *PlaybackHandler, w http.ResponseWriter, r *http.Request) { h.HandleGetTranscodeManifest(w, r) }},
		{name: "segment", path: "/playback/transcode/shared-files/segment/seg_0.m4s", params: map[string]string{"session_id": "shared-files", "name": "seg_0.m4s"}, wantStatus: http.StatusOK, wantBody: "G1-segment-bytes", handle: func(h *PlaybackHandler, w http.ResponseWriter, r *http.Request) { h.HandleGetTranscodeSegment(w, r) }},
		{name: "segment_range", path: "/playback/transcode/shared-files/segment/seg_0.m4s", params: map[string]string{"session_id": "shared-files", "name": "seg_0.m4s"}, rangeHeader: "bytes=3-9", wantStatus: http.StatusPartialContent, wantBody: "segment", handle: func(h *PlaybackHandler, w http.ResponseWriter, r *http.Request) { h.HandleGetTranscodeSegment(w, r) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr := playback.NewSessionManager(0, 0)
			startedAt := time.Now().UTC().Add(-time.Minute)
			if got, err := mgr.RegisterReconstructedChecked(t.Context(), &playback.Session{
				ID: "shared-files", Generation: "g1", UserID: 1, MediaFileID: 42,
				PlayMethod: playback.PlayTranscode, StartedAt: startedAt,
			}); err != nil || got == nil {
				t.Fatalf("register G1: session=%+v err=%v", got, err)
			}
			h := NewPlaybackHandler(mgr)
			h.JWTSecret = "filesystem-barrier-secret"
			var servedSegment *trackingTranscodeSegmentFile
			if tc.name != "manifest" {
				h.transcodeSegmentOpen = func(path string) (transcodeSegmentFile, error) {
					file, err := os.Open(path)
					if err != nil {
						return nil, err
					}
					servedSegment = &trackingTranscodeSegmentFile{File: file}
					return servedSegment, nil
				}
			}
			outputDir := filepath.Join(t.TempDir(), "transcode", "shared-files")
			g1Transcode := startNativeFileTranscode(t, "shared-files", outputDir)
			if err := writeNativeTranscodeFiles(outputDir, "G1"); err != nil {
				t.Fatal(err)
			}
			h.tm.RegisterTranscodeSession("shared-files", g1Transcode)
			t.Cleanup(func() { h.tm.CloseTranscodeSession("shared-files", "") })

			g1Card := playback.NewRecipeCard(1, "profile-1", 42, "", g1Transcode.Opts()).WithSessionIdentity("g1", startedAt)
			token := h.signSessionToken(g1Card)
			captureEntered := make(chan struct{})
			releaseCapture := make(chan struct{})
			replacementFinished := make(chan struct{})
			var replacementErr error
			h.transcodeServeResourceCaptureHook = func() {
				close(captureEntered)
				<-releaseCapture
			}
			h.transcodeServeResourceCapturedHook = func() { <-replacementFinished }

			recorder := httptest.NewRecorder()
			requestDone := make(chan struct{})
			go func() {
				defer close(requestDone)
				req := playbackTestRequest(http.MethodGet, tc.path+"?st="+url.QueryEscape(token), nil, tc.params)
				if tc.rangeHeader != "" {
					req.Header.Set("Range", tc.rangeHeader)
				}
				tc.handle(h, recorder, req)
			}()
			select {
			case <-captureEntered:
			case <-time.After(2 * time.Second):
				t.Fatal("serve request did not reach resource-capture barrier")
			}

			go func() {
				defer close(replacementFinished)
				replacementErr = mgr.TerminateSessionGeneration(t.Context(), "shared-files", "g1", func() error {
					h.tm.CloseTranscodeSessionIf("shared-files", g1Transcode, "")
					g2Transcode, startErr := playback.StartTranscode(t.Context(), playback.TranscodeOpts{
						SessionID: "shared-files", InputPath: "ignored", OutputDir: outputDir, FFmpegPath: "/bin/true",
						TargetCodecVideo: "copy", TargetCodecAudio: "copy", SegmentDuration: 2,
					})
					if startErr != nil {
						return startErr
					}
					if fixtureErr := writeNativeTranscodeFiles(outputDir, "G2"); fixtureErr != nil {
						_ = g2Transcode.CloseProcess()
						return fixtureErr
					}
					h.tm.RegisterTranscodeSession("shared-files", g2Transcode)
					return nil
				})
				if replacementErr == nil {
					_, replacementErr = mgr.RegisterReconstructedChecked(t.Context(), &playback.Session{
						ID: "shared-files", Generation: "g2", UserID: 1, MediaFileID: 99, PlayMethod: playback.PlayTranscode,
					})
				}
			}()
			select {
			case <-replacementFinished:
				close(releaseCapture)
				<-requestDone
				t.Fatalf("G2 replacement completed before G1 resource capture: %v", replacementErr)
			case <-time.After(100 * time.Millisecond):
			}
			close(releaseCapture)
			select {
			case <-requestDone:
			case <-time.After(5 * time.Second):
				t.Fatal("serve request did not complete")
			}
			if replacementErr != nil {
				t.Fatalf("install G2: %v", replacementErr)
			}
			bodyMatches := recorder.Body.String() == tc.wantBody
			if tc.name == "manifest" {
				bodyMatches = strings.Contains(recorder.Body.String(), tc.wantBody)
			}
			if recorder.Code != tc.wantStatus || !bodyMatches {
				t.Fatalf("stale G1 serve status=%d body=%q, want status=%d body=%q", recorder.Code, recorder.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if strings.Contains(recorder.Body.String(), "G2") {
				t.Fatalf("stale G1 serve leaked G2 bytes: %q", recorder.Body.String())
			}
			if servedSegment != nil && !servedSegment.closed.Load() {
				t.Fatal("served segment handle remained open")
			}
			if tc.rangeHeader != "" && recorder.Header().Get("Content-Range") != "bytes 3-9/16" {
				t.Fatalf("Content-Range = %q", recorder.Header().Get("Content-Range"))
			}
		})
	}
}

func TestNativeRemoteTranscodeServeAcquiresResponseBeforeSameTransportReplacement(t *testing.T) {
	for _, tc := range []struct {
		name       string
		path       string
		params     map[string]string
		partial    bool
		wantStatus int
		wantBody   string
		handle     func(*PlaybackHandler, http.ResponseWriter, *http.Request)
	}{
		{name: "manifest", path: "/playback/transcode/shared-remote/master.m3u8", params: map[string]string{"session_id": "shared-remote"}, wantStatus: http.StatusOK, wantBody: "G1-manifest", handle: func(h *PlaybackHandler, w http.ResponseWriter, r *http.Request) { h.HandleGetTranscodeManifest(w, r) }},
		{name: "segment_partial_response", path: "/playback/transcode/shared-remote/segment/seg_0.m4s", params: map[string]string{"session_id": "shared-remote", "name": "seg_0.m4s"}, partial: true, wantStatus: http.StatusPartialContent, wantBody: "G1-range", handle: func(h *PlaybackHandler, w http.ResponseWriter, r *http.Request) { h.HandleGetTranscodeSegment(w, r) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstreamEntered := make(chan struct{})
			releaseUpstream := make(chan struct{})
			var nodeGeneration atomic.Value
			nodeGeneration.Store("G1")
			node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				close(upstreamEntered)
				<-releaseUpstream
				generation := nodeGeneration.Load().(string)
				w.Header().Set("X-Test-Generation", generation)
				if tc.partial {
					w.Header().Set("Content-Range", "bytes 3-9/16")
					w.WriteHeader(http.StatusPartialContent)
					_, _ = w.Write([]byte(generation + "-range"))
					return
				}
				_, _ = w.Write([]byte(generation + "-manifest"))
			}))
			t.Cleanup(node.Close)

			mgr := playback.NewSessionManager(0, 0)
			startedAt := time.Now().UTC().Add(-time.Minute)
			if got, err := mgr.RegisterReconstructedChecked(t.Context(), &playback.Session{
				ID: "shared-remote", Generation: "g1", UserID: 1, MediaFileID: 42,
				PlayMethod: playback.PlayTranscode, TranscodeNodeURL: node.URL,
				TranscodeTransportID: "shared-transport", StartedAt: startedAt,
			}); err != nil || got == nil {
				t.Fatalf("register G1: session=%+v err=%v", got, err)
			}
			h := NewPlaybackHandler(mgr)
			replacementFinished := make(chan struct{})
			var replacementErr error
			h.transcodeServeResourceCapturedHook = func() { <-replacementFinished }

			recorder := httptest.NewRecorder()
			requestDone := make(chan struct{})
			go func() {
				defer close(requestDone)
				req := playbackTestRequest(http.MethodGet, tc.path, nil, tc.params)
				tc.handle(h, recorder, req)
			}()
			select {
			case <-upstreamEntered:
			case <-time.After(2 * time.Second):
				t.Fatal("G1 proxy request did not reach upstream")
			}

			go func() {
				defer close(replacementFinished)
				replacementErr = mgr.TerminateSessionGeneration(t.Context(), "shared-remote", "g1", func() error {
					nodeGeneration.Store("G2")
					return nil
				})
				if replacementErr == nil {
					_, replacementErr = mgr.RegisterReconstructedChecked(t.Context(), &playback.Session{
						ID: "shared-remote", Generation: "g2", UserID: 1, MediaFileID: 99,
						PlayMethod: playback.PlayTranscode, TranscodeNodeURL: node.URL,
						TranscodeTransportID: "shared-transport", StartedAt: time.Now().UTC(),
					})
				}
			}()
			select {
			case <-replacementFinished:
				close(releaseUpstream)
				<-requestDone
				t.Fatalf("G2 replacement completed before G1 upstream response acquisition: %v", replacementErr)
			case <-time.After(100 * time.Millisecond):
			}
			close(releaseUpstream)
			select {
			case <-requestDone:
			case <-time.After(5 * time.Second):
				t.Fatal("G1 proxy request did not complete")
			}
			if replacementErr != nil {
				t.Fatalf("install G2: %v", replacementErr)
			}
			if recorder.Code != tc.wantStatus || recorder.Body.String() != tc.wantBody {
				t.Fatalf("stale G1 proxy status=%d body=%q, want status=%d body=%q", recorder.Code, recorder.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if recorder.Header().Get("X-Test-Generation") != "G1" || strings.Contains(recorder.Body.String(), "G2") {
				t.Fatalf("stale G1 proxy leaked successor response: headers=%v body=%q", recorder.Header(), recorder.Body.String())
			}
			if tc.partial && recorder.Header().Get("Content-Range") != "bytes 3-9/16" {
				t.Fatalf("Content-Range = %q", recorder.Header().Get("Content-Range"))
			}
		})
	}
}

type playbackProxyRoundTripFunc func(*http.Request) (*http.Response, error)

func (f playbackProxyRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type trackingProxyBody struct {
	reader  io.Reader
	readErr error
	closed  atomic.Bool
}

func (b *trackingProxyBody) Read(p []byte) (int, error) {
	if b.readErr != nil {
		return 0, b.readErr
	}
	return b.reader.Read(p)
}

func (b *trackingProxyBody) Close() error {
	b.closed.Store(true)
	return nil
}

func TestNativeRemoteTranscodeServeClosesCapturedResponseOnEveryPath(t *testing.T) {
	for _, tc := range []struct {
		name         string
		segment      bool
		roundTripErr error
		bodyErr      error
		touchError   bool
		badURL       bool
		wantStatus   int
	}{
		{name: "request_build_error_preserves_mapping", badURL: true, wantStatus: http.StatusInternalServerError},
		{name: "transport_error_releases_lease", roundTripErr: errors.New("injected upstream failure"), wantStatus: http.StatusBadGateway},
		{name: "manifest_read_error_closes_body", bodyErr: errors.New("injected manifest read failure"), wantStatus: http.StatusBadGateway},
		{name: "post_capture_error_closes_body", touchError: true, wantStatus: http.StatusInternalServerError},
		{name: "client_cancel_read_closes_body", segment: true, bodyErr: context.Canceled, wantStatus: http.StatusOK},
		{name: "success_closes_body", wantStatus: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr := playback.NewSessionManager(0, 0)
			startedAt := time.Now().UTC().Add(-time.Minute)
			nodeURL := "http://node.invalid"
			if tc.badURL {
				nodeURL = "://invalid"
			}
			if got, err := mgr.RegisterReconstructedChecked(t.Context(), &playback.Session{
				ID: "remote-close", Generation: "g1", UserID: 1, MediaFileID: 42,
				PlayMethod: playback.PlayTranscode, TranscodeNodeURL: nodeURL,
				TranscodeTransportID: "remote-close-transport", StartedAt: startedAt,
			}); err != nil || got == nil {
				t.Fatalf("register session: session=%+v err=%v", got, err)
			}
			var handlerSessionMgr SessionManagerInterface = mgr
			if tc.touchError {
				handlerSessionMgr = &touchErrorSessionManager{SessionManager: mgr}
			}
			h := NewPlaybackHandler(handlerSessionMgr)
			h.JWTSecret = "remote-close-secret"
			var body *trackingProxyBody
			h.transcodeProxyClient = &http.Client{Transport: playbackProxyRoundTripFunc(func(*http.Request) (*http.Response, error) {
				if tc.roundTripErr != nil {
					return nil, tc.roundTripErr
				}
				body = &trackingProxyBody{reader: strings.NewReader("#EXTM3U\nsegment.ts\n"), readErr: tc.bodyErr}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       body,
				}, nil
			})}

			card := playback.NewRecipeCard(1, "profile-1", 42, nodeURL, playback.TranscodeOpts{SessionID: "remote-close"}).WithSessionIdentity("g1", startedAt)
			target := "/playback/transcode/remote-close/master.m3u8?st=" + url.QueryEscape(h.signSessionToken(card))
			params := map[string]string{"session_id": "remote-close"}
			handle := h.HandleGetTranscodeManifest
			if tc.segment {
				target = "/playback/transcode/remote-close/segment/seg_0.ts"
				params["name"] = "seg_0.ts"
				handle = h.HandleGetTranscodeSegment
			}
			recorder := httptest.NewRecorder()
			handle(recorder, playbackTestRequest(http.MethodGet, target, nil, params))
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d body=%q, want %d", recorder.Code, recorder.Body.String(), tc.wantStatus)
			}
			if body != nil && !body.closed.Load() {
				t.Fatal("captured upstream response body remained open")
			}

			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if err := mgr.TerminateSessionGeneration(ctx, "remote-close", "g1", nil); err != nil {
				t.Fatalf("generation lease remained held after proxy completion: %v", err)
			}
		})
	}
}

type trackingTranscodeSegmentFile struct {
	*os.File
	statErr error
	closed  atomic.Bool
}

type touchErrorSessionManager struct {
	*playback.SessionManager
}

func (m *touchErrorSessionManager) TouchActivity(string) error {
	return errors.New("injected touch failure")
}

func (f *trackingTranscodeSegmentFile) Stat() (os.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	return f.File.Stat()
}

func (f *trackingTranscodeSegmentFile) Close() error {
	f.closed.Store(true)
	return f.File.Close()
}

func TestNativeTranscodeSegmentCaptureCleansUpEarlyErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		openErr    bool
		statErr    bool
		touchError bool
	}{
		{name: "open_error", openErr: true},
		{name: "stat_error_closes_handle", statErr: true},
		{name: "post_capture_error_closes_handle", touchError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr := playback.NewSessionManager(0, 0)
			if got, err := mgr.RegisterReconstructedChecked(t.Context(), &playback.Session{
				ID: "capture-error", Generation: "g1", UserID: 1, MediaFileID: 42,
				PlayMethod: playback.PlayTranscode, StartedAt: time.Now().UTC().Add(-time.Minute),
			}); err != nil || got == nil {
				t.Fatalf("register session: session=%+v err=%v", got, err)
			}
			var handlerSessionMgr SessionManagerInterface = mgr
			if tc.touchError {
				handlerSessionMgr = &touchErrorSessionManager{SessionManager: mgr}
			}
			h := NewPlaybackHandler(handlerSessionMgr)
			outputDir := filepath.Join(t.TempDir(), "capture-error")
			transcode := startNativeFileTranscode(t, "capture-error", outputDir)
			if err := writeNativeTranscodeFiles(outputDir, "G1"); err != nil {
				t.Fatal(err)
			}
			h.tm.RegisterTranscodeSession("capture-error", transcode)
			t.Cleanup(func() { h.tm.CloseTranscodeSession("capture-error", "") })

			var tracked *trackingTranscodeSegmentFile
			if tc.openErr {
				h.transcodeSegmentOpen = func(string) (transcodeSegmentFile, error) {
					return nil, errors.New("injected open failure")
				}
			} else {
				h.transcodeSegmentOpen = func(path string) (transcodeSegmentFile, error) {
					file, err := os.Open(path)
					if err != nil {
						return nil, err
					}
					tracked = &trackingTranscodeSegmentFile{File: file}
					if tc.statErr {
						tracked.statErr = errors.New("injected stat failure")
					}
					return tracked, nil
				}
			}

			recorder := httptest.NewRecorder()
			h.HandleGetTranscodeSegment(recorder, playbackTestRequest(http.MethodGet,
				"/playback/transcode/capture-error/segment/seg_0.m4s", nil,
				map[string]string{"session_id": "capture-error", "name": "seg_0.m4s"}))
			if recorder.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d body=%q", recorder.Code, recorder.Body.String())
			}
			if tracked != nil && !tracked.closed.Load() {
				t.Fatal("segment handle remained open after early failure")
			}

			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if err := mgr.TerminateSessionGeneration(ctx, "capture-error", "g1", nil); err != nil {
				t.Fatalf("generation lease remained held after capture error: %v", err)
			}
		})
	}
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
		16,
	)
	if copyResp.CanSeekAnywhere {
		t.Fatal("copy-mode response should require explicit restart seeks")
	}
	if math.Abs(copyResp.PlayerStartSeconds-2.261) > 0.0001 {
		t.Fatalf("copy-mode PlayerStartSeconds = %v, want 2.261", copyResp.PlayerStartSeconds)
	}
	if copyResp.StreamOriginSeconds != 16 {
		t.Fatalf("copy-mode StreamOriginSeconds = %v, want 16", copyResp.StreamOriginSeconds)
	}
	if copyResp.TimelineOffsetSeconds != 16 {
		t.Fatalf("copy-mode TimelineOffsetSeconds = %v, want 16", copyResp.TimelineOffsetSeconds)
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
		0,
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

func TestHandleStartPlayback_AudioLanguageResolvesCanonically(t *testing.T) {
	// The default audio track comes from the canonical playback.audio_language
	// value resolved through the settings contract, not from the legacy
	// user_profiles.language column. The column always carries the language of
	// a different track than the canonical answer, so a regression to reading
	// it flips the selected index.
	newFile := func(t *testing.T) *models.MediaFile {
		return &models.MediaFile{
			ID:        42,
			ContentID: "movie-1",
			FilePath:  writePlaybackTestMediaFile(t, "movie.mkv"),
			Duration:  3600,
			AudioTracks: []models.AudioTrack{
				{Language: "eng", Codec: "aac", Default: true},
				{Language: "jpn", Codec: "aac"},
			},
		}
	}

	setLegacyLanguage := func(t *testing.T, store userstore.UserStore, language string) {
		t.Helper()
		if err := store.UpdateProfile(context.Background(), "profile-1", userstore.UpdateProfileInput{
			Language: &language,
		}); err != nil {
			t.Fatalf("seed legacy language column: %v", err)
		}
	}

	startPlayback := func(t *testing.T, store userstore.UserStore, file *models.MediaFile) playbackSessionResponse {
		t.Helper()
		handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
		handler.StoreProvider = testUserStoreProvider{store: store}
		handler.ItemAccess = allowAllPlaybackItemAccess{}

		req := httptest.NewRequest("POST", "/api/v1/playback/start",
			strings.NewReader(`{"file_id":42,"profile_id":"profile-1","play_method":"direct"}`))
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
		return resp
	}

	t.Run("canonical value wins over legacy column", func(t *testing.T) {
		store := newPlaybackTestStore(t)
		setLegacyLanguage(t, store, "eng")
		if _, err := store.UpsertSettingValue(context.Background(), userstore.SettingIdentity{
			Key:       settingskeys.PlaybackAudioLanguage,
			Scope:     settingscontract.ScopeProfile,
			ProfileID: "profile-1",
		}, json.RawMessage(`"ja"`)); err != nil {
			t.Fatalf("seed canonical audio language: %v", err)
		}

		resp := startPlayback(t, store, newFile(t))
		if resp.AudioTrackIndex != 1 {
			t.Fatalf("AudioTrackIndex = %d, want 1 (canonical \"ja\" track)", resp.AudioTrackIndex)
		}
	})

	t.Run("legacy column alone no longer selects a track", func(t *testing.T) {
		store := newPlaybackTestStore(t)
		setLegacyLanguage(t, store, "jpn")

		resp := startPlayback(t, store, newFile(t))
		// No canonical value stored: the contract default is "no preference",
		// so selection falls to the file's default track, not the column's.
		if resp.AudioTrackIndex != 0 {
			t.Fatalf("AudioTrackIndex = %d, want 0 (file default track)", resp.AudioTrackIndex)
		}
	})
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
		strings.NewReader(`{"session_id":"`+startResp.SessionID+`","seek_seconds":0,"target_resolution":"2160p","target_codec_video":"h264","target_codec_audio":"aac","target_bitrate_kbps":10000,"segment_duration":2,"subtitle_track_index":-1,"subtitle_burn_in":false}`),
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
	if remoteStartReq.TargetResolution != transcodeResolution1080p {
		t.Fatalf("remote target resolution = %q, want 1080p", remoteStartReq.TargetResolution)
	}
	if remoteStartReq.TargetBitrateKbps != 10000 {
		t.Fatalf("remote target bitrate = %d, want unchanged 10000", remoteStartReq.TargetBitrateKbps)
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
	if session.TargetResolution != transcodeResolution1080p {
		t.Fatalf("session target resolution = %q, want 1080p", session.TargetResolution)
	}
	if session.TargetBitrateKbps != 10000 {
		t.Fatalf("session target bitrate = %d, want unchanged 10000", session.TargetBitrateKbps)
	}
}

// newRemoteAudioSwitchSession builds a session that is already an offloaded
// (remote) transcode with a populated target recipe, mirroring the post-start
// state HandleChangeAudioTrack sees for an offloaded transcode.
func newRemoteAudioSwitchSession(t *testing.T, sessionMgr *playback.SessionManager, file *models.MediaFile, nodeURL string) string {
	t.Helper()
	session, err := sessionMgr.StartSession(1, "profile-1", file.ID, playback.PlayTranscode, true)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if err := sessionMgr.UpdateStreamState(session.ID, playback.SessionStreamState{
		PlayMethod:        playback.PlayTranscode,
		BasePlayMethod:    playback.PlayTranscode,
		AudioTrackIndex:   0,
		TranscodeAudio:    true,
		TargetResolution:  "720p",
		TargetVideoCodec:  "h264",
		TargetAudioCodec:  "aac",
		TargetBitrateKbps: 2000,
		TranscodeHWAccel:  "none",
	}); err != nil {
		t.Fatalf("UpdateStreamState: %v", err)
	}
	if err := sessionMgr.SetTranscodeNodeURL(session.ID, nodeURL); err != nil {
		t.Fatalf("SetTranscodeNodeURL: %v", err)
	}
	return session.ID
}

func TestHandleChangeAudioTrack_RechecksVideoTranscodePermission(t *testing.T) {
	transcodingDisabled := false
	sessionMgr := playback.NewSessionManager(0, 0)
	sessionMgr.SetLimitProvider(func(context.Context, int) (playback.SessionLimits, error) {
		return playback.SessionLimits{TranscodingDisabled: transcodingDisabled}, nil
	})
	file := &models.MediaFile{
		ID: 42,
		AudioTracks: []models.AudioTrack{
			{Codec: "aac", Default: true},
			{Codec: "aac"},
		},
	}
	session, err := sessionMgr.StartSession(1, "profile-1", file.ID, playback.PlayTranscode, true)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if err := sessionMgr.UpdateStreamState(session.ID, playback.SessionStreamState{
		PlayMethod:        playback.PlayTranscode,
		BasePlayMethod:    playback.PlayTranscode,
		AudioTrackIndex:   0,
		TranscodeAudio:    true,
		TargetVideoCodec:  "h264",
		TargetAudioCodec:  "aac",
		TargetResolution:  "720p",
		TargetBitrateKbps: 2000,
	}); err != nil {
		t.Fatalf("UpdateStreamState: %v", err)
	}
	transcodingDisabled = true

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	request := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/playback/"+session.ID+"/audio",
		strings.NewReader(`{"audio_track_index":1,"position":10}`),
	)
	request = request.WithContext(newAuthorizedPlaybackContext())
	request = withPlaybackRouteParam(request, "session_id", session.ID)

	response := httptest.NewRecorder()
	handler.HandleChangeAudioTrack(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body = %s", response.Code, http.StatusForbidden, response.Body.String())
	}
	var body errorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error != "transcoding_disabled" {
		t.Fatalf("error = %q, want transcoding_disabled", body.Error)
	}
	updated, err := sessionMgr.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if updated.AudioTrackIndex != 0 {
		t.Fatalf("AudioTrackIndex = %d, want unchanged 0", updated.AudioTrackIndex)
	}
}

func TestHandleChangeAudioTrack_AllowsAudioOnlyTranscodeWhenVideoDisabled(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	sessionMgr.SetLimitProvider(func(context.Context, int) (playback.SessionLimits, error) {
		return playback.SessionLimits{TranscodingDisabled: true}, nil
	})
	file := &models.MediaFile{
		ID: 42,
		AudioTracks: []models.AudioTrack{
			{Codec: "aac", Default: true},
			{Codec: "ac3"},
		},
	}
	session, err := sessionMgr.StartSession(1, "profile-1", file.ID, playback.PlayRemux, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	request := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/playback/"+session.ID+"/audio",
		strings.NewReader(`{"audio_track_index":1,"position":10}`),
	)
	request = request.WithContext(newAuthorizedPlaybackContext())
	request = withPlaybackRouteParam(request, "session_id", session.ID)

	response := httptest.NewRecorder()
	handler.HandleChangeAudioTrack(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	updated, err := sessionMgr.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !updated.TranscodeAudio {
		t.Fatal("TranscodeAudio = false, want true")
	}
}

// TestHandleChangeAudioTrack_RemoteTranscodeRestartsNodeAndMintsFullRecipe
// verifies BUG A + BUG B: an audio switch on an offloaded transcode POSTs a
// fresh /transcode/start to the node carrying the NEW AudioTrackIndex (so the
// node's ffmpeg actually switches tracks), and the returned proxy URL carries a
// recipe-COMPLETE stream token (not identity-only) so a later node restart can
// reconstruct with the switched audio.
func TestHandleChangeAudioTrack_RemoteTranscodeRestartsNodeAndMintsFullRecipe(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	file := &models.MediaFile{
		ID:         42,
		ContentID:  "movie-1",
		FilePath:   writePlaybackTestMediaFile(t, "movie.mkv"),
		Resolution: "1080p",
		CodecVideo: "h264",
		CodecAudio: "aac",
		Container:  "mkv",
		Bitrate:    8000,
		Duration:   3600,
		AudioTracks: []models.AudioTrack{
			{Codec: "aac", Default: true},
			{Codec: "ac3"},
		},
	}

	var remoteStartRequests int
	var remoteStartReq transcodenode.TranscodeStartRequest
	var remoteDeletes []string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			remoteStartRequests++
			if err := json.NewDecoder(r.Body).Decode(&remoteStartReq); err != nil {
				t.Errorf("decode remote start request: %v", err)
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(transcodenode.TranscodeStartResponse{
				SessionID: remoteStartReq.SessionID,
				Status:    "accepted",
				HWAccel:   "none",
			})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/transcode/"):
			remoteDeletes = append(remoteDeletes, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected remote request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer remote.Close()

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.JWTSecret = "test-secret"

	group := "rack-a"
	proxies := nodepool.NewProxyPool()
	proxies.SetNodes([]*nodepool.Node{{
		ID: 1, Name: "proxy-1", Type: nodepool.NodeTypeProxy, URL: "http://proxy-1",
		Enabled: true, Healthy: true, Group: &group,
	}})
	transcodes := nodepool.NewTranscodePool()
	transcodes.SetNodes([]*nodepool.Node{{
		ID: 2, Name: "transcode-1", Type: nodepool.NodeTypeTranscode, URL: remote.URL,
		Enabled: true, Healthy: true, Group: &group,
	}})
	handler.NodePlanner = nodepool.NewPlanner(proxies, transcodes)

	sessionID := newRemoteAudioSwitchSession(t, sessionMgr, file, remote.URL)

	changeReq := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/playback/"+sessionID+"/audio",
		strings.NewReader(`{"audio_track_index":1,"position":121.5}`),
	)
	changeReq = changeReq.WithContext(newAuthorizedPlaybackContext())
	changeReq = withPlaybackRouteParam(changeReq, "session_id", sessionID)

	rr := httptest.NewRecorder()
	handler.HandleChangeAudioTrack(rr, changeReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("change status = %d, body = %s", rr.Code, rr.Body.String())
	}

	// BUG A: the node must have been told to restart on the new track.
	if remoteStartRequests != 1 {
		t.Fatalf("remote /transcode/start calls = %d, want 1", remoteStartRequests)
	}
	if remoteStartReq.AudioTrackIndex != 1 {
		t.Fatalf("remote AudioTrackIndex = %d, want 1", remoteStartReq.AudioTrackIndex)
	}
	if remoteStartReq.SessionID == sessionID {
		t.Fatalf("remote SessionID = public ID %q, want generation-bound transport", sessionID)
	}
	if remoteStartReq.RequireReady {
		t.Fatal("encoded audio switch unexpectedly changed to legacy copy replacement lifecycle")
	}
	if len(remoteDeletes) != 1 || remoteDeletes[0] != "/transcode/"+sessionID {
		t.Fatalf("remote DELETEs = %v, want rollout cleanup of bare predecessor", remoteDeletes)
	}
	// Seek 121.5s with the node-default 2s segments => align to 120s / segment 60.
	if remoteStartReq.StartSegmentNumber != 60 {
		t.Fatalf("remote StartSegmentNumber = %d, want 60", remoteStartReq.StartSegmentNumber)
	}
	if remoteStartReq.SeekSeconds != 120 {
		t.Fatalf("remote SeekSeconds = %v, want aligned 120", remoteStartReq.SeekSeconds)
	}
	if remoteStartReq.TargetResolution != "720p" || remoteStartReq.TargetCodecVideo != "h264" {
		t.Fatalf("remote target recipe = %q/%q, want 720p/h264", remoteStartReq.TargetResolution, remoteStartReq.TargetCodecVideo)
	}

	var resp changeAudioResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode change response: %v", err)
	}

	// BUG B: the returned proxy URL token must be recipe-COMPLETE.
	prefix := "http://proxy-1/stream/transcode/"
	suffix := "/master.m3u8"
	if !strings.HasPrefix(resp.StreamURL, prefix) || !strings.HasSuffix(resp.StreamURL, suffix) {
		t.Fatalf("stream URL = %q, want proxy transcode manifest URL", resp.StreamURL)
	}
	token := strings.TrimSuffix(strings.TrimPrefix(resp.StreamURL, prefix), suffix)
	claims, err := streamtoken.Verify(token, handler.JWTSecret)
	if err != nil {
		t.Fatalf("verify stream token: %v", err)
	}
	if claims.AudioTrackIndex != 1 {
		t.Fatalf("token AudioTrackIndex = %d, want 1", claims.AudioTrackIndex)
	}
	// Identity-only claims would leave these zero; a full recipe carries them.
	if claims.TargetCodec != "h264" {
		t.Fatalf("token TargetCodec (video) = %q, want h264 (recipe-complete)", claims.TargetCodec)
	}
	if claims.TargetCodecAudio != "aac" {
		t.Fatalf("token TargetCodecAudio = %q, want aac (recipe-complete)", claims.TargetCodecAudio)
	}
	if claims.TargetRes != "720p" {
		t.Fatalf("token TargetRes = %q, want 720p (recipe-complete)", claims.TargetRes)
	}
	if claims.TargetBitrateKbps != 2000 {
		t.Fatalf("token TargetBitrateKbps = %d, want 2000 (recipe-complete)", claims.TargetBitrateKbps)
	}
	if claims.SeekSeconds != 120 {
		t.Fatalf("token SeekSeconds = %v, want aligned 120", claims.SeekSeconds)
	}
	if claims.StartSegmentNumber != 60 {
		t.Fatalf("token StartSegmentNumber = %d, want 60", claims.StartSegmentNumber)
	}
	// SegmentDuration must be > 0: the node treats a zero/omitted value as an
	// incomplete token and falls back to a recipe store the native path never
	// populates, which would 404 on a node restart. This guards that regression.
	if claims.SegmentDuration != playback.DefaultSegmentDuration {
		t.Fatalf("token SegmentDuration = %d, want %d (recipe-complete)", claims.SegmentDuration, playback.DefaultSegmentDuration)
	}
	if claims.MediaPath != file.FilePath {
		t.Fatalf("token MediaPath = %q, want %q", claims.MediaPath, file.FilePath)
	}
	if claims.TranscodeNode != remote.URL {
		t.Fatalf("token TranscodeNode = %q, want %q", claims.TranscodeNode, remote.URL)
	}
	if claims.TranscodeTransportID != remoteStartReq.SessionID {
		t.Fatalf("token TranscodeTransportID = %q, want %q", claims.TranscodeTransportID, remoteStartReq.SessionID)
	}
	updated, err := sessionMgr.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if claims.SessionGeneration != updated.Generation {
		t.Fatalf("token generation = %q, want %q", claims.SessionGeneration, updated.Generation)
	}
	expectedTransportID, err := playback.GenerationBoundTranscodeTransportID(sessionID, updated.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if remoteStartReq.SessionID != expectedTransportID || remoteStartReq.PublicSessionID != sessionID || remoteStartReq.SessionGeneration != updated.Generation {
		t.Fatalf("remote generation identity = (%q,%q,%q), want (%q,%q,%q)", remoteStartReq.SessionID, remoteStartReq.PublicSessionID, remoteStartReq.SessionGeneration, expectedTransportID, sessionID, updated.Generation)
	}
	claimStartedAt, err := time.Parse(time.RFC3339Nano, claims.StartedAt)
	if err != nil || !claimStartedAt.Equal(updated.StartedAt) {
		t.Fatalf("token started_at = %q, want %s (parse error: %v)", claims.StartedAt, updated.StartedAt, err)
	}
	if updated.TranscodeNodeURL != remote.URL || updated.TranscodeTransportID != expectedTransportID {
		t.Fatalf("published transcode route = %q/%q", updated.TranscodeNodeURL, updated.TranscodeTransportID)
	}
}

func TestHandleChangeAudioTrack_RemoteCopyRestartUsesFreshSeekAnchor(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	file := &models.MediaFile{
		ID:         42,
		ContentID:  "movie-1",
		FilePath:   writePlaybackTestMediaFile(t, "movie-copy.mkv"),
		Resolution: "1080p",
		CodecVideo: "hevc",
		CodecAudio: "ac3",
		Container:  "mkv",
		Bitrate:    8000,
		Duration:   3600,
		AudioTracks: []models.AudioTrack{
			{Codec: "ac3", Default: true},
			{Codec: "dts"},
		},
	}

	var remoteStartReq transcodenode.TranscodeStartRequest
	var remoteDeletes []string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			if err := json.NewDecoder(r.Body).Decode(&remoteStartReq); err != nil {
				t.Errorf("decode remote start request: %v", err)
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(transcodenode.TranscodeStartResponse{
				SessionID: remoteStartReq.SessionID,
				Status:    "accepted",
				HWAccel:   "none",
			})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/transcode/"):
			remoteDeletes = append(remoteDeletes, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected remote request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer remote.Close()

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.JWTSecret = "test-secret"
	handler.copySeekAnchor = func(context.Context, string, string, float64, int) (float64, int, error) {
		return 96, 48, nil
	}
	group := "rack-a"
	proxies := nodepool.NewProxyPool()
	proxies.SetNodes([]*nodepool.Node{{
		ID: 1, Name: "proxy-1", Type: nodepool.NodeTypeProxy, URL: "http://proxy-1",
		Enabled: true, Healthy: true, Group: &group,
	}})
	transcodes := nodepool.NewTranscodePool()
	transcodes.SetNodes([]*nodepool.Node{{
		ID: 2, Name: "transcode-1", Type: nodepool.NodeTypeTranscode, URL: remote.URL,
		Enabled: true, Healthy: true, Group: &group,
	}})
	handler.NodePlanner = nodepool.NewPlanner(proxies, transcodes)

	session, err := sessionMgr.StartSession(1, "profile-1", file.ID, playback.PlayRemux, true)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if err := sessionMgr.UpdateStreamState(session.ID, playback.SessionStreamState{
		PlayMethod:       playback.PlayTranscode,
		BasePlayMethod:   playback.PlayRemux,
		AudioTrackIndex:  0,
		TranscodeAudio:   true,
		TargetVideoCodec: "copy",
		TargetAudioCodec: "aac",
		TranscodeHWAccel: "none",
		SegmentDuration:  2,
	}); err != nil {
		t.Fatalf("UpdateStreamState: %v", err)
	}
	if err := sessionMgr.SetTranscodeNodeURL(session.ID, remote.URL); err != nil {
		t.Fatalf("SetTranscodeNodeURL: %v", err)
	}

	changeReq := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/playback/"+session.ID+"/audio",
		strings.NewReader(`{"audio_track_index":1,"position":100}`),
	).WithContext(newAuthorizedPlaybackContext())
	changeReq = withPlaybackRouteParam(changeReq, "session_id", session.ID)

	rr := httptest.NewRecorder()
	handler.HandleChangeAudioTrack(rr, changeReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("change status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if remoteStartReq.TargetCodecVideo != "copy" || remoteStartReq.SeekSeconds != 100 ||
		remoteStartReq.StreamOriginSeconds != 96 || !remoteStartReq.CopySeekAnchorResolved ||
		remoteStartReq.StartSegmentNumber != 48 {
		t.Fatalf("remote copy restart recipe = %+v", remoteStartReq)
	}
	expectedTransportID, err := playback.GenerationBoundTranscodeTransportID(session.ID, session.Generation)
	if err != nil || remoteStartReq.SessionID != expectedTransportID || remoteStartReq.PublicSessionID != session.ID || remoteStartReq.SessionGeneration != session.Generation {
		t.Fatalf("remote identity = (%q,%q,%q), want generation-bound %q: %v", remoteStartReq.SessionID, remoteStartReq.PublicSessionID, remoteStartReq.SessionGeneration, expectedTransportID, err)
	}
	if remoteStartReq.RequireReady {
		t.Fatal("generation-bound same-transport restart unexpectedly required successor readiness")
	}
	if len(remoteDeletes) != 1 || remoteDeletes[0] != "/transcode/"+session.ID {
		t.Fatalf("remote DELETEs = %v, want predecessor %q", remoteDeletes, session.ID)
	}

	var resp changeAudioResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode change response: %v", err)
	}
	token := strings.TrimSuffix(strings.TrimPrefix(resp.StreamURL, "http://proxy-1/stream/transcode/"), "/master.m3u8")
	claims, err := streamtoken.Verify(token, handler.JWTSecret)
	if err != nil {
		t.Fatalf("verify stream token: %v", err)
	}
	if claims.TargetCodec != "copy" || claims.SeekSeconds != 100 ||
		claims.StreamOriginSeconds != 96 || !claims.CopySeekAnchorResolved ||
		claims.StartSegmentNumber != 48 {
		t.Fatalf("remote copy reconstruction claims = %+v", claims)
	}
	if claims.TranscodeTransportID != remoteStartReq.SessionID {
		t.Fatalf("token TranscodeTransportID = %q, want %q", claims.TranscodeTransportID, remoteStartReq.SessionID)
	}
}

func TestHandleChangeAudioTrack_RemoteCopyRejectionPreservesPredecessorState(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	file := &models.MediaFile{
		ID:          42,
		ContentID:   "movie-1",
		FilePath:    writePlaybackTestMediaFile(t, "movie-copy-rejected.mkv"),
		Resolution:  "1080p",
		CodecVideo:  "hevc",
		CodecAudio:  "ac3",
		Container:   "mkv",
		Bitrate:     8000,
		Duration:    3600,
		AudioTracks: []models.AudioTrack{{Codec: "ac3", Default: true}, {Codec: "dts"}},
	}

	var replacementID string
	var deletedIDs []string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			var startReq transcodenode.TranscodeStartRequest
			if err := json.NewDecoder(r.Body).Decode(&startReq); err != nil {
				t.Errorf("decode remote start request: %v", err)
			}
			replacementID = startReq.SessionID
			http.Error(w, "rejected", http.StatusInternalServerError)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/transcode/"):
			deletedIDs = append(deletedIDs, strings.TrimPrefix(r.URL.Path, "/transcode/"))
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected remote request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer remote.Close()

	session, err := sessionMgr.StartSession(1, "profile-1", file.ID, playback.PlayRemux, true)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if err := sessionMgr.UpdateStreamState(session.ID, playback.SessionStreamState{
		PlayMethod:       playback.PlayTranscode,
		BasePlayMethod:   playback.PlayRemux,
		AudioTrackIndex:  0,
		TranscodeAudio:   true,
		TargetVideoCodec: "copy",
		TargetAudioCodec: "aac",
		TranscodeHWAccel: "none",
		SegmentDuration:  2,
	}); err != nil {
		t.Fatalf("UpdateStreamState: %v", err)
	}
	if err := sessionMgr.SetTranscodeNodeURL(session.ID, remote.URL); err != nil {
		t.Fatalf("SetTranscodeNodeURL: %v", err)
	}

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.JWTSecret = "test-secret"
	handler.copySeekAnchor = func(context.Context, string, string, float64, int) (float64, int, error) {
		return 96, 48, nil
	}
	group := "rack-a"
	proxies := nodepool.NewProxyPool()
	proxies.SetNodes([]*nodepool.Node{{
		ID: 1, Name: "proxy-1", Type: nodepool.NodeTypeProxy, URL: "http://proxy-1",
		Enabled: true, Healthy: true, Group: &group,
	}})
	transcodes := nodepool.NewTranscodePool()
	transcodes.SetNodes([]*nodepool.Node{{
		ID: 2, Name: "transcode-1", Type: nodepool.NodeTypeTranscode, URL: remote.URL,
		Enabled: true, Healthy: true, Group: &group,
	}})
	handler.NodePlanner = nodepool.NewPlanner(proxies, transcodes)

	request := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/playback/"+session.ID+"/audio",
		strings.NewReader(`{"audio_track_index":1,"position":100}`),
	).WithContext(newAuthorizedPlaybackContext())
	request = withPlaybackRouteParam(request, "session_id", session.ID)
	response := httptest.NewRecorder()
	handler.HandleChangeAudioTrack(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d, body = %s", response.Code, http.StatusBadGateway, response.Body.String())
	}
	expectedTransportID, err := playback.GenerationBoundTranscodeTransportID(session.ID, session.Generation)
	if err != nil || replacementID != expectedTransportID {
		t.Fatalf("replacement ID = %q, want generation-bound %q: %v", replacementID, expectedTransportID, err)
	}
	if len(deletedIDs) != 0 {
		t.Fatalf("deleted transports = %v, want rejected same-transport restart untouched", deletedIDs)
	}
	persisted, err := sessionMgr.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if persisted.TranscodeNodeURL != remote.URL || persisted.TranscodeTransportID != "" {
		t.Fatalf("predecessor route changed after rejection: %q/%q", persisted.TranscodeNodeURL, persisted.TranscodeTransportID)
	}
	if persisted.AudioTrackIndex != 0 || persisted.BasePlayMethod != playback.PlayRemux ||
		persisted.TargetVideoCodec != "copy" || persisted.TargetAudioCodec != "aac" {
		t.Fatalf("predecessor state changed after rejection: %+v", persisted)
	}
}

// TestHandleChangeAudioTrack_RemoteTranscodeNodeUnreachableSurfacesError
// verifies the chosen error posture: when the transcode node cannot be reached
// for the restart, the handler surfaces a 502 rather than returning a 200 that
// clients (silo-android / silo-apple) would trust as "audio switched".
func TestHandleChangeAudioTrack_RemoteTranscodeNodeUnreachableSurfacesError(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	file := &models.MediaFile{
		ID:         42,
		ContentID:  "movie-1",
		FilePath:   writePlaybackTestMediaFile(t, "movie.mkv"),
		Resolution: "1080p",
		CodecVideo: "h264",
		CodecAudio: "aac",
		Container:  "mkv",
		Bitrate:    8000,
		Duration:   3600,
		AudioTracks: []models.AudioTrack{
			{Codec: "aac", Default: true},
			{Codec: "ac3"},
		},
	}

	// A node URL that nothing is listening on => connection refused.
	deadNodeURL := "http://127.0.0.1:1"

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.JWTSecret = "test-secret"

	group := "rack-a"
	proxies := nodepool.NewProxyPool()
	proxies.SetNodes([]*nodepool.Node{{
		ID: 1, Name: "proxy-1", Type: nodepool.NodeTypeProxy, URL: "http://proxy-1",
		Enabled: true, Healthy: true, Group: &group,
	}})
	transcodes := nodepool.NewTranscodePool()
	transcodes.SetNodes([]*nodepool.Node{{
		ID: 2, Name: "transcode-1", Type: nodepool.NodeTypeTranscode, URL: deadNodeURL,
		Enabled: true, Healthy: true, Group: &group,
	}})
	handler.NodePlanner = nodepool.NewPlanner(proxies, transcodes)

	sessionID := newRemoteAudioSwitchSession(t, sessionMgr, file, deadNodeURL)

	changeReq := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/playback/"+sessionID+"/audio",
		strings.NewReader(`{"audio_track_index":1,"position":0}`),
	)
	changeReq = changeReq.WithContext(newAuthorizedPlaybackContext())
	changeReq = withPlaybackRouteParam(changeReq, "session_id", sessionID)

	rr := httptest.NewRecorder()
	handler.HandleChangeAudioTrack(rr, changeReq)

	// Chosen posture: surface the failure (mirror HandleStartTranscode) so a 200
	// never lies to the client about an audio switch that didn't happen.
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("change status = %d, want %d, body = %s", rr.Code, http.StatusBadGateway, rr.Body.String())
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
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())

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
		strings.NewReader(`{"session_id":"`+startResp.SessionID+`","seek_seconds":0,"target_resolution":"2160p","target_codec_video":"h264","target_codec_audio":"aac","target_bitrate_kbps":10000,"segment_duration":2,"subtitle_track_index":-1,"subtitle_burn_in":false}`),
	)
	transcodeReq = transcodeReq.WithContext(newAuthorizedPlaybackContext())

	transcodeRR := httptest.NewRecorder()
	handler.HandleStartTranscode(transcodeRR, transcodeReq)
	if transcodeRR.Code != 202 {
		t.Fatalf("transcode status = %d, body = %s", transcodeRR.Code, transcodeRR.Body.String())
	}

	transcodeSession := handler.tm.GetTranscodeSession(startResp.SessionID)
	if transcodeSession == nil {
		t.Fatal("expected local transcode session")
	}
	t.Cleanup(func() {
		_ = transcodeSession.Close()
	})
	if got := transcodeSession.Opts().AudioTrackIndex; got != 1 {
		t.Fatalf("local transcode audio track index = %d, want 1", got)
	}
	if got := transcodeSession.Opts().TargetResolution; got != transcodeResolution1080p {
		t.Fatalf("local target resolution = %q, want 1080p", got)
	}
	if got := transcodeSession.Opts().TargetBitrateKbps; got != 10000 {
		t.Fatalf("local target bitrate = %d, want unchanged 10000", got)
	}
	session, err := sessionMgr.GetSession(startResp.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if session.TargetResolution != transcodeResolution1080p {
		t.Fatalf("session target resolution = %q, want 1080p", session.TargetResolution)
	}
	if session.TargetBitrateKbps != 10000 {
		t.Fatalf("session target bitrate = %d, want unchanged 10000", session.TargetBitrateKbps)
	}
}

func TestHandleChangeAudioTrack_LocalCopyRestartUsesFreshSeekAnchor(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	file := &models.MediaFile{
		ID:          42,
		ContentID:   "movie-1",
		FilePath:    writePlaybackTestMediaFile(t, "movie-local-copy.mkv"),
		Resolution:  "1080p",
		CodecVideo:  "hevc",
		CodecAudio:  "ac3",
		Container:   "mkv",
		Bitrate:     8000,
		Duration:    3600,
		AudioTracks: []models.AudioTrack{{Codec: "ac3", Default: true}, {Codec: "dts"}},
	}
	session, err := sessionMgr.StartSession(1, "profile-1", file.ID, playback.PlayRemux, true)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.JWTSecret = "test-secret"
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	handler.copySeekAnchor = func(_ context.Context, _, _ string, requested float64, _ int) (float64, int, error) {
		switch requested {
		case 18:
			return 16, 8, nil
		case 100:
			return 96, 48, nil
		default:
			return 0, 0, fmt.Errorf("unexpected seek %v", requested)
		}
	}

	startReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/playback/transcode/start",
		strings.NewReader(`{"session_id":"`+session.ID+`","seek_seconds":18,"target_codec_video":"copy","target_codec_audio":"aac","segment_duration":2,"subtitle_track_index":-1}`),
	).WithContext(newAuthorizedPlaybackContext())
	startRR := httptest.NewRecorder()
	handler.HandleStartTranscode(startRR, startReq)
	if startRR.Code != http.StatusAccepted {
		t.Fatalf("start status = %d, body = %s", startRR.Code, startRR.Body.String())
	}

	predecessor := handler.tm.GetTranscodeSession(session.ID)
	if predecessor == nil {
		t.Fatal("expected local copy-video session")
	}
	t.Cleanup(func() { handler.tm.CloseTranscodeSession(session.ID, "") })

	changeReq := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/playback/"+session.ID+"/audio",
		strings.NewReader(`{"audio_track_index":1,"position":100}`),
	).WithContext(newAuthorizedPlaybackContext())
	changeReq = withPlaybackRouteParam(changeReq, "session_id", session.ID)
	changeRR := httptest.NewRecorder()
	handler.HandleChangeAudioTrack(changeRR, changeReq)
	if changeRR.Code != http.StatusOK {
		t.Fatalf("change status = %d, body = %s", changeRR.Code, changeRR.Body.String())
	}

	successor := handler.tm.GetTranscodeSession(session.ID)
	if successor == nil || successor == predecessor {
		t.Fatal("audio switch did not publish a prepared successor")
	}
	if predecessor.IsRunning() {
		t.Fatal("audio switch predecessor is still running after commit")
	}
	opts := successor.Opts()
	if opts.TargetCodecVideo != "copy" || opts.SeekSeconds != 100 ||
		opts.StreamOriginSeconds != 96 || !opts.CopySeekAnchorResolved ||
		opts.StartSegmentNumber != 48 || opts.AudioTrackIndex != 1 {
		t.Fatalf("local copy restart opts = %+v", opts)
	}

	var resp changeAudioResponse
	if err := json.NewDecoder(changeRR.Body).Decode(&resp); err != nil {
		t.Fatalf("decode change response: %v", err)
	}
	manifestURL, err := url.Parse(resp.StreamURL)
	if err != nil {
		t.Fatalf("parse stream URL: %v", err)
	}
	claims, err := streamtoken.Verify(manifestURL.Query().Get(streamTokenParam), handler.JWTSecret)
	if err != nil {
		t.Fatalf("verify stream token: %v", err)
	}
	if claims.TargetCodec != "copy" || claims.SeekSeconds != 100 ||
		claims.StreamOriginSeconds != 96 || !claims.CopySeekAnchorResolved ||
		claims.StartSegmentNumber != 48 || claims.AudioTrackIndex != 1 {
		t.Fatalf("local copy reconstruction claims = %+v", claims)
	}
	if claims.SessionGeneration != session.Generation {
		t.Fatalf("token generation = %q, want %q", claims.SessionGeneration, session.Generation)
	}
	claimStartedAt, err := time.Parse(time.RFC3339Nano, claims.StartedAt)
	if err != nil || !claimStartedAt.Equal(session.StartedAt) {
		t.Fatalf("token started_at = %q, want %s (parse error: %v)", claims.StartedAt, session.StartedAt, err)
	}
	if resp.PlayerStartSeconds == nil || *resp.PlayerStartSeconds != 4 ||
		resp.StreamOriginSeconds == nil || *resp.StreamOriginSeconds != 96 ||
		resp.TimelineOffsetSeconds == nil || *resp.TimelineOffsetSeconds != 96 ||
		resp.CanSeekAnywhere == nil || *resp.CanSeekAnywhere {
		t.Fatalf("local copy response timeline = %+v", resp)
	}
}

func TestHandleChangeAudioTrack_LocalCopyFailurePreservesPredecessor(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	file := &models.MediaFile{
		ID:          42,
		ContentID:   "movie-1",
		FilePath:    writePlaybackTestMediaFile(t, "movie-local-copy-failure.mkv"),
		Resolution:  "1080p",
		CodecVideo:  "hevc",
		CodecAudio:  "ac3",
		Container:   "mkv",
		Bitrate:     8000,
		Duration:    3600,
		AudioTracks: []models.AudioTrack{{Codec: "ac3", Default: true}, {Codec: "dts"}},
	}
	session, err := sessionMgr.StartSession(1, "profile-1", file.ID, playback.PlayRemux, true)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.PlaybackConfig = playbackTestConfig(
		writePlaybackTestFFmpegFailingAfterFirstStart(t),
		t.TempDir(),
	)
	handler.copySeekAnchor = func(_ context.Context, _, _ string, requested float64, _ int) (float64, int, error) {
		return requested - 4, int((requested - 4) / 2), nil
	}

	startReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/playback/transcode/start",
		strings.NewReader(`{"session_id":"`+session.ID+`","seek_seconds":18,"target_codec_video":"copy","target_codec_audio":"aac","segment_duration":2,"subtitle_track_index":-1}`),
	).WithContext(newAuthorizedPlaybackContext())
	startRR := httptest.NewRecorder()
	handler.HandleStartTranscode(startRR, startReq)
	if startRR.Code != http.StatusAccepted {
		t.Fatalf("start status = %d, body = %s", startRR.Code, startRR.Body.String())
	}
	predecessor := handler.tm.GetTranscodeSession(session.ID)
	if predecessor == nil || !predecessor.IsRunning() {
		t.Fatal("expected running predecessor")
	}
	t.Cleanup(func() { handler.tm.CloseTranscodeSession(session.ID, "") })

	changeReq := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/playback/"+session.ID+"/audio",
		strings.NewReader(`{"audio_track_index":1,"position":100}`),
	).WithContext(newAuthorizedPlaybackContext())
	changeReq = withPlaybackRouteParam(changeReq, "session_id", session.ID)
	changeRR := httptest.NewRecorder()
	handler.HandleChangeAudioTrack(changeRR, changeReq)
	if changeRR.Code != http.StatusInternalServerError {
		t.Fatalf("change status = %d, body = %s", changeRR.Code, changeRR.Body.String())
	}
	if current := handler.tm.GetTranscodeSession(session.ID); current != predecessor {
		t.Fatal("failed successor replaced the predecessor")
	}
	if !predecessor.IsRunning() {
		t.Fatal("failed successor stopped the predecessor")
	}
	persisted, err := sessionMgr.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if persisted.AudioTrackIndex != 0 {
		t.Fatalf("persisted audio track = %d, want predecessor track 0", persisted.AudioTrackIndex)
	}
}

func TestHandleStartTranscode_SeekedCopyRemainsCopyVideo(t *testing.T) {
	for _, tc := range []struct {
		name       string
		videoCodec string
	}{
		{name: "h264", videoCodec: "h264"},
		{name: "hevc", videoCodec: "hevc"},
		{name: "mpeg2", videoCodec: "mpeg2video"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessionMgr := playback.NewSessionManager(0, 0)
			file := &models.MediaFile{
				ID:          42,
				ContentID:   "movie-1",
				FilePath:    writePlaybackTestMediaFile(t, "movie-"+tc.name+".mkv"),
				Resolution:  "1080p",
				CodecVideo:  tc.videoCodec,
				CodecAudio:  "dts",
				Container:   "mkv",
				Bitrate:     25000,
				Duration:    3600,
				AudioTracks: []models.AudioTrack{{Codec: "dts", Default: true}},
			}
			session, err := sessionMgr.StartSession(1, "profile-1", file.ID, playback.PlayRemux, true)
			if err != nil {
				t.Fatalf("StartSession: %v", err)
			}

			handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
			handler.ItemAccess = allowAllPlaybackItemAccess{}
			handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
			handler.copySeekAnchor = func(context.Context, string, string, float64, int) (float64, int, error) {
				return 16, 8, nil
			}

			transcodeReq := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/playback/transcode/start",
				strings.NewReader(`{"session_id":"`+session.ID+`","seek_seconds":18.261,"target_resolution":"","target_codec_video":"copy","target_codec_audio":"aac","target_bitrate_kbps":0,"segment_duration":2,"subtitle_track_index":-1,"subtitle_burn_in":false}`),
			).WithContext(newAuthorizedPlaybackContext())

			transcodeRR := httptest.NewRecorder()
			handler.HandleStartTranscode(transcodeRR, transcodeReq)
			if transcodeRR.Code != http.StatusAccepted {
				t.Fatalf("transcode status = %d, body = %s", transcodeRR.Code, transcodeRR.Body.String())
			}

			var response transcodeStartResponse
			if err := json.NewDecoder(transcodeRR.Body).Decode(&response); err != nil {
				t.Fatalf("decode transcode response: %v", err)
			}
			if math.Abs(response.PlayerStartSeconds-2.261) > 0.0001 || response.StreamOriginSeconds != 16 ||
				response.TimelineOffsetSeconds != 16 || response.CanSeekAnywhere {
				t.Fatalf("copy response timeline = %+v", response)
			}

			transcodeSession := handler.tm.GetTranscodeSession(session.ID)
			if transcodeSession == nil {
				t.Fatal("expected local transcode session")
			}
			t.Cleanup(func() { _ = transcodeSession.Close() })
			opts := transcodeSession.Opts()
			if opts.TargetCodecVideo != "copy" || opts.SourceVideoCodec != tc.videoCodec || opts.TargetCodecAudio != "aac" {
				t.Fatalf("copy recipe = video %q source %q audio %q", opts.TargetCodecVideo, opts.SourceVideoCodec, opts.TargetCodecAudio)
			}
			if opts.SeekSeconds != 18.261 || opts.StreamOriginSeconds != 16 || !opts.CopySeekAnchorResolved || opts.StartSegmentNumber != 8 || opts.TargetResolution != "" {
				t.Fatalf("copy seek recipe = seek %v origin %v segment %d resolution %q", opts.SeekSeconds, opts.StreamOriginSeconds, opts.StartSegmentNumber, opts.TargetResolution)
			}

			persisted, err := sessionMgr.GetSession(session.ID)
			if err != nil {
				t.Fatalf("GetSession: %v", err)
			}
			if persisted.TargetVideoCodec != "copy" || semanticPlayMethod(persisted) != playback.PlayRemux {
				t.Fatalf("persisted recipe = target %q semantic method %q", persisted.TargetVideoCodec, semanticPlayMethod(persisted))
			}
		})
	}
}

func TestHandleStartTranscode_CopyAnchorFailureKeepsActiveTransport(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	file := &models.MediaFile{
		ID:          42,
		ContentID:   "movie-1",
		FilePath:    writePlaybackTestMediaFile(t, "movie-anchor-failure.mkv"),
		Resolution:  "1080p",
		CodecVideo:  "h264",
		CodecAudio:  "aac",
		Container:   "mkv",
		Bitrate:     8000,
		Duration:    3600,
		AudioTracks: []models.AudioTrack{{Codec: "aac", Default: true}},
	}
	session, err := sessionMgr.StartSession(1, "profile-1", file.ID, playback.PlayTranscode, true)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())

	initialReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/playback/transcode/start",
		strings.NewReader(`{"session_id":"`+session.ID+`","seek_seconds":0,"target_resolution":"720p","target_codec_video":"h264","target_codec_audio":"aac","target_bitrate_kbps":2000,"segment_duration":2,"subtitle_track_index":-1}`),
	).WithContext(newAuthorizedPlaybackContext())
	initialRR := httptest.NewRecorder()
	handler.HandleStartTranscode(initialRR, initialReq)
	if initialRR.Code != http.StatusAccepted {
		t.Fatalf("initial start status = %d, body = %s", initialRR.Code, initialRR.Body.String())
	}
	active := handler.tm.GetTranscodeSession(session.ID)
	if active == nil || !active.IsRunning() {
		t.Fatal("expected initial transcode to be running")
	}
	t.Cleanup(func() { _ = active.Close() })

	handler.copySeekAnchor = func(context.Context, string, string, float64, int) (float64, int, error) {
		return 0, 0, errors.New("probe failed")
	}
	replacementReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/playback/transcode/start",
		strings.NewReader(`{"session_id":"`+session.ID+`","seek_seconds":100,"target_codec_video":"copy","target_codec_audio":"aac","segment_duration":2,"subtitle_track_index":-1}`),
	).WithContext(newAuthorizedPlaybackContext())
	replacementRR := httptest.NewRecorder()
	handler.HandleStartTranscode(replacementRR, replacementReq)
	if replacementRR.Code != http.StatusInternalServerError {
		t.Fatalf("replacement status = %d, body = %s", replacementRR.Code, replacementRR.Body.String())
	}
	if got := handler.tm.GetTranscodeSession(session.ID); got != active {
		t.Fatalf("active transcode was replaced after failed preflight: got %p, want %p", got, active)
	}
	if !active.IsRunning() {
		t.Fatal("active transcode was stopped before copy seek anchor preflight completed")
	}
}

func TestHandleStartTranscode_SeekedCopyPreservedOnRemoteNode(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	file := &models.MediaFile{
		ID:          42,
		ContentID:   "movie-1",
		FilePath:    writePlaybackTestMediaFile(t, "movie-remote.mkv"),
		Resolution:  "1080p",
		CodecVideo:  "h264",
		CodecAudio:  "ac3",
		Container:   "mkv",
		Bitrate:     8000,
		Duration:    3600,
		AudioTracks: []models.AudioTrack{{Codec: "ac3", Default: true}},
	}
	session, err := sessionMgr.StartSession(1, "profile-1", file.ID, playback.PlayRemux, true)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	var remoteStartReq transcodenode.TranscodeStartRequest
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/transcode/start" {
			t.Errorf("unexpected remote request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&remoteStartReq); err != nil {
			t.Errorf("decode remote start request: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(transcodenode.TranscodeStartResponse{
			SessionID: remoteStartReq.SessionID,
			Status:    "accepted",
			HWAccel:   "none",
		})
	}))
	defer remote.Close()

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.JWTSecret = "test-secret"
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	handler.copySeekAnchor = func(context.Context, string, string, float64, int) (float64, int, error) {
		return 16, 8, nil
	}
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{
		Name: "transcode-1", Type: nodepool.NodeTypeTranscode, URL: remote.URL,
		Enabled: true, Healthy: true,
	}})
	handler.NodePlanner = nodepool.NewPlanner(nodepool.NewProxyPool(), pool)

	transcodeReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/playback/transcode/start",
		strings.NewReader(`{"session_id":"`+session.ID+`","seek_seconds":18.261,"target_resolution":"2160p","target_codec_video":"copy","target_codec_audio":"aac","target_bitrate_kbps":0,"segment_duration":2,"subtitle_track_index":-1,"subtitle_burn_in":false}`),
	).WithContext(newAuthorizedPlaybackContext())
	transcodeRR := httptest.NewRecorder()
	handler.HandleStartTranscode(transcodeRR, transcodeReq)
	if transcodeRR.Code != http.StatusAccepted {
		t.Fatalf("transcode status = %d, body = %s", transcodeRR.Code, transcodeRR.Body.String())
	}
	if remoteStartReq.TargetCodecVideo != "copy" || remoteStartReq.SeekSeconds != 18.261 ||
		remoteStartReq.StreamOriginSeconds != 16 || !remoteStartReq.CopySeekAnchorResolved || remoteStartReq.StartSegmentNumber != 8 {
		t.Fatalf("remote copy recipe = codec %q seek %v origin %v segment %d", remoteStartReq.TargetCodecVideo, remoteStartReq.SeekSeconds, remoteStartReq.StreamOriginSeconds, remoteStartReq.StartSegmentNumber)
	}
	if remoteStartReq.TargetResolution != transcodeResolution2160p {
		t.Fatalf("remote copy target resolution = %q, want unchanged 2160p", remoteStartReq.TargetResolution)
	}
	expectedTransportID, err := playback.GenerationBoundTranscodeTransportID(session.ID, session.Generation)
	if err != nil || remoteStartReq.SessionID != expectedTransportID || remoteStartReq.PublicSessionID != session.ID || remoteStartReq.SessionGeneration != session.Generation {
		t.Fatalf("remote identity = (%q,%q,%q), want generation-bound %q: %v", remoteStartReq.SessionID, remoteStartReq.PublicSessionID, remoteStartReq.SessionGeneration, expectedTransportID, err)
	}
	if remoteStartReq.RequireReady {
		t.Fatal("generation-bound same-transport start unexpectedly required successor readiness")
	}

	var response transcodeStartResponse
	if err := json.NewDecoder(transcodeRR.Body).Decode(&response); err != nil {
		t.Fatalf("decode transcode response: %v", err)
	}
	if math.Abs(response.PlayerStartSeconds-2.261) > 0.0001 || response.StreamOriginSeconds != 16 ||
		response.TimelineOffsetSeconds != 16 || response.CanSeekAnywhere {
		t.Fatalf("remote copy response timeline = %+v", response)
	}
	manifestURL, err := url.Parse(response.ManifestURL)
	if err != nil {
		t.Fatalf("parse manifest URL: %v", err)
	}
	claims, err := streamtoken.Verify(manifestURL.Query().Get(streamTokenParam), handler.JWTSecret)
	if err != nil {
		t.Fatalf("verify stream token: %v", err)
	}
	if claims.TargetCodec != "copy" || claims.SeekSeconds != 18.261 || claims.StreamOriginSeconds != 16 || !claims.CopySeekAnchorResolved ||
		claims.StartSegmentNumber != 8 || claims.TranscodeNode != remote.URL {
		t.Fatalf("remote reconstruction claims = codec %q seek %v origin %v segment %d node %q", claims.TargetCodec, claims.SeekSeconds, claims.StreamOriginSeconds, claims.StartSegmentNumber, claims.TranscodeNode)
	}
	if claims.TranscodeTransportID != remoteStartReq.SessionID {
		t.Fatalf("remote reconstruction transport = %q, want %q", claims.TranscodeTransportID, remoteStartReq.SessionID)
	}
	persisted, err := sessionMgr.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if persisted.TranscodeNodeURL != remote.URL || persisted.TranscodeTransportID != remoteStartReq.SessionID {
		t.Fatalf("published remote route = %q/%q", persisted.TranscodeNodeURL, persisted.TranscodeTransportID)
	}
}

func TestHandleStartTranscode_RemoteRejectionPreservesLegacyPredecessor(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	file := &models.MediaFile{
		ID:          42,
		ContentID:   "movie-1",
		FilePath:    writePlaybackTestMediaFile(t, "movie-remote-reject.mkv"),
		Resolution:  "1080p",
		CodecVideo:  "h264",
		CodecAudio:  "ac3",
		Container:   "mkv",
		Bitrate:     8000,
		Duration:    3600,
		AudioTracks: []models.AudioTrack{{Codec: "ac3", Default: true}},
	}
	session, err := sessionMgr.StartSession(1, "profile-1", file.ID, playback.PlayRemux, true)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	var replacementID string
	var deletedIDs []string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			var startReq transcodenode.TranscodeStartRequest
			if err := json.NewDecoder(r.Body).Decode(&startReq); err != nil {
				t.Errorf("decode remote start request: %v", err)
			}
			replacementID = startReq.SessionID
			http.Error(w, "rejected", http.StatusInternalServerError)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/transcode/"):
			deletedIDs = append(deletedIDs, strings.TrimPrefix(r.URL.Path, "/transcode/"))
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected remote request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer remote.Close()
	if err := sessionMgr.SetTranscodeNodeURL(session.ID, remote.URL); err != nil {
		t.Fatalf("SetTranscodeNodeURL: %v", err)
	}

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.JWTSecret = "test-secret"
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	handler.copySeekAnchor = func(context.Context, string, string, float64, int) (float64, int, error) {
		return 16, 8, nil
	}
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{
		Name: "transcode-1", Type: nodepool.NodeTypeTranscode, URL: remote.URL,
		Enabled: true, Healthy: true,
	}})
	handler.NodePlanner = nodepool.NewPlanner(nodepool.NewProxyPool(), pool)

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/playback/transcode/start",
		strings.NewReader(`{"session_id":"`+session.ID+`","seek_seconds":18,"target_codec_video":"copy","target_codec_audio":"aac","segment_duration":2,"subtitle_track_index":-1}`),
	).WithContext(newAuthorizedPlaybackContext())
	response := httptest.NewRecorder()
	handler.HandleStartTranscode(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d, body = %s", response.Code, http.StatusBadGateway, response.Body.String())
	}
	expectedTransportID, err := playback.GenerationBoundTranscodeTransportID(session.ID, session.Generation)
	if err != nil || replacementID != expectedTransportID {
		t.Fatalf("replacement ID = %q, want generation-bound %q: %v", replacementID, expectedTransportID, err)
	}
	if len(deletedIDs) != 0 {
		t.Fatalf("deleted transports = %v, want rejected same-transport start untouched", deletedIDs)
	}
	persisted, err := sessionMgr.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if persisted.TranscodeNodeURL != remote.URL || persisted.TranscodeTransportID != "" {
		t.Fatalf("predecessor route changed after rejection: %q/%q", persisted.TranscodeNodeURL, persisted.TranscodeTransportID)
	}
}

func TestHandleStartTranscode_ConcurrentRemoteReplacementsPublishOneWinner(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	file := &models.MediaFile{
		ID:          42,
		ContentID:   "movie-1",
		FilePath:    writePlaybackTestMediaFile(t, "movie-remote-concurrent.mkv"),
		Resolution:  "1080p",
		CodecVideo:  "h264",
		CodecAudio:  "ac3",
		Container:   "mkv",
		Bitrate:     8000,
		Duration:    3600,
		AudioTracks: []models.AudioTrack{{Codec: "ac3", Default: true}},
	}
	session, err := sessionMgr.StartSession(1, "profile-1", file.ID, playback.PlayRemux, true)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	var remoteMu sync.Mutex
	var preparedIDs []string
	var deletedIDs []string
	prepared := make(chan struct{}, 2)
	release := make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			var startReq transcodenode.TranscodeStartRequest
			if err := json.NewDecoder(r.Body).Decode(&startReq); err != nil {
				t.Errorf("decode remote start request: %v", err)
			}
			remoteMu.Lock()
			preparedIDs = append(preparedIDs, startReq.SessionID)
			remoteMu.Unlock()
			prepared <- struct{}{}
			<-release
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(transcodenode.TranscodeStartResponse{
				SessionID: startReq.SessionID,
				Status:    "accepted",
				HWAccel:   "none",
			})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/transcode/"):
			remoteMu.Lock()
			deletedIDs = append(deletedIDs, strings.TrimPrefix(r.URL.Path, "/transcode/"))
			remoteMu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected remote request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer remote.Close()
	if err := sessionMgr.SetTranscodeNodeURL(session.ID, remote.URL); err != nil {
		t.Fatalf("SetTranscodeNodeURL: %v", err)
	}

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.JWTSecret = "test-secret"
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	handler.copySeekAnchor = func(context.Context, string, string, float64, int) (float64, int, error) {
		return 16, 8, nil
	}
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{
		Name: "transcode-1", Type: nodepool.NodeTypeTranscode, URL: remote.URL,
		Enabled: true, Healthy: true,
	}})
	handler.NodePlanner = nodepool.NewPlanner(nodepool.NewProxyPool(), pool)

	responses := make(chan int, 2)
	start := func() {
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/playback/transcode/start",
			strings.NewReader(`{"session_id":"`+session.ID+`","seek_seconds":18,"target_codec_video":"copy","target_codec_audio":"aac","segment_duration":2,"subtitle_track_index":-1}`),
		).WithContext(newAuthorizedPlaybackContext())
		response := httptest.NewRecorder()
		handler.HandleStartTranscode(response, request)
		responses <- response.Code
	}
	go start()
	go start()
	<-prepared
	select {
	case <-prepared:
		t.Fatal("same-generation remote starts were not serialized")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-prepared

	statuses := []int{<-responses, <-responses}
	sort.Ints(statuses)
	if statuses[0] != http.StatusAccepted || statuses[1] != http.StatusAccepted {
		t.Fatalf("replacement statuses = %v, want two serialized accepts", statuses)
	}
	persisted, err := sessionMgr.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	remoteMu.Lock()
	preparedSnapshot := append([]string(nil), preparedIDs...)
	deletedSnapshot := append([]string(nil), deletedIDs...)
	remoteMu.Unlock()
	if len(preparedSnapshot) != 2 || preparedSnapshot[0] != preparedSnapshot[1] {
		t.Fatalf("prepared transport IDs = %v, want one stable generation transport", preparedSnapshot)
	}
	winner := persisted.TranscodeTransportID
	if winner != preparedSnapshot[0] && winner != preparedSnapshot[1] {
		t.Fatalf("published transport = %q, not one of %v", winner, preparedSnapshot)
	}
	sort.Strings(deletedSnapshot)
	if len(deletedSnapshot) != 1 || deletedSnapshot[0] != session.ID {
		t.Fatalf("deleted transports = %v, want only bare rollout predecessor", deletedSnapshot)
	}
}

func TestHandleStartTranscode_RemoteSuccessorCannotBeOverwrittenByStaleFinalization(t *testing.T) {
	baseSessionMgr := playback.NewSessionManager(0, 0)
	sessionMgr := &firstBlockingSessionManager{
		SessionManager: baseSessionMgr,
		entered:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	file := &models.MediaFile{
		ID:          42,
		ContentID:   "movie-1",
		FilePath:    writePlaybackTestMediaFile(t, "movie-remote-state-order.mkv"),
		Resolution:  "1080p",
		CodecVideo:  "h264",
		CodecAudio:  "ac3",
		Container:   "mkv",
		Bitrate:     8000,
		Duration:    3600,
		AudioTracks: []models.AudioTrack{{Codec: "ac3", Default: true}},
	}
	session, err := sessionMgr.StartSession(1, "profile-1", file.ID, playback.PlayRemux, true)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	var remoteMu sync.Mutex
	preparedBitrates := make(map[string]int)
	preparedCount := 0
	var deletedIDs []string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			var startReq transcodenode.TranscodeStartRequest
			if err := json.NewDecoder(r.Body).Decode(&startReq); err != nil {
				t.Errorf("decode remote start request: %v", err)
			}
			remoteMu.Lock()
			preparedBitrates[startReq.SessionID] = startReq.TargetBitrateKbps
			preparedCount++
			remoteMu.Unlock()
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(transcodenode.TranscodeStartResponse{
				SessionID: startReq.SessionID,
				Status:    "accepted",
				HWAccel:   "none",
			})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/transcode/"):
			deletedID := strings.TrimPrefix(r.URL.Path, "/transcode/")
			remoteMu.Lock()
			deletedIDs = append(deletedIDs, deletedID)
			remoteMu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected remote request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer remote.Close()
	if err := sessionMgr.SetTranscodeNodeURL(session.ID, remote.URL); err != nil {
		t.Fatalf("SetTranscodeNodeURL: %v", err)
	}

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.JWTSecret = "test-secret"
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	handler.copySeekAnchor = func(context.Context, string, string, float64, int) (float64, int, error) {
		return 16, 8, nil
	}
	pool := nodepool.NewTranscodePool()
	pool.SetNodes([]*nodepool.Node{{
		Name: "transcode-1", Type: nodepool.NodeTypeTranscode, URL: remote.URL,
		Enabled: true, Healthy: true,
	}})
	handler.NodePlanner = nodepool.NewPlanner(nodepool.NewProxyPool(), pool)

	start := func(bitrate int) <-chan int {
		result := make(chan int, 1)
		go func() {
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/playback/transcode/start",
				strings.NewReader(fmt.Sprintf(
					`{"session_id":%q,"seek_seconds":18,"target_codec_video":"copy","target_codec_audio":"aac","target_bitrate_kbps":%d,"segment_duration":2,"subtitle_track_index":-1}`,
					session.ID,
					bitrate,
				)),
			).WithContext(newAuthorizedPlaybackContext())
			response := httptest.NewRecorder()
			handler.HandleStartTranscode(response, request)
			result <- response.Code
		}()
		return result
	}

	firstResult := start(1111)
	<-sessionMgr.entered
	secondResult := start(2222)

	// The exact generation lifecycle guard serializes same-transport starts: the
	// second request must not replace the node process before the first durable
	// publication completes.
	time.Sleep(50 * time.Millisecond)
	remoteMu.Lock()
	countBeforeRelease := preparedCount
	remoteMu.Unlock()
	if countBeforeRelease != 1 {
		t.Fatalf("remote starts before release = %d, want 1", countBeforeRelease)
	}
	close(sessionMgr.release)

	if status := <-firstResult; status != http.StatusAccepted {
		t.Fatalf("first status = %d, want %d", status, http.StatusAccepted)
	}
	if status := <-secondResult; status != http.StatusAccepted {
		t.Fatalf("serialized second status = %d, want %d", status, http.StatusAccepted)
	}
	persisted, err := sessionMgr.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	remoteMu.Lock()
	winnerBitrate := preparedBitrates[persisted.TranscodeTransportID]
	deletedSnapshot := append([]string(nil), deletedIDs...)
	remoteMu.Unlock()
	if winnerBitrate != 2222 {
		t.Fatalf("published transport bitrate = %d, want serialized last writer bitrate 2222", winnerBitrate)
	}
	if persisted.TargetBitrateKbps != winnerBitrate {
		t.Fatalf("persisted bitrate = %d, want route winner bitrate %d", persisted.TargetBitrateKbps, winnerBitrate)
	}
	if len(deletedSnapshot) != 1 || deletedSnapshot[0] != session.ID {
		t.Fatalf("deleted transports = %v, want only bare rollout predecessor", deletedSnapshot)
	}
}

func TestHandleStartTranscode_ConcurrentLocalEncodedStartsRemainLastWriterWins(t *testing.T) {
	baseSessionMgr := playback.NewSessionManager(0, 0)
	sessionMgr := &firstBlockingSessionManager{
		SessionManager: baseSessionMgr,
		entered:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	file := &models.MediaFile{
		ID:          42,
		ContentID:   "movie-1",
		FilePath:    writePlaybackTestMediaFile(t, "movie-local-encoded-overlap.mkv"),
		Resolution:  "2160p",
		CodecVideo:  "hevc",
		CodecAudio:  "ac3",
		Container:   "mkv",
		Bitrate:     25000,
		Duration:    3600,
		AudioTracks: []models.AudioTrack{{Codec: "ac3", Default: true}},
	}
	session, err := sessionMgr.StartSession(1, "profile-1", file.ID, playback.PlayTranscode, true)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	t.Cleanup(func() { handler.tm.CloseTranscodeSession(session.ID, "") })

	start := func(bitrate int) <-chan int {
		result := make(chan int, 1)
		go func() {
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/playback/transcode/start",
				strings.NewReader(fmt.Sprintf(
					`{"session_id":%q,"target_resolution":"1080p","target_codec_video":"h264","target_codec_audio":"aac","target_bitrate_kbps":%d,"segment_duration":2,"subtitle_track_index":-1}`,
					session.ID,
					bitrate,
				)),
			).WithContext(newAuthorizedPlaybackContext())
			response := httptest.NewRecorder()
			handler.HandleStartTranscode(response, request)
			result <- response.Code
		}()
		return result
	}

	firstResult := start(1111)
	<-sessionMgr.entered
	secondResult := start(2222)
	close(sessionMgr.release)
	if status := <-firstResult; status != http.StatusAccepted {
		t.Fatalf("first status = %d, want %d", status, http.StatusAccepted)
	}
	if status := <-secondResult; status != http.StatusAccepted {
		t.Fatalf("second status = %d, want %d", status, http.StatusAccepted)
	}
	persisted, err := sessionMgr.GetSession(session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if persisted.TargetBitrateKbps != 2222 {
		t.Fatalf("persisted bitrate = %d, want last writer bitrate 2222", persisted.TargetBitrateKbps)
	}
}

func TestHandleStartTranscode_SeekedCopyAllowedWhenVideoTranscodingDisabled(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	sessionMgr.SetLimitProvider(func(context.Context, int) (playback.SessionLimits, error) {
		return playback.SessionLimits{TranscodingDisabled: true}, nil
	})
	file := &models.MediaFile{
		ID:          42,
		ContentID:   "movie-1",
		FilePath:    writePlaybackTestMediaFile(t, "movie-copy-permission.mkv"),
		Resolution:  "1080p",
		CodecVideo:  "h264",
		CodecAudio:  "ac3",
		Container:   "mkv",
		Bitrate:     8000,
		Duration:    3600,
		AudioTracks: []models.AudioTrack{{Codec: "ac3", Default: true}},
	}
	session, err := sessionMgr.StartSession(1, "profile-1", file.ID, playback.PlayRemux, true)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	handler.copySeekAnchor = func(context.Context, string, string, float64, int) (float64, int, error) {
		return 16, 8, nil
	}
	transcodeReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/playback/transcode/start",
		strings.NewReader(`{"session_id":"`+session.ID+`","seek_seconds":18.261,"target_resolution":"","target_codec_video":"copy","target_codec_audio":"aac","target_bitrate_kbps":0,"segment_duration":2,"subtitle_track_index":-1,"subtitle_burn_in":false}`),
	).WithContext(newAuthorizedPlaybackContext())
	transcodeRR := httptest.NewRecorder()
	handler.HandleStartTranscode(transcodeRR, transcodeReq)
	if transcodeRR.Code != http.StatusAccepted {
		t.Fatalf("transcode status = %d, body = %s", transcodeRR.Code, transcodeRR.Body.String())
	}
	transcodeSession := handler.tm.GetTranscodeSession(session.ID)
	if transcodeSession == nil {
		t.Fatal("expected local copy-video session")
	}
	t.Cleanup(func() { _ = transcodeSession.Close() })
	if got := transcodeSession.Opts().TargetCodecVideo; got != "copy" {
		t.Fatalf("target video codec = %q, want copy", got)
	}
	if got := transcodeSession.Opts().TargetResolution; got != "" {
		t.Fatalf("copy-video target resolution = %q, want existing empty recipe", got)
	}
}

func TestHandleStartTranscode_SubtitleBurnInRechecksVideoTranscodePermission(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	sessionMgr.SetLimitProvider(func(context.Context, int) (playback.SessionLimits, error) {
		return playback.SessionLimits{TranscodingDisabled: true}, nil
	})
	file := &models.MediaFile{
		ID:          42,
		ContentID:   "movie-1",
		FilePath:    writePlaybackTestMediaFile(t, "movie-burn-in.mkv"),
		Resolution:  "1080p",
		CodecVideo:  "h264",
		CodecAudio:  "ac3",
		Container:   "mkv",
		Bitrate:     8000,
		Duration:    3600,
		AudioTracks: []models.AudioTrack{{Codec: "ac3", Default: true}},
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 0, Language: "en", Codec: "subrip"},
		},
	}
	session, err := sessionMgr.StartSession(1, "profile-1", file.ID, playback.PlayRemux, true)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	transcodeReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/playback/transcode/start",
		strings.NewReader(`{"session_id":"`+session.ID+`","seek_seconds":0,"target_resolution":"1080p","target_codec_video":"copy","target_codec_audio":"aac","target_bitrate_kbps":0,"segment_duration":2,"subtitle_track_index":0,"subtitle_burn_in":true}`),
	).WithContext(newAuthorizedPlaybackContext())
	transcodeRR := httptest.NewRecorder()
	handler.HandleStartTranscode(transcodeRR, transcodeReq)

	if transcodeRR.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body = %s", transcodeRR.Code, http.StatusForbidden, transcodeRR.Body.String())
	}
	var response errorResponse
	if err := json.NewDecoder(transcodeRR.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error != "transcoding_disabled" {
		t.Fatalf("error = %q, want transcoding_disabled", response.Error)
	}
	if transcodeSession := handler.tm.GetTranscodeSession(session.ID); transcodeSession != nil {
		t.Fatal("transcode session started despite disabled video transcoding")
	}
}

func TestHandleStartTranscode_BitmapBurnInForcesEncodeAndResolvesCodec(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	filePath := writePlaybackTestMediaFile(t, "movie-pgs.mkv")
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
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 0, Language: "en", Codec: "subrip"},
			{Index: 1, Language: "en", Codec: "hdmv_pgs_subtitle"},
		},
	}
	session, err := sessionMgr.StartSession(1, "profile-1", file.ID, playback.PlayRemux, true)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())

	// A remux "original" restart that adds PGS burn-in still asks for codec
	// copy — the server must force an encoding transcode and resolve the
	// track's bitmap codec for the ffmpeg arg builder.
	transcodeReq := httptest.NewRequest(
		"POST",
		"/api/v1/playback/transcode/start",
		strings.NewReader(`{"session_id":"`+session.ID+`","seek_seconds":125.0,"target_resolution":"","target_codec_video":"copy","target_codec_audio":"aac","target_bitrate_kbps":0,"segment_duration":2,"subtitle_track_index":1,"subtitle_burn_in":true}`),
	)
	transcodeReq = transcodeReq.WithContext(newAuthorizedPlaybackContext())

	transcodeRR := httptest.NewRecorder()
	handler.HandleStartTranscode(transcodeRR, transcodeReq)
	if transcodeRR.Code != 202 {
		t.Fatalf("transcode status = %d, body = %s", transcodeRR.Code, transcodeRR.Body.String())
	}

	transcodeSession := handler.tm.GetTranscodeSession(session.ID)
	if transcodeSession == nil {
		t.Fatal("expected local transcode session")
	}
	t.Cleanup(func() {
		_ = transcodeSession.Close()
	})
	opts := transcodeSession.Opts()
	if got := opts.TargetCodecVideo; got != "h264" {
		t.Fatalf("burn-in target video codec = %q, want h264 (encode forced)", got)
	}
	if !opts.SubtitleBurnIn || opts.SubtitleTrackIndex != 1 {
		t.Fatalf("burn-in selection lost: burnIn=%v index=%d", opts.SubtitleBurnIn, opts.SubtitleTrackIndex)
	}
	if got := opts.SubtitleCodec; got != "hdmv_pgs_subtitle" {
		t.Fatalf("burn-in subtitle codec = %q, want hdmv_pgs_subtitle", got)
	}
	// Encode-forced restarts must snap the ffmpeg start to the segment
	// boundary (timeline alignment contract).
	if got := opts.SeekSeconds; got != 124.0 {
		t.Fatalf("burn-in aligned seek = %v, want 124.0 (125.0 snapped to the segment boundary)", got)
	}
}

func TestHandleStartTranscode_BitmapRestartUsesDeclaredSubtitleInventory(t *testing.T) {
	requested := &models.MediaFile{
		ID:         42,
		ContentID:  "movie-1",
		FilePath:   writePlaybackTestMediaFile(t, "movie-4k.mkv"),
		Resolution: "2160p",
		CodecVideo: "hevc",
		CodecAudio: "aac",
		Container:  "mkv",
		Duration:   3600,
		AudioTracks: []models.AudioTrack{
			{Codec: "aac", Default: true},
		},
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 0, Language: "ja", Codec: "hdmv_pgs_subtitle", Title: "Japanese"},
			{Index: 1, Language: "en", Codec: "hdmv_pgs_subtitle", Title: "English"},
		},
	}
	effective := &models.MediaFile{
		ID:         99,
		ContentID:  "movie-1",
		FilePath:   writePlaybackTestMediaFile(t, "movie-1080p.mkv"),
		Resolution: "1080p",
		CodecVideo: "h264",
		CodecAudio: "aac",
		Container:  "mkv",
		Duration:   3600,
		AudioTracks: []models.AudioTrack{
			{Codec: "aac", Default: true},
		},
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 0, Language: "en", Codec: "hdmv_pgs_subtitle", Title: "English"},
			{Index: 1, Language: "ja", Codec: "hdmv_pgs_subtitle", Title: "Japanese"},
		},
	}
	tests := []struct {
		name                string
		subtitleTrackIndex  int
		subtitleMediaFileID int
	}{
		{
			name:               "legacy request uses original inventory",
			subtitleTrackIndex: 1,
		},
		{
			name:                "declared original inventory remaps to effective file",
			subtitleTrackIndex:  1,
			subtitleMediaFileID: requested.ID,
		},
		{
			name:                "declared effective inventory keeps effective ordinal",
			subtitleTrackIndex:  0,
			subtitleMediaFileID: effective.ID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionMgr := playback.NewSessionManager(0, 0)
			session, err := sessionMgr.StartSessionWithFiles(
				1,
				"profile-1",
				effective.ID,
				requested.ID,
				playback.PlayRemux,
				true,
			)
			if err != nil {
				t.Fatalf("StartSessionWithFiles: %v", err)
			}

			handler := NewPlaybackHandler(sessionMgr, mapPlaybackFileResolver{files: map[int]*models.MediaFile{
				requested.ID: requested,
				effective.ID: effective,
			}})
			handler.ItemAccess = allowAllPlaybackItemAccess{}
			handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())

			body := map[string]any{
				"session_id":           session.ID,
				"seek_seconds":         120,
				"target_resolution":    "720p",
				"target_codec_video":   "h264",
				"target_codec_audio":   "aac",
				"target_bitrate_kbps":  2000,
				"segment_duration":     2,
				"subtitle_track_index": tt.subtitleTrackIndex,
				"subtitle_burn_in":     true,
			}
			if tt.subtitleMediaFileID > 0 {
				body["subtitle_media_file_id"] = tt.subtitleMediaFileID
			}
			encodedBody, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal transcode request: %v", err)
			}
			transcodeReq := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/playback/transcode/start",
				strings.NewReader(string(encodedBody)),
			)
			transcodeReq = transcodeReq.WithContext(newAuthorizedPlaybackContext())

			transcodeRR := httptest.NewRecorder()
			handler.HandleStartTranscode(transcodeRR, transcodeReq)
			if transcodeRR.Code != http.StatusAccepted {
				t.Fatalf("transcode status = %d, body = %s", transcodeRR.Code, transcodeRR.Body.String())
			}

			transcodeSession := handler.tm.GetTranscodeSession(session.ID)
			if transcodeSession == nil {
				t.Fatal("expected local transcode session")
			}
			t.Cleanup(func() { _ = transcodeSession.Close() })
			opts := transcodeSession.Opts()
			if opts.SubtitleTrackIndex != 0 || opts.SubtitleCodec != "hdmv_pgs_subtitle" {
				t.Fatalf("burn-in resolved to index=%d codec=%q, want English index 0 PGS", opts.SubtitleTrackIndex, opts.SubtitleCodec)
			}
		})
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
	handler.PlaybackConfig = playbackTestConfig("", writePlaybackTestMediaFile(t, "occupied-transcode-dir"))

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

func TestClampEncodedTargetResolution(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		source    string
		want      string
	}{
		{
			name:      "clamps 2160p to 1080p",
			requested: transcodeResolution2160p,
			source:    transcodeResolution1080p,
			want:      transcodeResolution1080p,
		},
		{
			name:      "keeps lower target",
			requested: transcodeResolution720p,
			source:    transcodeResolution1080p,
			want:      transcodeResolution720p,
		},
		{
			name:      "keeps equal target",
			requested: transcodeResolution1080p,
			source:    transcodeResolution1080p,
			want:      transcodeResolution1080p,
		},
		{
			name:      "keeps empty target",
			requested: "",
			source:    transcodeResolution1080p,
			want:      "",
		},
		{
			name:      "keeps unknown target",
			requested: "unrecognized-target",
			source:    transcodeResolution1080p,
			want:      "unrecognized-target",
		},
		{
			name:      "keeps target for unknown source",
			requested: transcodeResolution2160p,
			source:    "native",
			want:      transcodeResolution2160p,
		},
		{
			name:      "supports low tiers",
			requested: transcodeResolution480p,
			source:    transcodeResolution420p,
			want:      transcodeResolution420p,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampEncodedTargetResolution(tt.requested, tt.source); got != tt.want {
				t.Fatalf(
					"clampEncodedTargetResolution(%q, %q) = %q, want %q",
					tt.requested,
					tt.source,
					got,
					tt.want,
				)
			}
		})
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
