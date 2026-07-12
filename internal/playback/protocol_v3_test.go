package playback

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestStartRequestV3Validation(t *testing.T) {
	index := 1
	req := validStartRequestV3()
	req.AudioTrackIndex = &index
	req.AudioTrackID = TrackIDV3(req.FileID, "audio", index)
	warnings, err := req.NormalizeAndValidate()
	if err != nil {
		t.Fatalf("NormalizeAndValidate: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v, want none", warnings)
	}

	req.AudioTrackID = TrackIDV3(req.FileID, "audio", 2)
	if _, err := req.NormalizeAndValidate(); err == nil {
		t.Fatal("mismatched track id/index accepted")
	}
}

func TestStartRequestV3UnknownQualityFallsBackToAuto(t *testing.T) {
	req := validStartRequestV3()
	req.QualityPreference = "future-super-quality"
	warnings, err := req.NormalizeAndValidate()
	if err != nil {
		t.Fatalf("NormalizeAndValidate: %v", err)
	}
	if req.QualityPreference != "auto" || len(warnings) != 1 || warnings[0].Code != "quality_preference_normalized" {
		t.Fatalf("quality=%q warnings=%#v", req.QualityPreference, warnings)
	}
}

func TestPlanAttemptKeyV3KotlinFixture(t *testing.T) {
	type fixture struct {
		Name                  string           `json:"name"`
		PlanID                string           `json:"plan_id"`
		Delivery              DeliveryV3       `json:"delivery"`
		StreamProtocol        StreamProtocolV3 `json:"stream_protocol"`
		Container             string           `json:"container"`
		VideoCodec            string           `json:"video_codec"`
		AudioCodec            string           `json:"audio_codec"`
		Width                 int              `json:"width"`
		Height                int              `json:"height"`
		BitrateKbps           int              `json:"bitrate_kbps"`
		DynamicRange          string           `json:"dynamic_range"`
		SubtitleMode          SubtitleModeV3   `json:"subtitle_mode"`
		Transformations       []string         `json:"transformations"`
		OutputRouteGeneration int64            `json:"output_route_generation"`
		LocalMutations        []string         `json:"local_mutations"`
		Expected              string           `json:"expected"`
	}
	body, err := os.ReadFile("testdata/protocol_v3/attempt_keys.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []fixture
	if err := json.Unmarshal(body, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, value := range fixtures {
		t.Run(value.Name, func(t *testing.T) {
			plan := PlanV3{
				PlanID:          value.PlanID,
				Delivery:        value.Delivery,
				Stream:          StreamV3{Protocol: value.StreamProtocol, Container: value.Container},
				EffectiveRecipe: EffectiveRecipeV3{VideoCodec: value.VideoCodec, AudioCodec: value.AudioCodec, Width: &value.Width, Height: &value.Height, BitrateKbps: &value.BitrateKbps, DynamicRange: value.DynamicRange},
				Subtitle:        SubtitleDecisionV3{Mode: value.SubtitleMode},
			}
			for _, name := range value.Transformations {
				plan.Transformations = append(plan.Transformations, TransformationV3{Name: name})
			}
			if got := PlanAttemptKeyV3(plan, value.OutputRouteGeneration, value.LocalMutations); got != value.Expected {
				t.Fatalf("key = %q, want %q", got, value.Expected)
			}
		})
	}
}

func TestProtocolV3GoldenWireFixtures(t *testing.T) {
	startBody, err := os.ReadFile("testdata/protocol_v3/start_request.json")
	if err != nil {
		t.Fatal(err)
	}
	var start StartRequestV3
	if err := json.Unmarshal(startBody, &start); err != nil {
		t.Fatal(err)
	}
	if _, err := start.NormalizeAndValidate(); err != nil {
		t.Fatalf("golden start request: %v", err)
	}
	responseBody, err := os.ReadFile("testdata/protocol_v3/decision_response.json")
	if err != nil {
		t.Fatal(err)
	}
	var response DecisionResponseV3
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatal(err)
	}
	if response.ProtocolVersion != ProtocolV3 || response.Outcome != OutcomePlayableV3 || response.PlaybackPlan == nil || response.PlaybackPlan.Stream.Protocol != StreamHTTPProgressiveV3 {
		t.Fatalf("golden response = %#v", response)
	}
}

func TestPlanPlaybackV3DirectRequiresDetailedEvidence(t *testing.T) {
	file := detailedFixtureFileV3()
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3, FeatureLayoutPassthrough)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3, FeatureLayoutPassthrough)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	req.ClientPlaybackContext.Output.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}

	req.ClientFeatures = []string{FeaturePlaybackPlanV3, FeatureMedia3Only}
	req.ClientPlaybackContext.Features = req.ClientFeatures
	result = PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: false, Allow4KTranscode: true}})
	if result.Terminal == nil || result.Terminal.Reason != "transcoding_disabled" {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
}

