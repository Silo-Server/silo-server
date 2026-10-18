package proxy

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/bandwidthprobe"
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

// TestBandwidthProbeReapsAStalledReader: a client that stops reading loses its
// probe slot at the write deadline instead of holding it forever.
func TestBandwidthProbeReapsAStalledReader(t *testing.T) {
	const secret = "test-probe-secret"
	previous := probeWriteTimeout
	probeWriteTimeout = 300 * time.Millisecond
	t.Cleanup(func() { probeWriteTimeout = previous })
	srv := newSocketProxyServer(t, secret, nil)
	server := httptest.NewServer(srv.router())
	defer server.Close()
	token, err := streamtoken.Sign(streamtoken.Claims{PlayMethod: streamtoken.PlayMethodBandwidthProbe, UserID: 7}, secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Two probes fill the account's slots; neither reads its body.
	for range 2 {
		conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if _, err := fmt.Fprintf(conn, "GET /stream/bandwidth-probe/%s?size=%d HTTP/1.1\r\nHost: proxy\r\n\r\n", token, bandwidthprobe.MaxSize); err != nil {
			t.Fatal(err)
		}
	}
	probe := func() int {
		return socketProxyRequest(t, server.Client(), http.MethodGet, server.URL+"/stream/bandwidth-probe/"+token+"?size=4096", nil).status
	}
	deadline := time.Now().Add(10 * time.Second)
	sawRefusal := false
	for {
		status := probe()
		if status == http.StatusTooManyRequests {
			sawRefusal = true
		}
		if status == http.StatusOK && sawRefusal {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("last probe status %d, refused while stalled: %v; the stalled probes never released their slots", status, sawRefusal)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
