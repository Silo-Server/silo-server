package jellycompat

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderecipe"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

const (
	compatMoveDeadNodeID  = 1
	compatMoveLiveNodeID  = 2
	compatMoveProxyNodeID = 3
	compatMoveSegmentPath = "/transcode/upstream-1/segment/seg_00019.ts"
)

// compatMoveFixture is a Jellyfin-compatible play whose transcode runs on a
// transcode node that has just died. The pool still lists that node as
// healthy, as it does until the next health sweep.
type compatMoveFixture struct {
	handler    *PlaybackHandler
	store      *PlaybackSessionStore
	sessions   *testCompatSessionManager
	recipes    *stubRecipeNodeStore
	deadURL    string
	liveURL    string
	liveMu     sync.Mutex
	livePaths  []string
	segmentHit chan struct{}
}

func closedNodeURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	url := "http://" + listener.Addr().String()
	_ = listener.Close()
	return url
}

// newCompatMoveFixture builds the play. withLiveNode adds a second, working
// transcode node to the pool; egress is the committed route's egress.
func newCompatMoveFixture(t *testing.T, withLiveNode bool, egress noderouting.Egress) *compatMoveFixture {
	t.Helper()
	return newCompatMoveFixtureOn(t, closedNodeURL(t), withLiveNode, egress)
}

// newCompatMoveFixtureOn is newCompatMoveFixture with the node the transcode
// leaves at oldNodeURL, for tests where that node still answers some callers.
func newCompatMoveFixtureOn(t *testing.T, oldNodeURL string, withLiveNode bool, egress noderouting.Egress) *compatMoveFixture {
	t.Helper()
	f := &compatMoveFixture{deadURL: oldNodeURL, segmentHit: make(chan struct{}, 64)}
	nodes := []*nodepool.Node{{ID: compatMoveDeadNodeID, URL: f.deadURL, Enabled: true, Healthy: true}}
	if withLiveNode {
		live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			f.liveMu.Lock()
			f.livePaths = append(f.livePaths, r.URL.Path)
			f.liveMu.Unlock()
			if strings.HasSuffix(r.URL.Path, "/master.m3u8") {
				_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2.0,\nseg_00019.ts\n"))
				return
			}
			f.segmentHit <- struct{}{}
			_, _ = w.Write([]byte("segment from the live node"))
		}))
		t.Cleanup(live.Close)
		f.liveURL = live.URL
		nodes = append(nodes, &nodepool.Node{ID: compatMoveLiveNodeID, URL: live.URL, Enabled: true, Healthy: true})
	}
	transcodes := nodepool.NewTranscodePool()
	transcodes.SetNodes(nodes)
	proxies := nodepool.NewProxyPool()
	proxies.SetNodes([]*nodepool.Node{
		{ID: compatMoveProxyNodeID, URL: "http://proxy-a.invalid", Enabled: true, Healthy: true},
		{ID: compatMoveProxyNodeID + 1, URL: "http://proxy-b.invalid", Enabled: true, Healthy: true},
	})

	source := testCompatSource(NewResourceIDCodec(), testCompatVersion())
	card := playback.NewRecipeCard(7, "profile-1", source.FileID, f.deadURL, playback.TranscodeOpts{
		SessionID: "upstream-1", InputPath: "/media/movie.mkv",
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: compatSegmentDuration,
		AudioTrackIndex: compatAudioTrackIndexOrDefault(source),
		HWAccel:         "videotoolbox", EncoderHWAccel: "videotoolbox",
	})
	card.RoutingWorkload = string(noderouting.WorkloadVideoTranscode)
	card.RoutingExecution = string(noderouting.ExecutionTranscode)
	card.RoutingExecutionNodeID = compatMoveDeadNodeID
	if !compatRecipeMatchesSource(&card, source) {
		t.Fatal("fixture recipe does not match its source")
	}
	assignment := playback.NodeRoutingAssignment{
		Workload: string(noderouting.WorkloadVideoTranscode), Execution: string(noderouting.ExecutionTranscode),
		ExecutionNodeID: compatMoveDeadNodeID, ExecutionNodeURL: f.deadURL, Egress: string(egress),
	}
	if egress == noderouting.EgressProxy {
		assignment.EgressNodeID = compatMoveProxyNodeID
		assignment.EgressNodeURL = "http://proxy-a.invalid"
	}
	f.store = NewPlaybackSessionStore(time.Hour, nil)
	f.store.Put(PlaybackSession{
		ID: "play-1", CompatToken: "compat-token", RouteItemID: "item-1",
		UpstreamSessionID: "upstream-1", UpstreamPlayMethod: "transcode", TranscodeStarted: true,
		MediaSources: []PlaybackMediaSource{source}, Recipe: &card, RoutingAssignment: &assignment,
	})
	f.sessions = &testCompatSessionManager{sessions: map[string]*playback.Session{
		"upstream-1": {
			ID: "upstream-1", UserID: 7, ProfileID: "profile-1", MediaFileID: source.FileID,
			PlayMethod: playback.PlayTranscode, BasePlayMethod: playback.PlayTranscode, TranscodeNodeURL: f.deadURL,
		},
	}}
	f.recipes = &stubRecipeNodeStore{cards: map[string]playback.RecipeCard{"upstream-1": card}}
	f.handler = &PlaybackHandler{
		playbackStore:   f.store,
		sessionMgr:      f.sessions,
		fileResolver:    testCompatFileResolver{file: &models.MediaFile{ID: source.FileID, FilePath: "/media/movie.mkv"}},
		tm:              playback.NewTranscodeManager(),
		JWTSecret:       "test-secret",
		RecipeNodeStore: f.recipes,
		NodePlanner:     nodepool.NewPlanner(proxies, transcodes),
	}
	return f
}

