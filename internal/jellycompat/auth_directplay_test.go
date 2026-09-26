package jellycompat

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/clientip"
)

// directPlayRouter wraps the probe handler in PlaybackSessionAuth and mounts it
// on a chi router so RoutePattern() and URLParam("id") are populated exactly as
// they are in production. The same handler also fronts /Items/{id}/Download to
// check that playback grants remain scoped to their negotiated item.
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
	r.Handle("/Videos/{routeItemId}/{routeMediaSourceId}/Subtitles/{routeIndex}/stream.{routeFormat}", probe)
	return r
}

func TestPlaybackSessionAuth_ExplicitPlaybackGrant(t *testing.T) {
	for _, tc := range []struct {
		name, path, token string
		want              int
	}{
		{"item ID alone", "/Videos/item123/stream.mkv", "", http.StatusUnauthorized},
		{"source ID alone", "/Videos/item123/stream?mediaSourceId=src9", "", http.StatusUnauthorized},
		{"login token", "/Videos/item123/stream.mkv", "compat-tok", http.StatusOK},
		{"playback grant", "/Videos/item123/stream.mkv?playSessionId=ps1", "", http.StatusOK},
		{"selected source grant", "/Videos/item123/stream.mkv?PlaySessionId=ps1&MediaSourceId=src9", "", http.StatusOK},
		{"foreign source", "/Videos/item123/stream.mkv?PlaySessionId=ps1&MediaSourceId=other", "", http.StatusUnauthorized},
		{"foreign item", "/Videos/other/stream.mkv?PlaySessionId=ps1", "", http.StatusUnauthorized},
		{"foreign download", "/Items/other/Download?PlaySessionId=ps1", "", http.StatusUnauthorized},
		{"own subtitle", "/Videos/item123/src9/Subtitles/2/stream.vtt?PlaySessionId=ps1", "", http.StatusOK},
		{"foreign subtitle source", "/Videos/item123/other/Subtitles/2/stream.vtt?PlaySessionId=ps1", "", http.StatusUnauthorized},
		{"invalid token cannot fall back", "/Videos/item123/stream.mkv?PlaySessionId=ps1", "invalid", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				sessions, playback := seedDirectPlaySession(t, fixedNow)
				var reached bool
				router := directPlayRouter(t, sessions, playback, nil, &reached)
				req := httptest.NewRequest(method, tc.path, nil)
				if tc.token != "" {
					req.Header.Set("X-Emby-Token", tc.token)
				}
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if rec.Code != tc.want || reached != (tc.want == http.StatusOK) {
					t.Fatalf("%s status=%d reached=%v, want %d; %s", method, rec.Code, reached, tc.want, rec.Body.String())
				}
			}
		})
	}
}

