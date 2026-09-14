package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

// fakeProxyNode is an httptest stand-in for a proxy's bearer network-access
// routes: it records what it was asked, refuses a wrong bearer, and answers
// the status the API stores.
type fakeProxyNode struct {
	secret string
	mu     sync.Mutex
	calls  []string
	// connected flips on connect and off on disconnect.
	connected bool
}

func (f *fakeProxyNode) status() netaccess.Status {
	status := netaccess.Status{InstallationID: 5, Provider: "stub", State: netaccess.StateDisconnected}
	if f.connected {
		status.State = netaccess.StateConnected
		status.Origin = "https://proxy-1.stub.test"
		status.AuthURL = ""
	}
	return status
}

func (f *fakeProxyNode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer "+f.secret {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/network-access/status":
		_ = json.NewEncoder(w).Encode(netaccess.HostStatusReport{Providers: []netaccess.Status{f.status(), {InstallationID: 9, Provider: "down", State: netaccess.StateUnavailable, Error: "plugin process is failed"}}})
	case r.Method == http.MethodPost && r.URL.Path == "/network-access/stub/connect":
		f.connected = true
		_ = json.NewEncoder(w).Encode(f.status())
	case r.Method == http.MethodPost && r.URL.Path == "/network-access/stub/disconnect":
		f.connected = false
		_ = json.NewEncoder(w).Encode(f.status())
	case strings.HasPrefix(r.URL.Path, "/network-access/"):
		http.Error(w, "network access provider not found", http.StatusNotFound)
	default:
		http.NotFound(w, r)
	}
}

// The node handler is the API's reach into proxy nodes: it lists only enabled
// proxies, calls the node's bearer routes with the node secret, decodes what
// the proxy answers, and maps a 404 to "provider not installed there".
func TestNodeHandlerFansNetworkAccessOutToProxies(t *testing.T) {
	const secret = "node-bearer-secret"
	proxy := &fakeProxyNode{secret: secret}
	server := httptest.NewServer(proxy)
	defer server.Close()

	repo := &stubNodeRepository{nodes: []*nodepool.Node{
		{ID: 3, Name: "proxy-1", Type: nodepool.NodeTypeProxy, URL: server.URL + "/", Enabled: true},
		{ID: 4, Name: "proxy-off", Type: nodepool.NodeTypeProxy, URL: "http://proxy-off", Enabled: false},
		{ID: 5, Name: "gpu-1", Type: nodepool.NodeTypeTranscode, URL: "http://gpu-1", Enabled: true},
	}}
	handler := NewNodeHandler(repo, nil, nil, nil, nil, nil, secret)
	ctx := context.Background()

	nodes, err := handler.ListNetworkAccessNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].ID != 3 || nodes[0].Name != "proxy-1" || nodes[0].URL != server.URL+"/" {
		t.Fatalf("nodes = %+v", nodes)
	}
	if host := nodes[0].Host(); host.ID != "node:3" || host.Role != "proxy" || host.Name != "proxy-1" {
		t.Fatalf("host = %+v", host)
	}
	node := nodes[0]

	status, err := handler.NodeNetworkAccessStatus(ctx, node, "stub")
	if err != nil || status.State != netaccess.StateDisconnected || status.InstallationID != 5 {
		t.Fatalf("status = %+v, %v", status, err)
	}
	down, err := handler.NodeNetworkAccessStatus(ctx, node, "down")
	if err != nil || down.State != netaccess.StateUnavailable || down.Error == "" {
		t.Fatalf("down status = %+v, %v", down, err)
	}
	if _, err := handler.NodeNetworkAccessStatus(ctx, node, "netbird"); !errors.Is(err, plugins.ErrNetworkAccessProviderNotFound) {
		t.Fatalf("unknown provider status err = %v", err)
	}

	connected, err := handler.NodeNetworkAccessConnect(ctx, node, "stub")
	if err != nil || connected.State != netaccess.StateConnected || connected.Origin != "https://proxy-1.stub.test" {
		t.Fatalf("connect = %+v, %v", connected, err)
	}
	disconnected, err := handler.NodeNetworkAccessDisconnect(ctx, node, "stub")
	if err != nil || disconnected.State != netaccess.StateDisconnected {
		t.Fatalf("disconnect = %+v, %v", disconnected, err)
	}
	if _, err := handler.NodeNetworkAccessConnect(ctx, node, "netbird"); !errors.Is(err, plugins.ErrNetworkAccessProviderNotFound) {
		t.Fatalf("unknown provider connect err = %v", err)
	}

	want := []string{
		"GET /network-access/status", "GET /network-access/status", "GET /network-access/status",
		"POST /network-access/stub/connect", "POST /network-access/stub/disconnect", "POST /network-access/netbird/connect",
	}
	proxy.mu.Lock()
	calls := append([]string(nil), proxy.calls...)
	proxy.mu.Unlock()
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("proxy calls = %v, want %v", calls, want)
	}

	// A wrong node secret is reported as the proxy refusing the bearer, not as
	// an unknown provider.
	wrong := NewNodeHandler(repo, nil, nil, nil, nil, nil, "other-secret")
	if _, err := wrong.NodeNetworkAccessStatus(ctx, node, "stub"); err == nil || errors.Is(err, plugins.ErrNetworkAccessProviderNotFound) || !strings.Contains(err.Error(), "bearer") {
		t.Fatalf("wrong bearer err = %v", err)
	}

	// An unreachable proxy is an error the service turns into unavailable.
	server.Close()
	if _, err := handler.NodeNetworkAccessStatus(ctx, node, "stub"); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("unreachable err = %v", err)
	}
}
