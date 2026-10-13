package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

// probeRecorderV3 answers route-node probes from a fixed set of dead URLs and
// records which URLs were probed.
type probeRecorderV3 struct {
	mu     sync.Mutex
	dead   map[string]bool
	probed []string
}

func (p *probeRecorderV3) probe(_ context.Context, nodeURL string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.probed = append(p.probed, nodeURL)
	return !p.dead[nodeURL]
}

func (p *probeRecorderV3) probedURLs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	urls := slices.Clone(p.probed)
	slices.Sort(urls)
	return urls
}

func TestUnreachableSessionNodesV3ProbesTheSessionsCurrentNodes(t *testing.T) {
	const (
		transcodeURL = "http://transcode-a:8082"
		proxyURL     = "http://proxy-a:8083"
	)
	tests := []struct {
		name        string
		session     playback.Session
		dead        []string
		wantProbed  []string
		wantMissing []string
	}{
		{
			name:       "transcode node and proxy egress",
			session:    playback.Session{ID: "s1", TranscodeNodeURL: transcodeURL, RoutingEgress: string(noderouting.EgressProxy), RoutingEgressNodeURL: proxyURL},
			dead:       []string{proxyURL},
			wantProbed: []string{proxyURL, transcodeURL}, wantMissing: []string{proxyURL},
		},
		{
			name:       "API egress probes only the transcode node",
			session:    playback.Session{ID: "s2", TranscodeNodeURL: transcodeURL, RoutingEgress: string(noderouting.EgressAPI)},
			dead:       []string{transcodeURL},
			wantProbed: []string{transcodeURL}, wantMissing: []string{transcodeURL},
		},
		{
			name:       "direct play through a proxy",
			session:    playback.Session{ID: "s3", RoutingEgress: string(noderouting.EgressProxy), RoutingEgressNodeURL: proxyURL},
			wantProbed: []string{proxyURL},
		},
		{
			name:    "local session probes nothing",
			session: playback.Session{ID: "s4", RoutingEgress: string(noderouting.EgressAPI)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &probeRecorderV3{dead: map[string]bool{}}
			for _, nodeURL := range tt.dead {
				recorder.dead[nodeURL] = true
			}
			handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
			handler.v3RouteNodeProbe = recorder.probe

			unreachable := handler.unreachableSessionNodesV3(context.Background(), &tt.session)
			if got := recorder.probedURLs(); !slices.Equal(got, tt.wantProbed) {
				t.Fatalf("probed %v, want %v", got, tt.wantProbed)
			}
			if len(unreachable) != len(tt.wantMissing) {
				t.Fatalf("unreachable = %v, want %v", unreachable, tt.wantMissing)
			}
			for _, nodeURL := range tt.wantMissing {
				if _, ok := unreachable[nodeURL]; !ok {
					t.Fatalf("unreachable = %v, want it to contain %s", unreachable, nodeURL)
				}
			}
		})
	}
}

// The pool still lists a node that died as healthy until the next health
// sweep, and a session sticks to its current node. A request that found the
// node unreachable must start the replacement elsewhere without trying it.
func TestPrepareTransportV3SkipsAnUnreachableCurrentTranscodeNode(t *testing.T) {
	var deadRequests atomic.Int32
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadRequests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(dead.Close)
	live := remoteTranscodeNodeStubV3(t)

	transcodes := nodepool.NewTranscodePool()
	transcodes.SetNodes([]*nodepool.Node{
		{ID: 1, URL: dead.URL, Enabled: true, Healthy: true},
		{ID: 2, URL: live.URL, Enabled: true, Healthy: true},
	})
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = nodepool.NewPlanner(nodepool.NewProxyPool(), transcodes)
	requireWorkerRoutingV3(handler)

	session := &playback.Session{ID: "session-dead-node", UserID: 7, ProfileID: "profile-1", TranscodeNodeURL: dead.URL}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request = request.WithContext(withUnreachableRouteNodesV3(request.Context(), map[string]struct{}{dead.URL: {}}))
	transport, transportErr := handler.prepareTransportV3(request, session, v3HandlerFixtureFile(t), remoteHLSResultV3(), mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare transport: %v", transportErr)
	}
	defer transport.rollback()
	if transport.nodeURL != live.URL {
		t.Fatalf("transport node = %q, want the live node %q", transport.nodeURL, live.URL)
	}
	if n := deadRequests.Load(); n != 0 {
		t.Fatalf("the unreachable node received %d requests", n)
	}
}

