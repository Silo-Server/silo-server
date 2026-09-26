package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

// remoteTranscodeNodeStubV3 is a pooled transcode node that advertises the H.264
// recipe and accepts a start, so a test can drive prepareRemoteTransportV3
// without ffmpeg.
func remoteTranscodeNodeStubV3(t *testing.T) *httptest.Server {
	t.Helper()
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: []playback.TransformationV3{
				{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3},
				{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			var request transcodenode.TranscodeStartRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode remote start: %v", err)
			}
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: request.SessionID, Status: "started", AudioRecipeVersion: request.AudioRecipeVersion})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(node.Close)
	return node
}

func remoteHLSPlanV3() *playback.PlanV3 {
	return &playback.PlanV3{
		PlanID:   "plan:node-recipe",
		Delivery: playback.DeliveryTranscodeHLSV3,
		Transformations: []playback.TransformationV3{
			{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3},
			{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
		},
	}
}

func remoteHLSResultV3() playback.PlannerResultV3 {
	return playback.PlannerResultV3{Plan: remoteHLSPlanV3(), PlayMethod: playback.PlayTranscode, TranscodeAudio: true, TargetVideoCodec: "h264", TargetAudioCodec: "aac", SourceAudioChannels: 6, TargetAudioChannels: 2}
}

func TestPrepareRemoteTransportV3VersionsOnlyExactAACStereoDownmix(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*playback.PlannerResultV3)
		wantSource int
		wantTarget int
		wantRecipe string
	}{
		{name: "explicit stereo", wantSource: 6, wantTarget: 2, wantRecipe: playback.TransformationAudioToAACRecipeVersionV3},
		{name: "default stereo", mutate: func(r *playback.PlannerResultV3) { r.TargetAudioChannels = 0 }, wantSource: 6, wantTarget: 2, wantRecipe: playback.TransformationAudioToAACRecipeVersionV3},
		{name: "stereo source", mutate: func(r *playback.PlannerResultV3) { r.SourceAudioChannels = 2 }, wantTarget: 2},
		{name: "surround output", mutate: func(r *playback.PlannerResultV3) { r.TargetAudioChannels = 6 }, wantTarget: 6},
		{name: "non AAC output", mutate: func(r *playback.PlannerResultV3) { r.TargetAudioCodec = "eac3" }, wantTarget: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got transcodenode.TranscodeStartRequest
			node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path == "/transcode/start" {
					if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
						t.Errorf("decode remote start: %v", err)
					}
					writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: got.SessionID, Status: "started", AudioRecipeVersion: got.AudioRecipeVersion})
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer node.Close()

			handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
			handler.JWTSecret = "test-secret"
			result := remoteHLSResultV3()
			if test.mutate != nil {
				test.mutate(&result)
			}
			transport, transportErr := handler.prepareRemoteTransportV3(
				httptest.NewRequest(http.MethodPost, "/", nil),
				&playback.Session{ID: "session-exact-recipe", UserID: 7, ProfileID: "profile-1"},
				v3HandlerFixtureFile(t), result,
				nodepool.Plan{TranscodeNode: &nodepool.Node{URL: node.URL}}, preparedTimelineV3{}, mediaAuthModeV3{},
			)
			if transportErr != nil {
				t.Fatalf("prepare remote transport: %v", transportErr)
			}
			defer transport.rollback()
			if got.SourceAudioChannels != test.wantSource || got.TargetAudioChannels != test.wantTarget || got.AudioRecipeVersion != test.wantRecipe {
				t.Fatalf("remote audio recipe = source %d, target %d, version %q; want %d, %d, %q", got.SourceAudioChannels, got.TargetAudioChannels, got.AudioRecipeVersion, test.wantSource, test.wantTarget, test.wantRecipe)
			}
		})
	}
}

