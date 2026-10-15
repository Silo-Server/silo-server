package apiv2

import (
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/routeinventory"
)

// Every node control route on the API listener is described, with a JSON
// body schema where it takes one. TestWorkerRegistryMatchesOwningInventory
// checks each description's handler and auth class against the inventory.
func TestNodeControlProtocolCoverage(t *testing.T) {
	inventory, err := routeinventory.LoadArtifact(".")
	if err != nil {
		t.Fatal(err)
	}
	described := map[string]bool{}
	for _, op := range describeWorkerProtocols().Operations {
		if op.Listener != "api" {
			continue
		}
		described[op.Method+" "+op.Path] = true
		if op.Method == "POST" && op.Path != "/api/internal/nodes/self/health-changed" {
			if op.RequestBody == nil || op.RequestBody.Content["application/json"] == nil {
				t.Fatal("node control command without a JSON body schema", op.Path)
			}
		}
		if op.Responses["401"] == nil {
			t.Fatal("node control route without its authentication failure", op.Method, op.Path)
		}
	}
	count := 0
	for _, route := range inventory.Routes {
		if route.Listener != "api" || !strings.HasPrefix(route.Path, "/api/internal/nodes/") {
			continue
		}
		count++
		if !described[route.Method+" "+route.Path] {
			t.Fatal("undescribed node control route", route.Method, route.Path)
		}
	}
	if count == 0 || count != len(described) {
		t.Fatal("node control description drift", count, len(described))
	}
}
