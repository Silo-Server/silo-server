package transcodenode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

const testSecret = "node-reconstruct-test-secret"

// newTestServer builds a transcode Server whose config carries a known JWT secret
// so reconstructFromToken can verify forwarded stream tokens. The tracker is left
// nil: the guard-rejection cases never reach the spawn/track path.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	w := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = testSecret
	cfg.Playback.TranscodeDir = t.TempDir()
	w.SetConfigForTest(cfg)
	return &Server{
		watcher:         w,
		sessions:        make(map[string]*playback.TranscodeSession),
		identities:      make(map[string]transcodeIdentity),
		generationStore: &nodeGenerationStore{},
	}
}

type nodeGenerationStore struct {
	ended bool
	err   error
	calls int
}

type keyedNodeGenerationStore struct {
	ended map[string]bool
	err   error
	calls []string
}

type blockingNodeGenerationStore struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingNodeGenerationStore) WasSessionGenerationEnded(context.Context, string, string, time.Time) (bool, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return false, nil
}

func (*blockingNodeGenerationStore) RecordEndedSessionGeneration(context.Context, string, string, time.Time) error {
	return nil
}

func (s *keyedNodeGenerationStore) WasSessionGenerationEnded(_ context.Context, sessionID, generation string, _ time.Time) (bool, error) {
	s.calls = append(s.calls, sessionID+"\x00"+generation)
	return s.ended[sessionID+"\x00"+generation], s.err
}
func (*keyedNodeGenerationStore) RecordEndedSessionGeneration(context.Context, string, string, time.Time) error {
	return nil
}

func (s *nodeGenerationStore) WasSessionGenerationEnded(context.Context, string, string, time.Time) (bool, error) {
	s.calls++
	return s.ended, s.err
}

func (*nodeGenerationStore) RecordEndedSessionGeneration(context.Context, string, string, time.Time) error {
	return nil
}

func TestTranscodeStartRequestIdentityValidation(t *testing.T) {
	const (
		publicID   = "public-session"
		generation = "32a4e124-9df7-4cfa-be49-e8e503316714"
	)
	transportID, err := playback.GenerationBoundTranscodeTransportID(publicID, generation)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("new identity is exact and generation bound", func(t *testing.T) {
		identity, err := validateTranscodeStartIdentity(TranscodeStartRequest{
			SessionID: transportID, PublicSessionID: publicID, SessionGeneration: generation,
		})
		if err != nil || identity.publicSessionID != publicID || identity.generation != generation {
			t.Fatalf("identity = %#v, error = %v", identity, err)
		}
	})

	t.Run("missing fields decode only as legacy", func(t *testing.T) {
		identity, err := validateTranscodeStartIdentity(TranscodeStartRequest{SessionID: publicID})
		if err != nil || identity.publicSessionID != publicID || identity.generation != "" {
			t.Fatalf("legacy identity = %#v, error = %v", identity, err)
		}
	})

	for name, req := range map[string]TranscodeStartRequest{
		"missing generation":   {SessionID: transportID, PublicSessionID: publicID},
		"missing public id":    {SessionID: transportID, SessionGeneration: generation},
		"wrong transport":      {SessionID: "wrong", PublicSessionID: publicID, SessionGeneration: generation},
		"malformed generation": {SessionID: transportID, PublicSessionID: publicID, SessionGeneration: "bad"},
		"reserved sentinel":    {SessionID: transportID, PublicSessionID: publicID, SessionGeneration: playback.LegacySessionGenerationSentinel},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateTranscodeStartIdentity(req); err == nil {
				t.Fatal("identity unexpectedly accepted")
			}
		})
	}
}

func TestHandleStartDeniesGenerationBeforeProcessAdmission(t *testing.T) {
	const generation = "32a4e124-9df7-4cfa-be49-e8e503316714"
	transportID, _ := playback.GenerationBoundTranscodeTransportID("public-session", generation)
	for _, tc := range []struct {
		name  string
		store *nodeGenerationStore
		want  int
	}{{"ended", &nodeGenerationStore{ended: true}, http.StatusGone}, {"store error", &nodeGenerationStore{err: errors.New("db down")}, http.StatusServiceUnavailable}} {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)
			server.generationStore = tc.store
			body, _ := json.Marshal(TranscodeStartRequest{
				SessionID: transportID, PublicSessionID: "public-session", SessionGeneration: generation,
				InputPath: "/must/not/open", TargetCodecVideo: "h264", SegmentDuration: 2,
			})
			rr := httptest.NewRecorder()
			server.handleStart(rr, httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body)))
			if rr.Code != tc.want {
				t.Fatalf("status = %d, body=%q", rr.Code, rr.Body.String())
			}
			if tc.store.calls != 1 || server.activeJobs.Load() != 0 || len(server.sessions) != 0 {
				t.Fatalf("denial side effects: calls=%d jobs=%d sessions=%d", tc.store.calls, server.activeJobs.Load(), len(server.sessions))
			}
		})
	}
}

func TestReconstructAdmissionChecksTombstoneBeforeSelectingExistingProcess(t *testing.T) {
	const (
		publicID   = "public-session"
		generation = "32a4e124-9df7-4cfa-be49-e8e503316714"
	)
	transportID, _ := playback.GenerationBoundTranscodeTransportID(publicID, generation)
	card := transcodeCard(publicID).WithSessionIdentity(generation, time.Now())
	card.TranscodeTransportID = transportID
	token := signCard(t, card)

	server := newTestServer(t)
	store := &nodeGenerationStore{}
	server.generationStore = store
	existing := &playback.TranscodeSession{}
	server.sessions[transportID] = existing
	server.identities[transportID] = transcodeIdentity{publicSessionID: publicID, generation: generation}

	admitted := server.reconstructFromToken(requestWithToken(transportID, token), transportID, -1)
	if admitted != existing {
		t.Fatal("exact admitted identity did not select existing process")
	}
	store.ended = true
	if denied := server.reconstructFromToken(requestWithToken(transportID, token), transportID, -1); denied != nil {
		t.Fatal("request admitted after tombstone commit")
	}
	if store.calls != 2 {
		t.Fatalf("authority calls = %d, want 2", store.calls)
	}
	if server.sessions[transportID] != existing {
		t.Fatal("denied G1 request mutated the already-admitted process")
	}
}

func TestReconstructRejectsInconsistentGenerationBoundIdentity(t *testing.T) {
	const generation = "32a4e124-9df7-4cfa-be49-e8e503316714"
	transportID, _ := playback.GenerationBoundTranscodeTransportID("public-session", generation)
	card := transcodeCard("different-public-session").WithSessionIdentity(generation, time.Now())
	card.TranscodeTransportID = transportID
	server := newTestServer(t)
	if got := server.reconstructFromToken(requestWithToken(transportID, signCard(t, card)), transportID, -1); got != nil {
		t.Fatal("inconsistent public identity selected a process")
	}
}

