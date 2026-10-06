package playback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestPlanPlaybackV3HEVCTranscodeSelectionAndFallbacks(t *testing.T) {
	tests := []struct {
		name          string
		allowHEVC     bool
		hlsHEVC       bool
		hevcAvailable bool
		wantCodec     string
	}{
		{name: "enabled", allowHEVC: true, hlsHEVC: true, hevcAvailable: true, wantCodec: "hevc"},
		{name: "disabled", hlsHEVC: true, hevcAvailable: true, wantCodec: "h264"},
		{name: "delivery does not support HEVC", allowHEVC: true, hevcAvailable: true, wantCodec: "h264"},
		{name: "HEVC encoder unavailable", allowHEVC: true, hlsHEVC: true, wantCodec: "h264"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := hevcTranscodePlannerInputV3(tc.allowHEVC, tc.hlsHEVC, tc.hevcAvailable)
			result := PlanPlaybackV3(input)
			if result.Plan == nil || result.TargetVideoCodec != tc.wantCodec || result.Plan.EffectiveRecipe.VideoCodec != tc.wantCodec {
				t.Fatalf("plan = %s, want %s transcode", ExplainPlannerResultV3(result), tc.wantCodec)
			}
			if tc.wantCodec == "hevc" {
				if result.Plan.EffectiveRecipe.VideoSampleEntry != VideoSampleEntryHVC1 || result.Plan.Transformations[0].Name != TransformationVideoToHEVCV3 {
					t.Fatalf("HEVC plan lacks fMP4 identity: %#v", result.Plan)
				}
				recipe := FreezeExecutableRecipeV3(result)
				if !recipe.Valid() || recipe.TargetVideoCodec != "hevc" || recipe.PlannerResult(result.Plan).TargetVideoCodec != "hevc" {
					t.Fatalf("frozen HEVC recipe lost target identity: %#v", recipe)
				}
			} else if result.Plan.EffectiveRecipe.VideoSampleEntry != "" || result.Plan.Transformations[0].Name != TransformationVideoToH264V3 {
				t.Fatalf("H.264 fallback retained HEVC identity: %#v", result.Plan)
			}
		})
	}
}

// A scaled encode never targets more than the source's own bits counted in
// the output codec: a 3 Mbps HEVC source converted to 1080p HEVC stays at
// 3 Mbps, while H.264 output may use the 5 Mbps H.264 equivalent. The HEVC
// decoder is checked against the HEVC target, and a failed HEVC attempt falls
// back to H.264 at the H.264 bitrate.
func TestPlanPlaybackV3HEVCOutputKeepsTheSourceBitrateBound(t *testing.T) {
	input4K := func(allowHEVC bool) PlannerInputV3 {
		input := hevcTranscodePlannerInputV3(allowHEVC, true, true)
		file := *input.RequestedFile
		file.CodecVideo, file.Resolution, file.Bitrate = "hevc", "2160p", 3_000
		file.VideoTracks = []models.VideoTrack{{Codec: "hevc", Profile: "Main", Width: 3840, Height: 2160, FrameRate: "24/1", Bitrate: 3_000, BitDepth: 8, VideoRange: "SDR"}}
		input.RequestedFile, input.EffectiveFile = &file, &file
		input.Settings.Allow4KTranscode = true
		input.Request.QualityPreference = "auto"
		estimate := 10_000
		input.Request.BandwidthEstimateKbps = &estimate
		return input
	}
	check := func(name string, result PlannerResultV3, wantCodec string, wantBitrate int) {
		t.Helper()
		if result.Plan == nil || result.TargetVideoCodec != wantCodec || result.TargetBitrateKbps != wantBitrate || result.TargetResolution != "1080p" ||
			result.Plan.EffectiveRecipe.BitrateKbps == nil || *result.Plan.EffectiveRecipe.BitrateKbps != wantBitrate {
			t.Fatalf("%s: %s codec %q res %q bitrate %d, want %s 1080p %d", name, ExplainPlannerResultV3(result), result.TargetVideoCodec, result.TargetResolution, result.TargetBitrateKbps, wantCodec, wantBitrate)
		}
		if wantCodec == "h264" && result.Plan.Transformations[0].Name != TransformationVideoToH264V3 {
			t.Fatalf("H264 fallback transformation = %+v", result.Plan.Transformations)
		}
	}
	first := PlanPlaybackV3(input4K(true))
	check("HEVC allowed", first, "hevc", 3_000)
	check("HEVC off", PlanPlaybackV3(input4K(false)), "h264", 5_000)

	retry := input4K(true)
	retry.AttemptedKeys = []string{first.Plan.PlanAttemptKey}
	check("after a failed HEVC attempt", PlanPlaybackV3(retry), "h264", 5_000)

	hevcOnly := input4K(true)
	hls := hevcOnly.Request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
	hls.VideoCodecs = []string{"hevc"}
	hevcOnly.Request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = hls
	hevcOnly.Request.Capabilities.VideoDecode[0].MaxBitrateKbps = 4_000
	check("HEVC-only delivery with a 4 Mbps decoder", PlanPlaybackV3(hevcOnly), "hevc", 3_000)
}