func (f *compatMoveFixture) requestSegment(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/Videos/item-1/hls/play-1/seg_00019.ts", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", "item-1")
	routeCtx.URLParams.Add("playlistId", "play-1")
	routeCtx.URLParams.Add("segmentId", "seg_00019")
	routeCtx.URLParams.Add("segmentContainer", "ts")
	ctx := context.WithValue(t.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "compat-token", StreamAppUserID: 7})
	recorder := httptest.NewRecorder()
	f.handler.HandleHLSSegment(recorder, request.WithContext(ctx))
	return recorder
}

func (f *compatMoveFixture) storedRecipe(t *testing.T) playback.RecipeCard {
	t.Helper()
	card, ok := f.recipes.Get("upstream-1")
	if !ok {
		t.Fatal("node recipe record is gone")
	}
	return card
}

func (f *compatMoveFixture) playSession(t *testing.T) *PlaybackSession {
	t.Helper()
	playSession, ok := f.store.Get("play-1")
	if !ok || playSession.Recipe == nil || playSession.RoutingAssignment == nil {
		t.Fatal("play session lost its recipe or route")
	}
	return playSession
}

func countOperations(operations []string, want string) int {
	count := 0
	for _, operation := range operations {
		if operation == want {
			count++
		}
	}
	return count
}

// The API relay cannot reach the node that runs the transcode. The transcode
// moves to the other node under the same transport id, the same request is
// served from there, and every record of the route names the new node.
func TestCompatSegmentRelayMovesTranscodeOffUnreachableNode(t *testing.T) {
	f := newCompatMoveFixture(t, true, noderouting.EgressAPI)

	recorder := f.requestSegment(t)

	if recorder.Code != http.StatusOK || recorder.Body.String() != "segment from the live node" {
		t.Fatalf("segment = %d %q, want 200 from the live node", recorder.Code, recorder.Body.String())
	}
	f.liveMu.Lock()
	paths := append([]string(nil), f.livePaths...)
	f.liveMu.Unlock()
	if len(paths) != 1 || paths[0] != compatMoveSegmentPath {
		t.Fatalf("live node requests = %v, want the same segment under the same transport", paths)
	}
	card := f.storedRecipe(t)
	if card.TranscodeNodeURL != f.liveURL || card.RoutingExecutionNodeID != compatMoveLiveNodeID {
		t.Fatalf("node recipe names %q (node %d), want %q (node %d)", card.TranscodeNodeURL, card.RoutingExecutionNodeID, f.liveURL, compatMoveLiveNodeID)
	}
	if card.HWAccel != "" || card.EncoderHWAccel != "" {
		t.Fatalf("moved recipe kept the dead node's hardware: %q/%q", card.HWAccel, card.EncoderHWAccel)
	}
	if card.SegmentDuration != compatSegmentDuration || card.TargetCodecVideo != "h264" || card.SessionID != "upstream-1" {
		t.Fatalf("moved recipe changed byte-affecting fields: %+v", card)
	}
	playSession := f.playSession(t)
	if playSession.Recipe.TranscodeNodeURL != f.liveURL {
		t.Fatalf("compat recipe names %q, want %q", playSession.Recipe.TranscodeNodeURL, f.liveURL)
	}
	assignment := playSession.RoutingAssignment
	if assignment.Execution != string(noderouting.ExecutionTranscode) || assignment.ExecutionNodeID != compatMoveLiveNodeID ||
		assignment.ExecutionNodeURL != f.liveURL || assignment.Egress != string(noderouting.EgressAPI) {
		t.Fatalf("route = %+v, want API egress from node %d", assignment, compatMoveLiveNodeID)
	}
	if upstream, _ := f.sessions.GetSession("upstream-1"); upstream.TranscodeNodeURL != f.liveURL {
		t.Fatalf("upstream session runs on %q, want %q", upstream.TranscodeNodeURL, f.liveURL)
	}
}