func TestPlanPlaybackV3AudioAdaptationCopiesVideo(t *testing.T) {
	file := detailedFixtureFileV3()
	file.AudioTracks[0] = models.AudioTrack{Codec: "truehd", Channels: 8, Layout: "7.1"}
	file.CodecAudio = "truehd"
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 || !result.TranscodeAudio || result.TargetVideoCodec != "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestPlanPlaybackV3FallsBackFromProgressiveToHLSWithoutRepeatingKey(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].ColorTransfer = "bt709"
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.Containers = []string{"mp4"}
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	first := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}})
	if first.Plan == nil || first.Plan.Delivery != DeliveryRemuxProgressiveV3 {
		t.Fatalf("first = %s", ExplainPlannerResultV3(first))
	}
	failedKey := PlanAttemptKeyV3(*first.Plan, req.OutputRouteGeneration, nil)
	second := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, AttemptedKeys: []string{failedKey}})
	if second.Plan == nil || second.Plan.Delivery != DeliveryRemuxHLSV3 || second.TargetVideoCodec != "copy" {
		t.Fatalf("second = %#v", second)
	}
	secondKey := PlanAttemptKeyV3(*second.Plan, req.OutputRouteGeneration, nil)
	third := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3(), AttemptedKeys: []string{failedKey, secondKey}})
	if third.Plan == nil || third.Plan.Delivery != DeliveryTranscodeHLSV3 || third.Plan.DecisionReason != "copy_routes_exhausted" {
		t.Fatalf("third = %#v", third)
	}
}

func TestPlanPlaybackV3NeverClaimsUnimplementedHDRTranscode(t *testing.T) {
	file := detailedFixtureFileV3()
	req := validStartRequestV3()
	req.QualityPreference = "1080p"
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}})
	if result.Terminal == nil || result.Terminal.Reason != "hdr_transcode_unsupported" {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
}

func TestPlanPlaybackV3Profile7StripNeverFallsThroughToHLSCopy(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].DVProfile = 7
	file.VideoTracks[0].DVBLCompatID = 1
	file.VideoTracks[0].DVELPresent = true
	file.VideoTracks[0].DVEnhancementLayer = "mel"
	file.VideoTracks[0].VideoRange = "DolbyVision"
	file.VideoTracks[0].VideoRangeType = "DOVIWithEL"
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.Containers = []string{"mp4"}
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{7}}
	registry := NewTransformationRegistryV3([]TransformationSpecV3{{Name: "dv_metadata_strip_to_hdr10", Available: true}})
	first := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: registry})
	if first.Plan == nil || first.Plan.Delivery != DeliveryRemuxProgressiveV3 || len(first.Plan.Transformations) != 1 {
		t.Fatalf("first = %#v", first)
	}
	failedKey := PlanAttemptKeyV3(*first.Plan, req.OutputRouteGeneration, nil)
	second := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: registry, AttemptedKeys: []string{failedKey}})
	if second.Plan != nil || second.Terminal == nil {
		t.Fatalf("second = %#v", second)
	}
}