// A server bitrate cap reserves the audio share before the HEVC decoder is
// checked, so an HEVC-only client whose decoder takes the capped video rate
// is not refused over the uncapped one.
func TestPlanPlaybackV3HEVCCheckedAfterServerCapAudioReserve(t *testing.T) {
	input := hevcTranscodePlannerInputV3(true, true, true)
	file := *input.RequestedFile
	file.CodecVideo, file.Bitrate = "hevc", 5_100
	file.VideoTracks = []models.VideoTrack{{Codec: "hevc", Profile: "Main", Width: 1920, Height: 1080, FrameRate: "24/1", Bitrate: 4_800, BitDepth: 8, VideoRange: "SDR"}}
	input.RequestedFile, input.EffectiveFile = &file, &file
	input.ServerBitrateCapKbps = 5_000
	input.Request.QualityPreference = QualityOriginalV3
	hls := input.Request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
	hls.VideoCodecs = []string{"hevc"}
	input.Request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = hls
	input.Request.Capabilities.VideoDecode[0].MaxBitrateKbps = 4_700
	result := PlanPlaybackV3(input)
	if result.Plan == nil || result.TargetVideoCodec != "hevc" || result.TargetBitrateKbps <= 0 || result.TargetBitrateKbps > 4_700 {
		t.Fatalf("plan = %s codec %q bitrate %d, want HEVC within the 4.7 Mbps decoder", ExplainPlannerResultV3(result), result.TargetVideoCodec, result.TargetBitrateKbps)
	}
}

// A same-size conversion to HEVC is also capped at the source's bits counted
// as HEVC: an 8 Mbps H.264 source that must be re-encoded for an HEVC-only
// client with a 6 Mbps decoder gets a 4.8 Mbps HEVC stream.
func TestPlanPlaybackV3HEVCConversionAtSourceSizeIsCapped(t *testing.T) {
	input := hevcTranscodePlannerInputV3(true, true, true)
	file := *input.RequestedFile
	file.CodecVideo, file.Bitrate = "h264", 8_200
	file.VideoTracks = []models.VideoTrack{{Codec: "h264", Profile: "High", Width: 1920, Height: 1080, FrameRate: "24/1", Bitrate: 8_000, BitDepth: 8, VideoRange: "SDR"}}
	input.RequestedFile, input.EffectiveFile = &file, &file
	input.Request.QualityPreference = QualityOriginalV3
	hls := input.Request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
	hls.VideoCodecs = []string{"hevc"}
	input.Request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = hls
	input.Request.Capabilities.VideoDecode[0].MaxBitrateKbps = 6_000
	result := PlanPlaybackV3(input)
	if result.Plan == nil || result.TargetVideoCodec != "hevc" || result.TargetBitrateKbps != 4_800 {
		t.Fatalf("plan = %s codec %q bitrate %d, want HEVC at 4800", ExplainPlannerResultV3(result), result.TargetVideoCodec, result.TargetBitrateKbps)
	}
}

