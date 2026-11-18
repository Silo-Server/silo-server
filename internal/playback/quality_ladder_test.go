package playback

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

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
