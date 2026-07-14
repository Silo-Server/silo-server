package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

type mutablePlaybackSettingsV3 struct {
	mu     sync.Mutex
	values map[string]string
}

type failingCompletePlanStoreV3 struct {
	playback.PlanStoreV3
}

type staticNodePlannerV3 struct {
	plan nodepool.Plan
}

func (p staticNodePlannerV3) PlanSession(string, string, bool, int) nodepool.Plan {
	return p.plan
}

func (f *failingCompletePlanStoreV3) CompleteReplan(context.Context, string, string, string, json.RawMessage, playback.AttemptRecordV3) error {
	return fmt.Errorf("injected complete replan failure")
}

func TestShouldTryAlternateFileV3PinsOriginalQuality(t *testing.T) {
	if shouldTryAlternateFileV3("original") || shouldTryAlternateFileV3(" ORIGINAL ") {
		t.Fatal("original quality must pin the requested media file")
	}
	for _, quality := range []string{"auto", "2160p", "1080p", "480p"} {
		if !shouldTryAlternateFileV3(quality) {
			t.Fatalf("quality %q should permit alternate selection", quality)
		}
	}
}

func TestReplanAllowsAlternateFileV3PinsSeekOperations(t *testing.T) {
	tests := []struct {
		name      string
		operation playback.ReplanOperationV3
		quality   string
		want      bool
	}{
		{name: "ordinary failure may use another version", operation: playback.ReplanOperationFailureRecoveryV3, quality: "auto", want: true},
		{name: "original quality remains pinned", operation: playback.ReplanOperationFailureRecoveryV3, quality: "original", want: false},
		{name: "exact seek reanchor pins current version", operation: playback.ReplanOperationSeekReanchorV3, quality: "auto", want: false},
		{name: "failed seek recovery pins current version", operation: playback.ReplanOperationSeekFailureRecoveryV3, quality: "auto", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := replanAllowsAlternateFileV3(test.operation, test.quality); got != test.want {
				t.Fatalf("replanAllowsAlternateFileV3(%q, %q) = %v, want %v", test.operation, test.quality, got, test.want)
			}
		})
	}
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
	// The flag stays DB-backed (no restart needed for rollback) behind a
	// short TTL cache; expiring the cache stands in for the TTL elapsing.
	handler.v3FlagMu.Lock()
	handler.v3Flags = nil
	handler.v3FlagMu.Unlock()
	if response := request(); !response.Enabled || len(response.Deliveries) != 4 || !playback.HasFeatureV3(response.Features, playback.FeatureSeekReanchorV3) {
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
	bandwidthEstimate := 3_500
	bandwidthCap := 4_000
	failedKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, startRequest.OutputRouteGeneration, nil)
	replan := playback.ReplanRequestV3{ProtocolVersion: playback.ProtocolV3, PlaybackAttemptID: startRequest.PlaybackAttemptID, ReplanRequestID: "replan-0001", FailedPlanID: started.PlaybackPlan.PlanID, PlanAttemptID: "plan-attempt-0001", PlanAttemptKey: failedKey, AttemptedPlanKeys: []string{failedKey}, AttemptCount: 1, QualityPreference: "original", PositionSeconds: 12, OutputRouteGeneration: startRequest.OutputRouteGeneration, Metered: true, BandwidthEstimateKbps: &bandwidthEstimate, BandwidthCapKbps: &bandwidthCap, SelectedTracks: playback.SelectedTracksV3{Audio: &playback.TrackIdentityV3{ID: playback.TrackIDV3(file.ID, "audio", audioIndex), Index: &audioIndex}}, Failure: playback.FailureV3{Classification: "audio_renderer_error"}, Capabilities: startRequest.Capabilities, ClientPlaybackContext: startRequest.ClientPlaybackContext}
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
	record, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !record.NormalizedRequest.Metered || record.NormalizedRequest.BandwidthEstimateKbps == nil || *record.NormalizedRequest.BandwidthEstimateKbps != bandwidthEstimate ||
		record.NormalizedRequest.BandwidthCapKbps == nil || *record.NormalizedRequest.BandwidthCapKbps != bandwidthCap {
		t.Fatalf("stored replan network evidence = %#v", record.NormalizedRequest)
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

func TestHandleReplanPlaybackV3RollsBackLiveSessionWhenPersistenceFails(t *testing.T) {
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
	if err := json.Unmarshal(startRR.Body.Bytes(), &started); err != nil || started.PlaybackPlan == nil {
		t.Fatalf("start response: err=%v response=%#v", err, started)
	}
	beforeSession, err := manager.GetSession(started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRecord, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	underlyingStore := handler.PlanStoreV3
	handler.PlanStoreV3 = &failingCompletePlanStoreV3{PlanStoreV3: underlyingStore}

	audioIndex := 1
	failedKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, startRequest.OutputRouteGeneration, nil)
	replan := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "replan-rollback-0001",
		FailedPlanID:          started.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-rollback-0001",
		PlanAttemptKey:        failedKey,
		AttemptedPlanKeys:     []string{failedKey},
		AttemptCount:          1,
		QualityPreference:     "original",
		PositionSeconds:       12,
		OutputRouteGeneration: startRequest.OutputRouteGeneration,
		SelectedTracks: playback.SelectedTracksV3{Audio: &playback.TrackIdentityV3{
			ID: playback.TrackIDV3(file.ID, "audio", audioIndex), Index: &audioIndex,
		}},
		Failure:               playback.FailureV3{Classification: "audio_renderer_error"},
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	body, err := json.Marshal(replan)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
	req = withPlaybackRouteParam(req, "session_id", started.SessionID)
	rr := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("replan status = %d, body = %s", rr.Code, rr.Body.String())
	}

	afterSession, err := manager.GetSession(started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if afterSession.MediaFileID != beforeSession.MediaFileID || afterSession.AudioTrackIndex != beforeSession.AudioTrackIndex ||
		afterSession.PlayMethod != beforeSession.PlayMethod || afterSession.TranscodeNodeURL != beforeSession.TranscodeNodeURL {
		t.Fatalf("live session was not rolled back: before=%#v after=%#v", beforeSession, afterSession)
	}
	afterRecord, err := underlyingStore.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if afterRecord.CurrentPlanID != beforeRecord.CurrentPlanID || afterRecord.CurrentReplanRequestID != beforeRecord.CurrentReplanRequestID {
		t.Fatalf("durable attempt changed after failed commit: before=%#v after=%#v", beforeRecord, afterRecord)
	}
}

func TestHandleReplanPlaybackV3SeekReanchorKeepsCurrentRecipeEligible(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "ass", Language: "eng"}}
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"playback.protocol_v3_enabled": "true", "allow_4k_transcode": "false"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	subtitleIndex := 0
	startRequest.SubtitleTrackID = playback.TrackIDV3(file.ID, "subtitle", subtitleIndex)
	startRequest.SubtitleTrackIndex = &subtitleIndex
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
	if started.PlaybackPlan.Subtitle.Artifact == nil || started.PlaybackPlan.Subtitle.Artifact.Format != "ass" {
		t.Fatalf("start returned no ASS artifact: %#v", started.PlaybackPlan.Subtitle)
	}
	if err := manager.UpdateProgress(started.SessionID, 12, true); err != nil {
		t.Fatal(err)
	}
	currentKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, startRequest.OutputRouteGeneration, nil)
	reanchor := playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationSeekReanchorV3,
		PlaybackAttemptID: startRequest.PlaybackAttemptID,
		ReplanRequestID:   "seek-reanchor-0001", FailedPlanID: started.PlaybackPlan.PlanID,
		PlanAttemptID: "plan-attempt-seek-0001", PlanAttemptKey: currentKey,
		// Clients may include the current key defensively. A seek reanchor must
		// ignore it because the recipe did not fail.
		AttemptedPlanKeys: []string{currentKey}, AttemptCount: 1,
		QualityPreference: "original", PositionSeconds: 321,
		OutputRouteGeneration: startRequest.OutputRouteGeneration,
		SelectedTracks:        started.PlaybackPlan.SelectedTracks,
		Failure:               playback.FailureV3{},
		Capabilities:          startRequest.Capabilities, ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	body, err := json.Marshal(reanchor)
	if err != nil {
		t.Fatal(err)
	}
	call := func() playback.DecisionResponseV3 {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
		req = withPlaybackRouteParam(req, "session_id", started.SessionID)
		rr := httptest.NewRecorder()
		handler.HandleReplanPlaybackV3(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("reanchor status = %d, body = %s", rr.Code, rr.Body.String())
		}
		var response playback.DecisionResponseV3
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	first := call()
	second := call()
	for index, response := range []playback.DecisionResponseV3{first, second} {
		if response.PlaybackPlan == nil || response.SessionID != started.SessionID ||
			response.PlaybackPlan.SessionID != started.SessionID ||
			response.PlaybackPlan.PlanID != started.PlaybackPlan.PlanID ||
			response.PlaybackPlan.RequestedMediaFileID != started.PlaybackPlan.RequestedMediaFileID ||
			response.PlaybackPlan.EffectiveMediaFileID != started.PlaybackPlan.EffectiveMediaFileID ||
			!sameSelectedTracksV3(response.PlaybackPlan.SelectedTracks, started.PlaybackPlan.SelectedTracks) ||
			response.PlaybackPlan.Timeline.SourceStartSeconds != 321 ||
			playback.PlanAttemptKeyV3(*response.PlaybackPlan, startRequest.OutputRouteGeneration, nil) != currentKey {
			t.Fatalf("reanchored response %d = %#v", index, response.PlaybackPlan)
		}
		if response.PlaybackPlan.Subtitle.Artifact == nil ||
			response.PlaybackPlan.Subtitle.Artifact.Format != started.PlaybackPlan.Subtitle.Artifact.Format ||
			response.PlaybackPlan.Subtitle.Artifact.MIMEType != started.PlaybackPlan.Subtitle.Artifact.MIMEType {
			t.Fatalf("reanchored response %d changed the ASS artifact: %#v", index, response.PlaybackPlan.Subtitle)
		}
		if !playback.HasFeatureV3(response.ServerFeatures, playback.FeatureSeekReanchorV3) {
			t.Fatalf("reanchored response %d omitted %q: %#v", index, playback.FeatureSeekReanchorV3, response.ServerFeatures)
		}
	}
	liveSessionManager := handler.sessionMgr
	handler.sessionMgr = playback.NewSessionManager(0, 0)
	restartReplayReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
	restartReplayReq = withPlaybackRouteParam(restartReplayReq, "session_id", started.SessionID)
	restartReplayRR := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(restartReplayRR, restartReplayReq)
	handler.sessionMgr = liveSessionManager
	if restartReplayRR.Code != http.StatusNotFound || !strings.Contains(restartReplayRR.Body.String(), playbackSessionNotFoundErrorCode) {
		t.Fatalf("restart replay status = %d, body = %s", restartReplayRR.Code, restartReplayRR.Body.String())
	}
	session, err := manager.GetSession(started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.Position != 321 || !session.IsPaused {
		t.Fatalf("session progress = (%v, paused=%v), want (321, paused=true)", session.Position, session.IsPaused)
	}
	record, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if record.CurrentPlanID != started.PlaybackPlan.PlanID || record.EffectiveMediaFileID != file.ID ||
		record.NormalizedRequest.StartPosition == nil || *record.NormalizedRequest.StartPosition != 321 {
		t.Fatalf("stored reanchor = %#v", record)
	}

	mismatch := reanchor
	mismatch.ReplanRequestID = "seek-reanchor-mismatch-0001"
	mismatch.QualityPreference = "480p"
	mismatchBody, err := json.Marshal(mismatch)
	if err != nil {
		t.Fatal(err)
	}
	mismatchReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(mismatchBody))).WithContext(newAuthorizedPlaybackContext())
	mismatchReq = withPlaybackRouteParam(mismatchReq, "session_id", started.SessionID)
	mismatchRR := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(mismatchRR, mismatchReq)
	if mismatchRR.Code != http.StatusOK {
		t.Fatalf("mismatch status = %d, body = %s", mismatchRR.Code, mismatchRR.Body.String())
	}
	var mismatchResponse playback.DecisionResponseV3
	if err := json.Unmarshal(mismatchRR.Body.Bytes(), &mismatchResponse); err != nil {
		t.Fatal(err)
	}
	if mismatchResponse.Terminal == nil || mismatchResponse.Terminal.Reason != "seek_reanchor_intent_mismatch" {
		t.Fatalf("mismatch response = %#v", mismatchResponse)
	}
	recordAfterMismatch, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if recordAfterMismatch.CurrentPlanID != record.CurrentPlanID || recordAfterMismatch.NormalizedRequest.StartPosition == nil || *recordAfterMismatch.NormalizedRequest.StartPosition != 321 {
		t.Fatalf("intent mismatch changed stored route: before=%#v after=%#v", record, recordAfterMismatch)
	}

	newer := reanchor
	newer.ReplanRequestID = "seek-reanchor-0002"
	newer.PositionSeconds = 500
	newerBody, err := json.Marshal(newer)
	if err != nil {
		t.Fatal(err)
	}
	newerReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(newerBody))).WithContext(newAuthorizedPlaybackContext())
	newerReq = withPlaybackRouteParam(newerReq, "session_id", started.SessionID)
	newerRR := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(newerRR, newerReq)
	if newerRR.Code != http.StatusOK {
		t.Fatalf("newer reanchor status = %d, body = %s", newerRR.Code, newerRR.Body.String())
	}

	staleRetryReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
	staleRetryReq = withPlaybackRouteParam(staleRetryReq, "session_id", started.SessionID)
	staleRetryRR := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(staleRetryRR, staleRetryReq)
	if staleRetryRR.Code != http.StatusConflict || !strings.Contains(staleRetryRR.Body.String(), "stale_playback_plan") {
		t.Fatalf("stale reanchor replay status = %d, body = %s", staleRetryRR.Code, staleRetryRR.Body.String())
	}
	latestRecord, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if latestRecord.CurrentReplanRequestID != newer.ReplanRequestID || latestRecord.NormalizedRequest.StartPosition == nil || *latestRecord.NormalizedRequest.StartPosition != newer.PositionSeconds {
		t.Fatalf("stale replay changed latest reanchor: %#v", latestRecord)
	}
	// Simulate a rolling-deploy writer which updated the current plan but did
	// not know about CurrentReplanRequestID. The durable plan comparison must
	// still reject A's cached response after B became active.
	mixedWriterRecord := *latestRecord
	mixedWriterRecord.CurrentReplanRequestID = reanchor.ReplanRequestID
	handler.PlanStoreV3.(*playback.MemoryPlanStoreV3).ReplaceAttempt(context.Background(), mixedWriterRecord)
	mixedWriterRetryReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
	mixedWriterRetryReq = withPlaybackRouteParam(mixedWriterRetryReq, "session_id", started.SessionID)
	mixedWriterRetryRR := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(mixedWriterRetryRR, mixedWriterRetryReq)
	if mixedWriterRetryRR.Code != http.StatusConflict || !strings.Contains(mixedWriterRetryRR.Body.String(), "stale_playback_plan") {
		t.Fatalf("mixed-writer stale replay status = %d, body = %s", mixedWriterRetryRR.Code, mixedWriterRetryRR.Body.String())
	}

	beyondEnd := newer
	beyondEnd.ReplanRequestID = "seek-reanchor-beyond-end-0001"
	beyondEnd.PositionSeconds = float64(file.Duration) + 1
	beyondBody, err := json.Marshal(beyondEnd)
	if err != nil {
		t.Fatal(err)
	}
	beyondReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(beyondBody))).WithContext(newAuthorizedPlaybackContext())
	beyondReq = withPlaybackRouteParam(beyondReq, "session_id", started.SessionID)
	beyondRR := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(beyondRR, beyondReq)
	if beyondRR.Code != http.StatusOK || !strings.Contains(beyondRR.Body.String(), "invalid_seek_position") {
		t.Fatalf("beyond-end reanchor status = %d, body = %s", beyondRR.Code, beyondRR.Body.String())
	}

	missingSince := time.Now()
	file.MissingSince = &missingSince
	missing := newer
	missing.ReplanRequestID = "seek-reanchor-missing-source-0001"
	missing.PositionSeconds = 600
	missingBody, err := json.Marshal(missing)
	if err != nil {
		t.Fatal(err)
	}
	missingReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(missingBody))).WithContext(newAuthorizedPlaybackContext())
	missingReq = withPlaybackRouteParam(missingReq, "session_id", started.SessionID)
	missingRR := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(missingRR, missingReq)
	if missingRR.Code != http.StatusOK || !strings.Contains(missingRR.Body.String(), "source_unavailable") {
		t.Fatalf("missing-source reanchor status = %d, body = %s", missingRR.Code, missingRR.Body.String())
	}
	unchanged, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.CurrentPlanID != latestRecord.CurrentPlanID || unchanged.EffectiveMediaFileID != latestRecord.EffectiveMediaFileID ||
		unchanged.NormalizedRequest.StartPosition == nil || *unchanged.NormalizedRequest.StartPosition != newer.PositionSeconds {
		t.Fatalf("rejected seeks changed the active route: before=%#v after=%#v", latestRecord, unchanged)
	}
}

