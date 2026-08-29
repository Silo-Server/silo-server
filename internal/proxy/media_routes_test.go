package proxy

import (
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

var updateRouteManifest = flag.Bool("update-route-manifest", false, "update checked-in route manifest")

func TestMediaRouteManifest(t *testing.T) {
	declareProxyMediaRoutes()
	makeRouter := func() chi.Routes {
		return NewServer(nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{}), nodesessions.NewTracker(nil, "", "", "")).Handler().(chi.Routes)
	}
	assertMediaManifest(t, []chi.Routes{makeRouter(), makeRouter()}, proxyMediaRoutes, "testdata/media_routes.txt")
}

// A pre-v2 proxy only has /stream/remux/{token}. The extra literal segment is
// intentional: chi must return 404 instead of treating "audio-v2" as the token
// and ever reaching the legacy FFmpeg path.
func TestAudioV2RemuxPathIsNotCapturedByLegacyRoute(t *testing.T) {
	router := chi.NewRouter()
	router.Get("/stream/remux/{token}", func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("versioned request reached the legacy remux handler")
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/stream/remux/audio-v2/signed-token", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("legacy router status = %d, want 404", recorder.Code)
	}
}

func TestAudioV2RemuxClaimsRequireExactStereoEncodeShape(t *testing.T) {
	valid := streamtoken.Claims{
		PlayMethod:          streamtoken.PlayMethodAudioDownmixRemux,
		TranscodeAudio:      true,
		TargetCodecAudio:    "aac",
		SourceAudioChannels: 6,
		TargetAudioChannels: 2,
	}
	tests := []struct {
		name   string
		mutate func(*streamtoken.Claims)
		want   bool
	}{
		{name: "complete recipe", want: true},
		{name: "ordinary method", mutate: func(c *streamtoken.Claims) { c.PlayMethod = "remux" }},
		{name: "audio copy", mutate: func(c *streamtoken.Claims) { c.TranscodeAudio = false }},
		{name: "default AAC codec", mutate: func(c *streamtoken.Claims) { c.TargetCodecAudio = "" }, want: true},
		{name: "non AAC codec", mutate: func(c *streamtoken.Claims) { c.TargetCodecAudio = "eac3" }},
		{name: "stereo source", mutate: func(c *streamtoken.Claims) { c.SourceAudioChannels = 2 }},
		{name: "missing target", mutate: func(c *streamtoken.Claims) { c.TargetAudioChannels = 0 }},
		{name: "surround target", mutate: func(c *streamtoken.Claims) { c.TargetAudioChannels = 6 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claims := valid
			if test.mutate != nil {
				test.mutate(&claims)
			}
			if got := validAudioV2RemuxClaims(&claims); got != test.want {
				t.Fatalf("validAudioV2RemuxClaims() = %t, want %t for %#v", got, test.want, claims)
			}
		})
	}
}

func assertMediaManifest(t *testing.T, fixtures []chi.Routes, declared []streamtelemetry.MediaRoute, path string) {
	t.Helper()
	actual, err := streamtelemetry.BuildRouteManifest(fixtures, declared)
	if err != nil {
		t.Fatal(err)
	}
	if *updateRouteManifest {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(actual), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != actual {
		t.Fatalf("route manifest changed; inspect it and run go test . -update-route-manifest")
	}
	for _, route := range declared {
		if !route.Enrolled || route.Capture == nil {
			t.Fatalf("proxy route not fully enrolled: %s %s", route.Method, route.Pattern)
		}
	}
}
