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

func TestFireTVDV8HDR10PlusCorrectionRequiresAdvertisedRuntime(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].DVProfile = 8
	file.VideoTracks[0].DVBLCompatID = 1
	file.VideoTracks[0].HDR10Plus = true
	file.VideoTracks[0].VideoRange = "HDR"
	file.VideoTracks[0].VideoRangeType = "DOVI HDR10+"
	req := quirkRequestV3()
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true, HDR10Plus: true, DolbyVisionProfiles: []int{8}}
	req.ClientPlaybackContext.Output.HDRDetails = req.Capabilities.HDRDetails
	direct := req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
	direct.Features = append(direct.Features, ClientDV8HDR10PlusSanitizerV3)
	req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = direct

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: false}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 || len(result.Plan.RuntimeCorrections) != 1 || result.Plan.RuntimeCorrections[0] != ClientDV8HDR10PlusSanitizerV3 || len(result.Plan.AppliedQuirks) != 1 || result.Plan.AppliedQuirks[0].ID != QuirkFireTVDV8HDR10PlusV3 {
		t.Fatalf("result = %#v", result)
	}

	direct.Features = nil
	req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = direct
	withoutRuntime := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: false}, Registry: testTransformationRegistryV3()})
	if withoutRuntime.Plan == nil || len(withoutRuntime.Plan.AppliedQuirks) != 0 || len(withoutRuntime.Plan.RuntimeCorrections) != 0 {
		t.Fatalf("unadvertised correction applied: %#v", withoutRuntime.Plan)
	}
}

