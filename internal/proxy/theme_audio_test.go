package proxy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

func writeThemeAudio(t *testing.T) (string, int64, int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "theme.mp3")
	if err := os.WriteFile(path, []byte(socketProxyMedia), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, info.Size(), info.ModTime().Truncate(time.Microsecond).UnixNano()
}

func themeDirectClaims(path string, size, modified int64) streamtoken.Claims {
	return streamtoken.Claims{
		SessionID: "theme-direct", MediaPath: path, PlayMethod: streamtoken.PlayMethodThemeDirect, AudioOnly: true,
		UserID: 7, ProfileID: "profile-1", ThemeID: 9, ThemeSize: size, ThemeModifiedUnixNano: modified,
		RoutingWorkload: string(noderouting.WorkloadDirectPlay), RoutingExecution: string(noderouting.ExecutionNone),
		RoutingEgress: string(noderouting.EgressProxy), RoutingEgressNodeID: 11,
	}
}

func themeAACClaims(path string, size, modified int64) streamtoken.Claims {
	claims := themeDirectClaims(path, size, modified)
	claims.SessionID = "theme-aac"
	claims.PlayMethod = streamtoken.PlayMethodThemeAAC
	claims.RoutingWorkload = string(noderouting.WorkloadRemux)
	claims.RoutingExecution = string(noderouting.ExecutionProxy)
	claims.RoutingExecutionNodeID = 11
	claims.TranscodeAudio = true
	claims.TargetCodecAudio = "aac"
	claims.TargetAudioChannels = 2
	claims.TargetAudioBitrateKbps = 192
	return claims
}

func TestThemeAudioRouteServesOnlyThemeTokens(t *testing.T) {
	const secret = "proxy-theme-secret"
	path, size, modified := writeThemeAudio(t)
	srv := newSocketProxyServer(t, secret, nil)
	srv.nodeRowID = func() (int, bool) { return 11, true }
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	sign := func(claims streamtoken.Claims) string {
		t.Helper()
		token, err := streamtoken.Sign(claims, secret, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	direct := themeDirectClaims(path, size, modified)
	directToken := sign(direct)

	response := socketProxyRequest(t, server.Client(), http.MethodGet, server.URL+"/stream/theme/"+directToken, map[string]string{"Range": "bytes=2-5"})
	if response.status != http.StatusPartialContent || response.body != socketProxyMedia[2:6] {
		t.Fatalf("ranged theme = %d %q", response.status, response.body)
	}
	if head := socketProxyRequest(t, server.Client(), http.MethodHead, server.URL+"/stream/theme/"+directToken, nil); head.status != http.StatusOK {
		t.Fatalf("theme HEAD = %d", head.status)
	}

	// A theme token is not authority on any video route.
	for _, route := range []string{"/stream/direct/", "/stream/remux/", "/stream/remux/audio-v2/"} {
		if got := socketProxyRequest(t, server.Client(), http.MethodGet, server.URL+route+directToken, nil); got.status != http.StatusServiceUnavailable {
			t.Fatalf("theme token on %s = %d", route, got.status)
		}
	}
	// A video token is not authority on the theme route.
	video := direct
	video.PlayMethod = string(playback.PlayDirect)
	if got := socketProxyRequest(t, server.Client(), http.MethodGet, server.URL+"/stream/theme/"+sign(video), nil); got.status != http.StatusServiceUnavailable {
		t.Fatalf("video token on theme route = %d", got.status)
	}
	// A theme token whose tuple does not match its method is refused.
	mismatched := direct
	mismatched.RoutingWorkload = string(noderouting.WorkloadRemux)
	mismatched.RoutingExecution = string(noderouting.ExecutionProxy)
	if got := socketProxyRequest(t, server.Client(), http.MethodGet, server.URL+"/stream/theme/"+sign(mismatched), nil); got.status != http.StatusServiceUnavailable {
		t.Fatalf("mismatched theme tuple = %d", got.status)
	}
	// A replaced file is refused until a scan and a new token describe it.
	stale := direct
	stale.ThemeSize++
	if got := socketProxyRequest(t, server.Client(), http.MethodGet, server.URL+"/stream/theme/"+sign(stale), nil); got.status != http.StatusNotFound {
		t.Fatalf("replaced theme = %d", got.status)
	}

	sibling := newSocketProxyServer(t, secret, nil)
	sibling.nodeRowID = func() (int, bool) { return 12, true }
	siblingServer := httptest.NewServer(sibling.Handler())
	t.Cleanup(siblingServer.Close)
	if got := socketProxyRequest(t, siblingServer.Client(), http.MethodGet, siblingServer.URL+"/stream/theme/"+directToken, nil); got.status != http.StatusServiceUnavailable {
		t.Fatalf("theme token on a sibling proxy = %d", got.status)
	}
}

func TestThemeConversionHeadAndRelay(t *testing.T) {
	const secret = "proxy-theme-relay-secret"
	path, size, modified := writeThemeAudio(t)
	var relayed *http.Request
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relayed = r.Clone(r.Context())
		w.Header().Set("Content-Type", playback.AudioOnlyRemuxMIMEV3)
		_, _ = w.Write([]byte("converted"))
	}))
	t.Cleanup(node.Close)
	srv := newSocketProxyServer(t, secret, nil)
	srv.nodeRowID = func() (int, bool) { return 11, true }
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	sign := func(claims streamtoken.Claims) string {
		t.Helper()
		token, err := streamtoken.Sign(claims, secret, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}

	// No FFmpeg is configured here: HEAD must answer without starting one.
	local := themeAACClaims(path, size, modified)
	head := socketProxyRequest(t, server.Client(), http.MethodHead, server.URL+"/stream/theme/"+sign(local), nil)
	if head.status != http.StatusOK {
		t.Fatalf("conversion HEAD = %d %q", head.status, head.body)
	}

	relay := themeAACClaims(path, size, modified)
	relay.RoutingExecution = string(noderouting.ExecutionTranscode)
	relay.RoutingExecutionNodeID = 21
	relay.TranscodeNode = node.URL
	relay.TranscodeTransportID = "theme-transport"
	relayToken := sign(relay)
	got := socketProxyRequest(t, server.Client(), http.MethodGet, server.URL+"/stream/theme/"+relayToken+"?seek=4", nil)
	if got.status != http.StatusOK || got.body != "converted" {
		t.Fatalf("relayed conversion = %d %q", got.status, got.body)
	}
	if relayed == nil || relayed.URL.Path != "/remux/theme-transport" || relayed.URL.Query().Get("seek") != "4" ||
		relayed.Header.Get("X-Silo-Stream-Token") != relayToken || !strings.HasPrefix(relayed.Header.Get("Authorization"), "Bearer ") {
		t.Fatalf("relayed request = %+v", relayed)
	}

	noTransport := relay
	noTransport.TranscodeTransportID = ""
	if got := socketProxyRequest(t, server.Client(), http.MethodGet, server.URL+"/stream/theme/"+sign(noTransport), nil); got.status != http.StatusServiceUnavailable {
		t.Fatalf("relay without a transport = %d", got.status)
	}
}
