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
	s.logger.InfoContext(ctx, "ffmpeg event", args...)
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
	return args
}
