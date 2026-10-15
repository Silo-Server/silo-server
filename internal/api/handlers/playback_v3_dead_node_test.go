package handlers

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodepool/nodepooltest"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

// TestPrepareTransportV3MovesOnFromANodeThatStopsAccepting covers a node that
// dies between answering its capability read and receiving the start: the
// start's connect is refused, the route moves to the next node without
// sending the dead one a stop, and the planner stops offering the dead node to
// later sessions.
func TestPrepareTransportV3MovesOnFromANodeThatStopsAccepting(t *testing.T) {
	transformations := []playback.TransformationV3{
		{Name: "video_to_h264", Executor: "server", RecipeVersion: "2"},
		{Name: "audio_to_aac", Executor: "server", RecipeVersion: "2"},
	}
	var deadCapabilityReads atomic.Int32
	deadListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	dead := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/hw-capabilities" {
			t.Errorf("dead node received %s %s after it stopped accepting", r.Method, r.URL.Path)
			return
		}
		deadCapabilityReads.Add(1)
		w.Header().Set("Connection", "close")
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: transformations})
		_ = deadListener.Close()
	}))
	dead.Listener = deadListener
	dead.Start()
	defer dead.Close()

	var liveStarts atomic.Int32
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: transformations})
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			liveStarts.Add(1)
			var request transcodenode.TranscodeStartRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode remote start: %v", err)
			}
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: request.SessionID, Status: "started"})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer live.Close()

	transcodes := nodepooltest.NewTranscodePool()
	transcodes.SetNodes([]*nodepool.Node{
		{ID: 1, URL: dead.URL, Type: nodepool.NodeTypeTranscode, Enabled: true, Healthy: true},
		{ID: 2, URL: live.URL, Type: nodepool.NodeTypeTranscode, Enabled: true, Healthy: true, ActiveJobs: 2},
	})
	planner := nodepool.NewPlanner(nodepool.NewProxyPool(), transcodes)
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.NodeKeys = testNodeKeys
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = planner
	requireWorkerRoutingV3(handler)
	plan := &playback.PlanV3{PlanID: "plan:dead-node", Delivery: playback.DeliveryTranscodeHLSV3, Transformations: transformations}

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-dead-node", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayTranscode, TargetVideoCodec: "h264", TargetAudioCodec: "aac"},
		mediaAuthModeV3{},
	)
	if transportErr != nil {
		t.Fatalf("prepare transport: %v", transportErr)
	}
	defer transport.rollback()
	if deadCapabilityReads.Load() != 1 {
		t.Fatalf("dead node capability reads = %d, want it tried first", deadCapabilityReads.Load())
	}
	if transport.nodeURL != live.URL || liveStarts.Load() != 1 {
		t.Fatalf("transport node = %q with %d live starts, want the live node", transport.nodeURL, liveStarts.Load())
	}
	if planner.TranscodeNodeHealthy(dead.URL) {
		t.Fatal("the planner still offers the node whose connect was refused")
	}
	if next := planner.PlanSession("next-session", "", true, 0).TranscodeNode; next == nil || next.URL != live.URL {
		t.Fatalf("next session planned onto %+v, want the live node", next)
	}
}

// textBurnInRefusingNodeV3 is a transcode node that refuses every text
// subtitle burn-in start as a node reading its source through the core does.
func textBurnInRefusingNodeV3(t *testing.T, transformations []playback.TransformationV3, refusals *atomic.Int32) *httptest.Server {
	t.Helper()
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: transformations})
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			var request transcodenode.TranscodeStartRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !request.SubtitleBurnIn || request.SubtitleCodec != "subrip" {
				t.Errorf("refusing node start = %+v, %v; want a text burn-in", request, err)
			}
			refusals.Add(1)
			http.Error(w, "text subtitle burn-in needs local source access on this node", http.StatusUnprocessableEntity)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(node.Close)
	return node
}

func textBurnInResultV3(planID string, transformations []playback.TransformationV3) playback.PlannerResultV3 {
	return playback.PlannerResultV3{
		Plan:       &playback.PlanV3{PlanID: planID, Delivery: playback.DeliveryTranscodeHLSV3, Transformations: transformations},
		PlayMethod: playback.PlayTranscode, TargetVideoCodec: "h264", TargetAudioCodec: "aac",
		SubtitleTrackIndex: 0, SubtitleTransportTrackIndex: 0, SubtitleBurnIn: true, SubtitleCodec: "subrip",
	}
}

