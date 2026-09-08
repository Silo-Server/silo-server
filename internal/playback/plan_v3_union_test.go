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
		{Name: "audio_to_aac", RecipeVersion: TransformationAudioToAACRecipeVersionV3},
		{Name: "video_to_h264", RecipeVersion: "2"},
		{Name: "server_dv7_to_hdr10", RecipeVersion: "1", Available: true},
	})
	if got := registry.WithAdvertised(nil); got != registry {
		t.Fatal("empty advertisement must return the receiver unchanged")
	}
	widened := registry.WithAdvertised([]TransformationV3{
		{Name: "Audio_To_AAC", Executor: "server", RecipeVersion: TransformationAudioToAACRecipeVersionV3},
		{Name: "video_to_h264", Executor: "server", RecipeVersion: "1"},
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
	clientOnly := registry.WithAdvertised([]TransformationV3{{Name: "audio_to_aac", Executor: "client", RecipeVersion: TransformationAudioToAACRecipeVersionV3}})
	if clientOnly.Available("audio_to_aac") {
		t.Fatal("client-executor advertisements must not widen server availability")
	}
}

func TestTransformationRegistryOnlyAdvertised(t *testing.T) {
	registry := NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: "audio_to_aac", RecipeVersion: TransformationAudioToAACRecipeVersionV3},
		{Name: "video_to_h264", RecipeVersion: "2", Available: true},
		{Name: "server_dv7_to_hdr10", RecipeVersion: "1", Available: true},
	})
	restricted := registry.OnlyAdvertised([]TransformationV3{
		{Name: "Audio_To_AAC", Executor: "server", RecipeVersion: TransformationAudioToAACRecipeVersionV3},
		{Name: "video_to_h264", Executor: "server", RecipeVersion: "1"},
		{Name: "server_dv7_to_hdr10", Executor: "client", RecipeVersion: "1"},
		{Name: "made_up_transform", Executor: "server", RecipeVersion: "1"},
	})
	if !restricted.Available("audio_to_aac") {
		t.Fatal("a matching node advertisement must be available")
	}
	if restricted.Available("video_to_h264") {
		t.Fatal("local availability and a mismatched node recipe must be excluded")
	}
	if restricted.Available("server_dv7_to_hdr10") {
		t.Fatal("local availability and client-executor advertisements must be excluded")
	}
	if restricted.Available("made_up_transform") {
		t.Fatal("advertisements must not introduce unknown specs")
	}
	if !registry.Available("video_to_h264") || !registry.Available("server_dv7_to_hdr10") {
		t.Fatal("restricting availability must not mutate the receiver")
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
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	local := NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: "video_to_h264", RecipeVersion: "2"},
		{Name: "audio_to_aac", RecipeVersion: TransformationAudioToAACRecipeVersionV3},
	})
	settings := PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}

	withoutNodes := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: settings, Registry: local})
	if withoutNodes.Terminal == nil || withoutNodes.Terminal.Reason != "conversion_tool_unavailable" {
		t.Fatalf("without nodes = %s", ExplainPlannerResultV3(withoutNodes))
	}

	union := local.WithAdvertised([]TransformationV3{
		{Name: "video_to_h264", Executor: "server", RecipeVersion: "2"},
		{Name: "audio_to_aac", Executor: "server", RecipeVersion: TransformationAudioToAACRecipeVersionV3},
	})
	withNodes := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: settings, Registry: local, HLSVideoRegistry: staticHLSRegistryV3(union)})
	if withNodes.Plan == nil || withNodes.Plan.Delivery != DeliveryTranscodeHLSV3 {
		t.Fatalf("with nodes = %s", ExplainPlannerResultV3(withNodes))
	}
}