func TestPrepareRemoteTransportV3RejectsOldNodeAfterStaleAudioCapabilityProbe(t *testing.T) {
	var startRequest transcodenode.TranscodeStartRequest
	stopRequests := 0
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
			// This is the cached answer from the v2 binary before it was replaced.
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: []playback.TransformationV3{
				{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3},
				{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			if err := json.NewDecoder(r.Body).Decode(&startRequest); err != nil {
				t.Errorf("decode remote start: %v", err)
			}
			// A pre-v2 node ignores both new request fields and omits the receipt.
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: startRequest.SessionID, Status: "started"})
		case r.Method == http.MethodDelete:
			stopRequests++
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer node.Close()

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{TranscodeNode: &nodepool.Node{URL: node.URL}}}
	policy := config.DefaultPlaybackRoutingPolicy()
	// This test exercises the stale worker's start receipt, not fallback to the
	// API host. Pin the executor boundary so the assertion cannot depend on a
	// concurrent local FFmpeg capability probe.
	policy.VideoTranscodeExecution = config.PlaybackExecutionWorkerOnly
	_, transportErr := handler.prepareTransportWithPolicyV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-stale-audio-node", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t), remoteHLSResultV3(), mediaAuthModeV3{}, policy)
	if transportErr == nil || transportErr.reason != transcodeStartFailedReasonV3 {
		t.Fatalf("transport error = %#v, want %q", transportErr, transcodeStartFailedReasonV3)
	}
	if startRequest.AudioRecipeVersion != playback.TransformationAudioToAACRecipeVersionV3 || startRequest.SourceAudioChannels != 6 {
		t.Fatalf("remote request = %#v, want source channels plus audio recipe v2", startRequest)
	}
	if stopRequests != 1 {
		t.Fatalf("stop requests = %d, want one cleanup for the unconfirmed job", stopRequests)
	}
}

// A header-authenticated attempt publishes no stream token, so when the node
// restarts neither the client nor this server's relay has a recipe to hand back
// — the node 404s until the client replans. The recipe therefore goes into the
// shared store the node reads, keyed by the TRANSPORT id the node serves the job
// under (not the playback session id, which is what the client URL carries).
func TestPrepareTransportV3HeaderAuthStoresTheNodeRecipeForRestartRecovery(t *testing.T) {
	for _, test := range []struct {
		name string
		mode mediaAuthModeV3
	}{
		// Both sub-modes need it: without authorized origins the API relays the
		// node itself, and with them the API relay is still the fallback whenever
		// the proxy URL is not published.
		{name: "header auth only", mode: headerAuthenticatedMediaV3([]string{playback.FeatureHeaderAuthenticatedMediaV3})},
		{name: "authorized origins", mode: authorizedOriginsModeV3()},
	} {
		t.Run(test.name, func(t *testing.T) {
			node := remoteTranscodeNodeStubV3(t)
			handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
			handler.JWTSecret = "test-secret"
			handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{TranscodeNode: &nodepool.Node{URL: node.URL}, ProxyNode: &nodepool.Node{URL: "http://proxy-1"}}}
			handler.ProxyGrantStore = &recordingRecipeCardStoreV3{}
			recipes := &recordingRecipeCardStoreV3{}
			handler.NodeRecipeStore = recipes

			transport, transportErr := handler.prepareTransportV3(
				httptest.NewRequest(http.MethodPost, "/", nil),
				&playback.Session{ID: "session-node-recipe", UserID: 7, ProfileID: "profile-1"},
				v3HandlerFixtureFile(t),
				remoteHLSResultV3(),
				test.mode)
			if transportErr != nil {
				t.Fatalf("prepare remote transport: %v", transportErr)
			}

			card, ok := recipes.cards[transport.transportID]
			if !ok {
				t.Fatalf("no recipe stored under transport %q; a node restart would 404 this session", transport.transportID)
			}
			if card.TranscodeNodeURL != node.URL || card.TargetCodecVideo != "h264" || card.SourceAudioChannels != 6 || card.TargetAudioChannels != 2 || card.SegmentDuration <= 0 {
				t.Fatalf("stored recipe = %#v, want the complete recipe the node accepted", card)
			}
			if _, keyedBySession := recipes.cards["session-node-recipe"]; keyedBySession {
				t.Fatal("recipe stored under the playback session id; the node serves under the transport id")
			}

			// A transport that never commits leaves no node job, so its recipe
			// must not survive to rebuild one.
			transport.rollback()
			if len(recipes.deleted) != 1 || recipes.deleted[0] != transport.transportID {
				t.Fatalf("recipes deleted on rollback = %v, want the transport's recipe dropped", recipes.deleted)
			}
		})
	}
}

