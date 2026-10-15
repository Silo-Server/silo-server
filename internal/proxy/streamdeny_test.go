package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/noderouting"
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

// A transcode node checks no deny marker: every proxy route that relays to
// one must, before it signs the node request. A denied session gets 410 even
// though the node runtime is alive (its stop DELETE failed), and the node is
// never asked.
func TestStreamDenyRefusesEveryTranscodeNodeRelay(t *testing.T) {
	var nodeHits atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nodeHits.Add(1)
		_, _ = w.Write([]byte("#EXTM3U\n"))
	}))
	t.Cleanup(node.Close)

	routed := func(claims streamtoken.Claims) streamtoken.Claims {
		claims.TranscodeNode, claims.TranscodeTransportID = node.URL, claims.SessionID+"-transport"
		return claims
	}
	transcode := routed(streamtoken.Claims{SessionID: "transcode-session", PlayMethod: string(playback.PlayTranscode)})
	remux := routed(streamtoken.Claims{
		SessionID: "remux-session", MediaPath: "/media/movie.mkv", PlayMethod: string(playback.PlayRemux),
		RoutingWorkload: string(noderouting.WorkloadRemux), RoutingExecution: string(noderouting.ExecutionTranscode),
		RoutingExecutionNodeID: 11, RoutingEgress: string(noderouting.EgressProxy), RoutingEgressNodeID: 3,
	})
	downmix := remux
	downmix.SessionID, downmix.PlayMethod = "downmix-session", streamtoken.PlayMethodAudioDownmixRemux
	theme := routed(streamtoken.Claims{
		SessionID: "theme-session", MediaPath: "/media/theme.mp3", PlayMethod: streamtoken.PlayMethodThemeAAC,
		ThemeID: 5, TranscodeAudio: true, AudioOnly: true, TargetCodecAudio: "aac",
		RoutingWorkload: string(noderouting.WorkloadRemux), RoutingExecution: string(noderouting.ExecutionTranscode),
		RoutingExecutionNodeID: 11, RoutingEgress: string(noderouting.EgressProxy), RoutingEgressNodeID: 3,
	})
	grantTranscode := playback.RecipeCardFromClaims(&transcode)
	grantTranscode.SessionID, grantTranscode.UserID = "grant-transcode", 7
	grantRemux := playback.RecipeCardFromClaims(&remux)
	grantRemux.SessionID, grantRemux.UserID = "grant-remux", 7

	srv := newGrantProxyServer(t, map[string]playback.RecipeCard{"grant-transcode": grantTranscode, "grant-remux": grantRemux})
	srv.nodeRowID = func() (int, bool) { return 3, true }
	deny := unreachableStreamDeny(t)
	srv.SetStreamDeny(deny)
	sign := func(claims streamtoken.Claims) string {
		token, err := streamtoken.Sign(claims, grantTestSecret, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	bearer := grantAccessToken(t, 7, "login-1")
	relays := []struct {
		name, method, path, bearer, session string
	}{
		{"transcode manifest", http.MethodGet, "/stream/transcode/" + sign(transcode) + "/master.m3u8", "", transcode.SessionID},
		{"transcode manifest HEAD", http.MethodHead, "/stream/transcode/" + sign(transcode) + "/master.m3u8", "", transcode.SessionID},
		{"transcode segment", http.MethodGet, "/stream/transcode/" + sign(transcode) + "/segment/seg_00001.ts", "", transcode.SessionID},
		{"progressive remux", http.MethodGet, "/stream/remux/" + sign(remux), "", remux.SessionID},
		{"audio-v2 remux", http.MethodGet, "/stream/remux/audio-v2/" + sign(downmix), "", downmix.SessionID},
		{"theme conversion", http.MethodGet, "/stream/theme/" + sign(theme), "", theme.SessionID},
		{"grant manifest", http.MethodGet, "/stream/v3/grant-transcode/master.m3u8", bearer, "grant-transcode"},
		{"grant segment", http.MethodGet, "/stream/v3/grant-transcode/segment/seg_00001.ts", bearer, "grant-transcode"},
		{"grant remux", http.MethodGet, "/stream/v3/grant-remux", bearer, "grant-remux"},
	}

	// The node is alive and the relays reach it while nothing is denied.
	for _, relay := range relays[:3] {
		if rr := grantRequest(t, srv, relay.method, relay.path, relay.bearer); rr.Code != http.StatusOK {
			t.Fatalf("%s before the deny = %d %q", relay.name, rr.Code, rr.Body.String())
		}
	}
	served := nodeHits.Load()
	if served == 0 {
		t.Fatal("the relays never reached the node")
	}

	for _, relay := range relays {
		deny.Deny(t.Context(), relay.session)
		rr := grantRequest(t, srv, relay.method, relay.path, relay.bearer)
		if rr.Code != http.StatusGone {
			t.Fatalf("%s of a denied session = %d %q, want 410", relay.name, rr.Code, rr.Body.String())
		}
	}
	if got := nodeHits.Load(); got != served {
		t.Fatalf("node hits = %d after the deny, want %d: a relay forwarded a denied session", got, served)
	}
}
