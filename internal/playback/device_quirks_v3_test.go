package playback

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestAFTKRTHigh10OverrideIsExactAndPreservesVideo(t *testing.T) {
	file := &models.MediaFile{
		ID: 42, FilePath: "/media/high10.mkv", Container: "mkv", CodecVideo: "h264", CodecAudio: "aac",
		Resolution: "1080p", Bitrate: 12_000, AudioChannels: 2,
		VideoTracks: []models.VideoTrack{{Codec: "h264", Profile: "High 10", Level: 52, Width: 1920, Height: 1080, FrameRate: "24000/1001", Bitrate: 12_000, BitDepth: 10, VideoRange: "SDR", VideoRangeType: "SDR"}},
		AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, Layout: "stereo"}},
	}
	req := quirkRequestV3()
	req.Capabilities.Containers = []string{"mkv"}
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "h264", Profiles: []string{"high"}, Levels: []int{51}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 20_000, Hardware: true}}

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: false}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 || result.PlayMethod != PlayDirect || len(result.Plan.AppliedQuirks) != 1 || result.Plan.AppliedQuirks[0].ID != QuirkFireTVAFTKRTHigh10V3 {
		t.Fatalf("result = %#v", result)
	}

	withoutFeature := req
	withoutFeature.ClientFeatures = []string{FeaturePlaybackPlanV3}
	withoutOptIn := PlanPlaybackV3(PlannerInputV3{Request: withoutFeature, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: false}, Registry: testTransformationRegistryV3()})
	if withoutOptIn.Plan != nil && (withoutOptIn.Plan.Delivery == DeliveryOriginalHTTPV3 || len(withoutOptIn.Plan.AppliedQuirks) != 0) {
		t.Fatalf("unadvertised quirk applied: %#v", withoutOptIn.Plan)
	}

	req.ClientPlaybackContext.Device.Model = "AFTKA"
	withoutExactEvidence := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: false}, Registry: testTransformationRegistryV3()})
	if withoutExactEvidence.Plan != nil && withoutExactEvidence.Plan.Delivery == DeliveryOriginalHTTPV3 {
		t.Fatalf("untested model received override: %#v", withoutExactEvidence.Plan)
	}
}

func TestAFTKRTEAC3HLSCorrectionTranscodesAudioOnly(t *testing.T) {
	file := &models.MediaFile{
		ID: 42, FilePath: "/media/eac3.avi", Container: "avi", CodecVideo: "h264", CodecAudio: "eac3",
		Resolution: "1080p", Bitrate: 12_000, AudioChannels: 8,
		VideoTracks: []models.VideoTrack{{Codec: "h264", Profile: "High", Level: 42, Width: 1920, Height: 1080, FrameRate: "24", Bitrate: 12_000, BitDepth: 8, VideoRange: "SDR", VideoRangeType: "SDR"}},
		AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 8, Layout: "7.1"}},
	}
	req := quirkRequestV3()
	req.Capabilities.Containers = []string{"mkv"}
	req.Capabilities.CodecsAudio = []string{"aac", "eac3"}
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "h264", Profiles: []string{"high"}, Levels: []int{42}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 20_000, Hardware: true}}
	req.ClientPlaybackContext.Deliveries[DeliveryClassProgressiveV3] = DeliveryCapabilityV3{}

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: false}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxHLSV3 || result.PlayMethod != PlayRemux || result.TargetVideoCodec != "copy" || !result.TranscodeAudio || result.TargetAudioCodec != "aac" || result.Plan.EffectiveRecipe.VideoCodec != "h264" {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Plan.AppliedQuirks) != 1 || result.Plan.AppliedQuirks[0].ID != QuirkFireTVAFTKRTEAC3HLSV3 {
		t.Fatalf("quirks = %#v", result.Plan.AppliedQuirks)
	}
	wire, err := json.Marshal(result.Plan)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	if !bytes.Contains(wire, []byte(`"runtime_corrections":[]`)) {
		t.Fatalf("runtime corrections must remain an array: %s", wire)
	}

	req.ClientPlaybackContext.Device.Model = "AFTKA"
	withoutQuirk := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: false}, Registry: testTransformationRegistryV3()})
	if withoutQuirk.Plan == nil || withoutQuirk.Plan.Delivery != DeliveryRemuxHLSV3 {
		t.Fatalf("non-quirk HLS result = %#v", withoutQuirk)
	}
	wire, err = json.Marshal(withoutQuirk.Plan)
	if err != nil {
		t.Fatalf("marshal non-quirk plan: %v", err)
	}
	if !bytes.Contains(wire, []byte(`"applied_quirks":[]`)) || !bytes.Contains(wire, []byte(`"runtime_corrections":[]`)) {
		t.Fatalf("quirk fields must remain arrays: %s", wire)
	}
}

