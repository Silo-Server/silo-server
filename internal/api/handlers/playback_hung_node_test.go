package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

// hungNode is a transcode node that serves segments until it is frozen, then
// behaves like a process stopped with SIGSTOP: connections still open and
// requests are still delivered, but nothing is answered, health included.
type hungNode struct {
	server   *httptest.Server
	freezeAt string // the first segment request that finds the node frozen

	mu      sync.Mutex
	frozen  bool
	arrived []string // "METHOD path" of every request, in arrival order
	// arrival is closed and replaced on every request, waking waitForRequest.
	arrival  chan struct{}
	released chan struct{}
}

func newHungNode(t *testing.T, freezeAt string) *hungNode {
	t.Helper()
	node := &hungNode{freezeAt: freezeAt, arrival: make(chan struct{}), released: make(chan struct{})}
	node.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		segment := strings.TrimPrefix(r.URL.Path, "/transcode/"+rehomeTestTransport+"/segment/")
		node.mu.Lock()
		node.arrived = append(node.arrived, r.Method+" "+r.URL.Path)
		close(node.arrival)
		node.arrival = make(chan struct{})
		if segment == node.freezeAt {
			node.frozen = true
		}
		frozen := node.frozen
		node.mu.Unlock()
		if frozen {
			select {
			case <-node.released:
			case <-r.Context().Done():
			}
			return
		}
		switch {
		case r.URL.Path == "/api/v1/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"ok"}`)
		case r.Method == http.MethodGet && segment != r.URL.Path:
			_, _ = io.WriteString(w, "hung-node:"+segment)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(func() {
		close(node.released)
		node.server.Close()
	})
	return node
}

// waitForRequest waits until a request matching method and path has reached
// the node, frozen or not.
func (n *hungNode) waitForRequest(t *testing.T, method, path string) {
	t.Helper()
	want := method + " " + path
	deadline := time.After(10 * time.Second)
	for {
		n.mu.Lock()
		for _, seen := range n.arrived {
			if seen == want {
				n.mu.Unlock()
				return
			}
		}
		arrival := n.arrival
		n.mu.Unlock()
		select {
		case <-arrival:
		case <-deadline:
			n.mu.Lock()
			seen := append([]string(nil), n.arrived...)
			n.mu.Unlock()
			t.Fatalf("%s never reached the node; it saw %v", want, seen)
		}
	}
}

// serveRehomeFixture mounts the native segment route on a real HTTP server, so
// requests reach the relay the way a player's do: over a connection whose
// context ends only when the client gives up.
func serveRehomeFixture(t *testing.T, f *rehomeFixture) *httptest.Server {
	t.Helper()
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := apimw.SetClaims(r.Context(), &auth.Claims{UserID: 1, Role: "user", TokenType: auth.TokenTypeAccess})
			next.ServeHTTP(w, r.WithContext(apimw.SetProfileID(ctx, "profile-1")))
		})
	})
	router.Get("/api/v2/playback/transcode/{session_id}/segment/{name}", f.handler.HandleGetTranscodeSegment)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server
}

// fetchSegment requests a segment the way a player does, giving up after the
// hls.js load timeout. It returns the status (0 when the client gave up), the
// body, and how long the answer took.
func fetchSegment(t *testing.T, api *httptest.Server, f *rehomeFixture, name string) (int, string, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		api.URL+"/api/v2/playback/transcode/"+f.session.ID+"/segment/"+name+"?st="+f.token, nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	resp, err := api.Client().Do(req)
	if err != nil {
		return 0, err.Error(), time.Since(started)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), time.Since(started)
}

