package playback

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// TestToneMapFFmpegGraphsCoverSupportedExecutors verifies each executor emits its required graph.
func TestToneMapFFmpegGraphsCoverSupportedExecutors(t *testing.T) {
	tests := []struct {
		name       string
		mode       tonemap.Mode
		hwAccel    string
		filter     string
		sourceKind tonemap.SourceKind
		want       []string
	}{
		{name: "software PQ", mode: tonemap.ModeSoftware, hwAccel: "none", filter: "tonemapx", sourceKind: tonemap.SourcePQ, want: []string{"tonemapx=tonemap=bt2390", "color_trc=smpte2084", "libx264"}},
		{name: "software HLG fallback", mode: tonemap.ModeSoftware, hwAccel: "none", filter: "tonemap", sourceKind: tonemap.SourceHLG, want: []string{"tonemap=hable", "color_trc=arib-std-b67", "libx264"}},
		{name: "QSV", mode: tonemap.ModeHardware, hwAccel: "qsv", filter: "tonemap_opencl", sourceKind: tonemap.SourcePQ, want: []string{"-init_hw_device opencl=ocl@va", "tonemap_opencl", "hwmap=derive_device=qsv:mode=read+write", "h264_qsv"}},
		{name: "VAAPI", mode: tonemap.ModeHardware, hwAccel: "vaapi", filter: "tonemap_vaapi", sourceKind: tonemap.SourceHLG, want: []string{"tonemap_vaapi", "scale_vaapi", "h264_vaapi"}},
		{name: "NVENC", mode: tonemap.ModeHardware, hwAccel: "nvenc", filter: "tonemap_cuda", sourceKind: tonemap.SourcePQ, want: []string{"color_trc=smpte2084", "tonemap_cuda", "scale_cuda", "h264_nvenc"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := buildFFmpegArgs(TranscodeOpts{
				InputPath: "/media/hdr.mkv", OutputDir: t.TempDir(), TargetCodecVideo: "h264", TargetCodecAudio: "aac",
				TargetResolution: "1080p", HWAccel: tt.hwAccel, ToneMapPolicy: tonemap.PolicyHardwareThenSoftware,
				ToneMapMode: tt.mode, ToneMapSourceKind: tt.sourceKind, ToneMapFilter: tt.filter,
				ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3,
			})
			joined := strings.Join(args, " ")
			for _, token := range append(tt.want, "-color_range tv", "-color_primaries bt709", "-color_trc bt709") {
				if !strings.Contains(joined, token) {
					t.Fatalf("args missing %q: %s", token, joined)
				}
			}
			if tt.mode == tonemap.ModeSoftware && !strings.Contains(joined, "-colorspace bt709") {
				t.Fatalf("software args omit explicit output matrix: %s", joined)
			}
			if tt.mode == tonemap.ModeHardware && strings.Contains(joined, "-colorspace bt709") {
				t.Fatalf("hardware args request an incompatible software colorspace conversion: %s", joined)
			}
			if tt.mode == tonemap.ModeHardware && strings.Contains(joined, "-pix_fmt yuv420p") {
				t.Fatalf("hardware graph requested a software pixel format conversion: %s", joined)
			}
		})
	}
}

// TestUnsupportedHardwareToneMapDoesNotAppendEmptyFilterGraph verifies unsupported executors fail validation.
func TestUnsupportedHardwareToneMapDoesNotAppendEmptyFilterGraph(t *testing.T) {
	tests := []struct {
		name string
		opts TranscodeOpts
	}{
		{name: "scale", opts: TranscodeOpts{ToneMapMode: tonemap.ModeHardware, ToneMapSourceKind: tonemap.SourcePQ, HWAccel: HWAccelNone}},
		{name: "text subtitles", opts: TranscodeOpts{ToneMapMode: tonemap.ModeHardware, ToneMapSourceKind: tonemap.SourcePQ, HWAccel: HWAccelNone, SubtitleBurnIn: true, SubtitleTrackIndex: 0, SubtitleCodec: "ass"}},
		{name: "bitmap subtitles", opts: TranscodeOpts{ToneMapMode: tonemap.ModeHardware, ToneMapSourceKind: tonemap.SourcePQ, HWAccel: HWAccelNone, SubtitleBurnIn: true, SubtitleTrackIndex: 0, SubtitleCodec: "hdmv_pgs_subtitle"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := []string{"prefix"}
			got := appendVideoFilterArgs(append([]string(nil), original...), tt.opts)
			if len(got) != len(original) || got[0] != original[0] {
				t.Fatalf("unsupported hardware appended a filter graph: %v", got)
			}
		})
	}
}

// TestEveryToneMapSourceKindBuildsEveryExecutorGraph verifies graph construction covers the source matrix.
func TestEveryToneMapSourceKindBuildsEveryExecutorGraph(t *testing.T) {
	executors := []struct {
		name    string
		mode    tonemap.Mode
		hwAccel string
		filter  string
		encoder string
	}{
		{name: "software", mode: tonemap.ModeSoftware, hwAccel: "none", filter: tonemap.SoftwareFilterHable, encoder: "libx264"},
		{name: "qsv", mode: tonemap.ModeHardware, hwAccel: "qsv", filter: tonemap.HardwareFilterOpenCL, encoder: "h264_qsv"},
		{name: "vaapi", mode: tonemap.ModeHardware, hwAccel: "vaapi", filter: tonemap.HardwareFilterVAAPI, encoder: "h264_vaapi"},
		{name: "nvenc", mode: tonemap.ModeHardware, hwAccel: "nvenc", filter: tonemap.HardwareFilterCUDA, encoder: "h264_nvenc"},
	}
	for _, kind := range tonemap.AllSourceKinds() {
		for _, executor := range executors {
			t.Run(string(kind)+"/"+executor.name, func(t *testing.T) {
				args := buildPrepareFileArgs(TranscodeOpts{
					InputPath: "/media/source.mkv", SourceVideoBitDepth: 10, TargetCodecVideo: "h264", TargetCodecAudio: "aac",
					TargetResolution: "720p", ToneMapPolicy: tonemap.PolicyHardwareThenSoftware,
					ToneMapMode: executor.mode, ToneMapSourceKind: kind, ToneMapFilter: executor.filter,
					ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3, HWAccel: executor.hwAccel,
				}, "/tmp/output.mp4")
				joined := strings.Join(args, " ")
				for _, token := range []string{executor.encoder, "-color_range tv", "-color_primaries bt709", "-color_trc bt709", "sidedata=mode=delete:type=DOVI_RPU_BUFFER"} {
					if !strings.Contains(joined, token) {
						t.Fatalf("%s/%s graph missing %q: %s", kind, executor.name, token, joined)
					}
				}
				hasLuminanceToneMap := strings.Contains(joined, "tonemap=hable") || strings.Contains(joined, "tonemap_opencl") || strings.Contains(joined, "tonemap_vaapi") || strings.Contains(joined, "tonemap_cuda")
				if hasLuminanceToneMap == tonemap.IsSDRSource(kind) {
					t.Fatalf("%s/%s luminance tone-map decision is wrong: %s", kind, executor.name, joined)
				}
			})
		}
	}
}

