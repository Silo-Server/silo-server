package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/directconn"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

func TestReplanPlaybackV2SidecarChangeLeavesProxyForDirectOnlyDevice(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.Container = "mkv"
	file.FilePath = writePlaybackTestMediaFile(t, "movie.mkv")
	file.ExternalSubtitles = []models.ExternalSubtitle{
		{Path: writePlaybackTestMediaFile(t, "movie.de.srt"), Language: "de", Format: "srt"},
		{Path: writePlaybackTestMediaFile(t, "movie.en.srt"), Language: "en", Format: "srt"},
	}

	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{})
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			var req transcodenode.TranscodeStartRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode worker start: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{
				SessionID:             req.SessionID,
				Status:                "started",
				CopyFMP4RecipeVersion: req.CopyFMP4RecipeVersion,
				CopyVideoMPEGTS:       req.CopyVideoMPEGTS,
				CopyAudioCodec:        req.CopyAudioCodec,
				KeyframePlaylist:      req.KeyframePlaylist,
				KeyframePlanResume:    req.KeyframePlanResume,
				ThrottleSeconds:       req.ThrottleSeconds,
			})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(worker.Close)

	state := &directStateV3{enrolled: true, device: directconn.DeviceDirect{Policy: "automatic"}}
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.InstallationID = serviceInstallation
	handler.JWTSecret = "sidecar-direct-only-test"
	handler.DirectState = state
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3(nil))
	stubCopySeekAnchorV3(handler)
	proxy := &nodepool.Node{ID: 42, URL: "http://proxy-1.example"}
	handler.NodePlanner = &progressiveFallbackPlannerV3{
		transcode: &nodepool.Node{ID: 43, URL: worker.URL},
		proxy:     proxy,
	}
	handler.v3RouteNodeProbe = func(context.Context, string) bool { return true }

	start := v3HandlerStartRequest()
	start.Capabilities.Containers = []string{"m3u8"}
	start.ClientPlaybackContext.Deliveries = map[string]playback.DeliveryCapabilityV3{
		playback.DeliveryClassHLSV3: {
			Enabled: true, SupportedOnDevice: true,
			Subtitles: playback.DeliverySubtitleCapabilitiesV3{SidecarText: true},
		},
	}
	german := 0
	start.SubtitleTrackID = playback.TrackIDV3(file.ID, "subtitle", german)
	start.SubtitleTrackIndex = &german
	caller := PlaybackCaller{UserID: 1, ProfileID: "profile-1", InstallationID: serviceInstallation, ConnectionDeviceID: "tv-1"}
	started, err := handler.StartPlaybackV2(newAuthorizedPlaybackContext(), caller, start)
	if err != nil || started.PlaybackPlan == nil {
		t.Fatalf("fixture: start err = %v, terminal = %#v", err, started.Terminal)
	}
	before := started.PlaybackPlan
	if before.Delivery != playback.DeliveryRemuxHLSV3 || before.Subtitle.Mode != playback.SubtitleRenderV3 ||
		urlOriginV3(before.Stream.URL) != proxy.URL {
		t.Fatalf("fixture: delivery %q, subtitle %q, origin %q, want copy HLS with a sidecar on %q",
			before.Delivery, before.Subtitle.Mode, urlOriginV3(before.Stream.URL), proxy.URL)
	}

	state.mu.Lock()
	state.device.Policy = directPolicyDirectOnlyV3
	state.mu.Unlock()

	english := 1
	request := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationTrackChangeV3,
		PlaybackAttemptID:     start.PlaybackAttemptID,
		ReplanRequestID:       "sidecar-direct-only-replan",
		FailedPlanID:          before.PlanID,
		PlanAttemptID:         "sidecar-direct-only-attempt",
		PlanAttemptKey:        before.PlanAttemptKey,
		AttemptCount:          1,
		PositionSeconds:       120,
		SelectedTracks:        playback.SelectedTracksV3{Audio: before.SelectedTracks.Audio, Subtitle: &playback.TrackIdentityV3{ID: playback.TrackIDV3(file.ID, "subtitle", english), Index: &english}},
		Capabilities:          start.Capabilities,
		ClientPlaybackContext: start.ClientPlaybackContext,
	}
	replanned, err := handler.ReplanPlaybackV2(newAuthorizedPlaybackContext(), caller, started.SessionID, PlaybackReplanCommand{Request: request})
	if err != nil || replanned.PlaybackPlan == nil {
		t.Fatalf("sidecar change: err = %v, terminal = %#v", err, replanned.Terminal)
	}
	if raw := replanned.PlaybackPlan.Stream.URL; !strings.HasPrefix(raw, "/") {
		t.Fatalf("sidecar change on direct_only republished origin %q, want the API host", urlOriginV3(raw))
	}
}
