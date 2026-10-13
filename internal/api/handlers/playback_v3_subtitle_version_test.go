package handlers

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

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
	if !slices.Contains(plan.DegradationWarnings, playback.SubtitleNotShownWarningV3()) {
		t.Fatalf("warnings = %#v, want the subtitle reported as on the file but not shown", plan.DegradationWarnings)
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

// outputChangeAfterKeptAlternateV3 starts on the 4K edition (every other
// version is unreadable at start), turns its English PGS on, repairs the other
// versions, and then switches to an output that cannot draw PGS. The active
// 4K edition was kept only for that subtitle, so the returned plan shows
// where playback goes once the subtitle cannot be shown there either.
func outputChangeAfterKeptAlternateV3(t *testing.T, handler *PlaybackHandler, active, requested *models.MediaFile, startRequest playback.StartRequestV3, others ...*models.MediaFile) *playback.PlanV3 {
	t.Helper()
	failedAt := time.Now().UTC()
	unreadable := append([]*models.MediaFile{requested}, others...)
	repaired := make([]models.MediaFile, len(unreadable))
	for i, file := range unreadable {
		repaired[i] = *file
		*file = models.MediaFile{ID: file.ID, ContentID: file.ContentID, FilePath: file.FilePath, ProbeFailedAt: &failedAt}
	}
	startRequest.FileID = requested.ID
	original := startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
	original.Subtitles.EmbeddedBitmap = true
	startRequest.ClientPlaybackContext.Deliveries = maps.Clone(startRequest.ClientPlaybackContext.Deliveries)
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = original

	rec := httptest.NewRecorder()
	handler.HandleStartPlayback(rec, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext()))
	var started playback.DecisionResponseV3
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil || started.PlaybackPlan.EffectiveMediaFileID != active.ID {
		t.Fatalf("start did not land on the 4K edition: status=%d body=%s", rec.Code, rec.Body.String())
	}
	t.Cleanup(func() { handler.tm.CloseTranscodeSession(started.SessionID, "") })
	for i, file := range unreadable {
		*file = repaired[i]
	}

	subtitleIndex := 0
	subtitled := postPlaybackReplanV3(t, handler, started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationTrackChangeV3,
		PlaybackAttemptID: startRequest.PlaybackAttemptID, ReplanRequestID: "kept-edition-track-0001",
		FailedPlanID: started.PlaybackPlan.PlanID, PlanAttemptID: "kept-edition-track-attempt-0001",
		PlanAttemptKey: started.PlaybackPlan.PlanAttemptKey, AttemptCount: 1, PositionSeconds: 30,
		QualityPreference: "auto",
		SelectedTracks: playback.SelectedTracksV3{
			Audio:    started.PlaybackPlan.SelectedTracks.Audio,
			Subtitle: &playback.TrackIdentityV3{ID: playback.TrackIDV3(active.ID, "subtitle", subtitleIndex), Index: &subtitleIndex},
		},
		Capabilities: startRequest.Capabilities, ClientPlaybackContext: startRequest.ClientPlaybackContext,
	})
	if subtitled.PlaybackPlan == nil || subtitled.PlaybackPlan.EffectiveMediaFileID != active.ID || subtitled.PlaybackPlan.SelectedTracks.Subtitle == nil {
		t.Fatalf("the PGS on the 4K edition was not selected: %#v", subtitled)
	}

	// The new output cannot draw PGS, so the 4K edition would need a burn-in
	// its HDR picture cannot take.
	nextContext := startRequest.ClientPlaybackContext
	nextContext.Output.OutputContextID = "route-2"
	nextContext.Deliveries = maps.Clone(startRequest.ClientPlaybackContext.Deliveries)
	original.Subtitles.EmbeddedBitmap = false
	nextContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = original
	replanned := postPlaybackReplanV3(t, handler, started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationOutputChangeV3,
		PlaybackAttemptID: startRequest.PlaybackAttemptID, ReplanRequestID: "kept-edition-output-0001",
		FailedPlanID: subtitled.PlaybackPlan.PlanID, PlanAttemptID: "kept-edition-output-attempt-0001",
		PlanAttemptKey: subtitled.PlaybackPlan.PlanAttemptKey, AttemptCount: 1, PositionSeconds: 40,
		QualityPreference: "auto",
		SelectedTracks:    subtitled.PlaybackPlan.SelectedTracks,
		Capabilities:      startRequest.Capabilities, ClientPlaybackContext: nextContext,
	})
	if replanned.PlaybackPlan == nil || replanned.Terminal != nil {
		t.Fatalf("output change = %#v", replanned)
	}
	return replanned.PlaybackPlan
}

