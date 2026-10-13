package handlers

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodepool"
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

	transcodes := nodepool.NewTranscodePool()
	transcodes.SetNodes([]*nodepool.Node{
		{ID: 1, URL: dead.URL, Type: nodepool.NodeTypeTranscode, Enabled: true, Healthy: true},
		{ID: 2, URL: live.URL, Type: nodepool.NodeTypeTranscode, Enabled: true, Healthy: true, ActiveJobs: 2},
	})
	planner := nodepool.NewPlanner(nodepool.NewProxyPool(), transcodes)
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
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
