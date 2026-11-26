package directconn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/darthShadow/shunpike/coordination/protocol"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/netaccess"
)

func TestDirectWrapper(t *testing.T) {
	trusted, err := clientip.ParseCIDRs("192.168.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	key := protocol.NodeKey{1}
	for _, test := range []struct {
		name, endpoint string
		peer           protocol.NodeKey
		status         int
	}{
		{"public source", "198.51.100.7:41641", key, http.StatusNoContent},
		{"private source", "192.168.1.20:41641", key, http.StatusNoContent},
		{"no peer", "198.51.100.7:41641", protocol.NodeKey{}, http.StatusMisdirectedRequest},
		{"no direct endpoint", "", key, http.StatusMisdirectedRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			var endpoint netip.AddrPort
			if test.endpoint != "" {
				endpoint = netip.MustParseAddrPort(test.endpoint)
			}
			called := false
			next := clientip.Middleware(clientip.NewResolver(trusted))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if got := clientip.FromContext(r.Context()); got != endpoint.Addr().String() {
					t.Errorf("clientip = %q, want %q", got, endpoint.Addr().String())
				}
				if got := clientip.PeerFromContext(r.Context()); got != test.endpoint {
					t.Errorf("transport peer = %q, want %q", got, test.endpoint)
				}
				for name := range r.Header {
					lower := strings.ToLower(name)
					if lower == "forwarded" || strings.HasPrefix(lower, "x-forwarded-") || lower == "x-real-ip" || strings.HasPrefix(lower, "x-silo-ingress-") {
						t.Errorf("untrusted header reached handler: %s", name)
					}
				}
				path := netaccess.PathFromContext(r.Context())
				if path.Direct == nil {
					t.Error("Path.Direct is nil")
				} else {
					want := netaccess.DirectPath{HostID: "api-test", PeerKey: key.String(), Class: "direct"}
					if *path.Direct != want {
						t.Errorf("Path.Direct = %+v, want %+v", *path.Direct, want)
					}
				}
				if !path.IsDefault() {
					t.Errorf("IsDefault = false for %+v", path)
				}
				if r.Header.Get("Authorization") != "Bearer test" {
					t.Error("application credential changed")
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			source := func(context.Context) (protocol.NodeKey, netip.AddrPort) { return test.peer, endpoint }
			h := wrapDirect("api-test", source, next)
			req := httptest.NewRequest(http.MethodGet, "https://peer.invalid/probe", nil)
			req.RemoteAddr = "127.0.0.1:1"
			req.Header.Set("Forwarded", "for=203.0.113.9")
			req.Header.Set("X-Forwarded-For", "203.0.113.9")
			req.Header.Set("X-Forwarded-Proto", "http")
			req.Header.Set("X-Forwarded-Host", "spoof.invalid")
			req.Header.Set("X-Forwarded-Custom", "untrusted")
			req.Header.Set("X-Real-IP", "203.0.113.9")
			req.Header.Set(netaccess.IngressTokenHeader, "forged")
			req.Header.Set(netaccess.IngressPeerHeader, "203.0.113.9")
			req.Header.Set("Authorization", "Bearer test")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != test.status {
				t.Errorf("status = %d, want %d", rec.Code, test.status)
			}
			if called != (test.status == http.StatusNoContent) {
				t.Errorf("handler called = %t for status %d", called, test.status)
			}
		})
	}
}
