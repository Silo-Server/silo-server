package clientip

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRequestOrigin pins how RequestOrigin combines the trusted scheme and
// host, and that ambiguous proxy scheme metadata names the transport's scheme
// rather than a guess that could match a client's configured origin. The
// trust boundary itself is TestRequestSchemeTrustBoundary's and
// TestRequestHostTrustBoundary's; serialization is TestSerializeOrigin's.
func TestRequestOrigin(t *testing.T) {
	trusted, err := ParseCIDRs("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	resolver := NewResolver(trusted)
	for _, tc := range []struct {
		name  string
		proto []string
		tls   bool
		want  string
	}{
		{"trusted proxy", []string{"https"}, false, "https://media.example.com:8443"},
		{"ambiguous proto over http", []string{"https", "https"}, false, "http://media.example.com:8443"},
		{"ambiguous proto over tls", []string{"https", "https"}, true, "https://media.example.com:8443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://silo:8090/", nil)
			req.RemoteAddr = "10.1.2.3:5000"
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			req.Header["X-Forwarded-Proto"] = tc.proto
			req.Header.Set("X-Forwarded-Host", "Media.Example.COM:8443")
			var got string
			Middleware(resolver)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = RequestOrigin(r)
			})).ServeHTTP(httptest.NewRecorder(), req)
			if got != tc.want {
				t.Fatalf("RequestOrigin = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSerializeOrigin(t *testing.T) {
	for _, tc := range []struct{ scheme, host, want string }{
		{"https", "silo.test", "https://silo.test"},
		{"HTTPS", "Silo.Example.TEST:443", "https://silo.example.test"},
		{"http", "10.0.0.5:80", "http://10.0.0.5"},
		{"http", "10.0.0.5:8096", "http://10.0.0.5:8096"},
		{"https", "silo.test:80", "https://silo.test:80"},
		{"https", "[FE80::1]:8443", "https://[fe80::1]:8443"},
		{"https", "[::1]", "https://[::1]"},
	} {
		if got := SerializeOrigin(tc.scheme, tc.host); got != tc.want {
			t.Errorf("SerializeOrigin(%q, %q) = %q, want %q", tc.scheme, tc.host, got, tc.want)
		}
	}
}
