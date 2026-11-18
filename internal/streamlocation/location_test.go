package streamlocation

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/clientip"
)

// A forwarder outside the trusted proxies resolves to its own private address;
// its client may be anywhere, so the request is remote.
func TestFromContextBehindForwarders(t *testing.T) {
	trusted, err := clientip.ParseCIDRs("10.0.0.20/32")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		remote string
		xff    string
		want   Location
	}{
		{name: "untrusted forwarder naming a public client", remote: "10.0.0.21:4000", xff: "203.0.113.7", want: Remote},
		{name: "trusted proxy naming a private client", remote: "10.0.0.20:4000", xff: "192.168.1.7", want: Local},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got Location
			h := clientip.Middleware(clientip.NewResolver(trusted))(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = FromContext(r.Context())
			}))
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = test.remote
			r.Header.Set("X-Forwarded-For", test.xff)
			h.ServeHTTP(httptest.NewRecorder(), r)
			if got != test.want {
				t.Fatalf("FromContext() = %q, want %q", got, test.want)
			}
		})
	}
}
