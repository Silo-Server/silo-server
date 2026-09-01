package main

import (
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/routeinventory"
)

// TestRouteInventoryMatchesRootListener is the runtime backstop for the root
// listener, the one listener with no chi tree to walk. It builds the real
// http.ServeMux the primary port serves and compares its patterns against the
// artifact in both directions: every pattern the mux registers has a row, and
// every root row is a pattern the mux registers. Every root row is
// unconditional, so one wiring is the whole surface.
func TestRouteInventoryMatchesRootListener(t *testing.T) {
	inventory, err := routeinventory.LoadArtifact(".")
	if err != nil {
		t.Fatal(err)
	}
	if count := inventory.ConditionalCount(routeinventory.ListenerRoot); count != 0 {
		t.Fatalf("the root listener now has %d conditionally registered row(s); "+
			"equality with one wiring no longer holds — switch to Reconcile and say why here", count)
	}

	apiStub := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	mux, ok := newRootHandler(apiStub).(*http.ServeMux)
	if !ok {
		t.Fatal("newRootHandler no longer returns an *http.ServeMux; the route inventory's root listener model is wrong")
	}
	observed, err := routeinventory.ObservedServeMux(mux)
	if err != nil {
		t.Fatal(err)
	}
	if len(observed) == 0 {
		t.Fatal("no patterns observed; the root handler no longer registers anything")
	}
	unledgered, unobserved := inventory.ReconcileExact(routeinventory.ListenerRoot, observed)
	if len(unledgered) > 0 {
		t.Errorf("the root listener registers %d route(s) with no inventory row; run `make route-inventory`:\n  %v",
			len(unledgered), unledgered)
	}
	if len(unobserved) > 0 {
		t.Errorf("the inventory claims %d root route(s) the real listener does not register; "+
			"run `make route-inventory`:\n  %v", len(unobserved), unobserved)
	}
}
