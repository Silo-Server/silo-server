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
	request.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = goldenHLSDelivery()
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

// goldenHLSSubtitleRenditionsAllDecisionResponse is a /api/v2 transcode for a
// client with hls_subtitle_renditions_all_v1: both embedded SubRip tracks
// are on-demand renditions, the selected one the default.
func goldenHLSSubtitleRenditionsAllDecisionResponse() playback.DecisionResponseV3 {
	file := conformanceFallbackFile()
	file.SubtitleTracks = []models.SubtitleTrack{
		{Index: 3, ContainerTrackID: "4", Language: languageEnglish, Codec: "subrip", Title: titleEnglish},
		{Index: 4, ContainerTrackID: "5", Language: "spa", Codec: "subrip", Title: "Español", Forced: true},
	}
	request := goldenStartRequest()
	request.ClientFeatures = append(request.ClientFeatures, playback.FeatureHLSSubtitleRenditionsV3, playback.FeatureHLSSubtitleRenditionsAllV3)
	request.QualityPreference = playback.QualityRung720pMediumV3
	request.SubtitleTrackIndex = new(1)
	request.SubtitleTrackID = playback.TrackIDV3(file.ID, "subtitle", 1)
	request.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = goldenHLSDelivery()
	response := goldenDecisionForSourceWith(file, request, nil, func(input *playback.PlannerInputV3) {
		input.SubtitleRenditionExecutable = func() bool { return true }
		input.OnDemandSubtitleRenditionsExecutable = func() bool { return true }
		input.CueIndexedSubtitleTracks = func() map[uint64]bool { return map[uint64]bool{4: true, 5: true} }
	})
	if plan := response.PlaybackPlan; plan.Delivery != playback.DeliveryTranscodeHLSV3 || plan.Subtitle.HLSRendition == nil || plan.Subtitle.HLSRendition.Index != 2 {
		failf("golden on-demand rendition plan is %s without the selected rendition", plan.Delivery)
	}
	response.PlaybackPlan.Stream.URL = "/playback/transcode/" + goldenSessionID + "/master.m3u8"
	response.ServerFeatures = playback.NativeServerFeaturesV3()
	return response
}

// goldenHLSDelivery is a client that can play the server's H.264 HLS
// transcode with sidecar text subtitles.
func goldenHLSDelivery() playback.DeliveryCapabilityV3 {
	return playback.DeliveryCapabilityV3{
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
}