func TestDV8HDR10PlusCorrectionRequiresAdvertisedRuntime(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].DVProfile = 8
	file.VideoTracks[0].DVBLCompatID = 1
	file.VideoTracks[0].HDR10Plus = true
	file.VideoTracks[0].VideoRange = "HDR"
	file.VideoTracks[0].VideoRangeType = "DOVI HDR10+"
	dv8Output := &HDRCapabilitiesV3{HDR10: true, HDR10Plus: true, DolbyVisionProfiles: []int{8}}
	philips := func(soc string) DeviceContextV3 {
		return DeviceContextV3{Platform: "android", Manufacturer: "TPV", Model: "Philips Google TV TA5", PlatformDetails: map[string]string{"sdk_int": "34", "soc_manufacturer": soc}}
	}

	for _, test := range []struct {
		name   string
		device DeviceContextV3
		quirk  string
	}{
		{name: "Fire TV", device: quirkRequestV3().ClientPlaybackContext.Device, quirk: QuirkFireTVDV8HDR10PlusV3},
		{name: "MediaTek TV", device: philips("Mediatek"), quirk: "android.mediatek.dv8_hdr10plus_sei_v1"},
		{name: "other SoC", device: philips("QTI")},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := quirkRequestV3()
			req.ClientPlaybackContext.Device = test.device
			req.Capabilities.VideoDecode = hevcMain10DecodeV3()
			req.Capabilities.HDRDetails = dv8Output
			req.ClientPlaybackContext.Output.HDRDetails = dv8Output
			direct := req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
			direct.Features = append(direct.Features, ClientDV8HDR10PlusSanitizerV3)
			req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = direct

			result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: false}, Registry: testTransformationRegistryV3()})
			if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 {
				t.Fatalf("result = %s", ExplainPlannerResultV3(result))
			}
			if test.quirk == "" {
				if len(result.Plan.AppliedQuirks) != 0 || len(result.Plan.RuntimeCorrections) != 0 {
					t.Fatalf("unexpected correction: quirks=%#v corrections=%v", result.Plan.AppliedQuirks, result.Plan.RuntimeCorrections)
				}
				return
			}
			if len(result.Plan.RuntimeCorrections) != 1 || result.Plan.RuntimeCorrections[0] != ClientDV8HDR10PlusSanitizerV3 || len(result.Plan.AppliedQuirks) != 1 || result.Plan.AppliedQuirks[0].ID != test.quirk {
				t.Fatalf("quirks=%#v corrections=%v, want %s", result.Plan.AppliedQuirks, result.Plan.RuntimeCorrections, test.quirk)
			}

			direct.Features = nil
			req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = direct
			withoutRuntime := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: false}, Registry: testTransformationRegistryV3()})
			if withoutRuntime.Plan == nil || len(withoutRuntime.Plan.AppliedQuirks) != 0 || len(withoutRuntime.Plan.RuntimeCorrections) != 0 {
				t.Fatalf("unadvertised correction applied: %#v", withoutRuntime.Plan)
			}
		})
	}
}

