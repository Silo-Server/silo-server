package transcodenode

import (
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/routeinventory"
)

// TestRouteInventoryMatchesRuntimeRouter proves the source-built inventory and
// the real transcode-node listener describe the same route set.
//
// The standalone execute endpoint is conditional. Legacy and enrolled fixtures
// must each contain only inventoried rows; their union must cover every row.
func TestRouteInventoryMatchesRuntimeRouter(t *testing.T) {
	inventory, err := routeinventory.LoadArtifact(".")
	if err != nil {
		t.Fatal(err)
	}
	router := NewServer(
		nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{}),
		nodesessions.NewTracker(nil, "", "", ""),
	).router()

	var observed []string
	for _, standalone := range []bool{false, true} {
		if standalone {
			routerServer := NewServer(nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{}), nodesessions.NewTracker(nil, "", "", ""))
			routerServer.executeHandler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
			router = routerServer.router()
		}
		routes, err := routeinventory.Observed(router)
		if err != nil {
			t.Fatal(err)
		}
		if len(routes) == 0 {
			t.Fatal("no routes observed; fixture no longer builds real router")
		}
		if missing := inventory.Reconcile(routeinventory.ListenerTranscodeNode, routes); len(missing) > 0 {
			t.Fatalf("standalone=%v missing=%v", standalone, missing)
		}
		observed = append(observed, routes...)
	}
	unledgered, unobserved := inventory.ReconcileExact(routeinventory.ListenerTranscodeNode, observed)
	if len(unledgered) > 0 {
		t.Errorf("the transcode-node listener registers %d route(s) with no inventory row; run `make route-inventory`:\n  %v",
			len(unledgered), unledgered)
	}
	if len(unobserved) > 0 {
		t.Errorf("the inventory claims %d transcode-node route(s) the real listener does not register; "+
			"run `make route-inventory`:\n  %v", len(unobserved), unobserved)
	}
}