// TestHardwareToneMapRemovesMetadataAfterHardwareFormatConversion verifies output metadata is ordered safely.
func TestHardwareToneMapRemovesMetadataAfterHardwareFormatConversion(t *testing.T) {
	tests := []struct {
		name    string
		hwAccel string
		filter  string
		before  string
	}{
		{name: "QSV", hwAccel: "qsv", filter: tonemap.HardwareFilterOpenCL, before: "hwmap=derive_device=qsv"},
		{name: "VAAPI", hwAccel: "vaapi", filter: tonemap.HardwareFilterVAAPI, before: "scale_vaapi"},
		{name: "NVENC", hwAccel: "nvenc", filter: tonemap.HardwareFilterCUDA, before: "scale_cuda"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			graph := strings.Join(buildFFmpegArgs(TranscodeOpts{
				InputPath: "/media/hdr.mkv", OutputDir: t.TempDir(), TargetCodecVideo: "h264", TargetCodecAudio: "aac",
				TargetResolution: "1080p", HWAccel: tt.hwAccel, ToneMapPolicy: tonemap.PolicyHardwareOnly,
				ToneMapMode: tonemap.ModeHardware, ToneMapSourceKind: tonemap.SourcePQ, ToneMapFilter: tt.filter,
				ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3,
			}), " ")
			conversion := strings.Index(graph, tt.before)
			metadata := strings.Index(graph, "sidedata=mode=delete")
			if conversion < 0 || metadata <= conversion {
				t.Fatalf("metadata removal precedes hardware format conversion: %s", graph)
			}
		})
	}
}

// TestToneMapGraphOrdersTextAndBitmapSubtitles verifies subtitle composition follows color conversion.
func TestToneMapGraphOrdersTextAndBitmapSubtitles(t *testing.T) {
	base := TranscodeOpts{
		InputPath: "/media/hdr.mkv", OutputDir: t.TempDir(), TargetCodecVideo: "h264", TargetCodecAudio: "aac", TargetResolution: "1080p",
		HWAccel: "none", ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware, ToneMapSourceKind: tonemap.SourcePQ,
		ToneMapFilter: "tonemapx", ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3, SubtitleBurnIn: true, SubtitleTrackIndex: 0,
	}
	textOpts := base
	textOpts.SubtitleCodec = "subrip"
	textGraph := strings.Join(buildFFmpegArgs(textOpts), " ")
	assertTokenOrder := func(graph string, tokens ...string) {
		t.Helper()
		last := -1
		for _, token := range tokens {
			index := strings.Index(graph, token)
			if index < 0 || index <= last {
				t.Fatalf("tokens %v are not ordered in %s", tokens, graph)
			}
			last = index
		}
	}
	assertTokenOrder(textGraph, "tonemapx=tonemap=bt2390", "scale=-2:1080", "subtitles=")

	bitmapOpts := base
	bitmapOpts.SubtitleCodec = "hdmv_pgs_subtitle"
	bitmapGraph := strings.Join(buildFFmpegArgs(bitmapOpts), " ")
	assertTokenOrder(bitmapGraph, "tonemapx=tonemap=bt2390", "overlay=eof_action=pass", "scale=-2:1080")
}

// TestSDRBaseGraphsBypassLuminanceToneMapping verifies SDR-compatible bases avoid needless luminance mapping.
func TestSDRBaseGraphsBypassLuminanceToneMapping(t *testing.T) {
	tests := []struct {
		name       string
		mode       tonemap.Mode
		hwAccel    string
		filter     string
		sourceKind tonemap.SourceKind
		want       []string
	}{
		{name: "software BT709", mode: tonemap.ModeSoftware, hwAccel: "none", filter: tonemap.SoftwareFilterHable, sourceKind: tonemap.SourceSDRBT709, want: []string{"color_primaries=bt709", "zscale=p=bt709"}},
		{name: "software BT2020", mode: tonemap.ModeSoftware, hwAccel: "none", filter: tonemap.SoftwareFilterBT2390, sourceKind: tonemap.SourceSDRBT2020, want: []string{"color_primaries=bt2020", "zscale=p=bt709"}},
		{name: "QSV", mode: tonemap.ModeHardware, hwAccel: "qsv", filter: tonemap.HardwareFilterOpenCL, sourceKind: tonemap.SourceSDRBT709, want: []string{"scale_vaapi=format=nv12", "hwmap=derive_device=qsv"}},
		{name: "VAAPI", mode: tonemap.ModeHardware, hwAccel: "vaapi", filter: tonemap.HardwareFilterVAAPI, sourceKind: tonemap.SourceSDRBT2020, want: []string{"scale_vaapi=format=nv12", "h264_vaapi"}},
		{name: "NVENC", mode: tonemap.ModeHardware, hwAccel: "nvenc", filter: tonemap.HardwareFilterCUDA, sourceKind: tonemap.SourceSDRBT2020, want: []string{"hwdownload,format=p010le", "zscale=p=bt709", "hwupload_cuda", "h264_nvenc"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := buildFFmpegArgs(TranscodeOpts{
				InputPath: "/media/dovi.mkv", OutputDir: t.TempDir(), SourceVideoBitDepth: 10,
				TargetCodecVideo: "h264", TargetCodecAudio: "aac", TargetResolution: "1080p", HWAccel: tt.hwAccel,
				ToneMapPolicy: tonemap.PolicyHardwareThenSoftware, ToneMapMode: tt.mode,
				ToneMapSourceKind: tt.sourceKind, ToneMapFilter: tt.filter, ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3,
			})
			joined := strings.Join(args, " ")
			for _, token := range tt.want {
				if !strings.Contains(joined, token) {
					t.Fatalf("args missing %q: %s", token, joined)
				}
			}
			for _, token := range []string{"tonemapx=", "tonemap=hable", "tonemap_vaapi", "tonemap_cuda"} {
				if strings.Contains(joined, token) {
					t.Fatalf("SDR fallback unexpectedly applies luminance tone mapping %q: %s", token, joined)
				}
			}
			if tt.hwAccel == transcodeHWNVENC && !strings.Contains(joined, "sidedata=mode=delete:type=DOVI_RPU_BUFFER") {
				t.Fatalf("NVENC SDR fallback did not remove Dolby Vision side data: %s", joined)
			}
		})
	}
}