// Every request that finds the node dead waits on one move.
func TestCompatConcurrentRelaysShareOneMove(t *testing.T) {
	f := newCompatMoveFixture(t, true, noderouting.EgressAPI)

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if recorder := f.requestSegment(t); recorder.Code != http.StatusOK {
				t.Errorf("segment = %d %s, want 200", recorder.Code, recorder.Body.String())
			}
		})
	}
	wg.Wait()
	if got := countOperations(f.recipes.Operations(), "put:upstream-1"); got != 1 {
		t.Fatalf("node recipe writes = %d (%v), want one move", got, f.recipes.Operations())
	}
}

// A node that answers is alive, even when it answers with an error: the
// answer is relayed and nothing moves.
func TestCompatSegmentRelayPassesLiveNodeErrorThrough(t *testing.T) {
	f := newCompatMoveFixture(t, true, noderouting.EgressAPI)
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "encoder failed", http.StatusInternalServerError)
	}))
	defer failing.Close()
	if err := f.store.Update("play-1", func(current *PlaybackSession) error {
		current.Recipe.TranscodeNodeURL = failing.URL
		return nil
	}); err != nil {
		t.Fatalf("point the recipe at the failing node: %v", err)
	}

	recorder := f.requestSegment(t)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("segment = %d, want the node's 500", recorder.Code)
	}
	if ops := f.recipes.Operations(); len(ops) != 0 {
		t.Fatalf("a live node's error moved the transcode: %v", ops)
	}
}

// With no other node, an API-egress transcode moves to this server, which
// rebuilds it from the compat recipe through its local reconstruction.
func TestCompatMoveFallsBackToThisServer(t *testing.T) {
	f := newCompatMoveFixture(t, false, noderouting.EgressAPI)

	nodeURL, err := f.handler.moveCompatTranscodeOffNode(t.Context(), "upstream-1", f.deadURL)

	if err != nil || nodeURL != "" {
		t.Fatalf("move = %q, %v; want this server", nodeURL, err)
	}
	if _, ok := f.recipes.Get("upstream-1"); ok {
		t.Fatal("a local transcode kept the dead node's recipe record")
	}
	playSession := f.playSession(t)
	if playSession.Recipe.TranscodeNodeURL != "" ||
		playSession.RoutingAssignment.Execution != string(noderouting.ExecutionAPI) ||
		playSession.RoutingAssignment.ExecutionNodeID != 0 ||
		playSession.RoutingAssignment.Egress != string(noderouting.EgressAPI) {
		t.Fatalf("route after a local move = recipe node %q, %+v", playSession.Recipe.TranscodeNodeURL, playSession.RoutingAssignment)
	}
	if upstream, _ := f.sessions.GetSession("upstream-1"); upstream.TranscodeNodeURL != "" {
		t.Fatalf("upstream session still runs on %q", upstream.TranscodeNodeURL)
	}
	// The next local request reconstructs from this card.
	card := f.handler.upstreamRecipeCard(playSession, &Session{StreamAppUserID: 7}, playSession.MediaSources[0], "transcode")
	if card.TranscodeNodeURL != "" || card.RoutingExecution != string(noderouting.ExecutionAPI) || card.SegmentDuration != compatSegmentDuration {
		t.Fatalf("local reconstruction card = %+v", card)
	}
}

