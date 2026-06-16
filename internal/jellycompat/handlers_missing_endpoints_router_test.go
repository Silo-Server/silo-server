package jellycompat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
)

// TestRouter_MissingEndpointsRegistered exercises the newly added routes through
// the full NewRouter/ServeHTTP stack — registration, chi static-vs-{id} ordering,
// and auth-group placement that the per-handler tests cannot see. Auth-group
// routes must answer 401 (registered + behind auth) rather than 404 (route
// dropped) or 2xx (accidentally anonymous); /UserImage must serve its anonymous
// avatar; and an authenticated Filters2/Sessions request must reach the right
// handler with the right shape.
func TestRouter_MissingEndpointsRegistered(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	store := NewSessionStore(time.Hour, time.Now)
	const token = "router-test-token"
	if err := store.Put(Session{Token: token, StreamAppUserID: 1, ProfileID: "p1", PseudoUserID: PseudoUserID(1, "p1")}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	router := NewRouter(Dependencies{Config: cfg, SessionStore: store})

	// (1) Registration + auth placement: unauthenticated requests to the
	// session-auth-group routes must be 401 (registered + behind auth), never 404
	// (route dropped/misordered) or 2xx (accidentally registered anonymous).
	authGroup := []struct{ method, path string }{
		{http.MethodGet, "/Items/Filters2"},
		{http.MethodGet, "/Items/abc/LocalTrailers"},
		{http.MethodGet, "/Users/u1/Items/abc/LocalTrailers"},
		{http.MethodGet, "/Sessions"},
		{http.MethodPost, "/ClientLog/Document"},
	}
	for _, tc := range authGroup {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("unauth %s %s = %d, want 401 (registered + behind auth)", tc.method, tc.path, rec.Code)
		}
	}

	// (2) /UserImage is anonymous-by-design (PR #158 palette): serves with no auth.
	{
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/UserImage?userId=router-test", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET /UserImage = %d, want 200", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
			t.Errorf("GET /UserImage Content-Type = %q, want image/png", ct)
		}
	}

	// (3) Authenticated routing/shape: prove /Items/Filters2 reaches the v2
	// filters handler (not shadowed by /Items/{id}, which would 404 "Item not
	// found") and /Sessions returns the empty list.
	authed := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("X-Emby-Token", token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := authed(http.MethodGet, "/Items/Filters2"); rec.Code != http.StatusOK {
		t.Errorf("authed GET /Items/Filters2 = %d, want 200; body=%s", rec.Code, rec.Body.String())
	} else if !strings.Contains(rec.Body.String(), "AudioLanguages") {
		t.Errorf("Filters2 not the v2 QueryFilters shape (no AudioLanguages); shadowed by /Items/{id}? body=%s", rec.Body.String())
	}
	if rec := authed(http.MethodGet, "/Sessions"); rec.Code != http.StatusOK {
		t.Errorf("authed GET /Sessions = %d, want 200", rec.Code)
	} else if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("authed GET /Sessions body = %q, want []", got)
	}
}