// TestNVENCSDRBaseGraphsDownloadBeforeSubtitleComposition verifies CUDA frames return to software before overlays.
func TestNVENCSDRBaseGraphsDownloadBeforeSubtitleComposition(t *testing.T) {
	base := TranscodeOpts{
		InputPath: "/media/dovi.mkv", OutputDir: t.TempDir(), SourceVideoBitDepth: 10,
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", TargetResolution: "1080p", HWAccel: "nvenc",
		ToneMapPolicy: tonemap.PolicyHardwareOnly, ToneMapMode: tonemap.ModeHardware,
		ToneMapSourceKind: tonemap.SourceSDRBT2020, ToneMapFilter: tonemap.HardwareFilterCUDA,
		ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3, SubtitleBurnIn: true, SubtitleTrackIndex: 0,
	}
	for _, codec := range []string{"subrip", "hdmv_pgs_subtitle"} {
		t.Run(codec, func(t *testing.T) {
			opts := base
			opts.SubtitleCodec = codec
			graph := strings.Join(buildFFmpegArgs(opts), " ")
			download := strings.Index(graph, "hwdownload,format=p010le")
			convert := strings.Index(graph, "zscale=p=bt709")
			compose := strings.Index(graph, "subtitles=")
			if codec == "hdmv_pgs_subtitle" {
				compose = strings.Index(graph, "overlay=eof_action=pass")
			}
			upload := strings.LastIndex(graph, "hwupload_cuda")
			if download < 0 || convert <= download || compose <= convert || upload <= compose {
				t.Fatalf("unsafe filter order: %s", graph)
			}
			if !strings.Contains(graph, "sidedata=mode=delete:type=DOVI_RPU_BUFFER") {
				t.Fatalf("NVENC SDR subtitle graph did not remove Dolby Vision side data: %s", graph)
			}
		})
	}
}

func TestPrepareSubtitleFilterInputCreatesParserSafeAlias(t *testing.T) {
	outputDir := t.TempDir()
	inputPath := "/media/I'm here [1080p].mkv"
	opts := TranscodeOpts{
		InputPath:          inputPath,
		OutputDir:          outputDir,
		SubtitleBurnIn:     true,
		SubtitleTrackIndex: 2,
		SubtitleCodec:      "subrip",
		TargetCodecVideo:   "h264",
		TargetCodecAudio:   "aac",
	}

	if err := prepareSubtitleFilterInput(&opts); err != nil {
		t.Fatalf("prepareSubtitleFilterInput() error = %v", err)
	}
	wantAlias := filepath.Join(outputDir, subtitleFilterAliasName)
	if opts.subtitleFilterInputPath != wantAlias {
		t.Fatalf("subtitleFilterInputPath = %q, want %q", opts.subtitleFilterInputPath, wantAlias)
	}
	target, err := os.Readlink(wantAlias)
	if err != nil {
		t.Fatalf("read subtitle filter alias: %v", err)
	}
	if target != inputPath {
		t.Fatalf("subtitle filter alias target = %q, want %q", target, inputPath)
	}

	joined := strings.Join(buildFFmpegArgs(opts), " ")
	if !strings.Contains(joined, "-i "+inputPath) {
		t.Fatalf("media input should keep its original path: %s", joined)
	}
	if !strings.Contains(joined, "subtitles='"+wantAlias+"':si=2") {
		t.Fatalf("subtitle filter should use the parser-safe alias: %s", joined)
	}
}

func TestStartTranscodeRejectsUnvalidatedBitstreamFilter(t *testing.T) {
	_, err := StartTranscode(context.Background(), TranscodeOpts{
		VideoBitstreamFilter: "arbitrary_filter=1",
		TargetCodecVideo:     "copy",
	})
	if err == nil {
		t.Fatal("unvalidated bitstream filter was accepted")
	}
	_, err = StartTranscode(context.Background(), TranscodeOpts{
		VideoBitstreamFilter: DV7ToHDR10BitstreamFilter,
		TargetCodecVideo:     "h264",
	})
	if err == nil {
		t.Fatal("DV copy filter was accepted for encoded video")
	}
}

func TestBuildFFmpegArgsCopyVideoAppliesSampleEntry(t *testing.T) {
	tests := []struct {
		name  string
		entry string
		want  string
		not   string
	}{
		{name: "Dolby Vision", entry: VideoSampleEntryDVH1, want: "-c:v copy -tag:v dvh1 -strict unofficial"},
		{name: "HDR10", entry: VideoSampleEntryHVC1, want: "-c:v copy -tag:v hvc1", not: "-strict unofficial"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := strings.Join(buildFFmpegArgs(TranscodeOpts{
				InputPath: "/media/movie.mkv", OutputDir: t.TempDir(),
				TargetCodecVideo: "copy", TargetCodecAudio: "copy",
				VideoSampleEntry: tc.entry, SegmentDuration: 2,
			}), " ")
			if !strings.Contains(args, tc.want) || tc.not != "" && strings.Contains(args, tc.not) {
				t.Fatalf("args = %s", args)
			}
		})
	}
}

func TestBuildFFmpegArgsCopyVideoAcceptsNoncanonicalCodecCase(t *testing.T) {
	args := strings.Join(buildFFmpegArgs(TranscodeOpts{
		InputPath:        "/media/movie.mkv",
		OutputDir:        t.TempDir(),
		TargetCodecVideo: "COPY",
		TargetCodecAudio: "copy",
		VideoSampleEntry: VideoSampleEntryHVC1,
		SegmentDuration:  2,
	}), " ")
	if !strings.Contains(args, "-c:v copy -tag:v hvc1") {
		t.Fatalf("case-insensitive copy recipe did not apply its sample entry: %s", args)
	}
}

func TestStartTranscodeRejectsInvalidVideoSampleEntry(t *testing.T) {
	for _, opts := range []TranscodeOpts{
		{TargetCodecVideo: "copy", VideoSampleEntry: "dvhe"},
		{TargetCodecVideo: "h264", VideoSampleEntry: VideoSampleEntryDVH1},
	} {
		if _, err := StartTranscode(context.Background(), opts); err == nil {
			t.Fatalf("invalid recipe accepted: %+v", opts)
		}
	}
}

// TestValidateToneMapOptsRequiresFrozenSourceRevision verifies executable recipes bind stable source facts.
func TestValidateToneMapOptsRequiresFrozenSourceRevision(t *testing.T) {
	opts := TranscodeOpts{
		TargetCodecVideo: "h264", HWAccel: "qsv", ToneMapPolicy: tonemap.PolicyHardwareOnly,
		ToneMapMode: tonemap.ModeHardware, ToneMapSourceKind: tonemap.SourcePQ,
		ToneMapFilter: tonemap.HardwareFilterOpenCL, ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3,
	}
	if err := validateToneMapOpts(opts); err == nil {
		t.Fatal("tone-map recipe without a frozen source revision was accepted")
	}
	opts.ToneMapSourceRevision = tonemap.SourceRevision{MediaFileID: 1, FileSize: 1, StreamSignature: "stream"}
	if err := validateToneMapOpts(opts); err != nil {
		t.Fatalf("complete tone-map recipe rejected: %v", err)
	}
}

func TestBuildFFmpegArgs_QSVDropsSuperfastPreset(t *testing.T) {
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:         "/media/movie.mkv",
		OutputDir:         "/tmp/out",
		SessionID:         "session-1",
		TargetCodecVideo:  "h264",
		TargetCodecAudio:  "aac",
		SegmentDuration:   2,
		HWAccel:           "qsv",
		FastStart:         true,
		TargetResolution:  "1080p",
		TargetBitrateKbps: 2000,
	})

	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-preset superfast") {
		t.Fatalf("QSV args should not use superfast preset: %s", joined)
	}
	if !strings.Contains(joined, "-preset veryfast") {
		t.Fatalf("QSV args should use veryfast preset: %s", joined)
	}
}

