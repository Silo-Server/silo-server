package playback

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func cellularCapFixtureFileV3(bitrateKbps, width, height int, codec, resolution string) *models.MediaFile {
	file := detailedFixtureFileV3()
	file.Resolution = resolution
	file.CodecVideo = codec
	file.Bitrate = bitrateKbps
	file.VideoTracks = []models.VideoTrack{{
		Codec:          codec,
		Profile:        "main",
		Level:          153,
		Width:          width,
		Height:         height,
		FrameRate:      "24000/1001",
		Bitrate:        bitrateKbps,
		BitDepth:       8,
		VideoRange:     "SDR",
		VideoRangeType: "SDR",
		ColorTransfer:  "bt709",
	}}
	return file
}

func cellularCapDirectPlayRequestV3() StartRequestV3 {
	req := validStartRequestV3()
	req.QualityPreference = QualityAutoV3
	req.Capabilities.MaxResolution = "2160p"
	req.Capabilities.CodecsVideo = []string{"hevc", "h264"}
	req.Capabilities.CodecsVideoHardware = []string{"hevc", "h264"}
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{
		{
			Codec: "hevc", Profiles: []string{"main", "main 10"}, Levels: []int{153}, BitDepths: []int{8, 10},
			MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true,
		},
		{
			Codec: "h264", Profiles: []string{"high", "main"}, Levels: []int{51}, BitDepths: []int{8},
			MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true,
		},
	}
	req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true,
		Containers: []string{"mkv", "mp4"}, VideoCodecs: []string{"hevc", "h264"}, AudioDecodeCodecs: []string{"aac"},
		Subtitles: DeliverySubtitleCapabilitiesV3{EmbeddedText: true, SidecarText: true},
	}
	req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true,
		Containers: []string{"hls"}, VideoCodecs: []string{"h264"}, AudioDecodeCodecs: []string{"aac"},
	}
	return req
}

func TestPlanPlaybackV3ExplicitBandwidthCapRefusesWhenTranscodingDisabled(t *testing.T) {
	file := cellularCapFixtureFileV3(25_000, 3840, 2160, "hevc", "2160p")
	req := cellularCapDirectPlayRequestV3()
	req.BandwidthCapKbps = new(10_000)

	res := PlanPlaybackV3(PlannerInputV3{
		Request:         req,
		RequestedFile:   file,
		EffectiveFile:   file,
		AudioTrackIndex: 0,
		Settings:        PlannerSettingsV3{TranscodeEnabled: false, Allow4KTranscode: true},
		Registry:        testTransformationRegistryV3(),
	})
	if res.Terminal == nil || res.Terminal.Reason != "transcoding_disabled" {
		t.Fatalf("explicit bandwidth cap with transcoding disabled = %s, want terminal transcoding_disabled", ExplainPlannerResultV3(res))
	}
}

func TestPlanPlaybackV3ExplicitBandwidthCapRefusesWithLowerEstimateWhenTranscodingDisabled(t *testing.T) {
	file := cellularCapFixtureFileV3(25_000, 3840, 2160, "hevc", "2160p")
	req := cellularCapDirectPlayRequestV3()
	req.BandwidthCapKbps = new(10_000)
	req.BandwidthEstimateKbps = new(5_000)

	res := PlanPlaybackV3(PlannerInputV3{
		Request:         req,
		RequestedFile:   file,
		EffectiveFile:   file,
		AudioTrackIndex: 0,
		Settings:        PlannerSettingsV3{TranscodeEnabled: false, Allow4KTranscode: true},
		Registry:        testTransformationRegistryV3(),
	})
	if res.Terminal == nil || res.Terminal.Reason != "transcoding_disabled" {
		t.Fatalf("over-cap source with lower estimate and transcoding disabled = %s, want terminal transcoding_disabled", ExplainPlannerResultV3(res))
	}
}

func TestPlanPlaybackV3ExplicitBandwidthCapRefusesWithDeviceLimitWhenTranscodingDisabled(t *testing.T) {
	file := cellularCapFixtureFileV3(25_000, 3840, 2160, "hevc", "2160p")
	req := cellularCapDirectPlayRequestV3()
	req.Capabilities.MaxResolution = "1080p"
	req.BandwidthCapKbps = new(10_000)

	res := PlanPlaybackV3(PlannerInputV3{
		Request:         req,
		RequestedFile:   file,
		EffectiveFile:   file,
		AudioTrackIndex: 0,
		Settings:        PlannerSettingsV3{TranscodeEnabled: false, Allow4KTranscode: true},
		Registry:        testTransformationRegistryV3(),
	})
	if res.Terminal == nil || res.Terminal.Reason != "transcoding_disabled" {
		t.Fatalf("over-cap source with device limit and transcoding disabled = %s, want terminal transcoding_disabled", ExplainPlannerResultV3(res))
	}
}