func TestPlanAttemptKeyV3DeviceQuirkIsStable(t *testing.T) {
	width, height, bitrate := 3840, 2160, 60_000
	plan := PlanV3{
		PlanID: "plan:quirk", Delivery: DeliveryOriginalHTTPV3,
		Stream:             StreamV3{Protocol: StreamHTTPProgressiveV3, Container: "mkv"},
		EffectiveRecipe:    EffectiveRecipeV3{VideoCodec: "hevc", AudioCodec: "eac3", Width: &width, Height: &height, BitrateKbps: &bitrate, DynamicRange: "dolby_vision"},
		Subtitle:           SubtitleDecisionV3{Mode: SubtitleOffV3},
		AppliedQuirks:      []AppliedQuirkV3{{ID: QuirkFireTVDV8HDR10PlusV3, RegistryRevision: DeviceQuirkRegistryRevisionV3, Action: "client_runtime_correction"}},
		RuntimeCorrections: []string{ClientDV8HDR10PlusSanitizerV3},
	}
	if got := PlanAttemptKeyV3(plan, "9", nil); got != "v3:32a3a37d71bc4f43" {
		t.Fatalf("key = %q", got)
	}
}

// A channel ceiling below the source must not displace the AFTKRT quirk: the
// quirk's stereo recipe and applied-quirk record still own the conversion.
func TestAFTKRTEAC3HLSCorrectionSurvivesChannelCeiling(t *testing.T) {
	file := &models.MediaFile{
		ID: 42, FilePath: "/media/eac3.avi", Container: "avi", CodecVideo: "h264", CodecAudio: "eac3",
		Resolution: "1080p", Bitrate: 12_000, AudioChannels: 8,
		VideoTracks: []models.VideoTrack{{Codec: "h264", Profile: "High", Level: 42, Width: 1920, Height: 1080, FrameRate: "24", Bitrate: 12_000, BitDepth: 8, VideoRange: "SDR", VideoRangeType: "SDR"}},
		AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 8, Layout: "7.1"}},
	}
	req := quirkRequestV3()
	req.Capabilities.Containers = []string{"mkv"}
	req.Capabilities.CodecsAudio = []string{"aac", "eac3"}
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "h264", Profiles: []string{"high"}, Levels: []int{42}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 20_000, Hardware: true}}
	req.ClientPlaybackContext.Deliveries[DeliveryClassProgressiveV3] = DeliveryCapabilityV3{}
	hls := req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
	hls.MaxChannels = intPointerV3(6)
	req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = hls

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: false}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxHLSV3 || result.TargetVideoCodec != "copy" || !result.TranscodeAudio || result.TargetAudioChannels != 2 {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
	if len(result.Plan.AppliedQuirks) != 1 || result.Plan.AppliedQuirks[0].ID != QuirkFireTVAFTKRTEAC3HLSV3 || result.Plan.Claims.Audio.Reason != "device_hls_audio_adaptation" {
		t.Fatalf("quirks = %#v, audio claim = %#v", result.Plan.AppliedQuirks, result.Plan.Claims.Audio)
	}
	if names := SortedTransformationNamesV3(result.Plan.Transformations); len(names) != 1 || names[0] != TransformationAudioToAACV3 {
		t.Fatalf("transformations = %v, want one %s", names, TransformationAudioToAACV3)
	}
}

func quirkRequestV3() StartRequestV3 {
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDeviceQuirksV3)
	req.ClientPlaybackContext.Device = DeviceContextV3{Platform: "android", Manufacturer: "Amazon", Model: "AFTKRT", PlatformDetails: map[string]string{"sdk_int": "30"}}
	return req
}

