package playback

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestLadderClassForBitrate(t *testing.T) {
	for _, tc := range []struct {
		kbps  int
		fps   float64
		codec string
		want  int
	}{
		// The download presets at film and video frame rates.
		{20_000, 23.976, "h264", 2160},
		{10_000, 23.976, "h264", 1080},
		{2_000, 23.976, "h264", 720},
		{20_000, 60, "h264", 1080},
		{5_000, 60, "h264", 720},
		// HEVC stretches the same budget further.
		{12_000, 24, "hevc", 2160},
		{1_000, 24, "hevc", 540},
		// Unknown frame rates are treated as <=30 fps.
		{5_000, 0, "h264", 1080},
		{1_199, 24, "h264", 480},
		{1_200, 24, "h264", 540},
	} {
		if got := LadderClassForBitrate(tc.kbps, tc.fps, tc.codec); got != tc.want {
			t.Errorf("LadderClassForBitrate(%d, %v, %s) = %d, want %d", tc.kbps, tc.fps, tc.codec, got, tc.want)
		}
	}
}

func TestFitLadderBox(t *testing.T) {
	for _, tc := range []struct {
		srcW, srcH, class int
		wantW, wantH      int
	}{
		{3840, 2160, 1080, 1920, 1080},
		{3840, 1600, 1080, 1920, 800}, // scope keeps its shape
		{3840, 1606, 720, 1276, 534},  // 536 would scale to 1282 wide
		{1920, 800, 720, 1276, 532},
		{1440, 1080, 720, 960, 720}, // 4:3 is bounded by height
		{1920, 1080, 1080, 1920, 1080},
		{1280, 720, 1080, 1280, 720},  // never enlarged
		{1080, 1920, 1080, 608, 1080}, // portrait
		{1918, 872, 540, 960, 436},
		{0, 0, 1080, 0, 0},
		{0, 480, 720, 0, 480},  // known only by height: never enlarged
		{0, 1080, 720, 0, 720}, // and scaled down to the class
	} {
		w, h := FitLadderBox(tc.srcW, tc.srcH, tc.class)
		if w != tc.wantW || h != tc.wantH {
			t.Errorf("FitLadderBox(%dx%d, %d) = %dx%d, want %dx%d", tc.srcW, tc.srcH, tc.class, w, h, tc.wantW, tc.wantH)
		}
	}
}

func ladderTestFile(width, height int, codec, frameRate string, bitrateKbps int) *models.MediaFile {
	return &models.MediaFile{
		CodecVideo: codec, Container: "mkv", Bitrate: bitrateKbps,
		VideoTracks: []models.VideoTrack{{Codec: codec, Width: width, Height: height, FrameRate: frameRate, BitDepth: 8, Bitrate: bitrateKbps}},
	}
}

// appleDownloadCaps mirrors the Apple client's download caps: a flat 1080p
// ceiling for old servers plus attested hardware decoders that reach 4K.
func appleDownloadCaps() ClientCapabilities {
	return ClientCapabilities{
		ClientFeatures: []string{FeatureSoftwareVideoDecodeV3},
		VideoEvidence:  EvidencePlatformAttestedV3,
		CodecsVideo:    []string{"h264", "hevc"},
		MaxResolution:  "1080p",
		VideoDecode: []VideoDecodeCapabilityV3{
			{Codec: "h264", BitDepths: []int{8}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, Hardware: true},
			{Codec: "hevc", BitDepths: []int{8, 10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, Hardware: true},
			{Codec: "h264", Profiles: []string{"high 10"}, BitDepths: []int{10}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 30},
		},
	}
}

func TestResolveDownloadTranscodeTarget(t *testing.T) {
	uhd := ladderTestFile(3840, 2160, "hevc", "24000/1001", 40_000)
	scope := ladderTestFile(3840, 1600, "hevc", "24000/1001", 30_000)
	hd := ladderTestFile(1920, 1080, "h264", "24000/1001", 8_000)
	smallWeb := ladderTestFile(1920, 1080, "h264", "24", 3_000)
	uhd60 := ladderTestFile(3840, 2160, "hevc", "60", 40_000)
	h264Only := ClientCapabilities{VideoEvidence: EvidencePlatformAttestedV3, CodecsVideo: []string{"h264"}, VideoDecode: []VideoDecodeCapabilityV3{
		{Codec: "h264", BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, Hardware: true},
	}}
	for _, tc := range []struct {
		name        string
		file        *models.MediaFile
		caps        ClientCapabilities
		kbps        int
		settings    DownloadTranscodeSettings
		wantCodec   string
		wantRes     string
		wantBitrate int
	}{
		{"no caps: 4K at 1 Mbps drops to 480p", uhd, ClientCapabilities{}, 1_000, DownloadTranscodeSettings{}, "h264", "480p", 1_000},
		{"Apple caps reach 4K at 20 Mbps", uhd, appleDownloadCaps(), 20_000, DownloadTranscodeSettings{}, "h264", "", 20_000},
		{"Apple caps: HEVC when allowed", uhd, appleDownloadCaps(), 10_000, DownloadTranscodeSettings{AllowHEVCEncoding: true}, "hevc", "1080p", 10_000},
		{"HEVC not attested stays H.264", uhd, h264Only, 10_000, DownloadTranscodeSettings{AllowHEVCEncoding: true}, "h264", "1080p", 10_000},
		{"device ceiling beats the ladder", uhd, h264Only, 20_000, DownloadTranscodeSettings{}, "h264", "1080p", 20_000},
		{"flat ceiling without detailed caps", uhd, ClientCapabilities{MaxResolution: "720p"}, 10_000, DownloadTranscodeSettings{}, "h264", "720p", 10_000},
		{"policy ceiling", uhd, appleDownloadCaps(), 20_000, DownloadTranscodeSettings{MaxHeight: 1080}, "h264", "1080p", 20_000},
		{"scope keeps the class label", scope, ClientCapabilities{}, 5_000, DownloadTranscodeSettings{}, "h264", "1080p", 5_000},
		{"60 fps drops a class", uhd60, ClientCapabilities{}, 5_000, DownloadTranscodeSettings{}, "h264", "720p", 5_000},
		{"source fits: no scale", hd, ClientCapabilities{}, 10_000, DownloadTranscodeSettings{}, "h264", "", 8_000},
		{"never above the source bitrate", smallWeb, ClientCapabilities{}, 10_000, DownloadTranscodeSettings{}, "h264", "", 3_000},
		{"HEVC source counts as more bits in H.264", ladderTestFile(1920, 1080, "hevc", "24", 3_000), ClientCapabilities{}, 10_000, DownloadTranscodeSettings{}, "h264", "", 5_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ResolveDownloadTranscodeTarget(tc.file, tc.caps, tc.kbps, tc.settings)
			if !ok {
				t.Fatal("no decoder for the download")
			}
			if got.CodecVideo != tc.wantCodec || got.Resolution != tc.wantRes || got.TargetBitrateKbps != tc.wantBitrate {
				t.Fatalf("target = codec %q res %q bitrate %d, want %q %q %d", got.CodecVideo, got.Resolution, got.TargetBitrateKbps, tc.wantCodec, tc.wantRes, tc.wantBitrate)
			}
			if got.Container != "mp4" || got.CodecAudio != "aac" || got.AudioTrackIndex != -1 {
				t.Fatalf("target container/audio = %+v", got)
			}
		})
	}
}

