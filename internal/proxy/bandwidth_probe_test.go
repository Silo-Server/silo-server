package proxy

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	// A probe the deadline ended reports here, then keeps its slot until the
	// test lets go, so the refusal below does not race the reap.
	ended := make(chan struct{}, 2)
	letGo := make(chan struct{})
	probeEndHook = func() {
		select {
		case ended <- struct{}{}:
		default:
		}
		<-letGo
	}
	t.Cleanup(func() {
		probeWriteTimeout = previous
		probeEndHook = nil
	})
	srv := newSocketProxyServer(t, secret, nil)
	server := httptest.NewServer(srv.router())
	defer server.Close()
	// The handlers wait on letGo; it opens before server.Close waits for them.
	var once sync.Once
	release := func() { once.Do(func() { close(letGo) }) }
	defer release()
	token, err := streamtoken.Sign(streamtoken.Claims{PlayMethod: streamtoken.PlayMethodBandwidthProbe, UserID: 7}, secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Two probes fill the account's slots; neither reads its body. Their
	// connections close before server.Close waits, so a probe the deadline
	// never ends fails the test instead of hanging it.
	var stalled []net.Conn
	defer func() {
		for _, conn := range stalled {
			_ = conn.Close()
		}
	}()
	for range 2 {
		conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		stalled = append(stalled, conn)
		if _, err := fmt.Fprintf(conn, "GET /stream/bandwidth-probe/%s?size=%d HTTP/1.1\r\nHost: proxy\r\n\r\n", token, bandwidthprobe.MaxSize); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		select {
		case <-ended:
		case <-time.After(10 * time.Second):
			t.Fatal("the write deadline never ended a stalled probe")
		}
	}
	probe := func() int {
		return socketProxyRequest(t, server.Client(), http.MethodGet, server.URL+"/stream/bandwidth-probe/"+token+"?size=4096", nil).status
	}
	if status := probe(); status != http.StatusTooManyRequests {
		t.Fatalf("probe beside two stalled ones: status %d, want 429", status)
	}
	release()
	deadline := time.Now().Add(10 * time.Second)
	for status := probe(); status != http.StatusOK; status = probe() {
		if time.Now().After(deadline) {
			t.Fatalf("last probe status %d; the reaped probes never released their slots", status)
		}
		//nolint:forbidigo // polls the endpoint until the reaped probes give their slots back
		time.Sleep(10 * time.Millisecond)
	}
}
