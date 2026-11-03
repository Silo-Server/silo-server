package transcodeproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

// shortLiveness shortens the liveness timings for one test.
func shortLiveness(t *testing.T) livenessTiming {
	t.Helper()
	timing := livenessTiming{grace: 100 * time.Millisecond, probeTimeout: 200 * time.Millisecond, interval: 100 * time.Millisecond}
	liveness.Store(&timing)
	t.Cleanup(func() {
		liveness.Store(nil)
		notAnswering.Clear()
	})
	return timing
}

// frozenNode accepts connections and never answers anything, health included,
// as a stopped process does. It counts health probes.
func frozenNode(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var probes atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" {
			probes.Add(1)
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})
	return server, &probes
}

func nodeGet(t *testing.T, ctx context.Context, nodeURL string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nodeURL+"/transcode/t/segment/seg_00001.ts", nil)
	if err != nil {
		t.Fatal(err)
	}
	return DoNode(NodeClient(), req, nodeURL, "stream")
}

// boundedContext bounds a call whose node answers only once probes arrive, so a
// watch that stops probing fails the test instead of hanging it.
func boundedContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestDoNodeFailsCallToHungNode(t *testing.T) {
	timing := shortLiveness(t)
	node, _ := frozenNode(t)

	started := time.Now()
	resp, err := nodeGet(t, context.Background(), node.URL)
	took := time.Since(started)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, nodepool.ErrNodeNotAnswering) || !nodepool.IsConnectFailure(err) {
		t.Fatalf("err = %v, want ErrNodeNotAnswering counted as a connect failure", err)
	}
	if bound := timing.grace + 2*timing.probeTimeout + time.Second; took > bound {
		t.Fatalf("hung node detected after %v, want within %v", took, bound)
	}
	if _, known := notAnswering.Load(nodepool.NormalizeNodeURL(node.URL)); !known {
		t.Fatal("a node found hung is not remembered for its other calls")
	}
}

func TestDoNodeWaitsOnSlowLiveNode(t *testing.T) {
	shortLiveness(t)
	var probes atomic.Int32
	probedAlive := make(chan struct{})
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" {
			if probes.Add(1) == 3 {
				close(probedAlive)
			}
			w.WriteHeader(http.StatusServiceUnavailable) // any answer means alive
			return
		}
		// Answer only after several probes have found the node alive.
		select {
		case <-probedAlive:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "segment")
	}))
	t.Cleanup(node.Close)

	resp, err := nodeGet(t, boundedContext(t), node.URL)
	if err != nil {
		t.Fatalf("slow live node failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "segment" {
		t.Fatalf("body = %q", body)
	}
	if got := probes.Load(); got < 2 {
		t.Fatalf("probes = %d, want the node probed while the call waited", got)
	}
}

// bodyNode controls the response and health transports without real sockets.
// Closing the request context interrupts the body just as net/http does.
func bodyNode(t *testing.T, health func(context.Context) error) (*http.Client, *io.PipeWriter) {
	t.Helper()
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = reader.Close() })
	previous := probeClient
	probeClient = &http.Client{Transport: livenessRoundTrip(func(req *http.Request) (*http.Response, error) {
		if err := health(req.Context()); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})}
	t.Cleanup(func() { probeClient = previous })
	client := &http.Client{Transport: livenessRoundTrip(func(req *http.Request) (*http.Response, error) {
		stop := context.AfterFunc(req.Context(), func() { _ = reader.CloseWithError(req.Context().Err()) })
		t.Cleanup(func() { stop() })
		return &http.Response{StatusCode: http.StatusOK, Body: reader, Header: make(http.Header)}, nil
	})}
	return client, writer
}

type livenessRoundTrip func(*http.Request) (*http.Response, error)

func (f livenessRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDoNodeFailsHungBodyRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		timing := shortLiveness(t)
		client, writer := bodyNode(t, func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		})
		go func() { _, _ = io.WriteString(writer, "partial") }()
		ctx, cancel := context.WithTimeout(context.Background(), timing.grace+2*timing.probeTimeout+time.Second)
		defer cancel()
		const nodeURL = "http://body-node.test"
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, nodeURL+"/segment", nil)
		resp, err := DoNode(client, req, nodeURL, "stream")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		started := time.Now()
		body, err := io.ReadAll(resp.Body)
		if string(body) != "partial" || !errors.Is(err, nodepool.ErrNodeNotAnswering) || !nodepool.IsConnectFailure(err) {
			t.Fatalf("body = %q, err = %v, want partial body and ErrNodeNotAnswering counted as a connect failure", body, err)
		}
		if took, bound := time.Since(started), timing.grace+2*timing.probeTimeout; took > bound {
			t.Fatalf("hung body detected after %v, want within %v", took, bound)
		}
		if _, known := notAnswering.Load(nodepool.NormalizeNodeURL(nodeURL)); !known {
			t.Fatal("a node with a hung body is not remembered")
		}
	})
}