func TestDownloadScaleResolution(t *testing.T) {
	for _, tc := range []struct {
		file  *models.MediaFile
		class string
		want  string
	}{
		{ladderTestFile(3840, 1600, "hevc", "24", 30_000), "1080p", "800p"},
		{ladderTestFile(3840, 2160, "hevc", "24", 30_000), "540p", "540p"},
		{ladderTestFile(1920, 1080, "h264", "24", 8_000), "1080p", ""},
		{ladderTestFile(1920, 1080, "h264", "24", 8_000), "", ""},
		{&models.MediaFile{CodecVideo: "h264"}, "720p", "720p"},
		{ladderTestFile(1920, 1079, "h264", "24", 8_000), "", "1078p"},      // an odd frame still gets an even size
		{ladderTestFile(1917, 1080, "h264", "24", 8_000), "1080p", "1080p"}, // scale=-2 evens the width
		{ladderTestFile(1920, 1079, "h264", "24", 8_000), "1080p", "1078p"},
	} {
		if got := DownloadScaleResolution(tc.file, tc.class); got != tc.want {
			t.Errorf("DownloadScaleResolution(%dx?, %q) = %q, want %q", len(tc.file.VideoTracks), tc.class, got, tc.want)
		}
	}
}

// Exact-tier decoder facts beyond size bound the download: a decoder that
// cannot take the source's frame rate or the High profile is not chosen, a
// level-bound HEVC decoder keeps the encode on H.264, and the target bitrate
// stays within the chosen decoder's maximum.
func TestH264LevelFor(t *testing.T) {
	for _, tc := range []struct {
		w, h int
		fps  float64
		kbps int
		want int
	}{
		{1280, 720, 24, 2_000, 31},
		{1920, 1080, 24, 10_000, 40},
		{1920, 1080, 24, 20_000, 41}, // a 40 Mbit buffer outgrows level 4.0
		{1920, 1080, 24, 30_000, 41},
		{1920, 1080, 60, 10_000, 42},
		{3840, 2160, 24, 20_000, 51},
		{3840, 2160, 60, 20_000, 52},
	} {
		if got := h264LevelFor(tc.w, tc.h, tc.fps, tc.kbps); got != tc.want {
			t.Errorf("h264LevelFor(%dx%d@%v, %d) = %d, want %d", tc.w, tc.h, tc.fps, tc.kbps, got, tc.want)
		}
	}
}