func TestHandleReplanPlaybackV3SeekFailureRecoveryNeverChangesMediaVersion(t *testing.T) {
	source := v3HandlerFixtureFile(t)
	source.Resolution = "2160p"
	source.Bitrate = 32_000
	source.VideoTracks = append([]models.VideoTrack(nil), source.VideoTracks...)
	source.VideoTracks[0].Level = 51
	source.VideoTracks[0].Width = 3840
	source.VideoTracks[0].Height = 2160
	source.VideoTracks[0].Bitrate = 32_000

	alternateValue := *source
	alternate := &alternateValue
	alternate.ID = 84
	alternate.Resolution = "1080p"
	alternate.Bitrate = 8_000
	alternate.VideoTracks = append([]models.VideoTrack(nil), source.VideoTracks...)
	alternate.VideoTracks[0].Level = 41
	alternate.VideoTracks[0].Width = 1920
	alternate.VideoTracks[0].Height = 1080
	alternate.VideoTracks[0].Bitrate = 8_000

	manager := playback.NewSessionManager(0, 0)
	files := map[int]*models.MediaFile{
		source.ID: source, alternate.ID: alternate,
	}
	handler := NewPlaybackHandler(manager, mapPlaybackFileResolver{files: files})
	handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{
		source.ContentID: {source, alternate},
	}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"playback.protocol_v3_enabled": "true", "allow_4k_transcode": "false"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	startRequest := v3HandlerStartRequest()
	startRequest.QualityPreference = "auto"
	startRequest.Capabilities.MaxResolution = "2160p"
	startRequest.Capabilities.VideoDecode[0].Levels = []int{51}
	startRequest.Capabilities.VideoDecode[0].MaxWidth = 3840
	startRequest.Capabilities.VideoDecode[0].MaxHeight = 2160
	startRequest.Capabilities.VideoDecode[0].MaxBitrateKbps = 50_000
	startRequest.ClientPlaybackContext.Engines[string(playback.EngineMedia3HLSV3)] = playback.EngineCapabilityV3{Enabled: true, SupportedOnDevice: true}

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
	if started.PlaybackPlan == nil || started.PlaybackPlan.EffectiveMediaFileID != source.ID {
		t.Fatalf("initial 4K plan = %#v", started.PlaybackPlan)
	}
	if err := manager.UpdateProgress(started.SessionID, 15, true); err != nil {
		t.Fatal(err)
	}

	seekCapabilities := startRequest.Capabilities
	seekCapabilities.VideoDecode = append([]playback.VideoDecodeCapabilityV3(nil), startRequest.Capabilities.VideoDecode...)
	seekCapabilities.MaxResolution = "1080p"
	seekCapabilities.VideoDecode[0].MaxWidth = 1920
	seekCapabilities.VideoDecode[0].MaxHeight = 1080
	seekCapabilities.VideoDecode[0].MaxBitrateKbps = 20_000
	seekContext := startRequest.ClientPlaybackContext
	seekContext.Device.Model = "request-only-model"
	currentKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, startRequest.OutputRouteGeneration, nil)
	staleClientKey := "v3:0000000000000000"
	replan := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationSeekFailureRecoveryV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "seek-failure-recovery-0001",
		FailedPlanID:          started.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-seek-failure-0001",
		PlanAttemptKey:        staleClientKey,
		AttemptedPlanKeys:     nil,
		AttemptCount:          1,
		QualityPreference:     startRequest.QualityPreference,
		PositionSeconds:       417,
		OutputRouteGeneration: startRequest.OutputRouteGeneration,
		SelectedTracks:        started.PlaybackPlan.SelectedTracks,
		Failure:               playback.FailureV3{Classification: "decoder_failure", Message: "reanchored route failed"},
		Capabilities:          seekCapabilities,
		ClientPlaybackContext: seekContext,
	}
	body, err := json.Marshal(replan)
	if err != nil {
		t.Fatal(err)
	}
	replanReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
	replanReq = withPlaybackRouteParam(replanReq, "session_id", started.SessionID)
	replanRR := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(replanRR, replanReq)
	if replanRR.Code != http.StatusOK {
		t.Fatalf("replan status = %d, body = %s", replanRR.Code, replanRR.Body.String())
	}
	var response playback.DecisionResponseV3
	if err := json.Unmarshal(replanRR.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.PlaybackPlan == nil || response.Terminal != nil ||
		response.PlaybackPlan.RequestedMediaFileID != source.ID || response.PlaybackPlan.EffectiveMediaFileID != source.ID {
		t.Fatalf("failed seek did not recover on the pinned media version: %#v", response)
	}
	if response.PlaybackPlan.Delivery == playback.DeliveryTranscodeHLSV3 ||
		response.PlaybackPlan.EffectiveRecipe.Width == nil || *response.PlaybackPlan.EffectiveRecipe.Width != 3840 ||
		response.PlaybackPlan.EffectiveRecipe.Height == nil || *response.PlaybackPlan.EffectiveRecipe.Height != 2160 {
		t.Fatalf("failed seek downgraded or video-transcoded the pinned 4K source: %#v", response.PlaybackPlan)
	}
	if playback.PlanAttemptKeyV3(*response.PlaybackPlan, startRequest.OutputRouteGeneration, nil) == currentKey {
		t.Fatalf("failed seek retried the failed route: %#v", response.PlaybackPlan)
	}
	record, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if record.EffectiveMediaFileID != source.ID || record.CurrentPlan.EffectiveMediaFileID != source.ID {
		t.Fatalf("failed seek changed the durable media version: %#v", record)
	}
	if record.NormalizedRequest.Capabilities.MaxResolution != startRequest.Capabilities.MaxResolution ||
		record.NormalizedRequest.ClientPlaybackContext.Device.Model != startRequest.ClientPlaybackContext.Device.Model {
		t.Fatalf("failed seek accepted request-only capability evidence: %#v", record.NormalizedRequest)
	}
	session, err := manager.GetSession(started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.Position != replan.PositionSeconds || !session.IsPaused {
		t.Fatalf("session progress = (%v, paused=%v), want (%v, paused=true)", session.Position, session.IsPaused, replan.PositionSeconds)
	}

	delete(files, source.ID)
	for index, operation := range []playback.ReplanOperationV3{
		playback.ReplanOperationSeekReanchorV3,
		playback.ReplanOperationSeekFailureRecoveryV3,
	} {
		missing := replan
		missing.Operation = operation
		missing.ReplanRequestID = fmt.Sprintf("seek-missing-current-%04d", index)
		missing.FailedPlanID = response.PlaybackPlan.PlanID
		missing.PlanAttemptKey = playback.PlanAttemptKeyV3(*response.PlaybackPlan, startRequest.OutputRouteGeneration, nil)
		missing.AttemptedPlanKeys = []string{missing.PlanAttemptKey}
		missing.SelectedTracks = response.PlaybackPlan.SelectedTracks
		missing.PositionSeconds++
		if operation == playback.ReplanOperationSeekReanchorV3 {
			missing.Failure = playback.FailureV3{}
		}
		missingBody, err := json.Marshal(missing)
		if err != nil {
			t.Fatal(err)
		}
		missingReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(missingBody))).WithContext(newAuthorizedPlaybackContext())
		missingReq = withPlaybackRouteParam(missingReq, "session_id", started.SessionID)
		missingRR := httptest.NewRecorder()
		handler.HandleReplanPlaybackV3(missingRR, missingReq)
		if missingRR.Code != http.StatusOK || !strings.Contains(missingRR.Body.String(), "source_unavailable") {
			t.Fatalf("missing current %s status = %d, body = %s", operation, missingRR.Code, missingRR.Body.String())
		}
	}
	finalRecord, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	finalSession, err := manager.GetSession(started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if finalRecord.EffectiveMediaFileID != source.ID || finalRecord.CurrentPlan.EffectiveMediaFileID != source.ID || finalSession.MediaFileID != source.ID {
		t.Fatalf("missing 4K source fell through to alternate: record=%#v session=%#v", finalRecord, finalSession)
	}
}

