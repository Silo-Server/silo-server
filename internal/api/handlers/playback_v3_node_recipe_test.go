package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodepool"
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
	_, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-stale-audio-node", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t), remoteHLSResultV3(), mediaAuthModeV3{})
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

// The planner charged a proxy for bytes that will not cross it. Keeping that
// half of the reservation makes a healthy proxy look saturated after a burst of
// grant-store failures, so it is given back as soon as the URL is settled — the
// transcode node keeps its half, because it is running the job.
func TestPrepareTransportV3ReleasesTheProxyHalfWhenTheManifestIsNotProxyServed(t *testing.T) {
	for _, test := range []struct {
		name      string
		jwtSecret string
		mode      mediaAuthModeV3
		grants    recipeCardStoreV3
	}{
		{
			name:      "grant write failed",
			jwtSecret: "test-secret",
			mode:      authorizedOriginsModeV3(),
			grants:    &recordingRecipeCardStoreV3{putErr: errors.New("redis is down")},
		},
		{
			// The legacy no-token fallback leaks the same half: no signable
			// token means the proxy URL cannot be addressed at all.
			name:   "legacy attempt with no signable token",
			mode:   mediaAuthModeV3{},
			grants: &recordingRecipeCardStoreV3{},
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
			if len(planner.releasedProxy) != 1 || planner.releasedProxy[0] != "session-proxy-half" {
				t.Fatalf("proxy-half releases = %v, want the unused proxy reservation given back", planner.releasedProxy)
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