func TestResolveDownloadTranscodeTargetHonorsDecoderLimits(t *testing.T) {
	uhd60 := ladderTestFile(3840, 2160, "hevc", "60", 40_000)
	uhd := ladderTestFile(3840, 2160, "hevc", "24", 40_000)
	exact := func(decoders ...VideoDecodeCapabilityV3) ClientCapabilities {
		return ClientCapabilities{VideoEvidence: EvidenceExactV3, CodecsVideo: []string{"h264", "hevc"}, VideoDecode: decoders}
	}
	for _, tc := range []struct {
		name        string
		file        *models.MediaFile
		caps        ClientCapabilities
		kbps        int
		hevc        bool
		wantCodec   string
		wantRes     string
		wantBitrate int
	}{
		{"a decoder too slow for 60 fps is skipped", uhd60, exact(
			VideoDecodeCapabilityV3{Codec: "h264", BitDepths: []int{8}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 30, Hardware: true},
			VideoDecodeCapabilityV3{Codec: "h264", BitDepths: []int{8}, MaxWidth: 1280, MaxHeight: 720, MaxFrameRate: 60, Hardware: true},
		), 20_000, false, "h264", "720p", 20_000},
		{"a Baseline-only decoder is not used for High output", uhd, exact(
			VideoDecodeCapabilityV3{Codec: "h264", Profiles: []string{"baseline"}, BitDepths: []int{8}, MaxWidth: 3840, MaxHeight: 2160, Hardware: true},
			VideoDecodeCapabilityV3{Codec: "h264", Profiles: []string{"baseline", "main", "high"}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, Hardware: true},
		), 20_000, false, "h264", "1080p", 20_000},
		{"a level-bound HEVC decoder keeps H.264", uhd, exact(
			VideoDecodeCapabilityV3{Codec: "h264", BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, Hardware: true},
			VideoDecodeCapabilityV3{Codec: "hevc", Levels: []int{150}, BitDepths: []int{8}, MaxWidth: 3840, MaxHeight: 2160, Hardware: true},
		), 10_000, true, "h264", "1080p", 10_000},
		{"the decoder's bitrate limit caps the target", uhd, exact(
			VideoDecodeCapabilityV3{Codec: "h264", BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxBitrateKbps: 8_000, Hardware: true},
		), 10_000, false, "h264", "1080p", 8_000},
		{"a low decoder bitrate limit also lowers the class", uhd, exact(
			VideoDecodeCapabilityV3{Codec: "h264", BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxBitrateKbps: 3_000, Hardware: true},
		), 10_000, false, "h264", "720p", 3_000},
		{"an H.264 level limit bounds the size and rate", uhd, exact(
			VideoDecodeCapabilityV3{Codec: "h264", Levels: []int{31}, BitDepths: []int{8}, MaxWidth: 3840, MaxHeight: 2160, Hardware: true},
		), 10_000, false, "h264", "720p", 8_750},
		{"a decoder too slow for the frame rate still bounds the size", uhd60, exact(
			VideoDecodeCapabilityV3{Codec: "h264", BitDepths: []int{8}, MaxWidth: 1280, MaxHeight: 720, MaxFrameRate: 30, Hardware: true},
		), 20_000, false, "h264", "720p", 20_000},
		{"the decoder reaching the best class wins over the largest", uhd, exact(
			VideoDecodeCapabilityV3{Codec: "h264", BitDepths: []int{8}, MaxWidth: 3840, MaxHeight: 2160, MaxBitrateKbps: 1_000, Hardware: true},
			VideoDecodeCapabilityV3{Codec: "h264", BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxBitrateKbps: 20_000, Hardware: true},
		), 10_000, false, "h264", "1080p", 10_000},
		{"a platform-attested software decoder keeps its profile list", uhd, ClientCapabilities{
			VideoEvidence: EvidencePlatformAttestedV3, ClientFeatures: []string{FeatureSoftwareVideoDecodeV3}, CodecsVideo: []string{"h264"},
			VideoDecode: []VideoDecodeCapabilityV3{
				{Codec: "h264", Profiles: []string{"baseline"}, BitDepths: []int{8}, MaxWidth: 3840, MaxHeight: 2160},
				{Codec: "h264", Profiles: []string{"high"}, BitDepths: []int{8}, MaxWidth: 1280, MaxHeight: 720},
			},
		}, 20_000, false, "h264", "720p", 20_000},
		{"the H.264 level is checked at the bitrate the encoder gets", ladderTestFile(1920, 1080, "h264", "24", 3_000), exact(
			VideoDecodeCapabilityV3{Codec: "h264", Levels: []int{40}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, Hardware: true},
		), 20_000, false, "h264", "", 3_000},
		{"an odd source is checked at the even frame it encodes to", ladderTestFile(1917, 1080, "hevc", "24", 8_000), exact(
			VideoDecodeCapabilityV3{Codec: "h264", BitDepths: []int{8}, MaxWidth: 1917, MaxHeight: 1080, Hardware: true},
		), 20_000, false, "h264", "720p", 13_333},
		{"HEVC-only caps use HEVC when the server allows it", uhd, exact(
			VideoDecodeCapabilityV3{Codec: "hevc", BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, Hardware: true},
		), 10_000, true, "hevc", "1080p", 10_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ResolveDownloadTranscodeTarget(tc.file, tc.caps, tc.kbps, DownloadTranscodeSettings{AllowHEVCEncoding: tc.hevc})
			if !ok {
				t.Fatal("no decoder for the download")
			}
			if got.CodecVideo != tc.wantCodec || got.Resolution != tc.wantRes || got.TargetBitrateKbps != tc.wantBitrate {
				t.Fatalf("target = codec %q res %q bitrate %d, want %q %q %d", got.CodecVideo, got.Resolution, got.TargetBitrateKbps, tc.wantCodec, tc.wantRes, tc.wantBitrate)
			}
		})
	}
}

