package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

// recordingRecipeCardStoreV3 stands in for either key space of the shared Redis
// recipe store: it records what a proxy would be told to serve, or what a
// restarted transcode node would rebuild from, so a test can assert on the
// authority the URL depends on rather than only on the URL's shape.
type recordingRecipeCardStoreV3 struct {
	disabled  bool
	putErr    error
	deleteErr error
	cards     map[string]playback.RecipeCard
	deleted   []string
	// ops is the ordered call log ("get", "put", "delete"), so a test can
	// assert that a replan read the grant it displaced before overwriting it.
	ops []string
}

func (s *recordingRecipeCardStoreV3) Enabled() bool { return !s.disabled }

func (s *recordingRecipeCardStoreV3) Get(_ context.Context, sessionID string) (*playback.RecipeCard, bool) {
	s.ops = append(s.ops, "get")
	card, ok := s.cards[sessionID]
	if !ok {
		return nil, false
	}
	return &card, true
}

func (s *recordingRecipeCardStoreV3) Put(_ context.Context, sessionID string, card playback.RecipeCard) error {
	s.ops = append(s.ops, "put")
	if s.putErr != nil {
		return s.putErr
	}
	if s.cards == nil {
		s.cards = map[string]playback.RecipeCard{}
	}
	s.cards[sessionID] = card
	return nil
}

func (s *recordingRecipeCardStoreV3) Delete(_ context.Context, sessionID string) error {
	s.ops = append(s.ops, "delete")
	s.deleted = append(s.deleted, sessionID)
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.cards, sessionID)
	return nil
}

func authorizedOriginsModeV3() mediaAuthModeV3 {
	return headerAuthenticatedMediaV3([]string{playback.FeatureHeaderAuthenticatedMediaV3, playback.FeatureAuthorizedMediaOriginsV3})
}

// The point of the mode: a header-authenticated attempt egresses from the pool
// again. The URL must name the proxy and carry no credential of any kind, and
// the proxy must have been handed the recipe it will serve from.
func TestPrepareTransportV3AuthorizedOriginsRestoreDirectPlayProxyEgress(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	planner := &recordingNodePlannerV3{plan: nodepool.Plan{ProxyNode: &nodepool.Node{ID: 51, URL: "http://proxy-1"}}}
	handler.NodePlanner = planner
	grants := &recordingRecipeCardStoreV3{}
	handler.ProxyGrantStore = grants
	file := v3HandlerFixtureFile(t)

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-origin-direct", UserID: 7, ProfileID: "profile-1"},
		file,
		playback.PlannerResultV3{Plan: identityProxyPlanV3(playback.DeliveryOriginalHTTPV3), PlayMethod: playback.PlayDirect},
		authorizedOriginsModeV3())
	if transportErr != nil {
		t.Fatalf("prepare identity transport: %v", transportErr)
	}

	if transport.url != "http://proxy-1/stream/v3/session-origin-direct" {
		t.Fatalf("stream url = %q, want the credential-free proxy route", transport.url)
	}
	assertNoPlaybackCredentialV3(t, transport.url)

	card, ok := grants.cards["session-origin-direct"]
	if !ok {
		t.Fatal("no grant was written; the proxy has nothing to serve this session from")
	}
	if card.InputPath != file.FilePath {
		t.Fatalf("grant media path = %q, want %q", card.InputPath, file.FilePath)
	}
	if card.UserID != 7 || card.PlayMethod != playback.PlayDirect {
		t.Fatalf("grant identity = %#v, want the session's owner and play method", card)
	}
	if card.RoutingEgressNodeID != 51 {
		t.Fatalf("grant egress node ID = %d, want 51", card.RoutingEgressNodeID)
	}

	// A transport that never reaches the client must not leave a live grant
	// behind, or the proxy keeps serving playback the server considers over.
	transport.rollback()
	if len(grants.deleted) != 1 || grants.deleted[0] != "session-origin-direct" {
		t.Fatalf("grants deleted on rollback = %v, want the session's grant revoked", grants.deleted)
	}
	if len(planner.released) != 1 {
		t.Fatalf("planner releases = %v, want the proxy reservation released", planner.released)
	}
}

