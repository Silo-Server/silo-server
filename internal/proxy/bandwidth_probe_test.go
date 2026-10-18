package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

func TestBandwidthProbePurposeAndBytes(t *testing.T) {
	const secret = "test-probe-secret"
	srv := newSocketProxyServer(t, secret, nil)
	server := httptest.NewServer(srv.router())
	defer server.Close()
	for _, method := range []string{streamtoken.PlayMethodBandwidthProbe, "direct"} {
		token, err := streamtoken.Sign(streamtoken.Claims{PlayMethod: method, UserID: 7}, secret, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		got := socketProxyRequest(t, server.Client(), http.MethodGet, server.URL+"/stream/bandwidth-probe/"+token+"?size=4096", nil)
		if method == streamtoken.PlayMethodBandwidthProbe {
			if got.status != 200 || len(got.body) != 4096 {
				t.Fatalf("probe: status=%d bytes=%d", got.status, len(got.body))
			}
		} else if got.status != http.StatusForbidden {
			t.Fatalf("media token authorized probe: %d", got.status)
		}
	}
}