func TestBuildFFmpegArgs_CPUPreservesSuperfastFastStart(t *testing.T) {
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:        "/media/movie.mkv",
		OutputDir:        "/tmp/out",
		SessionID:        "session-1",
		TargetCodecVideo: "h264",
		TargetCodecAudio: "aac",
		SegmentDuration:  2,
		HWAccel:          "none",
		FastStart:        true,
		TargetResolution: "1080p",
	})

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-preset superfast") {
		t.Fatalf("CPU args should preserve superfast preset: %s", joined)
	}
}

func TestBuildFFmpegArgsBoundsHLSManifestSize(t *testing.T) {
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:        "/media/long.mkv",
		OutputDir:        "/tmp/out",
		SessionID:        "session-long",
		TargetCodecVideo: "h264",
		TargetCodecAudio: "aac",
		SegmentDuration:  2,
		TotalDuration:    1_000_000,
	})

	joined := strings.Join(args, " ")
	want := "-hls_list_size 50000"
	if !strings.Contains(joined, want) {
		t.Fatalf("FFmpeg args missing %q: %s", want, joined)
	}
}

func TestBuildFFmpegArgs_CopyVideoFromStartUsesZeroBasedTimestamps(t *testing.T) {
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:        "/media/movie.mkv",
		OutputDir:        "/tmp/out",
		SessionID:        "session-copy",
		TargetCodecVideo: "copy",
		TargetCodecAudio: "aac",
		SegmentDuration:  2,
	})

	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-copyts") {
		t.Fatalf("copy-video from-start should not preserve source timestamps: %s", joined)
	}
	if !strings.Contains(joined, "-avoid_negative_ts make_zero") {
		t.Fatalf("copy-video from-start should zero-base timestamps: %s", joined)
	}
}

func TestBuildFFmpegArgs_CopyVideoAppliesValidatedBitstreamFilter(t *testing.T) {
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:            "/media/movie.mkv",
		OutputDir:            "/tmp/out",
		SessionID:            "session-dv7",
		TargetCodecVideo:     "copy",
		TargetCodecAudio:     "copy",
		VideoBitstreamFilter: DV7ToHDR10BitstreamFilter,
		SegmentDuration:      2,
	})

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-c:v copy -bsf:v dovi_rpu=strip=1") {
		t.Fatalf("copy-video args should apply the validated DV bitstream filter: %s", joined)
	}
}

func TestBuildFFmpegArgs_CopyVideoResumePreservesSourceTimestamps(t *testing.T) {
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:          "/media/movie.mkv",
		OutputDir:          "/tmp/out",
		SessionID:          "session-copy-resume",
		SeekSeconds:        478.0,
		StartSegmentNumber: 239,
		TargetCodecVideo:   "copy",
		TargetCodecAudio:   "aac",
		SegmentDuration:    2,
	})

	joined := strings.Join(args, " ")
	// Resume must preserve source timestamps so TFDT in seg_K matches
	// playlist time K*segDur (the EXT-X-START anchor). Without -copyts,
	// strict players (ATV / ExoPlayer) treat the TFDT/playlist mismatch
	// as a discontinuity and abort.
	if !strings.Contains(joined, "-copyts") {
		t.Fatalf("copy-video resume should preserve source timestamps: %s", joined)
	}
	if !strings.Contains(joined, "-avoid_negative_ts disabled") {
		t.Fatalf("copy-video resume should disable negative-ts adjustment: %s", joined)
	}
	if strings.Contains(joined, "-avoid_negative_ts make_zero") {
		t.Fatalf("copy-video resume must not zero-base timestamps (ATV resume regression): %s", joined)
	}
}

func TestBuildFFmpegArgs_CopyVideoSeekPreservesCodecCopy(t *testing.T) {
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:          "/media/movie.mkv",
		OutputDir:          "/tmp/out",
		SessionID:          "session-copy-seek",
		SeekSeconds:        240.86,
		StartSegmentNumber: 120,
		TargetCodecVideo:   "copy",
		TargetCodecAudio:   "aac",
		SegmentDuration:    2,
	})

	joined := strings.Join(args, " ")

	// Video must remain copy — no re-encoding.
	if !strings.Contains(joined, "-c:v copy") {
		t.Fatalf("copy-mode seek should preserve -c:v copy: %s", joined)
	}
	// Must not contain any video encoder.
	for _, enc := range []string{"h264_qsv", "h264_vaapi", "h264_nvenc", "libx264", "hevc_qsv", "hevc_nvenc"} {
		if strings.Contains(joined, enc) {
			t.Fatalf("copy-mode seek should not use encoder %s: %s", enc, joined)
		}
	}
	// Seek must be before input.
	ssIdx := strings.Index(joined, "-ss")
	iIdx := strings.Index(joined, "-i ")
	if ssIdx < 0 || iIdx < 0 || ssIdx > iIdx {
		t.Fatalf("seek (-ss) should appear before input (-i): %s", joined)
	}
	// Audio should be transcoded to AAC.
	if !strings.Contains(joined, "-c:a aac") {
		t.Fatalf("copy-mode seek should transcode audio to AAC: %s", joined)
	}
	// Should use -noaccurate_seek for copy video + transcode audio.
	if !strings.Contains(joined, "-noaccurate_seek") {
		t.Fatalf("copy-mode seek with audio transcode should use -noaccurate_seek: %s", joined)
	}
	// Should use fMP4 segments.
	if !strings.Contains(joined, "-hls_segment_type fmp4") {
		t.Fatalf("copy-mode should use fMP4 segments: %s", joined)
	}
	// Should have start_number for seek alignment.
	if !strings.Contains(joined, "-start_number 120") {
		t.Fatalf("copy-mode seek should set start_number: %s", joined)
	}
}

func TestBuildFFmpegArgs_MPEG2CopyVideoUsesMPEGTS(t *testing.T) {
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:        "/media/movie.mkv",
		OutputDir:        "/tmp/out",
		SessionID:        "session-mpeg2-copy",
		SourceVideoCodec: "mpeg2video",
		TargetCodecVideo: "copy",
		TargetCodecAudio: "aac",
		SegmentDuration:  2,
	})

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-c:v copy") {
		t.Fatalf("mpeg2 copy-mode should preserve video copy: %s", joined)
	}
	if !strings.Contains(joined, "-hls_segment_type mpegts") {
		t.Fatalf("mpeg2 copy-mode should use MPEG-TS HLS segments: %s", joined)
	}
	if !strings.Contains(joined, "seg_%05d.ts") {
		t.Fatalf("mpeg2 copy-mode should write .ts segments: %s", joined)
	}
	if strings.Contains(joined, "movflags=+frag_discont") {
		t.Fatalf("mpeg2 MPEG-TS copy-mode should not use fMP4 movflags: %s", joined)
	}
	for _, enc := range []string{"h264_qsv", "h264_vaapi", "h264_nvenc", "libx264", "hevc_qsv", "hevc_nvenc", "libx265"} {
		if strings.Contains(joined, enc) {
			t.Fatalf("mpeg2 copy-mode should not use encoder %s: %s", enc, joined)
		}
	}
}

