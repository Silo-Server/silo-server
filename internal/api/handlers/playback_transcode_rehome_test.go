package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

const rehomeTestTransport = "rehome-transport"

// fakeRehomeNode is a transcode node that accepts a start and then serves the
// started transport's segments.
type fakeRehomeNode struct {
	server  *httptest.Server
	starts  atomic.Int32
	release chan struct{} // when non-nil, a start waits for it to close

	mu       sync.Mutex
	lastSeen transcodenode.TranscodeStartRequest
}

func newFakeRehomeNode(t *testing.T, gated bool) *fakeRehomeNode {
	t.Helper()
	node := &fakeRehomeNode{}
	if gated {
		node.release = make(chan struct{})
	}
	node.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			var req transcodenode.TranscodeStartRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			node.mu.Lock()
			node.lastSeen = req
			node.mu.Unlock()
			node.starts.Add(1)
			if node.release != nil {
				<-node.release
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(transcodenode.TranscodeStartResponse{SessionID: req.SessionID, Status: "started"})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/transcode/"+rehomeTestTransport+"/segment/"):
			_, _ = io.WriteString(w, "moved:"+strings.TrimPrefix(r.URL.Path, "/transcode/"+rehomeTestTransport+"/segment/"))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(node.server.Close)
	return node
}

func (n *fakeRehomeNode) lastStart() transcodenode.TranscodeStartRequest {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.lastSeen
}

// unreachableNodeURL returns a URL nothing listens on, so a dial is refused.
func unreachableNodeURL(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	return url
}

type rehomeFixture struct {
	handler  *PlaybackHandler
	sessions *playback.SessionManager
	planner  *nodepool.Planner
	session  *playback.Session
	token    string
	deadURL  string
}

// newRehomeFixture starts a native API-relayed transcode on nodes[0] and pools
// every node in nodes.
func newRehomeFixture(t *testing.T, nodes []*nodepool.Node, ffmpegPath string) *rehomeFixture {
	t.Helper()
	pool := nodepool.NewTranscodePool()
	pool.SetNodes(nodes)
	planner := nodepool.NewPlanner(nodepool.NewProxyPool(), pool)

	sessions := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(sessions)
	handler.JWTSecret = "rehome-secret"
	handler.NodePlanner = planner
	handler.PlaybackConfig = playbackTestConfig(ffmpegPath, t.TempDir())

	session, err := sessions.StartSession(1, "profile-1", 42, playback.PlayTranscode, false)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	t.Cleanup(func() {
		if ts := handler.tm.GetTranscodeSession(session.ID); ts != nil {
			_ = ts.Close()
		}
	})
	deadURL := nodes[0].URL
	if err := sessions.SetTranscodeRoute(session.ID, playback.TranscodeRoute{NodeURL: deadURL, TransportID: rehomeTestTransport}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.SetNodeRoutingAssignment(session.ID, playback.NodeRoutingAssignment{
		Workload: string(noderouting.WorkloadVideoTranscode), Execution: string(noderouting.ExecutionTranscode),
		ExecutionNodeID: nodes[0].ID, ExecutionNodeURL: deadURL, Egress: string(noderouting.EgressAPI),
	}); err != nil {
		t.Fatal(err)
	}
	card := playback.NewRecipeCard(1, "profile-1", 42, deadURL, playback.TranscodeOpts{
		SessionID: session.ID, TranscodeTransportID: rehomeTestTransport, InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
		AudioTrackIndex: -1, SubtitleTrackIndex: -1, TotalDuration: 600,
	})
	card.RoutingWorkload = string(noderouting.WorkloadVideoTranscode)
	card.RoutingExecution = string(noderouting.ExecutionTranscode)
	card.RoutingExecutionNodeID = nodes[0].ID
	card.RoutingEgress = string(noderouting.EgressAPI)
	token := handler.signSessionToken(card, false)
	if token == "" {
		t.Fatal("sign stream token returned empty")
	}
	return &rehomeFixture{handler: handler, sessions: sessions, planner: planner, session: session, token: token, deadURL: deadURL}
}

func (f *rehomeFixture) segment(name string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	f.handler.HandleGetTranscodeSegment(rr, playbackTestRequest(http.MethodGet,
		"/api/v2/playback/transcode/"+f.session.ID+"/segment/"+name+"?st="+f.token, nil,
		map[string]string{"session_id": f.session.ID, "name": name}))
	return rr
}

func pooledNode(id int, url string) *nodepool.Node {
	return &nodepool.Node{ID: id, Name: "node", Type: "transcode", URL: url, Enabled: true, Healthy: true}
}

// TestRelayMovesTranscodeOffUnreachableNodeToAnotherNode covers the reported
// failure: the node died, so the relay's connection is refused. The transcode
// must restart on the healthy node at the requested segment, the request must
// be served from it, and the dead node must stop being selected.
func TestRelayMovesTranscodeOffUnreachableNodeToAnotherNode(t *testing.T) {
	healthy := newFakeRehomeNode(t, false)
	f := newRehomeFixture(t, []*nodepool.Node{
		pooledNode(1, unreachableNodeURL(t)),
		pooledNode(2, healthy.server.URL),
	}, "ffmpeg")

	rr := f.segment("seg_00003.ts")
	if rr.Code != http.StatusOK || rr.Body.String() != "moved:seg_00003.ts" {
		t.Fatalf("status = %d, body = %q; want the segment served by the healthy node", rr.Code, rr.Body.String())
	}

	start := healthy.lastStart()
	if start.SessionID != rehomeTestTransport {
		t.Fatalf("started transport %q, want the session's transport %q", start.SessionID, rehomeTestTransport)
	}
	if start.StartSegmentNumber != 3 || start.SeekSeconds != 6 {
		t.Fatalf("started at segment %d (%.1fs), want segment 3 (6.0s) so numbering continues",
			start.StartSegmentNumber, start.SeekSeconds)
	}
	if start.InputPath != "/media/movie.mkv" || start.TargetCodecVideo != "h264" || !start.RequireReady {
		t.Fatalf("start request lost the recipe: %+v", start)
	}

	session, err := f.sessions.GetSession(f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if session.TranscodeNodeURL != healthy.server.URL || session.TranscodeTransportID != rehomeTestTransport {
		t.Fatalf("session route = %q/%q, want the healthy node with the same transport", session.TranscodeNodeURL, session.TranscodeTransportID)
	}
	if session.RoutingExecutionNodeID != 2 || session.RoutingEgress != string(noderouting.EgressAPI) {
		t.Fatalf("routing = node %d egress %q, want node 2 behind the API", session.RoutingExecutionNodeID, session.RoutingEgress)
	}
	if f.planner.TranscodeNodeHealthy(f.deadURL) {
		t.Fatal("the unreachable node is still selectable")
	}

	// The next segment goes straight to the new node, with no second start.
	if rr := f.segment("seg_00004.ts"); rr.Code != http.StatusOK || rr.Body.String() != "moved:seg_00004.ts" {
		t.Fatalf("next segment status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if got := healthy.starts.Load(); got != 1 {
		t.Fatalf("starts = %d, want 1", got)
	}
}

// TestRelayMovesTokenlessTranscodeFromStoredRecipe covers a session whose URLs
// carry no stream token: the recipe comes from the node recipe store, and the
// stored card follows the transport to its new node so that node can rebuild
// it after its own restart.
func TestRelayMovesTokenlessTranscodeFromStoredRecipe(t *testing.T) {
	healthy := newFakeRehomeNode(t, false)
	f := newRehomeFixture(t, []*nodepool.Node{
		pooledNode(1, unreachableNodeURL(t)),
		pooledNode(2, healthy.server.URL),
	}, "ffmpeg")
	stored, _ := verifiedStreamCardFromToken(f.token, f.session.ID, f.handler.JWTSecret)
	store := newSharedRecipeStore(*stored)
	f.handler.NodeRecipeStore = store

	rr := httptest.NewRecorder()
	f.handler.HandleGetTranscodeSegment(rr, playbackTestRequest(http.MethodGet,
		"/api/v2/playback/transcode/"+f.session.ID+"/segment/seg_00005.ts", nil,
		map[string]string{"session_id": f.session.ID, "name": "seg_00005.ts"}))
	if rr.Code != http.StatusOK || rr.Body.String() != "moved:seg_00005.ts" {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if got := healthy.lastStart().StartSegmentNumber; got != 5 {
		t.Fatalf("started at segment %d, want 5", got)
	}
	// The new card is written once the retire to the old node has finished.
	waitForStoredRecipeNode(t, store, healthy.server.URL, 2)
}

// TestRelayMovesTranscodeOffUnreachableNodeToThisServer covers the
// single-node deployment: with no other node, a policy that lets the API
// execute rebuilds the transcode here and serves it locally.
func TestRelayMovesTranscodeOffUnreachableNodeToThisServer(t *testing.T) {
	f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, unreachableNodeURL(t))}, writePlaybackTestFFmpegSleep(t, "30"))

	rr := httptest.NewRecorder()
	f.handler.HandleGetTranscodeManifest(rr, playbackTestRequest(http.MethodGet,
		"/api/v2/playback/transcode/"+f.session.ID+"/master.m3u8?st="+f.token, nil,
		map[string]string{"session_id": f.session.ID}))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "#EXTM3U") {
		t.Fatalf("status = %d, body = %q; want a manifest served by this server", rr.Code, rr.Body.String())
	}
	if f.handler.tm.GetTranscodeSession(f.session.ID) == nil {
		t.Fatal("no local transcode runs the moved session")
	}
	session, err := f.sessions.GetSession(f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if session.TranscodeNodeURL != "" || session.RoutingExecution != string(noderouting.ExecutionAPI) || session.RoutingExecutionNodeID != 0 {
		t.Fatalf("session still names node %q (execution %q, node %d)", session.TranscodeNodeURL, session.RoutingExecution, session.RoutingExecutionNodeID)
	}
}

// TestRelayDoesNotMoveTranscodeWhenWorkerOnlyHasNoOtherNode pins that the
// move never crosses a hard execution boundary: worker_only with no healthy
// node left answers the relay failure instead of transcoding on the API.
func TestRelayDoesNotMoveTranscodeWhenWorkerOnlyHasNoOtherNode(t *testing.T) {
	f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, unreachableNodeURL(t))}, writePlaybackTestFFmpegSleep(t, "30"))
	previous := f.handler.PlaybackConfig
	f.handler.PlaybackConfig = func() config.PlaybackConfig {
		cfg := previous()
		cfg.Routing = config.DefaultPlaybackRoutingPolicy()
		cfg.Routing.VideoTranscodeExecution = config.PlaybackExecutionWorkerOnly
		return cfg
	}

	if rr := f.segment("seg_00003.ts"); rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %q; want 502", rr.Code, rr.Body.String())
	}
	if f.handler.tm.GetTranscodeSession(f.session.ID) != nil {
		t.Fatal("worker_only transcode was moved onto the API")
	}
}

