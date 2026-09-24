package transcodeproxy

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestNodeClientKeepsStreamsOpenAndRefusesRedirects(t *testing.T) {
	client := NodeClient()
	// An overall client timeout would cut segment bodies that stream at the
	// viewer's download speed; only waiting for response headers is bounded.
	if client.Timeout != 0 {
		t.Fatalf("client timeout = %v, want none", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", client.Transport)
	}
	if transport.ResponseHeaderTimeout <= 0 {
		t.Fatal("hung nodes must be bounded by a response-header timeout")
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