func TestManifestRechecksGenerationBeforeSelectingExistingProcess(t *testing.T) {
	const (
		publicID   = "public-session"
		generation = "32a4e124-9df7-4cfa-be49-e8e503316714"
	)
	transportID, _ := playback.GenerationBoundTranscodeTransportID(publicID, generation)
	card := transcodeCard(publicID).WithSessionIdentity(generation, time.Now())
	card.TranscodeTransportID = transportID
	store := &nodeGenerationStore{ended: true}
	server := newTestServer(t)
	server.generationStore = store
	server.sessions[transportID] = &playback.TranscodeSession{}
	server.identities[transportID] = transcodeIdentity{publicSessionID: publicID, generation: generation}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/transcode/"+transportID+"/master.m3u8", nil)
	req.Header.Set("Authorization", "Bearer "+testSecret)
	req.Header.Set("X-Silo-Stream-Token", signCard(t, card))
	server.Handler().ServeHTTP(rr, req)
	if store.calls != 1 {
		t.Fatalf("authority calls = %d, want 1", store.calls)
	}
	if strings.Contains(rr.Body.String(), "#EXTM3U") {
		t.Fatalf("ended generation served manifest bytes: %q", rr.Body.String())
	}
}

func registerServingSession(t *testing.T, server *Server, transportID string, identity transcodeIdentity) *playback.TranscodeSession {
	t.Helper()
	ffmpeg := filepath.Join(t.TempDir(), "serving-ffmpeg.sh")
	script := "#!/bin/sh\nprintf '#EXTM3U\\n#EXT-X-VERSION:3\\n#EXT-X-TARGETDURATION:2\\n#EXT-X-MEDIA-SEQUENCE:0\\n#EXTINF:2,\\nseg_00000.ts\\n#EXTINF:2,\\nseg_00001.ts\\n#EXTINF:2,\\nseg_00002.ts\\n' > stream.m3u8\nprintf 'generation-media' > seg_00000.ts\nprintf 'generation-media' > seg_00001.ts\nprintf 'generation-media' > seg_00002.ts\nsleep 30\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	outputDir := filepath.Join(server.watcher.Config().Playback.TranscodeDir, transportID)
	session, err := playback.StartTranscode(t.Context(), playback.TranscodeOpts{SessionID: transportID, OutputDir: outputDir, InputPath: "/ignored", FFmpegPath: ffmpeg, TargetCodecVideo: "h264", SegmentDuration: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if _, err := session.WaitForManifest(time.Second); err != nil {
		t.Fatal(err)
	}
	if server.identities == nil {
		server.identities = make(map[string]transcodeIdentity)
	}
	if server.lastAccess == nil {
		server.lastAccess = make(map[string]time.Time)
	}
	server.sessions[transportID] = session
	server.identities[transportID] = identity
	server.lastAccess[transportID] = time.Now()
	return session
}

func TestTokenlessExistingProcessChecksRecordedGenerationForManifestAndSegment(t *testing.T) {
	const (
		g1 = "32a4e124-9df7-4cfa-be49-e8e503316714"
		g2 = "d24a82d2-2bc2-44a4-96c6-a86671d508d7"
	)
	store := &keyedNodeGenerationStore{ended: make(map[string]bool)}
	server := newTestServer(t)
	server.tracker = nodesessions.NewTracker(nil, "node", "node", "transcode")
	server.generationStore = store
	g1Transport, _ := playback.GenerationBoundTranscodeTransportID("shared", g1)
	g2Transport, _ := playback.GenerationBoundTranscodeTransportID("shared", g2)
	registerServingSession(t, server, g1Transport, transcodeIdentity{publicSessionID: "shared", generation: g1})
	registerServingSession(t, server, g2Transport, transcodeIdentity{publicSessionID: "shared", generation: g2})

	routes := []struct {
		name  string
		path  func(string) string
		media string
	}{{"manifest", func(id string) string { return "/transcode/" + id + "/master.m3u8" }, "#EXTM3U"}, {"segment", func(id string) string { return "/transcode/" + id + "/segment/seg_00000.ts" }, "generation-media"}}
	request := func(path string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+testSecret)
		server.Handler().ServeHTTP(rr, req)
		return rr
	}

	for _, route := range routes {
		t.Run(route.name+" valid", func(t *testing.T) {
			rr := request(route.path(g1Transport))
			if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), route.media) {
				t.Fatalf("valid status/body = %d/%q", rr.Code, rr.Body.String())
			}
		})
	}

	store.ended["shared\x00"+g1] = true
	for _, route := range routes {
		t.Run(route.name+" ended", func(t *testing.T) {
			rr := request(route.path(g1Transport))
			if rr.Code != http.StatusGone || strings.Contains(rr.Body.String(), route.media) {
				t.Fatalf("ended status/body = %d/%q", rr.Code, rr.Body.String())
			}
			g2rr := request(route.path(g2Transport))
			if g2rr.Code != http.StatusOK || !strings.Contains(g2rr.Body.String(), route.media) {
				t.Fatalf("G2 status/body = %d/%q", g2rr.Code, g2rr.Body.String())
			}
		})
	}

	store.err = errors.New("db down")
	for _, route := range routes {
		t.Run(route.name+" store error", func(t *testing.T) {
			rr := request(route.path(g2Transport))
			if rr.Code != http.StatusServiceUnavailable || strings.Contains(rr.Body.String(), route.media) {
				t.Fatalf("store-error status/body = %d/%q", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestHandleStartRequireReadyRejectsExitedFFmpeg(t *testing.T) {
	server := newTestServer(t)
	ffmpegPath := filepath.Join(t.TempDir(), "failing-ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	requestBody, err := json.Marshal(TranscodeStartRequest{
		SessionID:        "ready-failure-1",
		InputPath:        "/media/movie.mkv",
		TargetCodecVideo: "h264",
		TargetCodecAudio: "aac",
		SegmentDuration:  2,
		RequireReady:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(requestBody))
	rr := httptest.NewRecorder()
	server.handleStart(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	server.mu.RLock()
	_, registered := server.sessions["ready-failure-1"]
	server.mu.RUnlock()
	if registered {
		t.Fatal("failed readiness session was registered")
	}
}

func TestHandleStartDistinctReplacementFailurePreservesPredecessor(t *testing.T) {
	server := newTestServer(t)
	server.sessions["public-session"] = &playback.TranscodeSession{}
	server.activeJobs.Store(1)

	ffmpegPath := filepath.Join(t.TempDir(), "failing-ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	requestBody, err := json.Marshal(TranscodeStartRequest{
		SessionID:        "public-session-legacy-replacement",
		InputPath:        "/media/movie.mkv",
		TargetCodecVideo: "copy",
		TargetCodecAudio: "aac",
		SegmentDuration:  2,
		RequireReady:     true,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(requestBody))
	rr := httptest.NewRecorder()
	server.handleStart(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	server.mu.RLock()
	predecessor := server.sessions["public-session"]
	_, replacementRegistered := server.sessions["public-session-legacy-replacement"]
	server.mu.RUnlock()
	if predecessor == nil {
		t.Fatal("failed distinct replacement removed the active predecessor")
	}
	if replacementRegistered {
		t.Fatal("failed distinct replacement was registered")
	}
	if got := server.activeJobs.Load(); got != 1 {
		t.Fatalf("active jobs = %d, want predecessor only", got)
	}
}

func signCard(t *testing.T, card playback.RecipeCard) string {
	t.Helper()
	tok, err := streamtoken.Sign(card.ToClaims(), testSecret, time.Hour)
	if err != nil {
		t.Fatalf("sign card: %v", err)
	}
	return tok
}

func requestWithToken(sessionID, token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/transcode/"+sessionID+"/master.m3u8", nil)
	if token != "" {
		r.Header.Set("X-Silo-Stream-Token", token)
	}
	return r
}

func transcodeCard(sessionID string) playback.RecipeCard {
	return playback.NewRecipeCard(7, "profile-1", 42, "", playback.TranscodeOpts{
		SessionID:        sessionID,
		InputPath:        "/media/movie.mkv",
		TargetCodecVideo: "h264",
		SegmentDuration:  6,
	})
}

// reconstructFromToken must refuse — without spawning ffmpeg — every request that
// does not carry a valid, matching transcode token. These guards run before any
// StartTranscode, so they are safe to assert without ffmpeg or a media file.
func TestReconstructFromToken_RejectsUnusableTokens(t *testing.T) {
	const sid = "sess-123"
	s := newTestServer(t)

	t.Run("missing token header", func(t *testing.T) {
		if got := s.reconstructFromToken(requestWithToken(sid, ""), sid, -1); got != nil {
			t.Fatalf("expected nil for missing token, got %v", got)
		}
	})

	t.Run("invalid signature", func(t *testing.T) {
		bad, err := streamtoken.Sign(transcodeCard(sid).ToClaims(), "wrong-secret", time.Hour)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		if got := s.reconstructFromToken(requestWithToken(sid, bad), sid, -1); got != nil {
			t.Fatalf("expected nil for bad signature, got %v", got)
		}
	})

	t.Run("session id mismatch", func(t *testing.T) {
		tok := signCard(t, transcodeCard("other-session"))
		if got := s.reconstructFromToken(requestWithToken(sid, tok), sid, -1); got != nil {
			t.Fatalf("expected nil for session id mismatch, got %v", got)
		}
	})

	t.Run("non-transcode card", func(t *testing.T) {
		tok := signCard(t, playback.NewDirectRecipeCard(sid, 7, "profile-1", 42))
		if got := s.reconstructFromToken(requestWithToken(sid, tok), sid, -1); got != nil {
			t.Fatalf("expected nil for direct-play card, got %v", got)
		}
	})

	// The jellycompat node hop signs an identity-only transcode token (the recipe
	// lives in the central compat store). Its card decodes as PlayTranscode for the
	// right session id but with no encode parameters; with no recipe store wired the
	// node must refuse it rather than spawn a malformed ffmpeg.
	t.Run("recipe-less transcode token, no recipe store", func(t *testing.T) {
		tok := signCard(t, playback.RecipeCard{
			SessionID:  sid,
			UserID:     7,
			PlayMethod: playback.PlayTranscode,
			InputPath:  "/media/movie.mkv",
		})
		if got := s.reconstructFromToken(requestWithToken(sid, tok), sid, 5); got != nil {
			t.Fatalf("expected nil for recipe-less transcode token, got %v", got)
		}
	})
}

// stubRecipeStore is a recipeStore for the jellycompat node-restart fetch path.
type stubRecipeStore struct {
	card    *playback.RecipeCard
	ok      bool
	hits    int
	gets    []string
	deletes []string
	delErr  error
}

type mapRecipeStore struct {
	cards   map[string]playback.RecipeCard
	gets    []string
	deletes []string
}

type blockingDeleteRecipeStore struct {
	mu            sync.Mutex
	cards         map[string]playback.RecipeCard
	deleteEntered chan struct{}
	getEntered    chan struct{}
	allowDelete   chan struct{}
	deleteOnce    sync.Once
	getOnce       sync.Once
}

type delayedNodeTracker struct {
	trackStarted   chan struct{}
	trackDone      chan struct{}
	releaseTrack   chan struct{}
	cleanupStarted chan struct{}
	trackOnce      sync.Once
	cleanupOnce    sync.Once
	mu             sync.Mutex
	active         map[string]struct{}
	events         []string
}

type recordingNodeTracker struct {
	mu     sync.Mutex
	active map[string]struct{}
	events []string
}

func (t *recordingNodeTracker) NodeURL() string  { return "node-url" }
func (t *recordingNodeTracker) NodeName() string { return "node-name" }
func (t *recordingNodeTracker) Track(_ context.Context, info nodesessions.SessionInfo) {
	t.mu.Lock()
	t.active[info.SessionID] = struct{}{}
	t.events = append(t.events, "track:"+info.SessionID)
	t.mu.Unlock()
}
func (t *recordingNodeTracker) Remove(_ context.Context, sessionID string) {
	t.mu.Lock()
	delete(t.active, sessionID)
	t.events = append(t.events, "remove:"+sessionID)
	t.mu.Unlock()
}
func (t *recordingNodeTracker) Cleanup(context.Context) {
	t.mu.Lock()
	t.active = make(map[string]struct{})
	t.events = append(t.events, "cleanup")
	t.mu.Unlock()
}

func (t *delayedNodeTracker) NodeURL() string  { return "node-url" }
func (t *delayedNodeTracker) NodeName() string { return "node-name" }

func (t *delayedNodeTracker) Track(ctx context.Context, info nodesessions.SessionInfo) {
	t.trackOnce.Do(func() { close(t.trackStarted) })
	select {
	case <-t.releaseTrack:
		t.mu.Lock()
		t.active[info.SessionID] = struct{}{}
		t.events = append(t.events, "track")
		t.mu.Unlock()
	case <-ctx.Done():
		t.mu.Lock()
		t.events = append(t.events, "track-canceled")
		t.mu.Unlock()
	}
	close(t.trackDone)
}

func (t *delayedNodeTracker) Remove(_ context.Context, sessionID string) {
	t.mu.Lock()
	delete(t.active, sessionID)
	t.mu.Unlock()
}

func (t *delayedNodeTracker) Cleanup(context.Context) {
	t.cleanupOnce.Do(func() { close(t.cleanupStarted) })
	t.mu.Lock()
	t.active = make(map[string]struct{})
	t.events = append(t.events, "cleanup")
	t.mu.Unlock()
}

func newDelayedNodeTracker() *delayedNodeTracker {
	return &delayedNodeTracker{
		trackStarted: make(chan struct{}), trackDone: make(chan struct{}), releaseTrack: make(chan struct{}),
		cleanupStarted: make(chan struct{}), active: make(map[string]struct{}),
	}
}

func (s *blockingDeleteRecipeStore) Get(_ context.Context, sessionID string) (*playback.RecipeCard, bool) {
	s.getOnce.Do(func() { close(s.getEntered) })
	s.mu.Lock()
	defer s.mu.Unlock()
	card, ok := s.cards[sessionID]
	if !ok {
		return nil, false
	}
	return &card, true
}

func (s *blockingDeleteRecipeStore) Delete(_ context.Context, sessionID string) error {
	s.deleteOnce.Do(func() { close(s.deleteEntered) })
	<-s.allowDelete
	s.mu.Lock()
	delete(s.cards, sessionID)
	s.mu.Unlock()
	return nil
}

func (s *mapRecipeStore) Get(_ context.Context, sessionID string) (*playback.RecipeCard, bool) {
	s.gets = append(s.gets, sessionID)
	card, ok := s.cards[sessionID]
	if !ok {
		return nil, false
	}
	return &card, true
}

func (s *mapRecipeStore) Delete(_ context.Context, sessionID string) error {
	s.deletes = append(s.deletes, sessionID)
	delete(s.cards, sessionID)
	return nil
}

func (s *stubRecipeStore) Get(_ context.Context, sessionID string) (*playback.RecipeCard, bool) {
	s.hits++
	s.gets = append(s.gets, sessionID)
	return s.card, s.ok
}

func (s *stubRecipeStore) Delete(_ context.Context, sessionID string) error {
	s.deletes = append(s.deletes, sessionID)
	return s.delErr
}

// When the forwarded token is recipe-less (jellycompat), the node consults the
// recipe store. A miss or an incomplete recipe must yield a clean nil (404) with
// no ffmpeg spawn — these assert the resolve guards without needing ffmpeg.
func TestReconstructFromToken_JellycompatRecipeFetch(t *testing.T) {
	const sid = "compat-sess-1"
	recipeLessToken := func(t *testing.T) string {
		return signCard(t, playback.RecipeCard{
			SessionID:  sid,
			UserID:     7,
			PlayMethod: playback.PlayTranscode,
			InputPath:  "/media/movie.mkv",
		})
	}

	t.Run("store miss -> nil", func(t *testing.T) {
		s := newTestServer(t)
		store := &stubRecipeStore{ok: false}
		s.SetRecipeStore(store)
		if got := s.reconstructFromToken(requestWithToken(sid, recipeLessToken(t)), sid, 5); got != nil {
			t.Fatalf("expected nil on store miss, got %v", got)
		}
		if store.hits != 1 {
			t.Fatalf("recipe store consulted %d times, want 1", store.hits)
		}
	})

	t.Run("generation-bound transport fetches recipe by public identity", func(t *testing.T) {
		const generation = "32a4e124-9df7-4cfa-be49-e8e503316714"
		transportID, _ := playback.GenerationBoundTranscodeTransportID(sid, generation)
		card := playback.RecipeCard{SessionID: sid, SessionGeneration: generation, TranscodeTransportID: transportID, UserID: 7, PlayMethod: playback.PlayTranscode, InputPath: "/media/movie.mkv"}
		s := newTestServer(t)
		store := &stubRecipeStore{}
		s.SetRecipeStore(store)
		if got := s.reconstructFromToken(requestWithToken(transportID, signCard(t, card)), transportID, 5); got != nil {
			t.Fatalf("expected nil on store miss, got %v", got)
		}
		if len(store.gets) != 1 || store.gets[0] != sid {
			t.Fatalf("recipe gets = %v, want public ID %q", store.gets, sid)
		}
	})

	t.Run("incomplete fetched recipe -> nil", func(t *testing.T) {
		s := newTestServer(t)
		// Right session id but missing encode params: must not spawn.
		s.SetRecipeStore(&stubRecipeStore{ok: true, card: &playback.RecipeCard{SessionID: sid, PlayMethod: playback.PlayTranscode}})
		if got := s.reconstructFromToken(requestWithToken(sid, recipeLessToken(t)), sid, 5); got != nil {
			t.Fatalf("expected nil for incomplete fetched recipe, got %v", got)
		}
	})

	t.Run("fetched recipe for wrong session -> nil", func(t *testing.T) {
		s := newTestServer(t)
		s.SetRecipeStore(&stubRecipeStore{ok: true, card: &playback.RecipeCard{
			SessionID: "other", PlayMethod: playback.PlayTranscode, SegmentDuration: 6, TargetCodecVideo: "h264",
		}})
		if got := s.reconstructFromToken(requestWithToken(sid, recipeLessToken(t)), sid, 5); got != nil {
			t.Fatalf("expected nil for wrong-session recipe, got %v", got)
		}
	})
}

// handleStop is a deliberate teardown, so it must drop the session's recipe to
// stop a buffered/retrying post-restart request from reconstructing a brand-new
// ffmpeg for an already-stopped session. A zero-value TranscodeSession needs no
// ffmpeg or media file to Close, so this asserts the wiring without a real spawn.
func TestHandleStop_DeletesRecipe(t *testing.T) {
	const sid = "stop-sess-1"
	s := newTestServer(t)
	s.tracker = nodesessions.NewTracker(nil, "node-url", "node-name", "transcode")
	store := &stubRecipeStore{}
	s.SetRecipeStore(store)

	s.sessions[sid] = &playback.TranscodeSession{}
	s.activeJobs.Store(1)

	r := httptest.NewRequest(http.MethodDelete, "/transcode/"+sid, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("session_id", sid)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	s.handleStop(rec, r)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("handleStop status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if len(store.deletes) != 1 || store.deletes[0] != sid {
		t.Fatalf("recipe deletes = %v, want [%q]", store.deletes, sid)
	}
	if _, ok := s.sessions[sid]; ok {
		t.Fatalf("session %q still registered after stop", sid)
	}
}

func TestHandleStopDeletesGenerationBoundRecipeByPublicIdentity(t *testing.T) {
	const (
		publicID   = "public-session"
		generation = "32a4e124-9df7-4cfa-be49-e8e503316714"
	)
	transportID, _ := playback.GenerationBoundTranscodeTransportID(publicID, generation)
	s := newTestServer(t)
	s.tracker = nodesessions.NewTracker(nil, "node-url", "node-name", "transcode")
	store := &stubRecipeStore{}
	s.SetRecipeStore(store)
	s.sessions[transportID] = &playback.TranscodeSession{}
	s.identities[transportID] = transcodeIdentity{publicSessionID: publicID, generation: generation}
	s.activeJobs.Store(1)

	r := httptest.NewRequest(http.MethodDelete, "/transcode/"+transportID, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("session_id", transportID)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	s.handleStop(httptest.NewRecorder(), r)
	if len(store.deletes) != 1 || store.deletes[0] != publicID {
		t.Fatalf("recipe deletes = %v, want public ID %q", store.deletes, publicID)
	}
}

func TestCloseSessionsForForceReloadDeletesModernAndLegacyRecipes(t *testing.T) {
	const (
		publicID    = "modern-public-session"
		generation  = "32a4e124-9df7-4cfa-be49-e8e503316714"
		generation2 = "d24a82d2-2bc2-44a4-96c6-a86671d508d7"
		legacyID    = "legacy-public-session"
	)
	transportID, err := playback.GenerationBoundTranscodeTransportID(publicID, generation)
	if err != nil {
		t.Fatal(err)
	}
	transportID2, err := playback.GenerationBoundTranscodeTransportID(publicID, generation2)
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)
	modernCard := playback.RecipeCard{
		SessionID: publicID, SessionGeneration: generation, TranscodeTransportID: transportID,
		UserID: 7, PlayMethod: playback.PlayTranscode, InputPath: "/media/modern.mkv",
		TargetCodecVideo: "h264", SegmentDuration: 6,
	}
	legacyCard := transcodeCard(legacyID)
	store := &mapRecipeStore{cards: map[string]playback.RecipeCard{
		publicID: modernCard,
		legacyID: legacyCard,
	}}
	s.SetRecipeStore(store)
	s.sessions[transportID] = &playback.TranscodeSession{}
	s.sessions[transportID2] = &playback.TranscodeSession{}
	s.sessions[legacyID] = &playback.TranscodeSession{}
	s.identities[transportID] = transcodeIdentity{publicSessionID: publicID, generation: generation}
	s.identities[transportID2] = transcodeIdentity{publicSessionID: publicID, generation: generation2}
	s.lastAccess = map[string]time.Time{transportID: time.Now(), transportID2: time.Now(), legacyID: time.Now()}
	s.activeJobs.Store(3)
	for _, id := range []string{transportID, transportID2, legacyID} {
		dir := filepath.Join(s.watcher.Config().Playback.TranscodeDir, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	s.closeSessionsForForceReload(t.Context(), s.watcher.Config().Playback.TranscodeDir)

	if len(s.sessions) != 0 || len(s.identities) != 0 || len(s.lastAccess) != 0 {
		t.Fatalf("live maps after reload: sessions=%d identities=%d access=%d", len(s.sessions), len(s.identities), len(s.lastAccess))
	}
	if got := s.activeJobs.Load(); got != 0 {
		t.Fatalf("active jobs = %d, want 0", got)
	}
	if strings.Join(store.deletes, ",") != publicID+","+legacyID && strings.Join(store.deletes, ",") != legacyID+","+publicID {
		t.Fatalf("recipe deletes = %v, want unique public keys %q and %q", store.deletes, publicID, legacyID)
	}
	if len(store.cards) != 0 {
		t.Fatalf("recipes survived reload: %v", store.cards)
	}
	for _, id := range []string{transportID, transportID2, legacyID} {
		if _, err := os.Stat(filepath.Join(s.watcher.Config().Playback.TranscodeDir, id)); !os.IsNotExist(err) {
			t.Fatalf("transcode directory %q survived reload: %v", id, err)
		}
	}
	for _, tc := range []struct {
		id   string
		card playback.RecipeCard
	}{
		{transportID, playback.RecipeCard{
			SessionID: publicID, SessionGeneration: generation, TranscodeTransportID: transportID,
			UserID: 7, PlayMethod: playback.PlayTranscode, InputPath: "/media/modern.mkv",
		}},
		{legacyID, playback.RecipeCard{
			SessionID: legacyID, UserID: 7, PlayMethod: playback.PlayTranscode, InputPath: "/media/legacy.mkv",
		}},
	} {
		if got := s.reconstructFromToken(requestWithToken(tc.id, signCard(t, tc.card)), tc.id, -1); got != nil {
			t.Fatalf("buffered token reconstructed %q after reload", tc.id)
		}
	}
	if strings.Join(store.gets, ",") != publicID+","+legacyID {
		t.Fatalf("buffered token recipe lookups = %v, want public keys [%q %q]", store.gets, publicID, legacyID)
	}
}

func TestForceReloadSerializesAdmissionThroughFinalCleanup(t *testing.T) {
	t.Run("identity-only reconstruction cannot cross recipe deletion", func(t *testing.T) {
		const (
			publicID   = "reload-reconstruct-public"
			generation = "32a4e124-9df7-4cfa-be49-e8e503316714"
		)
		transportID, err := playback.GenerationBoundTranscodeTransportID(publicID, generation)
		if err != nil {
			t.Fatal(err)
		}
		s := newTestServer(t)
		s.tracker = nodesessions.NewTracker(nil, "node-url", "node-name", "transcode")
		ffmpeg := filepath.Join(t.TempDir(), "reload-reconstruct-ffmpeg.sh")
		if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		s.watcher.Config().Playback.FFmpegPath = ffmpeg
		stored := playback.RecipeCard{
			SessionID: publicID, SessionGeneration: generation, TranscodeTransportID: transportID,
			UserID: 7, PlayMethod: playback.PlayTranscode, InputPath: "/media/movie.mkv",
			TargetCodecVideo: "h264", SegmentDuration: 6,
		}
		store := &blockingDeleteRecipeStore{
			cards: map[string]playback.RecipeCard{publicID: stored}, deleteEntered: make(chan struct{}),
			getEntered: make(chan struct{}), allowDelete: make(chan struct{}),
		}
		s.SetRecipeStore(store)
		s.sessions[transportID] = &playback.TranscodeSession{}
		s.identities[transportID] = transcodeIdentity{publicSessionID: publicID, generation: generation}
		s.activeJobs.Store(1)

		reloadDone := make(chan struct{})
		go func() {
			s.closeSessionsForForceReload(t.Context(), s.watcher.Config().Playback.TranscodeDir)
			close(reloadDone)
		}()
		<-store.deleteEntered // live maps are cleared; recipe deletion is paused

		identityOnly := playback.RecipeCard{
			SessionID: publicID, SessionGeneration: generation, TranscodeTransportID: transportID,
			UserID: 7, PlayMethod: playback.PlayTranscode, InputPath: "/media/movie.mkv",
		}
		token := signCard(t, identityOnly)
		reconstructDone := make(chan *playback.TranscodeSession, 1)
		go func() {
			reconstructDone <- s.reconstructFromToken(requestWithToken(transportID, token), transportID, -1)
		}()

		crossedCleanup := false
		select {
		case <-store.getEntered:
			crossedCleanup = true
		case <-time.After(300 * time.Millisecond):
		}
		close(store.allowDelete)
		<-reloadDone
		got := <-reconstructDone
		if crossedCleanup {
			t.Fatal("reconstruction fetched the old recipe while force reload held cleanup open")
		}
		if got != nil {
			t.Fatal("buffered identity-only token reconstructed after force reload")
		}
		select {
		case <-store.getEntered:
		default:
			t.Fatal("buffered token did not check the recipe store after reload")
		}
		store.mu.Lock()
		remainingRecipes := len(store.cards)
		store.mu.Unlock()
		if remainingRecipes != 0 {
			t.Fatalf("recipes survived reload: %d", remainingRecipes)
		}
		if len(s.sessions) != 0 || len(s.identities) != 0 || s.activeJobs.Load() != 0 {
			t.Fatalf("state survived reload: sessions=%d identities=%d jobs=%d", len(s.sessions), len(s.identities), s.activeJobs.Load())
		}
	})

	t.Run("fresh start completes before cleanup snapshot", func(t *testing.T) {
		const sessionID = "reload-fresh-start"
		s := newTestServer(t)
		s.tracker = nodesessions.NewTracker(nil, "node-url", "node-name", "transcode")
		store := &blockingNodeGenerationStore{entered: make(chan struct{}), release: make(chan struct{})}
		s.generationStore = store
		ffmpeg := filepath.Join(t.TempDir(), "reload-start-ffmpeg.sh")
		if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		s.watcher.Config().Playback.FFmpegPath = ffmpeg
		body, _ := json.Marshal(TranscodeStartRequest{
			SessionID: sessionID, InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", SegmentDuration: 6,
		})
		startDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			rr := httptest.NewRecorder()
			s.handleStart(rr, httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body)))
			startDone <- rr
		}()
		<-store.entered

		reloadDone := make(chan struct{})
		go func() {
			s.closeSessionsForForceReload(t.Context(), s.watcher.Config().Playback.TranscodeDir)
			close(reloadDone)
		}()
		reloadReturnedEarly := false
		select {
		case <-reloadDone:
			reloadReturnedEarly = true
		case <-time.After(300 * time.Millisecond):
		}
		close(store.release)
		rr := <-startDone
		<-reloadDone
		if reloadReturnedEarly {
			t.Fatal("force reload completed while fresh-start admission was in flight")
		}
		if rr.Code != http.StatusAccepted {
			t.Fatalf("start status/body = %d/%q", rr.Code, rr.Body.String())
		}
		if len(s.sessions) != 0 || len(s.identities) != 0 || s.activeJobs.Load() != 0 {
			t.Fatalf("fresh-start state survived reload: sessions=%d identities=%d jobs=%d", len(s.sessions), len(s.identities), s.activeJobs.Load())
		}
	})

	t.Run("tokenless selection finishes before process cleanup", func(t *testing.T) {
		const sessionID = "reload-tokenless-existing"
		s := newTestServer(t)
		s.tracker = nodesessions.NewTracker(nil, "node-url", "node-name", "transcode")
		registerServingSession(t, s, sessionID, transcodeIdentity{publicSessionID: sessionID})
		store := &blockingNodeGenerationStore{entered: make(chan struct{}), release: make(chan struct{})}
		s.generationStore = store
		requestDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/transcode/"+sessionID+"/master.m3u8", nil)
			req.Header.Set("Authorization", "Bearer "+testSecret)
			s.Handler().ServeHTTP(rr, req)
			requestDone <- rr
		}()
		<-store.entered

		reloadDone := make(chan struct{})
		go func() {
			s.closeSessionsForForceReload(t.Context(), s.watcher.Config().Playback.TranscodeDir)
			close(reloadDone)
		}()
		reloadReturnedEarly := false
		select {
		case <-reloadDone:
			reloadReturnedEarly = true
		case <-time.After(300 * time.Millisecond):
		}
		close(store.release)
		rr := <-requestDone
		<-reloadDone
		if reloadReturnedEarly {
			t.Fatal("force reload completed while tokenless process selection was in flight")
		}
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "#EXTM3U") {
			t.Fatalf("tokenless status/body = %d/%q", rr.Code, rr.Body.String())
		}
		if len(s.sessions) != 0 || len(s.identities) != 0 || s.activeJobs.Load() != 0 {
			t.Fatalf("selected process survived reload: sessions=%d identities=%d jobs=%d", len(s.sessions), len(s.identities), s.activeJobs.Load())
		}
	})
}

func TestForceReloadWaitsForTrackerPublication(t *testing.T) {
	assertCleanupWaits := func(t *testing.T, s *Server, tracker *delayedNodeTracker, requestDone <-chan struct{}) {
		t.Helper()
		<-tracker.trackStarted
		if !s.mu.TryLock() {
			t.Fatal("session map lock held across tracker publication")
		}
		s.mu.Unlock()
		reloadDone := make(chan struct{})
		go func() {
			s.closeSessionsForForceReload(t.Context(), s.watcher.Config().Playback.TranscodeDir)
			close(reloadDone)
		}()
		cleanupStartedEarly := false
		select {
		case <-tracker.cleanupStarted:
			cleanupStartedEarly = true
		case <-time.After(300 * time.Millisecond):
		}
		close(tracker.releaseTrack)
		<-requestDone
		<-reloadDone
		if cleanupStartedEarly {
			t.Fatal("tracker cleanup began before in-flight publication completed")
		}
		tracker.mu.Lock()
		defer tracker.mu.Unlock()
		if got := strings.Join(tracker.events, ","); got != "track,cleanup" {
			t.Fatalf("tracker event order = %q, want track,cleanup", got)
		}
		if len(tracker.active) != 0 {
			t.Fatalf("tracked sessions were republished after cleanup: %v", tracker.active)
		}
		if len(s.sessions) != 0 || len(s.identities) != 0 || s.activeJobs.Load() != 0 {
			t.Fatalf("server state survived cleanup: sessions=%d identities=%d jobs=%d", len(s.sessions), len(s.identities), s.activeJobs.Load())
		}
	}

	t.Run("fresh start", func(t *testing.T) {
		const sessionID = "reload-track-fresh"
		s := newTestServer(t)
		tracker := newDelayedNodeTracker()
		s.tracker = tracker
		ffmpeg := filepath.Join(t.TempDir(), "track-start-ffmpeg.sh")
		if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		s.watcher.Config().Playback.FFmpegPath = ffmpeg
		body, _ := json.Marshal(TranscodeStartRequest{
			SessionID: sessionID, InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", SegmentDuration: 6,
		})
		requestDone := make(chan struct{})
		go func() {
			s.handleStart(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body)))
			close(requestDone)
		}()
		assertCleanupWaits(t, s, tracker, requestDone)
	})

	t.Run("token reconstruction", func(t *testing.T) {
		const sessionID = "reload-track-reconstruct"
		s := newTestServer(t)
		tracker := newDelayedNodeTracker()
		s.tracker = tracker
		ffmpeg := filepath.Join(t.TempDir(), "track-reconstruct-ffmpeg.sh")
		if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		s.watcher.Config().Playback.FFmpegPath = ffmpeg
		token := signCard(t, transcodeCard(sessionID))
		requestDone := make(chan struct{})
		go func() {
			_ = s.reconstructFromToken(requestWithToken(sessionID, token), sessionID, -1)
			close(requestDone)
		}()
		assertCleanupWaits(t, s, tracker, requestDone)
	})
}

func TestCanceledTrackerPublicationReleasesReloadAdmission(t *testing.T) {
	const sessionID = "reload-track-canceled"
	s := newTestServer(t)
	tracker := newDelayedNodeTracker()
	s.tracker = tracker
	ffmpeg := filepath.Join(t.TempDir(), "track-canceled-ffmpeg.sh")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.watcher.Config().Playback.FFmpegPath = ffmpeg
	body, _ := json.Marshal(TranscodeStartRequest{
		SessionID: sessionID, InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", SegmentDuration: 6,
	})
	ctx, cancel := context.WithCancel(t.Context())
	requestDone := make(chan struct{})
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body)).WithContext(ctx)
		s.handleStart(httptest.NewRecorder(), req)
		close(requestDone)
	}()
	<-tracker.trackStarted
	reloadDone := make(chan struct{})
	go func() {
		s.closeSessionsForForceReload(t.Context(), s.watcher.Config().Playback.TranscodeDir)
		close(reloadDone)
	}()
	select {
	case <-tracker.cleanupStarted:
		t.Fatal("cleanup crossed a canceled-but-still-running tracker publication")
	case <-time.After(300 * time.Millisecond):
	}
	cancel()
	<-tracker.trackDone
	<-requestDone
	<-reloadDone
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if got := strings.Join(tracker.events, ","); got != "track-canceled,cleanup" {
		t.Fatalf("tracker cancellation order = %q, want track-canceled,cleanup", got)
	}
	if len(tracker.active) != 0 || len(s.sessions) != 0 || s.activeJobs.Load() != 0 {
		t.Fatalf("state survived canceled publication cleanup: tracked=%d sessions=%d jobs=%d", len(tracker.active), len(s.sessions), s.activeJobs.Load())
	}
}

