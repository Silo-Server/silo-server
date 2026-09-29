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
		{5_000, 23.976, "h264", 1080},
		{2_000, 23.976, "h264", 720},
		{1_000, 23.976, "h264", 480},
		{20_000, 60, "h264", 1080},
		{10_000, 60, "h264", 1080},
		{5_000, 60, "h264", 720},
		{2_000, 60, "h264", 540},
		{1_000, 60, "h264", 480},
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

// Automatic streaming quality reads the same ladder as download presets,
// keeping a fifth of the bandwidth estimate as headroom.
func TestResolveQualityPolicyV3AutoUsesTheLadder(t *testing.T) {
	fitsBudget := SourceDescriptorV3{Width: 1920, Height: 1080, BitrateKbps: 5_000, FrameRate: 24}
	req := validStartRequestV3()
	req.QualityPreference = "auto"
	estimate := 7_000
	req.BandwidthEstimateKbps = &estimate
	if got := ResolveQualityPolicyV3(req, fitsBudget); !got.PreservesSource || got.RequiresTranscode {
		t.Fatalf("a source within the budget = %#v, want it preserved", got)
	}

	uhd := SourceDescriptorV3{Width: 3840, Height: 2160, BitrateKbps: 40_000, FrameRate: 23.976}
	scope := SourceDescriptorV3{Width: 3840, Height: 1600, BitrateKbps: 30_000, FrameRate: 23.976}
	uhd60 := SourceDescriptorV3{Width: 3840, Height: 2160, BitrateKbps: 40_000, FrameRate: 60}
	for _, tc := range []struct {
		name        string
		source      SourceDescriptorV3
		estimate    int
		wantW       int
		wantH       int
		wantLabel   string
		wantBitrate int
	}{
		{"7 Mbps reaches 1080p within its budget", uhd, 7_000, 1920, 1080, "1080p", 5_600},
		{"10 Mbps encodes 1080p at the class bitrate", uhd, 10_000, 1920, 1080, "1080p", 6_000},
		{"4 Mbps is 720p", uhd, 4_000, 1280, 720, "720p", 2_000},
		{"2 Mbps is 540p", uhd, 2_000, 960, 540, "540p", 1_600},
		{"1 Mbps is 480p", uhd, 1_000, 854, 480, "480p", 800},
		{"scope keeps its shape", scope, 10_000, 1920, 800, "800p", 6_000},
		{"60 fps drops a class", uhd60, 7_000, 1280, 720, "720p", 2_000},
		{"a source over the budget is re-encoded at its own size", SourceDescriptorV3{Width: 1920, Height: 1080, BitrateKbps: 8_000, FrameRate: 24}, 7_000, 1920, 1080, "1080p", 5_600},
		{"a scaled transcode never exceeds the source's bitrate", SourceDescriptorV3{Width: 3840, Height: 2160, VideoCodec: "hevc", BitrateKbps: 3_000, FrameRate: 24}, 10_000, 1920, 1080, "1080p", 5_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := validStartRequestV3()
			req.QualityPreference = "auto"
			req.BandwidthEstimateKbps = &tc.estimate
			got := ResolveQualityPolicyV3(req, tc.source)
			if got.Width != tc.wantW || got.Height != tc.wantH || got.Label != tc.wantLabel || got.BitrateKbps != tc.wantBitrate || !got.RequiresTranscode {
				t.Fatalf("auto at %d kbps = %dx%d %q %d kbps (transcode %v), want %dx%d %q %d kbps",
					tc.estimate, got.Width, got.Height, got.Label, got.BitrateKbps, got.RequiresTranscode, tc.wantW, tc.wantH, tc.wantLabel, tc.wantBitrate)
			}
		})
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
}
