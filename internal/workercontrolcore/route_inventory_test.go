package workercontrolcore

import (
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/routeinventory"
)

func TestRouteInventoryMatchesSealedCoreRouter(t *testing.T) {
	inventory, err := routeinventory.LoadArtifact(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, withSource := range []bool{false, true} {
		service := &Service{}
		if withSource {
			service.options.SourceHandler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
		}
		observed, err := routeinventory.Observed(service.router())
		if err != nil {
			t.Fatal(err)
		}
		want := 7
		if withSource {
			want = 9
		}
		if len(observed) != want {
			t.Fatalf("source=%v observed=%d want=%d", withSource, len(observed), want)
		}
		missing := inventory.Reconcile(routeinventory.ListenerWorkerControl, observed)
		var phantom []string
		if len(missing) != 0 || len(phantom) != 0 {
			t.Fatalf("source=%v missing=%v phantom=%v", withSource, missing, phantom)
		}
		if withSource {
			missing, phantom = inventory.ReconcileExact(routeinventory.ListenerWorkerControl, observed)
			if len(missing) != 0 || len(phantom) != 0 {
				t.Fatalf("full missing=%v phantom=%v", missing, phantom)
			}
		}
	}
}
