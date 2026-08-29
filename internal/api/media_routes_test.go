package api

import (
	"context"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

var updateRouteManifest = flag.Bool("update-route-manifest", false, "update checked-in route manifest")

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
	minimal := NewRouter(Dependencies{Config: cfg})
	maximal := NewRouter(Dependencies{DB: pool, Config: cfg, FileRepo: scanner.NewFileRepository(pool), FolderRepo: catalog.NewFolderRepository(pool), SessionMgr: playback.NewSessionManager(0, 0)})
	actual, err := streamtelemetry.BuildRouteManifest([]chi.Routes{minimal, maximal}, nativeMediaRoutes)
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
	for _, route := range nativeMediaRoutes {
		if !route.Enrolled {
			t.Fatalf("native route not enrolled: %s %s", route.Method, route.Pattern)
		}
	}
}

func TestNativeRejectedAndMissingRequestsRemainProvisional(t *testing.T) {
	for _, route := range nativeMediaRoutes {
		for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
			t.Run(route.Method+" "+route.Pattern+" "+http.StatusText(status), func(t *testing.T) {
				cfg := streamtelemetry.DefaultConfig("test")
				cfg.Enabled = true
				registry := streamtelemetry.NewRegistry(cfg, streamtelemetry.NewLocalStore(), nil)
				handler := registry.Observe(route)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
				handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(route.Method, "/", nil))
				snapshot := registry.Sweep()
				if len(snapshot.Sessions) != 0 || len(snapshot.Transfers) != 0 {
					t.Fatalf("status %d created logical activity: %+v", status, snapshot)
				}
			})
		}
	}
}
