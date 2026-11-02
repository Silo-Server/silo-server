package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	router := NewRouter(Dependencies{Config: cfg, SessionStore: store, ContentService: &genresContentService{}})

	// (1) Registration + auth placement: unauthenticated requests to the
	// session-auth-group routes must be 401 (registered + behind auth), never 404
	// (route dropped/misordered) or 2xx (accidentally registered anonymous).
	authGroup := []struct{ method, path string }{
		{http.MethodGet, "/Items/Filters2"},
		{http.MethodGet, "/Items/Counts"},
		{http.MethodGet, "/Items/abc/LocalTrailers"},
		{http.MethodGet, "/Users/u1/Items/abc/LocalTrailers"},
		{http.MethodGet, "/Videos/abc/AdditionalParts"},
		{http.MethodGet, "/items/abc/images"},
		{http.MethodGet, "/Sessions"},
		{http.MethodPost, "/ClientLog/Document"},
		{http.MethodGet, "/System/Configuration/encoding"},
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
	// Jellyfin Web draws no ASS/SSA subtitle until the encoding config loads, and
	// the Kodi add-on indexes EnableSubtitleExtraction from it.
	if rec := authed(http.MethodGet, "/System/Configuration/encoding"); rec.Code != http.StatusOK {
		t.Errorf("authed GET /System/Configuration/encoding = %d, want 200; body=%s", rec.Code, rec.Body.String())
	} else if got := strings.TrimSpace(rec.Body.String()); got != `{"EnableFallbackFont":false,"EnableSubtitleExtraction":true}` {
		t.Errorf("authed GET /System/Configuration/encoding body = %s, want fallback font off and subtitle extraction on", got)
	}
	if rec := authed(http.MethodGet, "/System/Configuration/livetv"); rec.Code != http.StatusNotFound {
		t.Errorf("authed GET /System/Configuration/livetv = %d, want 404", rec.Code)
	}
	// Moonfin for Roku asks for additional parts in lowercase (#1097), and the
	// Roku clients send some lowercase paths with a trailing slash (#2988).
	for _, path := range []string{"/Videos/abc/AdditionalParts", "/videos/abc/additionalparts", "/Videos/abc/AdditionalParts/", "/videos/abc/additionalparts/"} {
		rec := authed(http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Errorf("authed GET %s = %d, want 200; body=%s", path, rec.Code, rec.Body.String())
		} else if !strings.Contains(rec.Body.String(), `"Items":[]`) || !strings.Contains(rec.Body.String(), `"TotalRecordCount":0`) {
			t.Errorf("authed GET %s body = %s, want an empty QueryResult", path, rec.Body.String())
		}
	}
}

// itemCountsContentService answers count-only browses with a fixed total per
// type and records the params it was asked with.
type itemCountsContentService struct {
	countingContentService
	totals map[string]int
	params []url.Values
}

func (s *itemCountsContentService) BrowseItems(_ context.Context, _ *Session, params url.Values) (*upstreamBrowseResponse, error) {
	s.params = append(s.params, params)
	return &upstreamBrowseResponse{Total: s.totals[params.Get("type")], Items: []upstreamListItem{}}, nil
}

// TestRouter_ItemCounts pins that /Items/Counts reaches its handler instead of
// /Items/{id} (which answered 404 to Lenna) and returns Jellyfin's ItemCounts.
func TestRouter_ItemCounts(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	store := NewSessionStore(time.Hour, time.Now)
	const token = "router-test-token"
	session := Session{Token: token, StreamAppUserID: 1, ProfileID: "p1", PseudoUserID: PseudoUserID(1, "p1")}
	if err := store.Put(session); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	content := &itemCountsContentService{totals: map[string]int{"movie": 3, "series": 2}}
	router := NewRouter(Dependencies{Config: cfg, SessionStore: store, ContentService: content})

	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Emby-Token", token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	rec := get("/Items/Counts?userId=" + session.PseudoUserID.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /Items/Counts = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var counts itemCountsDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &counts); err != nil {
		t.Fatalf("decode ItemCounts: %v; body=%s", err, rec.Body.String())
	}
	if want := (itemCountsDTO{MovieCount: 3, SeriesCount: 2, ItemCount: 5}); counts != want {
		t.Errorf("ItemCounts = %+v, want %+v", counts, want)
	}
	for _, p := range content.params {
		if p.Get("count_only") != "true" || p.Get("is_favorite") != "" {
			t.Errorf("browse params = %v, want a count-only, unfiltered browse", p)
		}
	}

	content.params = nil
	if rec := get("/Items/Counts?IsFavorite=true"); rec.Code != http.StatusOK {
		t.Fatalf("GET /Items/Counts?IsFavorite=true = %d, want 200", rec.Code)
	}
	if len(content.params) != 2 {
		t.Fatalf("favorite counts browsed %d times, want 2", len(content.params))
	}
	for _, p := range content.params {
		if p.Get("is_favorite") != "true" {
			t.Errorf("favorite count params = %v, want is_favorite=true", p)
		}
	}

	if rec := get("/Items/Counts?userId=someone-else"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /Items/Counts for another user = %d, want 404", rec.Code)
	}
}