func TestDoNodeWaitsOnSlowLiveBody(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shortLiveness(t)
		var probes atomic.Int32
		probedAlive := make(chan struct{})
		client, writer := bodyNode(t, func(context.Context) error {
			if probes.Add(1) == 3 {
				close(probedAlive)
			}
			return nil
		})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		go func() {
			_, _ = io.WriteString(writer, "partial")
			select {
			case <-probedAlive:
				_, _ = io.WriteString(writer, "-complete")
			case <-ctx.Done():
			}
			_ = writer.Close()
		}()
		const nodeURL = "http://body-node.test"
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, nodeURL+"/segment", nil)
		resp, err := DoNode(client, req, nodeURL, "stream")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil || string(body) != "partial-complete" {
			t.Fatalf("body = %q, err = %v, want complete body from the slow live node", body, err)
		}
		if _, known := notAnswering.Load(nodepool.NormalizeNodeURL(nodeURL)); known {
			t.Fatal("a node answering health while its body waits is remembered as hung")
		}
	})
}

func TestDoNodeDoesNotProbeBetweenBodyReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		timing := shortLiveness(t)
		var probes atomic.Int32
		probed := make(chan struct{})
		client, writer := bodyNode(t, func(context.Context) error {
			if probes.Add(1) == 1 {
				close(probed)
			}
			return nil
		})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		go func() {
			select {
			case <-probed:
			case <-ctx.Done():
				return
			}
			_, _ = io.WriteString(writer, "partial-complete")
			_ = writer.Close()
		}()
		const nodeURL = "http://body-node.test"
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, nodeURL+"/segment", nil)
		resp, err := DoNode(client, req, nodeURL, "stream")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		partial := make([]byte, len("partial"))
		_, readErr := io.ReadFull(resp.Body, partial)
		if got := probes.Load(); got == 0 {
			t.Fatalf("health probes during a stalled body read = %d, want at least 1 (read error: %v)", got, readErr)
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		synctest.Wait()
		probesAfterRead := probes.Load()
		// Advance virtual time while no read is pending, as when the relay
		// writes to a slow viewer. synctest.Wait settles every ready watcher.
		time.Sleep(timing.grace + 2*timing.probeTimeout + timing.interval) //nolint:forbidigo // Inside synctest this advances virtual time to prove an idle reader starts no watch.
		synctest.Wait()
		if got := probes.Load(); got != probesAfterRead {
			t.Fatalf("health probes while no body read is pending = %d, want unchanged at %d", got, probesAfterRead)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil || string(body) != "-complete" {
			t.Fatalf("remaining body = %q, err = %v", body, err)
		}
	})
}

func TestDoNodeLeavesCallerCancellationAlone(t *testing.T) {
	shortLiveness(t)
	node, _ := frozenNode(t)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	resp, err := nodeGet(t, ctx, node.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil || errors.Is(err, nodepool.ErrNodeNotAnswering) || nodepool.IsConnectFailure(err) {
		t.Fatalf("err = %v, want the caller's own deadline, not a hung node", err)
	}
}

// TestDoNodeProbesKnownHungNodeAtOnce pins the memory: once a node is found
// hung, later calls skip the grace, and concurrent calls share one probe.
func TestDoNodeProbesKnownHungNodeAtOnce(t *testing.T) {
	timing := shortLiveness(t)
	timing.grace = 5 * time.Second
	liveness.Store(&timing)
	node, probes := frozenNode(t)
	notAnswering.Store(nodepool.NormalizeNodeURL(node.URL), struct{}{})

	const calls = 4
	errs := make(chan error, calls)
	started := time.Now()
	for range calls {
		go func() {
			resp, err := nodeGet(t, context.Background(), node.URL)
			if resp != nil {
				_ = resp.Body.Close()
			}
			errs <- err
		}()
	}
	for range calls {
		if err := <-errs; !errors.Is(err, nodepool.ErrNodeNotAnswering) {
			t.Fatalf("err = %v, want ErrNodeNotAnswering", err)
		}
	}
	if took := time.Since(started); took > timing.grace {
		t.Fatalf("known hung node took %v, want no grace wait", took)
	}
	if got := probes.Load(); got != 1 {
		t.Fatalf("health probes = %d, want one shared by every call", got)
	}
}

func TestDoNodeForgetsNodeThatAnswersAgain(t *testing.T) {
	t.Run("body", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			shortLiveness(t)
			var probes atomic.Int32
			client, writer := bodyNode(t, func(context.Context) error {
				probes.Add(1)
				// An inconclusive probe cannot clear the remembered mark.
				return &net.DNSError{Err: "local lookup failed", Name: "body-node.test"}
			})
			const nodeURL = "http://body-node.test"
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, nodeURL+"/segment", nil)
			resp, err := DoNode(client, req, nodeURL, "stream")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			key := nodepool.NormalizeNodeURL(nodeURL)
			// Another call can remember the node after these headers arrive.
			notAnswering.Store(key, struct{}{})
			for range 3 {
				read := make(chan error, 1)
				go func() {
					var data [1]byte
					_, err := io.ReadFull(resp.Body, data[:])
					if err == nil && data[0] != 'x' {
						t.Errorf("body byte = %q, want x", data[0])
					}
					read <- err
				}()
				// Settle an immediate watch before supplying the next byte.
				// No virtual time passes, so these reads stay within the grace.
				synctest.Wait()
				if _, err := io.WriteString(writer, "x"); err != nil {
					t.Fatal(err)
				}
				if err := <-read; err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
			}
			if got := probes.Load(); got > 1 {
				t.Fatalf("health probes across fast body reads = %d, want at most 1", got)
			}
			if _, known := notAnswering.Load(key); known {
				t.Fatal("a node that sent body data is still remembered as hung")
			}
		})
	})
	t.Run("probe", func(t *testing.T) {
		shortLiveness(t)
		probed := make(chan struct{})
		var once sync.Once
		node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/health" {
				once.Do(func() { close(probed) })
				_, _ = io.WriteString(w, "{}")
				return
			}
			// Answer the call only once the node has been probed, as a node that
			// recovered while the call waited.
			select {
			case <-probed:
			case <-r.Context().Done():
				return
			}
			_, _ = io.WriteString(w, "ok")
		}))
		t.Cleanup(node.Close)
		key := nodepool.NormalizeNodeURL(node.URL)
		notAnswering.Store(key, struct{}{})

		resp, err := nodeGet(t, boundedContext(t), node.URL)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if _, known := notAnswering.Load(key); known {
			t.Fatal("a node that answered its probe is still remembered as hung")
		}
	})
}