func TestPlanPlaybackV3Profile8CompatibleBaseLayerStripsToHDR10(t *testing.T) {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].DVProfile = 8
	file.VideoTracks[0].DVBLCompatID = 1
	file.VideoTracks[0].VideoRange = "DolbyVision"
	file.VideoTracks[0].VideoRangeType = "DOVI"
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	registry := NewTransformationRegistryV3([]TransformationSpecV3{{Name: "dv_metadata_strip_to_hdr10", Available: true}})
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: registry})
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 || result.Plan.EffectiveRecipe.DynamicRange != "hdr10" || len(result.Plan.Transformations) != 1 || result.Plan.Transformations[0].Name != "dv_metadata_strip_to_hdr10" {
		t.Fatalf("result = %#v", result)
	}
}

func TestPlanPlaybackV3PassthroughRequiresExactLayoutEntry(t *testing.T) {
	file := detailedFixtureFileV3()
	file.CodecAudio = "truehd"
	file.AudioTracks[0] = models.AudioTrack{Codec: "truehd", Channels: 8, Layout: "7.1"}
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3, FeatureLayoutPassthrough)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3, FeatureLayoutPassthrough)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	req.Capabilities.AudioPassthrough = &AudioPassthroughV3{PassthroughCodecs: []string{"truehd"}, MaxChannels: 8, Entries: []AudioPassthroughEntryV3{{Codec: "truehd"}}}
	withoutLayout := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if withoutLayout.Plan == nil || !withoutLayout.TranscodeAudio {
		t.Fatalf("without exact layout = %#v", withoutLayout)
	}
	req.Capabilities.AudioPassthrough.Entries[0].ChannelCounts = []int{8}
	req.Capabilities.AudioPassthrough.Entries[0].Layouts = []string{"7.1"}
	withLayout := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}})
	if withLayout.Plan == nil || withLayout.Plan.Delivery != DeliveryOriginalHTTPV3 || !withLayout.Plan.Claims.Audio.Passthrough {
		t.Fatalf("with exact layout = %#v", withLayout)
	}
}

func TestPlanPlaybackV3DownloadedSubtitleUsesFrozenCombinedOrdinal(t *testing.T) {
	file := detailedFixtureFileV3()
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	index := 0
	req.SubtitleTrackIndex = &index
	req.SubtitleTrackID = TrackIDV3(file.ID, "subtitle", index)
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, AdditionalSubtitles: []SubtitleInventoryEntryV3{{CombinedIndex: 0, Codec: "srt", Source: "downloaded"}}})
	if result.Plan == nil || result.Plan.Subtitle.Mode != SubtitleRenderV3 || result.Plan.SelectedTracks.Subtitle == nil || result.Plan.SelectedTracks.Subtitle.ID != req.SubtitleTrackID {
		t.Fatalf("result = %#v", result)
	}
}

func TestSubtitleBurnInUsesEmbeddedOrdinalAndRejectsUnsupportedSources(t *testing.T) {
	file := detailedFixtureFileV3()
	file.ExternalSubtitles = []models.ExternalSubtitle{{Format: "ass"}}
	file.SubtitleTracks = []models.SubtitleTrack{{Codec: "hdmv_pgs_subtitle"}}
	req := validStartRequestV3()
	embeddedCombinedIndex := 1
	req.SubtitleTrackIndex = &embeddedCombinedIndex
	req.SubtitleTrackID = TrackIDV3(file.ID, "subtitle", embeddedCombinedIndex)
	result := ResolveSubtitlePolicyV3(file, req, true, nil)
	if !result.RequiresBurn || result.SelectedIndex != 1 || result.TransportIndex != 0 {
		t.Fatalf("embedded burn-in result = %#v", result)
	}

	externalIndex := 0
	req.SubtitleTrackIndex = &externalIndex
	req.SubtitleTrackID = TrackIDV3(file.ID, "subtitle", externalIndex)
	req.SubtitleFidelityPreference = SubtitleFidelityPreserveV3
	result = ResolveSubtitlePolicyV3(file, req, true, nil)
	if result.Terminal == nil || result.Terminal.Reason != "subtitle_burn_in_source_unsupported" {
		t.Fatalf("external burn-in result = %#v", result)
	}
}

