package streamtelemetry

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestDelegatedManifestRequiresMountedObservedRoute(t *testing.T) {
	for _, kind := range []string{"valid", "missing_mount", "wrong_method", "outside_mount", "missing_child", "wrong_fixture_count"} {
		t.Run(kind, func(t *testing.T) {
			minimal, maximal := chi.NewRouter(), chi.NewRouter()
			if kind != "missing_mount" {
				method := http.MethodGet
				if kind == "wrong_method" {
					method = http.MethodPost
				}
				minimal.Method(method, "/api/v2/*", http.NotFoundHandler())
				maximal.Method(method, "/api/v2/*", http.NotFoundHandler())
			}
			children := []WalkedRoute{{Method: http.MethodGet, Pattern: "/api/v2/file"}}
			if kind == "outside_mount" {
				children[0].Pattern = "/other/file"
			}
			if kind == "missing_child" {
				children = nil
			}
			snapshots := []map[string][]WalkedRoute{{"/api/v2/*": children}, {"/api/v2/*": children}}
			if kind == "wrong_fixture_count" {
				snapshots = snapshots[:1]
			}
			manifest, err := BuildRouteManifest([]chi.Routes{minimal, maximal}, []MediaRoute{{Method: http.MethodGet, Pattern: "/api/v2/file"}}, snapshots...)
			if kind == "valid" {
				if err != nil || strings.Count(manifest, "GET /api/v2/file\tmedia") != 2 {
					t.Fatal(manifest, err)
				}
			} else if err == nil {
				t.Fatal("unproven media route accepted", manifest)
			}
		})
	}
}