// TestDoNodeKeepsCallWhenOneProbeIsLost pins the retry: a live node whose first
// probe goes unanswered (a lost packet) but answers the retry keeps its call.
func TestDoNodeKeepsCallWhenOneProbeIsLost(t *testing.T) {
	shortLiveness(t)
	var probes atomic.Int32
	retried := make(chan struct{})
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" {
			switch probes.Add(1) {
			case 1:
				<-r.Context().Done()
				return
			case 2:
				close(retried)
			}
			_, _ = io.WriteString(w, "{}")
			return
		}
		// Answer only once the lost probe has been retried.
		select {
		case <-retried:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "segment")
	}))
	t.Cleanup(node.Close)

	resp, err := nodeGet(t, boundedContext(t), node.URL)
	if err != nil {
		t.Fatalf("live node with one lost probe failed: %v", err)
	}
	_ = resp.Body.Close()
	if got := probes.Load(); got < 2 {
		t.Fatalf("probes = %d, want the lost probe retried", got)
	}
	if _, known := notAnswering.Load(nodepool.NormalizeNodeURL(node.URL)); known {
		t.Fatal("a node that answered the retry is remembered as hung")
	}
}

func TestProbeHealthTreatsLocalLookupFailureAsInconclusive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if got := probeHealth(ctx, "http://node.invalid"); got != probeInconclusive {
		t.Fatalf("verdict = %v, want inconclusive for a name this host cannot resolve", got)
	}
}

// TestProbeHealthTreatsStalledLookupAsInconclusive covers a resolver that
// stops answering on this host: the probe's deadline ends the lookup, and the
// transport reports only the deadline, with no *net.DNSError to recognize.
func TestProbeHealthTreatsStalledLookupAsInconclusive(t *testing.T) {
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	transport := newProbeTransport()
	transport.DialContext = (&net.Dialer{Resolver: resolver}).DialContext
	previous := probeClient
	probeClient = &http.Client{Transport: transport}
	t.Cleanup(func() { probeClient = previous })

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if got := probeHealth(ctx, "http://transcode-node.example"); got != probeInconclusive {
		t.Fatalf("verdict = %v, want inconclusive for a lookup this host could not finish", got)
	}
}

// TestWatchNodeLeavesMarkerToTheCall pins that a miss alone does not mark the
// node hung: the call marks it only if it was still waiting. A call answered
// while its watcher's last probe was missing must not leave the node marked,
// or the next call to the recovered node would accept a single miss.
func TestWatchNodeLeavesMarkerToTheCall(t *testing.T) {
	timing := shortLiveness(t)
	node, _ := frozenNode(t)
	called := make(chan struct{})
	watchNode(context.Background(), node.URL, timing, make(chan struct{}), func() { close(called) })
	select {
	case <-called:
	default:
		t.Fatal("watchNode returned without calling hung")
	}
	if _, known := notAnswering.Load(nodepool.NormalizeNodeURL(node.URL)); known {
		t.Fatal("watchNode marked the node hung although the call did not")
	}
}
