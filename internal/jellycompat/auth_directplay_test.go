package jellycompat

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// directPlayRouter wraps the probe handler in PlaybackSessionAuth and mounts it
// on a chi router so RoutePattern() and URLParam("id") are populated exactly as
// they are in production. The same handler also fronts /Items/{id}/Download to
// prove the new token-less fallback does NOT leak into that route.
func directPlayRouter(t *testing.T, sessions *SessionStore, playback *PlaybackSessionStore, keyAuth *AdminAPIKeyAuthenticator, reached *bool) *chi.Mux {
	t.Helper()
	probe := PlaybackSessionAuth(sessions, playback, keyAuth)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	}))
	r := chi.NewRouter()
	r.Handle("/Videos/{id}/stream", probe)
	r.Handle("/Videos/{id}/stream.{container}", probe)
	r.Handle("/Items/{id}/Download", probe)
	return r
}

// seedDirectPlaySession registers a resolvable compat session plus the
// PlaybackSession that PlaybackInfo would have negotiated for the item.
func seedDirectPlaySession(t *testing.T, clock func() time.Time) (*SessionStore, *PlaybackSessionStore) {
	t.Helper()
	sessions := NewSessionStore(time.Hour, clock)
	if err := sessions.Put(Session{Token: "compat-tok", StreamAppUserID: 7}); err != nil {
		t.Fatalf("put session: %v", err)
	}
	playback := NewPlaybackSessionStore(time.Hour, clock)
	playback.Put(PlaybackSession{
		ID:           "ps1",
		CompatToken:  "compat-tok",
		RouteItemID:  "item123",
		MediaSources: []PlaybackMediaSource{{ID: "src9"}},
	})
	return sessions, playback
}

// TestPlaybackSessionAuth_DirectPlayNoToken: stock Jellyfin Android TV requests
// the stream with no auth header, no api_key/ApiKey, and no PlaySessionId. The
// negotiated PlaybackSession for the item authorizes it — across both stream
// route patterns and both lookup branches (by mediaSourceId, and by route item
// id when mediaSourceId is absent).
func TestPlaybackSessionAuth_DirectPlayNoToken(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }

	cases := []struct {
		name string
		url  string
	}{
		{"stream + mediaSourceId", "/Videos/item123/stream?static=true&mediaSourceId=src9&container=mkv"},
		{"stream.{container} + mediaSourceId", "/Videos/item123/stream.mkv?static=true&mediaSourceId=src9"},
		{"stream, route-item lookup (no mediaSourceId)", "/Videos/item123/stream?static=true&container=mkv"},
		{"stream.{container}, route-item lookup", "/Videos/item123/stream.mkv?static=true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessions, playback := seedDirectPlaySession(t, clock)
			var reached bool
			router := directPlayRouter(t, sessions, playback, nil, &reached)

			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if !reached {
				t.Fatal("expected inner handler to be reached via PlaybackSession fallback")
			}
		})
	}
}

// TestPlaybackSessionAuth_DirectPlayNoMatchingSession: with a resolvable compat
// session present but NO PlaybackSession negotiated for the requested item, the
// token-less request stays a 401 (proves the playback-session match gates it,
// not a missing compat session).
func TestPlaybackSessionAuth_DirectPlayNoMatchingSession(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }
	sessions := NewSessionStore(time.Hour, clock)
	if err := sessions.Put(Session{Token: "compat-tok", StreamAppUserID: 7}); err != nil {
		t.Fatalf("put session: %v", err)
	}
	playback := NewPlaybackSessionStore(time.Hour, clock)
	// Session exists, but for a different item id and source id.
	playback.Put(PlaybackSession{ID: "ps1", CompatToken: "compat-tok", RouteItemID: "other"})

	var reached bool
	router := directPlayRouter(t, sessions, playback, nil, &reached)

	req := httptest.NewRequest(http.MethodGet, "/Videos/item123/stream?static=true&mediaSourceId=src9", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
	if reached {
		t.Fatal("inner handler must not run without a matching PlaybackSession")
	}
}

// TestPlaybackSessionAuth_DirectPlayCrossItemDenied: a mediaSourceId that resolves
// to a session for a DIFFERENT route item must not authorize the request — the
// session's RouteItemID must match the requested item.
func TestPlaybackSessionAuth_DirectPlayCrossItemDenied(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }
	sessions := NewSessionStore(time.Hour, clock)
	if err := sessions.Put(Session{Token: "compat-tok", StreamAppUserID: 7}); err != nil {
		t.Fatalf("put session: %v", err)
	}
	playback := NewPlaybackSessionStore(time.Hour, clock)
	// src9 belongs to itemB's session; the request targets itemA.
	playback.Put(PlaybackSession{
		ID:           "psB",
		CompatToken:  "compat-tok",
		RouteItemID:  "itemB",
		MediaSources: []PlaybackMediaSource{{ID: "src9"}},
	})

	var reached bool
	router := directPlayRouter(t, sessions, playback, nil, &reached)

	req := httptest.NewRequest(http.MethodGet, "/Videos/itemA/stream?static=true&mediaSourceId=src9", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
	if reached {
		t.Fatal("inner handler must not run when the matched session belongs to another item")
	}
}

// TestPlaybackSessionAuth_DownloadNotLoosened: the new direct-play fallback must
// not apply to /Items/{id}/Download — a token-less download stays 401 even when a
// resolvable compat session AND a PlaybackSession exist for the same item id, so
// the 401 proves route scoping rather than a missing session.
func TestPlaybackSessionAuth_DownloadNotLoosened(t *testing.T) {
	now := fixedNow()
	clock := func() time.Time { return now }
	sessions, playback := seedDirectPlaySession(t, clock)

	var reached bool
	router := directPlayRouter(t, sessions, playback, nil, &reached)

	req := httptest.NewRequest(http.MethodGet, "/Items/item123/Download", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
	if reached {
		t.Fatal("download route must not be served via the direct-play fallback")
	}
}