// A proxy that cannot reach the node asks the API server that owns the
// session to move it. The transcode moves to another node behind the same
// proxy, and the shared record the proxy follows names the new node.
func TestCompatMoveRequestFromProxyKeepsProxyEgress(t *testing.T) {
	f := newCompatMoveFixture(t, true, noderouting.EgressProxy)
	request := func(transportID, nodeURL string) cache.Event {
		payload, err := noderecipe.EncodeMoveRequest(noderecipe.MoveRequest{TransportID: transportID, NodeURL: nodeURL})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		return cache.Event{Type: cache.EventTranscodeMoveRequested, Payload: payload}
	}

	// Requests for a transport this process does not own, or for a node the
	// transport no longer runs on, are ignored.
	f.handler.HandleTranscodeMoveRequest(t.Context(), request("other-transport", f.deadURL))
	f.handler.HandleTranscodeMoveRequest(t.Context(), request("upstream-1", "http://some-other-node"))
	if ops := f.recipes.Operations(); len(ops) != 0 {
		t.Fatalf("an unrelated move request changed the record: %v", ops)
	}

	f.handler.HandleTranscodeMoveRequest(t.Context(), request("upstream-1", f.deadURL))

	deadline := time.Now().Add(10 * time.Second)
	for f.storedRecipe(t).TranscodeNodeURL != f.liveURL {
		if time.Now().After(deadline) {
			t.Fatalf("record still names %q", f.storedRecipe(t).TranscodeNodeURL)
		}
		time.Sleep(5 * time.Millisecond) //nolint:forbidigo // polls the recipe store, the observable state
	}
	assignment := f.playSession(t).RoutingAssignment
	if assignment.Egress != string(noderouting.EgressProxy) || assignment.EgressNodeID != compatMoveProxyNodeID ||
		assignment.ExecutionNodeID != compatMoveLiveNodeID {
		t.Fatalf("route = %+v, want node %d behind proxy %d", assignment, compatMoveLiveNodeID, compatMoveProxyNodeID)
	}
}

// A proxy-egress transcode cannot move to this server, because the client
// fetches from the proxy. With no other node it stays where it was.
func TestCompatProxyEgressMoveNeedsAnotherNode(t *testing.T) {
	f := newCompatMoveFixture(t, false, noderouting.EgressProxy)

	if _, err := f.handler.moveCompatTranscodeOffNode(t.Context(), "upstream-1", f.deadURL); err == nil {
		t.Fatal("moved a proxy-egress transcode with no other node")
	}
	if card := f.storedRecipe(t); card.TranscodeNodeURL != f.deadURL {
		t.Fatalf("record names %q after a failed move", card.TranscodeNodeURL)
	}
	if playSession := f.playSession(t); playSession.RoutingAssignment.ExecutionNodeID != compatMoveDeadNodeID {
		t.Fatalf("route changed after a failed move: %+v", playSession.RoutingAssignment)
	}
}

// A move that finds the transport already moved returns where it runs now.
func TestCompatMoveOfMovedTranscodeReturnsCurrentNode(t *testing.T) {
	f := newCompatMoveFixture(t, true, noderouting.EgressAPI)
	if _, err := f.handler.moveCompatTranscodeOffNode(t.Context(), "upstream-1", f.deadURL); err != nil {
		t.Fatalf("first move: %v", err)
	}
	nodeURL, err := f.handler.moveCompatTranscodeOffNode(t.Context(), "upstream-1", f.deadURL)
	if err != nil || nodeURL != f.liveURL {
		t.Fatalf("second move = %q, %v; want %q", nodeURL, err, f.liveURL)
	}
	if got := countOperations(f.recipes.Operations(), "put:upstream-1"); got != 1 {
		t.Fatalf("node recipe writes = %d, want 1", got)
	}
}

