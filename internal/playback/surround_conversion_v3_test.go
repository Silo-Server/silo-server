package playback

import (
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func surroundSourceFileV3(codec string, channels int, layout string) *models.MediaFile {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.CodecAudio = codec
	file.AudioChannels = channels
	file.AudioTracks = []models.AudioTrack{{Codec: codec, Channels: channels, Layout: layout}}
	return file
}

// surroundRequestV3 is an Android TV whose HDMI sink validates AC-3 and
// E-AC-3 5.1 but not TrueHD or DTS, which the client decodes with FFmpeg.
func surroundRequestV3(sinkCodecs ...string) StartRequestV3 {
	req := validStartRequestV3()
	req.ClientFeatures = append(req.ClientFeatures, FeatureLayoutPassthrough)
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.CodecsAudio = []string{"aac", "ac3", "eac3", "truehd", "dts", "dts_hd"}
	passthrough := &AudioPassthroughV3{PassthroughCodecs: sinkCodecs, MaxChannels: 8}
	for _, codec := range sinkCodecs {
		passthrough.Entries = append(passthrough.Entries, AudioPassthroughEntryV3{Codec: codec, ChannelCounts: []int{2, 6}, Layouts: []string{"stereo", "5.1", "5.1(side)"}})
	}
	req.Capabilities.AudioPassthrough = passthrough
	req.ClientPlaybackContext.Output.AudioPassthrough = passthrough
	for _, class := range []string{DeliveryClassOriginalHTTPV3, DeliveryClassHLSV3} {
		delivery := req.ClientPlaybackContext.Deliveries[class]
		delivery.AudioDecodeCodecs = req.Capabilities.CodecsAudio
		delivery.AudioPassthroughCodecs = sinkCodecs
		req.ClientPlaybackContext.Deliveries[class] = delivery
	}
	return req
}

func surroundRegistryV3() *TransformationRegistryV3 {
	return NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: TransformationAudioToAACV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: TransformationAudioToEAC3V3, RecipeVersion: TransformationAudioToEAC3RecipeVersionV3, Available: true},
		{Name: TransformationAudioToAC3V3, RecipeVersion: TransformationAudioToAC3RecipeVersionV3, Available: true},
		{Name: TransformationVideoToH264V3, RecipeVersion: TransformationVideoToH264RecipeVersionV3, Available: true},
	})
}

func planSurroundV3(req StartRequestV3, file *models.MediaFile, registry *TransformationRegistryV3, attempted ...string) PlannerResultV3 {
	return PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
		Registry: registry, AttemptedKeys: attempted,
	})
}

func TestPlanPlaybackV3ConvertsDecodedSurroundToSinkBitstream(t *testing.T) {
	for _, test := range []struct {
		name       string
		codec      string
		channels   int
		layout     string
		sink       []string
		wantCodec  string
		wantRecipe string
	}{
		{name: "TrueHD 7.1 to E-AC-3", codec: "truehd", channels: 8, layout: "7.1", sink: []string{"ac3", "eac3"}, wantCodec: "eac3", wantRecipe: TransformationAudioToEAC3V3},
		{name: "DTS 5.1 to E-AC-3", codec: "dts", channels: 6, layout: "5.1(side)", sink: []string{"ac3", "eac3"}, wantCodec: "eac3", wantRecipe: TransformationAudioToEAC3V3},
		{name: "AC-3 only sink", codec: "dts", channels: 6, layout: "5.1(side)", sink: []string{"ac3"}, wantCodec: "ac3", wantRecipe: TransformationAudioToAC3V3},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := planSurroundV3(surroundRequestV3(test.sink...), surroundSourceFileV3(test.codec, test.channels, test.layout), surroundRegistryV3())
			if result.Plan == nil {
				t.Fatalf("result = %s, want surround conversion", ExplainPlannerResultV3(result))
			}
			plan := result.Plan
			if plan.Delivery != DeliveryRemuxHLSV3 || plan.DecisionReason != decisionReasonSurroundBitstreamV3 {
				t.Fatalf("delivery=%s reason=%s, want copy-video HLS surround conversion", plan.Delivery, plan.DecisionReason)
			}
			if result.TargetVideoCodec != codecCopyV3 || plan.EffectiveRecipe.VideoCodec != "hevc" {
				t.Fatalf("video target=%q recipe=%q, want the source video copied", result.TargetVideoCodec, plan.EffectiveRecipe.VideoCodec)
			}
			if !plan.Claims.Audio.Passthrough || plan.Claims.Audio.Codec != test.wantCodec || plan.EffectiveRecipe.AudioCodec != test.wantCodec ||
				plan.EffectiveRecipe.AudioChannels == nil || *plan.EffectiveRecipe.AudioChannels != 6 || plan.EffectiveRecipe.AudioLayout != "5.1" {
				t.Fatalf("audio claim=%#v recipe=%#v, want %s 5.1 passthrough", plan.Claims.Audio, plan.EffectiveRecipe, test.wantCodec)
			}
			if !result.TranscodeAudio || result.TargetAudioCodec != test.wantCodec || result.TargetAudioChannels != 6 || result.TargetAudioBitrateKbps != 640 {
				t.Fatalf("executor target = %s/%d/%dk, want %s/6/640k", result.TargetAudioCodec, result.TargetAudioChannels, result.TargetAudioBitrateKbps, test.wantCodec)
			}
			if !slices.ContainsFunc(plan.Transformations, func(transformation TransformationV3) bool {
				return transformation.Name == test.wantRecipe && transformation.RecipeVersion == "1" && transformation.Executor == ExecutorServerV3
			}) {
				t.Fatalf("transformations = %#v, want %s@1", plan.Transformations, test.wantRecipe)
			}
			if !hasDegradationWarningV3(plan.DegradationWarnings, degradationAudioConvertedV3) {
				t.Fatalf("warnings = %#v, want the conversion disclosed", plan.DegradationWarnings)
			}
		})
	}
}