// A replan overwrites the grant of a session that is already proxy-served. If
// the replacement never commits, the client is left on the OLD plan's proxy
// URL — which only resolves while the OLD grant exists. Rolling back therefore
// has to put the displaced grant back, not revoke it.
func TestPrepareTransportV3AuthorizedOriginsRollbackRestoresTheDisplacedGrant(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{ProxyNode: &nodepool.Node{URL: "http://proxy-1"}}}
	priorCard := playback.RecipeCard{SessionID: "session-origin-replan", UserID: 7, InputPath: "/media/previous-plan.mkv"}
	grants := &recordingRecipeCardStoreV3{cards: map[string]playback.RecipeCard{"session-origin-replan": priorCard}}
	handler.ProxyGrantStore = grants
	file := v3HandlerFixtureFile(t)

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-origin-replan", UserID: 7, ProfileID: "profile-1"},
		file,
		playback.PlannerResultV3{Plan: identityProxyPlanV3(playback.DeliveryOriginalHTTPV3), PlayMethod: playback.PlayDirect},
		authorizedOriginsModeV3())
	if transportErr != nil {
		t.Fatalf("prepare identity transport: %v", transportErr)
	}
	if got := grants.cards["session-origin-replan"].InputPath; got != file.FilePath {
		t.Fatalf("grant media path after prepare = %q, want the replacement plan's %q", got, file.FilePath)
	}

	transport.rollback()

	restored, ok := grants.cards["session-origin-replan"]
	if !ok {
		t.Fatal("rollback revoked the grant; the restored plan's published proxy URL now 404s")
	}
	if restored.InputPath != priorCard.InputPath {
		t.Fatalf("restored grant media path = %q, want the previous plan's %q", restored.InputPath, priorCard.InputPath)
	}
	if len(grants.deleted) != 0 {
		t.Fatalf("grants deleted = %v, want none: the session is still proxy-served", grants.deleted)
	}
	if grants.ops[0] != "get" {
		t.Fatalf("store ops = %v, want the displaced grant read before it was overwritten", grants.ops)
	}
}

// The mirror defect: a replan that lands on a transport this server serves
// itself publishes a URL the proxy has no part in. Leaving the grant alive
// would keep the proxy authorized to serve the previous recipe for the rest of
// its TTL, so committing off the proxy revokes it.
func TestPrepareTransportV3AuthorizedOriginsCommitOffTheProxyRevokesTheGrant(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	// No proxy in the plan: direct play needs no server work, so this attempt
	// legitimately commits onto the API-local identity route.
	handler.NodePlanner = &recordingNodePlannerV3{}
	grants := &recordingRecipeCardStoreV3{cards: map[string]playback.RecipeCard{
		"session-origin-offproxy": {SessionID: "session-origin-offproxy", UserID: 7, InputPath: "/media/previous-plan.mkv"},
	}}
	handler.ProxyGrantStore = grants

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-origin-offproxy", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{Plan: identityProxyPlanV3(playback.DeliveryOriginalHTTPV3), PlayMethod: playback.PlayDirect},
		authorizedOriginsModeV3())
	if transportErr != nil {
		t.Fatalf("prepare identity transport: %v", transportErr)
	}
	if transport.url != "/stream/session-origin-offproxy" {
		t.Fatalf("stream url = %q, want the API-local route", transport.url)
	}

	transport.commit()

	if len(grants.deleted) != 1 || grants.deleted[0] != "session-origin-offproxy" {
		t.Fatalf("grants deleted on commit = %v, want the stale proxy authority revoked", grants.deleted)
	}
	if _, ok := grants.cards["session-origin-offproxy"]; ok {
		t.Fatal("a grant survived a commit onto a transport the proxy does not serve")
	}
}

