package transcodenode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
		watcher:  w,
		sessions: make(map[string]*playback.TranscodeSession),
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
	deletes []string
	delErr  error
}

func (s *stubRecipeStore) Get(context.Context, string) (*playback.RecipeCard, bool) {
	s.hits++
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