// The proxy lost the old node, but the API can still reach it, so the
// background stop lands. A node deletes a transport's record when it stops
// it; the moved record names the new node and must survive that stop.
func TestCompatMoveSurvivesOldNodeStop(t *testing.T) {
	for _, tt := range []struct {
		name          string
		keepsMovedRow bool
	}{
		// A current node keeps a card that names another node
		// (noderecipe.Store.DeleteUnlessMoved).
		{name: "current node", keepsMovedRow: true},
		// An older node deletes the record anyway; central writes it again
		// once the stop is done.
		{name: "node that deletes the record", keepsMovedRow: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var f *compatMoveFixture
			stopped := make(chan struct{}, 1)
			oldNode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || r.URL.Path != "/transcode/upstream-1" {
					http.NotFound(w, r)
					return
				}
				// What handleStop does with the shared record.
				self := playback.RecipeExecutor{NodeID: compatMoveDeadNodeID, NodeURL: f.deadURL}
				card, ok := f.recipes.Get("upstream-1")
				if !tt.keepsMovedRow || !ok || !self.NamesAnotherNode(card) {
					_ = f.recipes.Delete(r.Context(), "upstream-1")
				}
				w.WriteHeader(http.StatusNoContent)
				stopped <- struct{}{}
			}))
			defer oldNode.Close()
			f = newCompatMoveFixtureOn(t, oldNode.URL, true, noderouting.EgressProxy)
			payload, err := noderecipe.EncodeMoveRequest(noderecipe.MoveRequest{TransportID: "upstream-1", NodeURL: oldNode.URL})
			if err != nil {
				t.Fatalf("encode: %v", err)
			}

			f.handler.HandleTranscodeMoveRequest(t.Context(), cache.Event{Type: cache.EventTranscodeMoveRequested, Payload: payload})

			select {
			case <-stopped:
			case <-time.After(10 * time.Second):
				t.Fatal("the old node was never asked to stop the transport")
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				card, ok := f.recipes.Get("upstream-1")
				if ok && card.TranscodeNodeURL == f.liveURL {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("moved record lost after the old node's stop: %+v, %v (operations %v)", card, ok, f.recipes.Operations())
				}
				time.Sleep(5 * time.Millisecond) //nolint:forbidigo // polls the recipe store, the observable state
			}
			if tt.keepsMovedRow {
				if got := countOperations(f.recipes.Operations(), "delete:upstream-1"); got != 0 {
					t.Fatalf("the moved record was deleted %d time(s): %v", got, f.recipes.Operations())
				}
			}
		})
	}
}