// A proxy that died is skipped for the replacement route. With another proxy
// the stream moves there; with none left the route falls back to API egress.
func TestPrepareTransportV3RoutesAroundAnUnreachableProxy(t *testing.T) {
	const (
		deadProxyURL = "http://10.0.0.21:8083"
		liveProxyURL = "http://10.0.0.22:8083"
	)
	for _, tt := range []struct {
		name    string
		proxies []string
		wantURL string
	}{
		{name: "another proxy", proxies: []string{deadProxyURL, liveProxyURL}, wantURL: liveProxyURL + "/"},
		{name: "no proxy left", proxies: []string{deadProxyURL}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var nodes []*nodepool.Node
			for i, proxyURL := range tt.proxies {
				nodes = append(nodes, &nodepool.Node{ID: 40 + i, URL: proxyURL, Enabled: true, Healthy: true})
			}
			proxies := nodepool.NewProxyPool()
			proxies.SetNodes(nodes)
			handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
			handler.JWTSecret = "test-secret"
			handler.NodePlanner = nodepool.NewPlanner(proxies, nodepool.NewTranscodePool())
			handler.ProxyGrantStore = &recordingRecipeCardStoreV3{}

			request := httptest.NewRequest(http.MethodPost, "/", nil)
			request = request.WithContext(withUnreachableRouteNodesV3(request.Context(), map[string]struct{}{deadProxyURL: {}}))
			transport, transportErr := handler.prepareTransportV3(
				request,
				&playback.Session{ID: "session-dead-proxy", UserID: 7, ProfileID: "profile-1"},
				v3HandlerFixtureFile(t),
				playback.PlannerResultV3{Plan: identityProxyPlanV3(playback.DeliveryOriginalHTTPV3), PlayMethod: playback.PlayDirect},
				mediaAuthModeV3{})
			if transportErr != nil {
				t.Fatalf("prepare transport: %v", transportErr)
			}
			defer transport.rollback()
			if strings.Contains(transport.url, deadProxyURL) {
				t.Fatalf("transport url %q uses the unreachable proxy", transport.url)
			}
			if tt.wantURL == "" {
				assertAPIRelative(t, transport.url)
				return
			}
			if !strings.HasPrefix(transport.url, tt.wantURL) {
				t.Fatalf("transport url = %q, want the live proxy %q", transport.url, tt.wantURL)
			}
		})
	}
}

// remoteReplanFixtureV3 starts a protocol-v3 transcode on one of two fake
// transcode nodes and returns the handler, the start, and the node URLs.
type remoteReplanFixtureV3 struct {
	handler   *PlaybackHandler
	probe     *probeRecorderV3
	start     playback.StartRequestV3
	started   playback.DecisionResponseV3
	startedOn string
	other     string
	starts    func() int
}

func newRemoteReplanFixtureV3(t *testing.T) remoteReplanFixtureV3 {
	t.Helper()
	var mu sync.Mutex
	starts := 0
	newNode := func() *httptest.Server {
		node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
				writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: remoteHLSPlanV3().Transformations})
			case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
				var request transcodenode.TranscodeStartRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode remote start: %v", err)
				}
				mu.Lock()
				starts++
				mu.Unlock()
				writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: request.SessionID, Status: "started", AudioRecipeVersion: request.AudioRecipeVersion, ThrottleSeconds: request.ThrottleSeconds})
			default:
				w.WriteHeader(http.StatusNoContent)
			}
		}))
		t.Cleanup(node.Close)
		return node
	}
	first, second := newNode(), newNode()
	transcodes := nodepool.NewTranscodePool()
	transcodes.SetNodes([]*nodepool.Node{
		{ID: 1, URL: first.URL, Enabled: true, Healthy: true},
		{ID: 2, URL: second.URL, Enabled: true, Healthy: true},
	})

	file := v3HandlerFixtureFile(t)
	file.CodecVideo, file.VideoTracks[0].Codec = "mpeg4", "mpeg4"
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.JWTSecret = "test-secret"
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"transcode_enabled": "true", "allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	handler.NodePlanner = nodepool.NewPlanner(nodepool.NewProxyPool(), transcodes)
	requireWorkerRoutingV3(handler)
	handler.v3Registry = playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: playback.TransformationVideoToH264V3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3, Available: true},
	})
	recorder := &probeRecorderV3{dead: map[string]bool{}}
	handler.v3RouteNodeProbe = recorder.probe

	startRequest := v3HandlerStartRequest()
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true}
	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext()))
	var started playback.DecisionResponseV3
	if startRR.Code != http.StatusCreated || json.Unmarshal(startRR.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil {
		t.Fatalf("start status=%d body=%s", startRR.Code, startRR.Body.String())
	}
	session, err := handler.sessionMgr.GetSession(started.SessionID)
	if err != nil || session.TranscodeNodeURL == "" {
		t.Fatalf("start did not place the transcode on a node: %#v", session)
	}
	other := second.URL
	if session.TranscodeNodeURL == second.URL {
		other = first.URL
	}
	return remoteReplanFixtureV3{
		handler: handler, probe: recorder, start: startRequest, started: started,
		startedOn: session.TranscodeNodeURL, other: other,
		starts: func() int { mu.Lock(); defer mu.Unlock(); return starts },
	}
}

