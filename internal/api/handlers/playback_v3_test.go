package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type mutablePlaybackSettingsV3 struct {
	mu     sync.Mutex
	values map[string]string
}

func (s *mutablePlaybackSettingsV3) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key], nil
}

func (s *mutablePlaybackSettingsV3) set(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
}

func TestHandleStartPlaybackV3DisabledDoesNotAllocateLegacySession(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: v3HandlerFixtureFile(t)})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"playback.protocol_v3_enabled": "false"}}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, v3HandlerStartRequest())))
	req = req.WithContext(newAuthorizedPlaybackContext())
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response playback.DecisionResponseV3
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ProtocolVersion != playback.ProtocolV3 || len(response.ServerFeatures) != 0 || response.SessionID != "" {
		t.Fatalf("response = %#v", response)
	}
	if got := len(manager.AllSessions()); got != 0 {
		t.Fatalf("sessions = %d, want 0", got)
	}
}

func TestHandlePlaybackCapabilityV3ReadsFlagPerRequest(t *testing.T) {
	settings := &mutablePlaybackSettingsV3{values: map[string]string{"playback.protocol_v3_enabled": "false"}}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.SettingsRepo = settings

	request := func() playback.CapabilityResponseV3 {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/playback/capability", nil).WithContext(newAuthorizedPlaybackContext())
		rr := httptest.NewRecorder()
		handler.HandlePlaybackCapabilityV3(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
		var response playback.CapabilityResponseV3
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}

	if response := request(); response.Enabled || response.Reason != "disabled" {
		t.Fatalf("disabled response = %#v", response)
	}
	settings.set("playback.protocol_v3_enabled", "true")
	if response := request(); !response.Enabled || len(response.Deliveries) != 4 {
		t.Fatalf("enabled response = %#v", response)
	}
}

func TestHandleStartPlaybackV3ReturnsExecutableDirectPlan(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"playback.protocol_v3_enabled": "true", "allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, v3HandlerStartRequest())))
	req = req.WithContext(newAuthorizedPlaybackContext())
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response playback.DecisionResponseV3
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Outcome != playback.OutcomePlayableV3 || response.PlaybackPlan == nil {
		t.Fatalf("response = %#v", response)
	}
	if response.PlaybackPlan.Delivery != playback.DeliveryOriginalHTTPV3 || response.PlaybackPlan.Engine != playback.EngineMedia3DirectV3 || response.PlaybackPlan.Stream.URL == "" {
		t.Fatalf("plan = %#v", response.PlaybackPlan)
	}
	if response.PlaybackPlan.RequestedMediaFileID != file.ID || response.PlaybackPlan.EffectiveMediaFileID != file.ID || response.PlaybackPlan.Source.MediaFileID != file.ID {
		t.Fatalf("source identity = %#v", response.PlaybackPlan)
	}
	if got := len(manager.AllSessions()); got != 1 {
		t.Fatalf("sessions = %d, want 1", got)
	}
}

func TestHandleStartPlaybackV3DuplicateAttemptReturnsOriginalSession(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"playback.protocol_v3_enabled": "true", "allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	body := marshalV3StartRequest(t, v3HandlerStartRequest())

	start := func() playback.DecisionResponseV3 {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(body)).WithContext(newAuthorizedPlaybackContext())
		rr := httptest.NewRecorder()
		handler.HandleStartPlayback(rr, req)
		if rr.Code != http.StatusCreated {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
		var response playback.DecisionResponseV3
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	first := start()
	second := start()
	if first.SessionID == "" || second.SessionID != first.SessionID {
		t.Fatalf("first session %q, second %q", first.SessionID, second.SessionID)
	}
	if got := len(manager.AllSessions()); got != 1 {
		t.Fatalf("sessions = %d, want 1", got)
	}
}

func TestHandleStartPlaybackV3RejectsProfileMismatch(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: v3HandlerFixtureFile(t)})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"playback.protocol_v3_enabled": "true"}}
	request := v3HandlerStartRequest()
	request.ProfileID = "profile-other"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, request))).WithContext(newAuthorizedPlaybackContext())
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)
	if rr.Code != http.StatusBadRequest || len(manager.AllSessions()) != 0 {
		t.Fatalf("status = %d, sessions = %d, body = %s", rr.Code, len(manager.AllSessions()), rr.Body.String())
	}
}