// H.264 stays the universal HLS output, but its bitrate stays within an
// attested H.264 decoder's limit for the output size; HEVC output is checked
// against its own decoder instead. The 5 Mbps limit still earns 1080p.
func TestPlanPlaybackV3H264TargetStaysWithinTheDecoderBitrate(t *testing.T) {
	for _, tc := range []struct {
		allowHEVC   bool
		wantCodec   string
		wantBitrate int
	}{
		{false, "h264", 5_000},
		{true, "hevc", 4_800},
	} {
		input := hevcTranscodePlannerInputV3(tc.allowHEVC, true, true)
		file := *input.RequestedFile
		file.CodecVideo, file.Bitrate = "h264", 8_200
		file.VideoTracks = []models.VideoTrack{{Codec: "h264", Profile: "High", Width: 1920, Height: 1080, FrameRate: "24/1", Bitrate: 8_000, BitDepth: 8, VideoRange: "SDR"}}
		input.RequestedFile, input.EffectiveFile = &file, &file
		input.Request.Capabilities.CodecsVideo = []string{"h264", "hevc"}
		input.Request.Capabilities.VideoDecode = append(input.Request.Capabilities.VideoDecode,
			VideoDecodeCapabilityV3{Codec: "h264", Profiles: []string{"High"}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 5_000, Hardware: true})
		input.Request.QualityPreference = "auto"
		estimate := 7_000
		input.Request.BandwidthEstimateKbps = &estimate
		result := PlanPlaybackV3(input)
		if result.Plan == nil || result.TargetVideoCodec != tc.wantCodec || result.TargetBitrateKbps != tc.wantBitrate {
			t.Fatalf("HEVC %v: %s codec %q bitrate %d, want %s at %d", tc.allowHEVC, ExplainPlannerResultV3(result), result.TargetVideoCodec, result.TargetBitrateKbps, tc.wantCodec, tc.wantBitrate)
		}
	}
}

