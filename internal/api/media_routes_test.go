package api

import (
	"context"
	"flag"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

var updateRouteManifest = flag.Bool("update-route-manifest", false, "update checked-in route manifest")

var routeManifestPath = flag.String("route-manifest-path", "testdata/media_routes.txt", "route manifest fixture or isolated generation output")

func TestMediaRouteManifest(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(context.Background(), "postgres://nobody:nobody@127.0.0.1:1/none?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	declareNativeMediaRoutes()
	// NewRouter seals the router so no production caller can register on it;
	// a test walks the tree through the unexported constructor instead.
	snapshots := []map[string][]streamtelemetry.WalkedRoute{{}, {}}
	observeV2 := func(index int) func([]streamtelemetry.WalkedRoute) {
		return func(routes []streamtelemetry.WalkedRoute) { snapshots[index]["/api/v2/*"] = routes }
	}
	minimal := newChiRouter(Dependencies{Config: cfg, v2RouteSnapshot: observeV2(0)})
	// A live registry makes observeNative resolve every wrapped pattern, so a
	// handler wired to an undeclared media route panics here, not at startup.
	telemetryCfg := streamtelemetry.DefaultConfig("test")
	telemetryCfg.Enabled = true
	telemetry := streamtelemetry.NewRegistry(telemetryCfg, streamtelemetry.NewLocalStore(), nil)
	maximal := newChiRouter(Dependencies{DB: pool, Config: cfg, FileRepo: scanner.NewFileRepository(pool), FolderRepo: catalog.NewFolderRepository(pool), SessionMgr: playback.NewSessionManager(0, 0), StreamTelemetry: telemetry, v2RouteSnapshot: observeV2(1)})
	actual, err := streamtelemetry.BuildRouteManifest([]chi.Routes{minimal, maximal}, nativeMediaRoutes, snapshots...)
	if err != nil {
		t.Fatal(err)
	}
	path := *routeManifestPath
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
	for _, route := range nativeMediaRoutes {
		if !route.Enrolled {
			t.Fatalf("native route not enrolled: %s %s", route.Method, route.Pattern)
		}
	}
}

func TestNewRouterRegistersTranscodeShutdownWork(t *testing.T) {
	registered := make(chan (<-chan struct{}), 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	NewRouter(Dependencies{
		Config:     &config.Config{},
		AppContext: ctx,
		SessionMgr: playback.NewSessionManager(0, 0),
		RegisterShutdownWork: func(done <-chan struct{}) {
			registered <- done
		},
	})

	select {
	case done := <-registered:
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("registered transcode cleanup did not finish after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("router did not register transcode shutdown work")
	}
}

// A media route the session quota counted would refuse playback or a download
// mid-transfer once a session spent its hour, so every declared route must be
// one ratelimit.IsQuotaExemptPath exempts; the API calls beside them must not be.
func TestSessionQuotaExemptsMediaRoutes(t *testing.T) {
	param := regexp.MustCompile(`\{[^}]+\}`)
	for _, routes := range [][]streamtelemetry.MediaRoute{nativeMediaRoutes, playbackV2MediaRoutes} {
		for _, route := range routes {
			path := param.ReplaceAllString(route.Pattern, "x")
			if !ratelimit.IsQuotaExemptPath(path) {
				t.Errorf("%s %s is a media route the session quota would count", route.Method, route.Pattern)
			}
		}
	}
	for _, path := range []string{"/api/v2/playback", "/api/v2/playback/x/progress", "/api/v1/playback/x/progress"} {
		if !ratelimit.IsQuotaExemptPath(path) {
			t.Errorf("%s is a playback call the session quota must not count", path)
		}
	}
	for _, path := range []string{
		"/api/v2/catalog/items/x", "/api/v2/downloads/x", "/api/v1/ebooks/x/files/y",
		"/api/v2/direct-download/links", "/api/v2/playbackx", "/api/v2/streams",
	} {
		if ratelimit.IsQuotaExemptPath(path) {
			t.Errorf("%s is an API route the session quota must count", path)
		}
	}
}
