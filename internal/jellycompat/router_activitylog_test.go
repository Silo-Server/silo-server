package jellycompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/activitylog"
	"github.com/Silo-Server/silo-server/internal/config"
)

type activityCapture struct {
	mu      sync.Mutex
	entries []activitylog.LogEntry
}

func (c *activityCapture) Write(entry activitylog.LogEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, entry)
}

func (c *activityCapture) Close() error { return nil }

func (c *activityCapture) take() []activitylog.LogEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.entries
	c.entries = nil
	return out
}

// TestRouter_ActivityLog pins that compat requests reach the activity log
// attributed to the compat session's account, so the admin "last active" time,
// audit log and IP history cover Jellyfin clients, and that high-volume artwork,
// web asset and HLS segment fetches stay out of it.
func TestRouter_ActivityLog(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	store := NewSessionStore(time.Hour, time.Now)
	const token = "activity-test-token"
	if err := store.Put(Session{Token: token, StreamAppUserID: 42, ProfileID: "p1", PseudoUserID: PseudoUserID(42, "p1")}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	capture := &activityCapture{}
	router := NewRouter(Dependencies{
		Config:            cfg,
		SessionStore:      store,
		ContentService:    &genresContentService{},
		ActivityLogWriter: capture,
		NodeID:            "node-a",
	})

	serve := func(method, path string, authed bool) []activitylog.LogEntry {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		if authed {
			req.Header.Set("X-Emby-Token", token)
		}
		router.ServeHTTP(httptest.NewRecorder(), req)
		return capture.take()
	}

	t.Run("authenticated request is attributed", func(t *testing.T) {
		entries := serve(http.MethodGet, "/emby/Items/Filters2", true)
		if len(entries) != 1 {
			t.Fatalf("entries = %d, want 1", len(entries))
		}
		e := entries[0]
		if e.UserID == nil || *e.UserID != 42 {
			t.Fatalf("UserID = %v, want 42", e.UserID)
		}
		if e.PathPattern != "/Items/Filters2" || e.NodeID != "node-a" || e.StatusCode != http.StatusOK {
			t.Fatalf("entry = %+v, want pattern /Items/Filters2, node node-a, status 200", e)
		}
	})

	t.Run("unauthenticated request is logged without a user", func(t *testing.T) {
		entries := serve(http.MethodGet, "/Sessions", false)
		if len(entries) != 1 {
			t.Fatalf("entries = %d, want 1", len(entries))
		}
		if entries[0].UserID != nil || entries[0].StatusCode != http.StatusUnauthorized {
			t.Fatalf("entry = %+v, want no user and status 401", entries[0])
		}
	})

	for _, path := range []string{
		"/Items/abc/Images/Primary",
		"/Items/abc/Images/Backdrop/0",
		"/Users/abc/Images/Primary",
		"/UserImage?userId=abc",
		"/api/v2/artwork/x",
		"/web/index.html",
		"/Videos/abc/hls/p1/0.ts",
		"/videos/abc/hls/p1/stream.m3u8",
		"/Videos/abc/remux-v1/hls/p1/0.mp4",
	} {
		t.Run("skips "+path, func(t *testing.T) {
			if entries := serve(http.MethodGet, path, true); len(entries) != 0 {
				t.Fatalf("entries = %+v, want none", entries)
			}
		})
	}
}

// activityLoginResolver accepts one password and signs in to account 42.
type activityLoginResolver struct{}

func (activityLoginResolver) Resolve(_ context.Context, username, password, _, _ string) (*Session, error) {
	if password != "right" {
		return nil, &HTTPError{StatusCode: http.StatusUnauthorized, Message: "invalid credentials"}
	}
	return &Session{Token: "login-token", Username: username, StreamAppUserID: 42, ProfileID: "p1", PseudoUserID: PseudoUserID(42, "p1")}, nil
}

// TestRouter_ActivityLogSignIn pins that a failed Jellyfin sign-in is logged
// without a user and a successful one is attributed to the signed-in account,
// so the first request of a new compat session already counts as activity.
func TestRouter_ActivityLogSignIn(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	capture := &activityCapture{}
	router := NewRouter(Dependencies{
		Config:            cfg,
		LoginResolver:     activityLoginResolver{},
		ActivityLogWriter: capture,
	})

	signIn := func(password string) (int, []activitylog.LogEntry) {
		t.Helper()
		body := strings.NewReader(`{"Username":"alice","Pw":"` + password + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", body)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code, capture.take()
	}

	status, entries := signIn("wrong")
	if status != http.StatusUnauthorized || len(entries) != 1 {
		t.Fatalf("failed sign-in: status %d, %d entries; want 401 and 1 entry", status, len(entries))
	}
	if entries[0].UserID != nil || entries[0].StatusCode != http.StatusUnauthorized {
		t.Fatalf("failed sign-in entry = %+v, want no user and status 401", entries[0])
	}

	status, entries = signIn("right")
	if status != http.StatusOK || len(entries) != 1 {
		t.Fatalf("successful sign-in: status %d, %d entries; want 200 and 1 entry", status, len(entries))
	}
	if entries[0].UserID == nil || *entries[0].UserID != 42 {
		t.Fatalf("successful sign-in UserID = %v, want 42", entries[0].UserID)
	}
}

// TestSkipCompatActivityLog_MatchesRegisteredRoutes checks the skip list
// against the routes the router actually registers, so renaming a route
// cannot silently start or stop logging it.
func TestSkipCompatActivityLog_MatchesRegisteredRoutes(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	router := NewRouter(Dependencies{Config: cfg})

	registered := map[string]bool{}
	if err := chi.Walk(router, func(_ string, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		registered[route] = true
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}

	for _, pattern := range []string{
		"/Items/{id}/Images/{imageType}",
		"/Items/{id}/Images/{imageType}/{index}",
		"/Users/{id}/Images/Primary",
		"/UserImage",
		"/api/v2/artwork/*",
		"/web/*",
	} {
		if !registered[pattern] {
			t.Errorf("skip-list pattern %q is not a registered route", pattern)
		}
	}

	hlsSegments := 0
	for route := range registered {
		isHLS := strings.HasPrefix(route, "/Videos/") && strings.Contains(route, "/hls/")
		if isHLS {
			hlsSegments++
		}
		if got := skipCompatActivityLog(route); got != isHLS && !isSkippedArtworkOrWeb(route) {
			t.Errorf("skipCompatActivityLog(%q) = %v", route, got)
		}
	}
	if hlsSegments == 0 {
		t.Fatal("no HLS routes found; the walk or the route shape changed")
	}
	for _, kept := range []string{"/Items/{id}/PlaybackInfo", "/Videos/{id}/master.m3u8", "/Videos/{id}/stream", "/Users/AuthenticateByName"} {
		if skipCompatActivityLog(kept) {
			t.Errorf("skipCompatActivityLog(%q) = true, want the request logged", kept)
		}
	}
}

func isSkippedArtworkOrWeb(route string) bool {
	return strings.Contains(route, "/Images/") || route == "/UserImage" || route == "/api/v2/artwork/*" || route == "/web/*"
}