// A scaled H.264 encode steps down to the tallest class an attested H.264
// decoder takes, while HEVC output keeps the class its own decoder takes; the
// H.264 fallback after a failed HEVC attempt uses the H.264 size.
func TestPlanPlaybackV3H264TargetStepsDownToTheDecoderSize(t *testing.T) {
	decoderWidth, decoderHeight := 1280, 720
	input := func(allowHEVC bool) PlannerInputV3 {
		input := hevcTranscodePlannerInputV3(allowHEVC, true, true)
		file := *input.RequestedFile
		file.CodecVideo, file.Bitrate = "h264", 8_200
		file.VideoTracks = []models.VideoTrack{{Codec: "h264", Profile: "High", Width: 1920, Height: 1080, FrameRate: "24/1", Bitrate: 8_000, BitDepth: 8, VideoRange: "SDR"}}
		input.RequestedFile, input.EffectiveFile = &file, &file
		input.Request.Capabilities.CodecsVideo = []string{"h264", "hevc"}
		input.Request.Capabilities.VideoDecode = append(input.Request.Capabilities.VideoDecode,
			VideoDecodeCapabilityV3{Codec: "h264", Profiles: []string{"High"}, BitDepths: []int{8}, MaxWidth: decoderWidth, MaxHeight: decoderHeight, MaxFrameRate: 60, Hardware: true})
		input.Request.QualityPreference = "auto"
		estimate := 7_000
		input.Request.BandwidthEstimateKbps = &estimate
		return input
	}
	check := func(name string, result PlannerResultV3, codec, res string, width, bitrate int) {
		t.Helper()
		if result.Plan == nil || result.TargetVideoCodec != codec || result.TargetResolution != res || result.TargetBitrateKbps != bitrate ||
			result.Plan.EffectiveRecipe.Width == nil || *result.Plan.EffectiveRecipe.Width != width {
			t.Fatalf("%s: %s codec %q res %q bitrate %d, want %s %s %d wide at %d", name, ExplainPlannerResultV3(result), result.TargetVideoCodec, result.TargetResolution, result.TargetBitrateKbps, codec, res, width, bitrate)
		}
	}
	check("H.264 only", PlanPlaybackV3(input(false)), "h264", "720p", 1280, 2_000)
	first := PlanPlaybackV3(input(true))
	check("HEVC allowed", first, "hevc", "1080p", 1920, 4_800)
	retry := input(true)
	retry.AttemptedKeys = []string{first.Plan.PlanAttemptKey}
	check("after a failed HEVC attempt", PlanPlaybackV3(retry), "h264", "720p", 1280, 2_000)

	// Limits that match no ladder box still bound the fitted size itself.
	decoderWidth, decoderHeight = 1280, 962
	check("a 1280x962 decoder", PlanPlaybackV3(input(false)), "h264", "720p", 1280, 2_000)

	// A same-size codec conversion is fitted too: a 5 Mbps 1080p HEVC source
	// fits the estimate, but an H.264-only client with a 720p decoder needs
	// an H.264 encode it can take.
	decoderWidth, decoderHeight = 1280, 720
	conversion := input(false)
	file := *conversion.RequestedFile
	file.CodecVideo, file.Bitrate = "hevc", 5_200
	file.VideoTracks = []models.VideoTrack{{Codec: "hevc", Profile: "Main", Width: 1920, Height: 1080, FrameRate: "24/1", Bitrate: 5_000, BitDepth: 8, VideoRange: "SDR"}}
	conversion.RequestedFile, conversion.EffectiveFile = &file, &file
	conversion.Request.Capabilities.CodecsVideo = []string{"h264"}
	conversion.Request.Capabilities.VideoDecode = conversion.Request.Capabilities.VideoDecode[1:]
	check("a same-size conversion", PlanPlaybackV3(conversion), "h264", "720p", 1280, 2_000)

	// A decoder must also take the source's frame rate, which the encode keeps.
	fast := input(false)
	fastFile := *fast.RequestedFile
	fastFile.VideoTracks = []models.VideoTrack{{Codec: "h264", Profile: "High", Width: 1920, Height: 1080, FrameRate: "60/1", Bitrate: 8_000, BitDepth: 8, VideoRange: "SDR"}}
	fast.RequestedFile, fast.EffectiveFile = &fastFile, &fastFile
	fast.Request.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{
		{Codec: "h264", Profiles: []string{"High"}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 30, Hardware: true},
		{Codec: "h264", Profiles: []string{"High"}, BitDepths: []int{8}, MaxWidth: 1280, MaxHeight: 720, MaxFrameRate: 60, Hardware: true},
	}
	estimate := 9_000
	fast.Request.BandwidthEstimateKbps = &estimate
	check("a 60 fps source", PlanPlaybackV3(fast), "h264", "720p", 1280, 2_000)

	// The decoder is checked against the even frame the encoder writes: a
	// 1917-wide source becomes 1918 wide, past a 1917-wide decoder.
	odd := input(false)
	oddFile := *odd.RequestedFile
	oddFile.VideoTracks = []models.VideoTrack{{Codec: "h264", Profile: "High", Width: 1917, Height: 1080, FrameRate: "24/1", Bitrate: 8_000, BitDepth: 8, VideoRange: "SDR"}}
	odd.RequestedFile, odd.EffectiveFile = &oddFile, &oddFile
	odd.Request.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{
		{Codec: "h264", Profiles: []string{"High"}, BitDepths: []int{8}, MaxWidth: 1917, MaxHeight: 1080, MaxFrameRate: 60, Hardware: true},
	}
	check("an odd-width source at the decoder limit", PlanPlaybackV3(odd), "h264", "720p", 1278, 2_000)

	// A decoder takes a class only when its bitrate limit earns it on the
	// ladder: a 1080p decoder limited to 3 Mbps gets 720p, not 1080p at 3 Mbps.
	slow := input(false)
	slow.Request.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{
		{Codec: "h264", Profiles: []string{"High"}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 3_000, Hardware: true},
	}
	check("a 3 Mbps decoder limit", PlanPlaybackV3(slow), "h264", "720p", 1280, 2_000)
	// A large decoder with a low limit does not hold back a smaller, faster one.
	slow.Request.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{
		{Codec: "h264", Profiles: []string{"High"}, BitDepths: []int{8}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 1_000, Hardware: true},
		{Codec: "h264", Profiles: []string{"High"}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 20_000, Hardware: true},
	}
	check("a slow 4K decoder beside a fast 1080p one", PlanPlaybackV3(slow), "h264", "1080p", 1920, 5_600)

	// Hardware decoders win only when one takes the source's frame rate; an
	// opted-in software decoder that does is used instead.
	software := input(false)
	software.RequestedFile, software.EffectiveFile = &fastFile, &fastFile
	software.Request.BandwidthEstimateKbps = &estimate
	software.Request.ClientFeatures = append(software.Request.ClientFeatures, FeatureSoftwareVideoDecodeV3)
	software.Request.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{
		{Codec: "h264", Profiles: []string{"High"}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 30, Hardware: true},
		{Codec: "h264", Profiles: []string{"High"}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60},
	}
	check("a 60 fps software decoder", PlanPlaybackV3(software), "h264", "1080p", 1920, 6_000)
	// Without the opt-in only the hardware decoder is left. Its 30 fps limit
	// holds at its largest size, so it takes the largest class whose pixel
	// rate fits: 720p60 from a 1080p30 decoder, 1080p60 from a 4K30 one.
	software.Request.ClientFeatures = []string{FeaturePlaybackPlanV3}
	check("software decode not opted in", PlanPlaybackV3(software), "h264", "720p", 1280, 2_000)
	software.Request.Capabilities.VideoDecode = software.Request.Capabilities.VideoDecode[:1]
	software.Request.Capabilities.VideoDecode[0].MaxWidth, software.Request.Capabilities.VideoDecode[0].MaxHeight = 3840, 2160
	check("a 4K30 decoder for a 60 fps source", PlanPlaybackV3(software), "h264", "1080p", 1920, 6_000)

	// A same-size conversion of a wide source keeps the class its height
	// earns (2560x1080 is 1080p), so a decoder whose bitrate limit earns
	// 1080p takes it at its own size.
	wide := input(false)
	wideFile := *wide.RequestedFile
	wideFile.CodecVideo, wideFile.Bitrate = "hevc", 5_200
	wideFile.VideoTracks = []models.VideoTrack{{Codec: "hevc", Profile: "Main", Width: 2560, Height: 1080, FrameRate: "24/1", Bitrate: 5_000, BitDepth: 8, VideoRange: "SDR"}}
	wide.RequestedFile, wide.EffectiveFile = &wideFile, &wideFile
	wide.Request.Capabilities.CodecsVideo = []string{"h264"}
	wide.Request.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{
		{Codec: "h264", Profiles: []string{"High"}, BitDepths: []int{8}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 15_000, Hardware: true},
	}
	wideEstimate := 10_000
	wide.Request.BandwidthEstimateKbps = &wideEstimate
	check("a 2560x1080 conversion under a 15 Mbps decoder", PlanPlaybackV3(wide), "h264", "1080p", 2560, 5_000)
}