func (f remoteReplanFixtureV3) killStartedNode() {
	f.probe.mu.Lock()
	f.probe.dead[f.startedOn] = true
	f.probe.mu.Unlock()
}

func (f remoteReplanFixtureV3) replan(t *testing.T, operation playback.ReplanOperationV3, failure playback.FailureV3, attempted []string) playback.DecisionResponseV3 {
	t.Helper()
	return postPlaybackReplanV3(t, f.handler, f.started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             operation,
		PlaybackAttemptID:     f.start.PlaybackAttemptID,
		ReplanRequestID:       "dead-node-replan-0001",
		FailedPlanID:          f.started.PlaybackPlan.PlanID,
		PlanAttemptID:         "dead-node-plan-attempt-0001",
		PlanAttemptKey:        f.started.PlaybackPlan.PlanAttemptKey,
		AttemptedPlanKeys:     attempted,
		AttemptCount:          1,
		PositionSeconds:       120,
		Failure:               failure,
		SelectedTracks:        f.started.PlaybackPlan.SelectedTracks,
		Capabilities:          f.start.Capabilities,
		ClientPlaybackContext: f.start.ClientPlaybackContext,
	})
}

func (f remoteReplanFixtureV3) currentNode(t *testing.T) string {
	t.Helper()
	session, err := f.handler.sessionMgr.GetSession(f.started.SessionID)
	if err != nil {
		t.Fatalf("replan dropped the session: %v", err)
	}
	return session.TranscodeNodeURL
}

// A replan that changes nothing keeps a working remote transport, and moves
// one whose node no longer answers.
func TestHandleReplanPlaybackV3MovesTrackChangeOffAnUnreachableNode(t *testing.T) {
	t.Run("node answers", func(t *testing.T) {
		f := newRemoteReplanFixtureV3(t)
		replanned := f.replan(t, playback.ReplanOperationTrackChangeV3, playback.FailureV3{}, nil)
		if replanned.PlaybackPlan == nil {
			t.Fatalf("replan returned no plan: %#v", replanned)
		}
		if !slices.Contains(f.probe.probedURLs(), f.startedOn) {
			t.Fatalf("replan probed %v, want the current node %s", f.probe.probedURLs(), f.startedOn)
		}
		if node := f.currentNode(t); node != f.startedOn || replanned.PlaybackPlan.Stream.URL != f.started.PlaybackPlan.Stream.URL {
			t.Fatalf("working transport was replaced: node %s -> %s, url %s -> %s", f.startedOn, node, f.started.PlaybackPlan.Stream.URL, replanned.PlaybackPlan.Stream.URL)
		}
	})
	t.Run("node unreachable", func(t *testing.T) {
		f := newRemoteReplanFixtureV3(t)
		f.killStartedNode()
		replanned := f.replan(t, playback.ReplanOperationTrackChangeV3, playback.FailureV3{}, nil)
		if replanned.PlaybackPlan == nil {
			t.Fatalf("replan returned no plan: %#v", replanned)
		}
		if node := f.currentNode(t); node != f.other {
			t.Fatalf("replanned node = %s, want %s (started on %s)", node, f.other, f.startedOn)
		}
		if replanned.PlaybackPlan.Stream.URL == f.started.PlaybackPlan.Stream.URL {
			t.Fatalf("replan kept the stream URL of the unreachable node: %s", replanned.PlaybackPlan.Stream.URL)
		}
		if n := f.starts(); n != 2 {
			t.Fatalf("remote starts = %d, want the original start and one replacement", n)
		}
	})
}

// A failure recovery from a node that stopped answering keeps the failed
// recipe: the node failed, not the plan, so the same recipe runs on another
// node instead of being excluded.
func TestHandleReplanPlaybackV3FailureRecoveryFromAnUnreachableNodeKeepsTheRecipe(t *testing.T) {
	f := newRemoteReplanFixtureV3(t)
	key := f.started.PlaybackPlan.PlanAttemptKey
	f.killStartedNode()
	replanned := f.replan(t, playback.ReplanOperationFailureRecoveryV3, playback.FailureV3{Classification: "http_failure"}, []string{key})
	if replanned.PlaybackPlan == nil {
		t.Fatalf("recovery returned no plan: %#v", replanned)
	}
	if replanned.PlaybackPlan.PlanAttemptKey != key {
		t.Fatalf("recovery recipe = %s, want the unfailed recipe %s", replanned.PlaybackPlan.PlanAttemptKey, key)
	}
	if node := f.currentNode(t); node != f.other {
		t.Fatalf("recovered on %s, want %s", node, f.other)
	}

	// With the node answering, the same request still excludes the recipe.
	g := newRemoteReplanFixtureV3(t)
	gKey := g.started.PlaybackPlan.PlanAttemptKey
	recovered := g.replan(t, playback.ReplanOperationFailureRecoveryV3, playback.FailureV3{Classification: "http_failure"}, []string{gKey})
	if recovered.PlaybackPlan != nil && recovered.PlaybackPlan.PlanAttemptKey == gKey {
		t.Fatalf("recovery from a reachable node reused the failed recipe %s", gKey)
	}
}