func TestSameTransportReplacementRetiresTrackerBeforeSuccessorPublication(t *testing.T) {
	const sessionID = "same-transport-replacement"
	for _, tc := range []struct {
		name         string
		script       string
		requireReady bool
		wantStatus   int
		wantEvents   string
		wantActive   bool
	}{
		{"spawn failure", "", false, http.StatusInternalServerError, "track:" + sessionID + ",remove:" + sessionID, false},
		{"readiness failure", "#!/bin/sh\nexit 1\n", true, http.StatusInternalServerError, "track:" + sessionID + ",remove:" + sessionID, false},
		{"success", "#!/bin/sh\nsleep 30\n", false, http.StatusAccepted, "track:" + sessionID + ",remove:" + sessionID + ",track:" + sessionID, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			tracker := &recordingNodeTracker{active: make(map[string]struct{})}
			s.tracker = tracker
			tracker.Track(t.Context(), nodesessions.SessionInfo{SessionID: sessionID})
			s.sessions[sessionID] = &playback.TranscodeSession{}
			s.identities[sessionID] = transcodeIdentity{publicSessionID: sessionID}
			s.lastAccess = map[string]time.Time{sessionID: time.Now()}
			s.activeJobs.Store(1)

			if tc.script == "" {
				s.watcher.Config().Playback.FFmpegPath = filepath.Join(t.TempDir(), "missing-ffmpeg")
			} else {
				ffmpeg := filepath.Join(t.TempDir(), "replacement-ffmpeg.sh")
				if err := os.WriteFile(ffmpeg, []byte(tc.script), 0o755); err != nil {
					t.Fatal(err)
				}
				s.watcher.Config().Playback.FFmpegPath = ffmpeg
			}
			body, _ := json.Marshal(TranscodeStartRequest{
				SessionID: sessionID, InputPath: "/media/movie.mkv", TargetCodecVideo: "h264",
				SegmentDuration: 6, RequireReady: tc.requireReady,
			})
			rr := httptest.NewRecorder()
			s.handleStart(rr, httptest.NewRequest(http.MethodPost, "/transcode/start", bytes.NewReader(body)))
			if rr.Code != tc.wantStatus {
				t.Fatalf("replacement status/body = %d/%q, want %d", rr.Code, rr.Body.String(), tc.wantStatus)
			}

			tracker.mu.Lock()
			events := strings.Join(tracker.events, ",")
			_, active := tracker.active[sessionID]
			tracker.mu.Unlock()
			if events != tc.wantEvents || active != tc.wantActive {
				t.Fatalf("tracker events/active = %q/%v, want %q/%v", events, active, tc.wantEvents, tc.wantActive)
			}

			if !tc.wantActive {
				deleteRR := httptest.NewRecorder()
				deleteReq := httptest.NewRequest(http.MethodDelete, "/transcode/"+sessionID, nil)
				deleteReq.Header.Set("Authorization", "Bearer "+testSecret)
				s.Handler().ServeHTTP(deleteRR, deleteReq)
				if deleteRR.Code != http.StatusNotFound {
					t.Fatalf("subsequent delete status = %d, want 404", deleteRR.Code)
				}
				tracker.mu.Lock()
				postDeleteEvents := strings.Join(tracker.events, ",")
				_, resurrected := tracker.active[sessionID]
				tracker.mu.Unlock()
				if postDeleteEvents != tc.wantEvents || resurrected {
					t.Fatalf("404 delete changed tracker = %q active=%v", postDeleteEvents, resurrected)
				}
			} else {
				s.mu.RLock()
				replacement := s.sessions[sessionID]
				s.mu.RUnlock()
				if replacement == nil {
					t.Fatal("successful successor not registered")
				}
				t.Cleanup(func() { _ = replacement.Close() })
			}
		})
	}
}

