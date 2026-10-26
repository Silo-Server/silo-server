package playback

import (
	"strconv"
	"testing"
)

func canonicalHLSCopyRequestV3() StartRequestV3 {
	req := validStartRequestV3()
	req.Capabilities.VideoEvidence = EvidenceDeclaredV3
	req.Capabilities.AudioEvidence = EvidenceDeclaredV3
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	req.ClientPlaybackContext.Output.HDRDetails = req.Capabilities.HDRDetails
	delete(req.ClientPlaybackContext.Deliveries, DeliveryClassOriginalHTTPV3)
	delete(req.ClientPlaybackContext.Deliveries, DeliveryClassProgressiveV3)
	hls := req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
	hls.VideoCodecs = []string{"hevc"}
	hls.AudioDecodeCodecs = []string{"aac"}
	hls.HDRDetails = req.Capabilities.HDRDetails
	hls.Features = append(hls.Features, FeatureHLSCopyMPEGTSV3)
	req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = hls
	return req
}

func TestPlanPlaybackV3UnnegotiatedHLSPreservesDefaultPackaging(t *testing.T) {
	for _, feature := range []string{"", "native_hls_mpegts_v1", FeatureHLSCopyMPEGTSV3} {
		t.Run(feature, func(t *testing.T) {
			file := detailedFixtureFileV3()
			req := canonicalHLSCopyRequestV3()
			req.ClientFeatures = append(req.ClientFeatures, feature)
			hls := req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
			hls.Features = nil
			if feature == "native_hls_mpegts_v1" {
				hls.Features = []string{feature}
			}
			req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = hls
			result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Registry: NewTransformationRegistryV3(nil)})
			if result.Plan == nil || len(result.Plan.Transformations) != 0 || result.Plan.EffectiveRecipe.SegmentContainer != "" {
				t.Fatalf("unnegotiated packaging changed: %s", ExplainPlannerResultV3(result))
			}
		})
	}
}

func TestPlanPlaybackV3SeparateAudioHLS(t *testing.T) {
	file := detailedFixtureFileV3()
	req := canonicalHLSCopyRequestV3()
	req.ClientFeatures = []string{"native_hls_separate_audio_v1"}
	hls := req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
	hls.Features = []string{"native_hls_playback_v1", "native_hls_separate_audio_v1"}
	req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = hls
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Registry: NewTransformationRegistryV3([]TransformationSpecV3{{Name: "hls_separate_audio", RecipeVersion: "1", Available: true}})})
	if result.Plan == nil {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
	found := false
	for _, transformation := range result.Plan.Transformations {
		if transformation.Name == "hls_separate_audio" {
			found = true
		}
	}
	if !found {
		t.Fatalf("separate audio recipe missing: %+v", result.Plan.Transformations)
	}
}

func TestNativeHLSMasterDescribesEffectiveDVAndAudio(t *testing.T) {
	plan := &PlanV3{Source: SourceDescriptorV3{DVProfile: 8, DVLevel: 6}, EffectiveRecipe: EffectiveRecipeV3{VideoCodec: "hevc", DynamicRange: "dolby_vision", AudioCodec: "eac3"}}
	codecs, videoRange := NativeHLSMasterAttributesV3(plan, "")
	if codecs != "dvh1.08.06,ec-3" || videoRange != "PQ" {
		t.Fatalf("master attributes = %q/%q", codecs, videoRange)
	}
}

func serverDV81PlannerInputV3() PlannerInputV3 {
	file := unstrippableProfile7FixtureV3()
	file.VideoTracks[0].DVLevel = 6
	req := canonicalHLSCopyRequestV3()
	req.ClientFeatures = []string{"native_hls_separate_audio_v1", "server_dv7_to_dv81_v1"}
	hdr := &HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{8}, DolbyVisionProfileLevels: []DolbyVisionProfileCapabilityV3{{Profile: 8, MaxLevel: 6, BLCompatibilityIDs: []int{1}}}}
	req.Capabilities.HDRDetails = hdr
	req.ClientPlaybackContext.Output.HDRDetails = hdr
	hls := req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
	hls.HDRDetails = hdr
	hls.Features = []string{"native_hls_playback_v1", "native_hls_separate_audio_v1"}
	req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = hls
	return PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: NewTransformationRegistryV3([]TransformationSpecV3{{Name: "server_dv7_to_dv81", RecipeVersion: "1", Available: true}, {Name: "hls_separate_audio", RecipeVersion: "1", Available: true}})}
}

