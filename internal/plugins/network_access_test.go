package plugins

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

// A provider process that is up but does not answer must not keep
// advertising its last connected origin: the failed read replaces the cached
// status with unavailable so the origin check and the node health report
// stop naming it.
func TestNetworkAccessFailedRPCReportsUnavailableToTheStatusSink(t *testing.T) {
	f := newResidentFixture(t, ResidentOptions{})
	ctx := context.Background()
	broker := f.broker
	f.service.SetNetworkAccessStatusSink(broker)
	f.service.SetNetworkAccessHostInfo(func(context.Context) (pluginhost.HostInfo, error) {
		return pluginhost.HostInfo{Role: pluginhost.HostRoleAPI, Name: "Living Room"}, nil
	})
	f.service.StartResidents(ctx)
	waitState(t, f.service, 5, "running", running)
	if _, err := f.service.ConnectNetworkAccess(ctx, "stub", nil); err != nil {
		t.Fatal(err)
	}
	if origins := broker.Status.ConnectedOrigins(); len(origins) != 1 {
		t.Fatalf("connected origins = %v", origins)
	}

	// An already-expired context makes the RPC fail while the process and
	// its cached connected status are both still in place.
	expired, cancel := context.WithCancel(ctx)
	cancel()
	report, err := f.service.NetworkAccessStatus(expired, "stub")
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Hosts[0].Status; got.State != netaccess.StateUnavailable || got.Error == "" {
		t.Fatalf("status on failed RPC = %+v", got)
	}
	if origins := broker.Status.ConnectedOrigins(); len(origins) != 0 {
		t.Fatalf("dead origin still advertised after a failed RPC: %v", origins)
	}
	if cached, ok := broker.Status.Get(5); !ok || cached.State != netaccess.StateUnavailable {
		t.Fatalf("cache after failed RPC = %+v %v", cached, ok)
	}
}