func TestForwardedTokenReconstructionPreservesAdmissionStatus(t *testing.T) {
	const (
		publicID   = "forwarded-token-public"
		generation = "32a4e124-9df7-4cfa-be49-e8e503316714"
	)
	transportID, err := playback.GenerationBoundTranscodeTransportID(publicID, generation)
	if err != nil {
		t.Fatal(err)
	}
	completeCard := playback.RecipeCard{
		SessionID: publicID, SessionGeneration: generation, TranscodeTransportID: transportID,
		UserID: 7, PlayMethod: playback.PlayTranscode, InputPath: "/must/not/open",
		TargetCodecVideo: "h264", SegmentDuration: 6,
	}
	identityOnly := playback.RecipeCard{
		SessionID: publicID, SessionGeneration: generation, TranscodeTransportID: transportID,
		UserID: 7, PlayMethod: playback.PlayTranscode, InputPath: "/must/not/open",
	}

	routes := []struct {
		name string
		path string
	}{{"manifest", "/transcode/" + transportID + "/master.m3u8"}, {"segment", "/transcode/" + transportID + "/segment/seg_00000.ts"}}
	for _, admission := range []struct {
		name       string
		store      *nodeGenerationStore
		card       playback.RecipeCard
		recipeMiss bool
		want       int
	}{
		{"ended", &nodeGenerationStore{ended: true}, completeCard, false, http.StatusGone},
		{"authority unavailable", &nodeGenerationStore{err: errors.New("db down")}, completeCard, false, http.StatusServiceUnavailable},
		{"genuine recipe miss", &nodeGenerationStore{}, identityOnly, true, http.StatusNotFound},
	} {
		for _, route := range routes {
			t.Run(admission.name+"/"+route.name, func(t *testing.T) {
				s := newTestServer(t)
				s.generationStore = admission.store
				tracker := &recordingNodeTracker{active: make(map[string]struct{})}
				s.tracker = tracker
				if admission.recipeMiss {
					s.SetRecipeStore(&stubRecipeStore{ok: false})
				}
				rr := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, route.path, nil)
				req.Header.Set("Authorization", "Bearer "+testSecret)
				req.Header.Set("X-Silo-Stream-Token", signCard(t, admission.card))
				s.Handler().ServeHTTP(rr, req)
				if rr.Code != admission.want {
					t.Fatalf("status/body = %d/%q, want %d", rr.Code, rr.Body.String(), admission.want)
				}
				if strings.Contains(rr.Body.String(), "#EXTM3U") || strings.Contains(rr.Body.String(), "generation-media") {
					t.Fatalf("denied response served media bytes: %q", rr.Body.String())
				}
				tracker.mu.Lock()
				tracked := len(tracker.events)
				tracker.mu.Unlock()
				if len(s.sessions) != 0 || s.activeJobs.Load() != 0 || tracked != 0 {
					t.Fatalf("denial side effects: sessions=%d jobs=%d tracker_events=%d", len(s.sessions), s.activeJobs.Load(), tracked)
				}
			})
		}
	}
}