// appleAVPlayerRequestV3 returns a StartRequestV3 that mimics what a Silo Apple
// client (Apple TV, build 29) sends. The original_http delivery's validated_claims
// list includes "apple_execution_plan_v1", which is the server's signal that the
// client is using AVPlayer.
func appleAVPlayerRequestV3() StartRequestV3 {
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDeviceQuirksV3)
	req.ClientPlaybackContext.Device = DeviceContextV3{Platform: "tvos"}
	req.ClientPlaybackContext.FormFactor = "tv"
	// Mirror the real Apple TV delivery advertisement: enabled, validated claim
	// present. Containers list includes mkv as the Apple TV app currently sends.
	req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = DeliveryCapabilityV3{
		Enabled:           true,
		SupportedOnDevice: true,
		ValidatedClaims:   []string{"apple_execution_plan_v1", "authenticated_stream_headers", "client_subtitle_overlay"},
		VideoCodecs:       []string{"h264", "hevc"},
		AudioDecodeCodecs: []string{"aac", "ac3", "eac3", "dts", "truehd", "flac"},
		Containers:        []string{"mp4", "mov", "m4v", "mkv", "matroska", "ts", "m4a"},
		Features:          []string{},
	}
	req.Capabilities.Containers = []string{"mp4", "mov", "m4v", "mkv", "matroska", "webm", "ts"}
	req.Capabilities.CodecsVideo = []string{"h264", "hevc"}
	req.Capabilities.CodecsAudio = []string{"aac", "ac3", "eac3", "dts", "truehd", "flac"}
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{
		{Codec: "hevc", Profiles: []string{"main", "main 10"}, Levels: []int{153}, BitDepths: []int{8, 10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true, DecoderName: "VideoToolbox"},
		{Codec: "h264", Profiles: []string{"baseline", "main", "high"}, Levels: []int{51}, BitDepths: []int{8}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 120_000, Hardware: true, DecoderName: "VideoToolbox"},
	}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	return req
}

func TestAppleAVPlayerMKVContainerFallbackForcesRemux(t *testing.T) {
	// Apple TV claiming mkv in containers + HEVC HDR10 MKV source → must remux,
	// not direct-play. This matches the real bug: AVPlayer cannot play raw MKV.
	file := &models.MediaFile{
		ID: 5614291, FilePath: "virtual://movie/tt32357218", Container: "mkv",
		CodecVideo: "hevc", CodecAudio: "eac3",
		Resolution: "2160p", Bitrate: 72_000, AudioChannels: 6,
		VideoTracks: []models.VideoTrack{{
			Codec: "hevc", Profile: "Main 10", Level: 153, Width: 3840, Height: 2160,
			FrameRate: "24000/1001", Bitrate: 72_000, BitDepth: 10,
			VideoRange: "HDR", VideoRangeType: "HDR10",
		}},
		AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 6, Layout: "5.1"}},
	}
	req := appleAVPlayerRequestV3()

	result := PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file,
		AudioTrackIndex: 0,
		Settings:        PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
		Registry:        testTransformationRegistryV3(),
	})

	if result.Plan == nil {
		t.Fatal("expected a plan, got terminal")
	}
	if result.Plan.Delivery == DeliveryOriginalHTTPV3 {
		t.Errorf("Apple TV MKV source must not be direct-played (delivery=%s)", result.Plan.Delivery)
	}
	if result.PlayMethod == PlayDirect {
		t.Errorf("Apple TV MKV source must not use direct play method (method=%s)", result.PlayMethod)
	}
	found := false
	for _, q := range result.Plan.AppliedQuirks {
		if q.ID == QuirkAppleAVPlayerMKVContainerV3 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected quirk %s in applied quirks, got %v", QuirkAppleAVPlayerMKVContainerV3, result.Plan.AppliedQuirks)
	}
}