// Planning reads capabilities from every pooled node; a node the replan found
// unreachable is skipped, so the plan is not built on its stale inventory and
// the read does not wait on it.
func TestPlanningCapabilityReadsSkipUnreachableNodes(t *testing.T) {
	var deadRequests atomic.Int32
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadRequests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(dead.Close)
	live := remoteTranscodeNodeStubV3(t)
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"

	ctx := withUnreachableRouteNodesV3(context.Background(), map[string]struct{}{dead.URL: {}})
	capabilities := handler.pooledNodeCapabilitiesV3(ctx, []string{dead.URL, live.URL})
	if _, ok := capabilities[live.URL]; !ok {
		t.Fatalf("capabilities = %v, want the live node's inventory", capabilities)
	}
	if _, ok := capabilities[dead.URL]; ok {
		t.Fatal("the unreachable node contributed capabilities")
	}
	if n := deadRequests.Load(); n != 0 {
		t.Fatalf("the unreachable node received %d capability requests", n)
	}
}

// Clients echo plan keys opaquely, and planning compares them trimmed, so a
// padded copy of the failed key must not exclude the recipe either.
func TestHandleReplanPlaybackV3NodeRecoveryIgnoresPaddedFailedKeys(t *testing.T) {
	f := newRemoteReplanFixtureV3(t)
	key := f.started.PlaybackPlan.PlanAttemptKey
	f.killStartedNode()
	replanned := f.replan(t, playback.ReplanOperationFailureRecoveryV3, playback.FailureV3{Classification: "http_failure"}, []string{key + " "})
	if replanned.PlaybackPlan == nil || replanned.PlaybackPlan.PlanAttemptKey != key {
		t.Fatalf("recovery = %#v, want the unfailed recipe %s", replanned.PlaybackPlan, key)
	}
	if node := f.currentNode(t); node != f.other {
		t.Fatalf("recovered on %s, want %s", node, f.other)
	}
}

// A progressive remux under worker-only routing runs only behind a proxy. When
// the only proxy is one this request found not answering, the remux has no
// route, so it escalates to HLS on a transcode node rather than failing on
// route capacity.
func TestEscalateRefusedProgressiveRemuxV3TreatsAnUnreachableProxyAsAbsent(t *testing.T) {
	const proxyURL = "http://proxy-1"
	for _, tt := range []struct {
		name string
		mode mediaAuthModeV3
	}{
		{name: "signed URLs", mode: mediaAuthModeV3{}},
		{name: "authorized origins", mode: authorizedOriginsModeV3()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handler, input, result := escalationFixtureV3(t, true)
			handler.JWTSecret = "test-secret"
			handler.NodePlanner.(*recordingNodePlannerV3).plan.ProxyNode = &nodepool.Node{URL: proxyURL}
			handler.ProxyGrantStore = &recordingRecipeCardStoreV3{}
			plannerInput := func() playback.PlannerInputV3 { return input }

			kept, transportErr := handler.escalateRefusedProgressiveRemuxV3(context.Background(), tt.mode, plannerInput, result)
			if transportErr != nil || kept.Plan == nil || kept.Plan.Delivery != playback.DeliveryRemuxProgressiveV3 {
				t.Fatalf("with the proxy answering: %s %#v, want the progressive remux kept", playback.ExplainPlannerResultV3(kept), transportErr)
			}

			ctx := withUnreachableRouteNodesV3(context.Background(), map[string]struct{}{proxyURL: {}})
			escalated, transportErr := handler.escalateRefusedProgressiveRemuxV3(ctx, tt.mode, plannerInput, result)
			if transportErr != nil {
				t.Fatalf("escalation error = %#v", transportErr)
			}
			if escalated.Terminal != nil || escalated.Plan == nil || escalated.Plan.Delivery != playback.DeliveryRemuxHLSV3 {
				t.Fatalf("escalated result = %s, want %q", playback.ExplainPlannerResultV3(escalated), playback.DeliveryRemuxHLSV3)
			}
		})
	}
}