// A remux egresses from the proxy too, and the grant has to carry the source
// facts the proxy cannot look up: without them it would serve a subtly
// different stream than the plan promised.
func TestPrepareTransportV3AuthorizedOriginsCarryRemuxSourceFacts(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	stubCopySeekAnchorV3(handler)
	proxy := capableProxyStubV3(t)
	handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{ProxyNode: &nodepool.Node{URL: proxy.URL + "/"}}}
	grants := &recordingRecipeCardStoreV3{}
	handler.ProxyGrantStore = grants

	file := v3HandlerFixtureFile(t)
	file.VideoTracks[0].DVProfile = 7
	plan := identityProxyPlanV3(playback.DeliveryRemuxProgressiveV3, playback.TransformationV3{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3})
	plan.Timeline = playback.TimelineV3{SourceStartSeconds: 39.5}

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-origin-remux", UserID: 7, ProfileID: "profile-1"},
		file,
		playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux, TranscodeAudio: true, TargetAudioCodec: "aac", SourceAudioChannels: 6, TargetAudioChannels: 2},
		authorizedOriginsModeV3())
	if transportErr != nil {
		t.Fatalf("prepare identity transport: %v", transportErr)
	}
	defer transport.rollback()

	want := proxy.URL + "/stream/v3/session-origin-remux?seek=39.5"
	if transport.url != want {
		t.Fatalf("stream url = %q, want %q", transport.url, want)
	}
	assertNoPlaybackCredentialV3(t, transport.url)

	card := grants.cards["session-origin-remux"]
	if card.DVProfile != 7 {
		t.Fatalf("grant DV profile = %d, want 7 so the proxy strips the dangling RPU", card.DVProfile)
	}
	if !card.TranscodeAudio {
		t.Fatal("grant must tell the proxy to convert audio")
	}
	if card.SourceAudioChannels != 6 || card.TargetAudioChannels != 2 {
		t.Fatalf("grant audio recipe = source %d target %d, want 6 to 2", card.SourceAudioChannels, card.TargetAudioChannels)
	}
}

// A grant that cannot be stored is not fatal: the attempt degrades to exactly
// what a header-authenticated attempt without origins would have gotten.
func TestPrepareTransportV3AuthorizedOriginsFallBackToTheAPIWhenTheGrantFails(t *testing.T) {
	for _, test := range []struct {
		name  string
		store *recordingRecipeCardStoreV3
	}{
		{name: "write error", store: &recordingRecipeCardStoreV3{putErr: errors.New("redis is down")}},
		{name: "store disabled", store: &recordingRecipeCardStoreV3{disabled: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
			handler.JWTSecret = "test-secret"
			planner := &recordingNodePlannerV3{plan: nodepool.Plan{ProxyNode: &nodepool.Node{URL: "http://proxy-1"}}}
			handler.NodePlanner = planner
			handler.ProxyGrantStore = test.store

			transport, transportErr := handler.prepareTransportV3(
				httptest.NewRequest(http.MethodPost, "/", nil),
				&playback.Session{ID: "session-origin-fallback", UserID: 7, ProfileID: "profile-1"},
				v3HandlerFixtureFile(t),
				playback.PlannerResultV3{Plan: identityProxyPlanV3(playback.DeliveryOriginalHTTPV3), PlayMethod: playback.PlayDirect},
				authorizedOriginsModeV3())
			if transportErr != nil {
				t.Fatalf("prepare identity transport: %v", transportErr)
			}
			defer transport.rollback()

			if transport.url != "/stream/session-origin-fallback" {
				t.Fatalf("stream url = %q, want the API-local route", transport.url)
			}
			if len(planner.released) != 1 {
				t.Fatalf("planner releases = %v, want the unusable proxy reservation released", planner.released)
			}
		})
	}
}

