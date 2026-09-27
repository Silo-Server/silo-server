package transcodenode

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/routeinventory"
)

// TestRouteInventoryMatchesRuntimeRouter proves the source-built inventory and
// the real transcode-node listener describe the same route set.
//
// Every row for this listener is unconditional, so one wiring registers its
// whole surface and the comparison runs both ways: a route with no row is an
// unledgered route, and a row with no route is a phantom the generator
// invented.
func TestRouteInventoryMatchesRuntimeRouter(t *testing.T) {
	inventory, err := routeinventory.LoadArtifact(".")
	if err != nil {
		t.Fatal(err)
	}
	if count := inventory.ConditionalCount(routeinventory.ListenerTranscodeNode); count != 0 {
		t.Fatalf("the transcode-node listener now has %d conditionally registered row(s); "+
			"equality with one wiring no longer holds — switch to Reconcile and say why here", count)
	}
	router := NewServer(
		nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{}),
		nodesessions.NewTracker(nil, "", "", ""),
	).router()

	observed, err := routeinventory.Observed(router)
	if err != nil {
		t.Fatal(err)
	}
	if len(observed) == 0 {
		t.Fatal("no routes observed; the fixture no longer builds a real router")
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