// Strict caps that attest no decoder for any codec the server may encode
// leave nothing to convert to, rather than an H.264 file the device never
// claimed to play.
func TestResolveDownloadTranscodeTargetWithoutAttestedDecoder(t *testing.T) {
	caps := ClientCapabilities{VideoEvidence: EvidenceExactV3, CodecsVideo: []string{"hevc"}, VideoDecode: []VideoDecodeCapabilityV3{
		{Codec: "hevc", BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, Hardware: true},
	}}
	if target, ok := ResolveDownloadTranscodeTarget(ladderTestFile(3840, 2160, "hevc", "24", 40_000), caps, 10_000, DownloadTranscodeSettings{}); ok {
		t.Fatalf("HEVC-only caps without HEVC encoding resolved %+v", target)
	}
	// A decoder smaller than the smallest class takes no ladder output.
	tiny := ClientCapabilities{VideoEvidence: EvidenceExactV3, CodecsVideo: []string{"h264"}, VideoDecode: []VideoDecodeCapabilityV3{
		{Codec: "h264", BitDepths: []int{8}, MaxWidth: 640, MaxHeight: 360, Hardware: true},
	}}
	if target, ok := ResolveDownloadTranscodeTarget(ladderTestFile(1920, 1080, "h264", "24", 8_000), tiny, 1_000, DownloadTranscodeSettings{}); ok {
		t.Fatalf("a 640x360 decoder resolved %+v", target)
	}
}

// Auto is best-effort direct play: a source the limits allow is sent as-is.
// Without a measured estimate, or on a metered link alone, auto assumes the
// link can carry the source; an estimate or cap only lowers a source the link
// cannot carry, at any resolution; and a fixed resolution is the viewer's
// explicit choice that never reads the estimate.
func TestResolveQualityPolicyV3PreservesASourceTheLimitsAllow(t *testing.T) {
	uhd := SourceDescriptorV3{Width: 3840, Height: 2160, BitrateKbps: 40_000, FrameRate: 23.976}
	for _, tc := range []struct {
		name       string
		quality    string
		metered    bool
		estimate   int
		capKbps    int
		source     SourceDescriptorV3
		wantReason string // checked when set
	}{
		{"auto on a metered link without an estimate", "auto", true, 0, 0, uhd, "quality_auto_source"},
		{"a 3 Mbps 1080p film on 6 Mbps", "auto", false, 6_000, 0, SourceDescriptorV3{Width: 1920, Height: 1080, BitrateKbps: 3_000, VideoCodec: "h264", FrameRate: 24}, "quality_auto_source"},
		{"a 3 Mbps HEVC 4K film on 10 Mbps", "auto", false, 10_000, 0, SourceDescriptorV3{Width: 3840, Height: 2160, BitrateKbps: 3_000, VideoCodec: "hevc", FrameRate: 24}, "quality_auto_source"},
		{"a 5 Mbps source within a 7 Mbps estimate's budget", "auto", false, 7_000, 0, SourceDescriptorV3{Width: 1920, Height: 1080, BitrateKbps: 5_000, FrameRate: 24}, ""},
		{"a 3 Mbps source under a 6 Mbps cap", "auto", false, 0, 6_000, SourceDescriptorV3{Width: 1920, Height: 1080, BitrateKbps: 3_000, FrameRate: 24}, ""},
		{"1080p preference on a 2 Mbps estimate keeps a 30 Mbps source", "1080p", false, 2_000, 0, SourceDescriptorV3{Width: 1920, Height: 1080, BitrateKbps: 30_000, VideoCodec: "h264", FrameRate: 24}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := validStartRequestV3()
			req.QualityPreference = tc.quality
			req.Metered = tc.metered
			if tc.estimate > 0 {
				req.BandwidthEstimateKbps = &tc.estimate
			}
			if tc.capKbps > 0 {
				req.BandwidthCapKbps = &tc.capKbps
			}
			got := ResolveQualityPolicyV3(req, tc.source)
			if !got.PreservesSource || got.RequiresTranscode || tc.wantReason != "" && got.Reason != tc.wantReason {
				t.Fatalf("quality = %#v, want the source as-is (reason %q)", got, tc.wantReason)
			}
		})
	}
}

// "original" and its spellings are legacy aliases of auto.
func TestResolveQualityPolicyV3OriginalIsAuto(t *testing.T) {
	uhd := SourceDescriptorV3{Width: 3840, Height: 2160, BitrateKbps: 40_000, FrameRate: 23.976}
	for _, preference := range []string{"original", " Original ", "source", "max"} {
		if got, changed := NormalizeQualityV3(preference); got != "auto" || changed {
			t.Fatalf("NormalizeQualityV3(%q) = %q, %v, want auto without a warning", preference, got, changed)
		}
		req := validStartRequestV3()
		req.QualityPreference = preference
		req.Capabilities.MaxResolution = "1080p"
		got := ResolveQualityPolicyV3(req, uhd)
		if got.Height != 1080 || !got.RequiresTranscode || got.Reason != "quality_device_limit" {
			t.Fatalf("%q under a 1080p device = %#v, want the device limit auto applies", preference, got)
		}
	}
}