func TestPlanPlaybackV3VideoRegistryExcludesForbiddenLocalToolchain(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].ColorTransfer = "bt709"
	req := validStartRequestV3()
	req.QualityPreference = "480p"
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	local := NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: "video_to_h264", RecipeVersion: "2", Available: true},
		{Name: "audio_to_aac", RecipeVersion: TransformationAudioToAACRecipeVersionV3, Available: true},
	})
	workerOnly := local.OnlyAdvertised([]TransformationV3{{
		Name: "audio_to_aac", Executor: "server", RecipeVersion: TransformationAudioToAACRecipeVersionV3,
	}})

	result := PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
		Registry: local, HLSVideoRegistry: staticHLSRegistryV3(workerOnly),
	})
	if result.Terminal == nil || result.Terminal.Reason != "conversion_tool_unavailable" {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
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
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	local := NewTransformationRegistryV3([]TransformationSpecV3{{Name: "audio_to_aac", RecipeVersion: TransformationAudioToAACRecipeVersionV3}})
	settings := PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}

	withoutNodes := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: settings, Registry: local})
	if withoutNodes.Terminal == nil || withoutNodes.Terminal.Reason != "audio_conversion_unsupported" {
		t.Fatalf("without nodes = %s", ExplainPlannerResultV3(withoutNodes))
	}

	union := local.WithAdvertised([]TransformationV3{{Name: "audio_to_aac", Executor: "server", RecipeVersion: TransformationAudioToAACRecipeVersionV3}})
	offloaded := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: settings, Registry: local, HLSRemuxRegistry: staticHLSRegistryV3(union)})
	if offloaded.Plan == nil || offloaded.Plan.Delivery != DeliveryRemuxHLSV3 || !offloaded.TranscodeAudio || offloaded.TargetAudioCodec != "aac" {
		t.Fatalf("with nodes = %s", ExplainPlannerResultV3(offloaded))
	}

	// With the toolchain available locally the progressive remux keeps
	// priority — offloadability must never demote a local-capable route.
	localCapable := NewTransformationRegistryV3([]TransformationSpecV3{{Name: "audio_to_aac", RecipeVersion: TransformationAudioToAACRecipeVersionV3, Available: true}})
	preserved := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: settings, Registry: localCapable, HLSRegistry: staticHLSRegistryV3(localCapable)})
	if preserved.Plan == nil || preserved.Plan.Delivery != DeliveryRemuxProgressiveV3 {
		t.Fatalf("local capable = %s", ExplainPlannerResultV3(preserved))
	}
}

func TestPlanPlaybackV3AudioAdaptationUsesProgressiveProxyToolchain(t *testing.T) {
	file := detailedFixtureFileV3()
	file.AudioTracks[0] = models.AudioTrack{Codec: "truehd", Channels: 8, Layout: "7.1"}
	file.CodecAudio = "truehd"
	req := validStartRequestV3()
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	delete(req.ClientPlaybackContext.Deliveries, DeliveryClassHLSV3)
	local := NewTransformationRegistryV3([]TransformationSpecV3{{Name: TransformationAudioToAACV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3}})
	proxy := local.WithAdvertised([]TransformationV3{{Name: TransformationAudioToAACV3, Executor: ExecutorServerV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3}})

	result := PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: local,
		ProgressiveRemuxRegistry: staticHLSRegistryV3(proxy),
	})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 || !result.TranscodeAudio || result.TargetAudioCodec != audioCodecAACV3 {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
}

func TestPlanPlaybackV3AudioOnlyUsesProgressiveProxyToolchain(t *testing.T) {
	file := audioOnlyFixtureFileV3()
	file.CodecAudio = "flac"
	file.AudioTracks[0].Codec = "flac"
	req := validStartRequestV3()
	req.FileID = file.ID
	req.Capabilities.Containers = []string{"mp4"}
	local := NewTransformationRegistryV3([]TransformationSpecV3{{Name: TransformationAudioToAACV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3}})
	proxy := local.WithAdvertised([]TransformationV3{{Name: TransformationAudioToAACV3, Executor: ExecutorServerV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3}})

	result := PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: local,
		ProgressiveRemuxRegistry: staticHLSRegistryV3(proxy),
	})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 || !result.TranscodeAudio || result.TargetAudioCodec != audioCodecAACV3 {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
}