func TestBuildFFmpegArgs_MPEG4Part2DisablesHardwareDecode(t *testing.T) {
	for _, hwAccel := range []string{"qsv", "vaapi"} {
		t.Run(hwAccel, func(t *testing.T) {
			args := buildFFmpegArgs(TranscodeOpts{
				InputPath:         "/media/xvid.avi",
				OutputDir:         "/tmp/out",
				SessionID:         "session-xvid",
				SourceVideoCodec:  "mpeg4",
				TargetCodecVideo:  "h264",
				TargetCodecAudio:  "aac",
				SegmentDuration:   2,
				HWAccel:           hwAccel,
				TargetResolution:  "420p",
				TargetBitrateKbps: 720,
			})

			joined := strings.Join(args, " ")
			for _, forbidden := range []string{
				"-hwaccel vaapi",
				"h264_qsv",
				"h264_vaapi",
				"scale_vaapi",
				"hwmap=derive_device=qsv",
			} {
				if strings.Contains(joined, forbidden) {
					t.Fatalf("mpeg4 part 2 source should use software transcode, found %q: %s", forbidden, joined)
				}
			}
			if !strings.Contains(joined, "-c:v libx264") {
				t.Fatalf("mpeg4 part 2 source should fall back to libx264: %s", joined)
			}
			if !strings.Contains(joined, "-vf scale=-2:420") {
				t.Fatalf("mpeg4 part 2 software fallback should preserve requested scaling: %s", joined)
			}
		})
	}
}

func TestRequiresSoftwareVideoDecodeForH264High10(t *testing.T) {
	tests := []struct {
		codec    string
		profile  string
		bitDepth int
		want     bool
	}{
		{codec: "h264", profile: "High 10", bitDepth: 10, want: true},
		{codec: "avc", profile: "Hi10P", bitDepth: 0, want: true},
		{codec: "h264", profile: "High", bitDepth: 10, want: true},
		{codec: "h264", profile: "High", bitDepth: 8, want: false},
		{codec: "hevc", profile: "Main 10", bitDepth: 10, want: false},
	}
	for _, test := range tests {
		if got := RequiresSoftwareVideoDecode(test.codec, test.profile, test.bitDepth); got != test.want {
			t.Errorf("RequiresSoftwareVideoDecode(%q, %q, %d) = %v, want %v", test.codec, test.profile, test.bitDepth, got, test.want)
		}
	}
}

func TestBuildFFmpegArgs_H264High10QSVUsesSoftwareDecodeUpload(t *testing.T) {
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:           "/media/high10.mkv",
		OutputDir:           "/tmp/out",
		SessionID:           "session-high10-pgs-sidecar",
		SourceVideoCodec:    "h264",
		SoftwareVideoDecode: true,
		TargetCodecVideo:    "h264",
		TargetCodecAudio:    "aac",
		SegmentDuration:     2,
		HWAccel:             "qsv",
		TargetResolution:    "720p",
		SubtitleTrackIndex:  2,
		SubtitleCodec:       "hdmv_pgs_subtitle",
	})

	joined := strings.Join(args, " ")
	for _, forbidden := range []string{"-hwaccel vaapi", "-hwaccel_output_format vaapi", "hwdownload"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("High 10 AVC must software-decode, found %q: %s", forbidden, joined)
		}
	}
	for _, required := range []string{
		"-init_hw_device vaapi=va:",
		"-init_hw_device qsv=qs@va",
		"-c:v h264_qsv",
		"-vf scale=-2:720,format=nv12,hwupload,hwmap=derive_device=qsv,format=qsv",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("High 10 QSV recipe missing %q: %s", required, joined)
		}
	}
	if strings.Contains(joined, "scale_vaapi") {
		t.Fatalf("High 10 sidecar route must scale software frames before upload: %s", joined)
	}
}

func TestBuildFFmpegArgs_QSVPromotesForcedSegmentKeyframesToIDR(t *testing.T) {
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:        "/media/movie.mkv",
		OutputDir:        "/tmp/out",
		SessionID:        "session-qsv-idr",
		SourceVideoCodec: "vp9",
		TargetCodecVideo: "h264",
		TargetCodecAudio: "aac",
		SegmentDuration:  2,
		HWAccel:          "qsv",
	})

	joined := strings.Join(args, " ")
	for _, required := range []string{
		"-force_key_frames expr:gte(t,n_forced*2)",
		"-g 60 -keyint_min 60",
		"-forced_idr 1",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("QSV segment boundary args missing %q: %s", required, joined)
		}
	}
}

func TestBuildFFmpegArgs_NonQSVDoesNotUseQSVForcedIDROption(t *testing.T) {
	for _, hwAccel := range []string{"vaapi", "nvenc", "none"} {
		args := buildFFmpegArgs(TranscodeOpts{
			InputPath:        "/media/movie.mkv",
			OutputDir:        "/tmp/out",
			SessionID:        "session-non-qsv-idr",
			SourceVideoCodec: "vp9",
			TargetCodecVideo: "h264",
			TargetCodecAudio: "aac",
			SegmentDuration:  2,
			HWAccel:          hwAccel,
		})
		if joined := strings.Join(args, " "); strings.Contains(joined, "-forced_idr") {
			t.Fatalf("%s args must not contain QSV-only -forced_idr: %s", hwAccel, joined)
		}
	}
}

func TestBuildFFmpegArgs_H264High10DerivesSoftwareDecodeFromSourceFacts(t *testing.T) {
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:           "/media/high10.mkv",
		OutputDir:           "/tmp/out",
		SessionID:           "session-high10-derived",
		SourceVideoCodec:    "h264",
		SourceVideoProfile:  "High 10",
		SourceVideoBitDepth: 10,
		TargetCodecVideo:    "h264",
		TargetCodecAudio:    "aac",
		SegmentDuration:     2,
		HWAccel:             "qsv",
		TargetResolution:    "720p",
	})

	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-hwaccel vaapi") || strings.Contains(joined, "-hwaccel_output_format vaapi") {
		t.Fatalf("High 10 source facts must suppress hardware decode args: %s", joined)
	}
	if !strings.Contains(joined, "-c:v h264_qsv") || !strings.Contains(joined, "format=nv12,hwupload") {
		t.Fatalf("High 10 source facts must retain the software-decode QSV upload path: %s", joined)
	}
}