// TestRelayKeepsLiveNodeHTTPErrors pins the other side of the boundary: a node
// that answers is alive, so its HTTP error is relayed and nothing moves.
func TestRelayKeepsLiveNodeHTTPErrors(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "encoder exploded", http.StatusInternalServerError)
	}))
	t.Cleanup(failing.Close)
	spare := newFakeRehomeNode(t, false)
	f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, failing.URL), pooledNode(2, spare.server.URL)}, "ffmpeg")

	rr := f.segment("seg_00003.ts")
	if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), "encoder exploded") {
		t.Fatalf("status = %d, body = %q; want the node's own error relayed", rr.Code, rr.Body.String())
	}
	if got := spare.starts.Load(); got != 0 {
		t.Fatalf("starts on the spare node = %d, want 0", got)
	}
	session, err := f.sessions.GetSession(f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if session.TranscodeNodeURL != failing.URL {
		t.Fatalf("session moved to %q after an HTTP error", session.TranscodeNodeURL)
	}
	if !f.planner.TranscodeNodeHealthy(failing.URL) {
		t.Fatal("a node that answered was marked unhealthy")
	}
}

// TestRelayMovesEachSessionOnceUnderConcurrentRequests pins the pacing: every
// request that finds the node dead waits on one move, so the replacement node
// sees exactly one start however many segment requests arrive together.
func TestRelayMovesEachSessionOnceUnderConcurrentRequests(t *testing.T) {
	healthy := newFakeRehomeNode(t, true)
	f := newRehomeFixture(t, []*nodepool.Node{
		pooledNode(1, unreachableNodeURL(t)),
		pooledNode(2, healthy.server.URL),
	}, "ffmpeg")

	const requests = 8
	results := make(chan *httptest.ResponseRecorder, requests)
	for i := range requests {
		go func() { results <- f.segment("seg_0000" + string(rune('0'+i)) + ".ts") }()
	}
	deadline := time.Now().Add(10 * time.Second)
	for healthy.starts.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no start reached the healthy node")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(healthy.release)
	for range requests {
		rr := <-results
		if rr.Code != http.StatusOK || !strings.HasPrefix(rr.Body.String(), "moved:") {
			t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
		}
	}
	if got := healthy.starts.Load(); got != 1 {
		t.Fatalf("starts = %d, want exactly one move", got)
	}
}

