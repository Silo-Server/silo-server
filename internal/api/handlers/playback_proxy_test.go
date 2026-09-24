package handlers

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/transcodeproxy"
)

func TestProxyToTranscodeNodeAcknowledgesOnlyFullDownstreamResponse(t *testing.T) {
	const (
		body       = "complete segment"
		generation = "17"
	)
	tests := []struct {
		name        string
		rangeHeader string
		wantStatus  int
		wantAck     int32
	}{
		{name: "ordinary get", wantStatus: http.StatusOK, wantAck: 1},
		{name: "whole-file range", rangeHeader: "bytes=0-", wantStatus: http.StatusPartialContent, wantAck: 1},
		{name: "partial range", rangeHeader: "bytes=1-", wantStatus: http.StatusPartialContent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var acknowledgements atomic.Int32
			seenRange := make(chan string, 1)
			node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					if r.Header.Get(transcodeproxy.RequestHeader) != "1" {
						t.Error("segment request omitted proxy marker")
					}
					seenRange <- r.Header.Get("Range")
					w.Header().Set(transcodeproxy.GenerationHeader, generation)
					http.ServeContent(w, r, "seg_00007.ts", time.Time{}, strings.NewReader(body))
				case http.MethodPost:
					if r.Header.Get(transcodeproxy.GenerationHeader) != generation {
						t.Errorf("ack generation = %q, want %q", r.Header.Get(transcodeproxy.GenerationHeader), generation)
					}
					acknowledgements.Add(1)
					w.WriteHeader(http.StatusNoContent)
				default:
					http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
				}
			}))
			defer node.Close()

			handler := &PlaybackHandler{JWTSecret: "proxy-test-secret"}
			req := withPlaybackRouteParam(
				httptest.NewRequest(http.MethodGet, "/playback/transcode/public/segment/seg_00007.ts", nil),
				"session_id",
				"public",
			)
			req.Header.Set("Range", tt.rangeHeader)
			rr := httptest.NewRecorder()
			handler.proxyToTranscodeNode(rr, req, node.URL, "/transcode/remote/segment/seg_00007.ts")

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %q", rr.Code, tt.wantStatus, rr.Body.String())
			}
			if got := <-seenRange; got != tt.rangeHeader {
				t.Fatalf("forwarded Range = %q, want %q", got, tt.rangeHeader)
			}
			if got := rr.Header().Get(transcodeproxy.GenerationHeader); got != "" {
				t.Fatalf("internal generation leaked downstream: %q", got)
			}
			if got := acknowledgements.Load(); got != tt.wantAck {
				t.Fatalf("acknowledgements = %d, want %d", got, tt.wantAck)
			}
		})
	}
}

func TestProxyToTranscodeNodeDoesNotAcknowledgeFailedDownstreamWrite(t *testing.T) {
	const body = "complete segment"
	var acknowledgements atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			acknowledgements.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set(transcodeproxy.GenerationHeader, "21")
		http.ServeContent(w, r, "seg_00007.ts", time.Time{}, strings.NewReader(body))
	}))
	defer node.Close()

	handler := &PlaybackHandler{JWTSecret: "proxy-test-secret"}
	req := withPlaybackRouteParam(
		httptest.NewRequest(http.MethodGet, "/playback/transcode/public/segment/seg_00007.ts", nil),
		"session_id",
		"public",
	)
	w := &failingProxyResponseWriter{header: make(http.Header), remaining: 5}
	handler.proxyToTranscodeNode(w, req, node.URL, "/transcode/remote/segment/seg_00007.ts")

	if got := acknowledgements.Load(); got != 0 {
		t.Fatalf("failed downstream transfer produced %d acknowledgement(s)", got)
	}
}

type failingProxyResponseWriter struct {
	header    http.Header
	status    int
	remaining int
}

func (w *failingProxyResponseWriter) Header() http.Header { return w.header }

func (w *failingProxyResponseWriter) WriteHeader(status int) { w.status = status }

func (w *failingProxyResponseWriter) Write(p []byte) (int, error) {
	if w.remaining <= 0 {
		return 0, io.ErrClosedPipe
	}
	n := min(len(p), w.remaining)
	w.remaining -= n
	return n, io.ErrClosedPipe
}