func TestPlanPlaybackV3AudioOnlyPureRemuxDoesNotConsultProxyCapabilities(t *testing.T) {
	file := audioOnlyFixtureFileV3()
	req := validStartRequestV3()
	req.FileID = file.ID
	req.Capabilities.Containers = []string{"mp4"}
	delete(req.ClientPlaybackContext.Deliveries, DeliveryClassOriginalHTTPV3)

	result := PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3(),
		ProgressiveRemuxRegistry: func() *TransformationRegistryV3 {
			t.Fatal("transformation-free audio remux must not inspect proxy capabilities")
			return nil
		},
	})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 || result.TranscodeAudio {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
}

// A source that direct-plays must never trigger the lazy node-capability
// producer: building the widened registry can touch the network, and dead
// nodes must not add latency to starts that never use them.
func TestPlanPlaybackV3DirectPlayNeverConsultsNodeCapabilities(t *testing.T) {
	file := detailedFixtureFileV3()
	req := validStartRequestV3()
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}

	result := PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
		Registry: testTransformationRegistryV3(),
		ProgressiveRemuxRegistry: func() *TransformationRegistryV3 {
			t.Fatal("direct-play planning must not build the progressive capability registry")
			return nil
		},
		HLSRegistry: func() *TransformationRegistryV3 {
			t.Fatal("direct-play planning must not build the node capability registry")
			return nil
		},
	})
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
}

func TestPlanPlaybackV3DirectDolbyVisionDoesNotConsultStripCapabilities(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].DVProfile = 5
	file.VideoTracks[0].VideoRange = "DolbyVision"
	file.VideoTracks[0].VideoRangeType = "DOVI"
	req := validStartRequestV3()
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{DolbyVisionProfiles: []int{5}}
	req.ClientPlaybackContext.Output.HDRDetails = req.Capabilities.HDRDetails

	result := PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3(),
		ProgressiveRemuxRegistry: func() *TransformationRegistryV3 {
			t.Fatal("direct Dolby Vision planning must not inspect progressive strip capabilities")
			return nil
		},
		HLSRemuxRegistry: func() *TransformationRegistryV3 {
			t.Fatal("direct Dolby Vision planning must not inspect HLS strip capabilities")
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
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	delete(req.ClientPlaybackContext.Deliveries, DeliveryClassHLSV3)

	local := NewTransformationRegistryV3([]TransformationSpecV3{{Name: "audio_to_aac", RecipeVersion: TransformationAudioToAACRecipeVersionV3}})
	union := local.WithAdvertised([]TransformationV3{{Name: "audio_to_aac", Executor: "server", RecipeVersion: TransformationAudioToAACRecipeVersionV3}})
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
	offloaded := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: settings, Registry: local, HLSRemuxRegistry: staticHLSRegistryV3(union)})
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

func TestPlanPlaybackV3Profile7StripUsesProgressiveProxyToolchain(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].DVProfile = 7
	file.VideoTracks[0].DVBLCompatID = 6
	file.VideoTracks[0].DVELPresent = false
	file.VideoTracks[0].DVEnhancementLayer = ""
	file.VideoTracks[0].VideoRange = "DolbyVision"
	file.VideoTracks[0].VideoRangeType = "DOVIWithEL"
	req := validStartRequestV3()
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{5, 8}}
	req.ClientPlaybackContext.Output.HDRDetails = req.Capabilities.HDRDetails
	delete(req.ClientPlaybackContext.Deliveries, DeliveryClassHLSV3)
	local := NewTransformationRegistryV3([]TransformationSpecV3{{Name: TransformationServerDV7HDR10V3, RecipeVersion: "1"}})
	proxy := local.WithAdvertised([]TransformationV3{{Name: TransformationServerDV7HDR10V3, Executor: ExecutorServerV3, RecipeVersion: "1"}})

	result := PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: local,
		ProgressiveRemuxRegistry: staticHLSRegistryV3(proxy),
	})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
	if len(result.Plan.Transformations) != 1 || result.Plan.Transformations[0].Name != TransformationServerDV7HDR10V3 {
		t.Fatalf("transformations = %#v", result.Plan.Transformations)
	}
}
