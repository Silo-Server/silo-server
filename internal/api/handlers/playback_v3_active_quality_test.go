package handlers

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// active_quality and its feature are /api/v2 only: the frozen /api/v1 start
// writes the same plan without them.
func TestStartPlaybackActiveQualityIsPublishedOnV2Only(t *testing.T) {
	newHandler := func(t *testing.T) *PlaybackHandler {
		file := v3HandlerFixtureFile(t)
		handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
		handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
		presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
			{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
			{Name: playback.TransformationVideoToH264V3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3, Available: true},
		}))
		handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
		handler.ItemAccess = allowAllPlaybackItemAccess{}
		return handler
	}
	request := v3HandlerStartRequest()
	request.QualityPreference = "720p"
	request.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true}

	v2, err := newHandler(t).startPlaybackV2ForTest(t, request)
	if err != nil {
		t.Fatal(err)
	}
	if v2.PlaybackPlan == nil || v2.PlaybackPlan.ActiveQuality != playback.QualityRung720pMediumV3 {
		t.Fatalf("v2 plan = %#v, terminal = %#v", v2.PlaybackPlan, v2.Terminal)
	}
	if !slices.Contains(v2.ServerFeatures, playback.FeaturePlanActiveQualityV3) {
		t.Fatalf("v2 features = %v", v2.ServerFeatures)
	}

	request.PlaybackAttemptID = "attempt-handler-0002"
	rr := httptest.NewRecorder()
	newHandler(t).HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, request))).WithContext(newAuthorizedPlaybackContext()))
	if rr.Code != http.StatusCreated || !strings.Contains(rr.Body.String(), `"server_transcode_hls"`) {
		t.Fatalf("v1 status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); strings.Contains(body, "active_quality") || strings.Contains(body, playback.FeaturePlanActiveQualityV3) {
		t.Fatalf("v1 body published v2-only fields: %s", body)
	}
}

func (h *PlaybackHandler) startPlaybackV2ForTest(t *testing.T, request playback.StartRequestV3) (playback.DecisionResponseV3, error) {
	t.Helper()
	h.InstallationID = serviceInstallation
	return h.StartPlaybackV2(newAuthorizedPlaybackContext(), PlaybackCaller{UserID: 1, ProfileID: "profile-1", InstallationID: serviceInstallation}, request)
}
