package playback

import (
	"context"
	"testing"
)

type recordingFFmpegLogSink struct {
	sessionID string
	events    int
	message   string
}

func (sink *recordingFFmpegLogSink) WriteLine(_ context.Context, sessionID string, _ FFmpegLogAttrs, _ string) {
	sink.sessionID = sessionID
}

func (sink *recordingFFmpegLogSink) WriteEvent(_ context.Context, sessionID string, _ FFmpegLogAttrs, message string) {
	sink.sessionID = sessionID
	sink.events++
	sink.message = message
}

func TestFFmpegDiagnosticsDescribeSoftwareDecodeNVENCPipeline(t *testing.T) {
	opts := TranscodeOpts{
		FFmpegPath:            "/usr/bin/ffmpeg",
		InputPath:             "/media/anime.mkv",
		OutputDir:             t.TempDir(),
		SourceVideoCodec:      "h264",
		SourceVideoProfile:    "high 10",
		SourceVideoBitDepth:   10,
		SourceVideoResolution: "1080p",
		SoftwareVideoDecode:   true,
		TargetResolution:      "720p",
		TargetCodecVideo:      "h264",
		TargetCodecAudio:      "aac",
		TargetAudioChannels:   6,
		TargetBitrateKbps:     4_000,
		SegmentDuration:       2,
		HWAccel:               transcodeHWNVENC,
		HWDevice:              "0",
	}

	diagnostics := ffmpegDiagnosticsOf(opts, buildFFmpegArgs(opts))
	if diagnostics.Pipeline != pipelineSoftwareDecodeHardwareEncode {
		t.Fatalf("Pipeline = %q, want software decode with hardware encode", diagnostics.Pipeline)
	}
	if diagnostics.VideoEncoder != "h264_nvenc" {
		t.Fatalf("VideoEncoder = %q, want h264_nvenc", diagnostics.VideoEncoder)
	}
	if diagnostics.VideoFilter != "format=nv12,hwupload_cuda,scale_cuda=w=-2:h=720:format=nv12" {
		t.Fatalf("VideoFilter = %q", diagnostics.VideoFilter)
	}
	if len(diagnostics.VideoPixelFormats) != 1 || diagnostics.VideoPixelFormats[0] != "nv12" {
		t.Fatalf("VideoPixelFormats = %v, want [nv12]", diagnostics.VideoPixelFormats)
	}
	if diagnostics.SegmentType != "mpegts" {
		t.Fatalf("SegmentType = %q, want mpegts", diagnostics.SegmentType)
	}
	if len(diagnostics.FFmpegArgs) == 0 {
		t.Fatal("FFmpegArgs is empty")
	}
}

func TestFFmpegDiagnosticsReportsEffectiveGeneratedLimits(t *testing.T) {
	configured := TranscodeOpts{
		TargetAudioChannels:    6,
		TargetAudioBitrateKbps: 320,
		TargetBitrateKbps:      4_000,
	}
	withoutOverrides := ffmpegDiagnosticsOf(configured, nil)
	if withoutOverrides.TargetAudioChannels != 6 || withoutOverrides.TargetAudioBitrateKbps != 320 || withoutOverrides.TargetBitrateKbps != 4_000 {
		t.Fatalf("configured limits were not preserved: %+v", withoutOverrides)
	}

	generated := ffmpegDiagnosticsOf(configured, []string{
		"-ac", "2",
		"-b:a", "192k",
		"-maxrate", "6000k",
	})
	if generated.TargetAudioChannels != 2 || generated.TargetAudioBitrateKbps != 192 || generated.TargetBitrateKbps != 6_000 {
		t.Fatalf("effective generated limits = channels %d, audio %d kbps, video %d kbps",
			generated.TargetAudioChannels, generated.TargetAudioBitrateKbps, generated.TargetBitrateKbps)
	}
}

