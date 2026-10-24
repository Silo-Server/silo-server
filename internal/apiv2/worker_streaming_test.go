package apiv2

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/routeinventory"
)

func TestWorkerDeliveryInventoryDescriptionCoverage(t *testing.T) {
	inventory, err := routeinventory.LoadArtifact(".")
	if err != nil {
		t.Fatal(err)
	}
	described := map[string]bool{}
	for _, op := range describeWorkerProtocols().Operations {
		described[op.Listener+" "+op.Method+" "+op.Path] = true
	}
	count := 0
	for _, route := range inventory.Routes {
		if route.Listener != "proxy" && route.Listener != "transcode_node" {
			continue
		}
		switch route.Path {
		case "/health", "/ready", "/metrics", "/api/v1/health":
			continue
		}
		count++
		if !described[route.Listener+" "+route.Method+" "+route.Path] {
			t.Fatal("undescribed worker registration", route.Listener, route.Method, route.Path)
		}
	}
	if count != 51 || len(described) != 51 {
		t.Fatal("retained inventory drift", count, len(described))
	}
}