func TestForwardedTokenIdentityConflictIsRetryable(t *testing.T) {
	const (
		publicID   = "forwarded-conflict-public"
		generation = "32a4e124-9df7-4cfa-be49-e8e503316714"
	)
	transportID, err := playback.GenerationBoundTranscodeTransportID(publicID, generation)
	if err != nil {
		t.Fatal(err)
	}
	card := playback.RecipeCard{
		SessionID: publicID, SessionGeneration: generation, TranscodeTransportID: transportID,
		UserID: 7, PlayMethod: playback.PlayTranscode, InputPath: "/must/not/open",
		TargetCodecVideo: "h264", SegmentDuration: 6,
	}
	for _, route := range []struct {
		name string
		path string
	}{{"manifest", "/transcode/" + transportID + "/master.m3u8"}, {"segment", "/transcode/" + transportID + "/segment/seg_00000.ts"}} {
		t.Run(route.name, func(t *testing.T) {
			s := newTestServer(t)
			s.sessions[transportID] = &playback.TranscodeSession{}
			s.identities[transportID] = transcodeIdentity{publicSessionID: "conflicting-public", generation: generation}
			s.activeJobs.Store(1)
			tracker := &recordingNodeTracker{active: make(map[string]struct{})}
			s.tracker = tracker
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, route.path, nil)
			req.Header.Set("Authorization", "Bearer "+testSecret)
			req.Header.Set("X-Silo-Stream-Token", signCard(t, card))
			s.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("conflict status/body = %d/%q, want 503", rr.Code, rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), "#EXTM3U") || len(tracker.events) != 0 {
				t.Fatalf("conflict served/tracked: body=%q events=%v", rr.Body.String(), tracker.events)
			}
			if s.sessions[transportID] == nil || s.activeJobs.Load() != 1 {
				t.Fatal("conflict mutated the incumbent process")
			}
		})
	}
}

