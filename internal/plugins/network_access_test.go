package plugins

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

// TestNetworkAccessProvidersAndCommands drives the admin service layer
// against the real fixture plugin: providers are listed from the manifest
// without a launch, an installation whose process is not running answers
// unavailable, and once the supervisor has it running the status, connect
// and disconnect RPCs reach the plugin and refresh the status sink.
func TestNetworkAccessProvidersAndCommands(t *testing.T) {
	f := newResidentFixture(t, ResidentOptions{})
	ctx := context.Background()
	broker := netaccess.NewBroker()
	f.service.SetNetworkAccessStatusSink(broker)
	f.service.SetNetworkAccessHostInfo(func(context.Context) (pluginhost.HostInfo, error) {
		return pluginhost.HostInfo{Role: pluginhost.HostRoleAPI, Name: "Living Room"}, nil
	})

	providers, err := f.service.ListNetworkAccessProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 || providers[0].InstallationID != 5 || providers[0].Provider != "stub" || providers[0].CapabilityID != "stub" || providers[0].DisplayName != "Resident Fixture" {
		t.Fatalf("providers = %+v", providers)
	}
	if _, err := f.host.Client(5); !errors.Is(err, pluginhost.ErrClientNotFound) {
		t.Fatalf("listing providers launched the plugin: %v", err)
	}

	if _, err := f.service.NetworkAccessStatus(ctx, "netbird"); !errors.Is(err, ErrNetworkAccessProviderNotFound) {
		t.Fatalf("unknown provider err = %v", err)
	}
	if _, err := f.service.ConnectNetworkAccess(ctx, "stub", []string{"node:3"}); !errors.Is(err, ErrNetworkAccessHostUnknown) {
		t.Fatalf("unknown host err = %v", err)
	}

	// Before the supervisor arms, the api host reports unavailable and the
	// read does not start the process.
	report, err := f.service.NetworkAccessStatus(ctx, "stub")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Hosts) != 1 || report.Hosts[0].Host.ID != HostScopeAPI || report.Hosts[0].Host.Role != "api" || report.Hosts[0].Host.Name != "Living Room" {
		t.Fatalf("hosts = %+v", report.Hosts)
	}
	if got := report.Hosts[0].Status; got.State != netaccess.StateUnavailable || got.Error == "" || got.InstallationID != 5 || got.Provider != "stub" {
		t.Fatalf("status before start = %+v", got)
	}
	if _, err := f.host.Client(5); !errors.Is(err, pluginhost.ErrClientNotFound) {
		t.Fatalf("status read launched the plugin: %v", err)
	}

	f.service.StartResidents(ctx)
	waitState(t, f.service, 5, "running", running)

	report, err = f.service.NetworkAccessStatus(ctx, "stub")
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Hosts[0].Status; got.State != netaccess.StateDisconnected || !strings.HasPrefix(got.ProviderVersion, "stub 0.1.0") || got.UpdatedAt.IsZero() {
		t.Fatalf("status when running = %+v", got)
	}

	report, err = f.service.ConnectNetworkAccess(ctx, "stub", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Hosts[0].Status; got.State != netaccess.StateConnected || got.Origin != "https://silo.stub.test" || len(got.Listeners) != 1 || !got.DesiredConnected {
		t.Fatalf("status after connect = %+v", got)
	}
	if cached, ok := broker.Status.Get(5); !ok || !cached.Connected() {
		t.Fatalf("connect did not refresh the status sink: %+v %v", cached, ok)
	}
	if origins := broker.Status.ConnectedOrigins(); len(origins) != 1 || origins[0] != "https://silo.stub.test" {
		t.Fatalf("connected origins = %v", origins)
	}

	// Naming the api host explicitly applies the command to it too.
	report, err = f.service.DisconnectNetworkAccess(ctx, "stub", []string{HostScopeAPI})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Hosts[0].Status; got.State != netaccess.StateDisconnected || got.DesiredConnected {
		t.Fatalf("status after disconnect = %+v", got)
	}
	if origins := broker.Status.ConnectedOrigins(); len(origins) != 0 {
		t.Fatalf("connected origins after disconnect = %v", origins)
	}
}

// A provider process that is up but does not answer must not keep
// advertising its last connected origin: the failed read replaces the cached
// status with unavailable so the origin check and the node health report
// stop naming it.
func TestNetworkAccessFailedRPCReportsUnavailableToTheStatusSink(t *testing.T) {
	f := newResidentFixture(t, ResidentOptions{})
	ctx := context.Background()
	broker := netaccess.NewBroker()
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
