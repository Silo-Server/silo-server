package plugins

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

// fakeNetworkAccessNodes stands in for the API's node handler: two proxies,
// one answering, one whose plugin is not installed yet, plus an unreachable
// one when set.
type fakeNetworkAccessNodes struct {
	mu          sync.Mutex
	listErr     error
	unreachable bool
	connected   map[int]bool
	calls       []string
}

func (f *fakeNetworkAccessNodes) ListNetworkAccessNodes(context.Context) ([]NetworkAccessNode, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return []NetworkAccessNode{{ID: 7, Name: "proxy-b", URL: "http://proxy-b"}, {ID: 3, Name: "proxy-a", URL: "http://proxy-a"}, {ID: 9, Name: "proxy-new", URL: "http://proxy-new"}}, nil
}

func (f *fakeNetworkAccessNodes) answer(node NetworkAccessNode, provider, verb string) (netaccess.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, verb+":"+node.Host().ID)
	if f.unreachable {
		return netaccess.Status{}, errors.New("proxy node unreachable: dial tcp: connection refused")
	}
	if node.ID == 9 {
		return netaccess.Status{}, ErrNetworkAccessProviderNotFound
	}
	if f.connected == nil {
		f.connected = map[int]bool{}
	}
	switch verb {
	case "connect":
		f.connected[node.ID] = true
	case "disconnect":
		f.connected[node.ID] = false
	}
	status := netaccess.Status{InstallationID: 5, Provider: provider, State: netaccess.StateDisconnected, UpdatedAt: time.Now()}
	if f.connected[node.ID] {
		status.State = netaccess.StateConnected
		status.Origin = "https://" + node.Name + ".stub.test"
	}
	return status, nil
}

func (f *fakeNetworkAccessNodes) NodeNetworkAccessStatus(_ context.Context, node NetworkAccessNode, provider string) (netaccess.Status, error) {
	return f.answer(node, provider, "status")
}

func (f *fakeNetworkAccessNodes) NodeNetworkAccessConnect(_ context.Context, node NetworkAccessNode, provider string) (netaccess.Status, error) {
	return f.answer(node, provider, "connect")
}

func (f *fakeNetworkAccessNodes) NodeNetworkAccessDisconnect(_ context.Context, node NetworkAccessNode, provider string) (netaccess.Status, error) {
	return f.answer(node, provider, "disconnect")
}

func hostByID(t *testing.T, report NetworkAccessReport, id string) NetworkAccessHostStatus {
	t.Helper()
	for _, host := range report.Hosts {
		if host.Host.ID == id {
			return host
		}
	}
	t.Fatalf("host %s missing from %+v", id, report.Hosts)
	return NetworkAccessHostStatus{}
}

