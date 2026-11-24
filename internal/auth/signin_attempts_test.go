package auth

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/netaccess"
)

// A sign-in counts from, and makes known, only an address that says where
// the person is: never a forwarder's own address, this host's loopback, or a
// network access provider's connection, which strangers share.
func TestSignInSource(t *testing.T) {
	trusted, err := clientip.ParseCIDRs("10.0.0.0/8,127.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	resolver := clientip.NewResolver(trusted)
	tailscale := func(peer string) *netaccess.Path {
		path := netaccess.Path{Provider: "tailscale", InstallationID: 1}
		if peer != "" {
			path.Peer = netip.MustParseAddr(peer)
		}
		return &path
	}

	for _, tc := range []struct {
		name         string
		remoteAddr   string
		forwardedFor string
		overlay      *netaccess.Path
		want         string
	}{
		{name: "direct client", remoteAddr: "198.51.100.7:4000", want: "198.51.100.7"},
		{name: "client named by a trusted proxy", remoteAddr: "10.0.0.2:4000", forwardedFor: "198.51.100.7", want: "198.51.100.7"},
		{name: "untrusted forwarder", remoteAddr: "203.0.113.5:4000", forwardedFor: "198.51.100.7"},
		{name: "trusted proxy relaying an untrusted forwarder", remoteAddr: "10.0.0.2:4000", forwardedFor: "198.51.100.7, 203.0.113.5"},
		{name: "loopback", remoteAddr: "127.0.0.1:4000"},
		{name: "overlay peer", remoteAddr: "127.0.0.1:4000", overlay: tailscale("100.101.102.103"), want: "overlay:1:100.101.102.103"},
		{name: "overlay without a peer", remoteAddr: "127.0.0.1:4000", overlay: tailscale("")},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v2/auth/login", nil)
		req.RemoteAddr = tc.remoteAddr
		if tc.forwardedFor != "" {
			req.Header.Set("X-Forwarded-For", tc.forwardedFor)
		}
		var got string
		clientip.Middleware(resolver)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			if tc.overlay != nil {
				r = r.WithContext(netaccess.WithPath(r.Context(), *tc.overlay))
			}
			got = signInSource(r.Context())
		})).ServeHTTP(httptest.NewRecorder(), req)
		if got != tc.want {
			t.Errorf("%s: source %q, want %q", tc.name, got, tc.want)
		}
	}
}