func TestResolveQualityPolicyV3PreservesNonStandardSourceHeight(t *testing.T) {
	req := validStartRequestV3()
	req.QualityPreference = "auto"
	result := ResolveQualityPolicyV3(req, SourceDescriptorV3{Width: 2560, Height: 1440, BitrateKbps: 12_000})
	if !result.PreservesSource || result.RequiresTranscode || result.Width != 2560 || result.Height != 1440 || result.Label != "1440p" {
		t.Fatalf("quality result = %#v", result)
	}
}

func TestPlanPlaybackV3SeekedHLSCopyUsesVideoEncode(t *testing.T) {
	file := detailedFixtureFileV3()
	file.Resolution = "1080p"
	file.VideoTracks[0].Width = 1920
	file.VideoTracks[0].Height = 1080
	file.VideoTracks[0].BitDepth = 8
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].ColorTransfer = "bt709"
	req := validStartRequestV3()
	start := 20.0
	req.StartPosition = &start
	req.ClientFeatures = append(req.ClientFeatures, FeatureDetailedDecodeV3)
	req.ClientPlaybackContext.Features = append(req.ClientPlaybackContext.Features, FeatureDetailedDecodeV3)
	req.Capabilities.Containers = []string{"mp4"}
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.ClientPlaybackContext.Engines[string(EngineMedia3DirectV3)] = EngineCapabilityV3{}
	req.ClientPlaybackContext.Engines[string(EngineMedia3ProgressiveRemuxV3)] = EngineCapabilityV3{}
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Plan == nil || result.Plan.Delivery != DeliveryTranscodeHLSV3 || result.TargetVideoCodec != "h264" || result.Plan.DecisionReason != "seeked_hls_copy_unsafe" {
		t.Fatalf("result = %#v", result)
	}
}

func validStartRequestV3() StartRequestV3 {
	return StartRequestV3{
		ProtocolVersion:            ProtocolV3,
		ClientFeatures:             []string{FeaturePlaybackPlanV3, FeatureMedia3Only},
		FileID:                     42,
		ProfileID:                  "profile-1",
		PlaybackAttemptID:          "attempt-0001",
		QualityPreference:          "original",
		SubtitleFidelityPreference: SubtitleFidelityCompatibleV3,
		OutputRouteGeneration:      1,
		Capabilities:               ClientCodecCapabilitiesV3{CodecsVideo: []string{"hevc"}, CodecsAudio: []string{"aac"}, Containers: []string{"mkv"}, MaxResolution: "2160p"},
		ClientPlaybackContext: ClientPlaybackContextV3{ProtocolVersion: ProtocolV3, Features: []string{FeaturePlaybackPlanV3, FeatureMedia3Only}, Platform: "android", FormFactor: "tv", AppVersion: "test", Output: OutputContextV3{OutputRouteGeneration: 1}, Engines: map[string]EngineCapabilityV3{
			string(EngineMedia3DirectV3):           {Enabled: true, SupportedOnDevice: true, Subtitles: EngineSubtitleCapabilitiesV3{EmbeddedText: true, SidecarText: true}},
			string(EngineMedia3ProgressiveRemuxV3): {Enabled: true, SupportedOnDevice: true},
			string(EngineMedia3HLSV3):              {Enabled: true, SupportedOnDevice: true},
		}},
	}
}

func detailedFixtureFileV3() *models.MediaFile {
	return &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv", Container: "mkv", CodecVideo: "hevc", CodecAudio: "aac", Resolution: "2160p", Bitrate: 60_000, AudioChannels: 2, VideoTracks: []models.VideoTrack{{Codec: "hevc", Profile: "Main 10", Level: 153, Width: 3840, Height: 2160, FrameRate: "24000/1001", Bitrate: 60_000, BitDepth: 10, VideoRange: "HDR", VideoRangeType: "HDR10"}}, AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, Layout: "stereo"}}}
}

func testTransformationRegistryV3() *TransformationRegistryV3 {
	return NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: "audio_to_aac", Available: true},
		{Name: "video_to_h264", Available: true},
		{Name: "dv_metadata_strip_to_hdr10", Available: true},
	})
}
