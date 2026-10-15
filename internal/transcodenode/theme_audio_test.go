package transcodenode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
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
	req := httptest.NewRequest(http.MethodGet, "/remux/"+transport, nil)
	req.Header.Set("X-Silo-Stream-Token", token)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("session_id", transport)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
}

// A theme conversion runs on the source its resolved grant opens, and only
// while the core grants it; the core checks the theme against its catalog
// when the node opens it (see internal/nodecontrol).
func TestHandleRemuxConvertsThemeFromItsGrant(t *testing.T) {
	server := newTestServer(t)
	server.nodeRowID = func() (int, bool) { return 21, true }
	sources := &grantPathSources{}
	server.sources = sources
	path, size, modified := writeNodeTheme(t)
	ffmpegPath := filepath.Join(t.TempDir(), "ffmpeg.sh")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nprintf theme-aac-bytes\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server.watcher.Config().Playback.FFmpegPath = ffmpegPath
	claims := streamtoken.Claims{
		SessionID: "theme-transport", MediaPath: path, PlayMethod: streamtoken.PlayMethodThemeAAC, AudioOnly: true,
		TranscodeAudio: true, TargetCodecAudio: "aac", TargetAudioChannels: 2, TargetAudioBitrateKbps: 192,
		TranscodeNode: "http://node", TranscodeTransportID: "theme-transport", ThemeID: 9, ThemeSize: size, ThemeModifiedUnixNano: modified.UnixNano(),
		RoutingWorkload: string(noderouting.WorkloadRemux), RoutingExecution: string(noderouting.ExecutionTranscode),
		RoutingExecutionNodeID: 21, RoutingEgress: string(noderouting.EgressProxy), RoutingEgressNodeID: 11,
	}
	card := playback.RecipeCardFromClaims(&claims)
	server.useCoreRecipes(&stubRecipeStore{card: &card, ok: true})
	sign := func(claims streamtoken.Claims) string {
		t.Helper()
		token, err := streamtoken.Sign(claims, testSecret, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	token := sign(claims)

	recorder := httptest.NewRecorder()
	server.handleRemux(recorder, themeRemuxRequest(t, token, "theme-transport"))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "theme-aac-bytes" {
		t.Fatalf("granted theme = %d %q", recorder.Code, recorder.Body.String())
	}
	opened := sources.openInputs()
	if len(opened) != 1 || opened[0].Input() != path || !opened[0].closed() {
		t.Fatalf("opened inputs = %+v, want the theme's, released after the request", opened)
	}

	server.sources = refuseSources{}
	recorder = httptest.NewRecorder()
	server.handleRemux(recorder, themeRemuxRequest(t, token, "theme-transport"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("theme the core refuses = %d %q", recorder.Code, recorder.Body.String())
	}
	server.sources = sources

	notAAC := claims
	notAAC.TargetCodecAudio = "opus"
	recorder = httptest.NewRecorder()
	server.handleRemux(recorder, themeRemuxRequest(t, sign(notAAC), "theme-transport"))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("non-AAC theme recipe = %d %q", recorder.Code, recorder.Body.String())
	}
}

// refuseSources is a core that refuses every source, as it does a theme
// no longer in the catalog.
type refuseSources struct{}

func (refuseSources) OpenSource(context.Context, string) (SourceInput, error) {
	return nil, &nodesource.StatusError{Op: "open", Status: http.StatusGone}
}

func TestHWCapabilitiesAdvertisesThemeExecutionWithSourceAccess(t *testing.T) {
	server := newTestServer(t)
	server.nodeRowID = func() (int, bool) { return 42, true }
	features := func() []string {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/hw-capabilities", nil)
		nodeauthtest.Sign(t, request, testSecret)
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
	if !slices.Contains(features(), playback.TransportFeatureThemeAudioExecutionV1) {
		t.Fatal("theme execution not advertised with source access")
	}
	server.sources = nil
	if slices.Contains(features(), playback.TransportFeatureThemeAudioExecutionV1) {
		t.Fatal("theme execution advertised without source access")
	}
}