// Automatic streaming quality re-encodes a source over a bitrate cap or over
// an estimate's budget at a class of the same ladder as download presets. An
// estimate keeps a fifth of itself as headroom when choosing the class. A cap
// keeps none: it earns the tallest class whose encode bitrate fits within it,
// so a cap between two classes gets the lower one (#2643: every cap from 2500
// to 6249 kbps used to play 720p). The cap is a hard delivery ceiling on the
// bitrate too.
func TestResolveQualityPolicyV3AutoUsesTheLadder(t *testing.T) {
	uhd := SourceDescriptorV3{Width: 3840, Height: 2160, BitrateKbps: 40_000, FrameRate: 23.976}
	scope := SourceDescriptorV3{Width: 3840, Height: 1600, BitrateKbps: 30_000, FrameRate: 23.976}
	uhd60 := SourceDescriptorV3{Width: 3840, Height: 2160, BitrateKbps: 40_000, FrameRate: 60}
	hd8M := SourceDescriptorV3{Width: 1920, Height: 1080, BitrateKbps: 8_000, FrameRate: 24}
	hd12M := SourceDescriptorV3{VideoCodec: "h264", Width: 1920, Height: 1080, BitrateKbps: 12_000, FrameRate: 23.976}
	for _, tc := range []struct {
		name        string
		source      SourceDescriptorV3
		isCap       bool // the limit is a bandwidth cap, otherwise an estimate
		kbps        int
		wantW       int
		wantH       int
		wantLabel   string
		wantBitrate int
	}{
		{"cap: 7 Mbps reaches 1080p", uhd, true, 7_000, 1920, 1080, "1080p", 6_000},
		{"cap: 10 Mbps encodes 1080p at the class bitrate", uhd, true, 10_000, 1920, 1080, "1080p", 6_000},
		{"cap: 4 Mbps is 720p", uhd, true, 4_000, 1280, 720, "720p", 2_000},
		{"cap: 2 Mbps is 720p", uhd, true, 2_000, 1280, 720, "720p", 2_000},
		{"cap: 1 Mbps is 480p", uhd, true, 1_000, 854, 480, "480p", 1_000},
		{"cap: 20 Mbps reaches 2160p", uhd, true, 20_000, 3840, 2160, "2160p", 20_000},
		{"cap: just under 2160p is 1080p", uhd, true, 19_999, 1920, 1080, "1080p", 6_000},
		{"cap: 6249 kbps is 1080p", hd12M, true, 6_249, 1920, 1080, "1080p", 6_000},
		{"cap: 6 Mbps is 1080p", hd12M, true, 6_000, 1920, 1080, "1080p", 6_000},
		{"cap: just under 1080p is 720p", hd12M, true, 5_999, 1280, 720, "720p", 2_000},
		{"cap: 3 Mbps is 720p", hd12M, true, 3_000, 1280, 720, "720p", 2_000},
		{"cap: 2500 kbps is 720p", hd12M, true, 2_500, 1280, 720, "720p", 2_000},
		{"cap: just under 720p is 540p", hd12M, true, 1_999, 960, 540, "540p", 1_800},
		{"cap: 1800 kbps is 540p", hd12M, true, 1_800, 960, 540, "540p", 1_800},
		{"cap: just under 540p is 480p", hd12M, true, 1_799, 854, 480, "480p", 1_500},
		{"cap: 1500 kbps is 480p", hd12M, true, 1_500, 854, 480, "480p", 1_500},
		{"cap: under 480p lowers the bitrate", hd12M, true, 1_499, 854, 480, "480p", 1_499},
		{"cap: scope keeps its shape", scope, true, 10_000, 1920, 800, "800p", 6_000},
		{"cap: a cropped source shorter than the cap's class fits that class", SourceDescriptorV3{Width: 1920, Height: 600, BitrateKbps: 12_000, FrameRate: 24}, true, 2_000, 1280, 400, "400p", 2_000},
		{"cap: 60 fps keeps the class the cap pays for", uhd60, true, 7_000, 1920, 1080, "1080p", 6_000},
		{"cap: a source over the cap is re-encoded at its own size", hd8M, true, 7_000, 1920, 1080, "1080p", 6_000},
		{"cap: a 720p source over the cap keeps the 720p bitrate", SourceDescriptorV3{Width: 1280, Height: 720, BitrateKbps: 8_000, FrameRate: 24}, true, 7_000, 1280, 720, "720p", 2_000},
		{"cap: an odd source frame is re-encoded at an even size", SourceDescriptorV3{Width: 1920, Height: 1079, BitrateKbps: 8_000, FrameRate: 24}, true, 7_000, 1918, 1078, "1078p", 6_000},
		{"estimate: 7 Mbps reaches 1080p within its budget", uhd, false, 7_000, 1920, 1080, "1080p", 5_600},
		{"estimate: 10 Mbps encodes 1080p at the class bitrate", uhd, false, 10_000, 1920, 1080, "1080p", 6_000},
		{"estimate: 2 Mbps is 540p", uhd, false, 2_000, 960, 540, "540p", 1_600},
		{"estimate: 1 Mbps is 480p", uhd, false, 1_000, 854, 480, "480p", 800},
		{"estimate: a source over the budget is re-encoded at its own size", hd8M, false, 7_000, 1920, 1080, "1080p", 5_600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := validStartRequestV3()
			req.QualityPreference = "auto"
			if tc.isCap {
				req.BandwidthCapKbps = &tc.kbps
			} else {
				req.BandwidthEstimateKbps = &tc.kbps
			}
			got := ResolveQualityPolicyV3(req, tc.source)
			if got.Width != tc.wantW || got.Height != tc.wantH || got.Label != tc.wantLabel || got.BitrateKbps != tc.wantBitrate || !got.RequiresTranscode {
				t.Fatalf("auto under a %d kbps limit = %dx%d %q %d kbps (transcode %v), want %dx%d %q %d kbps",
					tc.kbps, got.Width, got.Height, got.Label, got.BitrateKbps, got.RequiresTranscode, tc.wantW, tc.wantH, tc.wantLabel, tc.wantBitrate)
			}
		})
	}
}