// requireRequestedEditionWithoutSubtitleV3 fails unless plan is back on the
// requested edition with the subtitle off and reported as not on that file.
func requireRequestedEditionWithoutSubtitleV3(t *testing.T, plan *playback.PlanV3, requested *models.MediaFile) {
	t.Helper()
	if plan.EffectiveMediaFileID != requested.ID || plan.SelectedTracks.Subtitle != nil {
		t.Fatalf("effective file = %d subtitle = %#v, want the requested edition %d without the subtitle", plan.EffectiveMediaFileID, plan.SelectedTracks.Subtitle, requested.ID)
	}
	if !slices.Contains(plan.DegradationWarnings, playback.SubtitleTrackUnavailableWarningV3()) {
		t.Fatalf("warnings = %#v, want the subtitle reported as not on this file", plan.DegradationWarnings)
	}
}

// The active alternate is kept during an output change only to keep the
// viewer's subtitle. When the new output cannot show that subtitle on the
// alternate either, playback still returns to the requested edition without
// it: the requested edition is the picture the viewer asked for.
func TestHandleReplanPlaybackV3OutputChangeReturnsToRequestedEditionWhenSubtitleCannotBeShown(t *testing.T) {
	handler, active, requested, startRequest := subtitleBlockedHDRFixtureV3(t)
	requireRequestedEditionWithoutSubtitleV3(t, outputChangeAfterKeptAlternateV3(t, handler, active, requested, startRequest), requested)
}

// The requested edition is listed after every other version, which the
// fallback tries lower resolutions first. Another version that also drops the
// subtitle must not take its place, and neither may the 4K edition without it.
func TestHandleReplanPlaybackV3OutputChangeReturnsToRequestedEditionAheadOfOtherVersions(t *testing.T) {
	handler, active, requested, startRequest := subtitleBlockedHDRFixtureV3(t)
	lowerValue := *requested
	lower := &lowerValue
	lower.ID = 126
	lower.FilePath = writePlaybackTestMediaFile(t, "movie-720p.mkv")
	lower.Resolution = "720p"
	lower.Bitrate = 3_000
	lower.VideoTracks = []models.VideoTrack{{
		Codec: "h264", Profile: "high", Level: 31, Width: 1280, Height: 720,
		FrameRate: "24000/1001", Bitrate: 3_000, BitDepth: 8,
		VideoRange: "SDR", VideoRangeType: "SDR",
	}}
	handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{
		active.ContentID: {active, requested, lower},
	}}
	requireRequestedEditionWithoutSubtitleV3(t, outputChangeAfterKeptAlternateV3(t, handler, active, requested, startRequest, lower), requested)
}

// workerOnlyHeaderAuthenticatedV3 moves the fixture onto header-authenticated
// media with remux_execution=worker_only, where the API refuses progressive
// remuxes. The 1080p sibling becomes an MP4 the client direct plays, while
// the 4K MKV without its subtitle needs a progressive MP4 remux, and HLS
// carries only H.264, so that remux has nowhere to escalate.
func workerOnlyHeaderAuthenticatedV3(t *testing.T, handler *PlaybackHandler, alternate *models.MediaFile, startRequest *playback.StartRequestV3) {
	t.Helper()
	requireWorkerRoutingV3(handler)
	alternate.Container = "mp4"
	alternate.FilePath = writePlaybackTestMediaFile(t, "movie-1080p.mp4")
	startRequest.ClientFeatures = append(startRequest.ClientFeatures, playback.FeatureHeaderAuthenticatedMediaV3)
	startRequest.Capabilities.VideoDecode = append(startRequest.Capabilities.VideoDecode, v3HandlerStartRequest().Capabilities.VideoDecode[0])
	startRequest.Capabilities.Containers = []string{"mp4", "hls"}
	deliveries := maps.Clone(startRequest.ClientPlaybackContext.Deliveries)
	original := deliveries[playback.DeliveryClassOriginalHTTPV3]
	original.Containers = []string{"mp4"}
	deliveries[playback.DeliveryClassOriginalHTTPV3] = original
	deliveries[playback.DeliveryClassProgressiveV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true, Containers: []string{"mp4"},
		VideoCodecs: []string{"hevc", "h264"}, AudioDecodeCodecs: []string{"aac"},
	}
	startRequest.ClientPlaybackContext.Deliveries = deliveries
}

