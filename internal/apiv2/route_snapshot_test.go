package apiv2

import (
	"net/http"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

func TestRouteSnapshotCannotChangeSealedDirectRoutes(t *testing.T) {
	var snapshot []streamtelemetry.WalkedRoute
	h := NewHandler(Dependencies{ObserveRoutes: func(routes []streamtelemetry.WalkedRoute) { snapshot = routes }})
	for _, path := range []string{directDownloadPath, directDownloadProxyPath} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			index := slices.IndexFunc(snapshot, func(route streamtelemetry.WalkedRoute) bool {
				return route.Pattern == Prefix+path && route.Method == method
			})
			if index < 0 {
				t.Fatalf("actual constructor did not register %s %s", method, path)
			}
			snapshot[index].Pattern = "/tampered"
			res := do(t, h, method, Prefix+path+"?file_id=42", "", nil)
			// Missing auth dependency proves the registered guarded operation was reached;
			// an unmatched path answers404. Mutating the snapshot cannot affect dispatch.
			if res.Code != 503 {
				t.Fatal(method, path, res.Code, res.Body)
			}
		}
	}
}
