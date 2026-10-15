package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// While a node key rotates, the node refuses the proxy's signature with 401.
// Relayed as is, a player reads that as its own sign-in failing and signs out,
// so every relay answers it as the node being unavailable. A client whose own
// token is bad still gets the proxy's 401.
func TestTranscodeNodeRelaysAnswerANodeRefusalAsNodeUnavailable(t *testing.T) {
	var nodeHits atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nodeHits.Add(1)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
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
	sign := func(claims streamtoken.Claims, secret string) string {
		token, err := streamtoken.Sign(claims, secret, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	bearer := grantAccessToken(t, 7, "login-1")
	for _, relay := range []struct {
		name, method, path, bearer string
	}{
		{"transcode manifest", http.MethodGet, "/stream/transcode/" + sign(transcode, grantTestSecret) + "/master.m3u8", ""},
		{"transcode segment", http.MethodGet, "/stream/transcode/" + sign(transcode, grantTestSecret) + "/segment/seg_00001.ts", ""},
		{"progressive remux", http.MethodGet, "/stream/remux/" + sign(remux, grantTestSecret), ""},
		{"progressive remux HEAD", http.MethodHead, "/stream/remux/" + sign(remux, grantTestSecret), ""},
		{"theme conversion", http.MethodGet, "/stream/theme/" + sign(theme, grantTestSecret), ""},
		{"grant manifest", http.MethodGet, "/stream/v3/grant-transcode/master.m3u8", bearer},
		{"grant segment", http.MethodGet, "/stream/v3/grant-transcode/segment/seg_00001.ts", bearer},
		{"grant remux", http.MethodGet, "/stream/v3/grant-remux", bearer},
	} {
		before := nodeHits.Load()
		rr := grantRequest(t, srv, relay.method, relay.path, relay.bearer)
		if nodeHits.Load() == before {
			t.Fatalf("%s never reached the node (%d %q)", relay.name, rr.Code, rr.Body.String())
		}
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("%s with the node refusing the proxy = %d %q, want 502", relay.name, rr.Code, rr.Body.String())
		}
		if relay.method == http.MethodGet && strings.TrimSpace(rr.Body.String()) != "transcode node unavailable" {
			t.Fatalf("%s body = %q, want the relay's node-unavailable error", relay.name, rr.Body.String())
		}
	}

	// The proxy's own refusal of a bad client token stays a 401.
	before := nodeHits.Load()
	rr := grantRequest(t, srv, http.MethodGet, "/stream/transcode/"+sign(transcode, "another-secret")+"/master.m3u8", "")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad client token = %d %q, want the proxy's 401", rr.Code, rr.Body.String())
	}
	if nodeHits.Load() != before {
		t.Fatal("a request with a bad client token reached the node")
	}
}