// requireExecutableSiblingV3 fails unless response plays the sibling that was
// already found playable, rather than a requested-file route nothing can run.
func requireExecutableSiblingV3(t *testing.T, response playback.DecisionResponseV3, alternate *models.MediaFile) {
	t.Helper()
	if response.PlaybackPlan == nil || response.Terminal != nil {
		t.Fatalf("response = terminal %#v, want the playable sibling %d", response.Terminal, alternate.ID)
	}
	if response.PlaybackPlan.EffectiveMediaFileID != alternate.ID || response.PlaybackPlan.Delivery != playback.DeliveryClassOriginalHTTPV3 {
		t.Fatalf("effective file = %d delivery = %s, want sibling %d direct", response.PlaybackPlan.EffectiveMediaFileID, response.PlaybackPlan.Delivery, alternate.ID)
	}
}

// The requested file without its subtitle only replaces a playable sibling
// when its plan can run. A progressive remux the API refuses and HLS cannot
// carry would otherwise end the start in a terminal.
func TestHandleStartPlaybackV3SubtitleOnlyRefusalKeepsAnExecutableSibling(t *testing.T) {
	handler, source, alternate, startRequest := subtitleBlockedHDRFixtureV3(t)
	workerOnlyHeaderAuthenticatedV3(t, handler, alternate, &startRequest)
	startRequest.SubtitleTrackID = playback.TrackIDV3(source.ID, "subtitle", 0)
	startRequest.SubtitleTrackIndex = new(0)

	rec := httptest.NewRecorder()
	handler.HandleStartPlayback(rec, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext()))
	var started playback.DecisionResponseV3
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &started) != nil {
		t.Fatalf("start status=%d body=%s", rec.Code, rec.Body.String())
	}
	if started.SessionID != "" {
		t.Cleanup(func() { handler.tm.CloseTranscodeSession(started.SessionID, "") })
	}
	requireExecutableSiblingV3(t, started, alternate)
}

// The same holds for an output change that leaves the 4K file unable to show
// the PGS it was playing with.
func TestHandleReplanPlaybackV3SubtitleOnlyRefusalKeepsAnExecutableSibling(t *testing.T) {
	handler, source, alternate, startRequest := subtitleBlockedHDRFixtureV3(t)
	workerOnlyHeaderAuthenticatedV3(t, handler, alternate, &startRequest)
	original := startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
	original.Containers = []string{"mkv", "mp4"}
	original.Subtitles.EmbeddedBitmap = true
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = original
	startRequest.Capabilities.Containers = []string{"mkv", "mp4", "hls"}
	startRequest.SubtitleTrackID = playback.TrackIDV3(source.ID, "subtitle", 0)
	startRequest.SubtitleTrackIndex = new(0)

	rec := httptest.NewRecorder()
	handler.HandleStartPlayback(rec, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext()))
	var started playback.DecisionResponseV3
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil || started.PlaybackPlan.EffectiveMediaFileID != source.ID || started.PlaybackPlan.SelectedTracks.Subtitle == nil {
		t.Fatalf("start did not play the 4K file with its PGS: status=%d body=%s", rec.Code, rec.Body.String())
	}
	t.Cleanup(func() { handler.tm.CloseTranscodeSession(started.SessionID, "") })

	capabilities := startRequest.Capabilities
	capabilities.Containers = []string{"mp4", "hls"}
	nextContext := startRequest.ClientPlaybackContext
	nextContext.Output.OutputContextID = "route-2"
	nextContext.Deliveries = maps.Clone(startRequest.ClientPlaybackContext.Deliveries)
	original.Containers = []string{"mp4"}
	original.Subtitles.EmbeddedBitmap = false
	nextContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = original
	replanned := postPlaybackReplanV3(t, handler, started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationOutputChangeV3,
		PlaybackAttemptID: startRequest.PlaybackAttemptID, ReplanRequestID: "executable-sibling-output-0001",
		FailedPlanID: started.PlaybackPlan.PlanID, PlanAttemptID: "executable-sibling-output-attempt-0001",
		PlanAttemptKey: started.PlaybackPlan.PlanAttemptKey, AttemptCount: 1, PositionSeconds: 30,
		QualityPreference: "auto", SelectedTracks: started.PlaybackPlan.SelectedTracks,
		Capabilities: capabilities, ClientPlaybackContext: nextContext,
	})
	requireExecutableSiblingV3(t, replanned, alternate)
}
