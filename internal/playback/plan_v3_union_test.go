package playback

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func staticHLSRegistryV3(registry *TransformationRegistryV3) func() *TransformationRegistryV3 {
	return func() *TransformationRegistryV3 { return registry }
}

func TestTransformationRegistryWithAdvertised(t *testing.T) {
	registry := NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: "audio_to_aac", RecipeVersion: "1"},
		{Name: "video_to_h264", RecipeVersion: "1"},
		{Name: "server_dv7_to_hdr10", RecipeVersion: "1", Available: true},
	})
	if got := registry.WithAdvertised(nil); got != registry {
		t.Fatal("empty advertisement must return the receiver unchanged")
	}
	widened := registry.WithAdvertised([]TransformationV3{
		{Name: "Audio_To_AAC", Executor: "server", RecipeVersion: "1"},
		{Name: "video_to_h264", Executor: "server", RecipeVersion: "2"},
		{Name: "made_up_transform", Executor: "server", RecipeVersion: "1"},
	})
	if !widened.Available("audio_to_aac") {
		t.Fatal("a matching node advertisement must widen availability")
	}
	if widened.Available("video_to_h264") {
		t.Fatal("a recipe-version mismatch must not widen availability")
	}
	if widened.Available("made_up_transform") {
		t.Fatal("advertisements must not introduce specs the server does not define")
	}
	if !widened.Available("server_dv7_to_hdr10") {
		t.Fatal("locally available specs must stay available")
	}
	if registry.Available("audio_to_aac") {
		t.Fatal("widening must not mutate the receiver")
	}
	clientOnly := registry.WithAdvertised([]TransformationV3{{Name: "audio_to_aac", Executor: "client", RecipeVersion: "1"}})
	if clientOnly.Available("audio_to_aac") {
		t.Fatal("client-executor advertisements must not widen server availability")
	}
}

// A deployment whose API host lacks the H.264/AAC toolchain must still plan
// an HLS transcode when pooled transcode nodes advertise it, and must keep
// the terminal when nothing does.
func TestPlanPlaybackV3TranscodeOffloadsToNodeToolchain(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].ColorTransfer = "bt709"
	req := validStartRequestV3()
	req.QualityPreference = "480p"
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	local := NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: "video_to_h264", RecipeVersion: "1"},
		{Name: "audio_to_aac", RecipeVersion: "1"},
	})
	settings := PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}

	withoutNodes := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: settings, Registry: local})
	if withoutNodes.Terminal == nil || withoutNodes.Terminal.Reason != "conversion_tool_unavailable" {
		t.Fatalf("without nodes = %s", ExplainPlannerResultV3(withoutNodes))
	}

	union := local.WithAdvertised([]TransformationV3{
		{Name: "video_to_h264", Executor: "server", RecipeVersion: "1"},
		{Name: "audio_to_aac", Executor: "server", RecipeVersion: "1"},
	})
	withNodes := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: settings, Registry: local, HLSRegistry: staticHLSRegistryV3(union)})
	if withNodes.Plan == nil || withNodes.Plan.Delivery != DeliveryTranscodeHLSV3 {
		t.Fatalf("with nodes = %s", ExplainPlannerResultV3(withNodes))
	}
}

// Audio conversion on the remux family must skip the locally-executed
// progressive remux when only pooled nodes carry the AAC toolchain, shipping
// the same recipe on the node-offloadable HLS remux delivery instead.
func TestPlanPlaybackV3AudioAdaptationOffloadsToHLSRemux(t *testing.T) {
	file := detailedFixtureFileV3()
	file.AudioTracks[0] = models.AudioTrack{Codec: "truehd", Channels: 8, Layout: "7.1"}
	file.CodecAudio = "truehd"
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	local := NewTransformationRegistryV3([]TransformationSpecV3{{Name: "audio_to_aac", RecipeVersion: "1"}})
	settings := PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}

	withoutNodes := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: settings, Registry: local})
	if withoutNodes.Terminal == nil || withoutNodes.Terminal.Reason != "audio_conversion_unsupported" {
		t.Fatalf("without nodes = %s", ExplainPlannerResultV3(withoutNodes))
	}

	union := local.WithAdvertised([]TransformationV3{{Name: "audio_to_aac", Executor: "server", RecipeVersion: "1"}})
	offloaded := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: settings, Registry: local, HLSRegistry: staticHLSRegistryV3(union)})
	if offloaded.Plan == nil || offloaded.Plan.Delivery != DeliveryRemuxHLSV3 || !offloaded.TranscodeAudio || offloaded.TargetAudioCodec != "aac" {
		t.Fatalf("with nodes = %s", ExplainPlannerResultV3(offloaded))
	}

	// With the toolchain available locally the progressive remux keeps
	// priority — offloadability must never demote a local-capable route.
	localCapable := NewTransformationRegistryV3([]TransformationSpecV3{{Name: "audio_to_aac", RecipeVersion: "1", Available: true}})
	preserved := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: settings, Registry: localCapable, HLSRegistry: staticHLSRegistryV3(localCapable)})
	if preserved.Plan == nil || preserved.Plan.Delivery != DeliveryRemuxProgressiveV3 {
		t.Fatalf("local capable = %s", ExplainPlannerResultV3(preserved))
	}
}

