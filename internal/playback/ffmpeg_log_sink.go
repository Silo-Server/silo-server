package playback

import (
	"context"
	"log/slog"
	"strings"
)

const (
	ffmpegComponent        = "ffmpeg"
	ffmpegEventKey         = "ffmpeg_event"
	ffmpegLineKey          = "ffmpeg_line"
	ffmpegDroppedLinesKey  = "dropped_lines"
	ffmpegLineIndexKey     = "line_index"
	ffmpegExecutionModeKey = "execution_mode"
)

// FFmpegLogSink persists ffmpeg stderr and lifecycle events for a transcode
// session without coupling playback to a specific logging backend.
type FFmpegLogSink interface {
	WriteLine(ctx context.Context, sessionID string, attrs FFmpegLogAttrs, line string)
	WriteEvent(ctx context.Context, sessionID string, attrs FFmpegLogAttrs, message string)
}

// FFmpegLogAttrs captures stable context for ffmpeg logs.
type FFmpegLogAttrs struct {
	NodeType           string
	ExecutionMode      string
	InputPath          string
	OutputDir          string
	TargetResolution   string
	TargetVideoCodec   string
	TargetAudioCodec   string
	HWAccel            string
	SeekSeconds        float64
	StartSegmentNumber int
	RestartCount       int
	DroppedLines       int
	LineIndex          int
	ExitError          string
	ThrottlePaused     *bool
	ThrottleGapSeconds int
	Diagnostics        *FFmpegDiagnostics
}

// SlogFFmpegLogSink emits ffmpeg logs through slog so the existing opslog
// handler captures them into operational_logs.
type SlogFFmpegLogSink struct {
	logger *slog.Logger
	nodeID string
}

func NewSlogFFmpegLogSink(logger *slog.Logger, nodeID string) *SlogFFmpegLogSink {
	if logger == nil {
		logger = slog.Default()
	}
	return &SlogFFmpegLogSink{logger: logger, nodeID: strings.TrimSpace(nodeID)}
}

func (s *SlogFFmpegLogSink) WriteLine(ctx context.Context, sessionID string, attrs FFmpegLogAttrs, line string) {
	if s == nil || s.logger == nil {
		return
	}
	args := s.baseArgs(sessionID, attrs)
	args = append(args, ffmpegLineKey, line)
	if attrs.LineIndex > 0 {
		args = append(args, ffmpegLineIndexKey, attrs.LineIndex)
	}
	if attrs.DroppedLines > 0 {
		args = append(args, ffmpegDroppedLinesKey, attrs.DroppedLines)
	}
	s.logger.InfoContext(ctx, "ffmpeg stderr", args...)
}

func (s *SlogFFmpegLogSink) WriteEvent(ctx context.Context, sessionID string, attrs FFmpegLogAttrs, message string) {
	if s == nil || s.logger == nil {
		return
	}
	args := s.baseArgs(sessionID, attrs)
	args = append(args, ffmpegEventKey, message)
	if attrs.ExitError != "" {
		args = append(args, "exit_error", attrs.ExitError)
	}
	if attrs.DroppedLines > 0 {
		args = append(args, ffmpegDroppedLinesKey, attrs.DroppedLines)
	}
	if attrs.Diagnostics != nil {
		args = appendFFmpegDiagnosticArgs(args, *attrs.Diagnostics)
	}
	s.logger.InfoContext(ctx, message, args...)
}

func (s *SlogFFmpegLogSink) baseArgs(sessionID string, attrs FFmpegLogAttrs) []any {
	args := []any{
		"component", ffmpegComponent,
		"playback_session_id", sessionID,
		"node_id", s.nodeID,
		"node_type", strings.TrimSpace(attrs.NodeType),
		ffmpegExecutionModeKey, strings.TrimSpace(attrs.ExecutionMode),
		"restart_count", attrs.RestartCount,
		"target_resolution", strings.TrimSpace(attrs.TargetResolution),
		"target_video_codec", strings.TrimSpace(attrs.TargetVideoCodec),
		"target_audio_codec", strings.TrimSpace(attrs.TargetAudioCodec),
		"hw_accel", strings.TrimSpace(attrs.HWAccel),
		"seek_seconds", attrs.SeekSeconds,
		"start_segment_number", attrs.StartSegmentNumber,
	}
	if v := strings.TrimSpace(attrs.InputPath); v != "" {
		args = append(args, "input_path", v)
	}
	if v := strings.TrimSpace(attrs.OutputDir); v != "" {
		args = append(args, "output_dir", v)
	}
	if attrs.ThrottlePaused != nil {
		args = append(args,
			"throttle_paused", *attrs.ThrottlePaused,
			"throttle_gap_seconds", attrs.ThrottleGapSeconds,
		)
	}
	return args
}

