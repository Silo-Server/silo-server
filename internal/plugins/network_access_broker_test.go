package plugins

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

// The host binds the RuntimeHost broker stream once at start and go-plugin
// drops the pending stream after five seconds if nothing dialed it. A
// resident provider typically idles far longer than that before its first
// host call, so the SDK must dial at bind time; otherwise every callback for
// the life of the process fails and Connect cannot read host info or
// persist state. The fixture's GetStatus reports whether a host call works.
func TestNetworkAccessHostCallbackSurvivesIdleAfterBind(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out go-plugin's fixed five-second pending-stream window")
	}
	f := newResidentFixture(t, ResidentOptions{})
	ctx := context.Background()
	f.service.SetNetworkAccessHostInfo(func(context.Context) (pluginhost.HostInfo, error) {
		return pluginhost.HostInfo{Role: pluginhost.HostRoleAPI, Name: "api"}, nil
	})
	f.service.StartResidents(ctx)
	waitState(t, f.service, 5, "running", running)

	// Idle past go-plugin's pending-stream window before the first callback.
	// The window is a constant inside go-plugin (GRPCBroker.timeoutWait)
	// with no seam and no signal when it fires, so the only way to observe
	// its effect is to outlive it. A scheduler stall that delays the cleanup
	// past this sleep would make the test pass without proving anything,
	// but never fail it, so the sleep cannot introduce flakiness.
	time.Sleep(6 * time.Second)

	report, err := f.service.NetworkAccessStatus(ctx, "stub")
	if err != nil {
		t.Fatal(err)
	}
	got := report.Hosts[0].Status
	if got.Error != "" || !strings.HasSuffix(got.ProviderVersion, "host-ok") {
		t.Fatalf("host callback after idle failed: %+v", got)
	}
}