func TestFFmpegEventAttrsIncludeRuntimeResourcePolicy(t *testing.T) {
	session := &TranscodeSession{
		opts: TranscodeOpts{
			FFmpegPath:              "/usr/bin/ffmpeg",
			OutputDir:               t.TempDir(),
			TargetCodecVideo:        "h264",
			TargetCodecAudio:        "aac",
			SegmentDuration:         2,
			SegmentRetentionSeconds: 120,
			HWAccel:                 HWAccelNone,
		},
		segmentGeneration:    3,
		lastRequestedSegment: 42,
		lastCompletedSegment: 41,
		throttleConfigured:   true,
		throttleEnabled:      true,
		throttleThreshold:    120,
		throttlePaused:       true,
	}

	attrs := session.ffmpegEventAttrsLocked()
	if attrs.Diagnostics == nil {
		t.Fatal("Diagnostics is nil")
	}
	diagnostics := attrs.Diagnostics
	if diagnostics.SegmentGeneration != 3 || diagnostics.LastRequestedSegment != 42 || diagnostics.LastCompletedSegment != 41 {
		t.Fatalf("segment runtime = generation %d requested %d completed %d",
			diagnostics.SegmentGeneration, diagnostics.LastRequestedSegment, diagnostics.LastCompletedSegment)
	}
	if !diagnostics.ThrottleConfigured || !diagnostics.ThrottleEnabled || !diagnostics.ThrottlePaused || diagnostics.ThrottleThresholdSeconds != 120 {
		t.Fatalf("throttle diagnostics = %+v", diagnostics)
	}
	if diagnostics.SegmentRetentionSeconds != 120 {
		t.Fatalf("SegmentRetentionSeconds = %d, want 120", diagnostics.SegmentRetentionSeconds)
	}
}

func TestFFmpegDiagnosticLogArgsCarryStructuredRecipe(t *testing.T) {
	args := appendFFmpegDiagnosticArgs(nil, FFmpegDiagnostics{
		FFmpegPath:               "/usr/bin/ffmpeg",
		FFmpegArgs:               []string{"-i", "/media/anime.mkv"},
		Pipeline:                 pipelineSoftwareDecodeHardwareEncode,
		VideoPixelFormats:        []string{"nv12"},
		SoftwareVideoDecode:      true,
		SegmentRetentionSeconds:  120,
		ThrottleConfigured:       true,
		ThrottleEnabled:          true,
		ThrottleThresholdSeconds: 120,
	})
	attrs := make(map[string]any, len(args)/2)
	for index := 0; index < len(args); index += 2 {
		attrs[args[index].(string)] = args[index+1]
	}

	if attrs["transcode_pipeline"] != pipelineSoftwareDecodeHardwareEncode {
		t.Fatalf("transcode_pipeline = %v", attrs["transcode_pipeline"])
	}
	if attrs["software_video_decode"] != true || attrs["throttle_enabled"] != true {
		t.Fatalf("boolean diagnostics = decode %v throttle %v",
			attrs["software_video_decode"], attrs["throttle_enabled"])
	}
	if attrs["segment_retention_seconds"] != 120 || attrs["throttle_threshold_seconds"] != 120 {
		t.Fatalf("resource diagnostics = retention %v threshold %v",
			attrs["segment_retention_seconds"], attrs["throttle_threshold_seconds"])
	}
}

func TestFFmpegThrottleLogArgsStayLightweight(t *testing.T) {
	paused := true
	sink := NewSlogFFmpegLogSink(nil, "node-1")
	args := sink.baseArgs("session-1", FFmpegLogAttrs{
		ThrottlePaused:     &paused,
		ThrottleGapSeconds: 125,
	})
	attrs := make(map[string]any, len(args)/2)
	for index := 0; index < len(args); index += 2 {
		attrs[args[index].(string)] = args[index+1]
	}

	if attrs["throttle_paused"] != true || attrs["throttle_gap_seconds"] != 125 {
		t.Fatalf("throttle attrs = paused %v gap %v", attrs["throttle_paused"], attrs["throttle_gap_seconds"])
	}
	if _, found := attrs["ffmpeg_args"]; found {
		t.Fatal("lightweight throttle event duplicated FFmpeg arguments")
	}
}

func TestFFmpegLogsPreferPlaybackSessionIDOverTransportID(t *testing.T) {
	sink := &recordingFFmpegLogSink{}
	session := &TranscodeSession{opts: TranscodeOpts{
		SessionID:         "transport-1",
		PlaybackSessionID: "playback-1",
		FFmpegLogSink:     sink,
	}}

	session.logFFmpegEvent(context.Background(), "ffmpeg diagnostic snapshot", "")
	if sink.sessionID != "playback-1" {
		t.Fatalf("log session ID = %q, want playback-1", sink.sessionID)
	}
}
