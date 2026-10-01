package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

const grantTestSecret = "media-grant-proxy-secret"

type stubGrantStore struct {
	cards map[string]playback.RecipeCard
}

func (s stubGrantStore) Get(_ context.Context, sessionID string) (*playback.RecipeCard, bool) {
	card, ok := s.cards[sessionID]
	if !ok {
		return nil, false
	}
	return &card, true
}

type stubLoginSessions struct {
	valid map[string]bool
}

func (s stubLoginSessions) IsValid(_ context.Context, sessionID string) (bool, error) {
	return s.valid[sessionID], nil
}

func newGrantProxyServer(t *testing.T, cards map[string]playback.RecipeCard) *Server {
	t.Helper()
	w := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = grantTestSecret
	w.SetConfigForTest(cfg)
	srv := NewServer(w, nodesessions.NewTracker(nil, "http://proxy-1", "proxy-1", "proxy"))
	srv.SetMediaGrantAuthority(stubGrantStore{cards: cards}, stubLoginSessions{valid: map[string]bool{"login-1": true}})
	return srv
}

func grantAccessToken(t *testing.T, userID int, loginSessionID string) string {
	t.Helper()
	token, err := auth.NewJWTService(grantTestSecret, time.Hour, time.Hour).GenerateAccessToken(userID, "user", loginSessionID)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func grantRequest(t *testing.T, srv *Server, method, path, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

func writeGrantMedia(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "movie.mp4")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The grant says what to serve; the caller's own access token says whether they
// may have it. Every way of failing the second question must refuse before any
// media byte is written.
func TestProxyGrantRoutesRefuseUnauthenticatedAndUnauthorizedCallers(t *testing.T) {
	path := writeGrantMedia(t, "0123456789")
	cards := map[string]playback.RecipeCard{
		"session-1": {SessionID: "session-1", UserID: 7, ProfileID: "profile-1", MediaFileID: 42, PlayMethod: playback.PlayDirect, InputPath: path},
	}
	srv := newGrantProxyServer(t, cards)

	for _, test := range []struct {
		name       string
		path       string
		bearer     string
		wantStatus int
		wantError  string
	}{
		{name: "no bearer", path: "/stream/v3/session-1", wantStatus: http.StatusUnauthorized, wantError: "unauthorized"},
		{name: "token signed by another secret", path: "/stream/v3/session-1", bearer: foreignAccessToken(t), wantStatus: http.StatusUnauthorized, wantError: "unauthorized"},
		{name: "revoked login session", path: "/stream/v3/session-1", bearer: grantAccessToken(t, 7, "login-revoked"), wantStatus: http.StatusUnauthorized, wantError: "unauthorized"},
		{name: "another user's session", path: "/stream/v3/session-1", bearer: grantAccessToken(t, 8, "login-1"), wantStatus: http.StatusForbidden, wantError: "forbidden"},
		{name: "no grant", path: "/stream/v3/session-missing", bearer: grantAccessToken(t, 7, "login-1"), wantStatus: http.StatusNotFound, wantError: "playback_session_not_found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rr := grantRequest(t, srv, http.MethodGet, test.path, test.bearer)
			if rr.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rr.Code, test.wantStatus, rr.Body.String())
			}
			var body grantErrorResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("error body %q is not the API's error shape: %v", rr.Body.String(), err)
			}
			if body.Error != test.wantError {
				t.Fatalf("error code = %q, want %q", body.Error, test.wantError)
			}
			if rr.Body.Len() > 0 && rr.Body.String()[0] == '0' {
				t.Fatal("refused request received media bytes")
			}
		})
	}

}

// An API key is a machine credential, not a viewer: it is refused here rather
// than validated, so scope enforcement stays on the API.
func TestProxyGrantRoutesRefuseAPIKeys(t *testing.T) {
	srv := newGrantProxyServer(t, map[string]playback.RecipeCard{})
	rr := grantRequest(t, srv, http.MethodGet, "/stream/v3/session-1", "sa_"+grantAccessToken(t, 7, "login-1"))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", rr.Code, rr.Body.String())
	}
}

// A node that predates the mode (no grant store, no database) must not pretend
// it can serve these routes, and must keep working otherwise.
func TestProxyGrantRoutesReportUnavailableWithoutTheirDependencies(t *testing.T) {
	w := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = grantTestSecret
	w.SetConfigForTest(cfg)
	srv := NewServer(w, nodesessions.NewTracker(nil, "http://proxy-1", "proxy-1", "proxy"))

	rr := grantRequest(t, srv, http.MethodGet, "/stream/v3/session-1", grantAccessToken(t, 7, "login-1"))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", rr.Code, rr.Body.String())
	}
}