func TestPlanPlaybackV3KeepsDirectPlayWhenSurroundConversionDoesNotApply(t *testing.T) {
	withoutRecipes := NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: TransformationAudioToAACV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3, Available: true},
	})
	for _, test := range []struct {
		name     string
		file     *models.MediaFile
		request  func() StartRequestV3
		registry *TransformationRegistryV3
	}{
		{name: "stereo source", file: surroundSourceFileV3("dts", 2, "stereo"), request: func() StartRequestV3 { return surroundRequestV3("ac3", "eac3") }},
		{name: "sink passes the source codec through", file: surroundSourceFileV3("dts", 6, "5.1(side)"), request: func() StartRequestV3 { return surroundRequestV3("ac3", "eac3", "dts") }},
		{name: "sink lists the source codec under another layout label", file: surroundSourceFileV3("ac3", 6, "5.1(side)"), request: func() StartRequestV3 {
			req := surroundRequestV3("ac3", "eac3")
			for _, passthrough := range []*AudioPassthroughV3{req.Capabilities.AudioPassthrough} {
				passthrough.Entries[0].Layouts = []string{"stereo", "5.1"}
			}
			return req
		}},
		{name: "sink has no surround bitstream", file: surroundSourceFileV3("truehd", 8, "7.1"), request: func() StartRequestV3 { return surroundRequestV3() }},
		{name: "audio evidence is not exact", file: surroundSourceFileV3("truehd", 8, "7.1"), request: func() StartRequestV3 {
			req := surroundRequestV3("ac3", "eac3")
			req.Capabilities.AudioEvidence = EvidencePlatformAttestedV3
			return req
		}},
		{name: "client lacks layout-aware passthrough", file: surroundSourceFileV3("truehd", 8, "7.1"), request: func() StartRequestV3 {
			req := surroundRequestV3("ac3", "eac3")
			req.ClientFeatures = []string{FeaturePlaybackPlanV3}
			return req
		}},
		{name: "HLS delivery cannot pass the target through", file: surroundSourceFileV3("truehd", 8, "7.1"), request: func() StartRequestV3 {
			req := surroundRequestV3("ac3", "eac3")
			hls := req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
			hls.AudioPassthroughCodecs = nil
			req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = hls
			return req
		}},
		{name: "no executor offers the recipe", file: surroundSourceFileV3("truehd", 8, "7.1"), request: func() StartRequestV3 { return surroundRequestV3("ac3", "eac3") }, registry: withoutRecipes},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := test.registry
			if registry == nil {
				registry = surroundRegistryV3()
			}
			result := planSurroundV3(test.request(), test.file, registry)
			if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 {
				t.Fatalf("result = %s, want the original file played directly", ExplainPlannerResultV3(result))
			}
		})
	}
}

// A conversion the client already tried (it failed to start, or the server
// refused it at admission) must give way to the direct route it improved on.
func TestPlanPlaybackV3FallsBackToDirectPlayAfterSurroundConversionWasTried(t *testing.T) {
	req := surroundRequestV3("ac3", "eac3")
	file := surroundSourceFileV3("truehd", 8, "7.1")
	first := planSurroundV3(req, file, surroundRegistryV3())
	if !IsSurroundBitstreamConversionV3(first.Plan) {
		t.Fatalf("first plan = %s, want surround conversion", ExplainPlannerResultV3(first))
	}
	next := planSurroundV3(req, file, surroundRegistryV3(), PlanAttemptKeyV3(*first.Plan, req.ClientPlaybackContext.Output.OutputContextID, nil))
	if next.Plan == nil || next.Plan.Delivery != DeliveryOriginalHTTPV3 || next.Plan.Claims.Audio.Reason != "client_decode_supported" {
		t.Fatalf("replan = %s, want direct play with local decode", ExplainPlannerResultV3(next))
	}
}

func TestAppendAudioArgsFreezesSurroundBitstreamRecipe(t *testing.T) {
	for _, codec := range []string{"eac3", "ac3"} {
		got := appendAudioArgs(nil, TranscodeOpts{TargetCodecAudio: codec, TargetAudioChannels: 6, SourceAudioChannels: 8})
		want := []string{"-c:a", codec, "-b:a", "640k", "-ac", "6", "-ar", "48000"}
		if !slices.Equal(got, want) {
			t.Fatalf("%s args = %v, want %v", codec, got, want)
		}
	}
	// Callers that never asked for the frozen recipe keep their arguments.
	if got := appendAudioArgs(nil, TranscodeOpts{TargetCodecAudio: "eac3"}); !slices.Equal(got, []string{"-c:a", "eac3", "-b:a", "384k"}) {
		t.Fatalf("legacy eac3 args = %v", got)
	}
}