func TestPrepareProgressiveTransportRollbackStopsAdmittedRemoteJob(t *testing.T) {
	var stoppedPath string
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{
				TransportFeatures: []string{playback.TransportFeatureProgressiveRemuxExecutionV1},
				Transformations: []playback.TransformationV3{{
					Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3,
					RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3,
				}},
			})
		case r.Method == http.MethodDelete:
			stoppedPath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(node.Close)
	proxy := capableProxyStubV3(t)

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodeRecipeStore = &recordingRecipeCardStoreV3{}
	handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{
		TranscodeNode: &nodepool.Node{ID: 84, URL: node.URL},
		ProxyNode:     &nodepool.Node{ID: 42, URL: proxy.URL},
	}}
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.RemuxExecution = config.PlaybackExecutionPreferTranscode
	handler.PlaybackConfig = func() config.PlaybackConfig { return config.PlaybackConfig{Routing: policy} }

	plan := identityProxyPlanV3(playback.DeliveryRemuxProgressiveV3, playback.TransformationV3{
		Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3,
		RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3,
	})
	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-progressive-rollback", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux, TranscodeAudio: true, SourceAudioChannels: 6, TargetAudioChannels: 2, TargetAudioCodec: "aac"},
		mediaAuthModeV3{},
	)
	if transportErr != nil {
		t.Fatalf("prepare progressive transport: %v", transportErr)
	}
	if transport.routingExecution != noderouting.ExecutionTranscode {
		t.Fatalf("routing execution = %q, want transcode", transport.routingExecution)
	}

	transport.rollback()

	if want := "/transcode/" + transport.transportID; stoppedPath != want {
		t.Fatalf("remote stop path = %q, want %q", stoppedPath, want)
	}
}

func TestPrepareProgressiveTransportRequiredRollbackPreservesRetryableRoute(t *testing.T) {
	deleteRequests := 0
	predecessorDeleteRequests := 0
	predecessor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/transcode/predecessor-transport" {
			predecessorDeleteRequests++
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(predecessor.Close)
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{
				TransportFeatures: []string{playback.TransportFeatureProgressiveRemuxExecutionV1},
				Transformations: []playback.TransformationV3{{
					Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3,
					RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3,
				}},
			})
		case r.Method == http.MethodDelete:
			deleteRequests++
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(node.Close)
	proxy := capableProxyStubV3(t)
	recipes := &recordingRecipeCardStoreV3{cards: map[string]playback.RecipeCard{
		"predecessor-transport": {},
	}}

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodeRecipeStore = recipes
	handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{
		TranscodeNode: &nodepool.Node{ID: 84, URL: node.URL},
		ProxyNode:     &nodepool.Node{ID: 42, URL: proxy.URL},
	}}
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.RemuxExecution = config.PlaybackExecutionPreferTranscode
	handler.PlaybackConfig = func() config.PlaybackConfig { return config.PlaybackConfig{Routing: policy} }

	plan := identityProxyPlanV3(playback.DeliveryRemuxProgressiveV3, playback.TransformationV3{
		Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3,
		RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3,
	})
	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{
			ID: "session-progressive-required-rollback", UserID: 7, ProfileID: "profile-1",
			TranscodeNodeURL: predecessor.URL, TranscodeTransportID: "predecessor-transport",
		},
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux, TranscodeAudio: true, SourceAudioChannels: 6, TargetAudioChannels: 2, TargetAudioCodec: "aac"},
		mediaAuthModeV3{},
	)
	if transportErr != nil {
		t.Fatalf("prepare progressive transport: %v", transportErr)
	}
	if transport.rollbackRequired == nil {
		t.Fatal("remote progressive transport has no required rollback")
	}

	if err := transport.rollbackRequired(); err == nil {
		t.Fatal("required rollback succeeded despite the node rejecting cancellation")
	}
	if deleteRequests != 1 {
		t.Fatalf("delete requests = %d, want one synchronous cancellation", deleteRequests)
	}
	if slices.Contains(recipes.deleted, transport.transportID) {
		t.Fatal("failed cancellation deleted the authority needed by the retryable live route")
	}
	if _, ok := recipes.cards[transport.transportID]; !ok {
		t.Fatal("failed cancellation did not preserve the node recipe authority")
	}
	if predecessorDeleteRequests != 1 {
		t.Fatalf("predecessor delete requests = %d, want one best-effort teardown", predecessorDeleteRequests)
	}
	if _, ok := recipes.cards["predecessor-transport"]; ok {
		t.Fatal("retained successor left the displaced predecessor recipe authoritative")
	}

	// A cancellation failure must still release the lifecycle boundary so the
	// retrying stop can acquire it instead of deadlocking behind this request.
	unlock := handler.tm.LockSessionLifecycle("session-progressive-required-rollback")
	unlock()
}

