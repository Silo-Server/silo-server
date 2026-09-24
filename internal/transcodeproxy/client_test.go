package transcodeproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestNodeClientKeepsStreamsOpenAndRefusesRedirects(t *testing.T) {
	client := NodeClient()
	// An overall client timeout would cut segment bodies that stream at the
	// viewer's download speed.
	if client.Timeout != 0 {
		t.Fatalf("client timeout = %v, want none", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", client.Transport)
	}
	if transport.MaxIdleConnsPerHost < 32 {
		t.Fatalf("idle connections per node = %d, want at least 32", transport.MaxIdleConnsPerHost)
	}

	// Every caller shares one connection pool, but a caller that changes its
	// copy must not change the client the other relays use.
	client.Timeout = time.Second
	if other := NodeClient(); other.Timeout != 0 || other.Transport != client.Transport {
		t.Fatalf("second client: timeout = %v, shares transport = %v; want no timeout and a shared transport",
			other.Timeout, other.Transport == client.Transport)
	}
	client.Timeout = 0

	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Store(true) }))
	t.Cleanup(target.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	t.Cleanup(redirect.Close)
	resp, err := client.Get(redirect.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound || followed.Load() {
		t.Fatalf("status = %d, followed = %v; node redirects must not be followed", resp.StatusCode, followed.Load())
	}
}

// TestNodeClientWaitsOutNodeReconstruction guards relays against a node that
// is slow to answer for a legitimate reason. A node that restarted rebuilds a
// lost session on the first manifest or segment request before it sends
// headers. Under hw_accel=auto the rebuild can wait out one FFmpeg attempt per
// fallback path, each up to playback.ManifestStartupTimeout, and a segment
// request then waits up to 12s (playback's activeSegmentWait) for its segment:
// 102s today, past the 60s response-header limit the relays once had. Any
// header limit the client carries is scaled down with that wait so the test
// runs in milliseconds.
func TestNodeClientWaitsOutNodeReconstruction(t *testing.T) {
	const timeScale = 1000
	reconstruction := time.Duration(playback.MaxAutoTranscodeStartupAttempts)*playback.ManifestStartupTimeout + 12*time.Second
	answerAfter := reconstruction / timeScale

	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(answerAfter):
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("segment bytes"))
	}))
	t.Cleanup(node.Close)

	client := NodeClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", client.Transport)
	}
	scaled := transport.Clone()
	scaled.ResponseHeaderTimeout /= timeScale
	client.Transport = scaled
	t.Cleanup(scaled.CloseIdleConnections)

	resp, err := client.Get(node.URL)
	if err != nil {
		t.Fatalf("relay to a node that answers after %v (scaled from %v): %v", answerAfter, reconstruction, err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || string(body) != "segment bytes" {
		t.Fatalf("relay = %d %q (read error %v), want 200 with the segment", resp.StatusCode, body, err)
	}
}

// TestNodeClientStopsWithTheDownstreamRequest checks the bound that replaces a
// response-header limit: every relay sends its node request with the
// downstream request's context, so a viewer that gives up on a node that never
// answers ends the node call at once.
func TestNodeClientStopsWithTheDownstreamRequest(t *testing.T) {
	arrived := make(chan struct{}, 1)
	node := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case arrived <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	t.Cleanup(node.Close)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, node.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		resp, err := NodeClient().Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		done <- err
	}()

	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("request did not reach the node")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("relay error = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("relay kept waiting on the node after the downstream request ended")
	}
}