// The grant-failure fallback lands on the API server, which is ffmpeg work for
// a remux carrying a server transformation. An operator who disabled
// worker-only remux routing forbids exactly that, so the fallback has
// to honor the same gate the no-origins mode enforces rather than quietly
// spawning an encoder. Escalation cannot cover this: it was legitimately
// skipped at plan time because the pool does offer a proxy.
//
// The pool offering one is also why the refusal is retryable: the policy is
// satisfiable and only this attempt's grant write failed.
func TestPrepareTransportV3AuthorizedOriginsRefuseLocalRemuxWhenTheGrantFails(t *testing.T) {
	handler, _, result := escalationFixtureV3(t, true)
	proxy := capableProxyStubV3(t)
	planner := &recordingNodePlannerV3{plan: nodepool.Plan{ProxyNode: &nodepool.Node{URL: proxy.URL}}}
	handler.NodePlanner = planner
	handler.ProxyGrantStore = &recordingRecipeCardStoreV3{putErr: errors.New("redis is down")}

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-origin-refused", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		result,
		authorizedOriginsModeV3())
	if transportErr == nil {
		transport.rollback()
		t.Fatalf("grant failure produced an API-local remux at %q; local fallback is disabled", transport.url)
	}
	if transportErr.reason != string(noderouting.OutcomeCapacityUnavailable) || !transportErr.retryable {
		t.Fatalf("transport error = %#v, want a retryable proxy-egress capacity failure", transportErr)
	}
	if len(planner.released) != 1 {
		t.Fatalf("planner releases = %v, want the unusable proxy reservation released", planner.released)
	}
}

func TestPrepareTransportV3AuthorizedOriginsFallsBackWhenTranscodeProxyGrantFails(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	stubCopySeekAnchorV3(handler)
	proxyServer := capableProxyStubV3(t)
	transcodeServer := progressiveRemuxExecutionStubV3(t)
	planner := &progressiveFallbackPlannerV3{
		proxy:     &nodepool.Node{ID: 42, URL: proxyServer.URL},
		transcode: &nodepool.Node{ID: 84, URL: transcodeServer.URL},
	}
	handler.NodePlanner = planner
	recipes := &recordingRecipeCardStoreV3{}
	handler.NodeRecipeStore = recipes
	handler.ProxyGrantStore = &recordingRecipeCardStoreV3{putErr: errors.New("redis is down")}
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.RemuxExecution = config.PlaybackExecutionPreferTranscode
	handler.PlaybackConfig = func() config.PlaybackConfig { return config.PlaybackConfig{Routing: policy} }

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-origin-grant-fallback", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{Plan: identityProxyPlanV3(playback.DeliveryRemuxProgressiveV3), PlayMethod: playback.PlayRemux},
		authorizedOriginsModeV3())
	if transportErr != nil {
		t.Fatalf("prepare identity transport: %v", transportErr)
	}
	defer transport.rollback()

	if transport.url != "/stream/session-origin-grant-fallback" ||
		transport.routingExecution != noderouting.ExecutionAPI || transport.nodeURL != "" {
		t.Fatalf("fallback transport = %#v, want API-executed remux", transport)
	}
	if len(planner.requests) != 2 || !planner.requests[0].NeedsTranscode || !planner.requests[0].NeedsProxy ||
		planner.requests[1].NeedsTranscode || !planner.requests[1].NeedsProxy {
		t.Fatalf("route requests = %#v, want transcode+proxy then proxy-only", planner.requests)
	}
	if len(planner.released) != 2 {
		t.Fatalf("released reservations = %v, want both unusable proxy routes released", planner.released)
	}
	if len(recipes.deleted) != 1 {
		t.Fatalf("node authority cleanup = %v, want failed transport deleted", recipes.deleted)
	}
}

func TestPrepareTransportV3AuthorizedOriginsDoNotFallBackToAnIncapableAPI(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	stubCopySeekAnchorV3(handler)
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{{
		Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3,
	}}))
	proxy := capableProxyStubV3(t)
	planner := &recordingNodePlannerV3{plan: nodepool.Plan{ProxyNode: &nodepool.Node{URL: proxy.URL}}}
	handler.NodePlanner = planner
	handler.ProxyGrantStore = &recordingRecipeCardStoreV3{putErr: errors.New("redis is down")}
	handler.NodeRecipeStore = &recordingRecipeCardStoreV3{}

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-origin-incapable-api", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{
			Plan: identityProxyPlanV3(playback.DeliveryRemuxProgressiveV3, playback.TransformationV3{
				Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3,
				RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3,
			}),
			PlayMethod: playback.PlayRemux, TranscodeAudio: true, TargetAudioCodec: "aac",
		}, authorizedOriginsModeV3())
	if transportErr == nil {
		transport.rollback()
		t.Fatalf("grant failure produced an API-local remux at %q even though the API lacks the recipe", transport.url)
	}
	if transportErr.reason != string(noderouting.OutcomeCapacityUnavailable) || !transportErr.retryable {
		t.Fatalf("transport error = %#v, want a retryable proxy-egress capacity failure", transportErr)
	}
	if len(planner.released) != 2 {
		t.Fatalf("planner releases = %v, want the unusable transcode and proxy reservations released", planner.released)
	}
}

