package nodecontrol

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodesource"
)

// streamFixture serves the routes on a real listener, so a source response
// streams through sendfile and blocks on a client that stops reading.
type streamFixture struct {
	*openFixture
	server *httptest.Server
	// done receives once for each source response that has returned.
	done chan struct{}
}

// bigSourceBytes is larger than the loopback socket buffers, so a response
// its client does not read stays in flight.
const bigSourceBytes = 256 << 20

func newStreamFixture(t *testing.T) *streamFixture {
	t.Helper()
	f := &streamFixture{openFixture: newOpenFixture(t), done: make(chan struct{}, 64)}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.handler.ServeHTTP(w, r)
		if r.URL.Path == PathSources {
			f.done <- struct{}{}
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

// bigGrant grants nodeID a sparse source of bigSourceBytes.
func (f *streamFixture) bigGrant(nodeID int) string {
	f.t.Helper()
	path := filepath.Join(f.t.TempDir(), "big.mkv")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Truncate(path, bigSourceBytes); err != nil {
		f.t.Fatal(err)
	}
	return f.mint(nodeID, 42, path)
}

// sourceStream is a read whose headers have arrived and whose body is left
// unread; the body closes with the test.
type sourceStream struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
}

// open starts a read of grant as nodeID and returns once the headers arrive,
// leaving the body unread. Each read has its own connection.
func (f *streamFixture) open(nodeID int, key, grant string) sourceStream {
	f.t.Helper()
	req, err := http.NewRequestWithContext(f.t.Context(), http.MethodGet, f.server.URL+PathSources, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set(nodesource.HeaderSourceGrant, grant)
	nodeauthtest.SignFromNode(f.t, req, nodeID, key)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true, DisableCompression: true}}
	resp, err := client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = resp.Body.Close() })
	return sourceStream{StatusCode: resp.StatusCode, Header: resp.Header, Body: resp.Body}
}

// waitDone waits for one source response to return.
func (f *streamFixture) waitDone(what string) {
	f.t.Helper()
	select {
	case <-f.done:
	case <-time.After(10 * time.Second):
		f.t.Fatalf("%s: the source response did not return", what)
	}
}

func TestSourceStreamsAreCappedPerNode(t *testing.T) {
	f := newStreamFixture(t)
	f.openFixture.server.sourceStreamLimit = 2
	grant := f.bigGrant(nodeA)
	throttledBefore := testutil.ToFloat64(nodeSourceEnds.WithLabelValues(sourceOutcomeThrottled))

	first := f.open(nodeA, f.keyA, grant)
	second := f.open(nodeA, f.keyA, grant)
	if first.StatusCode != http.StatusOK || second.StatusCode != http.StatusOK {
		t.Fatalf("streams under the cap = %d, %d", first.StatusCode, second.StatusCode)
	}
	over := f.open(nodeA, f.keyA, grant)
	if over.StatusCode != http.StatusTooManyRequests || over.Header.Get("Retry-After") != sourceRetryAfter ||
		over.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("a stream over the cap = %d %v, want 429 with Retry-After", over.StatusCode, over.Header)
	}
	f.waitDone("the refused stream")
	if got := testutil.ToFloat64(nodeSourceEnds.WithLabelValues(sourceOutcomeThrottled)) - throttledBefore; got != 1 {
		t.Fatalf("throttled outcomes %v, want 1", got)
	}
	// The cap is per node.
	if other := f.open(nodeB, f.keyB, f.bigGrant(nodeB)); other.StatusCode != http.StatusOK {
		t.Fatalf("another node's stream = %d, want 200", other.StatusCode)
	}

	// A client that goes away releases its stream.
	_ = first.Body.Close()
	f.waitDone("the disconnected stream")
	if again := f.open(nodeA, f.keyA, grant); again.StatusCode != http.StatusOK {
		t.Fatalf("a stream after one disconnected = %d, want 200", again.StatusCode)
	}
}

// When the API server's lifetime ends, a source response blocked on a client
// that is not reading returns at once instead of holding the shutdown, and
// a read that arrives afterwards is refused as transient.
func TestSourceStreamEndsWhenShutdownBegins(t *testing.T) {
	f := newStreamFixture(t)
	lifetime, shutdown := context.WithCancel(t.Context())
	defer shutdown()
	f.openFixture.server.opts.Lifetime = lifetime //nolint:fatcontext // the server's lifetime under test, set once
	grant := f.bigGrant(nodeA)
	shutdownBefore := testutil.ToFloat64(nodeSourceEnds.WithLabelValues(sourceOutcomeShutdown))

	resp := f.open(nodeA, f.keyA, grant)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream = %d", resp.StatusCode)
	}
	select {
	case <-f.done:
		t.Fatal("the stream returned before shutdown")
	case <-time.After(100 * time.Millisecond):
	}
	shutdown()
	f.waitDone("shutdown")
	if got := testutil.ToFloat64(nodeSourceEnds.WithLabelValues(sourceOutcomeShutdown)) - shutdownBefore; got != 1 {
		t.Fatalf("shutdown outcomes %v, want 1", got)
	}
	if n, err := io.Copy(io.Discard, resp.Body); err == nil && n == bigSourceBytes {
		t.Fatal("the cut stream delivered the whole source")
	}
	if late := f.open(nodeA, f.keyA, grant); late.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a read after shutdown began = %d, want 503", late.StatusCode)
	}
}

// The bytes counter advances while a stream runs, not only when it ends.
func TestSourceBytesCountWhileStreaming(t *testing.T) {
	f := newStreamFixture(t)
	grant := f.bigGrant(nodeA)
	counter := nodeSourceBytes.WithLabelValues(strconv.Itoa(nodeA))
	before := testutil.ToFloat64(counter)

	resp := f.open(nodeA, f.keyA, grant)
	if _, err := io.CopyN(io.Discard, resp.Body, 32<<20); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for testutil.ToFloat64(counter)-before < 16<<20 {
		if time.Now().After(deadline) {
			t.Fatalf("counted %v bytes after the client read 32 MiB", testutil.ToFloat64(counter)-before)
		}
		time.Sleep(10 * time.Millisecond) //nolint:forbidigo // polls a Prometheus counter, which has no notification to wait on
	}
	select {
	case <-f.done:
		t.Fatal("the stream ended before the check")
	default:
	}
	_ = resp.Body.Close()
	f.waitDone("the closed stream")
}