func TestProxyGrantDirectPlayServesTheGrantedFile(t *testing.T) {
	path := writeGrantMedia(t, "0123456789")
	srv := newGrantProxyServer(t, map[string]playback.RecipeCard{
		"session-1": {SessionID: "session-1", UserID: 7, ProfileID: "profile-1", MediaFileID: 42, PlayMethod: playback.PlayDirect, InputPath: path},
	})

	rr := grantRequest(t, srv, http.MethodGet, "/stream/v3/session-1", grantAccessToken(t, 7, "login-1"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != "0123456789" {
		t.Fatalf("body = %q, want the granted file's bytes", rr.Body.String())
	}
	// direct_stream_resume_v1 needs the strong validator the shared serve path
	// sets; the grant route must not lose it by serving the file some other way.
	if rr.Header().Get("ETag") == "" {
		t.Fatal("direct play served without an ETag; a resumed range could not validate")
	}
}

func TestProxyGrantRoutesEnforceCommittedProxyEgress(t *testing.T) {
	path := writeGrantMedia(t, "proxy-authorized")
	base := playback.RecipeCard{
		SessionID: "base", UserID: 7, ProfileID: "profile-1", MediaFileID: 42,
		PlayMethod: playback.PlayDirect, InputPath: path,
	}
	api := base
	api.SessionID = "api"
	api.RoutingWorkload = string(noderouting.WorkloadDirectPlay)
	api.RoutingExecution = string(noderouting.ExecutionNone)
	api.RoutingEgress = string(noderouting.EgressAPI)
	partial := base
	partial.SessionID = "partial"
	partial.RoutingWorkload = string(noderouting.WorkloadDirectPlay)
	proxy := base
	proxy.SessionID = "proxy"
	proxy.RoutingWorkload = string(noderouting.WorkloadDirectPlay)
	proxy.RoutingExecution = string(noderouting.ExecutionNone)
	proxy.RoutingEgress = string(noderouting.EgressProxy)
	proxy.RoutingEgressNodeID = 11
	legacy := base
	legacy.SessionID = "legacy"
	srv := newGrantProxyServer(t, map[string]playback.RecipeCard{
		api.SessionID: api, partial.SessionID: partial, proxy.SessionID: proxy, legacy.SessionID: legacy,
	})
	srv.nodeRowID = func() (int, bool) { return 11, true }
	bearer := grantAccessToken(t, 7, "login-1")

	for _, test := range []struct {
		sessionID  string
		wantStatus int
		wantError  string
		wantBody   string
	}{
		{sessionID: "api", wantStatus: http.StatusServiceUnavailable, wantError: "routing_policy_unsatisfied"},
		{sessionID: "partial", wantStatus: http.StatusConflict, wantError: "playback_route_unbound"},
		{sessionID: "proxy", wantStatus: http.StatusOK, wantBody: "proxy-authorized"},
		{sessionID: "legacy", wantStatus: http.StatusOK, wantBody: "proxy-authorized"},
	} {
		t.Run(test.sessionID, func(t *testing.T) {
			recorder := grantRequest(t, srv, http.MethodGet, "/stream/v3/"+test.sessionID, bearer)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if test.wantBody != "" {
				if recorder.Body.String() != test.wantBody {
					t.Fatalf("body = %q, want %q", recorder.Body.String(), test.wantBody)
				}
				return
			}
			var body grantErrorResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error != test.wantError {
				t.Fatalf("error = %q, want %q", body.Error, test.wantError)
			}
		})
	}

	sibling := newGrantProxyServer(t, map[string]playback.RecipeCard{proxy.SessionID: proxy})
	sibling.nodeRowID = func() (int, bool) { return 12, true }
	recorder := grantRequest(t, sibling, http.MethodGet, "/stream/v3/"+proxy.SessionID, bearer)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("sibling proxy status = %d, want 503; body = %s", recorder.Code, recorder.Body.String())
	}

	recorder = grantRequest(t, srv, http.MethodGet, "/stream/v3/"+proxy.SessionID+"/master.m3u8", bearer)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("identity grant on transcode route = %d, want 503; body = %s", recorder.Code, recorder.Body.String())
	}
}

