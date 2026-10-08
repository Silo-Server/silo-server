package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// subtitleBlockedHDRFixtureV3 is a 4K HDR10 HEVC file with an English PGS
// subtitle and a 1080p SDR H.264 sibling without one. The client direct
// plays the 4K file but cannot render PGS, and the server cannot re-encode
// HDR, so burning the subtitle in is the only thing the 4K file cannot do.
func subtitleBlockedHDRFixtureV3(t *testing.T) (*PlaybackHandler, *models.MediaFile, *models.MediaFile, playback.StartRequestV3) {
	t.Helper()
	source := v3HandlerFixtureFile(t)
	source.Container = "mkv"
	source.FilePath = writePlaybackTestMediaFile(t, "movie-4k.mkv")
	source.CodecVideo = "hevc"
	source.Resolution = "2160p"
	source.Bitrate = 64_000
	source.VideoTracks = []models.VideoTrack{{
		Codec: "hevc", Profile: "main 10", Level: 153, Width: 3840, Height: 2160,
		FrameRate: "24000/1001", Bitrate: 64_000, BitDepth: 10,
		VideoRange: "HDR10", VideoRangeType: "HDR10",
	}}
	source.SubtitleTracks = []models.SubtitleTrack{{Index: 4, Codec: "hdmv_pgs_subtitle", Language: "eng", Title: "English"}}

	alternateValue := *source
	alternate := &alternateValue
	alternate.ID = 84
	alternate.FilePath = writePlaybackTestMediaFile(t, "movie-1080p.mkv")
	alternate.CodecVideo = "h264"
	alternate.Resolution = "1080p"
	alternate.Bitrate = 8_000
	alternate.VideoTracks = []models.VideoTrack{{
		Codec: "h264", Profile: "high", Level: 41, Width: 1920, Height: 1080,
		FrameRate: "24000/1001", Bitrate: 8_000, BitDepth: 8,
		VideoRange: "SDR", VideoRangeType: "SDR",
	}}
	alternate.SubtitleTracks = nil

	files := map[int]*models.MediaFile{source.ID: source, alternate.ID: alternate}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), mapPlaybackFileResolver{files: files})
	handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{
		source.ContentID: {source, alternate},
	}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"transcode_enabled": "true", "allow_4k_transcode": "true"}}
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: playback.TransformationVideoToH264V3, RecipeVersion: "2", Available: true},
	}))
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	startRequest := v3HandlerStartRequest()
	startRequest.QualityPreference = "auto"
	startRequest.Capabilities.CodecsVideo = []string{"hevc", "h264"}
	startRequest.Capabilities.CodecsVideoHardware = []string{"hevc", "h264"}
	startRequest.Capabilities.Containers = []string{"mkv", "hls"}
	startRequest.Capabilities.MaxResolution = "2160p"
	startRequest.Capabilities.VideoDecode = []playback.VideoDecodeCapabilityV3{{
		Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10},
		MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true,
	}}
	hdr := &playback.HDRCapabilitiesV3{HDR10: true}
	startRequest.Capabilities.HDRDetails = hdr
	startRequest.ClientPlaybackContext.Output.HDRDetails = hdr
	startRequest.ClientPlaybackContext.Deliveries = map[string]playback.DeliveryCapabilityV3{
		playback.DeliveryClassOriginalHTTPV3: {
			Enabled: true, SupportedOnDevice: true, Containers: []string{"mkv"},
			VideoCodecs: []string{"hevc", "h264"}, AudioDecodeCodecs: []string{"aac"},
		},
		playback.DeliveryClassHLSV3: {
			Enabled: true, SupportedOnDevice: true, Containers: []string{"hls"},
			VideoCodecs: []string{"h264"}, AudioDecodeCodecs: []string{"aac"},
		},
	}
	return handler, source, alternate, startRequest
}

