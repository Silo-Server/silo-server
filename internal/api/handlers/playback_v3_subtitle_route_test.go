package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestSubtitleArtifactDescribesServedBytesOnResumedTransport(t *testing.T) {
	for _, tc := range []struct{ codec, format, mime string }{
		{"subrip", "vtt", "text/vtt"},
		{"srt", "vtt", "text/vtt"},
		{"mov_text", "vtt", "text/vtt"},
		{"ass", "ass", "text/x-ssa"},
		{"ssa", "ass", "text/x-ssa"},
		{"hdmv_pgs_subtitle", "sup", "application/octet-stream"},
		{"pgssub", "sup", "application/octet-stream"},
	} {
		t.Run(tc.codec, func(t *testing.T) {
			file := &models.MediaFile{ID: 42, SubtitleTracks: []models.SubtitleTrack{{Index: 4, Codec: tc.codec}}}
			plan := &playback.PlanV3{
				Delivery: playback.DeliveryRemuxProgressiveV3,
				Timeline: playback.TimelineV3{StreamOriginSeconds: 600, TimelineOffsetSeconds: 600},
				Subtitle: playback.SubtitleDecisionV3{
					Mode: playback.SubtitleRenderV3, TrackID: playback.TrackIDV3(file.ID, "subtitle", 0),
					Inventory: playback.BuildSubtitleInventoryV3(file, nil),
				},
			}
			handler := &PlaybackHandler{}
			if err := handler.attachSubtitleArtifactV3(t.Context(), "session", file, plan, 0, nil, nil); err != nil {
				t.Fatal(err)
			}
			artifact := plan.Subtitle.Artifact
			if artifact == nil || artifact.Format != tc.format || artifact.MIMEType != tc.mime || artifact.TimingOriginSeconds != 0 {
				t.Fatalf("artifact must describe served format and absolute source timestamps: %#v", artifact)
			}
		})
	}
}

func TestAttachNativeSubtitleValidatesRouteAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		name        string
		delivery    playback.DeliveryV3
		mode        playback.SubtitleModeV3
		streamIndex int
		wantError   bool
	}{
		{"native_original", playback.DeliveryOriginalHTTPV3, playback.SubtitleRenderV3, 4, false},
		{"reject_adapted_native", playback.DeliveryRemuxProgressiveV3, playback.SubtitleRenderV3, 4, true},
		{"reject_converted_native", playback.DeliveryOriginalHTTPV3, playback.SubtitleConvertV3, 4, true},
		{"reject_changed_index", playback.DeliveryOriginalHTTPV3, playback.SubtitleRenderV3, 7, true},
		{"clear_off", playback.DeliveryOriginalHTTPV3, playback.SubtitleOffV3, 4, false},
		{"clear_burned_in", playback.DeliveryTranscodeHLSV3, playback.SubtitleBurnInV3, 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := &models.MediaFile{ID: 42, SubtitleTracks: []models.SubtitleTrack{{Index: 4, Codec: "subrip"}}}
			plan := &playback.PlanV3{
				Delivery: tc.delivery,
				Subtitle: playback.SubtitleDecisionV3{
					Mode: tc.mode, TrackID: playback.TrackIDV3(file.ID, "subtitle", 0),
					Embedded:  &playback.EmbeddedSubtitleV3{StreamIndex: tc.streamIndex},
					Artifact:  &playback.SubtitleArtifactV3{URL: "/stale.vtt", Format: "vtt"},
					Inventory: playback.BuildSubtitleInventoryV3(file, nil),
				},
			}
			handler := &PlaybackHandler{}
			err := handler.attachSubtitleArtifactV3(t.Context(), "session", file, plan, 0, nil, nil)
			if (err != nil) != tc.wantError {
				t.Fatalf("attach error=%v, wantError=%v", err, tc.wantError)
			}
			if err == nil && plan.Subtitle.Artifact != nil {
				t.Fatalf("native/off/burned-in route retained stale artifact: %#v", plan.Subtitle)
			}
			if err == nil && tc.mode != playback.SubtitleRenderV3 && plan.Subtitle.Embedded != nil {
				t.Fatalf("off/burned-in route retained native track selection: %#v", plan.Subtitle)
			}
		})
	}
}