func TestBuildFFmpegArgs_H264High10UploadsBeforeHardwareToneMap(t *testing.T) {
	for _, test := range []struct {
		name    string
		hwAccel string
	}{
		{name: "QSV", hwAccel: transcodeHWQSV},
		{name: "VAAPI", hwAccel: transcodeHWVAAPI},
	} {
		for _, subtitle := range []struct {
			name  string
			codec string
		}{
			{name: "none"},
			{name: "text", codec: "ass"},
			{name: "bitmap", codec: "hdmv_pgs_subtitle"},
		} {
			t.Run(test.name+"/"+subtitle.name, func(t *testing.T) {
				opts := TranscodeOpts{
					InputPath: "/media/high10-hdr.mkv", OutputDir: t.TempDir(), SessionID: "high10-tone-map",
					SourceVideoCodec: "h264", SourceVideoProfile: "High 10", SourceVideoBitDepth: 10,
					TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
					HWAccel: test.hwAccel, TargetResolution: "720p",
					ToneMapPolicy: tonemap.PolicyHardwareOnly, ToneMapMode: tonemap.ModeHardware,
					ToneMapSourceKind: tonemap.SourcePQ, ToneMapFilter: map[string]string{transcodeHWQSV: tonemap.HardwareFilterOpenCL, transcodeHWVAAPI: tonemap.HardwareFilterVAAPI}[test.hwAccel],
					ToneMapRecipeVersion:  TransformationHDRToSDRToneMapRecipeVersionV3,
					ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 1},
					SubtitleTrackIndex:    -1,
				}
				if subtitle.codec != "" {
					opts.SubtitleTrackIndex = 0
					opts.SubtitleBurnIn = true
					opts.SubtitleCodec = subtitle.codec
				}
				joined := strings.Join(buildFFmpegArgs(opts), " ")
				upload := strings.Index(joined, "format=p010le,hwupload")
				toneMapName := tonemap.HardwareFilterVAAPI
				if test.hwAccel == transcodeHWQSV {
					toneMapName = tonemap.HardwareFilterOpenCL
				}
				toneMap := strings.Index(joined, toneMapName)
				if upload < 0 || toneMap < 0 || upload >= toneMap {
					t.Fatalf("software frames were not uploaded before %s: %s", toneMapName, joined)
				}
				if strings.Contains(joined, "-hwaccel vaapi") || strings.Contains(joined, "-hwaccel_output_format vaapi") {
					t.Fatalf("High 10 source unexpectedly selected hardware decode: %s", joined)
				}
			})
		}
	}
}

func TestBuildFFmpegArgs_H264High10QSVASSBurnInUsesSoftwareFrames(t *testing.T) {
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:           "/media/high10.mkv",
		OutputDir:           "/tmp/out",
		SessionID:           "session-high10-ass",
		SourceVideoCodec:    "h264",
		SoftwareVideoDecode: true,
		TargetCodecVideo:    "h264",
		TargetCodecAudio:    "aac",
		SegmentDuration:     2,
		HWAccel:             "qsv",
		TargetResolution:    "720p",
		SubtitleTrackIndex:  0,
		SubtitleBurnIn:      true,
		SubtitleCodec:       "ass",
	})

	joined := strings.Join(args, " ")
	want := "-vf format=yuv420p,scale=-2:720,subtitles='/media/high10.mkv':si=0,format=nv12,hwupload,hwmap=derive_device=qsv,format=qsv"
	if !strings.Contains(joined, want) {
		t.Fatalf("High 10 ASS burn-in should render on software frames then upload %q: %s", want, joined)
	}
	if strings.Contains(joined, "hwdownload") || strings.Contains(joined, "-hwaccel vaapi") {
		t.Fatalf("High 10 ASS burn-in must not assume hardware-decoded input: %s", joined)
	}
}

func TestBuildFFmpegArgs_H264High10QSVBitmapBurnInUsesSoftwareFrames(t *testing.T) {
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:           "/media/high10.mkv",
		OutputDir:           "/tmp/out",
		SessionID:           "session-high10-pgs-burn",
		SourceVideoCodec:    "h264",
		SoftwareVideoDecode: true,
		TargetCodecVideo:    "h264",
		TargetCodecAudio:    "aac",
		SegmentDuration:     2,
		HWAccel:             "qsv",
		TargetResolution:    "720p",
		SubtitleTrackIndex:  2,
		SubtitleBurnIn:      true,
		SubtitleCodec:       "hdmv_pgs_subtitle",
	})

	joined := strings.Join(args, " ")
	want := "-filter_complex [0:v:0]format=yuv420p[vmain];[vmain][0:s:2]overlay=eof_action=pass,scale=-2:720,format=nv12,hwupload,hwmap=derive_device=qsv,format=qsv[vout]"
	if !strings.Contains(joined, want) {
		t.Fatalf("High 10 PGS burn-in should composite on software frames then upload %q: %s", want, joined)
	}
	if strings.Contains(joined, "overlay_vaapi") || strings.Contains(joined, "hwdownload") || strings.Contains(joined, "-hwaccel vaapi") {
		t.Fatalf("High 10 PGS burn-in must not assume hardware-decoded input: %s", joined)
	}
}

func TestBuildFFmpegArgs_BitmapBurnInCPUUsesOverlayFilterComplex(t *testing.T) {
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:          "/media/movie.mkv",
		OutputDir:          "/tmp/out",
		SessionID:          "session-pgs",
		SourceVideoCodec:   "h264",
		TargetCodecVideo:   "h264",
		TargetCodecAudio:   "aac",
		SegmentDuration:    2,
		HWAccel:            "none",
		TargetResolution:   "1080p",
		SubtitleTrackIndex: 2,
		SubtitleBurnIn:     true,
		SubtitleCodec:      "hdmv_pgs_subtitle",
	})

	joined := strings.Join(args, " ")
	// Overlay runs at native resolution first, then scales.
	want := "-filter_complex [0:v:0][0:s:2]overlay=eof_action=pass,scale=-2:1080[vout]"
	if !strings.Contains(joined, want) {
		t.Fatalf("bitmap burn-in should use overlay filter_complex %q: %s", want, joined)
	}
	// The graph output replaces the raw video stream mapping.
	if !strings.Contains(joined, "-map [vout]") {
		t.Fatalf("bitmap burn-in should map the filter graph output: %s", joined)
	}
	if strings.Contains(joined, "-map 0:v:0") {
		t.Fatalf("bitmap burn-in must not also map the raw video stream: %s", joined)
	}
	// -vf and -filter_complex on the same video stream is an ffmpeg error.
	if strings.Contains(joined, "-vf ") {
		t.Fatalf("bitmap burn-in must not emit -vf alongside -filter_complex: %s", joined)
	}
	if strings.Contains(joined, "subtitles=") {
		t.Fatalf("bitmap burn-in must not use the libass subtitles filter: %s", joined)
	}
	if !strings.Contains(joined, "-c:v libx264") {
		t.Fatalf("bitmap burn-in requires a video encode: %s", joined)
	}
}

func TestBuildFFmpegArgs_BitmapBurnInNoScaleKeepsNativeResolution(t *testing.T) {
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:          "/media/movie.mkv",
		OutputDir:          "/tmp/out",
		SessionID:          "session-pgs-native",
		TargetCodecVideo:   "h264",
		TargetCodecAudio:   "aac",
		SegmentDuration:    2,
		HWAccel:            "none",
		SubtitleTrackIndex: 0,
		SubtitleBurnIn:     true,
		SubtitleCodec:      "dvd_subtitle",
	})

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-filter_complex [0:v:0][0:s:0]overlay=eof_action=pass[vout]") {
		t.Fatalf("native-resolution bitmap burn-in should overlay without scaling: %s", joined)
	}
}

