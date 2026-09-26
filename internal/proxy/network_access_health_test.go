package proxy

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/netaccess"
)

// /health carries the provider report the API stores on this proxy's row.
// Without a status source the field is absent — the same body a build that
// predates the field serves — and with one it mirrors the cache.
func TestHealthReportsNetworkAccessFromTheStatusSource(t *testing.T) {
	server := newMetricsProxyServer(t)
	server.metrics = newFakeSampler()

	if got := decodeProxyHealth(t, server).NetworkAccess; got != nil {
		t.Fatalf("health without a status source reported %#v", got)
	}

	cache := netaccess.NewStatusCache()
	server.SetNetworkAccessStatus(cache)
	if got := decodeProxyHealth(t, server).NetworkAccess; got != nil {
		t.Fatalf("health with an empty cache reported %#v", got)
	}

	cache.Report(netaccess.Status{
		InstallationID: 1, Provider: "tailscale", State: netaccess.StateConnected,
		Origin: "https://proxy-1.tail1234.ts.net", Hostname: "proxy-1.tail1234.ts.net",
		AuthURL: "https://login.tailscale.com/a/secret",
	})
	got := decodeProxyHealth(t, server).NetworkAccess
	entry, ok := got["tailscale"]
	if !ok || entry.State != netaccess.StateConnected || entry.Origin != "https://proxy-1.tail1234.ts.net" || entry.Hostname != "proxy-1.tail1234.ts.net" || entry.UpdatedAt.IsZero() {
		t.Fatalf("health network_access = %#v", got)
	}
	if origin, ok := got.ConnectedOrigin("tailscale"); !ok || origin != "https://proxy-1.tail1234.ts.net" {
		t.Fatalf("ConnectedOrigin over the wire form = %q, %v", origin, ok)
	}
}