// A transcode never asks for more than the ladder's 2160p class. On the
// original route a source taller than that is fitted into the 2160p box, as
// the old resolution buckets did, instead of being encoded at its own height.
func TestPlanPlaybackV3OriginalTranscodeStopsAt2160(t *testing.T) {
	for _, tc := range []struct {
		width, height int
		wantRes       string
		wantWidth     int
	}{
		{7680, 4320, "2160p", 3840},
		{7680, 3200, "1600p", 3840},
		{5760, 5760, "2160p", 2160},
	} {
		input := hevcTranscodePlannerInputV3(false, false, true)
		file := *input.RequestedFile
		file.CodecVideo, file.Bitrate = "hevc", 60_000
		file.VideoTracks = []models.VideoTrack{{Codec: "hevc", Profile: "Main", Width: tc.width, Height: tc.height, FrameRate: "30/1", Bitrate: 60_000, BitDepth: 8, VideoRange: "SDR"}}
		input.RequestedFile, input.EffectiveFile = &file, &file
		input.Request.QualityPreference = QualityOriginalV3
		input.Request.Capabilities.CodecsVideo = []string{"h264"}
		input.Request.Capabilities.VideoEvidence = EvidenceDeclaredV3
		input.Request.Capabilities.VideoDecode = nil
		input.Settings.Allow4KTranscode = true
		result := PlanPlaybackV3(input)
		if result.Plan == nil || result.TargetVideoCodec != "h264" || result.TargetResolution != tc.wantRes ||
			result.Plan.EffectiveRecipe.Width == nil || *result.Plan.EffectiveRecipe.Width != tc.wantWidth {
			t.Fatalf("%dx%d: %s codec %q res %q, want h264 %s %d wide", tc.width, tc.height, ExplainPlannerResultV3(result), result.TargetVideoCodec, result.TargetResolution, tc.wantRes, tc.wantWidth)
		}
	}
}