// A plain resolution under a cap keeps its resolution while the cap reaches
// that resolution's lowest menu rung and lowers only the bitrate, as the
// explicit rung does: 1080p at 3000 kbps is 1080p Low. It steps down only when
// the cap is below every rung of the resolution, and a source under the cap
// still plays as-is.
func TestResolveQualityPolicyV3PlainResolutionKeepsItsRungUnderACap(t *testing.T) {
	hd := SourceDescriptorV3{VideoCodec: "h264", Width: 1920, Height: 1080, BitrateKbps: 15_000, FrameRate: 24}
	uhd := SourceDescriptorV3{VideoCodec: "hevc", Width: 3840, Height: 2160, BitrateKbps: 40_000, FrameRate: 24}
	scope := SourceDescriptorV3{VideoCodec: "h264", Width: 1920, Height: 800, BitrateKbps: 15_000, FrameRate: 24}
	hd720 := SourceDescriptorV3{VideoCodec: "h264", Width: 1280, Height: 720, BitrateKbps: 5_000, FrameRate: 24}
	for _, tc := range []struct {
		name        string
		preference  string
		source      SourceDescriptorV3
		capKbps     int
		wantLabel   string
		wantBitrate int
	}{
		{"1080p at 1080p Low's bitrate", "1080p", hd, 3_000, "1080p", 3_000},
		{"1080p just below 1080p Low steps to 720p", "1080p", hd, 2_999, "720p", 2_000},
		{"1080p at 1080p Medium's bitrate", "1080p", hd, 6_000, "1080p", 6_000},
		{"1080p at 720p Low's bitrate", "1080p", hd, 1_500, "720p", 1_500},
		{"1080p below every 720p rung is 480p at the cap", "1080p", hd, 1_000, "480p", 1_000},
		{"1080p from a 4K source", "1080p", uhd, 3_000, "1080p", 3_000},
		{"2160p below 4K Low steps to 1080p", "2160p", uhd, 8_000, "1080p", 6_000},
		{"2160p at 4K Low", "2160p", uhd, 10_000, "2160p", 10_000},
		{"720p at 720p Low's bitrate", "720p", hd, 1_500, "720p", 1_500},
		{"a cropped source keeps its size", "1080p", scope, 3_000, "800p", 3_000},
		{"a cropped source wider than the capped class fits its box", "1080p", SourceDescriptorV3{VideoCodec: "h264", Width: 1920, Height: 600, BitrateKbps: 8_000, FrameRate: 24}, 1_500, "400p", 1_500},
		{"a 720p source keeps 720p", "1080p", hd720, 3_000, "720p", 2_000},
		{"a 720p source below 720p Low is 480p", "1080p", hd720, 1_000, "480p", 1_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := validStartRequestV3()
			req.QualityPreference = tc.preference
			req.BandwidthCapKbps = &tc.capKbps
			got := ResolveQualityPolicyV3(req, tc.source)
			if got.Label != tc.wantLabel || got.BitrateKbps != tc.wantBitrate || !got.RequiresTranscode || got.Reason != decisionReasonBandwidthCapV3 {
				t.Fatalf("%s under a %d kbps cap = %q %d kbps (transcode %v, reason %s), want %q %d kbps capped",
					tc.preference, tc.capKbps, got.Label, got.BitrateKbps, got.RequiresTranscode, got.Reason, tc.wantLabel, tc.wantBitrate)
			}
		})
	}

	// The plain preference and its explicit 1080p Low rung plan the same encode.
	req := validStartRequestV3()
	capKbps := 3_000
	req.BandwidthCapKbps = &capKbps
	req.QualityPreference = "1080p"
	plain := ResolveQualityPolicyV3(req, hd)
	req.QualityPreference = QualityRung1080pLowV3
	rung := ResolveQualityPolicyV3(req, hd)
	if plain.Height != rung.Height || plain.Width != rung.Width || plain.BitrateKbps != rung.BitrateKbps {
		t.Fatalf("1080p under a 3000 kbps cap = %dx%d %d kbps, 1080p Low = %dx%d %d kbps; want the same encode",
			plain.Width, plain.Height, plain.BitrateKbps, rung.Width, rung.Height, rung.BitrateKbps)
	}

	// A source under the cap, or a resolution whose class bitrate fits it,
	// is not capped.
	fits := SourceDescriptorV3{VideoCodec: "h264", Width: 1920, Height: 1080, BitrateKbps: 2_500, FrameRate: 24}
	req.QualityPreference = "1080p"
	if got := ResolveQualityPolicyV3(req, fits); !got.PreservesSource || got.Reason == decisionReasonBandwidthCapV3 {
		t.Fatalf("a 2.5 Mbps source under a 3 Mbps cap = %#v, want it preserved", got)
	}
	req.QualityPreference = "720p"
	if got := ResolveQualityPolicyV3(req, hd); got.Label != "720p" || got.BitrateKbps != 2_000 || got.Reason == decisionReasonBandwidthCapV3 {
		t.Fatalf("720p under a 3 Mbps cap = %#v, want the uncapped 720p class", got)
	}
}