// A legacy attempt carries its whole recipe in the client's stream token, which
// the relay forwards to the node. It needs no stored copy, and writing one would
// put a media path in Redis for no reason.
func TestPrepareTransportV3LegacyAttemptStoresNoNodeRecipe(t *testing.T) {
	node := remoteTranscodeNodeStubV3(t)
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{TranscodeNode: &nodepool.Node{URL: node.URL}}}
	recipes := &recordingRecipeCardStoreV3{}
	handler.NodeRecipeStore = recipes

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-legacy-recipe", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		remoteHLSResultV3(),
		mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare remote transport: %v", transportErr)
	}
	defer transport.rollback()

	if len(recipes.cards) != 0 {
		t.Fatalf("recipes stored = %v, want none for a token-carrying attempt", recipes.cards)
	}
}

// Committing a replacement stops the previous node process, so the recipe that
// rebuilds it has to go with it — otherwise a buffered request could resurrect
// the transport the replan just retired.
func TestPrepareTransportV3RemoteCommitDropsThePreviousTransportRecipe(t *testing.T) {
	node := remoteTranscodeNodeStubV3(t)
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{TranscodeNode: &nodepool.Node{URL: node.URL}}}
	const previousTransportID = "session-node-replan-plan0001-aaaabbbb"
	recipes := &recordingRecipeCardStoreV3{cards: map[string]playback.RecipeCard{
		previousTransportID: {SessionID: "session-node-replan", TranscodeTransportID: previousTransportID},
	}}
	handler.NodeRecipeStore = recipes

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{
			ID: "session-node-replan", UserID: 7, ProfileID: "profile-1",
			TranscodeNodeURL: node.URL, TranscodeTransportID: previousTransportID,
		},
		v3HandlerFixtureFile(t),
		remoteHLSResultV3(),
		headerAuthenticatedMediaV3([]string{playback.FeatureHeaderAuthenticatedMediaV3}))
	if transportErr != nil {
		t.Fatalf("prepare remote transport: %v", transportErr)
	}

	transport.commit()

	if len(recipes.deleted) != 1 || recipes.deleted[0] != previousTransportID {
		t.Fatalf("recipes deleted on commit = %v, want the replaced transport's recipe dropped", recipes.deleted)
	}
	if _, ok := recipes.cards[transport.transportID]; !ok {
		t.Fatal("commit dropped the recipe of the transport it just committed")
	}
}

// A stopped session's node job is gone, so its stored recipe must be too.
func TestFinalizeSessionStopDropsTheNodeRecipe(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	const transportID = "session-node-stop-plan0001-aaaabbbb"
	recipes := &recordingRecipeCardStoreV3{cards: map[string]playback.RecipeCard{
		transportID: {SessionID: "session-node-stop", TranscodeTransportID: transportID},
	}}
	handler.NodeRecipeStore = recipes
	session := &playback.Session{
		ID: "session-node-stop", UserID: 7, ProfileID: "profile-1",
		TranscodeNodeURL: "http://node-1", TranscodeTransportID: transportID,
	}

	handler.finalizeSessionStop(context.Background(), session, false, "", true)

	if len(recipes.deleted) != 1 || recipes.deleted[0] != transportID {
		t.Fatalf("recipes deleted on stop = %v, want the session's transport recipe dropped", recipes.deleted)
	}
}

