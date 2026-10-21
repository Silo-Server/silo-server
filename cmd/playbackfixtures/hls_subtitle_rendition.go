package main

import (
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// goldenHLSSubtitleRenditionDecisionResponse is a /api/v2 transcode whose
// embedded SubRip track is carried as the HLS stream's subtitle rendition.
func goldenHLSSubtitleRenditionDecisionResponse() playback.DecisionResponseV3 {
	file := conformanceFallbackFile()
	file.SubtitleTracks = []models.SubtitleTrack{
		{Index: 3, Language: languageEnglish, Codec: "subrip", Title: titleEnglish},
	}
	request := goldenStartRequest()
	request.ClientFeatures = append(request.ClientFeatures, playback.FeatureHLSSubtitleRenditionsV3)
	request.QualityPreference = playback.QualityRung720pMediumV3
	request.SubtitleTrackIndex = new(0)
	request.SubtitleTrackID = playback.TrackIDV3(file.ID, "subtitle", 0)
	request.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{
		Enabled:                true,
		SupportedOnDevice:      true,
		Containers:             []string{"hls"},
		VideoCodecs:            []string{codecH264},
		AudioDecodeCodecs:      []string{codecAAC},
		AudioPassthroughCodecs: []string{},
		Subtitles:              playback.DeliverySubtitleCapabilitiesV3{SidecarText: true},
		Features:               []string{},
		AuthHeaderRefresh:      true,
		ValidatedClaims:        []string{},
		Transformations:        []playback.TransformationV3{},
	}
	response := goldenDecisionForSourceWith(file, request, nil, func(input *playback.PlannerInputV3) {
		input.SubtitleRenditionExecutable = func() bool { return true }
	})
	if plan := response.PlaybackPlan; plan.Delivery != playback.DeliveryTranscodeHLSV3 || plan.Subtitle.HLSRendition == nil {
		failf("golden subtitle rendition plan is %s without a rendition", plan.Delivery)
	}
	response.PlaybackPlan.Stream.URL = "/playback/transcode/" + goldenSessionID + "/master.m3u8"
	response.ServerFeatures = playback.NativeServerFeaturesV3()
	return response
}