// sharedRecipeStore is the node recipe store every node and the API share
// (Redis in production). It is safe for the move's background retire.
type sharedRecipeStore struct {
	mu    sync.Mutex
	cards map[string]playback.RecipeCard
}

func newSharedRecipeStore(card playback.RecipeCard) *sharedRecipeStore {
	return &sharedRecipeStore{cards: map[string]playback.RecipeCard{rehomeTestTransport: card}}
}

func (s *sharedRecipeStore) Enabled() bool { return true }

func (s *sharedRecipeStore) Get(_ context.Context, transportID string) (*playback.RecipeCard, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	card, ok := s.cards[transportID]
	if !ok {
		return nil, false
	}
	return &card, true
}

func (s *sharedRecipeStore) Put(_ context.Context, transportID string, card playback.RecipeCard) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cards[transportID] = card
	return nil
}

func (s *sharedRecipeStore) Delete(_ context.Context, transportID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cards, transportID)
	return nil
}

func waitForStoredRecipeNode(t *testing.T, store *sharedRecipeStore, nodeURL string, nodeID int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		card, ok := store.Get(context.Background(), rehomeTestTransport)
		if ok && card.TranscodeNodeURL == nodeURL && card.RoutingExecutionNodeID == nodeID {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("stored recipe = %+v (present %v), want one naming node %q (id %d)", card, ok, nodeURL, nodeID)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// seedNodeTransformations records the transformation inventory a node
// advertises, as a capability probe would.
func seedNodeTransformations(h *PlaybackHandler, nodeURL, videoRecipeVersion string) {
	h.v3NodeCapabilitiesMu.Lock()
	defer h.v3NodeCapabilitiesMu.Unlock()
	if h.v3NodeCapabilities == nil {
		h.v3NodeCapabilities = map[string]v3NodeCapabilityCache{}
	}
	h.v3NodeCapabilities[nodepool.NormalizeNodeURL(nodeURL)] = v3NodeCapabilityCache{
		transformations: []playback.TransformationV3{{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: videoRecipeVersion}},
		expiresAt:       time.Now().Add(time.Hour),
	}
}

// TestRelayMoveKeepsTheSessionsRecipeVersions pins that a move applies the
// capability filter of a fresh start: during a rolling upgrade, a node that
// advertises an older video recipe must not continue a transport encoded with
// the newer one.
func TestRelayMoveKeepsTheSessionsRecipeVersions(t *testing.T) {
	older := newFakeRehomeNode(t, false)
	current := newFakeRehomeNode(t, false)
	f := newRehomeFixture(t, []*nodepool.Node{
		pooledNode(1, unreachableNodeURL(t)),
		pooledNode(2, older.server.URL),
		pooledNode(3, current.server.URL),
	}, "ffmpeg")
	store := playback.NewMemoryPlanStoreV3()
	if err := store.SaveAttempt(context.Background(), playback.AttemptRecordV3{
		PlaybackAttemptID: "attempt-1", SessionID: f.session.ID, UserID: 1, CurrentPlanID: "plan-1",
		CurrentPlan: playback.PlanV3{PlanID: "plan-1", Transformations: []playback.TransformationV3{
			{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: "2"},
		}},
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	f.handler.PlanStoreV3 = store
	seedNodeTransformations(f.handler, older.server.URL, "1")
	seedNodeTransformations(f.handler, current.server.URL, "2")

	if rr := f.segment("seg_00003.ts"); rr.Code != http.StatusOK || rr.Body.String() != "moved:seg_00003.ts" {
		t.Fatalf("status = %d, body = %q; want the segment served by the current node", rr.Code, rr.Body.String())
	}
	if got := older.starts.Load(); got != 0 {
		t.Fatalf("starts on the node with the older recipe = %d, want 0", got)
	}
	if got := current.starts.Load(); got != 1 {
		t.Fatalf("starts on the node with the session's recipe = %d, want 1", got)
	}
}

// TestRelayMoveRestoresStoredRecipeAfterRefusedStart pins that a candidate's
// rollback stop, which makes the node delete the transport's stored recipe,
// does not cost a tokenless session the only recipe it has.
func TestRelayMoveRestoresStoredRecipeAfterRefusedStart(t *testing.T) {
	var store *sharedRecipeStore
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			// A node's stop drops the transport's stored recipe.
			_ = store.Delete(r.Context(), strings.TrimPrefix(r.URL.Path, "/transcode/"))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "no capacity", http.StatusServiceUnavailable)
	}))
	t.Cleanup(refusing.Close)
	f := newRehomeFixture(t, []*nodepool.Node{
		pooledNode(1, unreachableNodeURL(t)),
		pooledNode(2, refusing.URL),
	}, "ffmpeg")
	previous := f.handler.PlaybackConfig
	f.handler.PlaybackConfig = func() config.PlaybackConfig {
		cfg := previous()
		cfg.Routing = config.DefaultPlaybackRoutingPolicy()
		cfg.Routing.VideoTranscodeExecution = config.PlaybackExecutionWorkerOnly
		return cfg
	}
	stored, _ := verifiedStreamCardFromToken(f.token, f.session.ID, f.handler.JWTSecret)
	store = newSharedRecipeStore(*stored)
	f.handler.NodeRecipeStore = store

	rr := httptest.NewRecorder()
	f.handler.HandleGetTranscodeSegment(rr, playbackTestRequest(http.MethodGet,
		"/api/v2/playback/transcode/"+f.session.ID+"/segment/seg_00005.ts", nil,
		map[string]string{"session_id": f.session.ID, "name": "seg_00005.ts"}))
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %q; want 502 with no executor left", rr.Code, rr.Body.String())
	}
	if card, ok := store.Get(context.Background(), rehomeTestTransport); !ok || card.TranscodeNodeURL != f.deadURL {
		t.Fatalf("stored recipe = %+v (present %v), want the original card kept", card, ok)
	}
}