// TestProxyToTranscodeNodeReusesNodeConnections guards the API→node relay pool.
// The relay holds a node connection while it streams a segment to the viewer,
// so many sessions on one node keep many connections in flight at once. A pool
// that keeps only two idle connections per host (http.DefaultClient) closes the
// rest after every wave and dials them again for the next segment and its ack.
func TestProxyToTranscodeNodeReusesNodeConnections(t *testing.T) {
	const (
		concurrency = 20
		waves       = 5
	)
	handler := &PlaybackHandler{JWTSecret: "proxy-test-secret"}

	t.Run("sequential", func(t *testing.T) {
		node := newConnCountingTranscodeNode(t, false)
		for range concurrency {
			relaySegment(t, handler, node.server.URL)
		}
		if got := node.acks.Load(); got != concurrency {
			t.Fatalf("acknowledgements = %d, want %d", got, concurrency)
		}
		t.Logf("new node connections for %d sequential segments and acks: %d", concurrency, node.dials.Load())
		if got := node.dials.Load(); got != 1 {
			t.Fatalf("new node connections = %d, want 1", got)
		}
	})

	t.Run("concurrent waves", func(t *testing.T) {
		node := newConnCountingTranscodeNode(t, true)
		for range waves {
			var wg sync.WaitGroup
			for range concurrency {
				wg.Go(func() { relaySegment(t, handler, node.server.URL) })
			}
			node.releaseWave(t, concurrency)
			wg.Wait()
		}
		if got := node.acks.Load(); got != concurrency*waves {
			t.Fatalf("acknowledgements = %d, want %d", got, concurrency*waves)
		}
		t.Logf("new node connections for %d waves of %d concurrent segments and acks: %d", waves, concurrency, node.dials.Load())
		if got := node.dials.Load(); got > concurrency {
			t.Fatalf("new node connections = %d, want at most %d (one per concurrent relay)", got, concurrency)
		}
	})
}

func relaySegment(t *testing.T, handler *PlaybackHandler, nodeURL string) {
	t.Helper()
	req := withPlaybackRouteParam(
		httptest.NewRequest(http.MethodGet, "/playback/transcode/public/segment/seg_00007.ts", nil),
		"session_id",
		"public",
	)
	rr := httptest.NewRecorder()
	handler.proxyToTranscodeNode(rr, req, nodeURL, "/transcode/remote/segment/seg_00007.ts")
	if rr.Code != http.StatusOK {
		t.Errorf("relay status = %d, want 200", rr.Code)
	}
}

// connCountingTranscodeNode is a transcode node that counts accepted TCP
// connections. With gate set, every segment request waits until the test
// releases its wave, so a wave's requests are all in flight at once.
type connCountingTranscodeNode struct {
	server  *httptest.Server
	dials   atomic.Int32
	acks    atomic.Int32
	arrived chan struct{}
	release chan struct{}
}

func newConnCountingTranscodeNode(t *testing.T, gate bool) *connCountingTranscodeNode {
	t.Helper()
	node := &connCountingTranscodeNode{}
	if gate {
		node.arrived = make(chan struct{})
		node.release = make(chan struct{})
	}
	done := make(chan struct{})
	node.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			node.acks.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if gate {
			select {
			case node.arrived <- struct{}{}:
			case <-done:
				return
			}
			select {
			case <-node.release:
			case <-done:
				return
			}
		}
		w.Header().Set(transcodeproxy.GenerationHeader, "7")
		http.ServeContent(w, r, "seg_00007.ts", time.Time{}, strings.NewReader("segment bytes"))
	}))
	node.server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			node.dials.Add(1)
		}
	}
	node.server.Start()
	t.Cleanup(node.server.Close)
	t.Cleanup(func() { close(done) })
	return node
}

// releaseWave waits until n segment requests are in flight, then lets them all
// respond.
func (n *connCountingTranscodeNode) releaseWave(t *testing.T, count int) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for range count {
		select {
		case <-n.arrived:
		case <-timeout:
			t.Fatal("segment requests did not reach the node")
		}
	}
	for range count {
		n.release <- struct{}{}
	}
}