// A node that refuses a text subtitle burn-in because it reads its source
// through the core is excluded like any failed start: the route moves to the
// next node.
func TestPrepareTransportV3MovesATextBurnInRefusalToTheNextNode(t *testing.T) {
	transformations := []playback.TransformationV3{
		{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3},
		{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
	}
	var refusals, accepted atomic.Int32
	refusing := textBurnInRefusingNodeV3(t, transformations, &refusals)
	accepting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: transformations})
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			var request transcodenode.TranscodeStartRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !request.SubtitleBurnIn {
				t.Errorf("accepting node start = %+v, %v", request, err)
			}
			accepted.Add(1)
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: request.SessionID, Status: "started"})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer accepting.Close()

	transcodes := nodepooltest.NewTranscodePool()
	transcodes.SetNodes([]*nodepool.Node{
		{ID: 1, URL: refusing.URL, Type: nodepool.NodeTypeTranscode, Enabled: true, Healthy: true},
		{ID: 2, URL: accepting.URL, Type: nodepool.NodeTypeTranscode, Enabled: true, Healthy: true, ActiveJobs: 2},
	})
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.NodeKeys = testNodeKeys
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = nodepool.NewPlanner(nodepool.NewProxyPool(), transcodes)
	requireWorkerRoutingV3(handler)

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-burn-in-refused", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		textBurnInResultV3("plan:burn-in-refused", transformations),
		mediaAuthModeV3{},
	)
	if transportErr != nil {
		t.Fatalf("prepare transport: %v", transportErr)
	}
	defer transport.rollback()
	if transport.nodeURL != accepting.URL || refusals.Load() != 1 || accepted.Load() != 1 {
		t.Fatalf("transport node = %q after %d refusals and %d accepted starts, want the second node",
			transport.nodeURL, refusals.Load(), accepted.Load())
	}
}

// With local execution allowed, a refused text burn-in falls back to the API
// server, which burns the subtitle in itself.
func TestPrepareTransportV3BurnsInLocallyAfterATextBurnInRefusal(t *testing.T) {
	transformations := []playback.TransformationV3{
		{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3},
		{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
	}
	var refusals atomic.Int32
	refusing := textBurnInRefusingNodeV3(t, transformations, &refusals)
	transcodes := nodepooltest.NewTranscodePool()
	transcodes.SetNodes([]*nodepool.Node{{ID: 1, URL: refusing.URL, Type: nodepool.NodeTypeTranscode, Enabled: true, Healthy: true}})

	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager)
	handler.NodeKeys = testNodeKeys
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = nodepool.NewPlanner(nodepool.NewProxyPool(), transcodes)
	baseFFmpeg := writePlaybackTestFFmpeg(t)
	scratch := t.TempDir()
	argv := filepath.Join(scratch, "argv")
	ffmpegPath := filepath.Join(scratch, "ffmpeg")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \""+argv+"\"\nexec \""+baseFFmpeg+"\" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	transcodeDir := t.TempDir()
	handler.PlaybackConfig = func() config.PlaybackConfig {
		return config.PlaybackConfig{
			FFmpegPath: ffmpegPath, TranscodeDir: transcodeDir, TranscodeEnabled: true, HWAccel: playback.HWAccelNone,
			Routing: config.DefaultPlaybackRoutingPolicy(),
		}
	}
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: playback.TransformationVideoToH264V3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3, Available: true},
		{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
	}))
	file := v3HandlerFixtureFile(t)
	session, err := manager.StartSession(7, "profile-1", file.ID, playback.PlayTranscode, true)
	if err != nil {
		t.Fatalf("start playback session: %v", err)
	}

	transport, transportErr := handler.prepareTransportV3(httptest.NewRequest(http.MethodPost, "/", nil), session, file,
		textBurnInResultV3("plan:burn-in-local", transformations), mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare transport: %v (cause: %v)", transportErr, transportErr.cause)
	}
	defer transport.rollback()
	if transport.nodeURL != "" || transport.routingExecution != noderouting.ExecutionAPI || refusals.Load() != 1 {
		t.Fatalf("transport = node %q execution %q after %d refusals, want the API server", transport.nodeURL, transport.routingExecution, refusals.Load())
	}
	runs, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(runs), "subtitles=") {
		t.Fatalf("local transcode did not burn the subtitle in: %s", runs)
	}
}