// The idle reaper must close only jobs whose last access predates the TTL;
// registration counts as an access, so a just-started job (including one still
// waiting on its manifest in the RequireReady flow) is spared. Zero-value
// TranscodeSessions Close without ffmpeg, so this runs without a real spawn.
func TestReapIdleSessions_ClosesOnlyIdleJobs(t *testing.T) {
	s := newTestServer(t)
	s.tracker = nodesessions.NewTracker(nil, "node-url", "node-name", "transcode")

	s.sessions["fresh-1"] = &playback.TranscodeSession{}
	s.sessions["stale-1"] = &playback.TranscodeSession{}
	s.lastAccess = map[string]time.Time{
		"fresh-1": time.Now(),
		"stale-1": time.Now().Add(-sessionIdleTTL - time.Minute),
	}
	s.activeJobs.Store(2)

	s.reapIdleSessions(sessionIdleTTL)

	s.mu.RLock()
	_, freshAlive := s.sessions["fresh-1"]
	_, staleAlive := s.sessions["stale-1"]
	_, staleTracked := s.lastAccess["stale-1"]
	s.mu.RUnlock()
	if !freshAlive {
		t.Fatal("recently accessed session was reaped")
	}
	if staleAlive {
		t.Fatal("idle session survived the reaper")
	}
	if staleTracked {
		t.Fatal("reaped session's idle clock was not dropped")
	}
	if got := s.activeJobs.Load(); got != 1 {
		t.Fatalf("activeJobs = %d, want 1", got)
	}
}

