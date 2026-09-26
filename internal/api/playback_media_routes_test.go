package api

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/apiv2"
)

// The v2 router is sealed behind the listener delegation, so its finite media
// declarations reconcile through the owning registry rather than chi's outer
// wildcard. The registry's own runtime/spec reconciliation covers its router.
func TestPlaybackV2MediaRouteDeclarations(t *testing.T) {
	operations := apiv2.DeclaredOperations()
	for _, route := range playbackV2MediaRoutes {
		found := false
		for _, operation := range operations {
			if operation.Method == route.Method && operation.Path == route.Pattern {
				found = true
				break
			}
		}
		if !found || !route.Enrolled || !route.CapRelevant {
			t.Fatalf("v2 media route not registered/enrolled: %+v", route)
		}
	}
}