// With proxy nodes wired, every admin operation reports the api host first
// and then each enabled proxy in id order, applies the command only to the
// named hosts, and turns a node's failure into that host's unavailable row
// without hiding the others.
func TestNetworkAccessFansOutToProxyNodes(t *testing.T) {
	f := newResidentFixture(t, ResidentOptions{})
	ctx := context.Background()
	f.service.SetNetworkAccessStatusSink(f.broker)
	f.service.SetNetworkAccessHostInfo(func(context.Context) (pluginhost.HostInfo, error) {
		return pluginhost.HostInfo{Role: pluginhost.HostRoleAPI, Name: "Living Room"}, nil
	})
	nodes := &fakeNetworkAccessNodes{}
	f.service.SetNetworkAccessNodes(nodes)
	f.service.StartResidents(ctx)
	waitState(t, f.service, 5, "running", running)

	report, err := f.service.NetworkAccessStatus(ctx, "stub")
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(report.Hosts))
	for _, host := range report.Hosts {
		ids = append(ids, host.Host.ID)
	}
	if len(ids) != 4 || ids[0] != HostScopeAPI || ids[1] != "node:3" || ids[2] != "node:7" || ids[3] != "node:9" {
		t.Fatalf("host order = %v", ids)
	}
	if host := hostByID(t, report, "node:3"); host.Host.Role != "proxy" || host.Host.Name != "proxy-a" || host.Status.State != netaccess.StateDisconnected {
		t.Fatalf("node:3 = %+v", host)
	}
	if host := hostByID(t, report, "node:9"); host.Status.State != netaccess.StateUnavailable || host.Status.Error != "provider is not installed on this node yet" || host.Status.InstallationID != 5 {
		t.Fatalf("node:9 = %+v", host)
	}

	// Connect one proxy only: the api host and the other proxies are read,
	// not commanded.
	report, err = f.service.ConnectNetworkAccess(ctx, "stub", []string{"node:7"})
	if err != nil {
		t.Fatal(err)
	}
	if host := hostByID(t, report, "node:7"); host.Status.State != netaccess.StateConnected || host.Status.Origin != "https://proxy-b.stub.test" {
		t.Fatalf("node:7 after connect = %+v", host)
	}
	if host := hostByID(t, report, "node:3"); host.Status.State != netaccess.StateDisconnected {
		t.Fatalf("node:3 after targeted connect = %+v", host)
	}
	if host := hostByID(t, report, HostScopeAPI); host.Status.State != netaccess.StateDisconnected {
		t.Fatalf("api after targeted connect = %+v", host)
	}
	nodes.mu.Lock()
	var connects []string
	for _, call := range nodes.calls {
		if call == "connect:node:7" || call == "connect:node:3" || call == "connect:node:9" {
			connects = append(connects, call)
		}
	}
	nodes.mu.Unlock()
	if len(connects) != 1 || connects[0] != "connect:node:7" {
		t.Fatalf("connect calls = %v", connects)
	}

	// Connect everywhere, then disconnect everywhere.
	report, err = f.service.ConnectNetworkAccess(ctx, "stub", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{HostScopeAPI, "node:3", "node:7"} {
		if host := hostByID(t, report, id); host.Status.State != netaccess.StateConnected {
			t.Fatalf("%s after connect all = %+v", id, host)
		}
	}
	report, err = f.service.DisconnectNetworkAccess(ctx, "stub", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{HostScopeAPI, "node:3", "node:7"} {
		if host := hostByID(t, report, id); host.Status.State != netaccess.StateDisconnected {
			t.Fatalf("%s after disconnect all = %+v", id, host)
		}
	}

	if _, err := f.service.ConnectNetworkAccess(ctx, "stub", []string{"node:42"}); !errors.Is(err, ErrNetworkAccessHostUnknown) {
		t.Fatalf("unknown node err = %v", err)
	}

	// An unreachable proxy is that host's problem alone.
	nodes.unreachable = true
	report, err = f.service.NetworkAccessStatus(ctx, "stub")
	if err != nil {
		t.Fatal(err)
	}
	if host := hostByID(t, report, "node:3"); host.Status.State != netaccess.StateUnavailable || host.Status.Error == "" {
		t.Fatalf("unreachable node:3 = %+v", host)
	}
	if host := hostByID(t, report, HostScopeAPI); host.Status.State != netaccess.StateDisconnected {
		t.Fatalf("api beside unreachable node = %+v", host)
	}

	// A node listing failure is an error: an admin must not read the api host
	// alone as the whole deployment.
	nodes.listErr = errors.New("database is away")
	if _, err := f.service.NetworkAccessStatus(ctx, "stub"); err == nil {
		t.Fatal("node listing failure was hidden")
	}
}

// A proxy's own service answers for itself: HostNetworkAccessStatus lists
// each provider instance here, connect and disconnect act on this host, and
// an unknown slug is ErrNetworkAccessProviderNotFound for the route's 404.
func TestHostNetworkAccessOperationsAnswerForThisHost(t *testing.T) {
	f := newResidentFixture(t, ResidentOptions{})
	ctx := context.Background()
	broker := f.broker
	f.service.SetNetworkAccessStatusSink(broker)

	report, err := f.service.HostNetworkAccessStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Providers) != 1 || report.Providers[0].State != netaccess.StateUnavailable {
		t.Fatalf("status before start = %+v", report)
	}
	status, err := f.service.HostNetworkAccessProviderStatus(ctx, "stub")
	if err != nil || status.Provider != "stub" || status.State != netaccess.StateUnavailable {
		t.Fatalf("provider status before start = %+v, %v", status, err)
	}

	f.service.StartResidents(ctx)
	waitState(t, f.service, 5, "running", running)

	status, err = f.service.HostNetworkAccessConnect(ctx, "stub")
	if err != nil || status.State != netaccess.StateConnected || status.Origin != "https://silo.stub.test" {
		t.Fatalf("connect = %+v, %v", status, err)
	}
	if cached, ok := broker.Status.Get(5); !ok || !cached.Connected() {
		t.Fatalf("connect did not refresh the status cache: %+v %v", cached, ok)
	}
	if report := broker.Status.NodeNetworkAccess(); report["stub"].Origin != "https://silo.stub.test" {
		t.Fatalf("health report = %+v", report)
	}
	report, err = f.service.HostNetworkAccessStatus(ctx)
	if err != nil || len(report.Providers) != 1 || report.Providers[0].State != netaccess.StateConnected {
		t.Fatalf("status after connect = %+v, %v", report, err)
	}
	status, err = f.service.HostNetworkAccessProviderStatus(ctx, "stub")
	if err != nil || status.Provider != "stub" || status.State != netaccess.StateConnected {
		t.Fatalf("provider status after connect = %+v, %v", status, err)
	}
	if _, err := f.service.HostNetworkAccessProviderStatus(ctx, "netbird"); !errors.Is(err, ErrNetworkAccessProviderNotFound) {
		t.Fatalf("unknown provider status err = %v", err)
	}
	status, err = f.service.HostNetworkAccessDisconnect(ctx, "stub")
	if err != nil || status.State != netaccess.StateDisconnected {
		t.Fatalf("disconnect = %+v, %v", status, err)
	}
	if _, err := f.service.HostNetworkAccessConnect(ctx, "netbird"); !errors.Is(err, ErrNetworkAccessProviderNotFound) || !errors.Is(err, netaccess.ErrProviderNotFound) {
		t.Fatalf("unknown provider err = %v", err)
	}
}