// A source that direct-plays must never trigger the lazy node-capability
// producer: building the widened registry can touch the network, and dead
// nodes must not add latency to starts that never use them.
func TestPlanPlaybackV3DirectPlayNeverConsultsNodeCapabilities(t *testing.T) {
	file := detailedFixtureFileV3()
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}

	result := PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
		Registry: testTransformationRegistryV3(),
		HLSRegistry: func() *TransformationRegistryV3 {
			t.Fatal("direct-play planning must not build the node capability registry")
			return nil
		},
	})
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
}

// A progressive-only client (no HLS engine) cannot consume node-offloaded
// conversions, so node capabilities must not suppress its specific retryable
// audio terminal in favor of a generic non-retryable adaptation_unavailable.
func TestPlanPlaybackV3NodeToolchainDoesNotMaskTerminalForProgressiveOnlyClient(t *testing.T) {
	file := detailedFixtureFileV3()
	file.AudioTracks[0] = models.AudioTrack{Codec: "truehd", Channels: 8, Layout: "7.1"}
	file.CodecAudio = "truehd"
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	delete(req.ClientPlaybackContext.Engines, string(EngineMedia3HLSV3))

	local := NewTransformationRegistryV3([]TransformationSpecV3{{Name: "audio_to_aac", RecipeVersion: "1"}})
	union := local.WithAdvertised([]TransformationV3{{Name: "audio_to_aac", Executor: "server", RecipeVersion: "1"}})
	result := PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
		Registry: local, HLSRegistry: staticHLSRegistryV3(union),
	})
	if result.Terminal == nil || result.Terminal.Reason != "audio_conversion_unsupported" || !result.Terminal.Retryable {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
}

// The Profile 7 HDR10 strip must ride the HLS remux when only pooled nodes
// carry the dovi_rpu filter: the progressive remux executes locally and is
// not eligible without the local filter.
func TestPlanPlaybackV3Profile7StripOffloadsToHLSRemux(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].DVProfile = 7
	file.VideoTracks[0].DVBLCompatID = 6
	file.VideoTracks[0].DVELPresent = false
	file.VideoTracks[0].DVEnhancementLayer = ""
	file.VideoTracks[0].VideoRange = "DolbyVision"
	file.VideoTracks[0].VideoRangeType = "DOVIWithEL"
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{5, 8}}
	req.ClientPlaybackContext.Output.HDRDetails = req.Capabilities.HDRDetails
	local := NewTransformationRegistryV3([]TransformationSpecV3{{Name: "server_dv7_to_hdr10", RecipeVersion: "1"}})
	settings := PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}

	withoutNodes := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: settings, Registry: local})
	if withoutNodes.Terminal == nil || withoutNodes.Terminal.Reason != "hdr_transcode_unsupported" {
		t.Fatalf("without nodes = %s", ExplainPlannerResultV3(withoutNodes))
	}

	union := local.WithAdvertised([]TransformationV3{{Name: "server_dv7_to_hdr10", Executor: "server", RecipeVersion: "1"}})
	offloaded := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: settings, Registry: local, HLSRegistry: staticHLSRegistryV3(union)})
	if offloaded.Plan == nil || offloaded.Plan.Delivery != DeliveryRemuxHLSV3 || offloaded.TargetVideoCodec != "copy" {
		t.Fatalf("with nodes = %s", ExplainPlannerResultV3(offloaded))
	}
	if len(offloaded.Plan.Transformations) != 1 || offloaded.Plan.Transformations[0].Name != "server_dv7_to_hdr10" {
		t.Fatalf("transformations = %#v", offloaded.Plan.Transformations)
	}
	if offloaded.Plan.EffectiveRecipe.DynamicRange != "hdr10" || !offloaded.Plan.Claims.Video.HDR10 {
		t.Fatalf("claims = %#v", offloaded.Plan.Claims)
	}
}
