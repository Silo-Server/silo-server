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
	req.ClientPlaybackContext.Engines[string(EngineMedia3ProgressiveRemuxV3)] = EngineCapabilityV3{}

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
	direct := req.ClientPlaybackContext.Engines[string(EngineMedia3DirectV3)]
	direct.Features = append(direct.Features, ClientDV8HDR10PlusSanitizerV3)
	req.ClientPlaybackContext.Engines[string(EngineMedia3DirectV3)] = direct

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: false}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 || len(result.Plan.RuntimeCorrections) != 1 || result.Plan.RuntimeCorrections[0] != ClientDV8HDR10PlusSanitizerV3 || len(result.Plan.AppliedQuirks) != 1 || result.Plan.AppliedQuirks[0].ID != QuirkFireTVDV8HDR10PlusV3 {
		t.Fatalf("result = %#v", result)
	}

	direct.Features = nil
	req.ClientPlaybackContext.Engines[string(EngineMedia3DirectV3)] = direct
	withoutRuntime := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: false}, Registry: testTransformationRegistryV3()})
	if withoutRuntime.Plan == nil || len(withoutRuntime.Plan.AppliedQuirks) != 0 || len(withoutRuntime.Plan.RuntimeCorrections) != 0 {
		t.Fatalf("unadvertised correction applied: %#v", withoutRuntime.Plan)
	}
}

func TestDeviceQuirkProtocolAcceptsEitherFeatureLocation(t *testing.T) {
	file := &models.MediaFile{
		ID: 42, FilePath: "/media/high10.mkv", Container: "mkv", CodecVideo: "h264", CodecAudio: "aac",
		Resolution: "1080p", Bitrate: 12_000, AudioChannels: 2,
		VideoTracks: []models.VideoTrack{{Codec: "h264", Profile: "High 10", Level: 52, Width: 1920, Height: 1080, FrameRate: "24000/1001", Bitrate: 12_000, BitDepth: 10, VideoRange: "SDR", VideoRangeType: "SDR"}},
		AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, Layout: "stereo"}},
	}
	req := quirkRequestV3()
	req.Capabilities.Containers = []string{"mkv"}
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "h264", Profiles: []string{"high"}, Levels: []int{51}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 20_000, Hardware: true}}
	// Advertise the quirk protocol in the top-level client features only.
	req.ClientPlaybackContext.Features = []string{FeaturePlaybackPlanV3, FeatureMedia3Only, FeatureDetailedDecodeV3}

	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: false}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 || len(result.Plan.AppliedQuirks) != 1 || result.Plan.AppliedQuirks[0].ID != QuirkFireTVAFTKRTHigh10V3 {
		t.Fatalf("client-features-only advertisement: %#v", result)
	}

	// Context-only advertisement is equally sufficient.
	contextOnly := quirkRequestV3()
	contextOnly.ClientFeatures = []string{FeaturePlaybackPlanV3, FeatureMedia3Only, FeatureDetailedDecodeV3}
	if !deviceQuirkProtocolAvailableV3(contextOnly) {
		t.Fatal("context-only quirk feature must enable the quirk protocol")
	}

	neither := quirkRequestV3()
	neither.ClientFeatures = []string{FeaturePlaybackPlanV3, FeatureMedia3Only}
	neither.ClientPlaybackContext.Features = neither.ClientFeatures
	if deviceQuirkProtocolAvailableV3(neither) {
		t.Fatal("quirk protocol enabled without any advertisement")
	}
}

func TestPlanAttemptKeyV3DeviceQuirkMatchesKotlin(t *testing.T) {
	width, height, bitrate := 3840, 2160, 60_000
	plan := PlanV3{
		PlanID: "plan:quirk", Delivery: DeliveryOriginalHTTPV3,
		Stream:             StreamV3{Protocol: StreamHTTPProgressiveV3, Container: "mkv"},
		EffectiveRecipe:    EffectiveRecipeV3{VideoCodec: "hevc", AudioCodec: "eac3", Width: &width, Height: &height, BitrateKbps: &bitrate, DynamicRange: "dolby_vision"},
		Subtitle:           SubtitleDecisionV3{Mode: SubtitleOffV3},
		AppliedQuirks:      []AppliedQuirkV3{{ID: QuirkFireTVDV8HDR10PlusV3, RegistryRevision: DeviceQuirkRegistryRevisionV3, Action: "client_runtime_correction"}},
		RuntimeCorrections: []string{ClientDV8HDR10PlusSanitizerV3},
	}
	if got := PlanAttemptKeyV3(plan, 9, nil); got != "v3:8d843bfffeb3adc3" {
		t.Fatalf("key = %q", got)
	}
}

func quirkRequestV3() StartRequestV3 {
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3, FeatureDeviceQuirksV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3, FeatureDeviceQuirksV3)
	req.ClientPlaybackContext.Device = DeviceContextV3{Manufacturer: "Amazon", Brand: "Amazon", Model: "AFTKRT", SDKInt: 30}
	return req
}
