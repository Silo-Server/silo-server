package jellycompat

import (
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

var updateRouteManifest = flag.Bool("update-route-manifest", false, "update checked-in route manifest")

func TestMediaRouteManifest(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	declareJellycompatMediaRoutes()
	minimal := NewRouter(Dependencies{Config: cfg})
	maximal := NewRouter(Dependencies{Config: cfg})
	actual, err := streamtelemetry.BuildRouteManifest([]chi.Routes{minimal, maximal}, jellycompatMediaRoutes)
	if err != nil {
		t.Fatal(err)
	}
	const path = "testdata/media_routes.txt"
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
	// Every declared compat route is enrolled and carries a capture hook. A route
	// that is declared but not enrolled, or enrolled with a nil Capture, would
	// fall back to genericCapture and quietly lose the Jellyfin client identity
	// compatCapture reads from the MediaBrowser authorization header.
	for _, route := range jellycompatMediaRoutes {
		if !route.Enrolled {
			t.Fatalf("jellycompat route not enrolled: %s %s", route.Method, route.Pattern)
		}
		if route.Capture == nil {
			t.Fatalf("jellycompat route has no capture hook: %s %s", route.Method, route.Pattern)
		}
	}
}

// A pre-v2 API pod can share the durable playback store with a newer pod, but
// it only knows the legacy Jellyfin byte routes. The extra literal segment must
// make every v2 request miss that old route table rather than bind audio-v2 as
// an id, playlist, or segment parameter and execute legacy FFmpeg arguments.
func TestAudioV2PlaybackPathsAreNotCapturedByLegacyRouter(t *testing.T) {
	sharedStore := NewPlaybackSessionStore(time.Hour, nil)
	version := testCompatVersion()
	version.AudioTracks[1].Channels = 6
	sharedStore.Put(PlaybackSession{
		ID:           "play-v2",
		RouteItemID:  "item-1",
		MediaSources: []PlaybackMediaSource{testCompatSource(NewResourceIDCodec(), version)},
	})

	legacyReached := false
	legacyHandler := func(w http.ResponseWriter, _ *http.Request) {
		if _, ok := sharedStore.Get("play-v2"); ok {
			legacyReached = true
		}
		w.WriteHeader(http.StatusOK)
	}
	legacy := chi.NewRouter()
	legacy.Method(http.MethodHead, "/Videos/{id}/stream", http.HandlerFunc(legacyHandler))
	legacy.Get("/Videos/{id}/stream", legacyHandler)
	legacy.Method(http.MethodHead, "/Videos/{id}/stream.{container}", http.HandlerFunc(legacyHandler))
	legacy.Get("/Videos/{id}/stream.{container}", legacyHandler)
	legacy.Get("/Videos/{id}/master.m3u8", legacyHandler)
	legacy.Get("/Videos/{id}/hls/{playlistId}/stream.m3u8", legacyHandler)
	legacy.Get("/Videos/{id}/hls/{playlistId}/{segmentId}.{segmentContainer}", legacyHandler)

	requests := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/Videos/item-1/audio-v2/stream"},
		{http.MethodHead, "/Videos/item-1/audio-v2/stream"},
		{http.MethodGet, "/Videos/item-1/audio-v2/stream.mkv"},
		{http.MethodHead, "/Videos/item-1/audio-v2/stream.mkv"},
		{http.MethodGet, "/Videos/item-1/audio-v2/master.m3u8"},
		{http.MethodGet, "/Videos/item-1/audio-v2/hls/play-v2/stream.m3u8"},
		{http.MethodGet, "/Videos/item-1/audio-v2/hls/play-v2/seg_00001.ts"},
	}
	for _, request := range requests {
		recorder := httptest.NewRecorder()
		legacy.ServeHTTP(recorder, httptest.NewRequest(request.method, request.path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want 404", request.method, request.path, recorder.Code)
		}
	}
	if legacyReached {
		t.Fatal("versioned request reached a legacy handler despite the literal route segment")
	}
}