// TestRelayMoveRestoresLegacySessionsStoredRecipe covers a session whose route
// records no transport id: its transport, and its stored recipe's key, is the
// session id. A refused candidate's stop deletes that recipe, and the rollback
// must still write it back under the same key.
func TestRelayMoveRestoresLegacySessionsStoredRecipe(t *testing.T) {
	var store *sharedRecipeStore
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			_ = store.Delete(r.Context(), strings.TrimPrefix(r.URL.Path, "/transcode/"))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "no capacity", http.StatusServiceUnavailable)
	}))
	t.Cleanup(refusing.Close)
	f := newRehomeFixture(t, []*nodepool.Node{
		pooledNode(1, unreachableNodeURL(t)),
		pooledNode(2, refusing.URL),
	}, "ffmpeg")
	if err := f.sessions.SetTranscodeRoute(f.session.ID, playback.TranscodeRoute{NodeURL: f.deadURL}); err != nil {
		t.Fatal(err)
	}
	stored, _ := verifiedStreamCardFromToken(f.token, f.session.ID, f.handler.JWTSecret)
	stored.TranscodeTransportID = ""
	store = &sharedRecipeStore{cards: map[string]playback.RecipeCard{f.session.ID: *stored}}
	f.handler.NodeRecipeStore = store

	f.handler.rollBackMovedStartV3(context.Background(), f.session.ID, f.deadURL, f.session.ID, refusing.URL, *stored, true)
	if card, ok := store.Get(context.Background(), f.session.ID); !ok || card.TranscodeNodeURL != f.deadURL {
		t.Fatalf("stored recipe = %+v (present %v), want the original card restored under the session id", card, ok)
	}
}