func TestPlanPlaybackV3ExplicitBandwidthCapAllowsEstimateFallbackWhenSourceFitsCap(t *testing.T) {
	file := cellularCapFixtureFileV3(6_000, 3840, 2160, "hevc", "2160p")
	req := cellularCapDirectPlayRequestV3()
	req.BandwidthCapKbps = new(10_000)
	req.BandwidthEstimateKbps = new(5_000)

	res := PlanPlaybackV3(PlannerInputV3{
		Request:         req,
		RequestedFile:   file,
		EffectiveFile:   file,
		AudioTrackIndex: 0,
		Settings:        PlannerSettingsV3{TranscodeEnabled: false, Allow4KTranscode: true},
		Registry:        testTransformationRegistryV3(),
	})
	if res.Plan == nil || res.Plan.Delivery != DeliveryOriginalHTTPV3 {
		t.Fatalf("under-cap source with lower estimate and transcoding disabled = %s, want direct play fallback", ExplainPlannerResultV3(res))
	}
}

func TestPlanPlaybackV3ExplicitBandwidthCapAllowsDirectPlayUnderCapWhenTranscodingDisabled(t *testing.T) {
	file := cellularCapFixtureFileV3(6_000, 1920, 1080, "hevc", "1080p")
	req := cellularCapDirectPlayRequestV3()
	req.BandwidthCapKbps = new(10_000)

	res := PlanPlaybackV3(PlannerInputV3{
		Request:         req,
		RequestedFile:   file,
		EffectiveFile:   file,
		AudioTrackIndex: 0,
		Settings:        PlannerSettingsV3{TranscodeEnabled: false, Allow4KTranscode: true},
		Registry:        testTransformationRegistryV3(),
	})
	if res.Plan == nil || res.Plan.Delivery != DeliveryOriginalHTTPV3 {
		t.Fatalf("source under explicit cap with transcoding disabled = %s, want direct play", ExplainPlannerResultV3(res))
	}
}

func TestPlanPlaybackV3EstimateWithoutCapStillDirectPlaysWhenTranscodingDisabled(t *testing.T) {
	file := cellularCapFixtureFileV3(25_000, 3840, 2160, "hevc", "2160p")
	req := cellularCapDirectPlayRequestV3()
	req.BandwidthEstimateKbps = new(10_000)

	res := PlanPlaybackV3(PlannerInputV3{
		Request:         req,
		RequestedFile:   file,
		EffectiveFile:   file,
		AudioTrackIndex: 0,
		Settings:        PlannerSettingsV3{TranscodeEnabled: false, Allow4KTranscode: true},
		Registry:        testTransformationRegistryV3(),
	})
	if res.Plan == nil || res.Plan.Delivery != DeliveryOriginalHTTPV3 {
		t.Fatalf("estimate-only reduction with transcoding disabled = %s, want direct play fallback", ExplainPlannerResultV3(res))
	}
}

func TestPlanPlaybackV3LowDataCapOnlyCeiling(t *testing.T) {
	req := cellularCapDirectPlayRequestV3()
	req.BandwidthCapKbps = new(2_000)

	lowBitrate1080 := cellularCapFixtureFileV3(1_800, 1920, 1080, "hevc", "1080p")
	direct := PlanPlaybackV3(PlannerInputV3{
		Request:         req,
		RequestedFile:   lowBitrate1080,
		EffectiveFile:   lowBitrate1080,
		AudioTrackIndex: 0,
		Settings:        PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
		Registry:        testTransformationRegistryV3(),
	})
	if direct.Plan == nil || direct.Plan.Delivery != DeliveryOriginalHTTPV3 {
		t.Fatalf("1.8 Mbps 1080p under Auto / 2 Mbps = %s, want direct play", ExplainPlannerResultV3(direct))
	}

	highBitrate1080 := cellularCapFixtureFileV3(6_000, 1920, 1080, "hevc", "1080p")
	transcoded := PlanPlaybackV3(PlannerInputV3{
		Request:         req,
		RequestedFile:   highBitrate1080,
		EffectiveFile:   highBitrate1080,
		AudioTrackIndex: 0,
		Settings:        PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
		Registry:        testTransformationRegistryV3(),
	})
	if transcoded.Plan == nil || transcoded.Plan.Delivery != DeliveryTranscodeHLSV3 {
		t.Fatalf("6 Mbps 1080p under Auto / 2 Mbps = %s, want HLS transcode", ExplainPlannerResultV3(transcoded))
	}
	if transcoded.Plan.EffectiveRecipe.BitrateKbps == nil || *transcoded.Plan.EffectiveRecipe.BitrateKbps <= 0 || *transcoded.Plan.EffectiveRecipe.BitrateKbps > 2_000 {
		t.Fatalf("transcoded video bitrate = %v kbps, want <= 2000", transcoded.Plan.EffectiveRecipe.BitrateKbps)
	}
	if transcoded.Plan.EffectiveRecipe.Height == nil || *transcoded.Plan.EffectiveRecipe.Height > 720 {
		t.Fatalf("transcoded height = %v, want <= 720", transcoded.Plan.EffectiveRecipe.Height)
	}
}