func TestHandleReplanPlaybackV3SeekUsesEffectiveEditionWhenRequestedEditionIsGone(t *testing.T) {
	effective := v3HandlerFixtureFile(t)
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, mapPlaybackFileResolver{files: map[int]*models.MediaFile{effective.ID: effective}})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"playback.protocol_v3_enabled": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	startRequest := v3HandlerStartRequest()
	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext())
	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, startReq)
	if startRR.Code != http.StatusCreated {
		t.Fatalf("start status = %d, body = %s", startRR.Code, startRR.Body.String())
	}
	var started playback.DecisionResponseV3
	if err := json.Unmarshal(startRR.Body.Bytes(), &started); err != nil || started.PlaybackPlan == nil {
		t.Fatalf("start response: err=%v response=%#v", err, started)
	}

	const missingRequestedID = 84
	record, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	record.RequestedMediaFileID = missingRequestedID
	record.NormalizedRequest.FileID = missingRequestedID
	record.CurrentPlan.RequestedMediaFileID = missingRequestedID
	record.CurrentPlan.PlanID = playback.DeterministicPlanIDV3(
		record.PlaybackAttemptID,
		missingRequestedID,
		effective.ID,
		record.CurrentPlan,
	)
	record.CurrentPlanID = record.CurrentPlan.PlanID
	handler.PlanStoreV3.(*playback.MemoryPlanStoreV3).ReplaceAttempt(context.Background(), *record)

	currentKey := playback.PlanAttemptKeyV3(record.CurrentPlan, record.NormalizedRequest.OutputRouteGeneration, nil)
	reanchor := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationSeekReanchorV3,
		PlaybackAttemptID:     record.PlaybackAttemptID,
		ReplanRequestID:       "seek-missing-requested-0001",
		FailedPlanID:          record.CurrentPlanID,
		PlanAttemptID:         "plan-attempt-missing-requested-0001",
		PlanAttemptKey:        currentKey,
		AttemptedPlanKeys:     []string{currentKey},
		AttemptCount:          1,
		QualityPreference:     record.NormalizedRequest.QualityPreference,
		PositionSeconds:       300,
		OutputRouteGeneration: record.NormalizedRequest.OutputRouteGeneration,
		SelectedTracks:        record.CurrentPlan.SelectedTracks,
		Capabilities:          record.NormalizedRequest.Capabilities,
		ClientPlaybackContext: record.NormalizedRequest.ClientPlaybackContext,
	}
	body, err := json.Marshal(reanchor)
	if err != nil {
		t.Fatal(err)
	}
	reanchorReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
	reanchorReq = withPlaybackRouteParam(reanchorReq, "session_id", started.SessionID)
	reanchorRR := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(reanchorRR, reanchorReq)
	if reanchorRR.Code != http.StatusOK {
		t.Fatalf("reanchor status = %d, body = %s", reanchorRR.Code, reanchorRR.Body.String())
	}
	var response playback.DecisionResponseV3
	if err := json.Unmarshal(reanchorRR.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.PlaybackPlan == nil || response.PlaybackPlan.RequestedMediaFileID != missingRequestedID ||
		response.PlaybackPlan.EffectiveMediaFileID != effective.ID || response.PlaybackPlan.Timeline.SourceStartSeconds != reanchor.PositionSeconds {
		t.Fatalf("reanchor did not retain the effective edition: %#v", response)
	}

	current, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	outputContext := current.NormalizedRequest.ClientPlaybackContext
	outputContext.Output.OutputRouteGeneration++
	currentKey = playback.PlanAttemptKeyV3(current.CurrentPlan, current.NormalizedRequest.OutputRouteGeneration, nil)
	ordinary := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		PlaybackAttemptID:     current.PlaybackAttemptID,
		ReplanRequestID:       "ordinary-missing-requested-0001",
		FailedPlanID:          current.CurrentPlanID,
		PlanAttemptID:         "plan-attempt-ordinary-missing-0001",
		PlanAttemptKey:        currentKey,
		AttemptedPlanKeys:     []string{currentKey},
		AttemptCount:          1,
		QualityPreference:     current.NormalizedRequest.QualityPreference,
		PositionSeconds:       320,
		OutputRouteGeneration: outputContext.Output.OutputRouteGeneration,
		SelectedTracks:        current.CurrentPlan.SelectedTracks,
		Failure:               playback.FailureV3{Classification: "output_route_changed"},
		Capabilities:          current.NormalizedRequest.Capabilities,
		ClientPlaybackContext: outputContext,
	}
	ordinaryBody, err := json.Marshal(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	ordinaryReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(ordinaryBody))).WithContext(newAuthorizedPlaybackContext())
	ordinaryReq = withPlaybackRouteParam(ordinaryReq, "session_id", started.SessionID)
	ordinaryRR := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(ordinaryRR, ordinaryReq)
	if ordinaryRR.Code != http.StatusOK {
		t.Fatalf("ordinary replan status = %d, body = %s", ordinaryRR.Code, ordinaryRR.Body.String())
	}
	var ordinaryResponse playback.DecisionResponseV3
	if err := json.Unmarshal(ordinaryRR.Body.Bytes(), &ordinaryResponse); err != nil {
		t.Fatal(err)
	}
	if ordinaryResponse.PlaybackPlan == nil || ordinaryResponse.PlaybackPlan.RequestedMediaFileID != missingRequestedID ||
		ordinaryResponse.PlaybackPlan.EffectiveMediaFileID != effective.ID {
		t.Fatalf("ordinary replan lost the requested/effective split: %#v", ordinaryResponse)
	}
}