// TestRelayMovesTranscodeOffHungNode covers issue #2296 end to end with the
// production liveness timings: the transcode node freezes mid-stream without
// closing its connections. The request that finds it frozen must be moved to
// the healthy node within the 10-second failover bar and answered from there,
// numbering must continue at the requested segment, the frozen node must stop
// being selected, and it must be told to drop the transport.
func TestRelayMovesTranscodeOffHungNode(t *testing.T) {
	hung := newHungNode(t, "seg_00003.ts")
	healthy := newFakeRehomeNode(t, false)
	f := newRehomeFixture(t, []*nodepool.Node{
		pooledNode(1, hung.server.URL),
		pooledNode(2, healthy.server.URL),
	}, "ffmpeg")
	api := serveRehomeFixture(t, f)

	for _, name := range []string{"seg_00000.ts", "seg_00001.ts", "seg_00002.ts"} {
		if status, body, _ := fetchSegment(t, api, f, name); status != http.StatusOK || body != "hung-node:"+name {
			t.Fatalf("%s before the hang: status = %d, body = %q", name, status, body)
		}
	}

	status, body, took := fetchSegment(t, api, f, "seg_00003.ts")
	if status != http.StatusOK || body != "moved:seg_00003.ts" {
		t.Fatalf("segment after the hang: status = %d, body = %q after %v; want it served by the healthy node", status, body, took)
	}
	if took > 10*time.Second {
		t.Fatalf("failover took %v, want under 10s", took)
	}

	start := healthy.lastStart()
	if start.SessionID != rehomeTestTransport || start.StartSegmentNumber != 3 || start.SeekSeconds != 6 {
		t.Fatalf("healthy node started %q at segment %d (%.1fs), want %q at segment 3 (6.0s)",
			start.SessionID, start.StartSegmentNumber, start.SeekSeconds, rehomeTestTransport)
	}
	session, err := f.sessions.GetSession(f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if session.TranscodeNodeURL != healthy.server.URL || session.RoutingExecutionNodeID != 2 {
		t.Fatalf("session runs on %q (node %d), want the healthy node", session.TranscodeNodeURL, session.RoutingExecutionNodeID)
	}
	if f.planner.TranscodeNodeHealthy(hung.server.URL) {
		t.Fatal("the hung node is still selectable")
	}
	hung.waitForRequest(t, http.MethodDelete, "/transcode/"+rehomeTestTransport)

	if status, body, _ := fetchSegment(t, api, f, "seg_00004.ts"); status != http.StatusOK || body != "moved:seg_00004.ts" {
		t.Fatalf("next segment: status = %d, body = %q", status, body)
	}
	if got := healthy.starts.Load(); got != 1 {
		t.Fatalf("starts on the healthy node = %d, want 1", got)
	}
}

// TestRelayFailsPromptlyWhenNoNodeReplacesHungNode covers a deployment with
// nowhere to move: worker_only with a single node. The relay must answer 502
// once the node is found hung instead of holding the request until the player
// gives up.
func TestRelayFailsPromptlyWhenNoNodeReplacesHungNode(t *testing.T) {
	hung := newHungNode(t, "seg_00003.ts")
	f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, hung.server.URL)}, writePlaybackTestFFmpegSleep(t, "30"))
	previous := f.handler.PlaybackConfig
	f.handler.PlaybackConfig = func() config.PlaybackConfig {
		cfg := previous()
		cfg.Routing = config.DefaultPlaybackRoutingPolicy()
		cfg.Routing.VideoTranscodeExecution = config.PlaybackExecutionWorkerOnly
		return cfg
	}
	api := serveRehomeFixture(t, f)

	status, body, took := fetchSegment(t, api, f, "seg_00003.ts")
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %q after %v; want 502", status, body, took)
	}
	if took > 10*time.Second {
		t.Fatalf("the relay answered after %v, want under 10s", took)
	}
	if f.handler.tm.GetTranscodeSession(f.session.ID) != nil {
		t.Fatal("worker_only transcode was moved onto the API")
	}
	if f.planner.TranscodeNodeHealthy(hung.server.URL) {
		t.Fatal("the hung node is still selectable")
	}
}

// TestRelayWaitsOnSlowLiveNode pins the other side: a node that takes longer
// than the liveness grace to answer, as during a rebuild, but still answers its
// health endpoint, is waited on and keeps the session.
func TestRelayWaitsOnSlowLiveNode(t *testing.T) {
	// The segment is answered only once the watch has probed twice, after the
	// grace and one interval, so the relay has had every chance to give up.
	answer := make(chan struct{})
	var probes atomic.Int32
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" {
			_, _ = io.WriteString(w, `{"status":"ok"}`)
			if probes.Add(1) == 2 {
				close(answer)
			}
			return
		}
		select {
		case <-answer:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "slow:"+strings.TrimPrefix(r.URL.Path, "/transcode/"+rehomeTestTransport+"/segment/"))
	}))
	t.Cleanup(slow.Close)
	spare := newFakeRehomeNode(t, false)
	f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, slow.URL), pooledNode(2, spare.server.URL)}, "ffmpeg")
	api := serveRehomeFixture(t, f)

	status, body, _ := fetchSegment(t, api, f, "seg_00003.ts")
	if status != http.StatusOK || body != "slow:seg_00003.ts" {
		t.Fatalf("status = %d, body = %q; want the slow node's answer", status, body)
	}
	if got := spare.starts.Load(); got != 0 {
		t.Fatalf("starts on the spare node = %d, want 0", got)
	}
	session, err := f.sessions.GetSession(f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if session.TranscodeNodeURL != slow.URL || !f.planner.TranscodeNodeHealthy(slow.URL) {
		t.Fatalf("session moved to %q or the live node was marked unhealthy", session.TranscodeNodeURL)
	}
}
