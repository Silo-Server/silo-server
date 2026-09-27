package opslog

import "time"

// Entry represents a single operational log record.
type Entry struct {
	Timestamp         time.Time      `json:"timestamp"`
	Level             string         `json:"level"`
	Component         string         `json:"component"`
	Message           string         `json:"message"`
	RequestID         string         `json:"request_id,omitempty"`
	UserID            *int           `json:"user_id,omitempty"`
	SessionID         string         `json:"session_id,omitempty"`
	PlaybackSessionID string         `json:"playback_session_id,omitempty"`
	ClientIP          string         `json:"client_ip,omitempty"`
	NodeID            string         `json:"node_id,omitempty"`
	Attrs             map[string]any `json:"attrs,omitempty"`
}

// Writer buffers operational log entries for asynchronous persistence. Handler
// calls Write on the logging goroutine, so it must not block or log.
type Writer interface {
	Write(entry Entry)
	Close() error
}