func TestValidateSeekRecoveryRequestV3PinsCurrentIntent(t *testing.T) {
	audioIndex := 0
	start := v3HandlerStartRequest()
	start.AudioTrackID = playback.TrackIDV3(start.FileID, "audio", audioIndex)
	start.AudioTrackIndex = &audioIndex
	selected := playback.SelectedTracksV3{Audio: &playback.TrackIdentityV3{ID: start.AudioTrackID, Index: &audioIndex}}
	plan := playback.PlanV3{
		PlanID:               "plan:seek-current",
		RequestedMediaFileID: start.FileID,
		EffectiveMediaFileID: start.FileID,
		SelectedTracks:       selected,
	}
	record := &playback.AttemptRecordV3{
		RequestedMediaFileID: start.FileID,
		EffectiveMediaFileID: start.FileID,
		CurrentPlanID:        plan.PlanID,
		CurrentPlan:          plan,
		NormalizedRequest:    start,
	}
	request := playback.ReplanRequestV3{
		Operation:             playback.ReplanOperationSeekReanchorV3,
		QualityPreference:     start.QualityPreference,
		OutputRouteGeneration: start.OutputRouteGeneration,
		SelectedTracks:        selected,
	}
	if err := validateSeekRecoveryRequestV3(record, request); err != nil {
		t.Fatalf("valid seek reanchor guard: %v", err)
	}

	// Capabilities and client context are structurally validated by the request
	// decoder but are not route inputs for a same-recipe reanchor.
	request.Capabilities.CodecsVideo = []string{"av1"}
	request.ClientPlaybackContext.Device.Model = "changed-client-claim"
	if err := validateSeekRecoveryRequestV3(record, request); err != nil {
		t.Fatalf("ignored client evidence changed stored intent: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*playback.ReplanRequestV3)
	}{
		{name: "quality", mutate: func(value *playback.ReplanRequestV3) { value.QualityPreference = "480p" }},
		{name: "output route", mutate: func(value *playback.ReplanRequestV3) { value.OutputRouteGeneration++ }},
		{name: "audio track", mutate: func(value *playback.ReplanRequestV3) {
			index := 1
			value.SelectedTracks.Audio = &playback.TrackIdentityV3{ID: playback.TrackIDV3(start.FileID, "audio", index), Index: &index}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := request
			test.mutate(&candidate)
			if err := validateSeekRecoveryRequestV3(record, candidate); err == nil {
				t.Fatalf("%s change was accepted", test.name)
			}
		})
	}
}

func TestValidateSeekReanchorPlanV3RejectsRouteDrift(t *testing.T) {
	audioIndex := 0
	frameRate := 23.976
	audioChannels := 2
	current := playback.PlanV3{
		PlanID:          "plan:seek-current",
		Delivery:        playback.DeliveryOriginalHTTPV3,
		Engine:          playback.EngineMedia3DirectV3,
		Stream:          playback.StreamV3{Protocol: playback.StreamHTTPProgressiveV3, Container: "mp4", MIMEType: "video/mp4", HeaderRefresh: playback.HeaderRefreshNoneV3},
		SelectedTracks:  playback.SelectedTracksV3{Audio: &playback.TrackIdentityV3{ID: playback.TrackIDV3(42, "audio", audioIndex), Index: &audioIndex}},
		EffectiveRecipe: playback.EffectiveRecipeV3{VideoCodec: "h264", AudioCodec: "aac", FrameRate: &frameRate, AudioChannels: &audioChannels, AudioLayout: "stereo"},
		Claims:          playback.ValidationClaimsV3{Video: playback.VideoClaimsV3{HDR10: true}},
		Subtitle: playback.SubtitleDecisionV3{Mode: playback.SubtitleRenderV3, TrackID: playback.TrackIDV3(42, "subtitle", 0), Artifact: &playback.SubtitleArtifactV3{
			MIMEType: "text/x-ssa", Format: "ass",
		}},
		Transformations:        []playback.TransformationV3{{Name: "subtitle-convert", Executor: "ffmpeg", RecipeVersion: "1", ValidatedClaims: []string{"ass"}}},
		AppliedQuirks:          []playback.AppliedQuirkV3{{ID: "quirk-1", RegistryRevision: "1", Action: "force-remux"}},
		RuntimeCorrections:     []string{"pcm_fallback"},
		RequestedMediaFileID:   42,
		EffectiveMediaFileID:   42,
		SubtitleFidelityPolicy: "preserve_styling",
	}
	record := &playback.AttemptRecordV3{
		RequestedMediaFileID: 42,
		EffectiveMediaFileID: 42,
		CurrentPlanID:        current.PlanID,
		CurrentPlan:          current,
		NormalizedRequest:    playback.StartRequestV3{OutputRouteGeneration: 9},
	}
	candidate := current
	candidate.Timeline.SourceStartSeconds = 321
	if err := validateSeekReanchorPlanV3(record, &candidate); err != nil {
		t.Fatalf("timeline-only change rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*playback.PlanV3)
	}{
		{name: "delivery recipe", mutate: func(value *playback.PlanV3) { value.Delivery = playback.DeliveryRemuxProgressiveV3 }},
		{name: "engine", mutate: func(value *playback.PlanV3) { value.Engine = playback.EngineMedia3HLSV3 }},
		{name: "stream MIME", mutate: func(value *playback.PlanV3) { value.Stream.MIMEType = "application/x-mpegURL" }},
		{name: "header refresh", mutate: func(value *playback.PlanV3) { value.Stream.HeaderRefresh = playback.HeaderRefreshSessionV3 }},
		{name: "frame rate", mutate: func(value *playback.PlanV3) {
			changed := 24.0
			value.EffectiveRecipe.FrameRate = &changed
		}},
		{name: "audio channels", mutate: func(value *playback.PlanV3) {
			changed := 6
			value.EffectiveRecipe.AudioChannels = &changed
		}},
		{name: "audio layout", mutate: func(value *playback.PlanV3) { value.EffectiveRecipe.AudioLayout = "5.1" }},
		{name: "claims", mutate: func(value *playback.PlanV3) { value.Claims.Video.HDR10 = false }},
		{name: "subtitle artifact MIME", mutate: func(value *playback.PlanV3) {
			copy := *value.Subtitle.Artifact
			copy.MIMEType = "text/vtt"
			value.Subtitle.Artifact = &copy
		}},
		{name: "subtitle artifact format", mutate: func(value *playback.PlanV3) {
			copy := *value.Subtitle.Artifact
			copy.Format = "vtt"
			value.Subtitle.Artifact = &copy
		}},
		{name: "subtitle fidelity", mutate: func(value *playback.PlanV3) { value.SubtitleFidelityPolicy = "compatibility" }},
		{name: "transformation claims", mutate: func(value *playback.PlanV3) {
			value.Transformations = append([]playback.TransformationV3(nil), value.Transformations...)
			value.Transformations[0].ValidatedClaims = []string{"vtt"}
		}},
		{name: "quirk action", mutate: func(value *playback.PlanV3) {
			value.AppliedQuirks = append([]playback.AppliedQuirkV3(nil), value.AppliedQuirks...)
			value.AppliedQuirks[0].Action = "disable-passthrough"
		}},
		{name: "effective version", mutate: func(value *playback.PlanV3) { value.EffectiveMediaFileID = 84 }},
		{name: "track", mutate: func(value *playback.PlanV3) {
			index := 1
			value.SelectedTracks.Audio = &playback.TrackIdentityV3{ID: playback.TrackIDV3(42, "audio", index), Index: &index}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			drifted := candidate
			test.mutate(&drifted)
			if err := validateSeekReanchorPlanV3(record, &drifted); err == nil {
				t.Fatalf("%s drift was accepted", test.name)
			}
		})
	}
}

func TestPrepareIdentityTransportV3ProgressiveRemuxNeverAdvertisesNativeSeek(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	session := &playback.Session{ID: "session-progressive", UserID: 7, ProfileID: "profile-1", MediaFileID: 42, PlayMethod: playback.PlayRemux, BasePlayMethod: playback.PlayRemux, AudioTrackIndex: 0}
	for index, origin := range []float64{321.25, 654.5} {
		plan := &playback.PlanV3{
			PlanID:               "plan:progressive",
			Delivery:             playback.DeliveryRemuxProgressiveV3,
			EffectiveMediaFileID: 42,
			Timeline:             playback.TimelineV3{SourceStartSeconds: origin, PlayerStartSeconds: origin, CanSeekAnywhere: true, SeekRestoration: "player_position"},
		}
		transport := handler.prepareIdentityTransportV3(session, playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux})

		parsed, err := url.Parse(transport.url)
		if err != nil {
			transport.rollback()
			t.Fatal(err)
		}
		if parsed.Query().Get("st") == "" || parsed.Query().Get("seek") != strconv.FormatFloat(origin, 'f', -1, 64) {
			transport.rollback()
			t.Fatalf("progressive reanchor URL %d = %q", index, transport.url)
		}
		if plan.Timeline.PlayerStartSeconds != 0 || plan.Timeline.StreamOriginSeconds != origin ||
			plan.Timeline.TimelineOffsetSeconds != origin || plan.Timeline.CanSeekAnywhere ||
			plan.Timeline.SeekWindowStartSeconds == nil || *plan.Timeline.SeekWindowStartSeconds != origin ||
			plan.Timeline.SeekWindowEndSeconds != nil || plan.Timeline.SeekRestoration != "source_position" {
			transport.rollback()
			t.Fatalf("progressive reanchor timeline %d = %#v", index, plan.Timeline)
		}
		transport.rollback()
	}
}

func TestPrepareTransportV3RejectsNodeMissingRequiredTransformation(t *testing.T) {
	startHits := 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: []playback.TransformationV3{{Name: "video_to_h264", Executor: "server", RecipeVersion: "2"}}})
		case "/transcode/start":
			startHits++
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer remote.Close()

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.NodePlanner = staticNodePlannerV3{plan: nodepool.Plan{TranscodeNode: &nodepool.Node{URL: remote.URL}}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"playback.local_transcode_fallback": "false"}}
	plan := &playback.PlanV3{
		PlanID:   "plan:remote-capability",
		Delivery: playback.DeliveryTranscodeHLSV3,
		Transformations: []playback.TransformationV3{
			{Name: "video_to_h264", Executor: "server", RecipeVersion: "1"},
			{Name: "audio_to_aac", Executor: "server", RecipeVersion: "1"},
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	_, transportErr := handler.prepareTransportV3(request, &playback.Session{ID: "session-capability"}, v3HandlerFixtureFile(t), playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayTranscode, TargetVideoCodec: "h264", TargetAudioCodec: "aac"})
	if transportErr == nil || transportErr.reason != "transcode_node_capability_unavailable" {
		t.Fatalf("transport error = %#v", transportErr)
	}
	if startHits != 0 {
		t.Fatalf("incompatible node received %d start requests", startHits)
	}
}

