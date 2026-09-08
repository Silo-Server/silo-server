package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// unreachableStreamDeny returns a marker store whose Redis never answers, so
// Deny populates only the in-process cache and every other lookup fails open.
// That is enough to prove the serve paths consult the marker before serving,
// without a Redis in the test environment.
func unreachableStreamDeny(t *testing.T) *playback.StreamDeny {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0", DialTimeout: 50 * time.Millisecond, MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	return playback.NewStreamDeny(client)
}

// A stream token stays valid for its whole TTL, so the deny marker is the only
// thing that stops a stopped, expired, or terminated session from streaming
// through the token routes.
func TestStreamDenyRefusesTokenRoutesBeforeServing(t *testing.T) {
	path := writeGrantMedia(t, "0123456789")
	srv := newGrantProxyServer(t, nil)
	deny := unreachableStreamDeny(t)
	srv.SetStreamDeny(deny)
	deny.Deny(t.Context(), "denied-session")

	token, err := streamtoken.Sign(streamtoken.Claims{
		SessionID: "denied-session", MediaPath: path, PlayMethod: string(playback.PlayDirect),
	}, grantTestSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/stream/direct/"+token, nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusGone || strings.TrimSpace(rr.Body.String()) != "playback session ended" {
		t.Fatalf("denied session = %d %q, want 410 playback session ended", rr.Code, rr.Body.String())
	}
	if !strings.HasPrefix(rr.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", rr.Header().Get("Content-Type"))
	}

	// An undenied session with an unreachable Redis fails open and streams.
	live, err := streamtoken.Sign(streamtoken.Claims{
		SessionID: "live-session", MediaPath: path, PlayMethod: string(playback.PlayDirect),
	}, grantTestSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/stream/direct/"+live, nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "0123456789" {
		t.Fatalf("fail-open live session = %d %q, want 200 with media", rr.Code, rr.Body.String())
	}
}

// The grant outlives the session it describes, so /stream/v3 needs the same
// marker check — with the JSON error shape that route family answers with.
func TestStreamDenyRefusesGrantRoutes(t *testing.T) {
	path := writeGrantMedia(t, "0123456789")
	srv := newGrantProxyServer(t, map[string]playback.RecipeCard{
		"session-1": {SessionID: "session-1", UserID: 7, ProfileID: "profile-1", MediaFileID: 42, PlayMethod: playback.PlayDirect, InputPath: path},
	})
	deny := unreachableStreamDeny(t)
	srv.SetStreamDeny(deny)
	deny.Deny(t.Context(), "session-1")

	rr := grantRequest(t, srv, http.MethodGet, "/stream/v3/session-1", grantAccessToken(t, 7, "login-1"))
	if rr.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410 (body %s)", rr.Code, rr.Body.String())
	}
	var body grantErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body %q is not the API's error shape: %v", rr.Body.String(), err)
	}
	if body.Error != "playback_session_ended" {
		t.Fatalf("error code = %q, want playback_session_ended", body.Error)
	}
}