func TestBuildFFmpegArgs_BitmapBurnInVAAPICompositesOnGPU(t *testing.T) {
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:          "/media/movie.mkv",
		OutputDir:          "/tmp/out",
		SessionID:          "session-pgs-vaapi",
		SourceVideoCodec:   "h264",
		TargetCodecVideo:   "h264",
		TargetCodecAudio:   "aac",
		SegmentDuration:    2,
		HWAccel:            "vaapi",
		TargetResolution:   "720p",
		SubtitleTrackIndex: 1,
		SubtitleBurnIn:     true,
		SubtitleCodec:      "hdmv_pgs_subtitle",
	})

	joined := strings.Join(args, " ")
	// Only the subtitle bitmap is uploaded; the video stays on the VAAPI surface
	// and is composited with overlay_vaapi — no full-frame hwdownload roundtrip.
	want := "-filter_complex [0:s:1]format=bgra,hwupload[sub];[0:v:0][sub]overlay_vaapi=eof_action=pass,scale_vaapi=w=-2:h=720:format=nv12[vout]"
	if !strings.Contains(joined, want) {
		t.Fatalf("vaapi bitmap burn-in should composite on GPU %q: %s", want, joined)
	}
	if strings.Contains(joined, "hwdownload") {
		t.Fatalf("vaapi bitmap burn-in must not roundtrip the video through CPU: %s", joined)
	}
	if !strings.Contains(joined, "-map [vout]") {
		t.Fatalf("vaapi bitmap burn-in should map the filter graph output: %s", joined)
	}
	if strings.Contains(joined, "-vf ") {
		t.Fatalf("vaapi bitmap burn-in must not emit -vf: %s", joined)
	}
	if !strings.Contains(joined, "-c:v h264_vaapi") {
		t.Fatalf("vaapi bitmap burn-in should keep the hardware encoder: %s", joined)
	}
}

func TestBuildFFmpegArgs_BitmapBurnInQSVCompositesOnGPU(t *testing.T) {
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:          "/media/movie.mkv",
		OutputDir:          "/tmp/out",
		SessionID:          "session-pgs-qsv",
		SourceVideoCodec:   "h264",
		TargetCodecVideo:   "h264",
		TargetCodecAudio:   "aac",
		SegmentDuration:    2,
		HWAccel:            "qsv",
		TargetResolution:   "720p",
		SubtitleTrackIndex: 1,
		SubtitleBurnIn:     true,
		SubtitleCodec:      "hdmv_pgs_subtitle",
	})

	joined := strings.Join(args, " ")
	// GPU composite via overlay_vaapi, then map the VAAPI surface to QSV for the
	// encoder — the video never leaves hardware memory.
	want := "-filter_complex [0:s:1]format=bgra,hwupload[sub];[0:v:0][sub]overlay_vaapi=eof_action=pass,scale_vaapi=w=-2:h=720:format=nv12,hwmap=derive_device=qsv,format=qsv[vout]"
	if !strings.Contains(joined, want) {
		t.Fatalf("qsv bitmap burn-in should composite on GPU %q: %s", want, joined)
	}
	if strings.Contains(joined, "hwdownload") {
		t.Fatalf("qsv bitmap burn-in must not roundtrip the video through CPU: %s", joined)
	}
	if strings.Contains(joined, "-vf ") {
		t.Fatalf("qsv bitmap burn-in must not emit -vf: %s", joined)
	}
	if !strings.Contains(joined, "-c:v h264_qsv") {
		t.Fatalf("qsv bitmap burn-in should keep the hardware encoder: %s", joined)
	}
}

func TestBuildFFmpegArgs_BitmapBurnInNVENCStaysOnCPUOverlay(t *testing.T) {
	// overlay_cuda is unverified on the bundled ffmpeg, so NVENC keeps the safe
	// software roundtrip: download the frame, overlay on CPU, re-upload to CUDA.
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:          "/media/movie.mkv",
		OutputDir:          "/tmp/out",
		SessionID:          "session-pgs-nvenc",
		SourceVideoCodec:   "h264",
		TargetCodecVideo:   "h264",
		TargetCodecAudio:   "aac",
		SegmentDuration:    2,
		HWAccel:            "nvenc",
		TargetResolution:   "720p",
		SubtitleTrackIndex: 1,
		SubtitleBurnIn:     true,
		SubtitleCodec:      "hdmv_pgs_subtitle",
	})

	joined := strings.Join(args, " ")
	want := "-filter_complex [0:v:0]hwdownload,format=yuv420p[vmain];[vmain][0:s:1]overlay=eof_action=pass,scale=-2:720,format=nv12,hwupload_cuda[vout]"
	if !strings.Contains(joined, want) {
		t.Fatalf("nvenc bitmap burn-in should keep the CPU roundtrip %q: %s", want, joined)
	}
	if strings.Contains(joined, "overlay_vaapi") {
		t.Fatalf("nvenc bitmap burn-in must not use the VAAPI GPU overlay: %s", joined)
	}
}

func TestBuildFFmpegArgs_TextBurnInStillUsesSubtitlesFilter(t *testing.T) {
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:          "/media/movie.mkv",
		OutputDir:          "/tmp/out",
		SessionID:          "session-srt",
		TargetCodecVideo:   "h264",
		TargetCodecAudio:   "aac",
		SegmentDuration:    2,
		HWAccel:            "none",
		TargetResolution:   "1080p",
		SubtitleTrackIndex: 1,
		SubtitleBurnIn:     true,
		SubtitleCodec:      "subrip",
	})

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-vf scale=-2:1080,subtitles='/media/movie.mkv':si=1") {
		t.Fatalf("text burn-in should keep the libass subtitles -vf path: %s", joined)
	}
	if strings.Contains(joined, "-filter_complex") {
		t.Fatalf("text burn-in must not switch to filter_complex: %s", joined)
	}
	if !strings.Contains(joined, "-map 0:v:0") {
		t.Fatalf("text burn-in should keep the raw video stream mapping: %s", joined)
	}
}

func TestBuildFFmpegArgs_LegacyBurnInWithoutCodecKeepsTextPath(t *testing.T) {
	// Recipe cards / tokens minted before SubtitleCodec existed decode with an
	// empty codec; they must reconstruct the exact same (text) command line.
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:          "/media/movie.mkv",
		OutputDir:          "/tmp/out",
		SessionID:          "session-legacy",
		TargetCodecVideo:   "h264",
		TargetCodecAudio:   "aac",
		SegmentDuration:    2,
		HWAccel:            "none",
		SubtitleTrackIndex: 0,
		SubtitleBurnIn:     true,
	})

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "subtitles='/media/movie.mkv':si=0") {
		t.Fatalf("legacy burn-in without codec should keep the subtitles filter: %s", joined)
	}
	if strings.Contains(joined, "-filter_complex") {
		t.Fatalf("legacy burn-in without codec must not use filter_complex: %s", joined)
	}
}