// TestRelayMoveWritesStoredRecipeAfterRetiringOldNode covers a node that was
// only briefly unreachable: the retire stop reaches it and it deletes the
// transport's stored recipe. The card naming the new node must be written
// after that delete, not before it.
func TestRelayMoveWritesStoredRecipeAfterRetiringOldNode(t *testing.T) {
	var store *sharedRecipeStore
	var retired atomic.Int32
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			_ = store.Delete(r.Context(), strings.TrimPrefix(r.URL.Path, "/transcode/"))
			retired.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// The relay's request dies without a response.
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(flaky.Close)
	healthy := newFakeRehomeNode(t, false)
	f := newRehomeFixture(t, []*nodepool.Node{
		pooledNode(1, flaky.URL),
		pooledNode(2, healthy.server.URL),
	}, "ffmpeg")
	// The pool already lists the node as unhealthy, so a transport error
	// counts as unreachable.
	f.planner.MarkTranscodeNodeUnreachable(flaky.URL)
	stored, _ := verifiedStreamCardFromToken(f.token, f.session.ID, f.handler.JWTSecret)
	store = newSharedRecipeStore(*stored)
	f.handler.NodeRecipeStore = store

	rr := httptest.NewRecorder()
	f.handler.HandleGetTranscodeSegment(rr, playbackTestRequest(http.MethodGet,
		"/api/v2/playback/transcode/"+f.session.ID+"/segment/seg_00005.ts", nil,
		map[string]string{"session_id": f.session.ID, "name": "seg_00005.ts"}))
	if rr.Code != http.StatusOK || rr.Body.String() != "moved:seg_00005.ts" {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	waitForStoredRecipeNode(t, store, healthy.server.URL, 2)
	if got := retired.Load(); got != 1 {
		t.Fatalf("retire stops on the old node = %d, want 1", got)
	}
}

// TestConnectFailureIgnoresLocalSocketExhaustion pins that a dial this host
// could not attempt does not condemn the node.
func TestConnectFailureIgnoresLocalSocketExhaustion(t *testing.T) {
	local := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("socket", syscall.EMFILE)}
	if isConnectFailure(local) {
		t.Fatal("a dial that ran out of file descriptors counted as a dead node")
	}
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	if !isConnectFailure(refused) {
		t.Fatal("a refused connection did not count as a dead node")
	}
}