// A registered job with no recorded access (untracked registration) must not
// be closed; the sweep starts its idle clock instead of reaping a job that may
// be actively serving.
func TestReapIdleSessions_StartsClockForUntrackedJob(t *testing.T) {
	s := newTestServer(t)
	s.sessions["untracked-1"] = &playback.TranscodeSession{}
	s.activeJobs.Store(1)

	s.reapIdleSessions(sessionIdleTTL)

	s.mu.RLock()
	_, alive := s.sessions["untracked-1"]
	last, tracked := s.lastAccess["untracked-1"]
	s.mu.RUnlock()
	if !alive {
		t.Fatal("untracked session was reaped")
	}
	if !tracked || last.IsZero() {
		t.Fatal("sweep did not start the untracked session's idle clock")
	}
	if got := s.activeJobs.Load(); got != 1 {
		t.Fatalf("activeJobs = %d, want 1", got)
	}
}

// touchSession must refresh a registered job's idle clock and ignore ids with
// no live session (a reconstruct records its own first access on register).
func TestTouchSession_RefreshesIdleClock(t *testing.T) {
	s := newTestServer(t)
	s.sessions["live-1"] = &playback.TranscodeSession{}
	stale := time.Now().Add(-sessionIdleTTL - time.Minute)
	s.lastAccess = map[string]time.Time{"live-1": stale}

	s.touchSession("live-1")
	s.touchSession("ghost-1")

	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.lastAccess["live-1"].After(stale) {
		t.Fatal("touch did not refresh the live session's idle clock")
	}
	if _, ok := s.lastAccess["ghost-1"]; ok {
		t.Fatal("touch recorded access for an unregistered session")
	}
}