// appendFFmpegDiagnosticArgs expands the event-only snapshot into stable slog
// keys consumed by the operational log API and Activity debug panel.
func appendFFmpegDiagnosticArgs(args []any, diagnostics FFmpegDiagnostics) []any {
	args = append(args,
		"ffmpeg_path", diagnostics.FFmpegPath,
		"ffmpeg_args", diagnostics.FFmpegArgs,
		"transcode_pipeline", diagnostics.Pipeline,
		"video_encoder", diagnostics.VideoEncoder,
		"audio_encoder", diagnostics.AudioEncoder,
		"video_pixel_formats", diagnostics.VideoPixelFormats,
		"segment_type", diagnostics.SegmentType,
		"source_video_bit_depth", diagnostics.SourceVideoBitDepth,
		"source_audio_channels", diagnostics.SourceAudioChannels,
		"software_video_decode", diagnostics.SoftwareVideoDecode,
		"target_audio_channels", diagnostics.TargetAudioChannels,
		"target_audio_bitrate_kbps", diagnostics.TargetAudioBitrateKbps,
		"target_bitrate_kbps", diagnostics.TargetBitrateKbps,
		"audio_track_index", diagnostics.AudioTrackIndex,
		"subtitle_track_index", diagnostics.SubtitleTrackIndex,
		"subtitle_burn_in", diagnostics.SubtitleBurnIn,
		"segment_duration_seconds", diagnostics.SegmentDuration,
		"segment_retention_seconds", diagnostics.SegmentRetentionSeconds,
		"total_duration_seconds", diagnostics.TotalDuration,
		"fast_start", diagnostics.FastStart,
		"segment_generation", diagnostics.SegmentGeneration,
		"last_requested_segment", diagnostics.LastRequestedSegment,
		"last_completed_segment", diagnostics.LastCompletedSegment,
		"throttle_configured", diagnostics.ThrottleConfigured,
		"throttle_enabled", diagnostics.ThrottleEnabled,
		"throttle_threshold_seconds", diagnostics.ThrottleThresholdSeconds,
		"throttle_paused", diagnostics.ThrottlePaused,
	)
	if diagnostics.ToneMapPolicy != "" || diagnostics.ToneMapMode != "" ||
		diagnostics.ToneMapSourceKind != "" || diagnostics.ToneMapPreflightRequired {
		args = append(args, "tone_map_preflight_required", diagnostics.ToneMapPreflightRequired)
	}
	for _, field := range [...]struct {
		key   string
		value string
	}{
		{"video_filter", diagnostics.VideoFilter},
		{"source_video_codec", diagnostics.SourceVideoCodec},
		{"source_video_profile", diagnostics.SourceVideoProfile},
		{"source_video_resolution", diagnostics.SourceVideoResolution},
		{"hw_device", diagnostics.HWDevice},
		{"tone_map_policy", diagnostics.ToneMapPolicy},
		{"tone_map_mode", diagnostics.ToneMapMode},
		{"tone_map_source_kind", diagnostics.ToneMapSourceKind},
		{"tone_map_filter", diagnostics.ToneMapFilter},
		{"tone_map_recipe_version", diagnostics.ToneMapRecipeVersion},
		{"video_bitstream_filter", diagnostics.VideoBitstreamFilter},
		{"video_sample_entry", diagnostics.VideoSampleEntry},
		{"subtitle_codec", diagnostics.SubtitleCodec},
	} {
		if value := strings.TrimSpace(field.value); value != "" {
			args = append(args, field.key, value)
		}
	}
	return args
}