// TestRelayMoveEndsOnSourceToneMapRefusal pins that a tone-map refusal about
// the source file itself ends the move with the documented terminal 422,
// instead of trying every executor and answering a retryable 502.
func TestRelayMoveEndsOnSourceToneMapRefusal(t *testing.T) {
	var starts atomic.Int32
	refusing := func() *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			starts.Add(1)
			w.Header().Set(transcodenode.ToneMapExecutionErrorHeader, transcodenode.ToneMapSourceRevisionChangedCode)
			http.Error(w, "source changed", http.StatusUnprocessableEntity)
		}))
		t.Cleanup(server.Close)
		return server
	}
	first, second := refusing(), refusing()
	f := newRehomeFixture(t, []*nodepool.Node{
		pooledNode(1, unreachableNodeURL(t)),
		pooledNode(2, first.URL),
		pooledNode(3, second.URL),
	}, "ffmpeg")
	card, _ := verifiedStreamCardFromToken(f.token, f.session.ID, f.handler.JWTSecret)
	card.ToneMapMode = tonemap.ModeSoftware
	card.ToneMapSourceKind = tonemap.SourcePQ
	f.token = f.handler.signSessionToken(*card, false)
	for _, url := range []string{first.URL, second.URL} {
		f.handler.v3NodeCapabilitiesMu.Lock()
		if f.handler.v3NodeCapabilities == nil {
			f.handler.v3NodeCapabilities = map[string]v3NodeCapabilityCache{}
		}
		f.handler.v3NodeCapabilities[nodepool.NormalizeNodeURL(url)] = v3NodeCapabilityCache{
			toneMapCapabilities: tonemap.Capabilities{{Mode: tonemap.ModeSoftware, Backend: "none", Filter: "tonemap", SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}}},
			expiresAt:           time.Now().Add(time.Hour),
		}
		f.handler.v3NodeCapabilitiesMu.Unlock()
	}

	rr := f.segment("seg_00003.ts")
	if rr.Code != http.StatusUnprocessableEntity || rr.Header().Get(transcodenode.ToneMapExecutionErrorHeader) != transcodenode.ToneMapSourceRevisionChangedCode {
		t.Fatalf("status = %d, tone-map error %q; want 422 %s", rr.Code, rr.Header().Get(transcodenode.ToneMapExecutionErrorHeader), transcodenode.ToneMapSourceRevisionChangedCode)
	}
	if got := starts.Load(); got != 1 {
		t.Fatalf("starts = %d, want the move to stop after the first refusal", got)
	}
}