func TestDeviceQuirkProtocolRequiresTopLevelFeature(t *testing.T) {
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
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 || len(result.Plan.AppliedQuirks) != 1 || result.Plan.AppliedQuirks[0].ID != QuirkFireTVAFTKRTHigh10V3 {
		t.Fatalf("top-level advertisement: %#v", result)
	}

	without := quirkRequestV3()
	without.ClientFeatures = []string{FeaturePlaybackPlanV3}
	if deviceQuirkProtocolAvailableV3(without) {
		t.Fatal("quirk protocol enabled without advertisement")
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

func TestFirefoxMacOSHEVCResumeNormalizationScope(t *testing.T) {
	macFirefox := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:153.0) Gecko/20100101 Firefox/153.0"
	cases := []struct {
		name       string
		platform   string
		userAgent  string
		videoCodec string
		want       bool
	}{
		{name: "macOS Firefox HEVC", platform: "web", userAgent: macFirefox, videoCodec: "hevc", want: true},
		{name: "Windows Firefox", platform: "web", userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:153.0) Gecko/20100101 Firefox/153.0", videoCodec: "hevc"},
		{name: "Linux Firefox", platform: "web", userAgent: "Mozilla/5.0 (X11; Linux x86_64; rv:153.0) Gecko/20100101 Firefox/153.0", videoCodec: "hevc"},
		{name: "macOS Safari", platform: "web", userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Version/26.0 Safari/605.1.15", videoCodec: "hevc"},
		{name: "macOS SeaMonkey", platform: "web", userAgent: macFirefox + " SeaMonkey/2.53", videoCodec: "hevc"},
		{name: "macOS Firefox H264", platform: "web", userAgent: macFirefox, videoCodec: "h264"},
		{name: "native client", platform: "macos", userAgent: macFirefox, videoCodec: "hevc"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			req := validStartRequestV3()
			req.ClientPlaybackContext.Device = DeviceContextV3{Platform: test.platform, PlatformDetails: map[string]string{"user_agent": test.userAgent}}
			got := requiresFirefoxMacOSHEVCResumeNormalizationV3(SourceDescriptorV3{VideoCodec: test.videoCodec}, req)
			if got != test.want {
				t.Fatalf("matched = %v, want %v", got, test.want)
			}
		})
	}
}

func TestPlanPlaybackV3FreezesFirefoxMacOSHEVCResumeTransformation(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].Profile = "Main"
	file.VideoTracks[0].BitDepth = 8
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"

	request := validStartRequestV3()
	request.ClientPlaybackContext.FormFactor = "desktop"
	request.ClientPlaybackContext.Device = DeviceContextV3{Platform: "web", PlatformDetails: map[string]string{
		"user_agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:153.0) Gecko/20100101 Firefox/153.0",
	}}
	request.Capabilities.VideoEvidence = EvidenceDeclaredV3
	request.Capabilities.AudioEvidence = EvidenceDeclaredV3
	request.Capabilities.Containers = []string{"mp4"}
	request.ClientPlaybackContext.Deliveries = map[string]DeliveryCapabilityV3{
		DeliveryClassProgressiveV3: {
			Enabled: true, SupportedOnDevice: true,
			Containers: []string{"mp4"}, VideoCodecs: []string{"hevc"}, AudioDecodeCodecs: []string{"aac"},
		},
	}
	registry := NewTransformationRegistryV3([]TransformationSpecV3{{
		Name: TransformationServerHEVCResumeLeadingPictureDropV3, RecipeVersion: TransformationServerHEVCResumeLeadingPictureDropVersionV3, Available: true,
	}})

	for _, test := range []struct {
		name  string
		start float64
	}{{name: "initial start", start: 0}, {name: "resume", start: 1_234.5}} {
		t.Run(test.name, func(t *testing.T) {
			request.StartPosition = floatPointerV3(test.start)
			result := PlanPlaybackV3(PlannerInputV3{Request: request, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: registry})
			if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 {
				t.Fatalf("result = %s", ExplainPlannerResultV3(result))
			}
			if result.Plan.Timeline.PlayerStartSeconds != test.start {
				t.Fatalf("player start = %v, want %v", result.Plan.Timeline.PlayerStartSeconds, test.start)
			}
			if len(result.Plan.Transformations) != 1 || result.Plan.Transformations[0].Name != TransformationServerHEVCResumeLeadingPictureDropV3 ||
				result.Plan.Transformations[0].Executor != ExecutorServerV3 || result.Plan.Transformations[0].RecipeVersion != TransformationServerHEVCResumeLeadingPictureDropVersionV3 {
				t.Fatalf("transformations = %#v", result.Plan.Transformations)
			}
			if len(result.Plan.AppliedQuirks) != 0 {
				t.Fatalf("server recipe must not publish an unnegotiated client quirk: %#v", result.Plan.AppliedQuirks)
			}
		})
	}

	withoutCapability := PlanPlaybackV3(PlannerInputV3{Request: request, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: NewTransformationRegistryV3(nil)})
	if withoutCapability.Plan != nil && withoutCapability.Plan.Delivery == DeliveryRemuxProgressiveV3 {
		t.Fatalf("progressive copy planned without the noise BSF: %#v", withoutCapability.Plan)
	}
	wrongVersion := NewTransformationRegistryV3([]TransformationSpecV3{{Name: TransformationServerHEVCResumeLeadingPictureDropV3, RecipeVersion: "0", Available: true}})
	withStaleCapability := PlanPlaybackV3(PlannerInputV3{Request: request, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: wrongVersion})
	if withStaleCapability.Plan != nil && withStaleCapability.Plan.Delivery == DeliveryRemuxProgressiveV3 {
		t.Fatalf("progressive copy planned against a stale recipe: %#v", withStaleCapability.Plan)
	}

	hlsRequest := request
	hlsRequest.ClientPlaybackContext.Deliveries = map[string]DeliveryCapabilityV3{
		DeliveryClassProgressiveV3: {
			Enabled: true, SupportedOnDevice: true,
			Containers: []string{"mp4"}, VideoCodecs: []string{"hevc"}, AudioDecodeCodecs: []string{"aac"},
		},
		DeliveryClassHLSV3: {Enabled: true, SupportedOnDevice: true},
	}
	hlsResult := PlanPlaybackV3(PlannerInputV3{Request: hlsRequest, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: NewTransformationRegistryV3(nil)})
	if hlsResult.Plan == nil || hlsResult.Plan.Delivery != DeliveryRemuxHLSV3 {
		t.Fatalf("HLS fallback = %s", ExplainPlannerResultV3(hlsResult))
	}
	for _, transformation := range hlsResult.Plan.Transformations {
		if transformation.Name == TransformationServerHEVCResumeLeadingPictureDropV3 {
			t.Fatalf("progressive-only resume transformation leaked into HLS: %#v", hlsResult.Plan.Transformations)
		}
	}
	if len(hlsResult.Plan.AppliedQuirks) != 0 {
		t.Fatalf("server recipe unexpectedly published a client quirk: %#v", hlsResult.Plan.AppliedQuirks)
	}
}

func quirkRequestV3() StartRequestV3 {
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDeviceQuirksV3)
	req.ClientPlaybackContext.Device = DeviceContextV3{Platform: "android", Manufacturer: "Amazon", Model: "AFTKRT", PlatformDetails: map[string]string{"sdk_int": "30"}}
	return req
}