func TestStopPlaybackRetainsProgressiveSessionUntilAuthorityRevocationSucceeds(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer node.Close()

	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSession(7, "profile-1", 42, playback.PlayRemux, false)
	if err != nil {
		t.Fatal(err)
	}
	const transportID = "session-required-stop-plan0001-aaaabbbb"
	if err := manager.SetTranscodeRoute(session.ID, playback.TranscodeRoute{NodeURL: node.URL, TransportID: transportID}); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetNodeRoutingAssignment(session.ID, playback.NodeRoutingAssignment{
		Workload: string(noderouting.WorkloadRemux), Execution: string(noderouting.ExecutionTranscode),
		Egress: string(noderouting.EgressProxy),
	}); err != nil {
		t.Fatal(err)
	}
	session, err = manager.GetSession(session.ID)
	if err != nil {
		t.Fatal(err)
	}

	handler := NewPlaybackHandler(manager)
	recipes := &recordingRecipeCardStoreV3{deleteErr: errors.New("redis is down")}
	handler.NodeRecipeStore = recipes
	if err := handler.stopPlaybackSession(t.Context(), session, true); err == nil {
		t.Fatal("stop succeeded without durable progressive authority revocation")
	}
	if _, err := manager.GetSession(session.ID); err != nil {
		t.Fatalf("failed stop removed the retryable playback session: %v", err)
	}
	if len(recipes.deleted) != 1 || recipes.deleted[0] != transportID {
		t.Fatalf("authority deletion attempts = %v, want [%q]", recipes.deleted, transportID)
	}

	recipes.deleteErr = nil
	if err := handler.stopPlaybackSession(t.Context(), session, true); err != nil {
		t.Fatalf("retry stop after authority recovery: %v", err)
	}
	if _, err := manager.GetSession(session.ID); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("session after successful retry = %v, want not found", err)
	}
}

func TestStopPlaybackRetainsProgressiveSessionUntilRemoteCancellationSucceeds(t *testing.T) {
	requests := 0
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		switch requests {
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 2:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer node.Close()

	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSession(7, "profile-1", 42, playback.PlayRemux, false)
	if err != nil {
		t.Fatal(err)
	}
	const transportID = "session-required-cancel-plan0001-aaaabbbb"
	if err := manager.SetTranscodeRoute(session.ID, playback.TranscodeRoute{NodeURL: node.URL, TransportID: transportID}); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetNodeRoutingAssignment(session.ID, playback.NodeRoutingAssignment{
		Workload: string(noderouting.WorkloadRemux), Execution: string(noderouting.ExecutionTranscode),
		Egress: string(noderouting.EgressProxy),
	}); err != nil {
		t.Fatal(err)
	}
	session, err = manager.GetSession(session.ID)
	if err != nil {
		t.Fatal(err)
	}

	handler := NewPlaybackHandler(manager)
	recipes := &recordingRecipeCardStoreV3{cards: map[string]playback.RecipeCard{
		transportID: {SessionID: session.ID, TranscodeTransportID: transportID},
	}}
	handler.NodeRecipeStore = recipes
	stopErr := handler.stopPlaybackSession(t.Context(), session, true)
	if stopErr == nil || !strings.Contains(stopErr.Error(), "cancel progressive remux transport") {
		t.Fatalf("stop error = %v, want remote cancellation failure", stopErr)
	}
	if _, err := manager.GetSession(session.ID); err != nil {
		t.Fatalf("failed cancellation removed the retryable playback session: %v", err)
	}
	if _, ok := recipes.cards[transportID]; ok {
		t.Fatal("failed cancellation left authority that could admit another remote process")
	}
	if requests != 1 {
		t.Fatalf("remote cancellation requests = %d, want 1", requests)
	}

	if err := handler.stopPlaybackSession(t.Context(), session, true); err != nil {
		t.Fatalf("retry stop after node recovery: %v", err)
	}
	if _, err := manager.GetSession(session.ID); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("session after successful retry = %v, want not found", err)
	}
	if requests != 3 {
		t.Fatalf("remote cancellation requests = %d, want required retry plus idempotent final cleanup", requests)
	}
}

