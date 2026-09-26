package activitylog

import "time"

// LogEntry represents a single activity log row.
type LogEntry struct {
	Timestamp          time.Time `json:"timestamp"`
	ClientIP           string    `json:"client_ip"`
	UserID             *int      `json:"user_id,omitempty"`
	ImpersonatorUserID *int      `json:"impersonator_user_id,omitempty"`
	SessionID          string    `json:"session_id,omitempty"`
	PlaybackSessionID  string    `json:"playback_session_id,omitempty"`
	RequestID          string    `json:"request_id,omitempty"`
	NodeID             string    `json:"node_id,omitempty"`
	Method             string    `json:"method"`
	Path               string    `json:"path"`
	PathPattern        string    `json:"path_pattern,omitempty"`
	StatusCode         int       `json:"status_code"`
	UserAgent          string    `json:"user_agent"`
	DurationMs         int       `json:"duration_ms"`
}

// Writer buffers log entries for asynchronous persistence.
type Writer interface {
	// Write enqueues a log entry on the request goroutine. It must not
	// block or log.
	Write(entry LogEntry)

	// Close flushes remaining entries and releases resources.
	Close() error
}