// A source of unknown bitrate is sent as-is only when the estimate's budget
// covers its class's full bitrate; otherwise it is re-encoded at its own size.
func TestResolveQualityPolicyV3AutoUnknownSourceBitrate(t *testing.T) {
	for _, tc := range []struct {
		name         string
		source       SourceDescriptorV3
		estimate     int
		wantPreserve bool
		wantBitrate  int
	}{
		{"1080p at 7 Mbps is re-encoded within the budget", SourceDescriptorV3{Width: 1920, Height: 1080, FrameRate: 24}, 7_000, false, 5_600},
		{"a cropped 1080p-class film counts as 1080p", SourceDescriptorV3{Width: 1920, Height: 800, FrameRate: 24}, 7_000, false, 5_600},
		{"1080p at 10 Mbps is sent as-is", SourceDescriptorV3{Width: 1920, Height: 1080, FrameRate: 24}, 10_000, true, 0},
		{"720p at 7 Mbps is sent as-is", SourceDescriptorV3{Width: 1280, Height: 720, FrameRate: 24}, 7_000, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := validStartRequestV3()
			req.QualityPreference = "auto"
			req.BandwidthEstimateKbps = &tc.estimate
			got := ResolveQualityPolicyV3(req, tc.source)
			if got.PreservesSource != tc.wantPreserve || got.RequiresTranscode == tc.wantPreserve {
				t.Fatalf("preserve/transcode = %v/%v, want preserve %v", got.PreservesSource, got.RequiresTranscode, tc.wantPreserve)
			}
			if !tc.wantPreserve && (got.Width != tc.source.Width || got.Height != tc.source.Height || got.BitrateKbps != tc.wantBitrate) {
				t.Fatalf("re-encode = %dx%d at %d kbps, want the source size at %d kbps", got.Width, got.Height, got.BitrateKbps, tc.wantBitrate)
			}
		})
	}
}

// A scaled transcode never asks for more bits than the whole source used.
func TestResolveQualityPolicyV3ScaledTranscodeStaysUnderTheSourceBitrate(t *testing.T) {
	req := validStartRequestV3()
	req.QualityPreference = "auto"
	req.Capabilities.MaxResolution = "1080p"
	got := ResolveQualityPolicyV3(req, SourceDescriptorV3{Width: 3840, Height: 2160, VideoCodec: "hevc", BitrateKbps: 3_000, FrameRate: 24})
	if got.Height != 1080 || got.BitrateKbps != 5_000 || !got.RequiresTranscode {
		t.Fatalf("a 3 Mbps HEVC 4K source on a 1080p device = %dp at %d kbps (transcode %v), want 1080p at 5000 kbps", got.Height, got.BitrateKbps, got.RequiresTranscode)
	}
}

// An original-quality route that still transcodes keeps the source frame.
func TestOriginalQualityResultKeepsTheSourceHeight(t *testing.T) {
	got := originalQualityResultV3(SourceDescriptorV3{Width: 3840, Height: 1600})
	if got.Label != "1600p" || resolutionToScale(got.Label) != "scale=-2:1600" {
		t.Fatalf("original label = %q (scale %q), want 1600p", got.Label, resolutionToScale(got.Label))
	}
	if got := originalQualityResultV3(SourceDescriptorV3{}); got.Label != "" {
		t.Fatalf("unknown source height label = %q, want no scale", got.Label)
	}
	// An odd height still scales, to the even height 4:2:0 output needs.
	if got := originalQualityResultV3(SourceDescriptorV3{Width: 1920, Height: 1081}); got.Label != "1080p" || resolutionToScale(got.Label) != "scale=-2:1080" || got.Width != 1918 || got.Height != 1080 {
		t.Fatalf("odd source = %q %dx%d (scale %q), want 1080p 1918x1080", got.Label, got.Width, got.Height, resolutionToScale(got.Label))
	}
}

// A bandwidth cap is a hard ceiling: a source of unknown bitrate cannot be
// shown to fit it, so it is re-encoded under the cap even when its class fits.
func TestResolveQualityPolicyV3CapWithUnknownSourceBitrate(t *testing.T) {
	source := SourceDescriptorV3{VideoCodec: "h264", Width: 1920, Height: 1080, FrameRate: 24}
	for _, preference := range []string{"auto", QualityOriginalV3} {
		req := validStartRequestV3()
		req.QualityPreference = preference
		capKbps := 7_000
		req.BandwidthCapKbps = &capKbps
		got := ResolveQualityPolicyV3(req, source)
		if got.PreservesSource || !got.RequiresTranscode || got.BitrateKbps <= 0 || got.BitrateKbps > capKbps {
			t.Fatalf("%s under a 7 Mbps cap = %#v, want a transcode within the cap", preference, got)
		}
	}
	known := source
	known.BitrateKbps = 5_000
	req := validStartRequestV3()
	req.QualityPreference = "auto"
	capKbps := 7_000
	req.BandwidthCapKbps = &capKbps
	if got := ResolveQualityPolicyV3(req, known); !got.PreservesSource {
		t.Fatalf("a 5 Mbps source under a 7 Mbps cap = %#v, want it preserved", got)
	}
}

func TestLadderClassForSize(t *testing.T) {
	for _, tc := range []struct{ w, h, want int }{
		{1920, 1080, 1080}, {1920, 800, 1080}, {1280, 720, 720}, {1280, 960, 1080},
		{960, 540, 540}, {640, 360, 480}, {3840, 1600, 2160}, {7680, 4320, 2160},
		{0, 800, 1080}, {0, 0, 0},
	} {
		if got := ladderClassForSize(tc.w, tc.h); got != tc.want {
			t.Errorf("ladderClassForSize(%dx%d) = %d, want %d", tc.w, tc.h, got, tc.want)
		}
	}
}