func TestStopPlaybackWaitsForReplacementAndRevokesItsCurrentAuthority(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer node.Close()

	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSession(7, "profile-1", 42, playback.PlayRemux, false)
	if err != nil {
		t.Fatal(err)
	}
	const oldTransportID = "session-stop-race-old-plan"
	const newTransportID = "session-stop-race-new-plan"
	if err := manager.SetTranscodeRoute(session.ID, playback.TranscodeRoute{NodeURL: node.URL, TransportID: oldTransportID}); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetNodeRoutingAssignment(session.ID, playback.NodeRoutingAssignment{
		Workload: string(noderouting.WorkloadRemux), Execution: string(noderouting.ExecutionTranscode),
		Egress: string(noderouting.EgressProxy),
	}); err != nil {
		t.Fatal(err)
	}
	staleSession, err := manager.GetSession(session.ID)
	if err != nil {
		t.Fatal(err)
	}

	handler := NewPlaybackHandler(manager)
	recipes := &recordingRecipeCardStoreV3{cards: map[string]playback.RecipeCard{
		oldTransportID: {SessionID: session.ID, TranscodeTransportID: oldTransportID},
		newTransportID: {SessionID: session.ID, TranscodeTransportID: newTransportID},
	}}
	handler.NodeRecipeStore = recipes

	// Model a replan after it has installed its successor in the session manager
	// but before it has committed the durable plan and released the lifecycle
	// boundary. The stop receives the intentionally stale predecessor copy.
	unlockReplacement := handler.tm.LockSessionLifecycle(session.ID)
	locked := true
	defer func() {
		if locked {
			unlockReplacement()
		}
	}()
	if _, err := manager.ApplyReplacement(session.ID, playback.SessionReplacement{
		EffectiveMediaFileID: 42,
		StreamState: playback.SessionStreamState{
			PlayMethod: playback.PlayRemux, TranscodeNodeURL: node.URL, TranscodeTransportID: newTransportID, TranscodeRouteSet: true,
			RoutingWorkload: string(noderouting.WorkloadRemux), RoutingExecution: string(noderouting.ExecutionTranscode), RoutingEgress: string(noderouting.EgressProxy),
		},
	}); err != nil {
		t.Fatal(err)
	}
	stopDone := make(chan error, 1)
	go func() {
		stopDone <- handler.stopPlaybackSession(t.Context(), staleSession, true)
	}()
	select {
	case stopErr := <-stopDone:
		t.Fatalf("stop crossed the in-flight replacement boundary: %v", stopErr)
	case <-time.After(50 * time.Millisecond):
	}

	unlockReplacement()
	locked = false
	select {
	case stopErr := <-stopDone:
		if stopErr != nil {
			t.Fatal(stopErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not resume after the replacement committed")
	}
	if !slices.Equal(recipes.deleted, []string{newTransportID, newTransportID}) {
		t.Fatalf("deleted recipe authorities = %v, want only the current replacement transport", recipes.deleted)
	}
	if _, err := manager.GetSession(session.ID); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("session after stop = %v, want not found", err)
	}
}

func TestPrepareProgressiveTransportPublishesAuthorityInsideLifecycleBoundary(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSession(7, "profile-1", 42, playback.PlayRemux, false)
	if err != nil {
		t.Fatal(err)
	}

	proxy := capableProxyStubV3(t)
	transcode := progressiveRemuxExecutionStubV3(t)
	handler := NewPlaybackHandler(manager)
	handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{
		ProxyNode:     &nodepool.Node{ID: 42, URL: proxy.URL},
		TranscodeNode: &nodepool.Node{ID: 84, URL: transcode.URL},
	}}
	recipes := &recordingRecipeCardStoreV3{}
	grants := &recordingRecipeCardStoreV3{}
	handler.NodeRecipeStore = recipes
	handler.ProxyGrantStore = grants
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.RemuxExecution = config.PlaybackExecutionPreferTranscode
	handler.PlaybackConfig = func() config.PlaybackConfig { return config.PlaybackConfig{Routing: policy} }

	reachedLifecycle := make(chan struct{})
	handler.beforeIdentityLifecycleLockV3 = sync.OnceFunc(func() { close(reachedLifecycle) })
	unlockStop := handler.tm.LockSessionLifecycle(session.ID)
	locked := true
	defer func() {
		if locked {
			unlockStop()
		}
	}()

	plan := identityProxyPlanV3(playback.DeliveryRemuxProgressiveV3)
	plan.SessionID = session.ID
	result := playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux, TargetAudioCodec: "aac"}
	prepared := make(chan *transportErrorV3, 1)
	go func() {
		_, transportErr := handler.prepareTransportV3(
			httptest.NewRequest(http.MethodPost, "/", nil), session, v3HandlerFixtureFile(t), result, authorizedOriginsModeV3())
		prepared <- transportErr
	}()

	select {
	case <-reachedLifecycle:
	case <-time.After(2 * time.Second):
		t.Fatal("transport preparation did not reach the lifecycle boundary")
	}
	if len(recipes.cards) != 0 || len(grants.cards) != 0 {
		t.Fatalf("authority published before lifecycle lock: recipes=%v grants=%v", recipes.cards, grants.cards)
	}
	// Model stop after it acquired this boundary: the session disappears before
	// the waiting replacement can enter and publish its successor authority.
	if err := manager.StopSession(session.ID); err != nil {
		t.Fatal(err)
	}
	unlockStop()
	locked = false

	select {
	case transportErr := <-prepared:
		if transportErr == nil || transportErr.reason != "session_expired" || !transportErr.retryable {
			t.Fatalf("transport error = %#v, want retryable session_expired", transportErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("transport preparation did not resume after stop released the lifecycle boundary")
	}
	if len(recipes.cards) != 0 || len(grants.cards) != 0 {
		t.Fatalf("stopped session retained successor authority: recipes=%v grants=%v", recipes.cards, grants.cards)
	}
}

// The planner charged a proxy for bytes that will not cross it. Keeping that
// half of the reservation makes a healthy proxy look saturated after a burst of
// grant-store failures, so it is given back as soon as the URL is settled — the
// transcode node keeps its half, because it is running the job.
func TestPrepareTransportV3ReleasesTheProxyHalfWhenTheManifestIsNotProxyServed(t *testing.T) {
	for _, test := range []struct {
		name             string
		jwtSecret        string
		mode             mediaAuthModeV3
		grants           recipeCardStoreV3
		wantProxyRelease bool
	}{
		{
			name:             "grant write failed",
			jwtSecret:        "test-secret",
			mode:             authorizedOriginsModeV3(),
			grants:           &recordingRecipeCardStoreV3{putErr: errors.New("redis is down")},
			wantProxyRelease: true,
		},
		{
			// The legacy no-token fallback leaks the same half: no signable
			// token means the proxy URL cannot be addressed at all.
			name:             "legacy attempt with no signable token",
			mode:             mediaAuthModeV3{},
			grants:           &recordingRecipeCardStoreV3{},
			wantProxyRelease: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			node := remoteTranscodeNodeStubV3(t)
			handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
			handler.JWTSecret = test.jwtSecret
			planner := &recordingNodePlannerV3{plan: nodepool.Plan{TranscodeNode: &nodepool.Node{URL: node.URL}, ProxyNode: &nodepool.Node{URL: "http://proxy-1"}}}
			handler.NodePlanner = planner
			handler.ProxyGrantStore = test.grants

			transport, transportErr := handler.prepareTransportV3(
				httptest.NewRequest(http.MethodPost, "/", nil),
				&playback.Session{ID: "session-proxy-half", UserID: 7, ProfileID: "profile-1"},
				v3HandlerFixtureFile(t),
				remoteHLSResultV3(),
				test.mode)
			if transportErr != nil {
				t.Fatalf("prepare remote transport: %v", transportErr)
			}
			defer transport.rollback()

			if transport.url != "/playback/transcode/session-proxy-half/master.m3u8" {
				t.Fatalf("manifest url = %q, want the API-relayed manifest", transport.url)
			}
			wantProxyReleases := 0
			if test.wantProxyRelease {
				wantProxyReleases = 1
			}
			if len(planner.releasedProxy) != wantProxyReleases {
				t.Fatalf("proxy-half releases = %v, want %d", planner.releasedProxy, wantProxyReleases)
			}
			if len(planner.released) != 0 {
				t.Fatalf("whole-reservation releases = %v, want none: the transcode node is running the job", planner.released)
			}
		})
	}
}

// A proxy that does serve the manifest keeps its reservation: the bytes really
// are going to cross it.
func TestPrepareTransportV3KeepsTheProxyReservationWhenTheProxyServes(t *testing.T) {
	node := remoteTranscodeNodeStubV3(t)
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	planner := &recordingNodePlannerV3{plan: nodepool.Plan{TranscodeNode: &nodepool.Node{URL: node.URL}, ProxyNode: &nodepool.Node{URL: "http://proxy-1"}}}
	handler.NodePlanner = planner
	handler.ProxyGrantStore = &recordingRecipeCardStoreV3{}

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-proxy-served", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		remoteHLSResultV3(),
		authorizedOriginsModeV3())
	if transportErr != nil {
		t.Fatalf("prepare remote transport: %v", transportErr)
	}
	defer transport.rollback()

	if transport.url != "http://proxy-1/stream/v3/session-proxy-served/master.m3u8" {
		t.Fatalf("manifest url = %q, want the proxy manifest", transport.url)
	}
	if len(planner.releasedProxy) != 0 {
		t.Fatalf("proxy-half releases = %v, want none: the proxy is serving this stream", planner.releasedProxy)
	}
}