func TestSubtitleArtifactOffersOriginalSubRipOnlyToOptedInClients(t *testing.T) {
	file := &models.MediaFile{ID: 42, ExternalSubtitles: []models.ExternalSubtitle{{Path: "/media/movie.ar.srt", Format: "srt"}}}
	for _, tc := range []struct {
		name     string
		mode     playback.SubtitleModeV3
		features []string
		format   string
		mime     string
		ext      string
	}{
		{"opted in", playback.SubtitleRenderV3, []string{playback.FeatureSubripSidecarV3}, "srt", "application/x-subrip", "/subtitles/0.srt?file_id=42&original=1"},
		{"not opted in", playback.SubtitleRenderV3, nil, "vtt", "text/vtt", "/subtitles/0.vtt?"},
		// A server conversion is WebVTT by definition.
		{"conversion", playback.SubtitleConvertV3, []string{playback.FeatureSubripSidecarV3}, "vtt", "text/vtt", "/subtitles/0.vtt?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := &playback.PlanV3{
				Delivery: playback.DeliveryRemuxProgressiveV3,
				Subtitle: playback.SubtitleDecisionV3{
					Mode: tc.mode, TrackID: playback.TrackIDV3(file.ID, "subtitle", 0),
					Inventory: playback.BuildSubtitleInventoryV3(file, nil),
				},
			}
			handler := &PlaybackHandler{}
			if err := handler.attachSubtitleArtifactV3(t.Context(), "session", file, plan, 0, nil, tc.features); err != nil {
				t.Fatal(err)
			}
			artifact := plan.Subtitle.Artifact
			if artifact == nil || artifact.Format != tc.format || artifact.MIMEType != tc.mime || !strings.Contains(artifact.URL, tc.ext) {
				t.Fatalf("artifact = %#v, want format %s, mime %s, url containing %s", artifact, tc.format, tc.mime, tc.ext)
			}
			// original=1 only means something on a .srt URL.
			if tc.format != "srt" && strings.Contains(artifact.URL, playback.SubtitleOriginalParamV3+"=") {
				t.Fatalf("a %s artifact must not carry original=1: %s", tc.format, artifact.URL)
			}
		})
	}
}

// A seek reanchor replays the frozen plan, so its SRT artifact keeps the
// representation the plan published. An attempt started by a server that did
// not know subrip_sidecar_v1 persisted WebVTT URLs beside a feature list that
// already names the feature; reanchoring it must not change the route.
func TestSeekReanchorKeepsTheFrozenSRTRepresentation(t *testing.T) {
	for _, tc := range []struct {
		name          string
		startFeature  bool
		recordFeature bool
		format        string
	}{
		{"negotiated at start", true, true, "srt"},
		{"started without the feature", false, true, "vtt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := v3HandlerFixtureFile(t)
			file.ExternalSubtitles = []models.ExternalSubtitle{{Path: "/media/movie.ar.srt", Language: "ar", Format: "srt"}}
			manager := playback.NewSessionManager(0, 0)
			handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
			handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "false"}}
			handler.ItemAccess = allowAllPlaybackItemAccess{}
			startRequest := v3HandlerStartRequest()
			if tc.startFeature {
				startRequest.ClientFeatures = append(startRequest.ClientFeatures, playback.FeatureSubripSidecarV3)
			}
			subtitleIndex := 0
			startRequest.SubtitleTrackID = playback.TrackIDV3(file.ID, "subtitle", subtitleIndex)
			startRequest.SubtitleTrackIndex = &subtitleIndex
			startRR := httptest.NewRecorder()
			handler.HandleStartPlayback(startRR, httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(WithNativeAPIV2(newAuthorizedPlaybackContext())))
			if startRR.Code != http.StatusCreated {
				t.Fatalf("start status = %d, body = %s", startRR.Code, startRR.Body.String())
			}
			var started playback.DecisionResponseV3
			if err := json.Unmarshal(startRR.Body.Bytes(), &started); err != nil {
				t.Fatal(err)
			}
			if started.PlaybackPlan == nil || started.PlaybackPlan.Subtitle.Artifact == nil || started.PlaybackPlan.Subtitle.Artifact.Format != tc.format {
				t.Fatalf("start artifact = %#v, want format %s", started.PlaybackPlan, tc.format)
			}
			if tc.recordFeature && !tc.startFeature {
				record, err := handler.PlanStoreV3.GetAttempt(t.Context(), started.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				record.NormalizedRequest.ClientFeatures = append(record.NormalizedRequest.ClientFeatures, playback.FeatureSubripSidecarV3)
				// The store keeps an attempt immutable, so persist the edited
				// record the way the older server would have written it.
				handler.PlanStoreV3 = playback.NewMemoryPlanStoreV3()
				if err := handler.PlanStoreV3.SaveAttempt(t.Context(), *record); err != nil {
					t.Fatal(err)
				}
			}
			if err := manager.UpdateProgress(started.SessionID, 12, true); err != nil {
				t.Fatal(err)
			}
			currentKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil)
			body, err := json.Marshal(playback.ReplanRequestV3{
				ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationSeekReanchorV3,
				PlaybackAttemptID: startRequest.PlaybackAttemptID,
				ReplanRequestID:   "seek-reanchor-srt", FailedPlanID: started.PlaybackPlan.PlanID,
				PlanAttemptID: "plan-attempt-seek-srt", PlanAttemptKey: currentKey, AttemptCount: 1,
				QualityPreference: "original", PositionSeconds: 321,
				SelectedTracks: started.PlaybackPlan.SelectedTracks,
				Capabilities:   startRequest.Capabilities, ClientPlaybackContext: startRequest.ClientPlaybackContext,
			})
			if err != nil {
				t.Fatal(err)
			}
			req := withPlaybackRouteParam(httptest.NewRequest(http.MethodPost, "/api/v2/playback/"+started.SessionID+"/replan", strings.NewReader(string(body))).WithContext(WithNativeAPIV2(newAuthorizedPlaybackContext())), "session_id", started.SessionID)
			rr := httptest.NewRecorder()
			handler.HandleReplanPlaybackV3(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("reanchor status = %d, body = %s", rr.Code, rr.Body.String())
			}
			var reanchored playback.DecisionResponseV3
			if err := json.Unmarshal(rr.Body.Bytes(), &reanchored); err != nil {
				t.Fatal(err)
			}
			if reanchored.PlaybackPlan == nil {
				t.Fatalf("reanchor returned no plan: %s", rr.Body.String())
			}
			artifact := reanchored.PlaybackPlan.Subtitle.Artifact
			if artifact == nil || artifact.Format != tc.format || !strings.Contains(artifact.URL, "/subtitles/0."+tc.format+"?") {
				t.Fatalf("reanchored artifact = %#v, want the frozen %s representation", artifact, tc.format)
			}
		})
	}
}

