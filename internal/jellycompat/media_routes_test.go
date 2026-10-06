package jellycompat

import (
	"flag"
	"os"
	"testing"

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