func TestAppleAVPlayerMP4SourceUnaffectedByMKVQuirk(t *testing.T) {
	// Apple TV with an MP4 source: the MKV quirk must NOT fire, and direct play
	// must be selected as normal.
	file := &models.MediaFile{
		ID: 1234, FilePath: "virtual://movie/tt0000001", Container: "mp4",
		CodecVideo: "hevc", CodecAudio: "aac",
		Resolution: "1080p", Bitrate: 20_000, AudioChannels: 2,
		VideoTracks: []models.VideoTrack{{
			Codec: "hevc", Profile: "Main 10", Level: 120, Width: 1920, Height: 1080,
			FrameRate: "24", Bitrate: 20_000, BitDepth: 8,
			VideoRange: "SDR", VideoRangeType: "SDR",
		}},
		AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, Layout: "stereo"}},
	}
	req := appleAVPlayerRequestV3()

	result := PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file,
		AudioTrackIndex: 0,
		Settings:        PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: false},
		Registry:        testTransformationRegistryV3(),
	})

	if result.Plan == nil {
		t.Fatal("expected a plan, got terminal")
	}
	if result.Plan.Delivery != DeliveryOriginalHTTPV3 {
		t.Errorf("MP4 source on Apple TV must direct-play (delivery=%s)", result.Plan.Delivery)
	}
	for _, q := range result.Plan.AppliedQuirks {
		if q.ID == QuirkAppleAVPlayerMKVContainerV3 {
			t.Errorf("MKV quirk must not fire for MP4 source, but found it in applied quirks")
		}
	}
}

func TestNonAppleClientMKVSourceUnaffectedByMKVQuirk(t *testing.T) {
	// Android client with MKV source: must be direct-played as before; MKV quirk
	// must not interfere.
	file := &models.MediaFile{
		ID: 42, FilePath: "/media/movie.mkv", Container: "mkv",
		CodecVideo: "hevc", CodecAudio: "aac",
		Resolution: "1080p", Bitrate: 15_000, AudioChannels: 2,
		VideoTracks: []models.VideoTrack{{
			Codec: "hevc", Profile: "Main 10", Level: 120, Width: 1920, Height: 1080,
			FrameRate: "24", Bitrate: 15_000, BitDepth: 8,
			VideoRange: "SDR", VideoRangeType: "SDR",
		}},
		AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, Layout: "stereo"}},
	}
	req := validStartRequestV3()
	req.Capabilities.Containers = []string{"mkv", "mp4"}
	req.Capabilities.CodecsVideo = []string{"hevc", "h264"}
	req.Capabilities.CodecsAudio = []string{"aac"}
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{
		{Codec: "hevc", Profiles: []string{"main", "main 10"}, Levels: []int{120}, BitDepths: []int{8, 10}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 30_000, Hardware: true},
	}
	// No apple_execution_plan_v1 in original_http validated claims.

	result := PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file,
		AudioTrackIndex: 0,
		Settings:        PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: false},
		Registry:        testTransformationRegistryV3(),
	})

	if result.Plan == nil {
		t.Fatal("expected a plan, got terminal")
	}
	if result.Plan.Delivery != DeliveryOriginalHTTPV3 {
		t.Errorf("Android client MKV source must still direct-play (delivery=%s)", result.Plan.Delivery)
	}
	for _, q := range result.Plan.AppliedQuirks {
		if q.ID == QuirkAppleAVPlayerMKVContainerV3 {
			t.Errorf("MKV quirk must not fire for non-Apple client")
		}
	}
}

func TestAppleAVPlayerMKVQuirkNotFiredWhenMKVNotClaimed(t *testing.T) {
	// Apple TV client that does NOT include mkv in its container list (e.g. a
	// future build that has removed the mis-advertisement): quirk must not fire.
	req := appleAVPlayerRequestV3()
	// Remove mkv and matroska from the capabilities (already-fixed future client build).
	req.Capabilities.Containers = []string{"mp4", "mov", "m4v", "ts"}
	delivery := req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
	delivery.Containers = []string{"mp4", "mov", "m4v", "ts"}
	req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = delivery

	if _, fired := appleAVPlayerMKVContainerFallback(SourceDescriptorV3{Container: "mkv"}, req); fired {
		t.Error("quirk must not fire when mkv is not in the claimed container list")
	}
}
