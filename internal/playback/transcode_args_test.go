package playback

import (
	"strings"
	"testing"
)

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

func TestBuildFFmpegArgs_BitmapBurnInVAAPIRoundTripsThroughCPU(t *testing.T) {
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
	want := "-filter_complex [0:v:0]hwdownload,format=yuv420p[vmain];[vmain][0:s:1]overlay=eof_action=pass,scale=-2:720,format=nv12,hwupload[vout]"
	if !strings.Contains(joined, want) {
		t.Fatalf("vaapi bitmap burn-in should hwdownload → overlay → hwupload %q: %s", want, joined)
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