func TestHandleReplanPlaybackV3UpdatesSelectedAudioAndReplaysIdempotently(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.AudioTracks = append(file.AudioTracks, models.AudioTrack{Codec: "aac", Channels: 2, Layout: "stereo", Language: "spa"})
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"playback.protocol_v3_enabled": "true", "allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext())
	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, startReq)
	if startRR.Code != http.StatusCreated {
		t.Fatalf("start status = %d, body = %s", startRR.Code, startRR.Body.String())
	}
	var started playback.DecisionResponseV3
	if err := json.Unmarshal(startRR.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.PlaybackPlan == nil {
		t.Fatal("start returned no plan")
	}
	audioIndex := 1
	failedKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, startRequest.OutputRouteGeneration, nil)
	replan := playback.ReplanRequestV3{ProtocolVersion: playback.ProtocolV3, PlaybackAttemptID: startRequest.PlaybackAttemptID, ReplanRequestID: "replan-0001", FailedPlanID: started.PlaybackPlan.PlanID, PlanAttemptID: "plan-attempt-0001", PlanAttemptKey: failedKey, AttemptedPlanKeys: []string{failedKey}, AttemptCount: 1, QualityPreference: "original", PositionSeconds: 12, OutputRouteGeneration: startRequest.OutputRouteGeneration, SelectedTracks: playback.SelectedTracksV3{Audio: &playback.TrackIdentityV3{ID: playback.TrackIDV3(file.ID, "audio", audioIndex), Index: &audioIndex}}, Failure: playback.FailureV3{Classification: "audio_renderer_error"}, Capabilities: startRequest.Capabilities, ClientPlaybackContext: startRequest.ClientPlaybackContext}
	replanBody, err := json.Marshal(replan)
	if err != nil {
		t.Fatal(err)
	}

	call := func() playback.DecisionResponseV3 {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(replanBody))).WithContext(newAuthorizedPlaybackContext())
		req = withPlaybackRouteParam(req, "session_id", started.SessionID)
		rr := httptest.NewRecorder()
		handler.HandleReplanPlaybackV3(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("replan status = %d, body = %s", rr.Code, rr.Body.String())
		}
		var response playback.DecisionResponseV3
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	first := call()
	second := call()
	if first.PlaybackPlan == nil || second.PlaybackPlan == nil || first.PlaybackPlan.PlanID != second.PlaybackPlan.PlanID {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	session, err := manager.GetSession(started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.AudioTrackIndex != audioIndex {
		t.Fatalf("audio index = %d, want %d", session.AudioTrackIndex, audioIndex)
	}
	replan.PositionSeconds++
	conflictBody, err := json.Marshal(replan)
	if err != nil {
		t.Fatal(err)
	}
	conflictReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(conflictBody))).WithContext(newAuthorizedPlaybackContext())
	conflictReq = withPlaybackRouteParam(conflictReq, "session_id", started.SessionID)
	conflictRR := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(conflictRR, conflictReq)
	if conflictRR.Code != http.StatusConflict || !strings.Contains(conflictRR.Body.String(), "idempotency_key_reused") {
		t.Fatalf("conflict status = %d, body = %s", conflictRR.Code, conflictRR.Body.String())
	}
}

func TestHandleStartPlaybackUnknownProtocolUsesLegacyBranch(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(`{"protocol_version":99,"file_id":42,"profile_id":"profile-1","play_method":"direct"}`))
	req = req.WithContext(newAuthorizedPlaybackContext())
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response playbackSessionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.SessionID == "" || response.PlayMethod != string(playback.PlayDirect) {
		t.Fatalf("legacy response = %#v", response)
	}
}

func TestHandleStartPlaybackLegacyBranchPreservesTrailingBodyBehavior(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(`{"file_id":42,"profile_id":"profile-1","play_method":"direct"} trailing`)).WithContext(newAuthorizedPlaybackContext())
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestConfigureHLSTimelineV3MatchesTransportSeekSemantics(t *testing.T) {
	copyPlan := &playback.PlanV3{Timeline: playback.TimelineV3{SourceStartSeconds: 17.3}}
	copySeek, copySegment := configureHLSTimelineV3(copyPlan, "copy", 2, 600)
	if copySeek != 17.3 || copySegment != 8 || copyPlan.Timeline.StreamOriginSeconds != 17.3 || copyPlan.Timeline.PlayerStartSeconds != 0 || copyPlan.Timeline.CanSeekAnywhere {
		t.Fatalf("copy timeline=%#v seek=%v segment=%d", copyPlan.Timeline, copySeek, copySegment)
	}

	encodePlan := &playback.PlanV3{Timeline: playback.TimelineV3{SourceStartSeconds: 17.3}}
	encodeSeek, encodeSegment := configureHLSTimelineV3(encodePlan, "h264", 2, 600)
	if encodeSeek != 16 || encodeSegment != 8 || encodePlan.Timeline.StreamOriginSeconds != 0 || encodePlan.Timeline.PlayerStartSeconds != 17.3 || !encodePlan.Timeline.CanSeekAnywhere {
		t.Fatalf("encode timeline=%#v seek=%v segment=%d", encodePlan.Timeline, encodeSeek, encodeSegment)
	}
	unknownDurationPlan := &playback.PlanV3{Timeline: playback.TimelineV3{SourceStartSeconds: 17.3}}
	configureHLSTimelineV3(unknownDurationPlan, "h264", 2, 0)
	if unknownDurationPlan.Timeline.CanSeekAnywhere {
		t.Fatalf("unknown-duration timeline = %#v", unknownDurationPlan.Timeline)
	}
}

