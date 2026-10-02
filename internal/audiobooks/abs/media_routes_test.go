package abs

import (
	"flag"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

var updateRouteManifest = flag.Bool("update-route-manifest", false, "update checked-in route manifest")

func TestMediaRouteManifest(t *testing.T) {
	declareABSMediaRoutes()
	makeRouter := func() chi.Routes {
		router := chi.NewRouter()
		New(Dependencies{MediaStore: noopMediaStore{}}).Mount(router)
		return router
	}
	actual, err := streamtelemetry.BuildRouteManifest([]chi.Routes{makeRouter(), makeRouter()}, absMediaRoutes)
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
	// Every declared ABS route is enrolled and carries a capture hook. A nil
	// Capture would fall back to genericCapture and lose the client identity
	// absCapture reads through absPlaybackClientInfoFromRequest.
	for _, route := range absMediaRoutes {
		if !route.Enrolled {
			t.Fatalf("abs route not enrolled: %s %s", route.Method, route.Pattern)
		}
		if route.Capture == nil {
			t.Fatalf("abs route has no capture hook: %s %s", route.Method, route.Pattern)
		}
	}
}
