package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// enumeratingNodePlannerV3 is a SessionPlanner stub that also exposes pooled
// transcode node URLs, matching *nodepool.Planner's production shape.
type enumeratingNodePlannerV3 struct {
	staticNodePlannerV3
	urls []string
}

func (p enumeratingNodePlannerV3) TranscodeNodeURLs() []string { return p.urls }

// presetLocalRegistryV3 pins the handler's local transformation registry so
// tests never probe the machine's real ffmpeg.
func presetLocalRegistryV3(h *PlaybackHandler, registry *playback.TransformationRegistryV3) {
	h.v3RegistryOnce.Do(func() {})
	h.v3Registry = registry
}

func TestHLSPlanningRegistryV3UnionsPooledNodeCapabilities(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hw-capabilities" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: []playback.TransformationV3{
			{Name: "video_to_h264", Executor: "server", RecipeVersion: "1"},
			{Name: "audio_to_aac", Executor: "server", RecipeVersion: "1"},
		}})
	}))
	defer remote.Close()

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: "video_to_h264", RecipeVersion: "1"},
		{Name: "audio_to_aac", RecipeVersion: "1"},
		{Name: "server_dv7_to_hdr10", RecipeVersion: "1"},
	}))
	handler.NodePlanner = enumeratingNodePlannerV3{urls: []string{remote.URL}}

	registry := handler.hlsPlanningRegistryV3(context.Background())
	if !registry.Available("video_to_h264") || !registry.Available("audio_to_aac") {
		t.Fatal("pooled node capabilities must widen the HLS planning registry")
	}
	if registry.Available("server_dv7_to_hdr10") {
		t.Fatal("transformations no node advertises must stay unavailable")
	}
	if handler.transformationRegistryV3(context.Background()).Available("video_to_h264") {
		t.Fatal("the local registry must not be widened by node capabilities")
	}
}

func TestHLSPlanningRegistryV3WithoutEnumeratorIsLocal(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	local := playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{{Name: "audio_to_aac", RecipeVersion: "1", Available: true}})
	presetLocalRegistryV3(handler, local)
	handler.NodePlanner = staticNodePlannerV3{plan: nodepool.Plan{}}

	if registry := handler.hlsPlanningRegistryV3(context.Background()); registry != local {
		t.Fatal("a planner without node enumeration must plan from the local registry")
	}
}

func TestRemoteTransformationsV3CachesFetchFailures(t *testing.T) {
	hits := 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer remote.Close()

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	if _, err := handler.remoteTransformationsV3(context.Background(), remote.URL); err == nil {
		t.Fatal("fetch against a failing node must error")
	}
	if _, err := handler.remoteTransformationsV3(context.Background(), remote.URL); err == nil {
		t.Fatal("memoized failure must still surface as an error")
	}
	if hits != 1 {
		t.Fatalf("failing node was fetched %d times; the failure must be memoized", hits)
	}
}

func TestPrepareTransportV3LocalFallbackRejectsUnavailableTransformations(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: "video_to_h264", RecipeVersion: "1"},
		{Name: "audio_to_aac", RecipeVersion: "1"},
	}))
	plan := &playback.PlanV3{
		PlanID:   "plan:local-capability",
		Delivery: playback.DeliveryTranscodeHLSV3,
		Transformations: []playback.TransformationV3{
			{Name: "video_to_h264", Executor: "server", RecipeVersion: "1"},
			{Name: "audio_to_aac", Executor: "server", RecipeVersion: "1"},
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	_, transportErr := handler.prepareTransportV3(request, &playback.Session{ID: "session-local-capability"}, v3HandlerFixtureFile(t), playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayTranscode, TargetVideoCodec: "h264", TargetAudioCodec: "aac"})
	if transportErr == nil || transportErr.reason != "transcode_node_capability_unavailable" || !transportErr.retryable {
		t.Fatalf("transport error = %#v", transportErr)
	}
}

func TestPlanRequiresServerTransformationsV3(t *testing.T) {
	if planRequiresServerTransformationsV3(nil) {
		t.Fatal("nil plan must not require server transformations")
	}
	clientOnly := &playback.PlanV3{Transformations: []playback.TransformationV3{{Name: playback.ClientDV7ToDV81V3, Executor: "client", RecipeVersion: "1"}}}
	if planRequiresServerTransformationsV3(clientOnly) {
		t.Fatal("client-executed transformations must not require a server executor")
	}
	server := &playback.PlanV3{Transformations: []playback.TransformationV3{{Name: "audio_to_aac", Executor: "server", RecipeVersion: "1"}}}
	if !planRequiresServerTransformationsV3(server) {
		t.Fatal("server-executed transformations must require executor validation")
	}
}