// The frozen /api/v1 start negotiates only its original features, so a v1
// client that sends subrip_sidecar_v1 keeps WebVTT URLs.
func TestV1StartDoesNotNegotiateSubripSidecar(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.ExternalSubtitles = []models.ExternalSubtitle{{Path: "/media/movie.ar.srt", Language: "ar", Format: "srt"}}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "false"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	startRequest.ClientFeatures = append(startRequest.ClientFeatures, playback.FeatureSubripSidecarV3)
	subtitleIndex := 0
	startRequest.SubtitleTrackID = playback.TrackIDV3(file.ID, "subtitle", subtitleIndex)
	startRequest.SubtitleTrackIndex = &subtitleIndex
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext()))
	if rr.Code != http.StatusCreated {
		t.Fatalf("start status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var started playback.DecisionResponseV3
	if err := json.Unmarshal(rr.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.PlaybackPlan == nil || started.PlaybackPlan.Subtitle.Artifact == nil || started.PlaybackPlan.Subtitle.Artifact.Format != "vtt" ||
		playback.HasFeatureV3(started.ServerFeatures, playback.FeatureSubripSidecarV3) {
		t.Fatalf("a v1 start must keep WebVTT and not advertise subrip_sidecar_v1: %#v", started)
	}

	// The same body retried through /api/v2 must not replay that WebVTT plan
	// to a client whose request promises it original SRT.
	retry := httptest.NewRecorder()
	handler.HandleStartPlayback(retry, httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(WithNativeAPIV2(newAuthorizedPlaybackContext())))
	if retry.Code != http.StatusConflict || !strings.Contains(retry.Body.String(), "playback_attempt_reused") {
		t.Fatalf("v2 retry of a v1 attempt status = %d, body = %s", retry.Code, retry.Body.String())
	}
}

// An attempt that negotiated subrip_sidecar_v1 on /api/v2 cannot continue on
// the frozen /api/v1 surface, which would publish its .srt?original=1 URLs on a
// route that serves WebVTT for them.
func TestV1RefusesAnAttemptNegotiatedWithSubripSidecar(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.ExternalSubtitles = []models.ExternalSubtitle{{Path: "/media/movie.ar.srt", Language: "ar", Format: "srt"}}
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "false"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	startRequest.ClientFeatures = append(startRequest.ClientFeatures, playback.FeatureSubripSidecarV3)
	subtitleIndex := 0
	startRequest.SubtitleTrackID = playback.TrackIDV3(file.ID, "subtitle", subtitleIndex)
	startRequest.SubtitleTrackIndex = &subtitleIndex
	startBody := marshalV3StartRequest(t, startRequest)
	start := func(ctx context.Context) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", strings.NewReader(startBody)).WithContext(ctx))
		return rr
	}
	startRR := start(WithNativeAPIV2(newAuthorizedPlaybackContext()))
	if startRR.Code != http.StatusCreated {
		t.Fatalf("v2 start status = %d, body = %s", startRR.Code, startRR.Body.String())
	}
	var started playback.DecisionResponseV3
	if err := json.Unmarshal(startRR.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.PlaybackPlan == nil || started.PlaybackPlan.Subtitle.Artifact == nil || started.PlaybackPlan.Subtitle.Artifact.Format != "srt" {
		t.Fatalf("v2 start must negotiate original SRT: %#v", started.PlaybackPlan)
	}

	// An identical start retried through /api/v1 must not replay the v2 plan.
	if rr := start(newAuthorizedPlaybackContext()); rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "playback_attempt_reused") {
		t.Fatalf("v1 start replay status = %d, body = %s", rr.Code, rr.Body.String())
	}

	body, err := json.Marshal(playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationSeekReanchorV3,
		PlaybackAttemptID: startRequest.PlaybackAttemptID,
		ReplanRequestID:   "seek-reanchor-v1", FailedPlanID: started.PlaybackPlan.PlanID,
		PlanAttemptID: "plan-attempt-seek-v1", AttemptCount: 1,
		PlanAttemptKey:    playback.PlanAttemptKeyV3(*started.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil),
		QualityPreference: "original", PositionSeconds: 60,
		SelectedTracks: started.PlaybackPlan.SelectedTracks,
		Capabilities:   startRequest.Capabilities, ClientPlaybackContext: startRequest.ClientPlaybackContext,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.UpdateProgress(started.SessionID, 12, true); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(rr, withPlaybackRouteParam(httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext()), "session_id", started.SessionID))
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "playback_attempt_reused") {
		t.Fatalf("v1 replan status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

// A server that predates subrip_sidecar_v1 stored the token verbatim while
// publishing WebVTT. The published representation, not the token, decides both
// the cross-surface guard and the representation a replan keeps.
func TestLegacyAttemptWithStoredSubripTokenKeepsWebVTT(t *testing.T) {
	file := &models.MediaFile{ID: 42, ExternalSubtitles: []models.ExternalSubtitle{{Path: "/media/movie.ar.srt", Format: "srt"}}}
	inventory := func(features []string) []playback.SubtitleInventoryItemV3 {
		return playback.ScopeSubtitleInventoryV3("session", file, playback.BuildSubtitleInventoryV3(file, nil), features)
	}
	record := func(stored []string, published []playback.SubtitleInventoryItemV3) *playback.AttemptRecordV3 {
		r := &playback.AttemptRecordV3{}
		r.NormalizedRequest.ClientFeatures = stored
		r.CurrentPlan.Subtitle.Inventory = published
		return r
	}
	withToken := []string{playback.FeatureSubripSidecarV3}
	legacy := record(withToken, inventory(nil))
	native := record(withToken, inventory(withToken))
	v1, v2 := t.Context(), WithNativeAPIV2(t.Context())

	if err := requireAttemptAPISurfaceV3(v1, legacy, nil); err != nil {
		t.Fatalf("a legacy v1 attempt that published WebVTT must continue on v1: %v", err)
	}
	if err := requireAttemptAPISurfaceV3(v1, native, nil); err == nil {
		t.Fatal("an attempt that published original SRT must not continue on v1")
	}
	if err := requireAttemptAPISurfaceV3(v2, legacy, withToken); err == nil {
		t.Fatal("a v2 start retry with the feature must not replay a WebVTT plan")
	}
	if err := requireAttemptAPISurfaceV3(v2, record(nil, nil), withToken); err != nil {
		t.Fatalf("an attempt that published no SRT has no representation to protect: %v", err)
	}
	if got := replanSubtitleFeaturesV3(legacy, withToken); playback.HasFeatureV3(got, playback.FeatureSubripSidecarV3) {
		t.Fatalf("a replan of a legacy attempt must keep WebVTT, got features %v", got)
	}
	if got := replanSubtitleFeaturesV3(native, withToken); !playback.HasFeatureV3(got, playback.FeatureSubripSidecarV3) {
		t.Fatalf("a replan of an original-SRT attempt must keep original SRT, got features %v", got)
	}
}