func TestPlanPlaybackV3HEVCFailureDoesNotGiveH264ToHEVCOnlyHLS(t *testing.T) {
	input := hevcTranscodePlannerInputV3(true, true, true)
	hls := input.Request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
	hls.VideoCodecs = []string{"hevc"}
	input.Request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = hls
	first := PlanPlaybackV3(input)
	if first.Plan == nil || first.TargetVideoCodec != "hevc" {
		t.Fatalf("first plan = %s, want HEVC", ExplainPlannerResultV3(first))
	}
	input.AttemptedKeys = []string{first.Plan.PlanAttemptKey}
	result := PlanPlaybackV3(input)
	if result.Terminal == nil || result.Terminal.Reason != "adaptation_unavailable" {
		t.Fatalf("HEVC-only client received an invalid fallback: %s", ExplainPlannerResultV3(result))
	}
}

func TestPlanPlaybackV3HEVCWithBoundedDecoderLevelFallsBackToH264(t *testing.T) {
	input := hevcTranscodePlannerInputV3(true, true, true)
	input.Request.Capabilities.VideoDecode[0].Levels = []int{123}
	result := PlanPlaybackV3(input)
	if result.Plan == nil || result.TargetVideoCodec != "h264" {
		t.Fatalf("level-bounded decoder received unpinned HEVC: %s", ExplainPlannerResultV3(result))
	}
}

func TestHEVCTranscodeUsesHVC1FMP4(t *testing.T) {
	opts := TranscodeOpts{InputPath: "/media/source.mkv", OutputDir: t.TempDir(), SourceVideoCodec: "av1", TargetCodecVideo: "hevc", TargetCodecAudio: "aac", VideoSampleEntry: VideoSampleEntryHVC1}
	if got := HLSOutputContainer(opts); got != OutputContainerFMP4 {
		t.Fatalf("HEVC HLS container = %q, want fmp4", got)
	}
	args := strings.Join(buildFFmpegArgs(opts), " ")
	for _, want := range []string{"-c:v libx265", "-tag:v hvc1", "-hls_segment_type fmp4", "-hls_segment_options movflags=+frag_discont", "seg_%05d.m4s"} {
		if !strings.Contains(args, want) {
			t.Fatalf("HEVC args missing %q: %s", want, args)
		}
	}
}

func TestProbeTransformationRegistryV3DoesNotAdvertiseFailedHEVCSmoke(t *testing.T) {
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\ncase \"$2\" in\n-bsfs) : ;;\n-encoders) echo ' V....D libx264 H.264'; echo ' V....D libx265 HEVC'; echo ' A....D aac AAC' ;;\nesac\ncase \" $* \" in\n*' -c:v libx265 '*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	registry := ProbeTransformationRegistryV3(context.Background(), ffmpeg)
	if registry.Available(TransformationVideoToHEVCV3) {
		t.Fatal("HEVC transformation advertised despite failed encode smoke")
	}
	if registry.NeedsRefresh(time.Now().Add(time.Hour)) {
		t.Fatal("unsupported HEVC inventory should stay cached")
	}
}

