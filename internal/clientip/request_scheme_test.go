package clientip

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestSchemeTrustBoundary(t *testing.T) {
	cidrs, err := ParseCIDRs("127.0.0.0/8,::1/128")
	if err != nil {
		t.Fatal(err)
	}
	resolver := NewResolver(cidrs)
	for _, tc := range []struct {
		name, peer string
		values     []string
		tls        bool
		want       string
		upgrade    bool
	}{
		{"trusted", "127.0.0.1:80", []string{"https"}, false, "https", false},
		{"ipv6", "[::1]:80", []string{"https"}, false, "https", false},
		{"untrusted", "192.0.2.1:80", []string{"https"}, false, "http", false},
		{"invalid peer", "invalid", []string{"https"}, false, "http", false},
		{"direct tls", "192.0.2.1:80", nil, true, "https", false},
		{"trusted no header", "127.0.0.1:80", nil, false, "http", false},
		{"repeated", "127.0.0.1:80", []string{"https", "https"}, false, "", false},
		{"list", "127.0.0.1:80", []string{"https,http"}, false, "", false},
		{"invalid", "127.0.0.1:80", []string{"wss"}, false, "", false},
		{"empty", "127.0.0.1:80", []string{""}, false, "", false},
		// Traefik forwards WebSocket upgrades as wss/ws (#1089).
		{"websocket wss", "127.0.0.1:80", []string{"wss"}, false, "https", true},
		{"websocket ws", "127.0.0.1:80", []string{"ws"}, false, "http", true},
		{"websocket https", "127.0.0.1:80", []string{"https"}, false, "https", true},
		{"websocket untrusted", "192.0.2.1:80", []string{"wss"}, false, "http", true},
		{"websocket repeated", "127.0.0.1:80", []string{"wss", "wss"}, false, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "http://example.test:8080/", nil)
			req.RemoteAddr = tc.peer
			req.Header["X-Forwarded-Proto"] = tc.values
			req.Header.Set("X-Forwarded-For", "198.51.100.1")
			if tc.upgrade {
				req.Header.Set("Connection", "keep-alive, Upgrade")
				req.Header.Set("Upgrade", "websocket")
			}
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			Middleware(resolver)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				if got := RequestScheme(r); got != tc.want {
					t.Fatalf("scheme=%q want %q", got, tc.want)
				}
				if PeerFromContext(r.Context()) != tc.peer {
					t.Fatal("transport peer lost")
				}
				if tc.name == "trusted" && r.RemoteAddr != "198.51.100.1" {
					t.Fatal("test did not exercise rewritten client address")
				}
			})).ServeHTTP(httptest.NewRecorder(), req)
		})
	}
	resolver.UpdateTrustedCIDRs(nil)
	req := httptest.NewRequest("GET", "http://example.test/", nil)
	req.RemoteAddr = "127.0.0.1:80"
	req.Header.Set("X-Forwarded-Proto", "https")
	if got := resolver.requestScheme(req); got != "http" {
		t.Fatal("trust reload ignored")
	}
	if got := RequestScheme(req); got != "http" {
		t.Fatal("header trusted without middleware")
	}
	all, _ := ParseCIDRs("0.0.0.0/0")
	req.RemoteAddr = "invalid"
	if got := NewResolver(all).requestScheme(req); got != "http" {
		t.Fatal("invalid peer trusted")
	}
}