// This server takes a moved transcode only when it can run the recipe: its
// frozen tone-map mode and its audio downmix. The candidate filter checks
// transcode nodes, and a local rebuild of a recipe this process cannot run
// fails on every request. A copy-video downmix routes as a remux, so the
// check covers both workloads' local shapes.
func TestCompatMoveToThisServerRequiresLocalRecipeCapabilities(t *testing.T) {
	hardware := tonemap.Capability{Mode: tonemap.ModeHardware, Backend: tonemap.BackendQSV,
		Filter: tonemap.HardwareFilterOpenCL, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}}
	software := tonemap.Capability{Mode: tonemap.ModeSoftware, Backend: playback.HWAccelNone,
		Filter: "zscale", SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}}
	toneMap := func(local tonemap.Capabilities) func(*testing.T, *compatMoveFixture) {
		return func(t *testing.T, f *compatMoveFixture) {
			if err := f.store.Update("play-1", func(current *PlaybackSession) error {
				current.Recipe.ToneMapMode = tonemap.ModeHardware
				current.Recipe.ToneMapSourceKind = tonemap.SourcePQ
				return nil
			}); err != nil {
				t.Fatalf("set the tone-map mode: %v", err)
			}
			f.handler.HWAccel = playback.HWAccelNone
			f.handler.compatToneMapProbe = func(context.Context, string, string, string) (tonemap.Capabilities, error) {
				return local, nil
			}
		}
	}
	copyVideoDownmix := func(localDownmix bool) func(*testing.T, *compatMoveFixture) {
		return func(t *testing.T, f *compatMoveFixture) {
			if err := f.store.Update("play-1", func(current *PlaybackSession) error {
				source := &current.MediaSources[0]
				source.HLSRemux, source.TranscodeAudio, source.TargetAudioChannels = true, true, 2
				source.Version.AudioTracks[compatAudioTrackIndexOrDefault(*source)].Channels = 6
				current.Recipe.TargetCodecVideo = compatCopyCodec
				current.Recipe.SourceAudioChannels = compatHLSRecipeSourceAudioChannels(*source)
				current.Recipe.RoutingWorkload = string(noderouting.WorkloadRemux)
				current.RoutingAssignment.Workload = string(noderouting.WorkloadRemux)
				if current.Recipe.SourceAudioChannels != 6 || !compatRecipeMatchesSource(current.Recipe, *source) {
					t.Fatal("fixture recipe is not a copy-video downmix of its source")
				}
				return nil
			}); err != nil {
				t.Fatalf("make the recipe a copy-video downmix: %v", err)
			}
			f.handler.compatAudioRegistryProbe = func(context.Context, string, tonemap.Capabilities) (*playback.TransformationRegistryV3, error) {
				var specs []playback.TransformationSpecV3
				if localDownmix {
					specs = append(specs, playback.TransformationSpecV3{
						Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true,
					})
				}
				return playback.NewTransformationRegistryV3(specs), nil
			}
		}
	}
	for _, tt := range []struct {
		name      string
		setup     func(*testing.T, *compatMoveFixture)
		wantLocal bool
	}{
		{name: "local runs the tone-map mode", setup: toneMap(tonemap.Capabilities{hardware, software}), wantLocal: true},
		{name: "local lacks the tone-map mode", setup: toneMap(tonemap.Capabilities{software})},
		{name: "local runs the copy-video downmix", setup: copyVideoDownmix(true), wantLocal: true},
		{name: "local lacks the copy-video downmix", setup: copyVideoDownmix(false)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newCompatMoveFixture(t, false, noderouting.EgressAPI)
			tt.setup(t, f)

			nodeURL, err := f.handler.moveCompatTranscodeOffNode(t.Context(), "upstream-1", f.deadURL)

			if tt.wantLocal {
				if err != nil || nodeURL != "" {
					t.Fatalf("move = %q, %v; want this server", nodeURL, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("moved to %q without an executor that can run the recipe", nodeURL)
			}
			if playSession := f.playSession(t); playSession.Recipe.TranscodeNodeURL != f.deadURL {
				t.Fatalf("compat recipe names %q after a failed move", playSession.Recipe.TranscodeNodeURL)
			}
		})
	}
}

// A request that already found a node unreachable does not move the
// transcode back onto it, even while the pool still lists it as healthy.
func TestCompatMoveSkipsNodesTheRequestFoundUnreachable(t *testing.T) {
	f := newCompatMoveFixture(t, false, noderouting.EgressAPI)
	otherDeadURL := closedNodeURL(t)
	transcodes := nodepool.NewTranscodePool()
	transcodes.SetNodes([]*nodepool.Node{
		{ID: compatMoveDeadNodeID, URL: f.deadURL, Enabled: true, Healthy: true},
		{ID: compatMoveLiveNodeID, URL: otherDeadURL, Enabled: true, Healthy: true},
	})
	f.handler.NodePlanner = nodepool.NewPlanner(nodepool.NewProxyPool(), transcodes)

	nodeURL, err := f.handler.moveCompatTranscodeOffNode(t.Context(), "upstream-1", f.deadURL, otherDeadURL)

	if err != nil || nodeURL != "" {
		t.Fatalf("move = %q, %v; want this server, not a node the request already failed on", nodeURL, err)
	}
}

// A durable store can fail its database write after it already applied the
// move to its live cache. The failed move leaves the compat session on the
// dead node, like the record and the upstream session, so the next move
// request for that node is not ignored.
func TestCompatMoveRollsBackCompatSessionWhenDurableWriteFails(t *testing.T) {
	f := newCompatMoveFixture(t, true, noderouting.EgressAPI)
	f.handler.playbackStore = &failAfterMutationPlaybackStore{CompatPlaybackStore: f.store, failNext: true}

	if _, err := f.handler.moveCompatTranscodeOffNode(t.Context(), "upstream-1", f.deadURL); err == nil {
		t.Fatal("move reported success after the durable write failed")
	}
	playSession := f.playSession(t)
	if playSession.Recipe.TranscodeNodeURL != f.deadURL || playSession.RoutingAssignment.ExecutionNodeID != compatMoveDeadNodeID {
		t.Fatalf("compat session after a failed move = recipe node %q, %+v", playSession.Recipe.TranscodeNodeURL, playSession.RoutingAssignment)
	}
	if card := f.storedRecipe(t); card.TranscodeNodeURL != f.deadURL {
		t.Fatalf("record names %q after a failed move", card.TranscodeNodeURL)
	}

	nodeURL, err := f.handler.moveCompatTranscodeOffNode(t.Context(), "upstream-1", f.deadURL)
	if err != nil || nodeURL != f.liveURL {
		t.Fatalf("retried move = %q, %v; want %q", nodeURL, err, f.liveURL)
	}
}

