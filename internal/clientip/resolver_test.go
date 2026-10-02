package clientip

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMiddlewareTrustBoundaryAndHotReload(t *testing.T) {
	trusted, err := ParseCIDRs("127.0.0.0/8,10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	resolver := NewResolver(trusted)
	resolve := func(remote, xff, realIP string) string {
		t.Helper()
		var got string
		h := Middleware(resolver)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got = r.RemoteAddr
			if contextIP := FromContext(r.Context()); contextIP != got {
				t.Fatalf("context IP = %q, RemoteAddr = %q", contextIP, got)
			}
		}))
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		r.Header.Set("X-Forwarded-For", xff)
		r.Header.Set("X-Real-IP", realIP)
		h.ServeHTTP(httptest.NewRecorder(), r)
		return got
	}

	if got := resolve("127.0.0.1:1234", "198.51.100.9", ""); got != "198.51.100.9" {
		t.Fatalf("trusted peer XFF = %q", got)
	}
	if got := resolve("203.0.113.7:1234", "198.51.100.9", ""); got != "203.0.113.7" {
		t.Fatalf("untrusted spoof = %q", got)
	}
	if got := resolve("127.0.0.1:1234", "10.1.1.1, 127.0.0.2", ""); got != "10.1.1.1" {
		t.Fatalf("all-trusted chain = %q", got)
	}
	if got := resolve("127.0.0.1:1234", "", "198.51.100.10"); got != "198.51.100.10" {
		t.Fatalf("X-Real-IP fallback = %q", got)
	}

	narrowed, err := ParseCIDRs("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	resolver.UpdateTrustedCIDRs(narrowed)
	if got := resolve("127.0.0.1:1234", "198.51.100.9", ""); got != "127.0.0.1" {
		t.Fatalf("hot reload did not narrow trust: %q", got)
	}
}