// Without the origins opt-in the mode is unchanged from what PR #723 shipped:
// everything stays on the API, and no grant is written at all.
func TestPrepareTransportV3HeaderAuthOnlyStaysOnTheAPIOrigin(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{ProxyNode: &nodepool.Node{URL: "http://proxy-1"}}}
	grants := &recordingRecipeCardStoreV3{}
	handler.ProxyGrantStore = grants

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-header-only", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{Plan: identityProxyPlanV3(playback.DeliveryOriginalHTTPV3), PlayMethod: playback.PlayDirect},
		headerAuthenticatedMediaV3([]string{playback.FeatureHeaderAuthenticatedMediaV3}))
	if transportErr != nil {
		t.Fatalf("prepare identity transport: %v", transportErr)
	}
	defer transport.rollback()

	if transport.url != "/stream/session-header-only" {
		t.Fatalf("stream url = %q, want the API-local route", transport.url)
	}
	if len(grants.cards) != 0 {
		t.Fatalf("grants written = %v, want none for an attempt that negotiated no origins", grants.cards)
	}
}

// HLS keeps its pooled transcode node and gets its proxy back: the manifest is
// fetched from the proxy, whose relative segment URIs stay inside the same
// credential-free /stream/v3 family.
func TestPrepareTransportV3AuthorizedOriginsPublishGrantBackedHLSManifest(t *testing.T) {
	var startRequest transcodenode.TranscodeStartRequest
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: []playback.TransformationV3{
				{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3},
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			if err := json.NewDecoder(r.Body).Decode(&startRequest); err != nil {
				t.Errorf("decode remote start: %v", err)
			}
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: startRequest.SessionID, Status: "started"})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer node.Close()

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	planner := &recordingNodePlannerV3{plan: nodepool.Plan{TranscodeNode: &nodepool.Node{URL: node.URL}, ProxyNode: &nodepool.Node{ID: 52, URL: "http://proxy-1"}}}
	handler.NodePlanner = planner
	grants := &recordingRecipeCardStoreV3{}
	handler.ProxyGrantStore = grants

	plan := &playback.PlanV3{
		PlanID:          "plan:origin-hls",
		Delivery:        playback.DeliveryTranscodeHLSV3,
		Transformations: []playback.TransformationV3{{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3}},
	}
	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-origin-hls", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayTranscode, TargetVideoCodec: "h264", TargetAudioCodec: "aac"},
		authorizedOriginsModeV3())
	if transportErr != nil {
		t.Fatalf("prepare remote transport: %v", transportErr)
	}
	defer transport.rollback()

	if transport.url != "http://proxy-1/stream/v3/session-origin-hls/master.m3u8" {
		t.Fatalf("manifest url = %q, want the credential-free proxy manifest", transport.url)
	}
	assertNoPlaybackCredentialV3(t, transport.url)

	card, ok := grants.cards["session-origin-hls"]
	if !ok {
		t.Fatal("no grant was written; the proxy cannot relay this transcode")
	}
	if card.TranscodeNodeURL != node.URL {
		t.Fatalf("grant transcode node = %q, want %q", card.TranscodeNodeURL, node.URL)
	}
	if card.TranscodeTransportID != transport.transportID {
		t.Fatalf("grant transport id = %q, want the plan-scoped transport %q", card.TranscodeTransportID, transport.transportID)
	}
	if card.RoutingEgressNodeID != 52 {
		t.Fatalf("grant egress node ID = %d, want 52", card.RoutingEgressNodeID)
	}
}

