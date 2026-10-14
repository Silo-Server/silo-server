package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

func TestPlaybackV3PersistsCurrentReportTransport(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.Container = "mkv"
	file.FilePath = writePlaybackTestMediaFile(t, "report-movie.mkv")
	file.ExternalSubtitles = []models.ExternalSubtitle{
		{Path: writePlaybackTestMediaFile(t, "report-movie.de.srt"), Language: "de", Format: "srt"},
		{Path: writePlaybackTestMediaFile(t, "report-movie.en.srt"), Language: "en", Format: "srt"},
	}
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/health":
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: remoteHLSPlanV3().Transformations})
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			var req transcodenode.TranscodeStartRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: req.SessionID, Status: "started", AudioRecipeVersion: req.AudioRecipeVersion, CopyFMP4RecipeVersion: req.CopyFMP4RecipeVersion, ThrottleSeconds: req.ThrottleSeconds, KeyframePlaylist: req.KeyframePlaylist, ToneMapMode: req.ToneMapMode})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(node.Close)
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.JWTSecret = "report-transport-test-secret"
	handler.PlaybackConfig = playbackTestConfig("unused-ffmpeg", t.TempDir())
	requireWorkerRoutingV3(handler)
	handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{TranscodeNode: &nodepool.Node{ID: 17, URL: node.URL}}}
	handler.NodeRecipeStore = &recordingRecipeCardStoreV3{}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	start := v3HandlerStartRequest()
	start.QualityPreference = "720p"
	start.ClientFeatures = append(start.ClientFeatures, playback.FeatureHeaderAuthenticatedMediaV3)
	start.Capabilities.Containers = []string{"m3u8"}
	start.ClientPlaybackContext.Deliveries = map[string]playback.DeliveryCapabilityV3{playback.DeliveryClassHLSV3: {Enabled: true, SupportedOnDevice: true, Subtitles: playback.DeliverySubtitleCapabilitiesV3{SidecarText: true}}}
	german := 0
	start.SubtitleTrackID, start.SubtitleTrackIndex = playback.TrackIDV3(file.ID, "subtitle", german), &german
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(newAuthorizedPlaybackContext()))
	var current playback.DecisionResponseV3
	if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &current) != nil || current.PlaybackPlan == nil {
		t.Fatalf("start status=%d body=%s", rr.Code, rr.Body.String())
	}
	t.Cleanup(func() { _ = handler.stopPlaybackSessionByID(context.Background(), current.SessionID, false) })
	check := func(stage string, response playback.DecisionResponseV3) playback.TranscodeRoute {
		t.Helper()
		live, err := manager.GetSession(response.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		record, err := handler.PlanStoreV3.GetAttempt(t.Context(), response.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if live.TranscodeNodeURL != node.URL || live.TranscodeTransportID == "" || record.FrozenRecipe.TranscodeNodeURL != live.TranscodeNodeURL || record.FrozenRecipe.TranscodeTransportID != live.TranscodeTransportID {
			t.Fatalf("%s committed route missing/mismatched: durable=%q/%q live=%q/%q", stage, record.FrozenRecipe.TranscodeNodeURL, record.FrozenRecipe.TranscodeTransportID, live.TranscodeNodeURL, live.TranscodeTransportID)
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), `"transcode_node_url"`) || strings.Contains(string(encoded), `"transcode_transport_id"`) {
			t.Fatal("internal executor identity leaked into the public decision DTO")
		}
		return playback.TranscodeRoute{NodeURL: live.TranscodeNodeURL, TransportID: live.TranscodeTransportID}
	}
	startedRoute := check("start", current)
	replan := func(id string, operation playback.ReplanOperationV3) playback.ReplanRequestV3 {
		return playback.ReplanRequestV3{ProtocolVersion: playback.ProtocolV3, Operation: operation, PlaybackAttemptID: start.PlaybackAttemptID, ReplanRequestID: id, FailedPlanID: current.PlaybackPlan.PlanID, PlanAttemptID: id + "-attempt", PlanAttemptKey: current.PlaybackPlan.PlanAttemptKey, AttemptCount: 1, PositionSeconds: 120, QualityPreference: start.QualityPreference, SelectedTracks: current.PlaybackPlan.SelectedTracks, Capabilities: start.Capabilities, ClientPlaybackContext: start.ClientPlaybackContext}
	}
	english := 1
	sidecar := replan("report-sidecar-change", playback.ReplanOperationTrackChangeV3)
	sidecar.SelectedTracks.Subtitle = &playback.TrackIdentityV3{ID: playback.TrackIDV3(file.ID, "subtitle", english), Index: &english}
	current = postPlaybackReplanV3(t, handler, current.SessionID, sidecar)
	if current.PlaybackPlan == nil {
		t.Fatalf("sidecar returned no plan: %+v", current)
	}
	if reused := check("sidecar reuse", current); reused != startedRoute {
		t.Fatalf("sidecar replan changed the reused route: got=%+v want=%+v", reused, startedRoute)
	}
	current = postPlaybackReplanV3(t, handler, current.SessionID, replan("report-seek-reanchor", playback.ReplanOperationSeekReanchorV3))
	if current.PlaybackPlan == nil {
		t.Fatalf("seek returned no plan: %+v", current)
	}
	seekRoute := check("seek replacement", current)
	if seekRoute.TransportID == startedRoute.TransportID {
		t.Fatal("seek fixture did not create a distinct transport")
	}
	store := handler.PlanStoreV3
	handler.PlanStoreV3 = &failingCompletePlanStoreV3{PlanStoreV3: store}
	failed := replan("report-failed-replacement", playback.ReplanOperationQualityChangeV3)
	failed.QualityPreference = "480p"
	body, err := json.Marshal(failed)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+current.SessionID+"/replan", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
	request = withPlaybackRouteParam(request, "session_id", current.SessionID)
	rr = httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(rr, request)
	handler.PlanStoreV3 = store
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("failed persistence status=%d body=%s", rr.Code, rr.Body.String())
	}
	if retained := check("failed replacement", current); retained != seekRoute {
		t.Fatalf("failed replacement lost the previous committed transport: got=%+v want=%+v", retained, seekRoute)
	}
}