func TestBuildFFmpegArgs_BitmapBurnInWithCopyVideoIsInert(t *testing.T) {
	// The API layer forces an encode before starting a burn-in transcode; if a
	// copy recipe slips through anyway the builder must stay a valid copy
	// command (no filter graph, raw stream mapping) rather than emit filters
	// against an unencoded stream.
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:          "/media/movie.mkv",
		OutputDir:          "/tmp/out",
		SessionID:          "session-copy-burnin",
		TargetCodecVideo:   "copy",
		TargetCodecAudio:   "aac",
		SegmentDuration:    2,
		SubtitleTrackIndex: 0,
		SubtitleBurnIn:     true,
		SubtitleCodec:      "hdmv_pgs_subtitle",
	})

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-c:v copy") {
		t.Fatalf("copy recipe should stay codec copy: %s", joined)
	}
	// Note: "-filter_complex_threads" is a legitimate copy-mode arg; only the
	// filter graph option itself must be absent.
	if strings.Contains(joined, "-filter_complex ") || strings.Contains(joined, "overlay") {
		t.Fatalf("copy recipe must not emit a filter graph: %s", joined)
	}
	if !strings.Contains(joined, "-map 0:v:0") {
		t.Fatalf("copy recipe should map the raw video stream: %s", joined)
	}
}

func TestResolveEffectiveTranscodeHWAccel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts TranscodeOpts
		want string
	}{
		{
			name: "hardware video transcode",
			opts: TranscodeOpts{HWAccel: "qsv", SourceVideoCodec: "h264", TargetCodecVideo: "h264"},
			want: "qsv",
		},
		{
			name: "copy video does not use hardware encode",
			opts: TranscodeOpts{HWAccel: "qsv", SourceVideoCodec: "h264", TargetCodecVideo: "copy"},
			want: "none",
		},
		{
			name: "mpeg4 part 2 falls back to software",
			opts: TranscodeOpts{HWAccel: "vaapi", SourceVideoCodec: "mpeg4", TargetCodecVideo: "h264"},
			want: "none",
		},
		{
			name: "nvenc passthrough",
			opts: TranscodeOpts{HWAccel: "nvenc", SourceVideoCodec: "h264", TargetCodecVideo: "h264"},
			want: "nvenc",
		},
		{
			name: "qsv keeps hardware encode with software decode",
			opts: TranscodeOpts{HWAccel: "qsv", SourceVideoCodec: "h264", SoftwareVideoDecode: true, TargetCodecVideo: "h264"},
			want: "qsv",
		},
		{
			name: "unvalidated nvenc upload falls back to software encode",
			opts: TranscodeOpts{HWAccel: "nvenc", SourceVideoCodec: "h264", SoftwareVideoDecode: true, TargetCodecVideo: "h264"},
			want: "none",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := resolveEffectiveTranscodeHWAccel(tt.opts); got != tt.want {
				t.Fatalf("resolveEffectiveTranscodeHWAccel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildFFmpegArgs_NVENCH264UsesCudaPipeline(t *testing.T) {
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:         "/media/movie.mkv",
		OutputDir:         "/tmp/out",
		SessionID:         "session-nvenc",
		SourceVideoCodec:  "h264",
		TargetCodecVideo:  "h264",
		TargetCodecAudio:  "aac",
		SegmentDuration:   2,
		HWAccel:           "nvenc",
		TargetResolution:  "720p",
		TargetBitrateKbps: 2000,
	})

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-hwaccel cuda") {
		t.Fatalf("nvenc args should enable cuda hwaccel: %s", joined)
	}
	if !strings.Contains(joined, "-c:v h264_nvenc") {
		t.Fatalf("nvenc args should use h264_nvenc encoder: %s", joined)
	}
	if !strings.Contains(joined, "-vf scale_cuda=w=-2:h=720:format=nv12") {
		t.Fatalf("nvenc args should use scale_cuda, not software scale: %s", joined)
	}
	if strings.Contains(joined, "-vf scale=-2:720") {
		t.Fatalf("nvenc args must not use software scale on cuda frames: %s", joined)
	}
	if !strings.Contains(joined, "-b:v 2000k -maxrate 2000k -bufsize 4000k") {
		t.Fatalf("nvenc args should include bitrate cap controls: %s", joined)
	}
}

func TestBuildFFmpegArgs_VAAPIScalingUsesHardwareFilter(t *testing.T) {
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:         "/media/movie.mkv",
		OutputDir:         "/tmp/out",
		SessionID:         "session-vaapi",
		SourceVideoCodec:  "h264",
		TargetCodecVideo:  "h264",
		TargetCodecAudio:  "aac",
		SegmentDuration:   2,
		HWAccel:           "vaapi",
		TargetResolution:  "720p",
		TargetBitrateKbps: 2000,
	})

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-hwaccel vaapi") {
		t.Fatalf("vaapi args should enable vaapi hwaccel: %s", joined)
	}
	if !strings.Contains(joined, "-vf scale_vaapi=w=-2:h=720:format=nv12") {
		t.Fatalf("vaapi args should use scale_vaapi, not software scale: %s", joined)
	}
	if strings.Contains(joined, "-vf scale=-2:720") {
		t.Fatalf("vaapi args must not use software scale on hardware frames: %s", joined)
	}
}

func TestBuildFFmpegArgs_EncodedTranscodePreservesExistingTimestampPolicy(t *testing.T) {
	args := buildFFmpegArgs(TranscodeOpts{
		InputPath:        "/media/movie.mkv",
		OutputDir:        "/tmp/out",
		SessionID:        "session-encoded",
		SeekSeconds:      2780.63,
		TargetCodecVideo: "h264",
		TargetCodecAudio: "aac",
		SegmentDuration:  2,
	})

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-copyts") {
		t.Fatalf("encoded args should preserve original timestamps: %s", joined)
	}
	if !strings.Contains(joined, "-avoid_negative_ts disabled") {
		t.Fatalf("encoded args should keep avoid_negative_ts disabled: %s", joined)
	}
}

// TranscodesAudio must agree with appendAudioArgs: only an explicit "copy"
// passes audio through; an empty codec runs ffmpeg's AAC default.
func TestTranscodesAudioMatchesFFmpegDefault(t *testing.T) {
	cases := []struct {
		codec string
		want  bool
	}{
		{"copy", false},
		{"COPY", false},
		{"", true},
		{"aac", true},
		{"opus", true},
	}
	for _, tc := range cases {
		if got := TranscodesAudio(tc.codec); got != tc.want {
			t.Errorf("TranscodesAudio(%q) = %v, want %v", tc.codec, got, tc.want)
		}
		args := appendAudioArgs(nil, TranscodeOpts{TargetCodecAudio: tc.codec})
		copied := strings.Contains(strings.Join(args, " "), "-c:a copy")
		if copied != !tc.want {
			t.Errorf("appendAudioArgs(%q) copy=%v disagrees with TranscodesAudio=%v", tc.codec, copied, tc.want)
		}
	}
}
