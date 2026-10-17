package workerwire

import (
	"time"

	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

// SessionReport binds runtime status to authenticated execution authority.
type SessionReport struct {
	WorkID     string                   `json:"work_id"`
	Generation uint64                   `json:"generation"`
	Info       nodesessions.SessionInfo `json:"info"`
}

type Report struct {
	BootID       string             `json:"boot_id"`
	Sequence     uint64             `json:"sequence"`
	Sessions     []SessionReport    `json:"sessions"`
	Observations *ObservationReport `json:"observations,omitempty"`
}

// ObservationScope binds an observed canonical session to the work that owns it.
type ObservationScope struct {
	WorkID     string `json:"work_id"`
	Generation uint64 `json:"generation"`
	SessionID  string `json:"session_id"`
}

type ObservationReport struct {
	Snapshot    streamtelemetry.Snapshot `json:"snapshot"`
	Scopes      []ObservationScope       `json:"scopes"`
	Logs        []FFmpegObservation      `json:"logs"`
	DroppedLogs uint64                   `json:"dropped_logs"`
}

// FFmpegObservation deliberately excludes paths, process arguments and raw errors.
type FFmpegObservation struct {
	WorkID             string    `json:"work_id"`
	Generation         uint64    `json:"generation"`
	At                 time.Time `json:"at"`
	Event              bool      `json:"event"`
	Message            string    `json:"message"`
	ExecutionMode      string    `json:"execution_mode,omitempty"`
	TargetVideoCodec   string    `json:"target_video_codec,omitempty"`
	TargetAudioCodec   string    `json:"target_audio_codec,omitempty"`
	HWAccel            string    `json:"hw_accel,omitempty"`
	SeekSeconds        float64   `json:"seek_seconds"`
	StartSegmentNumber int       `json:"start_segment_number"`
	RestartCount       int       `json:"restart_count"`
	LineIndex          int       `json:"line_index"`
	DroppedLines       int       `json:"dropped_lines"`
}