func TestProbeTransformationRegistryV3HEVCTimeoutKeepsBaselineInventory(t *testing.T) {
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\ncase \"$2\" in\n-bsfs) : ;;\n-encoders) echo ' V....D libx264 H.264'; echo ' V....D libx265 HEVC'; echo ' A....D aac AAC' ;;\nesac\ncase \" $* \" in\n*' -c:v libx265 '*) exec sleep 30 ;;\nesac\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	registry, err := ProbeTransformationRegistryWithToneMapV3Result(context.Background(), ffmpeg, nil)
	if err != nil {
		t.Fatalf("optional HEVC timeout invalidated baseline inventory: %v", err)
	}
	if registry.Available(TransformationVideoToHEVCV3) || !registry.Available(TransformationVideoToH264V3) || !registry.Available(TransformationAudioToAACV3) {
		t.Fatalf("HEVC timeout changed baseline capabilities: %#v", registry.Advertised())
	}
	if registry.NeedsRefresh(time.Now()) || registry.refreshAfter.IsZero() {
		t.Fatal("incomplete optional probe must keep baseline capabilities cached briefly")
	}
	if registry.NeedsRefresh(registry.refreshAfter.Add(-time.Nanosecond)) || !registry.NeedsRefresh(registry.refreshAfter) {
		t.Fatal("incomplete HEVC probe did not become retryable at its expiry")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = ProbeTransformationRegistryWithToneMapV3Result(ctx, ffmpeg, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller cancellation was swallowed: %v", err)
	}

	// Recovery changes the fake encoder's behavior; expiry above uses an
	// explicit timestamp, so no test needs to wait for the retry interval.
	if err := os.WriteFile(ffmpeg, []byte(strings.Replace(script, "exec sleep 30", "exit 0", 1)), 0o755); err != nil {
		t.Fatal(err)
	}
	recovered, err := ProbeTransformationRegistryWithToneMapV3Result(context.Background(), ffmpeg, nil)
	if err != nil || !recovered.Available(TransformationVideoToHEVCV3) ||
		!recovered.Available(TransformationVideoToH264V3) || !recovered.Available(TransformationAudioToAACV3) ||
		recovered.NeedsRefresh(time.Now().Add(time.Hour)) {
		t.Fatalf("HEVC recovery did not restore a complete stable inventory: %v, %#v", err, recovered.Advertised())
	}
}

func TestProbeTransformationRegistryV3HEVCStartupFailureExpires(t *testing.T) {
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	// Drop execute permission after the last baseline probe. The optional
	// HEVC command then fails to start rather than returning an unsupported
	// encoder result from a running FFmpeg process.
	script := "#!/bin/sh\ncase \"$2\" in\n-encoders) echo ' V....D libx264 H.264'; echo ' V....D libx265 HEVC'; echo ' A....D aac AAC' ;;\nesac\ncase \" $* \" in\n*'anullsrc=r=8000:cl=5.1'*) chmod -x \"$0\" ;;\nesac\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	registry, err := ProbeTransformationRegistryWithToneMapV3Result(context.Background(), ffmpeg, nil)
	if err != nil || registry.Available(TransformationVideoToHEVCV3) || !registry.NeedsRefresh(time.Now().Add(time.Minute)) {
		t.Fatalf("optional process startup failure did not expire: %v, %#v", err, registry)
	}
}

func TestProbeTransformationRegistryV3MissingHEVCEncoderStaysCached(t *testing.T) {
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\ncase \"$2\" in\n-encoders) echo ' V....D libx264 H.264'; echo ' A....D aac AAC' ;;\nesac\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	registry, err := ProbeTransformationRegistryWithToneMapV3Result(context.Background(), ffmpeg, nil)
	if err != nil || registry.Available(TransformationVideoToHEVCV3) || registry.NeedsRefresh(time.Now().Add(time.Hour)) {
		t.Fatalf("missing optional encoder should remain a stable negative result: %v, %#v", err, registry)
	}
}

func hevcTranscodePlannerInputV3(allowHEVC, hlsHEVC, hevcAvailable bool) PlannerInputV3 {
	file := &models.MediaFile{
		ID: 42, FilePath: "/media/movie.mkv", Container: "mkv", CodecVideo: "av1", CodecAudio: "aac", Resolution: "1080p", Bitrate: 8_000, AudioChannels: 2,
		VideoTracks: []models.VideoTrack{{Codec: "av1", Profile: "Main", Width: 1920, Height: 1080, FrameRate: "24/1", Bitrate: 8_000, BitDepth: 8, VideoRange: "SDR"}},
		AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, Layout: "stereo"}},
	}
	request := validStartRequestV3()
	request.Capabilities.CodecsVideo = []string{"hevc"}
	request.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"Main"}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 10_000, Hardware: true}}
	hls := request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
	hls.Containers = []string{"hls"}
	hls.AudioDecodeCodecs = []string{"aac"}
	if hlsHEVC {
		hls.VideoCodecs = []string{"h264", "hevc"}
	} else {
		hls.VideoCodecs = []string{"h264"}
	}
	request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = hls
	registry := []TransformationSpecV3{
		{Name: TransformationAudioToAACV3, RecipeVersion: TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: TransformationVideoToH264V3, RecipeVersion: TransformationVideoToH264RecipeVersionV3, Available: true},
	}
	if hevcAvailable {
		registry = append(registry, TransformationSpecV3{Name: TransformationVideoToHEVCV3, RecipeVersion: TransformationVideoToHEVCRecipeVersionV3, Available: true})
	}
	return PlannerInputV3{Request: request, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, AllowHEVCEncoding: allowHEVC}, Registry: NewTransformationRegistryV3(registry)}
}
