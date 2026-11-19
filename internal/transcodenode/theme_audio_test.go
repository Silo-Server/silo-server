package transcodenode

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

func writeNodeTheme(t *testing.T) (string, int64, time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "theme.ogg")
	if err := os.WriteFile(path, []byte("theme source"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, info.Size(), info.ModTime().Truncate(time.Microsecond)
}

func themeRemuxRequest(t *testing.T, token, transport string) *http.Request {
	t.Helper()
	req := withNodeRouteParams(httptest.NewRequest(http.MethodGet, "/remux/"+transport, nil), map[string]string{"session_id": transport})
	req.Header.Set("X-Silo-Stream-Token", token)
	return req
}

// A theme conversion runs FFmpeg on the theme file the core grants, never on
// a path the token names, and a grant the core refuses converts nothing.
func TestHandleRemuxConvertsTheGrantedTheme(t *testing.T) {
	server := newTestServer(t)
	path, size, modified := writeNodeTheme(t)
	ffmpegLog := filepath.Join(t.TempDir(), "ffmpeg.log")
	useFakeFFmpeg(t, server, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \""+ffmpegLog+"\"\nprintf theme-aac-bytes\n")
	claims := streamtoken.Claims{
		SessionID: "theme-transport", MediaPath: "/token/names/another.ogg", PlayMethod: streamtoken.PlayMethodThemeAAC, AudioOnly: true,
		TranscodeAudio: true, TargetCodecAudio: "aac", TargetAudioChannels: 2, TargetAudioBitrateKbps: 192,
		TranscodeNode: "http://node", TranscodeTransportID: "theme-transport", ThemeID: 9, ThemeSize: size, ThemeModifiedUnixNano: modified.UnixNano(),
		RoutingWorkload: string(noderouting.WorkloadRemux), RoutingExecution: string(noderouting.ExecutionTranscode),
		RoutingExecutionNodeID: 21, RoutingEgress: string(noderouting.EgressProxy), RoutingEgressNodeID: 11,
	}
	resolution := resolutionOf(playback.RecipeCardFromClaims(&claims))
	resolution.SourceGrant = path
	resolution.Theme = &nodecontrol.ResolvedTheme{ID: 9, Size: size, ModifiedUnixNano: modified.UnixNano()}
	coreOf(server).resolve("theme-transport", resolution)

	recorder := httptest.NewRecorder()
	server.handleRemux(recorder, themeRemuxRequest(t, "relay-token", "theme-transport"))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "theme-aac-bytes" {
		t.Fatalf("granted theme = %d %q", recorder.Code, recorder.Body.String())
	}
	invocations, err := os.ReadFile(ffmpegLog)
	if err != nil || !strings.Contains(string(invocations), "-i "+path+" ") || strings.Contains(string(invocations), claims.MediaPath) {
		t.Fatalf("ffmpeg ran with %q (%v), want the granted theme as its input", invocations, err)
	}

	sourcesOf(server).refuse(path, &nodesource.StatusError{Op: "open", Status: http.StatusGone})
	recorder = httptest.NewRecorder()
	server.handleRemux(recorder, themeRemuxRequest(t, "relay-token", "theme-transport"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("refused theme grant = %d %q", recorder.Code, recorder.Body.String())
	}
}

// Progressive remuxes and theme conversions resolve through the core and open
// the source it grants: a node advertises them only with both wired.
func TestHWCapabilitiesAdvertisesThemeExecutionOnlyWithAuthority(t *testing.T) {
	server := newProbingTestServer(t)
	server.nodeRowID = func() (int, bool) { return 42, true }
	features := func() []string {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/hw-capabilities", nil)
		nodeauthtest.Sign(t, request, testNodeKey)
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		if recorder.Code == http.StatusServiceUnavailable {
			t.Skip("this host's ffmpeg cannot answer a capability probe")
		}
		var info playback.HWAccelInfo
		if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &info) != nil {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		return info.TransportFeatures
	}
	server.core = nil
	if slices.Contains(features(), playback.TransportFeatureThemeAudioExecutionV1) {
		t.Fatal("theme execution advertised without the core's authority")
	}
	coreOf(server)
	if got := features(); !slices.Contains(got, playback.TransportFeatureThemeAudioExecutionV1) ||
		!slices.Contains(got, playback.TransportFeatureProgressiveRemuxExecutionV1) {
		t.Fatalf("features with the core's authority = %v, want theme and progressive remux execution", got)
	}
}