func TestTransportGenerationV3IsUniqueAndSessionScoped(t *testing.T) {
	first := transportGenerationV3("session-1", "plan:abcdef")
	second := transportGenerationV3("session-1", "plan:abcdef")
	if first == second || !strings.HasPrefix(first, "session-1-abcdef-") || !strings.HasPrefix(second, "session-1-abcdef-") {
		t.Fatalf("generations = %q, %q", first, second)
	}
}

func TestRemuxDVModeForPlanV3ExecutesProfile8Strip(t *testing.T) {
	plan := &playback.PlanV3{Source: playback.SourceDescriptorV3{DVProfile: 8}, Transformations: []playback.TransformationV3{{Name: "dv_metadata_strip_to_hdr10"}}}
	if got := remuxDVModeForPlanV3(plan); got != playback.RemuxDVStripToHDR10V3 {
		t.Fatalf("mode = %q", got)
	}
}

func TestHandlePlaybackRouteEventV3RejectsWhileDisabled(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"playback.protocol_v3_enabled": "false"}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/route-events", strings.NewReader(`{}`)).WithContext(newAuthorizedPlaybackContext())
	rr := httptest.NewRecorder()
	handler.HandlePlaybackRouteEventV3(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestRouteEventV3HasPerUserLimitAcrossAttemptIDs(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	for i := 0; i < 600; i++ {
		attemptID := "attempt-" + strconv.Itoa(i/100)
		if !handler.allowRouteEventV3(7, attemptID) {
			t.Fatalf("event %d was rejected before the user limit", i)
		}
	}
	if handler.allowRouteEventV3(7, "attempt-rotated") {
		t.Fatal("rotating attempt IDs bypassed the per-user limit")
	}
}

func TestLegacyShadowRequestV3ProducesExplicitDetailedInference(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	legacy := startPlaybackRequest{FileID: file.ID, ProfileID: "profile-1", CodecsVideo: []string{"h264"}, CodecsAudio: []string{"aac"}, Containers: []string{"mp4"}, MaxResolution: "1080p"}
	request := legacyShadowRequestV3(legacy, file, 0, "session-1234")
	if _, err := request.NormalizeAndValidate(); err != nil {
		t.Fatalf("shadow request validation: %v", err)
	}
	if len(request.Capabilities.VideoDecode) != 1 || !request.Capabilities.VideoDecode[0].Hardware || !playback.HasFeatureV3(request.ClientFeatures, playback.FeatureDetailedDecodeV3) {
		t.Fatalf("shadow request = %#v", request)
	}
}

func v3HandlerFixtureFile(t *testing.T) *models.MediaFile {
	t.Helper()
	return &models.MediaFile{ID: 42, ContentID: "movie-1", FilePath: writePlaybackTestMediaFile(t, "movie.mp4"), Container: "mp4", CodecVideo: "h264", CodecAudio: "aac", Resolution: "1080p", Bitrate: 8_000, AudioChannels: 2, Duration: 3600, VideoTracks: []models.VideoTrack{{Codec: "h264", Profile: "high", Level: 41, Width: 1920, Height: 1080, FrameRate: "24000/1001", Bitrate: 8_000, BitDepth: 8, VideoRange: "SDR", VideoRangeType: "SDR"}}, AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, Layout: "stereo"}}}
}

func v3HandlerStartRequest() playback.StartRequestV3 {
	return playback.StartRequestV3{ProtocolVersion: playback.ProtocolV3, ClientFeatures: []string{playback.FeaturePlaybackPlanV3, playback.FeatureMedia3Only, playback.FeatureDetailedDecodeV3}, FileID: 42, ProfileID: "profile-1", PlaybackAttemptID: "attempt-handler-0001", QualityPreference: "original", SubtitleFidelityPreference: playback.SubtitleFidelityCompatibleV3, OutputRouteGeneration: 1, Capabilities: playback.ClientCodecCapabilitiesV3{CodecsVideo: []string{"h264"}, CodecsVideoHardware: []string{"h264"}, CodecsAudio: []string{"aac"}, Containers: []string{"mp4"}, MaxResolution: "1080p", VideoDecode: []playback.VideoDecodeCapabilityV3{{Codec: "h264", Profiles: []string{"high"}, Levels: []int{41}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 20_000, Hardware: true}}}, ClientPlaybackContext: playback.ClientPlaybackContextV3{ProtocolVersion: playback.ProtocolV3, Features: []string{playback.FeaturePlaybackPlanV3, playback.FeatureMedia3Only, playback.FeatureDetailedDecodeV3}, Platform: "android", FormFactor: "tv", AppVersion: "test", Output: playback.OutputContextV3{OutputRouteGeneration: 1}, Engines: map[string]playback.EngineCapabilityV3{string(playback.EngineMedia3DirectV3): {Enabled: true, SupportedOnDevice: true, Subtitles: playback.EngineSubtitleCapabilitiesV3{EmbeddedText: true, SidecarText: true}}}}}
}

func marshalV3StartRequest(t *testing.T, request playback.StartRequestV3) string {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