// A closed resident gate keeps every resident stopped and names the reason
// in the admin status; opening it lets the next reconcile start them.
func TestResidentGateHoldsResidentsUntilItOpens(t *testing.T) {
	f := newResidentFixture(t, ResidentOptions{})
	ctx := context.Background()
	f.service.SetNetworkAccessStatusSink(f.broker)
	var mu sync.Mutex
	gateErr := errors.New("this proxy's stream_nodes row is not known yet")
	f.service.SetResidentGate(func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		return gateErr
	})

	f.service.StartResidents(ctx)
	if _, ok := f.service.Residents().State(5); ok {
		t.Fatal("resident was tracked while the gate was closed")
	}
	if _, err := f.host.Client(5); !errors.Is(err, pluginhost.ErrClientNotFound) {
		t.Fatalf("resident started behind a closed gate: %v", err)
	}
	report, err := f.service.NetworkAccessStatus(ctx, "stub")
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Hosts[0].Status; got.State != netaccess.StateUnavailable || got.Error != gateErr.Error() {
		t.Fatalf("status behind the gate = %+v", got)
	}

	mu.Lock()
	gateErr = nil
	mu.Unlock()
	f.service.OnLifecycleChange(ctx)
	waitState(t, f.service, 5, "running after the gate opened", running)
	if reason := f.service.Residents().GateError(); reason != "" {
		t.Fatalf("gate error after opening = %q", reason)
	}

	// Closing the gate again stops the resident.
	mu.Lock()
	gateErr = errors.New("row was deleted")
	mu.Unlock()
	f.service.OnLifecycleChange(ctx)
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, tracked := f.service.Residents().State(5)
		_, clientErr := f.host.Client(5)
		if !tracked && errors.Is(clientErr, pluginhost.ErrClientNotFound) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("resident still running behind a re-closed gate (tracked=%v, client err=%v)", tracked, clientErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A following host reconciles on EventPluginsChanged: the publisher's
// lifecycle change starts the resident on the follower, a restart event
// replaces the follower's process, and a disable stops it.
func TestFollowLifecycleChangesReconcilesOnPublishedEvents(t *testing.T) {
	bus := newFakeBus()
	follower := newResidentFixture(t, ResidentOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The follower's store starts with the installation disabled; the
	// publisher (any service with the bus wired) announces changes.
	disabled := false
	follower.store.byID[5].Enabled = disabled
	follower.service.StartResidents(ctx)
	if _, ok := follower.service.Residents().State(5); ok {
		t.Fatal("disabled installation was supervised")
	}
	if err := follower.service.FollowLifecycleChanges(ctx, bus, time.Hour); err != nil {
		t.Fatal(err)
	}

	publisher := &Service{}
	publisher.PublishLifecycleChanges(bus)

	follower.store.byID[5].Enabled = true
	publisher.OnLifecycleChange(ctx)
	waitState(t, follower.service, 5, "running after the publisher's change", running)
	first, err := follower.host.Client(5)
	if err != nil {
		t.Fatal(err)
	}

	publisher.publishPluginsChanged(ctx, PluginsChangedEvent{InstallationID: 5, Restart: true})
	deadline := time.Now().Add(30 * time.Second)
	for {
		current, err := follower.host.Client(5)
		state, _ := follower.service.Residents().State(5)
		if err == nil && current != first && state.State == ResidentRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("follower did not replace its process on the restart event (err=%v, state=%+v)", err, state)
		}
		time.Sleep(10 * time.Millisecond)
	}

	follower.store.byID[5].Enabled = false
	publisher.OnLifecycleChange(ctx)
	deadline = time.Now().Add(30 * time.Second)
	for {
		_, tracked := follower.service.Residents().State(5)
		_, clientErr := follower.host.Client(5)
		if !tracked && errors.Is(clientErr, pluginhost.ErrClientNotFound) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("follower kept the resident after the disable event (tracked=%v, err=%v)", tracked, clientErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = cache.EventPluginsChanged
}

// failingSubscribeBus is a bus whose subscription cannot be established, as
// when Redis is down while a proxy boots.
type failingSubscribeBus struct{ fakeBus }

func (b *failingSubscribeBus) Subscribe(context.Context, string, cache.EventHandler) error {
	return errors.New("redis unavailable")
}

// A follower whose subscription fails still reconciles on the poll: the
// error is reported for logging, but the poll goroutine is started anyway so
// later changes reach the proxy without a restart.
func TestFollowLifecycleChangesPollsWhenTheSubscriptionFails(t *testing.T) {
	bus := &failingSubscribeBus{fakeBus: *newFakeBus()}
	follower := newResidentFixture(t, ResidentOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	follower.store.byID[5].Enabled = false
	follower.service.StartResidents(ctx)
	if _, ok := follower.service.Residents().State(5); ok {
		t.Fatal("disabled installation was supervised")
	}
	if err := follower.service.FollowLifecycleChanges(ctx, bus, 20*time.Millisecond); err == nil {
		t.Fatal("subscription error was not reported")
	}

	// Nothing publishes to this follower; only the poll can notice.
	follower.store.byID[5].Enabled = true
	waitState(t, follower.service, 5, "running after the poll noticed the change", running)
}