func TestPlanPlaybackV3ServerDV81PreservesSourceIdentity(t *testing.T) {
	input := serverDV81PlannerInputV3()
	file := input.EffectiveFile
	result := PlanPlaybackV3(input)
	if result.Plan == nil {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
	plan := result.Plan
	if plan.Source.DVProfile != 7 || plan.Source.MediaFileID != file.ID || plan.EffectiveMediaFileID != file.ID || plan.EffectiveRecipe.DynamicRange != "dolby_vision" || !plan.Claims.Video.DolbyVision {
		t.Fatalf("source/output identity = %+v/%+v", plan.Source, plan.EffectiveRecipe)
	}
	found := false
	for _, transform := range plan.Transformations {
		if transform.Name == "server_dv7_to_dv81" && transform.Executor == "server" {
			found = true
		}
	}
	if !found {
		t.Fatalf("server conversion is not frozen: %+v", plan.Transformations)
	}
}

func TestNativeHLSMasterDescribesServerConvertedDV(t *testing.T) {
	plan := &PlanV3{Delivery: DeliveryRemuxHLSV3, Source: SourceDescriptorV3{DVProfile: 7, DVLevel: 6}, EffectiveRecipe: EffectiveRecipeV3{VideoCodec: "hevc", DynamicRange: "dolby_vision", AudioCodec: "eac3"}, Transformations: []TransformationV3{{Name: "hls_separate_audio", Executor: "server", RecipeVersion: "1"}, {Name: "server_dv7_to_dv81", Executor: "server", RecipeVersion: "1"}}}
	codecs, _ := NativeHLSMasterAttributesV3(plan, "")
	if codecs != "dvh1.08.06,ec-3" {
		t.Fatalf("master describes original DV profile: %s", codecs)
	}
}

func TestPlanPlaybackV3ServerDV81FailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		change func(*PlannerInputV3)
	}{
		{"transcoding disabled", func(input *PlannerInputV3) { input.Settings.TranscodeEnabled = false }},
		{"viewer transcoding disabled", func(input *PlannerInputV3) { input.Settings.ViewerTranscodeDisabled = true }},
		{"no opt in", func(input *PlannerInputV3) { input.Request.ClientFeatures = []string{ClientNativeHLSSeparateAudioV3} }},
		{"no executor", func(input *PlannerInputV3) { input.Registry = NewTransformationRegistryV3(nil) }},
		{"output level too low", func(input *PlannerInputV3) { input.EffectiveFile.VideoTracks[0].DVLevel = 7 }},
		{"missing RPU", func(input *PlannerInputV3) { input.EffectiveFile.VideoTracks[0].DVRPUPresent = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := serverDV81PlannerInputV3()
			tc.change(&input)
			result := PlanPlaybackV3(input)
			if PlanConvertsDV7ToDV81V3(result.Plan) {
				t.Fatal("unsupported conversion was offered")
			}
		})
	}
}

func TestPlanPlaybackV3ServerDV81DoesNotRepeatOrMutateSource(t *testing.T) {
	input := serverDV81PlannerInputV3()
	original := input.EffectiveFile.VideoTracks[0]
	result := PlanPlaybackV3(input)
	if result.Plan == nil {
		t.Fatal(ExplainPlannerResultV3(result))
	}
	if input.EffectiveFile.VideoTracks[0].DVProfile != original.DVProfile || input.EffectiveFile.VideoTracks[0].DVELPresent != original.DVELPresent {
		t.Fatal("planner mutated source facts")
	}
	input.AttemptedKeys = []string{result.Plan.PlanAttemptKey}
	next := PlanPlaybackV3(input)
	if PlanConvertsDV7ToDV81V3(next.Plan) {
		t.Fatal("failed conversion was repeated")
	}
}

func TestSeparateAudioHLSRequiresAudioTrack(t *testing.T) {
	input := serverDV81PlannerInputV3()
	input.EffectiveFile.AudioTracks = nil
	input.EffectiveFile.CodecAudio = ""
	result := PlanPlaybackV3(input)
	if PlanUsesSeparateAudioHLSV3(result.Plan) {
		t.Fatal("separate rendition offered without an audio track")
	}
}

func TestNativeHLSTriesOtherNegotiatedPackaging(t *testing.T) {
	for _, unavailable := range []bool{true, false} {
		t.Run(strconv.FormatBool(unavailable), func(t *testing.T) {
			file := detailedFixtureFileV3()
			req := canonicalHLSCopyRequestV3()
			req.ClientFeatures = append(req.ClientFeatures, ClientNativeHLSSeparateAudioV3)
			hls := req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
			hls.Features = append(hls.Features, ClientNativeHLSSeparateAudioV3)
			req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = hls
			input := PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Registry: NewTransformationRegistryV3([]TransformationSpecV3{{Name: TransformationHLSSeparateAudioV3, RecipeVersion: "1", Available: !unavailable}})}
			if !unavailable {
				first := PlanPlaybackV3(input)
				if !PlanUsesSeparateAudioHLSV3(first.Plan) {
					t.Fatal("preferred recipe missing")
				}
				input.AttemptedKeys = []string{first.Plan.PlanAttemptKey}
			}
			next := PlanPlaybackV3(input)
			if next.Plan == nil || !PlanCopiesVideoAsMPEGTSV3(*next.Plan) {
				t.Fatalf("TS fallback missing: %s", ExplainPlannerResultV3(next))
			}
		})
	}
}