// The escalation exists because header-authenticated identity work had no
// executor. Authorized origins give it one, so an attempt with a proxy
// available must keep the route the planner chose.
func TestEscalateRefusedProgressiveRemuxV3SkipsEscalationWhenOriginsHaveAProxy(t *testing.T) {
	handler, input, result := escalationFixtureV3(t, true)
	handler.NodePlanner.(*recordingNodePlannerV3).plan.ProxyNode = &nodepool.Node{URL: "http://proxy-1"}
	handler.ProxyGrantStore = &recordingRecipeCardStoreV3{}

	escalated, transportErr := handler.escalateRefusedProgressiveRemuxV3(context.Background(), authorizedOriginsModeV3(), func() playback.PlannerInputV3 { return input }, result)
	if transportErr != nil {
		t.Fatalf("escalation error = %#v", transportErr)
	}
	if escalated.Plan == nil || escalated.Plan.Delivery != playback.DeliveryRemuxProgressiveV3 {
		t.Fatalf("escalated delivery = %#v, want the planned progressive remux left alone", escalated.Plan)
	}
}

// With origins negotiated but no proxy in the pool the refusal is back, so the
// escalation must be too — otherwise the attempt plans a route nothing can run.
func TestEscalateRefusedProgressiveRemuxV3StillEscalatesWithoutAnyProxyOrigin(t *testing.T) {
	handler, input, result := escalationFixtureV3(t, true)
	handler.ProxyGrantStore = &recordingRecipeCardStoreV3{}

	escalated, transportErr := handler.escalateRefusedProgressiveRemuxV3(context.Background(), authorizedOriginsModeV3(), func() playback.PlannerInputV3 { return input }, result)
	if transportErr != nil {
		t.Fatalf("escalation error = %#v", transportErr)
	}
	if escalated.Plan == nil || escalated.Plan.Delivery != playback.DeliveryRemuxHLSV3 {
		t.Fatalf("escalated delivery = %#v, want %q", escalated.Plan, playback.DeliveryRemuxHLSV3)
	}
}

// A proxy the server can name but cannot authorize is no executor at all: the
// origin URL is only serviceable while a grant backs it. Without a usable grant
// store this process can never publish one, so the refusal is permanent and the
// escalation has to run — otherwise the remux sits on a retryable
// capacity_unavailable that nothing in this deployment will ever satisfy.
func TestEscalateRefusedProgressiveRemuxV3StillEscalatesWithoutAUsableGrantStore(t *testing.T) {
	for _, test := range []struct {
		name  string
		store recipeCardStoreV3
	}{
		{name: "no grant store", store: nil},
		{name: "grant store disabled", store: &recordingRecipeCardStoreV3{disabled: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, input, result := escalationFixtureV3(t, true)
			handler.NodePlanner.(*recordingNodePlannerV3).plan.ProxyNode = &nodepool.Node{URL: "http://proxy-1"}
			handler.ProxyGrantStore = test.store

			escalated, transportErr := handler.escalateRefusedProgressiveRemuxV3(context.Background(), authorizedOriginsModeV3(), func() playback.PlannerInputV3 { return input }, result)
			if transportErr != nil {
				t.Fatalf("escalation error = %#v", transportErr)
			}
			if escalated.Plan == nil || escalated.Plan.Delivery != playback.DeliveryRemuxHLSV3 {
				t.Fatalf("escalated delivery = %#v, want %q", escalated.Plan, playback.DeliveryRemuxHLSV3)
			}
		})
	}
}

// assertNoPlaybackCredentialV3 fails when a published URL carries any playback
// credential — the whole promise of the mode, on the proxy origin as much as on
// the API one.
func assertNoPlaybackCredentialV3(t *testing.T, rawURL string) {
	t.Helper()
	if strings.Contains(rawURL, streamTokenParam+"=") || strings.Contains(rawURL, "/stream/direct/") ||
		strings.Contains(rawURL, "/stream/remux/") || strings.Contains(rawURL, "/stream/transcode/") {
		t.Fatalf("URL %q carries a playback credential", rawURL)
	}
}