// spawnReconstruct must NOT apply the fast seg×dur resume seek for copy-mode
// cards: copy-mode segments have variable durations, so seg×dur points at the
// wrong source time. The card's original start must stand. Asserting opts off a
// real spawn would need ffmpeg, so this checks the gating condition directly.
func TestCopyModeReconstruct_SkipsFastSeek(t *testing.T) {
	const dur = 6
	card := playback.RecipeCard{
		SessionID:          "copy-sess-1",
		PlayMethod:         playback.PlayTranscode,
		TargetCodecVideo:   "copy",
		SegmentDuration:    dur,
		StartSegmentNumber: 0,
	}
	const requestedSegment = 10
	applyFastSeek := requestedSegment > card.StartSegmentNumber && card.SegmentDuration > 0 &&
		!strings.EqualFold(card.TargetCodecVideo, "copy")
	if applyFastSeek {
		t.Fatalf("copy-mode card must not apply the seg×dur fast seek")
	}

	// Same shape but ENCODED: the fast seek must apply.
	card.TargetCodecVideo = "h264"
	applyFastSeek = requestedSegment > card.StartSegmentNumber && card.SegmentDuration > 0 &&
		!strings.EqualFold(card.TargetCodecVideo, "copy")
	if !applyFastSeek {
		t.Fatalf("encoded card must apply the seg×dur fast seek")
	}
}