// TestMovedRecipeWriteRequiresTheLiveRoute pins the guard on the stored
// recipe writes a move makes late (after the old node's retire stop, or after
// a candidate's rollback stop): once the session has stopped or left the
// route, the stop or replan deleted the recipe, and writing it back would let
// a buffered request rebuild an ended transport after a node restart.
func TestMovedRecipeWriteRequiresTheLiveRoute(t *testing.T) {
	healthy := newFakeRehomeNode(t, false)
	f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, healthy.server.URL)}, "ffmpeg")
	stored, _ := verifiedStreamCardFromToken(f.token, f.session.ID, f.handler.JWTSecret)
	store := &sharedRecipeStore{cards: map[string]playback.RecipeCard{}}
	f.handler.NodeRecipeStore = store
	live := playback.TranscodeRoute{NodeURL: f.deadURL, TransportID: rehomeTestTransport}

	f.handler.putNodeRecipeIfRouteV3(context.Background(), f.session.ID, playback.TranscodeRoute{NodeURL: "http://elsewhere:8080", TransportID: rehomeTestTransport}, *stored)
	if _, ok := store.Get(context.Background(), rehomeTestTransport); ok {
		t.Fatal("wrote the recipe for a route the session no longer runs")
	}
	f.handler.putNodeRecipeIfRouteV3(context.Background(), f.session.ID, live, *stored)
	if _, ok := store.Get(context.Background(), rehomeTestTransport); !ok {
		t.Fatal("did not write the recipe for the live route")
	}

	if _, err := f.handler.stopPlaybackSessionWithResult(context.Background(), f.session, true); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, ok := store.Get(context.Background(), rehomeTestTransport); ok {
		t.Fatal("the stop left the stored recipe in place")
	}
	f.handler.putNodeRecipeIfRouteV3(context.Background(), f.session.ID, live, *stored)
	if _, ok := store.Get(context.Background(), rehomeTestTransport); ok {
		t.Fatal("wrote the recipe back after the session stopped")
	}
}