func TestPrepareTransportV3RequiresRemoteManifestReadiness(t *testing.T) {
	var startRequest transcodenode.TranscodeStartRequest
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: []playback.TransformationV3{
				{Name: "video_to_h264", Executor: "server", RecipeVersion: "1"},
				{Name: "audio_to_aac", Executor: "server", RecipeVersion: "1"},
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			if err := json.NewDecoder(r.Body).Decode(&startRequest); err != nil {
				t.Errorf("decode remote start: %v", err)
			}
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: startRequest.SessionID, Status: "started"})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer remote.Close()

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = staticNodePlannerV3{plan: nodepool.Plan{TranscodeNode: &nodepool.Node{URL: remote.URL}}}
	plan := &playback.PlanV3{
		PlanID:   "plan:remote-ready",
		Delivery: playback.DeliveryTranscodeHLSV3,
		Transformations: []playback.TransformationV3{
			{Name: "video_to_h264", Executor: "server", RecipeVersion: "1"},
			{Name: "audio_to_aac", Executor: "server", RecipeVersion: "1"},
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	transport, transportErr := handler.prepareTransportV3(request, &playback.Session{ID: "session-ready", UserID: 7, ProfileID: "profile-1"}, v3HandlerFixtureFile(t), playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayTranscode, TargetVideoCodec: "h264", TargetAudioCodec: "aac"})
	if transportErr != nil {
		t.Fatalf("prepare remote transport: %v", transportErr)
	}
	defer transport.rollback()
	if !startRequest.RequireReady {
		t.Fatal("protocol-v3 remote start did not require manifest readiness")
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
	if copySeek != 17.3 || copySegment != 8 || copyPlan.Timeline.StreamOriginSeconds != 17.3 || copyPlan.Timeline.TimelineOffsetSeconds != 17.3 || copyPlan.Timeline.PlayerStartSeconds != 0 || copyPlan.Timeline.CanSeekAnywhere ||
		copyPlan.Timeline.SeekWindowStartSeconds == nil || *copyPlan.Timeline.SeekWindowStartSeconds != 17.3 ||
		copyPlan.Timeline.SeekWindowEndSeconds == nil || *copyPlan.Timeline.SeekWindowEndSeconds != 600 ||
		copyPlan.Timeline.SeekRestoration != "source_position" {
		t.Fatalf("copy timeline=%#v seek=%v segment=%d", copyPlan.Timeline, copySeek, copySegment)
	}

	encodePlan := &playback.PlanV3{Timeline: playback.TimelineV3{SourceStartSeconds: 17.3}}
	encodeSeek, encodeSegment := configureHLSTimelineV3(encodePlan, "h264", 2, 600)
	if encodeSeek != 16 || encodeSegment != 8 || encodePlan.Timeline.StreamOriginSeconds != 0 || encodePlan.Timeline.TimelineOffsetSeconds != 0 || encodePlan.Timeline.PlayerStartSeconds != 17.3 || !encodePlan.Timeline.CanSeekAnywhere ||
		encodePlan.Timeline.SeekWindowStartSeconds != nil || encodePlan.Timeline.SeekWindowEndSeconds != nil ||
		encodePlan.Timeline.SeekRestoration != "player_position" {
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
	plan := &playback.PlanV3{Source: playback.SourceDescriptorV3{DVProfile: 8}, Transformations: []playback.TransformationV3{{Name: "server_dv7_to_hdr10"}}}
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

func TestSanitizeDiagnosticsV3PreservesPlayerFailureEvidence(t *testing.T) {
	got := sanitizeDiagnosticsV3(map[string]string{
		"error_code":                     "2004",
		"error_code_name":                "ERROR_CODE_PARSING_CONTAINER_MALFORMED",
		"error_cause":                    "ParserException",
		"network_transport":              "wifi",
		"network_metered":                "true",
		"network_validated":              "true",
		"bandwidth_estimate_kbps":        "3500",
		"link_downstream_kbps":           "5000",
		"target_source_position_seconds": "321.5",
		"reason":                         "seek_reanchor",
		"message":                        "must not be persisted",
	})
	if got["error_code"] != "2004" || got["error_code_name"] == "" || got["error_cause"] != "ParserException" {
		t.Fatalf("failure diagnostics = %#v", got)
	}
	if _, ok := got["message"]; ok {
		t.Fatalf("unapproved message persisted: %#v", got)
	}
	for _, key := range []string{"network_transport", "network_metered", "network_validated", "bandwidth_estimate_kbps", "link_downstream_kbps", "target_source_position_seconds", "reason"} {
		if got[key] == "" {
			t.Errorf("client diagnostic %q was stripped: %#v", key, got)
		}
	}
}

func TestRouteEventV3AcceptsAndroidSeekEvents(t *testing.T) {
	base := playback.RouteEventV3{
		ProtocolVersion:       playback.ProtocolV3,
		PlaybackAttemptID:     "attempt-route-0001",
		OutputRouteGeneration: 1,
	}
	for _, event := range []string{playback.RouteEventSeekReanchorRequestedV3, playback.RouteEventSeekReanchoredV3} {
		candidate := base
		candidate.Event = event
		if !validRouteEventV3(candidate) {
			t.Errorf("Android route event %q was rejected", event)
		}
	}
}

func TestRemapSubtitleSelectionV3RejectsNegativeIndex(t *testing.T) {
	index := -1
	request := playback.StartRequestV3{SubtitleTrackIndex: &index}
	source := &models.MediaFile{ID: 1, ExternalSubtitles: []models.ExternalSubtitle{{Language: "eng", Format: "srt"}}}
	target := &models.MediaFile{ID: 2, ExternalSubtitles: []models.ExternalSubtitle{{Language: "eng", Format: "srt"}}}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	if err := handler.remapSubtitleSelectionV3(context.Background(), source, target, &request); err == nil {
		t.Fatal("negative subtitle index was accepted")
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
