package playback

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// The ceiling on a node-advertised budget has to sit above what a real
// configuration asks for. Picked as a round five minutes it was already below a
// nine-device node's legitimate 311 seconds, so the API canceled that node's
// re-probe before its own deadline every time and its inventory never landed.
func TestNormalizeProbeRequestTimeoutAdmitsALargeButRealBudget(t *testing.T) {
	nineDevices := CapabilityRequestTimeout(tonemap.BackendQSV,
		"/dev/dri/renderD128,/dev/dri/renderD129,/dev/dri/renderD130,/dev/dri/renderD131,"+
			"/dev/dri/renderD132,/dev/dri/renderD133,/dev/dri/renderD134,/dev/dri/renderD135,/dev/dri/renderD136")

	got := NormalizeProbeRequestTimeout(nineDevices.Milliseconds(), time.Minute)
	if got != nineDevices {
		t.Fatalf("normalized = %v, want the %v a nine-device node legitimately asks for", got, nineDevices)
	}
}
