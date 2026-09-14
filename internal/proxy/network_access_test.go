package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/netaccess"
)

// fakeProviderHost stands in for the plugin service: one provider, "stub",
// whose connect and disconnect flip an in-memory state, and a "down" provider
// whose process is not running.
type fakeProviderHost struct {
	connected bool
	calls     []string
}

func (f *fakeProviderHost) status() netaccess.Status {
	status := netaccess.Status{InstallationID: 5, Provider: "stub", State: netaccess.StateDisconnected, ProviderVersion: "stub 0.1.0"}
	if f.connected {
		status.State = netaccess.StateConnected
		status.Origin = "https://proxy-1.stub.test"
		status.Hostname = "proxy-1.stub.test"
		status.AuthURL = ""
	}
	return status
}

func (f *fakeProviderHost) HostNetworkAccessStatus(context.Context) (netaccess.HostStatusReport, error) {
	f.calls = append(f.calls, "status")
	return netaccess.HostStatusReport{Providers: []netaccess.Status{
		f.status(),
		{InstallationID: 9, Provider: "down", State: netaccess.StateUnavailable, Error: "plugin process is backoff: plugin process exited"},
	}}, nil
}

func (f *fakeProviderHost) HostNetworkAccessConnect(_ context.Context, provider string) (netaccess.Status, error) {
	f.calls = append(f.calls, "connect:"+provider)
	if provider != "stub" {
		return netaccess.Status{}, netaccess.ErrProviderNotFound
	}
	f.connected = true
	return f.status(), nil
}

func (f *fakeProviderHost) HostNetworkAccessDisconnect(_ context.Context, provider string) (netaccess.Status, error) {
	f.calls = append(f.calls, "disconnect:"+provider)
	if provider != "stub" {
		return netaccess.Status{}, netaccess.ErrProviderNotFound
	}
	f.connected = false
	return f.status(), nil
}

func networkAccessRequest(t *testing.T, server *Server, method, path, secret string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, nil)
	if secret != "" {
		request.Header.Set("Authorization", "Bearer "+secret)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

// The bearer network-access routes drive the provider host: status lists
// every instance with the full status (auth URL and error included, since
// the caller holds the node bearer), connect and disconnect answer the state
// reached, an unknown provider is 404, and all of them refuse without the
// bearer with the listener's plain-text failure.
func TestProxyNetworkAccessRoutesDriveTheProviderHost(t *testing.T) {
	const secret = "network-access-proxy-secret"
	server := newDownloadProxyServer(t, secret)
	host := &fakeProviderHost{}
	server.SetNetworkAccessProviderHost(host)

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/network-access/status"},
		{http.MethodPost, "/network-access/stub/connect"},
		{http.MethodPost, "/network-access/stub/disconnect"},
	} {
		denied := networkAccessRequest(t, server, route.method, route.path, "")
		if denied.Code != http.StatusUnauthorized || denied.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Fatalf("%s %s without bearer: %d %s", route.method, route.path, denied.Code, denied.Body)
		}
	}
	if len(host.calls) != 0 {
		t.Fatalf("refused requests reached the host: %v", host.calls)
	}

	status := networkAccessRequest(t, server, http.MethodGet, "/network-access/status", secret)
	if status.Code != http.StatusOK || status.Header().Get("Content-Type") != "application/json" || status.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status: %d %s %q", status.Code, status.Body, status.Header())
	}
	var report netaccess.HostStatusReport
	if err := json.Unmarshal(status.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Providers) != 2 || report.Providers[0].Provider != "stub" || report.Providers[0].State != netaccess.StateDisconnected ||
		report.Providers[1].Provider != "down" || report.Providers[1].State != netaccess.StateUnavailable || report.Providers[1].Error == "" {
		t.Fatalf("status report = %+v", report)
	}

	connect := networkAccessRequest(t, server, http.MethodPost, "/network-access/stub/connect", secret)
	if connect.Code != http.StatusOK {
		t.Fatalf("connect: %d %s", connect.Code, connect.Body)
	}
	var connected netaccess.Status
	if err := json.Unmarshal(connect.Body.Bytes(), &connected); err != nil {
		t.Fatal(err)
	}
	if connected.State != netaccess.StateConnected || connected.Origin != "https://proxy-1.stub.test" || connected.InstallationID != 5 {
		t.Fatalf("connect status = %+v", connected)
	}

	disconnect := networkAccessRequest(t, server, http.MethodPost, "/network-access/stub/disconnect", secret)
	if disconnect.Code != http.StatusOK {
		t.Fatalf("disconnect: %d %s", disconnect.Code, disconnect.Body)
	}
	var disconnected netaccess.Status
	if err := json.Unmarshal(disconnect.Body.Bytes(), &disconnected); err != nil {
		t.Fatal(err)
	}
	if disconnected.State != netaccess.StateDisconnected || disconnected.Origin != "" {
		t.Fatalf("disconnect status = %+v", disconnected)
	}

	unknown := networkAccessRequest(t, server, http.MethodPost, "/network-access/netbird/connect", secret)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown provider connect: %d %s", unknown.Code, unknown.Body)
	}
	want := []string{"status", "connect:stub", "disconnect:stub", "connect:netbird"}
	if len(host.calls) != len(want) {
		t.Fatalf("host calls = %v, want %v", host.calls, want)
	}
	for i := range want {
		if host.calls[i] != want[i] {
			t.Fatalf("host calls = %v, want %v", host.calls, want)
		}
	}
}

// A proxy without a plugin host (no providers wired) answers 503 so the API's
// fan-out can say why the host is unavailable, and never 404, which would
// read as "provider not installed here".
func TestProxyNetworkAccessRoutesWithoutAHostAnswer503(t *testing.T) {
	const secret = "network-access-proxy-secret"
	server := newDownloadProxyServer(t, secret)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/network-access/status"},
		{http.MethodPost, "/network-access/stub/connect"},
		{http.MethodPost, "/network-access/stub/disconnect"},
	} {
		got := networkAccessRequest(t, server, route.method, route.path, secret)
		if got.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s without a host: %d %s", route.method, route.path, got.Code, got.Body)
		}
	}
}

// The proxy listener validates the ingress token a provider stamps on the
// requests it forwards, so an unknown token is refused before any handler and
// a valid one is stripped.
func TestProxyListenerValidatesIngressTokens(t *testing.T) {
	server := newDownloadProxyServer(t, "secret")
	registry := netaccess.NewRegistry()
	server.SetIngressTokens(registry)
	token, err := registry.Issue(5, "stub")
	if err != nil {
		t.Fatal(err)
	}

	forged := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	forged.Header.Set(netaccess.IngressTokenHeader, "not-the-token")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, forged)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("forged token: %d %s", recorder.Code, recorder.Body)
	}

	genuine := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	genuine.Header.Set(netaccess.IngressTokenHeader, token)
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, genuine)
	if recorder.Code != http.StatusOK {
		t.Fatalf("genuine token: %d %s", recorder.Code, recorder.Body)
	}
}
