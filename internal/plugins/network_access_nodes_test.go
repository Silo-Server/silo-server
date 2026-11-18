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

// failingSubscribeBus is a bus whose subscription cannot be established, as
// when Redis is down while a proxy boots.
type failingSubscribeBus struct{ fakeBus }

func (b *failingSubscribeBus) Subscribe(context.Context, string, cache.EventHandler) error {
	return errors.New("redis unavailable")
}