func TestH264EquivalentKbps(t *testing.T) {
	for _, tc := range []struct {
		name     string
		srcKbps  int
		srcCodec string
		want     int
	}{
		{"an H.264 source keeps its rate", 12000, "h264", 12000},
		{"an HEVC source counts at its H.264-equivalent rate", 12000, "hevc", 20000},
		{"an unknown source rate", 0, "h264", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := H264EquivalentKbps(tc.srcKbps, tc.srcCodec); got != tc.want {
				t.Errorf("H264EquivalentKbps(%d, %q) = %d, want %d", tc.srcKbps, tc.srcCodec, got, tc.want)
			}
		})
	}
}

// Auto and an explicit resolution direct play whenever the device and the
// viewer's limits allow: the original file goes out byte for byte, and only a
// source the measured link cannot carry, or one above the chosen resolution,
// is converted.
func TestPlanPlaybackV3DirectPlaysWhateverTheLimitsAllow(t *testing.T) {
	remux1080 := &models.MediaFile{ID: 42, FilePath: "/media/remux.mkv", Container: "mkv", CodecVideo: "h264", CodecAudio: "aac", Resolution: "1080p", Bitrate: 30_000, AudioChannels: 2,
		VideoTracks: []models.VideoTrack{{Codec: "h264", Profile: "High", Level: 41, Width: 1920, Height: 1080, FrameRate: "24000/1001", Bitrate: 30_000, BitDepth: 8, VideoRange: "SDR"}},
		AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, Layout: "stereo"}}}
	web4K := &models.MediaFile{ID: 43, FilePath: "/media/web.mkv", Container: "mkv", CodecVideo: "hevc", CodecAudio: "aac", Resolution: "2160p", Bitrate: 15_000, AudioChannels: 2,
		VideoTracks: []models.VideoTrack{{Codec: "hevc", Profile: "Main", Level: 150, Width: 3840, Height: 2160, FrameRate: "24000/1001", Bitrate: 15_000, BitDepth: 8, VideoRange: "SDR"}},
		AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, Layout: "stereo"}}}
	for _, tc := range []struct {
		name       string
		file       *models.MediaFile
		quality    string
		estimate   int
		capKbps    int
		wantDirect bool
		wantHeight int
	}{
		{name: "auto without an estimate", file: remux1080, quality: "auto", wantDirect: true},
		{name: "auto on a link that cannot", file: remux1080, quality: "auto", estimate: 8_000, wantHeight: 1080},
		{name: "auto under a cap the source fits", file: remux1080, quality: "auto", capKbps: 40_000, wantDirect: true},
		{name: "the legacy original preference", file: web4K, quality: "original", wantDirect: true},
		{name: "1080p ignores a low estimate", file: remux1080, quality: "1080p", estimate: 2_000, wantDirect: true},
		{name: "1080p converts a 4K source", file: web4K, quality: "1080p", wantHeight: 1080},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := validStartRequestV3()
			req.FileID = tc.file.ID
			req.QualityPreference = tc.quality
			req.Capabilities.CodecsVideo = []string{"h264", "hevc"}
			req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{
				{Codec: "h264", Profiles: []string{"high"}, Levels: []int{51}, BitDepths: []int{8}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 100_000, Hardware: true},
				{Codec: "hevc", Profiles: []string{"main"}, Levels: []int{153}, BitDepths: []int{8}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 100_000, Hardware: true},
			}
			if tc.estimate > 0 {
				req.BandwidthEstimateKbps = &tc.estimate
			}
			if tc.capKbps > 0 {
				req.BandwidthCapKbps = &tc.capKbps
			}
			result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: tc.file, EffectiveFile: tc.file, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
			if result.Plan == nil {
				t.Fatalf("no plan: %s", ExplainPlannerResultV3(result))
			}
			if tc.wantDirect {
				if result.Plan.Delivery != DeliveryOriginalHTTPV3 {
					t.Fatalf("delivery = %s, want the original file: %s", result.Plan.Delivery, ExplainPlannerResultV3(result))
				}
				return
			}
			if result.Plan.Delivery != DeliveryTranscodeHLSV3 || result.Plan.EffectiveRecipe.Height == nil || *result.Plan.EffectiveRecipe.Height != tc.wantHeight {
				t.Fatalf("plan = %s, want a %dp transcode", ExplainPlannerResultV3(result), tc.wantHeight)
			}
		})
	}
}

// An encode the device already requires stays within a measured estimate even
// though the source itself would fit it: a 3 Mbps HEVC 4K source on a 1080p
// device and a 4 Mbps link drops to the class the estimate earns.
func TestResolveQualityPolicyV3DeviceEncodeStaysWithinTheEstimate(t *testing.T) {
	req := validStartRequestV3()
	req.QualityPreference = "auto"
	req.Capabilities.MaxResolution = "1080p"
	estimate := 4_000
	req.BandwidthEstimateKbps = &estimate
	got := ResolveQualityPolicyV3(req, SourceDescriptorV3{Width: 3840, Height: 2160, VideoCodec: "hevc", BitrateKbps: 3_000, FrameRate: 24})
	if !got.RequiresTranscode || got.Height != 720 || got.BitrateKbps > 3_200 {
		t.Fatalf("device-limited encode on a 4 Mbps link = %dp at %d kbps (transcode %v), want 720p within 3200 kbps", got.Height, got.BitrateKbps, got.RequiresTranscode)
	}
}