// A transcode grant is relayed to the node exactly like the token route, with
// the node-facing token minted here — never handed to the client.
func TestProxyGrantTranscodeRelaysToTheNodeWithAProxyMintedToken(t *testing.T) {
	var forwarded, authorization, relayPath string
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded = r.Header.Get("X-Silo-Stream-Token")
		authorization = r.Header.Get("Authorization")
		relayPath = r.URL.Path
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\nsegment/seg_00001.m4s\n"))
	}))
	defer node.Close()

	srv := newGrantProxyServer(t, map[string]playback.RecipeCard{
		"session-hls": {
			SessionID:            "session-hls",
			UserID:               7,
			ProfileID:            "profile-1",
			MediaFileID:          42,
			PlayMethod:           playback.PlayTranscode,
			TranscodeNodeURL:     node.URL,
			TranscodeTransportID: "session-hls-plan-a",
			InputPath:            "/media/movie.mkv",
			TargetCodecVideo:     "h264",
		},
	})

	rr := grantRequest(t, srv, http.MethodGet, "/stream/v3/session-hls/master.m3u8", grantAccessToken(t, 7, "login-1"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if relayPath != "/transcode/session-hls-plan-a/master.m3u8" {
		t.Fatalf("relay path = %q, want the plan-scoped transport manifest", relayPath)
	}
	if authorization != "Bearer "+grantTestSecret {
		t.Fatalf("node authorization = %q", authorization)
	}
	claims, err := streamtoken.Verify(forwarded, grantTestSecret)
	if err != nil {
		t.Fatalf("verify forwarded stream token: %v", err)
	}
	if claims.SessionID != "session-hls" || claims.MediaPath != "/media/movie.mkv" {
		t.Fatalf("forwarded claims = %#v, want the granted recipe", claims)
	}
	// The manifest's segment URIs stay relative, so they resolve back into this
	// same credential-free family rather than a token route.
	if body := rr.Body.String(); body != "#EXTM3U\nsegment/seg_00001.m4s\n" {
		t.Fatalf("manifest body = %q, want the node's relative segment URIs", body)
	}

	segment := grantRequest(t, srv, http.MethodGet, "/stream/v3/session-hls/segment/seg_00001.m4s", grantAccessToken(t, 7, "login-1"))
	if segment.Code != http.StatusOK {
		t.Fatalf("segment status = %d, body = %s", segment.Code, segment.Body.String())
	}
	if relayPath != "/transcode/session-hls-plan-a/segment/seg_00001.m4s" {
		t.Fatalf("segment relay path = %q", relayPath)
	}
}

func TestProxyGrantProgressiveRemuxRelaysToTranscodeNode(t *testing.T) {
	var forwarded, relayPath, relayQuery string
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded = r.Header.Get("X-Silo-Stream-Token")
		relayPath = r.URL.Path
		relayQuery = r.URL.RawQuery
		_, _ = w.Write([]byte("remote-remux"))
	}))
	t.Cleanup(node.Close)

	card := playback.NewRemuxRecipeCard("session-remux", 7, "profile-1", 42, false, 0, playback.RemuxDVPreserveV3)
	card.InputPath = "/media/movie.mkv"
	card.TranscodeNodeURL = node.URL
	card.TranscodeTransportID = "session-remux-plan-a"
	card.RoutingWorkload = string(noderouting.WorkloadRemux)
	card.RoutingExecution = string(noderouting.ExecutionTranscode)
	card.RoutingEgress = string(noderouting.EgressProxy)
	card.RoutingEgressNodeID = 11
	srv := newGrantProxyServer(t, map[string]playback.RecipeCard{card.SessionID: card})
	srv.nodeRowID = func() (int, bool) { return 11, true }

	rr := grantRequest(t, srv, http.MethodGet, "/stream/v3/session-remux?seek=8", grantAccessToken(t, 7, "login-1"))
	if rr.Code != http.StatusOK || rr.Body.String() != "remote-remux" {
		t.Fatalf("response = %d %q", rr.Code, rr.Body.String())
	}
	if relayPath != "/remux/session-remux-plan-a" || relayQuery != "seek=8" {
		t.Fatalf("relay = %q?%s", relayPath, relayQuery)
	}
	claims, err := streamtoken.Verify(forwarded, grantTestSecret)
	if err != nil || claims.TranscodeNode != node.URL || claims.RoutingExecution != string(noderouting.ExecutionTranscode) {
		t.Fatalf("forwarded claims = %#v, err=%v", claims, err)
	}
}

// A transcode session has no progressive body to serve, and answering one from
// the identity route would hand the client a stream the plan never described.
func TestProxyGrantIdentityRefusesATranscodeGrant(t *testing.T) {
	srv := newGrantProxyServer(t, map[string]playback.RecipeCard{
		"session-hls": {SessionID: "session-hls", UserID: 7, PlayMethod: playback.PlayTranscode, TranscodeNodeURL: "http://node-1"},
	})
	rr := grantRequest(t, srv, http.MethodGet, "/stream/v3/session-hls", grantAccessToken(t, 7, "login-1"))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", rr.Code, rr.Body.String())
	}
}

func foreignAccessToken(t *testing.T) string {
	t.Helper()
	token, err := auth.NewJWTService("a-different-secret", time.Hour, time.Hour).GenerateAccessToken(7, "user", "login-1")
	if err != nil {
		t.Fatal(err)
	}
	return token
}