// An audio switch on the new node can land while the old node's stop is in
// flight. The record written after the stop is the switched recipe, not the
// one the move committed.
func TestCompatMoveRewriteAfterOldNodeStopKeepsNewerRecipe(t *testing.T) {
	var f *compatMoveFixture
	const switchedTrack = 5
	oldNode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.NotFound(w, r)
			return
		}
		// The switch rewrites the compat recipe and the record on the new node.
		var switched playback.RecipeCard
		if err := f.store.Update("play-1", func(current *PlaybackSession) error {
			current.Recipe.AudioTrackIndex = switchedTrack
			switched = *current.Recipe
			return nil
		}); err != nil {
			t.Errorf("switch audio: %v", err)
		}
		_ = f.recipes.Put(r.Context(), "upstream-1", switched)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer oldNode.Close()
	f = newCompatMoveFixtureOn(t, oldNode.URL, true, noderouting.EgressAPI)

	if _, err := f.handler.moveCompatTranscodeOffNode(t.Context(), "upstream-1", oldNode.URL); err != nil {
		t.Fatalf("move: %v", err)
	}

	// The move, the switch, and the rewrite after the stop.
	deadline := time.Now().Add(10 * time.Second)
	for countOperations(f.recipes.Operations(), "put:upstream-1") < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("record writes = %v, want the rewrite after the stop", f.recipes.Operations())
		}
		time.Sleep(5 * time.Millisecond) //nolint:forbidigo // polls the recipe store, the observable state
	}
	if card := f.storedRecipe(t); card.AudioTrackIndex != switchedTrack || card.TranscodeNodeURL != f.liveURL {
		t.Fatalf("record after the stop = track %d on %q, want track %d on %q",
			card.AudioTrackIndex, card.TranscodeNodeURL, switchedTrack, f.liveURL)
	}
}

// putHookRecipeStore runs onPut once, before the first record write.
type putHookRecipeStore struct {
	*stubRecipeNodeStore
	once  sync.Once
	onPut func()
}

func (s *putHookRecipeStore) Put(ctx context.Context, sessionID string, card playback.RecipeCard) error {
	s.once.Do(s.onPut)
	return s.stubRecipeNodeStore.Put(ctx, sessionID, card)
}

// A stop that lands while a move rewrites the node recipe record must still
// leave no record behind: a record of a stopped play lets a node that comes
// back, or a still-valid proxy token, rebuild the transcode.
func TestCompatStopDuringMoveLeavesNoRecipe(t *testing.T) {
	f := newCompatMoveFixture(t, true, noderouting.EgressAPI)
	playSession := f.playSession(t)
	stopped := make(chan struct{})
	f.handler.RecipeNodeStore = &putHookRecipeStore{stubRecipeNodeStore: f.recipes, onPut: func() {
		go func() {
			defer close(stopped)
			f.store.Delete(playSession.ID)
			f.handler.cleanupPlaySession(context.Background(), playSession, nil, f.deadURL)
		}()
		// Give the stop the chance to finish before the move writes, which it
		// does when nothing orders the two.
		select {
		case <-stopped:
		case <-time.After(200 * time.Millisecond):
		}
	}}

	_, _ = f.handler.moveCompatTranscodeOffNode(t.Context(), "upstream-1", f.deadURL)
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("the stop never finished")
	}
	if card, ok := f.recipes.Get("upstream-1"); ok {
		t.Fatalf("a stopped play kept a node recipe naming %q (operations %v)", card.TranscodeNodeURL, f.recipes.Operations())
	}
}