// requireRequestedVersionWithoutSubtitleV3 fails unless plan stays on the
// requested 4K file, plays it without converting the video, and reports the
// subtitle it could not show exactly once.
func requireRequestedVersionWithoutSubtitleV3(t *testing.T, plan *playback.PlanV3, source *models.MediaFile) {
	t.Helper()
	if plan.EffectiveMediaFileID != source.ID {
		t.Fatalf("effective file = %d, want the requested 4K file %d: only its subtitle was blocked", plan.EffectiveMediaFileID, source.ID)
	}
	if plan.Subtitle.Mode != playback.SubtitleOffV3 || plan.SelectedTracks.Subtitle != nil {
		t.Fatalf("subtitle = %#v selected = %#v, want subtitles off", plan.Subtitle, plan.SelectedTracks.Subtitle)
	}
	if plan.EffectiveRecipe.VideoCodec != "hevc" || !plan.Claims.Video.HDR10 {
		t.Fatalf("recipe = %#v claims = %#v, want the 4K HDR10 HEVC picture", plan.EffectiveRecipe, plan.Claims.Video)
	}
	warned := 0
	for _, warning := range plan.DegradationWarnings {
		if warning.Code == playback.DegradationWarningSubtitleTrackUnavailableV3 {
			warned++
		}
	}
	if warned != 1 {
		t.Fatalf("subtitle_track_unavailable reported %d times: %#v", warned, plan.DegradationWarnings)
	}
}

// A subtitle that only the requested file cannot show is no reason to switch
// to a lower version that has no subtitle either: the viewer would lose the
// 4K picture and still get no subtitle. Playback starts on the requested
// file with the subtitle off and says so.
func TestHandleStartPlaybackV3SubtitleOnlyRefusalKeepsTheRequestedVersion(t *testing.T) {
	handler, source, _, startRequest := subtitleBlockedHDRFixtureV3(t)
	subtitleIndex := 0
	startRequest.SubtitleTrackID = playback.TrackIDV3(source.ID, "subtitle", subtitleIndex)
	startRequest.SubtitleTrackIndex = &subtitleIndex

	rec := httptest.NewRecorder()
	handler.HandleStartPlayback(rec, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext()))
	var started playback.DecisionResponseV3
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil {
		t.Fatalf("start status=%d body=%s", rec.Code, rec.Body.String())
	}
	t.Cleanup(func() { handler.tm.CloseTranscodeSession(started.SessionID, "") })
	requireRequestedVersionWithoutSubtitleV3(t, started.PlaybackPlan, source)
}

// The same holds when the viewer turns the subtitle on during playback.
func TestHandleReplanPlaybackV3SubtitleOnlyRefusalKeepsTheRequestedVersion(t *testing.T) {
	handler, source, _, startRequest := subtitleBlockedHDRFixtureV3(t)
	rec := httptest.NewRecorder()
	handler.HandleStartPlayback(rec, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext()))
	var started playback.DecisionResponseV3
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil {
		t.Fatalf("start status=%d body=%s", rec.Code, rec.Body.String())
	}
	t.Cleanup(func() { handler.tm.CloseTranscodeSession(started.SessionID, "") })
	if started.PlaybackPlan.EffectiveMediaFileID != source.ID {
		t.Fatalf("start effective file = %d, want the 4K file %d", started.PlaybackPlan.EffectiveMediaFileID, source.ID)
	}

	subtitleIndex := 0
	replanned := postPlaybackReplanV3(t, handler, started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationTrackChangeV3,
		PlaybackAttemptID: startRequest.PlaybackAttemptID, ReplanRequestID: "subtitle-only-refusal-0001",
		FailedPlanID: started.PlaybackPlan.PlanID, PlanAttemptID: "subtitle-only-refusal-attempt-0001",
		PlanAttemptKey: started.PlaybackPlan.PlanAttemptKey, AttemptCount: 1, PositionSeconds: 30,
		QualityPreference: "auto",
		SelectedTracks: playback.SelectedTracksV3{
			Audio: started.PlaybackPlan.SelectedTracks.Audio,
			Subtitle: &playback.TrackIdentityV3{
				ID: playback.TrackIDV3(source.ID, "subtitle", subtitleIndex), Index: &subtitleIndex,
			},
		},
		Capabilities: startRequest.Capabilities, ClientPlaybackContext: startRequest.ClientPlaybackContext,
	})
	if replanned.PlaybackPlan == nil || replanned.Terminal != nil {
		t.Fatalf("subtitle replan = %#v", replanned)
	}
	requireRequestedVersionWithoutSubtitleV3(t, replanned.PlaybackPlan, source)
}
