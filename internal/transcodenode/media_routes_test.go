package transcodenode

import (
	"flag"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

var updateRouteManifest = flag.Bool("update-route-manifest", false, "update checked-in route manifest")

func TestMediaRouteManifest(t *testing.T) {
	declareTranscodeNodeMediaRoutes()
	makeRouter := func() chi.Routes {
		// Handler's idle reaper is guarded by reaperOnce, so repeated fixtures
		// start only the single process-wide goroutine existing tests already use.
		return NewServer(nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{}), nodesessions.NewTracker(nil, "", "", "")).Handler().(chi.Routes)
	}
	actual, err := streamtelemetry.BuildRouteManifest([]chi.Routes{makeRouter(), makeRouter()}, transcodeNodeMediaRoutes)
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
	for _, route := range transcodeNodeMediaRoutes {
		if !route.Enrolled || route.Capture == nil {
			t.Fatalf("transcode-node route not fully enrolled: %s %s", route.Method, route.Pattern)
		}
	}
}