func TestPlaybackSessionAuth_ExpiredRevokedAndTerminalGrants(t *testing.T) {
	for _, state := range []string{"expired", "revoked", "terminal"} {
		t.Run(state, func(t *testing.T) {
			now := fixedNow()
			sessions, playback := seedDirectPlaySession(t, func() time.Time { return now })
			switch state {
			case "expired":
				now = now.Add(2 * time.Hour)
			case "revoked":
				sessions.Delete("compat-tok")
			case "terminal":
				if err := playback.HideFromRouting("ps1", "compat-tok"); err != nil {
					t.Fatal(err)
				}
			}
			var reached bool
			router := directPlayRouter(t, sessions, playback, nil, &reached)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Videos/item123/stream.mkv?PlaySessionId=ps1", nil))
			if rec.Code != http.StatusUnauthorized || reached {
				t.Fatalf("status=%d reached=%v, want 401 without handler", rec.Code, reached)
			}
		})
	}
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

// Knowing an item or media-source ID must never grant another viewer's session.
func TestPlaybackSessionAuth_DirectPlayRequiresAuthority(t *testing.T) {
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

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
			}
			if reached {
				t.Fatal("item/source identifiers must not authorize the handler")
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

func requestWithCompatRouteItem(req *http.Request, itemID string) *http.Request {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", itemID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

// Jellyfin for Android TV and Findroid open static streams with no token and
// no PlaySessionId. Only a live, recently active negotiation from the same
// client address and transport peer for the same item and source grants one.
func TestPlaybackSessionAuth_CredentialLessStaticStreamGrant(t *testing.T) {
	const grantPath = "/Videos/item123/stream?static=true&mediaSourceId=src9"
	for _, tc := range []struct {
		name, method, path string
		// peer is the transport address; forwarded is an X-Forwarded-For value.
		peer, forwarded, token string
		directNegotiation      bool
		mutate                 func(now *time.Time, sessions *SessionStore, playback *PlaybackSessionStore)
		want                   int
	}{
		{name: "negotiated source from same client", path: grantPath, want: http.StatusOK},
		{name: "head", method: http.MethodHead, path: grantPath, want: http.StatusOK},
		{name: "container route", path: "/Videos/item123/stream.mkv?Static=true&MediaSourceId=src9", want: http.StatusOK},
		{name: "same client through trusted proxy", path: grantPath, peer: "10.0.0.1:443", forwarded: "192.0.2.1", want: http.StatusOK},
		{name: "same client through another load-balanced proxy", path: grantPath, peer: "10.0.0.2:443", forwarded: "192.0.2.1", want: http.StatusOK},
		{name: "not static", path: "/Videos/item123/stream?mediaSourceId=src9", want: http.StatusUnauthorized},
		{name: "static=1 is not the handler's static", path: "/Videos/item123/stream?static=1&mediaSourceId=src9", want: http.StatusUnauthorized},
		{name: "duplicate Static keys", path: "/Videos/item123/stream?static=true&Static=false&mediaSourceId=src9", want: http.StatusUnauthorized},
		{name: "duplicate source keys", path: "/Videos/item123/stream?static=true&mediaSourceId=src9&MediaSourceId=other", want: http.StatusUnauthorized},
		{name: "no source", path: "/Videos/item123/stream?static=true", want: http.StatusUnauthorized},
		{name: "other source", path: "/Videos/item123/stream?static=true&mediaSourceId=other", want: http.StatusUnauthorized},
		{name: "other item", path: "/Videos/other/stream?static=true&mediaSourceId=src9", want: http.StatusUnauthorized},
		{name: "download route", path: "/Items/item123/Download?static=true&mediaSourceId=src9", want: http.StatusUnauthorized},
		{name: "other address", path: grantPath, peer: "198.51.100.7:4000", want: http.StatusUnauthorized},
		{name: "proxied request for a directly connected negotiation", path: grantPath, peer: "10.0.0.9:5000", forwarded: "192.0.2.1", directNegotiation: true, want: http.StatusUnauthorized},
		{name: "invalid token is not rescued by a grant", path: grantPath, token: "invalid", want: http.StatusUnauthorized},
		{name: "idle negotiation", path: grantPath, mutate: func(now *time.Time, _ *SessionStore, _ *PlaybackSessionStore) {
			*now = now.Add(staticStreamGrantIdle + time.Minute)
		}, want: http.StatusUnauthorized},
		{name: "progress reports keep a long play granted", path: grantPath, mutate: func(now *time.Time, _ *SessionStore, playback *PlaybackSessionStore) {
			for range 3 {
				*now = now.Add(staticStreamGrantIdle - time.Minute)
				session, _ := playback.Get("ps1")
				session.UpdatedAt = now.Add(-staticStreamGrantTouchInterval)
				touchStaticStreamGrant(playback, session, false)
			}
		}, want: http.StatusOK},
		{name: "terminal negotiation", path: grantPath, mutate: func(_ *time.Time, _ *SessionStore, playback *PlaybackSessionStore) {
			if err := playback.HideFromRouting("ps1", "compat-tok"); err != nil {
				t.Fatal(err)
			}
		}, want: http.StatusUnauthorized},
		{name: "revoked login", path: grantPath, mutate: func(_ *time.Time, sessions *SessionStore, _ *PlaybackSessionStore) {
			sessions.Delete("compat-tok")
		}, want: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := fixedNow()
			clock := func() time.Time { return now }
			sessions := NewSessionStore(24*time.Hour, clock)
			if err := sessions.Put(Session{Token: "compat-tok", StreamAppUserID: 7}); err != nil {
				t.Fatal(err)
			}
			playback := NewPlaybackSessionStore(24*time.Hour, clock)
			negotiatedPeer := "192.0.2.1"
			if tc.forwarded != "" && !tc.directNegotiation {
				negotiatedPeer = "10.0.0.1" // negotiated through the same proxy
			}
			playback.PutNegotiated(PlaybackSession{ID: "ps1", CompatToken: "compat-tok", ClientIP: "192.0.2.1", ClientPeer: negotiatedPeer, RouteItemID: "item123", MediaSources: []PlaybackMediaSource{{ID: "src9"}}})
			if tc.mutate != nil {
				tc.mutate(&now, sessions, playback)
			}
			_, trustedLAN, _ := net.ParseCIDR("10.0.0.0/8")
			var reached bool
			router := clientip.Middleware(clientip.NewResolver([]*net.IPNet{trustedLAN}))(directPlayRouter(t, sessions, playback, nil, &reached))
			method := tc.method
			if method == "" {
				method = http.MethodGet
			}
			req := httptest.NewRequest(method, tc.path, nil)
			if tc.peer != "" {
				req.RemoteAddr = tc.peer
			}
			if tc.forwarded != "" {
				req.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			if tc.token != "" {
				req.Header.Set("X-Emby-Token", tc.token)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.want || reached != (tc.want == http.StatusOK) {
				t.Fatalf("status=%d reached=%v, want %d; %s", rec.Code, reached, tc.want, rec.Body.String())
			}
		})
	}
}
